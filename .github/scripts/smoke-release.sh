#!/usr/bin/env bash
# Run a RELEASED atlas binary on the OS it was built for.
#
# Everything else in this pipeline proves the artifacts exist, are signed,
# and were reproducibly built. None of it proves they START. The
# cross-compile happens on one Linux runner for six targets; until this
# script ran, no darwin or windows binary atlas published had ever been
# executed by anything.
#
# So the bar here is deliberately low and end-to-end rather than deep: the
# binary runs, indexes a real (tiny) repository, and doctor agrees the index
# it just built is coherent. A unit test cannot fail the way this can --
# what it catches is a target-specific runtime fault (a path separator, an
# absolute-path key, a missing syscall) that compiles perfectly.
#
# No toolchain is assumed. This runs against the downloaded artifact with
# nothing installed, because that is the situation a user is in.
#
# Usage: smoke-release.sh <path-to-atlas-binary>
# Exit codes:
#   0    the binary ran and produced a coherent index
#   1    it did not
#   2    bad usage

set -euo pipefail

ATLAS="${1:-}"
if [ -z "$ATLAS" ]; then
	echo "usage: smoke-release.sh <path-to-atlas-binary>" >&2
	exit 2
fi
if [ ! -f "$ATLAS" ]; then
	echo "FAIL: no such binary: $ATLAS" >&2
	exit 2
fi
# Resolve to an absolute path before the cd below.
ATLAS="$(cd "$(dirname "$ATLAS")" && pwd)/$(basename "$ATLAS")"
chmod +x "$ATLAS" 2>/dev/null || true

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# A fixture with one annotated symbol and one test, which is the smallest
# thing that exercises index -> registry -> coverage rather than just
# "the process started".
mkdir -p "$work/billing"
cat > "$work/go.mod" <<'EOF'
module smoke.test

go 1.21
EOF
cat > "$work/billing/order.go" <<'EOF'
package billing

// Pay settles an order.
//
// @atlas:feature billing.pay
func Pay(cents int) int {
	if cents < 0 {
		return 0
	}
	return cents
}
EOF
cat > "$work/billing/order_test.go" <<'EOF'
package billing

import "testing"

func TestPay(t *testing.T) {
	if Pay(5) != 5 {
		t.Fatal("Pay(5) != 5")
	}
}
EOF

cd "$work"
# A git repo, because atlas resolves its root and its .atlas/ location from
# one and several commands diff against refs.
git init -q -b main .
git config user.email "smoke@atlas.test"
git config user.name "atlas smoke"
git add -A
git -c commit.gpgsign=false commit -q -m "smoke fixture"

fail() {
	echo "" >&2
	echo "FAIL: $1" >&2
	echo "" >&2
	echo "The binary is present and signed; it did not survive contact with a" >&2
	echo "repository on this OS. That is a target-specific runtime fault, which" >&2
	echo "is exactly the class the cross-compile check cannot see." >&2
	exit 1
}

echo "── atlas --version"
"$ATLAS" --version || fail "--version did not run"

echo "── atlas init"
"$ATLAS" init || fail "init did not complete"

# The index has to exist on disk under the path atlas chose for itself.
[ -f ".atlas/atlas.db" ] || fail "init reported success but wrote no .atlas/atlas.db"

echo "── atlas scan"
"$ATLAS" scan || fail "scan did not complete"

# doctor is the assertion that matters: it is the command whose whole job is
# to notice an index that disagrees with the tree. Passing here means the
# scan on THIS os produced spans that still describe these files.
echo "── atlas doctor"
"$ATLAS" doctor || fail "doctor found the freshly-built index incoherent"

# And the honesty check on the check: doctor must actually have examined
# something. A report of zero checks exits 0 and means nothing -- the same
# vacuous-success shape that made `make secret-scan` pass for eight batches.
echo "── atlas doctor --json (must have examined something)"
"$ATLAS" doctor --json > doctor.json || fail "doctor --json did not run"
if ! grep -q '"checks"' doctor.json; then
	fail "doctor --json emitted no checks array"
fi
if grep -qE '"checks"[[:space:]]*:[[:space:]]*\[[[:space:]]*\]' doctor.json; then
	fail "doctor ran zero checks; a clean report over nothing is not a clean report"
fi

echo ""
echo "ok    the released binary indexed a repository and doctor agreed"
