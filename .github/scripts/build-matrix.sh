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

# Cross-compile the WHOLE MODULE per OS before building the artifacts.
#
# build.sh builds one command at a time, which is correct -- those are what
# ship. But each only reaches its own transitive imports, so a package outside
# both graphs
# can stop compiling on a target and every artifact still builds. That is not
# hypothetical: internal/adapters called syscall.Flock, which does not exist on
# Windows, and this matrix went green while `go vet ./...` on a Windows runner
# went red. A cross-compile check that passes on code the target cannot build
# is exactly the green-check-that-gates-nothing this pipeline exists to avoid.
#
# One representative arch per OS is enough: the failures this catches are
# OS-level API differences (syscall surface, path handling), not word size.
for goos in linux darwin windows; do
	echo "cross-compiling ./... for $goos"
	if ! GOOS="$goos" GOARCH=amd64 CGO_ENABLED=0 go build ./... >/dev/null; then
		echo "FAIL: the module does not compile for $goos" >&2
		exit 1
	fi
done

for cmd in $ATLAS_COMMANDS; do
	for target in $ATLAS_TARGETS; do
		goos="${target%%/*}"
		goarch="${target##*/}"
		GOOS="$goos" GOARCH="$goarch" DIST="$DIST" CMD="$cmd" \
			"$SCRIPT_DIR/build.sh" >/dev/null
	done
done

printf '\nverifying build settings of every artifact\n' >&2
failures=0
for cmd in $ATLAS_COMMANDS; do
for target in $ATLAS_TARGETS; do
	goos="${target%%/*}"
	goarch="${target##*/}"
	name="$(atlas_artifact_name "$VERSION" "$goos" "$goarch" "$cmd")"
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
done

if [ "$failures" -ne 0 ]; then
	atlas_err "$failures artifact(s) failed the cross-compile invariants"
	exit 1
fi

printf 'built %s of [%s] for: %s\n' "$VERSION" "$ATLAS_COMMANDS" "$ATLAS_TARGETS" >&2
