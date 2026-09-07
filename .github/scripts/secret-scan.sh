#!/usr/bin/env bash
# Scan the repository for committed credentials.
#
# Atlas ships a secret DETECTOR (packages/redact, issue #131), which creates a
# tension nothing resolves cleanly: testing a detector requires inputs that look
# exactly like the thing it detects. Those fixtures are not credentials and
# never were -- but no scanner can tell, and neither can a reviewer skimming a
# diff, which is the more important half.
#
# A literal AWS temporary key id in packages/redact raised a real GitHub
# secret-scanning alert after the M4 batch merged. Nothing leaked. The cost was
# an alert indistinguishable from one that mattered, and a maintainer who learns
# to dismiss alerts is the actual failure this script exists to prevent.
#
# This runs the same scan locally that CI runs, against the same config, so the
# answer does not depend on which machine asked. That is the whole reason it is
# a script rather than steps in a workflow: the alternative is that the only way
# to test a change is to push and watch.
#
# Inputs:
#   GITLEAKS_VERSION  override the pinned version (for testing an upgrade)
# Exit codes:
#   0    nothing found
#   1    a credential-shaped value was found
#   2    bad usage (unknown mode, missing config)
#   127  gitleaks is not installed -- could not look
#
# Inputs (continued):
#   SCAN_MODE         "tree" (default) scans the working tree; "history" scans
#                     every commit. CI runs history; the pre-commit path runs
#                     tree, because scanning 220 commits on every commit is how
#                     a hook gets uninstalled.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
# shellcheck source=lib.sh
. "$SCRIPT_DIR/lib.sh"

# Pinned, like the Go toolchain is pinned. A scanner that silently changes
# version changes what it finds, and "CI went red and nobody changed anything"
# is a bad afternoon.
# Exit codes are part of this script's contract, because a caller must be able
# to tell "I found a secret" from "I could not look". A test that treats any
# non-zero exit as detection passes when the scanner is simply absent -- which
# is exactly how the first version of the planted-key test in scripts_test.sh
# passed on a runner with no gitleaks installed.
readonly EXIT_LEAK_FOUND=1
readonly EXIT_BAD_USAGE=2
readonly EXIT_CANNOT_RUN=127

GITLEAKS_VERSION="${GITLEAKS_VERSION:-8.30.0}"
SCAN_MODE="${SCAN_MODE:-tree}"
CONFIG="$REPO_ROOT/.gitleaks.toml"

if [ ! -f "$CONFIG" ]; then
	atlas_err "FAIL: no .gitleaks.toml at $CONFIG"
	atlas_err "The allowlist lives there, and running without it would flag every"
	atlas_err "fixture in packages/redact. Refusing to scan with default rules."
	exit "$EXIT_BAD_USAGE"
fi

# Resolve the binary: an already-installed gitleaks on PATH wins, so a
# contributor is not made to download one. CI installs it in a prior step.
if command -v gitleaks >/dev/null 2>&1; then
	GITLEAKS_BIN="$(command -v gitleaks)"
else
	atlas_err "gitleaks is not on PATH."
	atlas_err ""
	atlas_err "Install it with one of:"
	atlas_err "  go install github.com/zricethezav/gitleaks/v8@v${GITLEAKS_VERSION}"
	atlas_err "    (the module path is zricethezav/..., not gitleaks/... -- the repo"
	atlas_err "     moved org but the go module path did not follow)"
	atlas_err "  brew install gitleaks"
	atlas_err ""
	atlas_err "Or see https://github.com/gitleaks/gitleaks#installing"
	exit "$EXIT_CANNOT_RUN"
fi

have_version="$("$GITLEAKS_BIN" version 2>/dev/null || echo unknown)"
if [ "$have_version" != "$GITLEAKS_VERSION" ]; then
	# A warning, not a failure. Pinning matters for reproducibility of the
	# RESULT, but refusing to run because a contributor has 8.29 installed
	# would mean most people simply do not run it.
	atlas_err "note: gitleaks $have_version installed, $GITLEAKS_VERSION pinned; findings may differ"
fi

case "$SCAN_MODE" in
tree)
	# --no-git: scan the files as they are on disk, including uncommitted
	# work. This is the mode that catches a secret BEFORE it is committed,
	# which is the only point at which the fix is free.
	set -- detect --no-git --source "$REPO_ROOT" -c "$CONFIG" --redact --no-banner
	;;
history)
	set -- detect --source "$REPO_ROOT" -c "$CONFIG" --redact --no-banner
	;;
*)
	atlas_err "FAIL: SCAN_MODE must be 'tree' or 'history', got '$SCAN_MODE'"
	exit "$EXIT_BAD_USAGE"
	;;
esac

echo "gitleaks $have_version, mode=$SCAN_MODE, config=.gitleaks.toml"

# Capture the output so the "did it actually read anything?" check below can
# see it. A scanner that reports success having read nothing is
# indistinguishable, from the exit code alone, from one that read everything
# and found nothing -- and this repo shipped exactly that for eight batches:
# .gitleaks.toml's worktree allowlist was unanchored, so it matched the
# ABSOLUTE path, and any scan rooted inside .claude/worktrees allowlisted its
# own entire tree. Every agent's "secrets: ok" there was vacuous.
scan_log="$(mktemp)"
trap 'rm -f "$scan_log"' EXIT
if "$GITLEAKS_BIN" "$@" 2>&1 | tee "$scan_log"; then
	# gitleaks prints "scanned ~N bytes". A repository this size is megabytes;
	# anything under 64 KB means the scan was allowlisted or misrooted out of
	# existence, whatever the exit code said.
	scanned="$(grep -oE 'scanned ~[0-9]+ bytes' "$scan_log" | head -1 | grep -oE '[0-9]+' || echo 0)"
	if [ "${scanned:-0}" -lt 65536 ]; then
		atlas_err ""
		atlas_err "FAIL: gitleaks reported success after reading only ${scanned:-0} bytes."
		atlas_err ""
		atlas_err "That is not a clean scan, it is a scan that did not happen. The usual"
		atlas_err "cause is an allowlist in .gitleaks.toml matching the scan root itself:"
		atlas_err "a path pattern without a leading ^ is matched against the ABSOLUTE"
		atlas_err "path, so running from inside a directory that pattern names allowlists"
		atlas_err "everything under it."
		atlas_err ""
		atlas_err "Check the [allowlist] paths in .gitleaks.toml against \$PWD."
		exit "$EXIT_BAD_USAGE"
	fi
	echo "ok    no committed credentials found (${scanned} bytes scanned)"
	exit 0
fi

atlas_err ""
atlas_err "FAIL: gitleaks found something that looks like a credential."
atlas_err ""
atlas_err "If it is REAL: rotate it first, then remove it. Rotation comes first"
atlas_err "because the value is already in the reflog, in every clone, and"
atlas_err "possibly in a CI log -- removing it from the tip does not unpublish it."
atlas_err ""
atlas_err "If it is a TEST FIXTURE for packages/redact:"
atlas_err "  - vendor-specific shape (AWS, Stripe, GitHub token)? assemble it at"
atlas_err "    runtime so no literal exists -- see awsSessionShaped in"
atlas_err "    packages/redact/detect_test.go. GitHub's own scanner cannot be"
atlas_err "    configured, so a literal there raises an alert we cannot suppress."
atlas_err "  - generic shape (PEM header, high-entropy assignment)? add it to the"
atlas_err "    scoped allowlist in .gitleaks.toml, by path AND rule."
atlas_err ""
atlas_err "Do not add a blanket allowlist. The allowlist is scoped so that a real"
atlas_err "key committed outside the detector's fixtures still fails this scan."
exit "$EXIT_LEAK_FOUND"
