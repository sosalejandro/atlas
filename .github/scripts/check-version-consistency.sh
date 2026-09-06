#!/usr/bin/env bash
# Assert that every place atlas records its own version agrees.
#
# Usage: check-version-consistency.sh [--root DIR] [--tag vX.Y.Z]
#
# Issue #121 names this as a configuration-management defect rather than a
# cosmetic one: at the time of writing, internal/cli.Version said v0.13.0
# while .release-please-manifest.json said 0.8.0 and CHANGELOG.md stopped at
# 0.8.0. A user who reports a bug from "atlas v0.13.0" is reporting it
# against a version that was never released, and nobody can map that back to
# a commit.
#
# There is exactly one mechanism that is allowed to move these numbers:
# release-please. This script is how the repo finds out when something else
# has moved one of them.
#
# The three sources:
#   internal/cli/root.go        what the binary tells the user
#   .release-please-manifest.json  what the release tool believes is current
#   CHANGELOG.md                what a human reads to find out what changed
# and optionally --tag, which is what the git tag says during a release.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "$SCRIPT_DIR/lib.sh"

ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
TAG=""

while [ $# -gt 0 ]; do
	case "$1" in
	--root)
		ROOT="$2"
		shift 2
		;;
	--tag)
		TAG="$2"
		shift 2
		;;
	*)
		atlas_err "unknown argument: $1"
		atlas_err "usage: check-version-consistency.sh [--root DIR] [--tag vX.Y.Z]"
		exit 2
		;;
	esac
done

# strip_v normalises "v1.2.3" and "1.2.3" to the same token. The sources
# genuinely disagree on the prefix — release-please stores bare semver, the
# binary carries the v — and treating that as a mismatch would make the
# check cry wolf on the one thing that is fine.
strip_v() { printf '%s\n' "${1#v}"; }

root_go="$ROOT/internal/cli/root.go"
manifest="$ROOT/.release-please-manifest.json"
changelog="$ROOT/CHANGELOG.md"

for f in "$root_go" "$manifest" "$changelog"; do
	if [ ! -f "$f" ]; then
		atlas_err "missing: $f"
		exit 2
	fi
done

# The binary version. Anchored on the exact declaration shape the
# release-please stamping step writes, so a refactor that moves or renames
# the var fails loudly here instead of silently reading nothing.
binver="$(sed -n 's/^[[:space:]]*Version[[:space:]]*=[[:space:]]*"\([^"]*\)".*$/\1/p' "$root_go" | head -1)"
if [ -z "$binver" ]; then
	atlas_err "could not read Version from $root_go"
	exit 2
fi

manver="$(sed -n 's/.*"\.":[[:space:]]*"\([^"]*\)".*/\1/p' "$manifest" | head -1)"
if [ -z "$manver" ]; then
	atlas_err "could not read the '.' entry from $manifest"
	exit 2
fi

# release-please writes "## [X.Y.Z](compare-link) (date)" as the newest
# heading. Take the first one; everything below it is history.
logver="$(sed -n 's/^##[[:space:]]*\[\([0-9][^]]*\)\].*$/\1/p' "$changelog" | head -1)"
if [ -z "$logver" ]; then
	atlas_err "could not read the newest release heading from $changelog"
	exit 2
fi

b="$(strip_v "$binver")"
m="$(strip_v "$manver")"
l="$(strip_v "$logver")"

printf 'binary (internal/cli/root.go): %s\n' "$binver"
printf 'manifest (.release-please-manifest.json): %s\n' "$manver"
printf 'changelog (CHANGELOG.md): %s\n' "$logver"
[ -n "$TAG" ] && printf 'tag: %s\n' "$TAG"

rc=0
if [ "$b" != "$m" ]; then
	atlas_err "MISMATCH: binary says $binver, release-please manifest says $manver"
	rc=1
fi
if [ "$m" != "$l" ]; then
	atlas_err "MISMATCH: release-please manifest says $manver, changelog's newest entry is $logver"
	rc=1
fi
if [ -n "$TAG" ] && [ "$(strip_v "$TAG")" != "$b" ]; then
	atlas_err "MISMATCH: tag $TAG does not match the version in the tree ($binver)"
	rc=1
fi

if [ "$rc" -ne 0 ]; then
	atlas_err ""
	atlas_err "These four numbers must be produced by one mechanism (release-please)."
	atlas_err "A hand-edited version constant is how they drift apart; see docs/releasing.md."
	exit 1
fi

printf 'consistent: %s\n' "$binver"
