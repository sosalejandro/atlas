#!/usr/bin/env bash
# Build the same commit twice and compare digests.
#
# This is the only thing that turns "reproducible" from a claim into a fact.
# A pipeline that sets -trimpath and prints "reproducible build" has proved
# nothing; the flags can be right and the output still differ, because
# reproducibility is a property of the whole environment, not of two flags.
#
# The two builds are deliberately run under conditions that differ in every
# way we can cheaply arrange and that must NOT matter:
#
#   - different output directories
#   - different TMPDIR
#   - different GOMAXPROCS (the compiler is parallel; ordering must not leak)
#   - different working directory for the second build (a copy of the
#     checkout at another absolute path) — this is the -trimpath check, and
#     it is the one a same-directory rebuild silently passes
#
# Inputs: VERSION, SOURCE_DATE_EPOCH (as build.sh), TARGETS (default: host).
#
# Note SOURCE_DATE_EPOCH is resolved once and shared. That is not cheating:
# it defaults to HEAD's committer date, so an independent verifier who only
# has the commit derives the same value. Pinning it here just stops the two
# builds racing across a second boundary.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
# shellcheck source=lib.sh
. "$SCRIPT_DIR/lib.sh"

VERSION="$(atlas_resolve_version "$REPO_ROOT")"
COMMIT="$(atlas_resolve_commit "$REPO_ROOT")"
SOURCE_DATE_EPOCH="$(atlas_source_date_epoch "$REPO_ROOT")"
export VERSION COMMIT SOURCE_DATE_EPOCH

# Which targets to compare. The host target is the default because it is the
# fast check CI runs on every push; the release job passes the full matrix.
TARGETS="${TARGETS:-$(go env GOOS)/$(go env GOARCH)}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/build1" "$work/build2" "$work/tmp1" "$work/tmp2"

build_into() {
	local dist="$1" tmp="$2" procs="$3" root="$4" target
	for target in $TARGETS; do
		GOOS="${target%%/*}" GOARCH="${target##*/}" \
			DIST="$dist" TMPDIR="$tmp" GOMAXPROCS="$procs" \
			"$root/.github/scripts/build.sh" >/dev/null
	done
}

printf 'build 1: %s\n' "$REPO_ROOT" >&2
build_into "$work/build1" "$work/tmp1" 1 "$REPO_ROOT"

# Second build from a copy of the SAME sources at a different absolute path.
#
# The copy is of the working tree, not of HEAD: comparing HEAD against an
# edited working tree would compare two different programs, and a green
# result would mean nothing. .git is excluded on purpose — build.sh must
# not need a checkout once VERSION/COMMIT/SOURCE_DATE_EPOCH are supplied,
# or the "rebuild from the source tarball" recipe in docs/install.md is a
# lie, and this is where that would show up.
mirror="$work/mirror-checkout"
mkdir -p "$mirror"
tar -c -C "$REPO_ROOT" \
	--exclude=./.git \
	--exclude=./dist \
	--exclude=./node_modules \
	. | tar -x -C "$mirror"
if [ ! -f "$mirror/go.mod" ]; then
	atlas_err "second tree at $mirror is missing go.mod; the copy did not work"
	exit 1
fi

printf 'build 2: %s\n' "$mirror" >&2
build_into "$work/build2" "$work/tmp2" 4 "$mirror"

printf '\ncomparing digests\n' >&2
if atlas_compare_trees "$work/build1" "$work/build2"; then
	printf '\nreproducible: %s at %s, targets [%s]\n' "$VERSION" "$COMMIT" "$TARGETS"
	exit 0
fi

atlas_err ""
atlas_err "the same commit produced different bytes twice."
atlas_err "Something in the build depends on the environment rather than on the source."
atlas_err "Usual suspects: a timestamp not derived from SOURCE_DATE_EPOCH, an absolute"
atlas_err "path escaping -trimpath, an embedded file whose generator is not deterministic,"
atlas_err "or a Go env var (GOFLAGS/GOEXPERIMENT/GOAMD64) leaking in from the shell."
exit 1
