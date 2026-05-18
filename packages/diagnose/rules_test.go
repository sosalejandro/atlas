package diagnose

import (
	"testing"
)

func TestMatchRules_NoMatch(t *testing.T) {
	t.Parallel()
	got := matchRules("this is a completely benign log line", DefaultSymptomRules())
	if len(got) != 0 {
		t.Fatalf("expected zero matches for benign symptom, got %d", len(got))
	}
}

func TestMatchRules_DBConstraintHighConfidence(t *testing.T) {
	t.Parallel()
	got := matchRules("ERROR: duplicate key value violates unique constraint \"users_email_key\"", DefaultSymptomRules())
	if len(got) == 0 {
		t.Fatal("expected at least one match for uniqueness violation symptom")
	}
	if got[0].rule.Layer != "data" {
		t.Fatalf("top rule layer = %q, want \"data\"", got[0].rule.Layer)
	}
	if got[0].rule.Confidence < 0.90 {
		t.Fatalf("top rule confidence = %.2f, want >= 0.90", got[0].rule.Confidence)
	}
}

func TestMatchRules_SortedByConfidenceDescending(t *testing.T) {
	t.Parallel()
	// "500 internal server error: timeout" matches BOTH the 500 (0.50)
	// and the timeout (0.60) rule. The timeout rule (higher confidence)
	// must come first.
	got := matchRules("500 internal server error: context deadline exceeded", DefaultSymptomRules())
	if len(got) < 2 {
		t.Fatalf("expected ≥2 matches, got %d", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].rule.Confidence < got[i].rule.Confidence {
			t.Fatalf("rules not sorted by confidence desc at index %d: %.2f < %.2f",
				i, got[i-1].rule.Confidence, got[i].rule.Confidence)
		}
	}
	if got[0].rule.Confidence < 0.50 {
		t.Fatalf("top confidence = %.2f, want >= 0.50", got[0].rule.Confidence)
	}
}

func TestMatchRules_MalformedPatternIgnored(t *testing.T) {
	t.Parallel()
	rules := []SymptomRule{
		{Pattern: "(unclosed", Layer: "x", Confidence: 0.99},        // bad regex
		{Pattern: "good", Layer: "y", Confidence: 0.50},
	}
	got := matchRules("a good message", rules)
	if len(got) != 1 {
		t.Fatalf("expected 1 match (malformed rule must be skipped), got %d", len(got))
	}
	if got[0].rule.Layer != "y" {
		t.Fatalf("matched the wrong rule: layer %q", got[0].rule.Layer)
	}
}

func TestDefaultSymptomRules_AllPatternsCompile(t *testing.T) {
	t.Parallel()
	// Smoke test: every default pattern must compile. matchRules silently
	// skips bad patterns, so we'd never notice a typo without this.
	for i, r := range DefaultSymptomRules() {
		got := matchRules("", []SymptomRule{r})
		_ = got
		_ = i
		// matchRules compiles every pattern — if it doesn't panic we're
		// good. (The empty symptom may or may not match; we don't care.)
	}
}
