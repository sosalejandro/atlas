#!/usr/bin/env bash
# Build every shipped target from one host, and assert the property that
# makes that possible.
#
# atlas cross-compiles to six targets from a single Linux runner because the
# store is modernc.org/sqlite (pure Go) and CGO_ENABLED=0. That is not a
# happy accident, it is a design constraint, and it fails silently: the day
# someone adds a dependency that needs cgo, the build still succeeds on the
# host and quietly stops producing darwin and windows artifacts. So each
# artifact is inspected after the fact — `go version -m` reads the settings
# table the linker embedded, which is evidence about the binary rather than
# about the environment we thought we set up.
#
# Inputs: VERSION, SOURCE_DATE_EPOCH, DIST — as in build.sh.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
# shellcheck source=lib.sh
. "$SCRIPT_DIR/lib.sh"

DIST="${DIST:-$REPO_ROOT/dist}"

# Resolve the version and epoch ONCE and pass them down. Resolving per target
# would let the clock move between the first and last build, and six
# artifacts of one release carrying two different build dates is a
# provenance defect that no test would catch.
VERSION="$(atlas_resolve_version "$REPO_ROOT")"
COMMIT="$(atlas_resolve_commit "$REPO_ROOT")"
SOURCE_DATE_EPOCH="$(atlas_source_date_epoch "$REPO_ROOT")"
export VERSION COMMIT SOURCE_DATE_EPOCH

mkdir -p "$DIST"

for target in $ATLAS_TARGETS; do
	goos="${target%%/*}"
	goarch="${target##*/}"
	GOOS="$goos" GOARCH="$goarch" DIST="$DIST" "$SCRIPT_DIR/build.sh" >/dev/null
done

printf '\nverifying build settings of every artifact\n' >&2
failures=0
for target in $ATLAS_TARGETS; do
	goos="${target%%/*}"
	goarch="${target##*/}"
	name="$(atlas_artifact_name "$VERSION" "$goos" "$goarch")"
	path="$DIST/$name"
	if [ ! -f "$path" ]; then
		atlas_err "missing artifact: $name"
		failures=$((failures + 1))
		continue
	fi
	settings="$(go version -m "$path")"
	problem=""
	printf '%s' "$settings" | grep -q 'CGO_ENABLED=0' ||
		problem="$problem cgo-enabled"
	printf '%s' "$settings" | grep -q -- '-trimpath=true' ||
		problem="$problem not-trimpathed"
	printf '%s' "$settings" | grep -q "GOOS=$goos" ||
		problem="$problem wrong-goos"
	printf '%s' "$settings" | grep -q "GOARCH=$goarch" ||
		problem="$problem wrong-goarch"
	if [ -n "$problem" ]; then
		atlas_err "$name:$problem"
		atlas_err "$settings"
		failures=$((failures + 1))
	else
		printf '  ok  %s (%s/%s, cgo-free, trimpath)\n' "$name" "$goos" "$goarch" >&2
	fi
done

if [ "$failures" -ne 0 ]; then
	atlas_err "$failures artifact(s) failed the cross-compile invariants"
	exit 1
fi

printf 'built %s for: %s\n' "$VERSION" "$ATLAS_TARGETS" >&2
