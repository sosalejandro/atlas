#!/usr/bin/env bash
# Rename a built dist/ from version-stamped asset names to the edge
# channel's fixed ones.
#
# Why this exists. The consumer action installs the edge channel by asking
# for `atlas_edge_<goos>_<goarch>`: install.sh builds the asset name out of
# the version string it was handed, and for the rolling channel that string
# is literally "edge". But an edge build is stamped with `git describe`, so
# build.sh writes `atlas_v0.13.0-7-gabc1234_linux_amd64`. Publishing only
# those names makes every documented edge install 404 — the channel is
# advertised in action.yml and docs/install.md and reachable from neither.
#
# Renaming, rather than building twice or dropping the describe string:
# the version stamped INSIDE the binary is untouched, so `atlas version`
# still reports `v0.13.0-7-gabc1234`, which is what identifies the build.
# Only the filename is made stable, because a rolling channel needs a URL
# that does not move between commits.
#
# Run this AFTER the SBOM is generated and BEFORE SHA256SUMS is written, so
# the manifest — and therefore the signature over it — covers the names
# consumers actually download. A manifest keyed by names nobody can fetch
# verifies nothing.
#
# Usage: edge-assets.sh --dist DIR --version VERSION

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "$SCRIPT_DIR/lib.sh"

DIST=""
VERSION=""

while [ $# -gt 0 ]; do
	case "$1" in
	--dist)
		DIST="$2"
		shift 2
		;;
	--version)
		VERSION="$2"
		shift 2
		;;
	*)
		atlas_err "unknown argument: $1"
		exit 2
		;;
	esac
done

if [ -z "$DIST" ] || [ -z "$VERSION" ]; then
	atlas_err "usage: edge-assets.sh --dist DIR --version VERSION"
	exit 2
fi
if [ ! -d "$DIST" ]; then
	atlas_err "not a directory: $DIST"
	exit 1
fi

# Already-fixed names: the caller stamped "edge" as the version itself.
# Nothing to do, and renaming would collide with itself.
if [ "$VERSION" = "edge" ]; then
	printf 'assets already carry the edge names\n' >&2
	exit 0
fi

# The prefix is stripped through a quoted variable, not an inline
# expansion: the version string is `git describe` output and the left side
# of `${var#pattern}` is a glob, so quoting is what keeps it a literal.
prefix="atlas_${VERSION}_"

renamed=0
for src in "$DIST"/atlas_"$VERSION"_*; do
	[ -e "$src" ] || continue
	base="$(basename "$src")"
	dst="$DIST/atlas_edge_${base#"$prefix"}"
	if [ -e "$dst" ]; then
		atlas_err "refusing to overwrite an existing asset: $dst"
		exit 1
	fi
	mv "$src" "$dst"
	printf '  %s -> %s\n' "$base" "$(basename "$dst")" >&2
	renamed=$((renamed + 1))
done

# Zero matches means the naming convention moved and the publish would ship
# assets no documented install command can fetch. Fail loudly here rather
# than 404 in someone else's CI.
if [ "$renamed" -eq 0 ]; then
	atlas_err "no assets matched atlas_${VERSION}_* in $DIST"
	atlas_err "the edge release would carry names no install path asks for"
	exit 1
fi

printf 'renamed %d asset(s) to the edge channel names\n' "$renamed" >&2
