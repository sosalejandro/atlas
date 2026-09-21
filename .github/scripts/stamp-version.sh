#!/usr/bin/env bash
# Write the release version, commit and build date into internal/cli/root.go.
#
# release-please opens the release PR; this stamps the three package-level
# vars in it so a `go install` from the tag reports what it actually is.
#
# This is a script rather than steps in release-please.yml for the reason
# lib.sh states: inlined in YAML, the only way to test a change is to push and
# watch. That cost was paid. The stamp silently stopped working at v0.8.0 and
# nobody found out until a release was attempted, because the only signal was
# a job nobody reads on a workflow that runs on main.
#
# What broke it is worth stating, because the fix is shaped around it. The sed
# anchored on the exact column the `=` sat in:
#
#     s|^(\tVersion   = ).*$|...|
#
# Three spaces, because gofmt had aligned Version, Commit and BuildDate as one
# group. Then an explanatory comment was added between Version and Commit.
# gofmt treats a comment as a group separator, so Version became its own group
# and re-aligned to a single space -- while Commit and BuildDate, still a pair,
# kept theirs. The pattern for those two went on matching. Only Version broke,
# which is why the failure read as one odd var rather than a broken stamp.
#
# So: never anchor on alignment. gofmt owns that whitespace and will change it
# whenever the declarations around it change, with no warning and no diff in
# the line you care about.
#
# Inputs (environment):
#   VERSION     semver WITHOUT a leading v (release-please's form: 0.15.0)
#   COMMIT      short sha to record; defaults to HEAD
#   BUILD_DATE  RFC3339 UTC; defaults to now
#   FILE        file to stamp; defaults to internal/cli/root.go
# Exit codes:
#   0    all three vars carry the requested values
#   1    a var was not rewritten, or the file does not declare it
#   2    bad usage (missing VERSION, malformed VERSION, no such file)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
# shellcheck source=lib.sh
. "$SCRIPT_DIR/lib.sh"

readonly EXIT_NOT_STAMPED=1
readonly EXIT_BAD_USAGE=2

FILE="${FILE:-internal/cli/root.go}"
case "$FILE" in
/*) ;;
*) FILE="$REPO_ROOT/$FILE" ;;
esac

if [ -z "${VERSION:-}" ]; then
	grunnr_err "FAIL: VERSION is empty."
	grunnr_err "Expected release-please's form, a bare semver with no leading v (0.15.0)."
	exit "$EXIT_BAD_USAGE"
fi

# Pin the shape. release-please is trusted, but a malformed version reaching
# sed's replacement text is how a stamp writes something that compiles and is
# wrong -- and the value ends up in a signed release.
if ! printf '%s' "$VERSION" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
	grunnr_err "FAIL: VERSION='$VERSION' is not a bare semver."
	grunnr_err "Pass 0.15.0, not v0.15.0 and not a tag ref."
	exit "$EXIT_BAD_USAGE"
fi

if [ ! -f "$FILE" ]; then
	grunnr_err "FAIL: no such file: $FILE"
	exit "$EXIT_BAD_USAGE"
fi

COMMIT="${COMMIT:-$(git -C "$REPO_ROOT" rev-parse --short=7 HEAD)}"
BUILD_DATE="${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

echo "stamping ${FILE#"$REPO_ROOT"/}: Version=v${VERSION} Commit=${COMMIT} BuildDate=${BUILD_DATE}"

# stamp_var rewrites one package-level var's value, whatever whitespace gofmt
# has put around the `=` and whatever the current value is.
#
# The left side matches a single leading tab, the name, any run of spaces, and
# the `=`; everything after it is replaced. One leading tab is what keeps this
# off `Version:` inside a struct literal deeper in the file, which is indented
# further and uses a colon.
stamp_var() {
	local name="$1" value="$2" file="$3"
	if ! grep -qE "^	${name}[[:space:]]*=" "$file"; then
		grunnr_err "FAIL: $file declares no package-level \`${name} =\` at one tab of indent."
		grunnr_err "The stamp writes into the var block in root.go; if that block moved or"
		grunnr_err "the var was renamed, this script must be updated with it rather than"
		grunnr_err "silently stamping nothing."
		return 1
	fi
	sed -i -E "s|^(	${name}[[:space:]]*= ).*\$|\1\"${value}\"|" "$file"

	# Assert the VALUE, not that the line changed. A "did it change" check
	# passes when a re-run rewrites an already-correct line, and passes when
	# sed writes the wrong thing.
	if ! grep -qE "^	${name}[[:space:]]*= \"${value}\"\$" "$file"; then
		grunnr_err "FAIL: ${name} was not rewritten to \"${value}\" in $file"
		grep -nE "^	${name}" "$file" >&2 || true
		return 1
	fi
	return 0
}

rc=0
stamp_var Version "v${VERSION}" "$FILE" || rc=1
stamp_var Commit "${COMMIT}" "$FILE" || rc=1
stamp_var BuildDate "${BUILD_DATE}" "$FILE" || rc=1
if [ "$rc" -ne 0 ]; then
	exit "$EXIT_NOT_STAMPED"
fi

# gofmt owns the alignment in that block, and the rewrite can change the width
# of a value enough to move it. Leaving the file unformatted would fail lint
# on the release PR -- the one PR nobody wants to debug.
if command -v gofmt >/dev/null 2>&1; then
	gofmt -w "$FILE"
	# Re-assert after formatting: gofmt may have changed the spacing this
	# script just wrote, and the values must survive that.
	for pair in "Version:v${VERSION}" "Commit:${COMMIT}" "BuildDate:${BUILD_DATE}"; do
		name="${pair%%:*}"
		value="${pair#*:}"
		if ! grep -qE "^	${name}[[:space:]]*= \"${value}\"\$" "$FILE"; then
			grunnr_err "FAIL: gofmt changed ${name} away from \"${value}\""
			exit "$EXIT_NOT_STAMPED"
		fi
	done
else
	grunnr_err "note: gofmt not on PATH; stamped file left unformatted"
fi

echo "ok    all three stamps written"
