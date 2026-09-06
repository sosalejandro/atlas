#!/usr/bin/env bash
# Shared helpers for the atlas build/release scripts.
#
# Sourced, never executed. Everything here is a pure-ish function so
# scripts_test.sh can exercise it directly: the alternative — inlining this
# logic in workflow YAML — means the only way to test a change is to push a
# tag and watch.

# ATLAS_TARGETS is the shipped matrix. It is a single list, in one place,
# because "which platforms do we ship?" is answered by the release job, the
# CI cross-compile check and the install docs, and three copies drift.
#
# The matrix is only buildable from one host because the store is
# modernc.org/sqlite, which is pure Go. Any dependency that pulls in cgo
# collapses this list to "whatever the runner is".
ATLAS_TARGETS="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"

# ATLAS_MODULE is the import path the -X ldflags target.
ATLAS_MODULE="github.com/sosalejandro/atlas"

atlas_err() { printf '%s\n' "$*" >&2; }

# atlas_date_flavor reports which date(1) dialect is available: "gnu"
# (-d @EPOCH), "bsd" (-r EPOCH), or "none".
#
# It probes with a known epoch and compares against the known answer rather
# than checking whether a flag is accepted. That matters because BSD date's
# -d flag exists but means "daylight saving", so `date -u -d @N` on macOS can
# succeed and print the CURRENT time. A build date that silently follows the
# wall clock is exactly the defect that makes releases unreproducible, and it
# would look completely fine in the log.
#
# 1000000000 is 2001-09-09T01:46:40Z. The result is memoised in
# ATLAS_DATE_FLAVOR; unset that to re-probe (the tests do, after shimming
# date on PATH).
atlas_date_flavor() {
	if [ -n "${ATLAS_DATE_FLAVOR:-}" ]; then
		printf '%s\n' "$ATLAS_DATE_FLAVOR"
		return 0
	fi
	local probe="2001-09-09T01:46:40Z" fmt="+%Y-%m-%dT%H:%M:%SZ"
	if [ "$(date -u -d @1000000000 "$fmt" 2>/dev/null)" = "$probe" ]; then
		ATLAS_DATE_FLAVOR=gnu
	elif [ "$(date -u -r 1000000000 "$fmt" 2>/dev/null)" = "$probe" ]; then
		ATLAS_DATE_FLAVOR=bsd
	else
		ATLAS_DATE_FLAVOR=none
	fi
	printf '%s\n' "$ATLAS_DATE_FLAVOR"
}

# atlas_epoch_to_iso renders a Unix epoch as RFC 3339 UTC.
atlas_epoch_to_iso() {
	local epoch="${1:-}"
	case "$epoch" in
	'' | *[!0-9]*)
		atlas_err "not a decimal Unix epoch: '${epoch}'"
		return 1
		;;
	esac
	local fmt="+%Y-%m-%dT%H:%M:%SZ"
	case "$(atlas_date_flavor)" in
	gnu) date -u -d "@$epoch" "$fmt" ;;
	bsd) date -u -r "$epoch" "$fmt" ;;
	*)
		atlas_err "no date(1) able to format a Unix epoch was found on PATH"
		return 1
		;;
	esac
}

# atlas_source_date_epoch resolves the build timestamp.
#
# The default is HEAD's COMMITTER date, not the wall clock. That is the whole
# trick behind "two builds of one commit are byte-identical": the timestamp
# baked into the binary becomes a function of the commit, so two people who
# have never spoken produce the same bytes without agreeing on anything.
# SOURCE_DATE_EPOCH (the reproducible-builds.org convention) overrides it,
# which is what lets a release job pin the value once and hand it to six
# cross-compiles.
atlas_source_date_epoch() {
	local root="${1:-.}"
	if [ -n "${SOURCE_DATE_EPOCH:-}" ]; then
		case "$SOURCE_DATE_EPOCH" in
		'' | *[!0-9]*)
			atlas_err "SOURCE_DATE_EPOCH must be a decimal Unix epoch; got '${SOURCE_DATE_EPOCH}'"
			return 1
			;;
		esac
		printf '%s\n' "$SOURCE_DATE_EPOCH"
		return 0
	fi
	local epoch
	if ! epoch="$(git -C "$root" log -1 --format=%ct 2>/dev/null)" || [ -z "$epoch" ]; then
		atlas_err "no SOURCE_DATE_EPOCH set and '$root' has no git history to take one from"
		return 1
	fi
	printf '%s\n' "$epoch"
}

# atlas_resolve_version resolves the version string stamped into the binary.
#
# Order: explicit VERSION, then an exact tag on HEAD, then `git describe`
# against the last v-tag, then "dev".
#
# The describe fallback depends on the local tag set, so it is deterministic
# for a full clone and NOT deterministic for a shallow one. Release and
# verification paths therefore always pass VERSION explicitly — see
# docs/install.md, whose rebuild recipe does exactly that.
atlas_resolve_version() {
	local root="${1:-.}" v
	if [ -n "${VERSION:-}" ]; then
		printf '%s\n' "$VERSION"
		return 0
	fi
	if v="$(git -C "$root" describe --tags --exact-match HEAD 2>/dev/null)" && [ -n "$v" ]; then
		printf '%s\n' "$v"
		return 0
	fi
	if v="$(git -C "$root" describe --tags --match 'v[0-9]*' HEAD 2>/dev/null)" && [ -n "$v" ]; then
		printf '%s\n' "$v"
		return 0
	fi
	printf 'dev\n'
}

# atlas_resolve_commit returns the 7-char short SHA stamped into the binary,
# matching shortSHALen in internal/cli/buildinfo.go.
atlas_resolve_commit() {
	local root="${1:-.}" c
	if [ -n "${COMMIT:-}" ]; then
		printf '%s\n' "$COMMIT"
		return 0
	fi
	if c="$(git -C "$root" rev-parse --short=7 HEAD 2>/dev/null)" && [ -n "$c" ]; then
		printf '%s\n' "$c"
		return 0
	fi
	printf 'unknown\n'
}

# atlas_artifact_name is the released filename for one target. Version first
# so a directory of several releases sorts by version, and the .exe suffix
# only where Windows requires it to be executable.
atlas_artifact_name() {
	local version="$1" goos="$2" goarch="$3"
	local name="atlas_${version}_${goos}_${goarch}"
	if [ "$goos" = "windows" ]; then
		name="${name}.exe"
	fi
	printf '%s\n' "$name"
}

# atlas_compare_trees reports whether two directories hold byte-identical
# files under identical names. This is the assertion the reproducibility
# claim rests on, so it is deliberately strict: a file present in one tree
# and not the other is a failure, not a skip.
#
# Digests are compared rather than `cmp`, because the failure message then
# names the two digests, which is what someone debugging a broken
# reproducible build actually needs to see.
atlas_compare_trees() {
	local a="$1" b="$2" rc=0 f base da db
	for f in "$a"/*; do
		[ -e "$f" ] || continue
		base="$(basename "$f")"
		if [ ! -e "$b/$base" ]; then
			atlas_err "missing from second build: $base"
			rc=1
			continue
		fi
		da="$(atlas_sha256 "$f")"
		db="$(atlas_sha256 "$b/$base")"
		if [ "$da" != "$db" ]; then
			atlas_err "NOT reproducible: $base"
			atlas_err "  build 1: $da"
			atlas_err "  build 2: $db"
			rc=1
		else
			printf 'identical  %s  %s\n' "$da" "$base"
		fi
	done
	for f in "$b"/*; do
		[ -e "$f" ] || continue
		base="$(basename "$f")"
		if [ ! -e "$a/$base" ]; then
			atlas_err "missing from first build: $base"
			rc=1
		fi
	done
	return $rc
}

# atlas_sha256 prints the bare hex digest of one file. sha256sum (GNU) and
# shasum -a 256 (macOS) both exist but neither exists everywhere, so try
# both rather than making the release pipeline Linux-only.
atlas_sha256() {
	local f="$1"
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$f" | cut -d' ' -f1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$f" | cut -d' ' -f1
	else
		atlas_err "neither sha256sum nor shasum is available"
		return 1
	fi
}

# atlas_target_is_shipped guards against typos in a GOOS/GOARCH pair. A
# silent success on an unshipped target would produce an artifact nobody
# signs and nobody publishes.
atlas_target_is_shipped() {
	local want="$1/$2" t
	for t in $ATLAS_TARGETS; do
		[ "$t" = "$want" ] && return 0
	done
	return 1
}
