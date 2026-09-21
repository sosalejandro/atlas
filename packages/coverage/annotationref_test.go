package coverage_test

import (
	"testing"

	"github.com/sosalejandro/grunnr/packages/coverage"
)

// The grammar must accept BOTH prefixes, and the reason the two halves are
// pinned separately is that they fail in opposite directions.
//
// Missing `@grunnr:` is the bug this file was written for: #184 moved what
// grunnr EMITS and DOCUMENTS to the canonical prefix while four coverage
// ingesters went on matching `@atlas:` alone. A user following the current
// documentation got no FeatureID from any of them -- the test was recorded,
// the coverage was ingested, and the feature silently scored zero.
//
// Dropping `@atlas:` would be the mirror failure: those test names are in
// other people's repositories, and un-reading them would unlink features that
// have been scoring correctly for months.
func TestFeatureAnnotationRe_AcceptsBothPrefixes(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"canonical prefix", "TestPay // @grunnr:feature billing.pay", "billing.pay"},
		{"legacy prefix", "TestPay // @atlas:feature billing.pay", "billing.pay"},
		{"canonical inside a JS test title", `test("checkout @grunnr:feature billing.pay", ...)`, "billing.pay"},
		{"legacy inside a JS test title", `test("checkout @atlas:feature billing.pay", ...)`, "billing.pay"},
		{"dashed and dotted id", "@grunnr:feature meal-prep.batch-session", "meal-prep.batch-session"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := coverage.FeatureAnnotationRe.FindStringSubmatch(tc.in)
			if len(m) != 2 {
				t.Fatalf("no match in %q", tc.in)
			}
			if m[1] != tc.want {
				t.Errorf("id = %q, want %q", m[1], tc.want)
			}
		})
	}
}

// A near-miss must not match. The prefix is an alternation of exactly two
// spellings, not a wildcard, or a stray `@foo:feature` in a test name would
// start linking features nobody declared.
func TestFeatureAnnotationRe_RejectsOtherPrefixes(t *testing.T) {
	for _, in := range []string{
		"@notgrunnr:feature billing.pay",
		"@feature billing.pay",
		"@grunnr:featurebilling.pay",
		"grunnr:feature billing.pay",
	} {
		if m := coverage.FeatureAnnotationRe.FindStringSubmatch(in); len(m) == 2 {
			t.Errorf("%q matched and yielded %q", in, m[1])
		}
	}
}

// The legacy form takes a comma-separated list where the newer grammar takes
// one id. Pinned because the two regexes are now declared side by side and the
// character classes differ by exactly that comma.
func TestTestregAnnotationRe_StillAcceptsAList(t *testing.T) {
	m := coverage.TestregAnnotationRe.FindStringSubmatch("TestPay // @testreg billing.pay,billing.refund")
	if len(m) != 2 {
		t.Fatal("no match")
	}
	if m[1] != "billing.pay,billing.refund" {
		t.Errorf("captured %q, want the whole comma-separated list", m[1])
	}
}
