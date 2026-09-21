package coverage_test

import (
	"strings"
	"testing"

	"github.com/sosalejandro/grunnr/packages/coverage"
	"github.com/sosalejandro/grunnr/packages/coverage/gotest"
	"github.com/sosalejandro/grunnr/packages/coverage/maestro"
	"github.com/sosalejandro/grunnr/packages/coverage/playwright"
	"github.com/sosalejandro/grunnr/packages/coverage/vitest"
)

// Every ingester that derives a FeatureID from a test name must accept both
// spellings, and this holds all of them to it in ONE place.
//
// A unit test on the shared regex would not have caught the bug that prompted
// this file. The grammar lived in four identical copies -- gotest, vitest,
// playwright and maestro each declared its own -- so the failure was not "the
// regex is wrong", it was "three of the four were never told". Only a test that
// drives each ingester's real Parse can see that, and only a table that
// enumerates them makes a fifth ingester's absence visible.
//
// What it prevents: #184 moved what grunnr EMITS and DOCUMENTS to `@grunnr:`
// while all four ingesters still matched `@atlas:` alone. A user following the
// current documentation got no FeatureID from any of them. The test ran, the
// coverage was ingested, and the feature scored zero -- a confident wrong
// number, which is the worst output this project has.
func TestEveryIngester_DerivesTheFeatureFromBothPrefixes(t *testing.T) {
	// %s is the annotation, spliced into each runner's real payload shape.
	ingesters := []struct {
		name  string
		build func(annotation string) string
		parse func(string) (coverage.Run, []coverage.Result, error)
	}{
		{
			name: "gotest",
			build: func(a string) string {
				return `{"Action":"run","Package":"example/auth","Test":"TestLogin"}
{"Action":"output","Package":"example/auth","Test":"TestLogin","Output":"` + a + `\n"}
{"Action":"pass","Package":"example/auth","Test":"TestLogin","Elapsed":0.5}`
			},
			parse: func(s string) (coverage.Run, []coverage.Result, error) {
				return gotest.Parse(strings.NewReader(s))
			},
		},
		{
			name: "vitest",
			build: func(a string) string {
				return `{"startTime":1747555200000,"numPassedTests":1,"numFailedTests":0,
"numPendingTests":0,"success":true,"testResults":[{"name":"src/auth/login.test.ts",
"startTime":1747555200000,"endTime":1747555201000,"assertionResults":[
{"title":"` + a + ` succeeds","fullName":"` + a + ` succeeds",
"ancestorTitles":[],"status":"passed","duration":8}]}]}`
			},
			parse: func(s string) (coverage.Run, []coverage.Result, error) {
				return vitest.Parse(strings.NewReader(s))
			},
		},
		{
			name: "playwright",
			build: func(a string) string {
				return `{"stats":{"startTime":"2026-05-18T10:00:00.000Z","duration":100,
"expected":1,"unexpected":0,"flaky":0,"skipped":0},"suites":[{"title":"e2e/auth/login.spec.ts",
"file":"e2e/auth/login.spec.ts","specs":[{"title":"logs in ` + a + `",
"tests":[{"status":"expected","results":[{"status":"passed","duration":1200}]}]}]}]}`
			},
			parse: func(s string) (coverage.Run, []coverage.Result, error) {
				return playwright.Parse(strings.NewReader(s))
			},
		},
		{
			name: "maestro",
			build: func(a string) string {
				return `<?xml version="1.0" encoding="UTF-8"?>
<testsuites>
  <testsuite name="flows" tests="1" failures="0" skipped="0" time="12.3" timestamp="2026-05-18T10:00:00Z">
    <testcase name="auth-login-valid ` + a + `" classname="login" time="12.3" file="flows/auth-login-valid.yaml"/>
  </testsuite>
</testsuites>`
			},
			parse: func(s string) (coverage.Run, []coverage.Result, error) {
				return maestro.Parse(strings.NewReader(s))
			},
		},
	}

	// The id must be one the FILE PATH cannot produce.
	//
	// vitest and playwright fall back to inferring a feature from the test
	// file's path when no annotation matches, and the first draft of this test
	// used `auth.login` against `src/auth/login.test.ts` -- which
	// inferFromPath derives exactly. Both vitest subtests passed with the
	// annotation regex reverted to @atlas-only, because the fallback was
	// quietly supplying the answer. A mutation run is the only reason that was
	// noticed. `billing.refund` is reachable from nothing in these fixtures
	// except the annotation itself.
	const wantID = "billing.refund"

	// Both spellings, permanently. The canonical one is what grunnr emits and
	// documents today; the legacy one is in test names in other people's
	// repositories, and un-reading it would unlink features that have been
	// scoring correctly for months.
	for _, prefix := range []string{"@grunnr:feature", "@atlas:feature"} {
		for _, ing := range ingesters {
			t.Run(ing.name+"/"+prefix, func(t *testing.T) {
				_, results, err := ing.parse(ing.build(prefix + " " + wantID))
				if err != nil {
					t.Fatalf("Parse: %v", err)
				}
				if len(results) == 0 {
					t.Fatal("no results parsed; the fixture does not exercise this ingester")
				}
				var got string
				for _, r := range results {
					if r.FeatureID != nil {
						got = string(*r.FeatureID)
						break
					}
				}
				if got != wantID {
					t.Errorf("%s did not derive %q from %q: got %q -- a path fallback "+
						"cannot produce this id, so an empty or different value means the "+
						"annotation was not read", ing.name, wantID, prefix, got)
				}
			})
		}
	}
}
