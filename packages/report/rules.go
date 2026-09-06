package report

import "sort"

// The rule ids atlas emits. They are a wire contract: GitHub keys an alert's
// history off the rule id, so renaming one closes every open alert and reopens
// it as new. Add rules; do not rename them.
//
// The "atlas/" prefix namespaces them against the other tools uploading SARIF
// to the same repository (CodeQL, Semgrep, golangci-lint), which is what keeps
// two tools' "dead-code" rules from being read as one.
const (
	RuleFeatureUncovered     = "atlas/feature-uncovered"
	RuleCoverageUnattributed = "atlas/coverage-unattributed"
	RuleDeadCode             = "atlas/dead-code"
	RuleContractDrift        = "atlas/contract-drift"
	RuleDiagnosis            = "atlas/diagnosis"
)

// Rule is the metadata SARIF requires for each rule a run reports against.
//
// The descriptions are not decoration: GitHub shows ShortDescription in the
// alert list and FullDescription on the alert page, and an alert whose page
// says nothing about why it fired gets dismissed rather than fixed.
type Rule struct {
	// ID is the wire id, one of the Rule* constants.
	ID string

	// Name is the human-facing rule name. SARIF wants an opaque-ish
	// identifier here; GitHub renders it as the alert's title.
	Name string

	// ShortDescription is one line, no trailing period per SARIF guidance.
	ShortDescription string

	// FullDescription explains what the rule measures and what to do.
	FullDescription string

	// HelpURI points at the docs page for the rule.
	HelpURI string

	// DefaultLevel is the level a finding gets when its producer expresses
	// no opinion. Individual findings may override it (a badly enough
	// scored feature is an error even though the rule defaults to warning).
	DefaultLevel Severity

	// SecuritySeverity is the CVSS-shaped number GitHub sorts the alert
	// list by, as a string because that is how SARIF carries it. Empty for
	// rules that are hygiene rather than risk — inventing a security score
	// for a dead-code candidate would push it above real vulnerabilities.
	SecuritySeverity string

	// Tags land in properties.tags and drive GitHub's alert filters.
	Tags []string
}

const helpBase = "https://github.com/sosalejandro/atlas/blob/main/docs/commands/report.md"

// catalog is the closed set of rules atlas can report. RenderSARIF refuses a
// finding whose rule is not here, which is the only defence against the
// failure mode where GitHub accepts the upload and drops the result.
var catalog = []Rule{
	{
		ID:               RuleContractDrift,
		Name:             "ContractDrift",
		ShortDescription: "A declared contract no longer matches its implementation",
		FullDescription: "The contract recorded for this symbol (an HTTP route, a GraphQL field, " +
			"an exported function signature) differs from what the current code exposes. " +
			"Consumers written against the recorded shape will break at runtime, not at compile time.",
		HelpURI:      helpBase + "#atlascontract-drift",
		DefaultLevel: SeverityError,
		// Drift in a published interface is the one atlas rule with a
		// blast radius outside the repo, so it is the one that earns a
		// place in GitHub's security-severity ordering.
		SecuritySeverity: "5.0",
		Tags:             []string{"atlas", "contract", "api"},
	},
	{
		ID:               RuleFeatureUncovered,
		Name:             "FeatureUncovered",
		ShortDescription: "A feature's health score is below the configured floor",
		FullDescription: "atlas scored this feature from its coverage, annotation freshness, pattern " +
			"compliance and contract drift signals, and the result is below the floor the repository " +
			"gates on. The score's components name which signal dragged it down.",
		HelpURI:      helpBase + "#atlasfeature-uncovered",
		DefaultLevel: SeverityWarning,
		Tags:         []string{"atlas", "coverage", "maintainability"},
	},
	{
		ID:               RuleCoverageUnattributed,
		Name:             "CoverageUnattributed",
		ShortDescription: "Executed statements atlas could not charge to any indexed symbol",
		FullDescription: "The coverage report says these statements ran, but atlas has no indexed symbol " +
			"covering them, so they contribute to no feature's score. This is a blind spot in the " +
			"measurement rather than a defect in the code: usually the file was excluded from the scan, " +
			"or the coverage report names it by an import path atlas cannot map to the checkout.",
		HelpURI:      helpBase + "#atlascoverage-unattributed",
		DefaultLevel: SeverityNote,
		Tags:         []string{"atlas", "coverage", "measurement"},
	},
	{
		ID:               RuleDeadCode,
		Name:             "DeadCode",
		ShortDescription: "A symbol with no qualifying incoming edges",
		FullDescription: "Nothing in the indexed graph references this symbol. Treat it as a triage " +
			"candidate, not a verdict: dynamic dispatch, plugin entry points and re-export chains are " +
			"invisible to static analysis and surface here even when the symbol is live at runtime.",
		HelpURI:      helpBase + "#atlasdead-code",
		DefaultLevel: SeverityNote,
		Tags:         []string{"atlas", "dead-code", "maintainability"},
	},
	{
		ID:               RuleDiagnosis,
		Name:             "Diagnosis",
		ShortDescription: "A symbol atlas ranks as a likely source of the reported symptom",
		FullDescription: "atlas matched a symptom string (an error message, a log line, a failing test's " +
			"output) back to this symbol by body text and graph centrality. Confidence is a ranking " +
			"signal, not a probability — the top few candidates are where to start looking.",
		HelpURI:      helpBase + "#atlasdiagnosis",
		DefaultLevel: SeverityNote,
		Tags:         []string{"atlas", "triage"},
	},
}

// byID indexes the catalog for LookupRule. Built once; the catalog is static.
var byID = func() map[string]Rule {
	m := make(map[string]Rule, len(catalog))
	for _, r := range catalog {
		m[r.ID] = r
	}
	return m
}()

// LookupRule returns the catalog entry for an id.
func LookupRule(id string) (Rule, bool) {
	r, ok := byID[id]
	return r, ok
}

// Rules returns the full catalog ordered by id, so callers that render it
// (docs generation, the SARIF driver block) get a stable list.
func Rules() []Rule {
	out := make([]Rule, len(catalog))
	copy(out, catalog)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
