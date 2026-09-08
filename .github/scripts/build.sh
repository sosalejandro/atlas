#!/usr/bin/env bash
# Build one atlas binary, reproducibly.
#
# This is THE build command. The release workflow calls it, CI's determinism
# check calls it twice, and docs/install.md tells third parties to call it —
# because a reproducibility claim is only meaningful if the person checking
# it runs the same command the publisher ran.
#
# Inputs (all optional; every one has a deterministic default):
#   VERSION            version string to stamp (default: git tag / describe)
#   COMMIT             short SHA to stamp     (default: HEAD)
#   SOURCE_DATE_EPOCH  build timestamp        (default: HEAD committer date)
#   GOOS / GOARCH      target                 (default: host)
#   CMD                which binary to build  (default: atlas; see
#                      ATLAS_COMMANDS in lib.sh for the full set)
#   DIST               output directory       (default: <repo>/dist)
#   ATLAS_SKIP_TOOLCHAIN_CHECK=1  downgrade the toolchain mismatch to a warning
#   ATLAS_TOOLCHAIN_PIN           override the pinned Go version (tests only)
#
# Output: $DIST/<cmd>_<version>_<goos>_<goarch>[.exe]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
# shellcheck source=lib.sh
. "$SCRIPT_DIR/lib.sh"

CMD="${CMD:-atlas}"
if [ ! -d "$REPO_ROOT/cmd/$CMD" ]; then
	atlas_err "no such command: cmd/$CMD (ATLAS_COMMANDS is: $ATLAS_COMMANDS)"
	exit 1
fi

GOOS="${GOOS:-$(go env GOOS)}"
GOARCH="${GOARCH:-$(go env GOARCH)}"
DIST="${DIST:-$REPO_ROOT/dist}"

if ! atlas_target_is_shipped "$GOOS" "$GOARCH"; then
	atlas_err "refusing to build unshipped target ${GOOS}/${GOARCH}"
	atlas_err "shipped targets: $ATLAS_TARGETS"
	exit 1
fi

version="$(atlas_resolve_version "$REPO_ROOT")"
commit="$(atlas_resolve_commit "$REPO_ROOT")"
epoch="$(atlas_source_date_epoch "$REPO_ROOT")"
build_date="$(atlas_epoch_to_iso "$epoch")"

# Toolchain pin. Two builds by different Go patch releases are not expected
# to be byte-identical — the compiler and the runtime both change — so the
# version is pinned in one file and asserted here rather than being left to
# whatever happens to be on PATH. Downgradable to a warning because a
# contributor doing a local `make build` should not be blocked by it; the
# release and verification paths leave it hard.
pinned="${ATLAS_TOOLCHAIN_PIN:-$(tr -d '[:space:]' <"$SCRIPT_DIR/toolchain.txt")}"
actual="$(go env GOVERSION)" # e.g. "go1.25.14"
if [ "$actual" != "go${pinned}" ]; then
	msg="toolchain mismatch: pinned go${pinned}, found ${actual}. Byte-identical output is only promised for the pinned toolchain (.github/scripts/toolchain.txt)."
	if [ "${ATLAS_SKIP_TOOLCHAIN_CHECK:-0}" = "1" ]; then
		atlas_err "warning: $msg"
	else
		atlas_err "$msg"
		atlas_err "set ATLAS_SKIP_TOOLCHAIN_CHECK=1 to build anyway (output will not match the published digests)"
		exit 1
	fi
fi

out="$DIST/$(atlas_artifact_name "$version" "$GOOS" "$GOARCH" "$CMD")"
mkdir -p "$DIST"

# Environment hygiene. Every variable below can change generated code, and
# each one has bitten someone's reproducible build:
#
#   GOFLAGS / GOEXPERIMENT / GODEBUG — inherited from a developer's shell,
#     invisible in the log, and enough to change the binary.
#   GOAMD64 / GOARM64 — micro-architecture levels. A machine with GOAMD64=v3
#     exported produces a faster binary with a different digest, and only
#     someone comparing digests would ever notice.
#
# They are pinned rather than merely unset so the value is a property of this
# script, not of the toolchain's current defaults.
export CGO_ENABLED=0
export GOOS GOARCH
export GOFLAGS="-mod=readonly"
unset GOEXPERIMENT GODEBUG GO111MODULE GOARM GOPPC64 GORISCV64 || true
case "$GOARCH" in
amd64) export GOAMD64=v1 ;;
arm64) export GOARM64=v8.0 ;;
esac

# -buildvcs=false: without it the toolchain stamps the VCS revision, the
# commit time AND a dirty-tree flag into the binary. That makes a build from
# a source tarball differ from a build from a git checkout of the same
# commit, which would put a reproducibility check out of reach for anyone
# who did not clone. Nothing is lost: the version, commit and date all
# arrive through -X below, and resolveBuildInfo() prefers those anyway.
#
# -trimpath: strips the absolute path of the checkout. Without it the same
# commit built in two different directories yields different bytes.
#
# -s -w: drop the symbol table and DWARF. Smaller download; also removes a
# chunk of path-flavoured data from the artifact.
# The stamp target differs per binary: atlas-serve does not import
# internal/cli, and -X against a symbol that is not linked in is silently a
# no-op -- so hardcoding one path would leave the second binary unversioned
# with nothing to notice. See atlas_ldflags_pkg in lib.sh.
stamp_pkg="$(atlas_ldflags_pkg "$CMD")"
ldflags="-s -w"
ldflags="$ldflags -X ${stamp_pkg}.Version=${version}"
ldflags="$ldflags -X ${stamp_pkg}.Commit=${commit}"
ldflags="$ldflags -X ${stamp_pkg}.BuildDate=${build_date}"

printf 'building %s\n' "$out" >&2
printf '  version=%s commit=%s date=%s (SOURCE_DATE_EPOCH=%s)\n' \
	"$version" "$commit" "$build_date" "$epoch" >&2
printf '  target=%s/%s toolchain=%s cgo=0\n' "$GOOS" "$GOARCH" "$actual" >&2

cd "$REPO_ROOT"
go build -trimpath -buildvcs=false -ldflags "$ldflags" -o "$out" "./cmd/$CMD"

printf '%s\n' "$out"
