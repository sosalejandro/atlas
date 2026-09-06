#!/usr/bin/env bash
#
# The dogfood runner: atlas measuring atlas.
#
# This is the headline of issue #122. Every other test in this repo asserts
# that a function behaves; this one runs the shipped commands against the
# shipped repo and checks the answers. It is the only evidence that survives
# contact with a reader who does not trust the test suite, because the subject
# and the instrument are the same artefact.
#
# It is a script rather than inline workflow YAML so the same gate runs
# identically on a laptop and in CI. .github/workflows/ci.yml calls it from
# the `atlas gates atlas (blocking)` job, which takes the coverprofile the
# build-and-test job already wrote and passes it in through
# ATLAS_DOGFOOD_PROFILE, so the suite is not run a second time.
#
# Environment:
#   ATLAS_DOGFOOD_PROFILE  a coverprofile for this repo. Generated here when
#                          unset, which costs a full `go test` run.
#   ATLAS_DOGFOOD_BASE     the git ref `atlas cov diff` compares against.
#                          Defaults to origin/main, then HEAD~1.
#   ATLAS_DOGFOOD_KEEP     set to any value to keep the work directory.
#
# Exit status is the gate: zero when every assertion in dogfood_test.go holds.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

work="$(mktemp -d -t atlas-dogfood-XXXXXX)"
cleanup() {
  if [ -z "${ATLAS_DOGFOOD_KEEP:-}" ]; then
    rm -rf "$work"
  else
    echo "dogfood: work directory kept at $work" >&2
  fi
}
trap cleanup EXIT

# The coverprofile. Reusing CI's is strongly preferred: generating one here
# means running the whole suite a second time, and the second run measures the
# same code as the first.
profile="${ATLAS_DOGFOOD_PROFILE:-}"
if [ -z "$profile" ]; then
  profile="$work/cover.coverprofile"
  echo "dogfood: no ATLAS_DOGFOOD_PROFILE set, generating one (this runs the suite)" >&2
  go test ./packages/... ./internal/... \
    -coverprofile="$profile" \
    -coverpkg=./packages/...,./internal/... \
    -timeout 15m >"$work/test.log" 2>&1 || {
      echo "dogfood: the suite failed while generating a coverprofile; see $work/test.log" >&2
      exit 1
    }
fi
if [ ! -s "$profile" ]; then
  echo "dogfood: coverprofile $profile is missing or empty" >&2
  exit 1
fi

# The base ref for `atlas cov diff`. A shallow CI checkout may have neither,
# in which case the diff assertions report themselves as not-applicable rather
# than inventing a comparison.
base="${ATLAS_DOGFOOD_BASE:-}"
if [ -z "$base" ]; then
  if git rev-parse --verify --quiet origin/main >/dev/null; then
    base="origin/main"
  elif git rev-parse --verify --quiet HEAD~1 >/dev/null; then
    base="HEAD~1"
  fi
fi

export ATLAS_DOGFOOD_PROFILE="$profile"
export ATLAS_DOGFOOD_BASE="$base"

echo "dogfood: profile=$profile base=${base:-<none>}" >&2
go test -tags=dogfood -count=1 -v -timeout 15m ./test/acceptance/ -run 'TestDogfood'
