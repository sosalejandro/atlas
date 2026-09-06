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
GITLEAKS_VERSION="${GITLEAKS_VERSION:-8.30.0}"
SCAN_MODE="${SCAN_MODE:-tree}"
CONFIG="$REPO_ROOT/.gitleaks.toml"

if [ ! -f "$CONFIG" ]; then
	atlas_err "FAIL: no .gitleaks.toml at $CONFIG"
	atlas_err "The allowlist lives there, and running without it would flag every"
	atlas_err "fixture in packages/redact. Refusing to scan with default rules."
	exit 1
fi

# Resolve the binary: an already-installed gitleaks on PATH wins, so a
# contributor is not made to download one. CI installs it in a prior step.
if command -v gitleaks >/dev/null 2>&1; then
	GITLEAKS_BIN="$(command -v gitleaks)"
else
	atlas_err "gitleaks is not on PATH."
	atlas_err ""
	atlas_err "Install it with one of:"
	atlas_err "  go install github.com/gitleaks/gitleaks/v8@v${GITLEAKS_VERSION}"
	atlas_err "  brew install gitleaks"
	atlas_err ""
	atlas_err "Or see https://github.com/gitleaks/gitleaks#installing"
	exit 127
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
	exit 2
	;;
esac

echo "gitleaks $have_version, mode=$SCAN_MODE, config=.gitleaks.toml"
if "$GITLEAKS_BIN" "$@"; then
	echo "ok    no committed credentials found"
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
exit 1
