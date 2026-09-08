#!/usr/bin/env bash
# Tests for the release/CI scripts in this directory.
#
# Why this file exists: every step of the release and CI workflows delegates
# to a script here, and a workflow whose logic lives in YAML is untestable by
# construction — the only way to find out whether it works is to tag a
# release and watch. These tests are what makes the build pipeline something
# the repo can exercise on a laptop.
#
# Run:   bash .github/scripts/scripts_test.sh
#        make test-scripts
#
# The cross-compile matrix case builds six binaries and is gated behind
# ATLAS_SCRIPT_TESTS_SLOW=1 so the default run stays quick; CI sets it.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# ---------------------------------------------------------------------------
# Minimal test harness. Deliberately tiny: a dependency on bats or shunit2
# would mean the build pipeline's own tests cannot run in the same places the
# build runs.
# ---------------------------------------------------------------------------

TESTS_RUN=0
TESTS_FAILED=0
CURRENT_TEST=""

it() {
	CURRENT_TEST="$1"
	TESTS_RUN=$((TESTS_RUN + 1))
}

fail() {
	TESTS_FAILED=$((TESTS_FAILED + 1))
	printf 'FAIL  %s\n      %s\n' "$CURRENT_TEST" "$1" >&2
}

pass() { printf 'ok    %s\n' "$CURRENT_TEST"; }

assert_eq() {
	if [ "$1" = "$2" ]; then
		pass
	else
		fail "got [$1] want [$2]"
	fi
}

assert_contains() {
	case "$1" in
	*"$2"*) pass ;;
	*) fail "output does not contain [$2]; got: $1" ;;
	esac
}

assert_ok() {
	if [ "$1" -eq 0 ]; then pass; else fail "expected exit 0, got $1"; fi
}

assert_not_ok() {
	if [ "$1" -ne 0 ]; then pass; else fail "expected non-zero exit, got 0"; fi
}

# skip records a test that could not run here and says why, instead of
# silently reporting green. A test that quietly does nothing is worse than no
# test at all.
skip() {
	printf 'skip  %s\n      %s\n' "$CURRENT_TEST" "$1"
}

# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# git_fixture_repo makes a throwaway repo with one commit at a known
# committer date, so the epoch/version helpers can be tested against a value
# this file controls rather than against whatever the real repo happens to
# have at HEAD.
FIXTURE_EPOCH=1000000000
git_fixture_repo() {
	local dir="$1"
	mkdir -p "$dir"
	git -C "$dir" init -q -b main
	git -C "$dir" config user.email test@example.com
	git -C "$dir" config user.name "Test"
	echo hello >"$dir/file.txt"
	git -C "$dir" add file.txt
	GIT_AUTHOR_DATE="$FIXTURE_EPOCH +0000" GIT_COMMITTER_DATE="$FIXTURE_EPOCH +0000" \
		git -C "$dir" commit -q -m "seed"
}

# shellcheck source=/dev/null
. "$SCRIPT_DIR/lib.sh" 2>/dev/null || {
	printf 'FATAL: .github/scripts/lib.sh not found or not sourceable\n' >&2
	exit 1
}

# ---------------------------------------------------------------------------
# lib.sh: date handling
#
# The epoch -> ISO conversion is the single most portability-sensitive line
# in the pipeline: GNU date spells it `-d @N`, BSD/macOS date spells it
# `-r N`, and BSD's `-d` flag means something else entirely (daylight
# saving), so a naive `date -d` on macOS can silently print the CURRENT time.
# That would produce a build date that changes every run — precisely the
# thing that makes builds unreproducible — while looking perfectly fine.
# Hence a probe against a known answer rather than a flag-support check.
# ---------------------------------------------------------------------------

it "atlas_date_flavor detects a usable date(1)"
flavor="$(atlas_date_flavor)"
if [ "$flavor" = "gnu" ] || [ "$flavor" = "bsd" ]; then
	pass
else
	fail "no usable date(1) found (flavor=$flavor)"
fi

it "atlas_epoch_to_iso converts the probe epoch"
assert_eq "$(atlas_epoch_to_iso 1000000000)" "2001-09-09T01:46:40Z"

it "atlas_epoch_to_iso converts epoch 0"
assert_eq "$(atlas_epoch_to_iso 0)" "1970-01-01T00:00:00Z"

it "atlas_epoch_to_iso rejects a non-decimal epoch"
atlas_epoch_to_iso "not-a-number" >/dev/null 2>&1
assert_not_ok $?

it "atlas_epoch_to_iso rejects an empty epoch"
atlas_epoch_to_iso "" >/dev/null 2>&1
assert_not_ok $?

# The BSD branch cannot be reached on a GNU host, so shim a date(1) that
# behaves like BSD's: rejects -d, accepts -r. Without this the fallback
# would be dead code that nobody discovers is broken until a macOS
# contributor tries to reproduce a release.
it "atlas_epoch_to_iso falls back to the BSD date spelling"
shimdir="$WORK/bsdshim"
mkdir -p "$shimdir"
cat >"$shimdir/date" <<'SHIM'
#!/usr/bin/env bash
# Stand-in for BSD date: -r <epoch> works, -d is rejected.
args=("$@")
for ((i = 0; i < ${#args[@]}; i++)); do
	if [ "${args[$i]}" = "-d" ]; then
		echo "date: illegal option -- d" >&2
		exit 1
	fi
	if [ "${args[$i]}" = "-r" ]; then
		epoch="${args[$((i + 1))]}"
		fmt="${args[$((${#args[@]} - 1))]}"
		exec /usr/bin/env -u PATH_SHIM /bin/date -u -d "@$epoch" "$fmt"
	fi
done
exec /bin/date "$@"
SHIM
chmod +x "$shimdir/date"
(
	PATH="$shimdir:$PATH"
	# Re-source so the flavor probe re-runs against the shimmed date.
	# shellcheck source=/dev/null
	. "$SCRIPT_DIR/lib.sh"
	unset ATLAS_DATE_FLAVOR
	[ "$(atlas_date_flavor)" = "bsd" ] || {
		echo "flavor probe did not pick bsd" >&2
		exit 1
	}
	[ "$(atlas_epoch_to_iso 1000000000)" = "2001-09-09T01:46:40Z" ] || {
		echo "bsd conversion wrong" >&2
		exit 1
	}
)
assert_ok $?

# ---------------------------------------------------------------------------
# lib.sh: SOURCE_DATE_EPOCH
#
# The build date must be a function of the commit, not of the wall clock.
# Defaulting it to HEAD's committer date is what makes "build the same
# commit twice, get the same bytes" true without the two builders having to
# agree on anything out of band.
# ---------------------------------------------------------------------------

it "atlas_source_date_epoch honours an explicit SOURCE_DATE_EPOCH"
out="$(SOURCE_DATE_EPOCH=1234567890 atlas_source_date_epoch "$REPO_ROOT")"
assert_eq "$out" "1234567890"

it "atlas_source_date_epoch defaults to HEAD's committer date"
fixture="$WORK/repo"
git_fixture_repo "$fixture"
out="$(unset SOURCE_DATE_EPOCH; atlas_source_date_epoch "$fixture")"
assert_eq "$out" "$FIXTURE_EPOCH"

it "atlas_source_date_epoch rejects a non-decimal SOURCE_DATE_EPOCH"
SOURCE_DATE_EPOCH="yesterday" atlas_source_date_epoch "$REPO_ROOT" >/dev/null 2>&1
assert_not_ok $?

# ---------------------------------------------------------------------------
# lib.sh: version + artifact naming
# ---------------------------------------------------------------------------

it "atlas_resolve_version prefers an explicit VERSION"
assert_eq "$(VERSION=v9.9.9 atlas_resolve_version "$REPO_ROOT")" "v9.9.9"

it "atlas_resolve_version uses an exact tag when HEAD is tagged"
tagged="$WORK/tagged"
git_fixture_repo "$tagged"
git -C "$tagged" tag v1.2.3
assert_eq "$(unset VERSION; atlas_resolve_version "$tagged")" "v1.2.3"

it "atlas_resolve_version describes an untagged commit against the last tag"
git -C "$tagged" commit -q --allow-empty -m "after the tag"
out="$(unset VERSION; atlas_resolve_version "$tagged")"
assert_contains "$out" "v1.2.3-1-g"

it "atlas_resolve_version falls back to dev with no tags at all"
assert_eq "$(unset VERSION; atlas_resolve_version "$fixture")" "dev"

it "atlas_artifact_name suffixes .exe on windows only"
assert_eq "$(atlas_artifact_name v1.0.0 windows amd64)" "atlas_v1.0.0_windows_amd64.exe"

it "atlas_artifact_name leaves unix targets unsuffixed"
assert_eq "$(atlas_artifact_name v1.0.0 darwin arm64)" "atlas_v1.0.0_darwin_arm64"

# ---------------------------------------------------------------------------
# lib.sh: tree comparison
#
# This is the assertion the whole reproducibility claim rests on, so it is
# tested for both verdicts. A comparison that can only say "same" proves
# nothing.
# ---------------------------------------------------------------------------

it "atlas_compare_trees accepts two byte-identical trees"
mkdir -p "$WORK/a" "$WORK/b"
printf 'aaa' >"$WORK/a/x"
printf 'aaa' >"$WORK/b/x"
atlas_compare_trees "$WORK/a" "$WORK/b" >/dev/null 2>&1
assert_ok $?

it "atlas_compare_trees rejects a one-byte difference"
printf 'aab' >"$WORK/b/x"
atlas_compare_trees "$WORK/a" "$WORK/b" >/dev/null 2>&1
assert_not_ok $?

it "atlas_compare_trees rejects a missing artifact"
printf 'aaa' >"$WORK/b/x"
printf 'zzz' >"$WORK/a/y"
atlas_compare_trees "$WORK/a" "$WORK/b" >/dev/null 2>&1
assert_not_ok $?
rm -f "$WORK/a/y"

# ---------------------------------------------------------------------------
# checksums.sh
# ---------------------------------------------------------------------------

it "checksums.sh emits sha256 lines keyed by basename"
sums="$WORK/sums"
mkdir -p "$sums"
printf 'one' >"$sums/atlas_v1_linux_amd64"
printf 'two' >"$sums/atlas_v1_darwin_arm64"
out="$(bash "$SCRIPT_DIR/checksums.sh" "$sums" 2>&1)"
rc=$?
if [ $rc -ne 0 ]; then
	fail "checksums.sh exited $rc: $out"
else
	assert_contains "$(cat "$sums/SHA256SUMS")" "atlas_v1_linux_amd64"
fi

it "checksums.sh output carries no directory components"
if grep -q '/' "$sums/SHA256SUMS"; then
	fail "SHA256SUMS contains a path separator; sha256sum -c would fail elsewhere"
else
	pass
fi

it "checksums.sh is byte-identical across runs regardless of readdir order"
first="$(cat "$sums/SHA256SUMS")"
rm -f "$sums/SHA256SUMS"
bash "$SCRIPT_DIR/checksums.sh" "$sums" >/dev/null 2>&1
assert_eq "$(cat "$sums/SHA256SUMS")" "$first"

it "checksums.sh never digests its own output file"
if grep -q 'SHA256SUMS' "$sums/SHA256SUMS"; then
	fail "SHA256SUMS lists itself; the file cannot contain its own digest"
else
	pass
fi

it "checksums.sh fails on an empty directory rather than writing an empty manifest"
mkdir -p "$WORK/emptydist"
bash "$SCRIPT_DIR/checksums.sh" "$WORK/emptydist" >/dev/null 2>&1
assert_not_ok $?

# ---------------------------------------------------------------------------
# check-version-consistency.sh
#
# Issue #121 calls out that the version in the binary, the release-please
# manifest and the changelog have drifted apart. A script that can only
# report agreement would be useless, so the disagreement verdict is tested
# against a fixture rather than against the live tree.
# ---------------------------------------------------------------------------

make_version_fixture() {
	local dir="$1" rootver="$2" manifestver="$3" changelogver="$4"
	mkdir -p "$dir/internal/cli"
	{
		printf 'package cli\n\nvar (\n'
		printf '\tVersion   = "%s"\n' "$rootver"
		printf '\tCommit    = "abc1234"\n'
		printf '\tBuildDate = "2026-01-01T00:00:00Z"\n'
		printf ')\n'
	} >"$dir/internal/cli/root.go"
	printf '{\n  ".": "%s"\n}\n' "$manifestver" >"$dir/.release-please-manifest.json"
	printf '# Changelog\n\n## [%s](https://example.com) (2026-01-01)\n' "$changelogver" >"$dir/CHANGELOG.md"
}

it "check-version-consistency.sh passes when all three sources agree"
ok_fix="$WORK/vok"
make_version_fixture "$ok_fix" "v1.2.3" "1.2.3" "1.2.3"
bash "$SCRIPT_DIR/check-version-consistency.sh" --root "$ok_fix" >/dev/null 2>&1
assert_ok $?

it "check-version-consistency.sh fails when the binary version leads the manifest"
bad_fix="$WORK/vbad"
make_version_fixture "$bad_fix" "v1.9.0" "1.2.3" "1.2.3"
out="$(bash "$SCRIPT_DIR/check-version-consistency.sh" --root "$bad_fix" 2>&1)"
rc=$?
if [ $rc -eq 0 ]; then
	fail "mismatched versions reported as consistent"
else
	assert_contains "$out" "1.9.0"
fi

it "check-version-consistency.sh fails when the changelog lags"
lag_fix="$WORK/vlag"
make_version_fixture "$lag_fix" "v1.2.3" "1.2.3" "1.1.0"
bash "$SCRIPT_DIR/check-version-consistency.sh" --root "$lag_fix" >/dev/null 2>&1
assert_not_ok $?

it "check-version-consistency.sh compares a supplied tag too"
bash "$SCRIPT_DIR/check-version-consistency.sh" --root "$ok_fix" --tag v1.2.3 >/dev/null 2>&1
assert_ok $?

it "check-version-consistency.sh rejects a tag that disagrees with the tree"
bash "$SCRIPT_DIR/check-version-consistency.sh" --root "$ok_fix" --tag v2.0.0 >/dev/null 2>&1
assert_not_ok $?

# ---------------------------------------------------------------------------
# Workflow YAML
#
# The workflows cannot be executed here, so the two properties that can be
# checked statically are checked statically: they parse, and every action
# they call is pinned to an immutable commit. A supply-chain tool that
# resolves `@v1` at run time hands whoever controls that tag the ability to
# change what runs in this repo's release job.
# ---------------------------------------------------------------------------

it "every workflow and action file is parseable YAML"
if ! command -v python3 >/dev/null 2>&1; then
	skip "python3 not available to parse YAML"
else
	out="$(python3 "$SCRIPT_DIR/yamlcheck.py" "$REPO_ROOT" 2>&1)"
	rc=$?
	if [ $rc -ne 0 ]; then fail "$out"; else pass; fi
fi

it "every third-party action is pinned to a 40-character commit SHA"
unpinned=""
while IFS= read -r line; do
	ref="${line#*uses:}"
	ref="$(printf '%s' "$ref" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]].*$//')"
	case "$ref" in
	./* | "") continue ;;
	esac
	sha="${ref#*@}"
	if ! printf '%s' "$sha" | grep -Eq '^[0-9a-f]{40}$'; then
		unpinned="$unpinned $ref"
	fi
done < <(grep -rhE '^[[:space:]]*(-[[:space:]]+)?uses:' \
	"$REPO_ROOT/.github/workflows" "$REPO_ROOT/.github/actions" 2>/dev/null)
if [ -n "$unpinned" ]; then
	fail "unpinned action reference(s):$unpinned"
else
	pass
fi

it "every pinned action carries a human-readable version comment"
missing=""
while IFS= read -r line; do
	case "$line" in
	*uses:*@*) ;;
	*) continue ;;
	esac
	case "$line" in
	*./*) continue ;;
	esac
	case "$line" in
	*"# v"*) ;;
	*) missing="$missing|$line" ;;
	esac
done < <(grep -rhE '^[[:space:]]*(-[[:space:]]+)?uses:' \
	"$REPO_ROOT/.github/workflows" "$REPO_ROOT/.github/actions" 2>/dev/null)
if [ -n "$missing" ]; then
	fail "pinned action with no '# vX.Y.Z' comment (nobody can tell what the SHA is):$missing"
else
	pass
fi

# GitHub does not start workflow runs from events raised with the default
# GITHUB_TOKEN, so the tag release-please pushes does NOT fire release.yml's
# `on: push: tags` trigger. Without an explicit hand-off no release is ever
# built and the whole pipeline is decoration. workflow_dispatch is one of
# the two documented exceptions to that rule, which is why the bridge uses
# it. This is a static check because the only dynamic one is "tag a release
# and see whether anything happens".
it "release-please hands the new tag to release.yml explicitly"
if grep -q 'gh workflow run release\.yml' "$REPO_ROOT/.github/workflows/release-please.yml"; then
	pass
else
	fail "release-please.yml never dispatches release.yml; a GITHUB_TOKEN tag push triggers nothing"
fi

it "release.yml accepts the dispatched tag as an input"
if grep -q 'workflow_dispatch:' "$REPO_ROOT/.github/workflows/release.yml" &&
	grep -qE '^ *tag:' "$REPO_ROOT/.github/workflows/release.yml"; then
	pass
else
	fail "release.yml has no workflow_dispatch 'tag' input for the bridge to target"
fi

it "the edge workflow publishes the fixed edge asset names"
if grep -qE '^ *run: \.github/scripts/edge-assets\.sh' "$REPO_ROOT/.github/workflows/edge.yml"; then
	pass
else
	fail "edge.yml never runs edge-assets.sh; it would publish describe-stamped names that every documented edge install 404s on"
fi

# docs/install.md's channels table promises edge the same provenance as
# stable, and the consumer action defaults verify-provenance to true on
# every channel. Both were false while edge.yml had no attestation step.
it "the edge workflow attests provenance, as the channels table promises"
if grep -qE '^ *uses: actions/attest-build-provenance@' "$REPO_ROOT/.github/workflows/edge.yml"; then
	pass
else
	fail "edge.yml has no attestation step, but edge is advertised as attested"
fi

# ---------------------------------------------------------------------------
# The consumer action's installer.
#
# The download itself needs the network and a published release, so what is
# tested here is the part that is wrong most often and silently: mapping
# GitHub's runner labels onto GOOS/GOARCH. runner.arch says X64 where Go
# says amd64 and runner.os says macOS where Go says darwin, so a plausible
# guess produces a 404 in somebody else's CI.
# ---------------------------------------------------------------------------

ACTION_INSTALL="$REPO_ROOT/.github/actions/atlas/install.sh"

it "the action's installer maps Linux/X64 to the linux amd64 asset"
assert_eq "$(bash "$ACTION_INSTALL" --print-asset-name --version v1.0.0 --os Linux --arch X64)" \
	"atlas_v1.0.0_linux_amd64"

it "the action's installer maps macOS/ARM64 to the darwin arm64 asset"
assert_eq "$(bash "$ACTION_INSTALL" --print-asset-name --version v1.0.0 --os macOS --arch ARM64)" \
	"atlas_v1.0.0_darwin_arm64"

it "the action's installer maps Windows/X64 to the .exe asset"
assert_eq "$(bash "$ACTION_INSTALL" --print-asset-name --version v1.0.0 --os Windows --arch X64)" \
	"atlas_v1.0.0_windows_amd64.exe"

it "the action's installer rejects an unknown runner OS"
bash "$ACTION_INSTALL" --print-asset-name --version v1.0.0 --os Solaris --arch X64 >/dev/null 2>&1
assert_not_ok $?

it "the action's installer rejects an unknown runner architecture"
bash "$ACTION_INSTALL" --print-asset-name --version v1.0.0 --os Linux --arch riscv64 >/dev/null 2>&1
assert_not_ok $?

# Pinning the action by commit SHA leaves github.action_ref holding a SHA,
# which names no release. Failing with an explanation beats a 404 on a URL
# built out of a commit hash.
it "the action's installer explains itself when handed a commit SHA as a version"
out="$(bash "$ACTION_INSTALL" --print-asset-name --version 0123456789abcdef0123456789abcdef01234567 \
	--os Linux --arch X64 2>&1)"
rc=$?
if [ $rc -eq 0 ]; then
	fail "a commit SHA was accepted as a release version"
else
	assert_contains "$out" "not a release version"
fi

it "the action's installer accepts the edge channel"
assert_eq "$(bash "$ACTION_INSTALL" --print-asset-name --version edge --os Linux --arch X64)" \
	"atlas_edge_linux_amd64"

# ---------------------------------------------------------------------------
# The edge channel's asset names.
#
# The installer asks for `atlas_edge_<goos>_<goarch>`, but an edge build is
# stamped with `git describe`, so build.sh writes
# `atlas_v0.13.0-7-gabc1234_linux_amd64`. Nothing reconciled the two, so the
# advertised edge install 404'd on every platform. edge-assets.sh closes
# that, and these cases check the two ends AGAINST EACH OTHER rather than
# each against a literal — a literal in both places is how they drifted
# apart in the first place.
# ---------------------------------------------------------------------------

EDGE_ASSETS="$SCRIPT_DIR/edge-assets.sh"
EDGE_DESCRIBE="v0.13.0-7-gabc1234"

edge_fixture() {
	local dir="$1" f
	rm -rf "$dir"
	mkdir -p "$dir"
	for f in linux_amd64 linux_arm64 darwin_arm64 windows_amd64.exe; do
		printf 'binary\n' >"$dir/atlas_${EDGE_DESCRIBE}_${f}"
	done
	printf '{}\n' >"$dir/atlas_${EDGE_DESCRIBE}_sbom.spdx.json"
}

it "edge-assets.sh produces exactly the asset name the installer downloads"
edgedist="$WORK/edge-dist"
edge_fixture "$edgedist"
if bash "$EDGE_ASSETS" --dist "$edgedist" --version "$EDGE_DESCRIBE" >/dev/null 2>&1; then
	want="$(bash "$ACTION_INSTALL" --print-asset-name --version edge --os Linux --arch X64)"
	if [ -f "$edgedist/$want" ]; then
		pass
	else
		fail "installer asks for [$want]; publish produced: $(ls "$edgedist" | tr '\n' ' ')"
	fi
else
	fail "edge-assets.sh failed on a normal edge dist"
fi

it "edge-assets.sh renames the windows asset, .exe suffix intact"
want="$(bash "$ACTION_INSTALL" --print-asset-name --version edge --os Windows --arch X64)"
if [ -f "$edgedist/$want" ]; then pass; else fail "missing [$want]"; fi

it "edge-assets.sh renames the SBOM alongside the binaries"
if [ -f "$edgedist/atlas_edge_sbom.spdx.json" ]; then
	pass
else
	fail "SBOM keeps a describe-stamped name: $(ls "$edgedist" | tr '\n' ' ')"
fi

it "edge-assets.sh leaves nothing behind under the describe-stamped name"
leftover="$(find "$edgedist" -name "atlas_${EDGE_DESCRIBE}_*" | wc -l | tr -d ' ')"
assert_eq "$leftover" "0"

# A publish that produced no edge-named asset would upload a release nobody
# can install from and report success. Empty must be an error, not a skip.
it "edge-assets.sh fails rather than publish a release with no reachable names"
emptydist="$WORK/edge-empty"
mkdir -p "$emptydist"
printf 'x\n' >"$emptydist/atlas_v9.9.9_linux_amd64"
bash "$EDGE_ASSETS" --dist "$emptydist" --version "$EDGE_DESCRIBE" >/dev/null 2>&1
assert_not_ok $?

it "edge-assets.sh is a no-op when the assets already carry the edge names"
donedist="$WORK/edge-done"
mkdir -p "$donedist"
printf 'x\n' >"$donedist/atlas_edge_linux_amd64"
bash "$EDGE_ASSETS" --dist "$donedist" --version edge >/dev/null 2>&1
assert_ok $?

# ---------------------------------------------------------------------------
# The Homebrew formula.
#
# `brew install` is an acceptance criterion of issue #121. The formula is
# generated from the release's signed SHA256SUMS, so the digest a user's
# brew checks and the digest the release published cannot disagree — and a
# platform missing from the manifest has to be a failure here, because
# otherwise it is a failure in whoever's `brew install` runs first.
# ---------------------------------------------------------------------------

BREW_FORMULA="$SCRIPT_DIR/brew-formula.sh"

brew_fixture() {
	local dir="$1" version="$2" t
	rm -rf "$dir"
	mkdir -p "$dir"
	for t in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64; do
		printf '%s\n' "$t" >"$dir/atlas_${version}_${t}"
	done
	bash "$SCRIPT_DIR/checksums.sh" "$dir" >/dev/null 2>&1
}

it "brew-formula.sh copies each digest out of the release manifest"
brewdist="$WORK/brew-dist"
brew_fixture "$brewdist" v1.2.3
formula="$(bash "$BREW_FORMULA" --version v1.2.3 --dist "$brewdist" --repo acme/atlas 2>&1)"
rc=$?
if [ $rc -ne 0 ]; then
	fail "brew-formula.sh failed: $formula"
else
	want="$(grep ' atlas_v1.2.3_darwin_arm64$' "$brewdist/SHA256SUMS" | awk '{print $1}')"
	assert_contains "$formula" "sha256 \"$want\""
fi

it "brew-formula.sh names the bare semver as the Homebrew version"
assert_contains "$formula" 'version "1.2.3"'

it "brew-formula.sh points at the release download URLs"
assert_contains "$formula" 'https://github.com/acme/atlas/releases/download/v1.2.3/atlas_v1.2.3_linux_arm64'

# A formula that silently omits a platform installs nothing on that
# platform, and the person who finds out is a user, not the pipeline.
it "brew-formula.sh fails when a platform is missing from the manifest"
partial="$WORK/brew-partial"
brew_fixture "$partial" v1.2.3
grep -v ' atlas_v1.2.3_darwin_amd64$' "$partial/SHA256SUMS" >"$partial/SHA256SUMS.tmp"
mv "$partial/SHA256SUMS.tmp" "$partial/SHA256SUMS"
bash "$BREW_FORMULA" --version v1.2.3 --dist "$partial" >/dev/null 2>&1
assert_not_ok $?

it "brew-formula.sh refuses a version that is not a release tag"
bash "$BREW_FORMULA" --version edge --dist "$brewdist" >/dev/null 2>&1
assert_not_ok $?

it "brew-formula.sh refuses to invent digests with no manifest present"
nosums="$WORK/brew-nosums"
mkdir -p "$nosums"
bash "$BREW_FORMULA" --version v1.2.3 --dist "$nosums" >/dev/null 2>&1
assert_not_ok $?

# ---------------------------------------------------------------------------
# The stable channel's asset names, checked END TO END.
#
# Four independent places spell out what a released file is called:
#
#   lib.sh    atlas_artifact_name  — what the publisher writes into dist/
#   install.sh                     — what the consumer action downloads
#   brew-formula.sh                — what `brew install` fetches
#   checksums.sh                   — what the signature ends up covering
#
# Until these cases existed, each was checked against a LITERAL in this
# file. Literals in four places is not agreement, it is four independent
# chances to drift, and the drift is invisible here and fatal there: a
# rename in lib.sh keeps every literal assertion green and 404s in a
# consumer's CI, or installs nothing on whichever platform nobody tried.
# The edge channel already learned this the expensive way (see the
# edge-assets.sh block above); the stable channel had the same hole and no
# incident to show for it yet.
#
# So these compare the ends AGAINST EACH OTHER. There is no expected string
# below that both sides do not have to produce.
# ---------------------------------------------------------------------------

# runner_labels_for maps a GOOS/GOARCH pair onto the runner.os / runner.arch
# labels a workflow would present for it. This is the one table that has to
# be written down, because GitHub's vocabulary and Go's are genuinely
# different words for the same machine.
runner_labels_for() {
	case "$1/$2" in
	linux/amd64) echo "Linux X64" ;;
	linux/arm64) echo "Linux ARM64" ;;
	darwin/amd64) echo "macOS X64" ;;
	darwin/arm64) echo "macOS ARM64" ;;
	windows/amd64) echo "Windows X64" ;;
	windows/arm64) echo "Windows ARM64" ;;
	*) return 1 ;;
	esac
}

it "every shipped target's published name is the name the installer asks for"
NAME_VERSION="v1.2.3"
mismatched=""
for target in $ATLAS_TARGETS; do
	goos="${target%%/*}"
	goarch="${target##*/}"
	labels="$(runner_labels_for "$goos" "$goarch")" || {
		mismatched="$mismatched $target(no-runner-labels)"
		continue
	}
	published="$(atlas_artifact_name "$NAME_VERSION" "$goos" "$goarch")"
	requested="$(bash "$ACTION_INSTALL" --print-asset-name \
		--version "$NAME_VERSION" --os "${labels%% *}" --arch "${labels##* }")"
	[ "$published" = "$requested" ] ||
		mismatched="$mismatched ${target}[publish=$published install=$requested]"
done
if [ -n "$mismatched" ]; then
	fail "publisher and installer disagree on asset names:$mismatched"
else
	pass
fi

# Homebrew resolves exactly one url per machine, so a formula whose urls do
# not match the published names fails for a user on one platform and for
# nobody else — the hardest kind of break to notice. The four platforms
# below are the ones a formula can express; Homebrew has no Windows.
it "the Homebrew formula's download urls are the published asset names"
brewnames="$WORK/brew-names"
brew_fixture "$brewnames" "$NAME_VERSION"
formula_out="$(bash "$BREW_FORMULA" --version "$NAME_VERSION" --dist "$brewnames" \
	--repo acme/atlas 2>&1)"
if [ $? -ne 0 ]; then
	fail "brew-formula.sh failed: $formula_out"
else
	missing=""
	for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
		goos="${target%%/*}"
		goarch="${target##*/}"
		labels="$(runner_labels_for "$goos" "$goarch")"
		want="$(bash "$ACTION_INSTALL" --print-asset-name \
			--version "$NAME_VERSION" --os "${labels%% *}" --arch "${labels##* }")"
		case "$formula_out" in
		*"/releases/download/${NAME_VERSION}/${want}\""*) ;;
		*) missing="$missing $want" ;;
		esac
	done
	if [ -n "$missing" ]; then
		fail "formula has no url ending in the published name(s):$missing"
	else
		pass
	fi
fi

# The signature covers SHA256SUMS and nothing else, so an asset the manifest
# does not name is an asset nobody can verify. A url in the formula pointing
# at one would send a user to an unverifiable download while every check in
# this pipeline stayed green.
it "every url in the formula names a file the signed manifest covers"
uncovered=""
while IFS= read -r assetname; do
	[ -n "$assetname" ] || continue
	grep -q "[[:space:]]\*\?${assetname}\$" "$brewnames/SHA256SUMS" ||
		uncovered="$uncovered $assetname"
done < <(printf '%s\n' "$formula_out" | sed -n 's|.*/releases/download/[^/]*/\([^"]*\)".*|\1|p')
if [ -n "$uncovered" ]; then
	fail "formula links assets absent from SHA256SUMS:$uncovered"
else
	pass
fi

# ---------------------------------------------------------------------------
# build.sh — the real thing, against this checkout
# ---------------------------------------------------------------------------

if ! command -v go >/dev/null 2>&1; then
	it "build.sh produces a stamped binary"
	skip "go toolchain not on PATH"
else
	# The toolchain pin is a hard failure by default, so every build case
	# below sets ATLAS_SKIP_TOOLCHAIN_CHECK=1: these tests are about the
	# script's behaviour, and must pass on a contributor's machine whatever
	# Go they happen to have. The pin itself gets its own two cases.
	export ATLAS_SKIP_TOOLCHAIN_CHECK=1

	it "build.sh refuses to build against an unpinned toolchain"
	env -u ATLAS_SKIP_TOOLCHAIN_CHECK \
		VERSION=v9.8.7 ATLAS_TOOLCHAIN_PIN=0.0.1 DIST="$WORK/dist-pin" \
		bash "$SCRIPT_DIR/build.sh" >/dev/null 2>&1
	assert_not_ok $?

	it "build.sh downgrades the toolchain pin to a warning when asked"
	out="$(VERSION=v9.8.7 ATLAS_TOOLCHAIN_PIN=0.0.1 DIST="$WORK/dist-pin2" \
		bash "$SCRIPT_DIR/build.sh" 2>&1)"
	rc=$?
	if [ $rc -ne 0 ]; then
		fail "build.sh exited $rc with the override set: $out"
	else
		assert_contains "$out" "toolchain mismatch"
	fi

	it "build.sh produces a binary carrying the requested stamps"
	dist="$WORK/dist1"
	buildlog="$WORK/build1.log"
	if VERSION=v9.8.7 SOURCE_DATE_EPOCH=1000000000 DIST="$dist" \
		bash "$SCRIPT_DIR/build.sh" >"$buildlog" 2>&1; then
		bin="$dist/$(atlas_artifact_name v9.8.7 "$(go env GOOS)" "$(go env GOARCH)")"
		if [ ! -x "$bin" ]; then
			fail "expected binary at $bin; dist contains: $(ls "$dist" 2>&1)"
		else
			ver="$("$bin" version --json 2>&1)"
			case "$ver" in
			*'"version": "v9.8.7"'*) pass ;;
			*) fail "version stamp missing from: $ver" ;;
			esac
		fi
	else
		fail "build.sh failed: $(cat "$buildlog")"
	fi

	it "build.sh stamps the build date from SOURCE_DATE_EPOCH, not the wall clock"
	bin="$dist/$(atlas_artifact_name v9.8.7 "$(go env GOOS)" "$(go env GOARCH)")"
	if [ -x "$bin" ]; then
		assert_contains "$("$bin" version --json 2>&1)" '"build_date": "2001-09-09T01:46:40Z"'
	else
		fail "no binary to inspect"
	fi

	it "build.sh output is trimpath'd and cgo-free"
	if [ -x "$bin" ]; then
		settings="$(go version -m "$bin" 2>&1)"
		if printf '%s' "$settings" | grep -q -- '-trimpath=true' &&
			printf '%s' "$settings" | grep -q 'CGO_ENABLED=0'; then
			pass
		else
			fail "build settings missing -trimpath/CGO_ENABLED=0: $settings"
		fi
	else
		fail "no binary to inspect"
	fi

	it "build.sh refuses a target it does not ship"
	VERSION=v9.8.7 GOOS=plan9 GOARCH=amd64 DIST="$WORK/dist-bad" \
		bash "$SCRIPT_DIR/build.sh" >/dev/null 2>&1
	assert_not_ok $?

	# The headline acceptance criterion of issue #121. Not a proxy for it —
	# the actual two-builds-one-digest comparison, run here so the claim is
	# measured rather than asserted.
	it "verify-repro.sh proves two builds of this commit are byte-identical"
	reprolog="$WORK/repro.log"
	if VERSION=v9.8.7 bash "$SCRIPT_DIR/verify-repro.sh" >"$reprolog" 2>&1; then
		pass
	else
		fail "verify-repro.sh failed: $(cat "$reprolog")"
	fi

	if [ "${ATLAS_SCRIPT_TESTS_SLOW:-0}" = "1" ]; then
		it "build-matrix.sh builds every shipped target cgo-free"
		mlog="$WORK/matrix.log"
		if VERSION=v9.8.7 DIST="$WORK/distmatrix" \
			bash "$SCRIPT_DIR/build-matrix.sh" >"$mlog" 2>&1; then
			count="$(find "$WORK/distmatrix" -type f | wc -l | tr -d ' ')"
			assert_eq "$count" "6"
		else
			fail "build-matrix.sh failed: $(cat "$mlog")"
		fi
	else
		it "build-matrix.sh builds every shipped target cgo-free"
		skip "set ATLAS_SCRIPT_TESTS_SLOW=1 to run the six-target cross-compile"
	fi
fi


# --- secret-scan.sh --------------------------------------------------------
#
# The tests that matter here are the NEGATIVE ones. A secret scanner that
# reports clean is indistinguishable from one that is not running.
#
# That is not hypothetical: the first version of the planted-key test below
# asserted only "exit code is non-zero", and it PASSED on a CI runner where
# gitleaks was not installed -- the script exited 127 without looking at
# anything. Every assertion here therefore checks the SPECIFIC exit code from
# secret-scan.sh's documented contract (0 clean, 1 found, 2 bad usage,
# 127 cannot run).

if ! command -v gitleaks >/dev/null 2>&1; then
	it "secret-scan.sh behaviour"
	skip "gitleaks not installed; run: go install github.com/zricethezav/gitleaks/v8@v8.30.0"
else
	scan_status() {
		# Run the scanner and echo its exit code, without set -e aborting us.
		local mode="$1"
		set +e
		SCAN_MODE="$mode" bash "$SCRIPT_DIR/secret-scan.sh" >"$WORK/scan.log" 2>&1
		local rc=$?
		set -e
		echo "$rc"
	}

	it "secret-scan.sh reports the tree as committed is clean"
	assert_eq "$(scan_status tree)" "0"

	it "secret-scan.sh reports the whole history is clean"
	assert_eq "$(scan_status history)" "0"

	it "secret-scan.sh catches a planted key outside the detector's fixtures"
	# The allowlist in .gitleaks.toml is scoped by path AND rule. Widen it to a
	# blanket rule and this goes red. Asserting exactly 1 -- not "non-zero" --
	# is what makes it a test of detection rather than of the script running.
	planted="$REPO_ROOT/packages/store/zz_secretscan_probe.go"
	printf 'package store\n\nvar probe = "%s%s"\n' "ASIA" "Y34FZKBOKMUTVV7A" >"$planted"
	planted_rc="$(scan_status tree)"
	rm -f "$planted"
	assert_eq "$planted_rc" "1"

	it "secret-scan.sh refuses to run without the repository config"
	# Scanning with gitleaks' default rules would flag every fixture in
	# packages/redact, so a missing config is a hard stop rather than a scan
	# whose output nobody can act on.
	mv "$REPO_ROOT/.gitleaks.toml" "$WORK/gitleaks.toml.bak"
	noconf_rc="$(scan_status tree)"
	mv "$WORK/gitleaks.toml.bak" "$REPO_ROOT/.gitleaks.toml"
	assert_eq "$noconf_rc" "2"

	it "secret-scan.sh rejects an unknown scan mode"
	assert_eq "$(scan_status sideways)" "2"
fi

# --- two binaries ------------------------------------------------------------
#
# The release ships `atlas` and `atlas-serve`. The second exists because
# docs/security.md's "nothing leaves your machine" is enforced by an import
# check on cmd/atlas, and an HTTP API needs net/http -- so the API lives in a
# binary that listens and is separately proven never to dial out.
#
# Everything below guards the ways that split can go quietly wrong in the
# pipeline: a stamp written into a symbol the binary does not link, an
# artifact name collision, or a manifest that covers one of them.

it "ATLAS_COMMANDS names both shipped binaries"
assert_eq "$ATLAS_COMMANDS" "atlas atlas-serve"

it "atlas_artifact_name defaults to atlas and accepts a binary"
assert_eq "$(atlas_artifact_name v1.2.3 linux amd64)" "atlas_v1.2.3_linux_amd64"
assert_eq "$(atlas_artifact_name v1.2.3 linux amd64 atlas-serve)" "atlas-serve_v1.2.3_linux_amd64"
assert_eq "$(atlas_artifact_name v1.2.3 windows arm64 atlas-serve)" "atlas-serve_v1.2.3_windows_arm64.exe"

it "the two artifact names never collide"
# atlas_* must not match atlas-serve_*, or the SLSA subject glob, the brew
# formula and the smoke download would each silently take the wrong set.
# `atlas_*` matching six of twelve is exactly the bug this pins.
case "$(atlas_artifact_name v1.2.3 linux amd64 atlas-serve)" in
atlas_*) fail "atlas-serve's artifact name matches the atlas_* glob" ;;
*) pass ;;
esac

it "atlas_ldflags_pkg targets a different package per binary"
# atlas-serve does not import internal/cli, and -X against a symbol that is
# not linked in is silently a no-op -- so one hardcoded path would leave the
# second binary unversioned with nothing to notice.
assert_eq "$(atlas_ldflags_pkg atlas)" "github.com/sosalejandro/atlas/internal/cli"
assert_eq "$(atlas_ldflags_pkg atlas-serve)" "main"

it "atlas_ldflags_pkg refuses a command it does not know"
set +e
atlas_ldflags_pkg not-a-binary >/dev/null 2>&1
ldflags_rc=$?
set -e
if [ "$ldflags_rc" -ne 0 ]; then pass; else fail "atlas_ldflags_pkg accepted an unknown command"; fi

it "build.sh refuses a command that does not exist"
set +e
CMD=not-a-binary bash "$SCRIPT_DIR/build.sh" >/dev/null 2>&1
badcmd_rc=$?
set -e
if [ "$badcmd_rc" -ne 0 ]; then pass; else fail "build.sh built a command with no cmd/ directory"; fi

it "build.sh stamps atlas-serve so it is not an unversioned binary"
if CMD=atlas-serve VERSION=v9.9.9 ATLAS_SKIP_TOOLCHAIN_CHECK=1 DIST="$WORK/two" \
	bash "$SCRIPT_DIR/build.sh" >"$WORK/serve-build.log" 2>&1; then
	serve_bin="$(ls "$WORK"/two/atlas-serve_* 2>/dev/null | head -1)"
	if [ -z "$serve_bin" ]; then
		fail "build.sh produced no atlas-serve artifact"
	else
		serve_ver="$("$serve_bin" --version 2>&1)"
		case "$serve_ver" in
		*v9.9.9*) pass ;;
		*) fail "atlas-serve reports '$serve_ver', not the stamped v9.9.9" ;;
		esac
	fi
else
	fail "build.sh failed for atlas-serve: $(cat "$WORK/serve-build.log")"
fi

it "atlas-serve renders its contract without binding a port"
if [ -n "${serve_bin:-}" ] && [ -x "${serve_bin:-}" ]; then
	if "$serve_bin" --openapi 2>/dev/null | grep -q 'openapi:'; then
		pass
	else
		fail "atlas-serve --openapi emitted no OpenAPI document"
	fi
else
	skip "atlas-serve was not built"
fi

# --- smoke-release.sh ------------------------------------------------------
#
# The smoke script is the release pipeline's only proof that a published
# binary STARTS. Its own failure modes therefore matter as much as the
# binary's: a smoke test that exits 0 because it could not find the binary
# is the vacuous-success shape this file was rewritten to stop tolerating.

it "smoke-release.sh rejects a missing argument rather than passing"
set +e
bash "$SCRIPT_DIR/smoke-release.sh" >/dev/null 2>&1
smoke_noarg_rc=$?
set -e
assert_eq "$smoke_noarg_rc" "2"

it "smoke-release.sh rejects a binary that does not exist"
set +e
bash "$SCRIPT_DIR/smoke-release.sh" "$WORK/not-a-binary" >/dev/null 2>&1
smoke_missing_rc=$?
set -e
assert_eq "$smoke_missing_rc" "2"

it "smoke-release.sh fails on a binary that is not atlas"
# A file that exists and is executable but cannot index anything. Exiting 0
# here would mean the release gate passes on any file of the right name.
printf '#!/bin/sh\nexit 0\n' >"$WORK/fake-atlas"
chmod +x "$WORK/fake-atlas"
set +e
bash "$SCRIPT_DIR/smoke-release.sh" "$WORK/fake-atlas" >/dev/null 2>&1
smoke_fake_rc=$?
set -e
assert_eq "$smoke_fake_rc" "1"

it "smoke-release.sh passes against a binary built from this checkout"
if go build -o "$WORK/atlas-smoke" "$REPO_ROOT/cmd/atlas" 2>/dev/null; then
	set +e
	bash "$SCRIPT_DIR/smoke-release.sh" "$WORK/atlas-smoke" >"$WORK/smoke.log" 2>&1
	smoke_real_rc=$?
	set -e
	if [ "$smoke_real_rc" -ne 0 ]; then
		printf '%s\n' "$(cat "$WORK/smoke.log")" >&2
	fi
	assert_eq "$smoke_real_rc" "0"
else
	skip "could not build cmd/atlas"
fi

# ---------------------------------------------------------------------------

printf '\n%d test(s), %d failure(s)\n' "$TESTS_RUN" "$TESTS_FAILED"
[ "$TESTS_FAILED" -eq 0 ]
