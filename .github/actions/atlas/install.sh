#!/usr/bin/env bash
# Download, verify and install one released atlas binary.
#
# Used by the composite action in this directory. It is a script rather
# than inline YAML so the parts that do not need the network — the
# runner-label-to-target mapping and the asset naming — are testable from a
# laptop (see .github/scripts/scripts_test.sh). Getting that mapping wrong
# is the likeliest failure in this whole action, and the symptom is a 404
# in someone else's CI.
#
# Usage:
#   install.sh --version vX.Y.Z --os <runner.os> --arch <runner.arch> \
#              --dest DIR [--verify true|false] [--repo owner/name]
#   install.sh --print-asset-name --version vX.Y.Z --os Linux --arch X64
#
# --verify true (the default) requires cosign on PATH.

set -euo pipefail

VERSION=""
RUNNER_OS=""
RUNNER_ARCH=""
DEST=""
VERIFY="true"
REPO="sosalejandro/atlas"
PRINT_ONLY="false"

err() { printf '%s\n' "$*" >&2; }

while [ $# -gt 0 ]; do
	case "$1" in
	--version)
		VERSION="$2"
		shift 2
		;;
	--os)
		RUNNER_OS="$2"
		shift 2
		;;
	--arch)
		RUNNER_ARCH="$2"
		shift 2
		;;
	--dest)
		DEST="$2"
		shift 2
		;;
	--verify)
		VERIFY="$2"
		shift 2
		;;
	--repo)
		REPO="$2"
		shift 2
		;;
	--print-asset-name)
		PRINT_ONLY="true"
		shift
		;;
	*)
		err "unknown argument: $1"
		exit 2
		;;
	esac
done

# A version that is not a release tag cannot name a release asset. The
# common way to get here is pinning the action to a commit SHA, in which
# case github.action_ref is that SHA and there is nothing to download —
# so say that, rather than 404ing on a URL built from a SHA.
if ! printf '%s' "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.-]+)?$'; then
	if [ "$VERSION" = "edge" ]; then
		: # the rolling edge prerelease; assets are named by the describe
	else
		err "not a release version: '${VERSION}'"
		err "Pass an explicit version, e.g."
		err "  - uses: ${REPO}/.github/actions/atlas@v0.14.0"
		err "    with: { version: v0.14.0 }"
		exit 1
	fi
fi

# GitHub's runner labels are not GOOS/GOARCH, and the two vocabularies look
# similar enough to guess wrong: runner.arch says X64 where Go says amd64,
# and runner.os says macOS where Go says darwin.
case "$RUNNER_OS" in
Linux) goos=linux ;;
macOS | Darwin) goos=darwin ;;
Windows) goos=windows ;;
*)
	err "unsupported runner OS: '${RUNNER_OS}' (expected Linux, macOS or Windows)"
	exit 1
	;;
esac

case "$RUNNER_ARCH" in
X64 | x64 | amd64) goarch=amd64 ;;
ARM64 | arm64) goarch=arm64 ;;
*)
	err "unsupported runner architecture: '${RUNNER_ARCH}' (expected X64 or ARM64)"
	exit 1
	;;
esac

asset="atlas_${VERSION}_${goos}_${goarch}"
[ "$goos" = "windows" ] && asset="${asset}.exe"

if [ "$PRINT_ONLY" = "true" ]; then
	printf '%s\n' "$asset"
	exit 0
fi

if [ -z "$DEST" ]; then
	err "--dest is required"
	exit 2
fi
mkdir -p "$DEST"

base="https://github.com/${REPO}/releases/download/${VERSION}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

printf 'downloading %s from %s\n' "$asset" "$VERSION" >&2
curl -fsSL --retry 3 -o "$work/$asset" "$base/$asset"
curl -fsSL --retry 3 -o "$work/SHA256SUMS" "$base/SHA256SUMS"

# Checksum first, signature second. The checksum answers "did the download
# arrive intact"; the signature answers "is this manifest the one the
# release job produced". Only the second is a security property, but doing
# the cheap check first means a truncated download reports a truncated
# download rather than a signature failure.
printf 'verifying checksum\n' >&2
(
	cd "$work"
	if command -v sha256sum >/dev/null 2>&1; then
		grep " ${asset}\$" SHA256SUMS | sha256sum -c -
	else
		grep " ${asset}\$" SHA256SUMS | shasum -a 256 -c -
	fi
)

if [ "$VERIFY" = "true" ]; then
	if ! command -v cosign >/dev/null 2>&1; then
		err "cosign is not on PATH but verification was requested"
		err "(the action installs it; set verify: false only if you have another attestation path)"
		exit 1
	fi
	printf 'verifying signature\n' >&2
	curl -fsSL --retry 3 -o "$work/SHA256SUMS.cosign.bundle" "$base/SHA256SUMS.cosign.bundle"
	# The identity being checked is the workflow file that signed, at a tag
	# in this repository. Without --certificate-identity-regexp any valid
	# Sigstore certificate would satisfy the check, which is to say: any
	# GitHub Actions workflow anywhere.
	cosign verify-blob \
		--bundle "$work/SHA256SUMS.cosign.bundle" \
		--certificate-oidc-issuer https://token.actions.githubusercontent.com \
		--certificate-identity-regexp "^https://github.com/${REPO}/\.github/workflows/(release|edge)\.yml@refs/" \
		"$work/SHA256SUMS"
else
	err "warning: signature verification disabled; the binary's provenance is unchecked"
fi

install_name="atlas"
[ "$goos" = "windows" ] && install_name="atlas.exe"
mv "$work/$asset" "$DEST/$install_name"
chmod +x "$DEST/$install_name"
printf '%s\n' "$DEST/$install_name"
