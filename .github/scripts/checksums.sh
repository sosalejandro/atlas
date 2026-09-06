#!/usr/bin/env bash
# Write SHA256SUMS for a directory of release artifacts.
#
# Usage: checksums.sh <dist-dir>
#
# The manifest is what actually gets signed. Signing six binaries costs six
# signatures and six verification steps for the user; signing one checksum
# file that covers all six costs one of each, and the digests inside it are
# what tie the signature to the bytes. That is why the published
# verification recipe in docs/install.md verifies the signature over this
# file and then runs `sha256sum -c` against it.
#
# Two properties matter and are tested in scripts_test.sh:
#   - basenames only. `sha256sum -c` resolves paths relative to the working
#     directory, so a manifest carrying the publisher's directory layout is
#     unusable by anyone who downloads it.
#   - a fixed order. Readdir order is filesystem-dependent, so an unsorted
#     manifest differs between the release job and a third party rebuilding
#     it — which looks exactly like a tampering signal.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "$SCRIPT_DIR/lib.sh"

DIST="${1:-}"
if [ -z "$DIST" ] || [ ! -d "$DIST" ]; then
	atlas_err "usage: checksums.sh <dist-dir>"
	exit 1
fi

OUT="$DIST/SHA256SUMS"
rm -f "$OUT"

# LC_ALL=C so the sort order is byte order rather than the runner's locale
# collation — otherwise the same six files can produce two different
# manifests on two machines.
files="$(cd "$DIST" && find . -maxdepth 1 -type f ! -name 'SHA256SUMS*' -print |
	sed 's|^\./||' | LC_ALL=C sort)"

if [ -z "$files" ]; then
	atlas_err "no artifacts in $DIST — refusing to write an empty manifest"
	atlas_err "(an empty SHA256SUMS verifies successfully against nothing, which is worse than no file)"
	exit 1
fi

tmp="$(mktemp)"
while IFS= read -r f; do
	[ -n "$f" ] || continue
	printf '%s  %s\n' "$(atlas_sha256 "$DIST/$f")" "$f" >>"$tmp"
done <<EOF
$files
EOF

mv "$tmp" "$OUT"
printf 'wrote %s\n' "$OUT" >&2
cat "$OUT" >&2
