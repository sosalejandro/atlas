package report

import "testing"

// The security-severity plumbing has no rule behind it today: every rule atlas
// ships is hygiene, and inventing a CVSS number for a dead-code candidate would
// sort it above real vulnerabilities. The mechanism still has to work the day a
// risk-bearing rule is added, and a path with no rule exercising it is exactly
// the path that quietly stops working — so it is covered here, in-package,
// against a synthetic Rule rather than by planting a fake one in the catalog.

func TestRuleProperties_CarriesSecuritySeverityAndTags(t *testing.T) {
	props := ruleProperties(Rule{
		ID:               "atlas/example",
		SecuritySeverity: "5.0",
		Tags:             []string{"atlas", "example"},
	})
	if got, _ := props["security-severity"].(string); got != "5.0" {
		t.Errorf("security-severity = %v, want 5.0", props["security-severity"])
	}
	if tags, _ := props["tags"].([]string); len(tags) != 2 {
		t.Errorf("tags = %v, want the rule's two tags", props["tags"])
	}
}

// A rule with nothing to say gets no properties bag at all, rather than an
// empty object cluttering every rule in the golden file.
func TestRuleProperties_NilWhenThereIsNothingToSay(t *testing.T) {
	if props := ruleProperties(Rule{ID: "atlas/example"}); props != nil {
		t.Errorf("properties = %v, want nil", props)
	}
}

// sarifResultFor copies the RULE's security-severity onto each result, because
// GitHub sorts the alert list by the per-result value and an alert without one
// sinks below every scored alert regardless of its level.
func TestSARIFResultFor_LevelIsTheFindingAndSecuritySeverityIsTheRule(t *testing.T) {
	rule := Rule{
		ID:               "atlas/test-only-risk",
		Name:             "TestOnlyRisk",
		ShortDescription: "synthetic",
		FullDescription:  "synthetic",
		DefaultLevel:     SeverityNote,
		SecuritySeverity: "7.5",
	}
	res := sarifResultFor(Finding{
		RuleID:   rule.ID,
		Severity: SeverityError, // deliberately NOT the rule default
		Path:     "pkg/a.go",
		Line:     3,
	}, rule, 0)

	if res.Level != string(SeverityError) {
		t.Errorf("level = %q, want the finding's severity %q (not the rule default %q)",
			res.Level, SeverityError, SeverityNote)
	}
	if got, _ := res.Properties["security-severity"].(string); got != "7.5" {
		t.Errorf("security-severity = %v, want the rule's 7.5", res.Properties["security-severity"])
	}
}
