package coverage

import "regexp"

// The inline feature-annotation grammar, as it appears inside a TEST NAME.
//
// This is a second, smaller grammar than the one packages/codeindex/annotations
// parses out of source comments. It exists because several runners carry no
// structured metadata and the only channel a developer has is the test's own
// name:
//
//	func TestPay(t *testing.T)   // @grunnr:feature billing.pay
//	test("checkout @grunnr:feature billing.pay", ...)
//
// It lived in four copies -- gotest, vitest, playwright and maestro each
// declared its own identical pair -- and that is precisely how it survived the
// rename. #184 taught the source parser, `onboard promote` and the docs to say
// `@grunnr:`, and every one of these four kept matching `@atlas:` only. A user
// who wrote the annotation grunnr now emits and documents got no FeatureID from
// any coverage ingester: the tests were recorded, the coverage was ingested,
// and the feature silently scored zero. A confident wrong number is the worst
// output this project has.
//
// One declaration, therefore, imported by all four. A duplicated grammar is not
// a style problem; it is four places for a spelling to be forgotten, and the
// next time the vocabulary grows this file is the only one that has to learn.
var (
	// FeatureAnnotationRe matches `@grunnr:feature <id>` and the pre-rename
	// `@atlas:feature <id>`. Both are permanent: the legacy spelling is in
	// test names in other people's repositories, and a coverage ingester that
	// stopped reading it would silently unlink their features.
	FeatureAnnotationRe = regexp.MustCompile(`@(?:grunnr|atlas):feature\s+([A-Za-z0-9_.-]+)`)

	// TestregAnnotationRe matches the original `@testreg <id>` form, which
	// accepts a comma-separated list where the newer grammar takes one id.
	TestregAnnotationRe = regexp.MustCompile(`@testreg\s+([A-Za-z0-9_.,-]+)`)
)
