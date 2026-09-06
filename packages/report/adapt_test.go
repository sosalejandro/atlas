package report_test

import (
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/audit"
	"github.com/sosalejandro/atlas/packages/diagnose"
	"github.com/sosalejandro/atlas/packages/report"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

func TestFromAudit_SeverityFollowsTheScoreBands(t *testing.T) {
	healths := []audit.FeatureHealth{
		{FeatureID: "billing.invoice", Score: 12, Components: map[string]float64{audit.SignalCoverage: 4}, SampledAt: time.Unix(0, 0)},
		{FeatureID: "auth.login", Score: 55, Components: map[string]float64{audit.SignalCoverage: 60}},
		{FeatureID: "search.index", Score: 92, Components: map[string]float64{audit.SignalCoverage: 95}},
	}
	anchors := map[shared.FeatureID]report.Anchor{
		"billing.invoice": {Path: "internal/billing/invoice.go", Line: 42, EndLine: 87},
		"auth.login":      {Path: "internal/auth/login.go", Line: 10},
		"search.index":    {Path: "internal/search/index.go", Line: 3},
	}

	got := report.FromAudit(healths, anchors, report.AuditThresholds{ErrorBelow: 30, WarnBelow: 70})
	if len(got) != 2 {
		t.Fatalf("want 2 findings (the healthy feature is not a finding), got %d: %+v", len(got), got)
	}
	if got[0].Severity != report.SeverityError {
		t.Errorf("score 12 under ErrorBelow 30 should be an error, got %q", got[0].Severity)
	}
	if got[1].Severity != report.SeverityWarning {
		t.Errorf("score 55 under WarnBelow 70 should be a warning, got %q", got[1].Severity)
	}
	if got[0].RuleID != report.RuleFeatureUncovered {
		t.Errorf("rule = %q, want %q", got[0].RuleID, report.RuleFeatureUncovered)
	}
	if got[0].EndLine != 87 {
		t.Errorf("anchor end line lost: %+v", got[0])
	}
}

// TestFromAudit_UnanchoredFeaturesAreNotInvented: a feature with no linked
// symbol has no file to annotate. Emitting it at some default position would
// hang a warning on an unrelated line, so it is skipped here and reported by
// the caller as an unanchored count instead.
func TestFromAudit_UnanchoredFeaturesAreNotInvented(t *testing.T) {
	healths := []audit.FeatureHealth{{FeatureID: "ghost.feature", Score: 0}}
	got := report.FromAudit(healths, nil, report.AuditThresholds{WarnBelow: 70})
	if len(got) != 0 {
		t.Fatalf("want no findings for an unanchored feature, got %+v", got)
	}
}

func TestFromCoverageGaps_AnchorsAtTheTopOfTheFile(t *testing.T) {
	gaps := []store.CoverageGap{
		{Path: "internal/worker/queue.go", Stmts: 118, Reason: "no-indexed-symbol"},
		{Path: "internal/worker/tiny.go", Stmts: 2, Reason: "no-indexed-symbol"},
	}
	got := report.FromCoverageGaps(gaps, 10)
	if len(got) != 1 {
		t.Fatalf("MinStmts should drop the 2-statement gap, got %+v", got)
	}
	f := got[0]
	if f.Line != 1 {
		t.Errorf("a whole-file gap has no line; want line 1, got %d", f.Line)
	}
	if f.RuleID != report.RuleCoverageUnattributed || f.Severity != report.SeverityNote {
		t.Errorf("unexpected rule/severity: %+v", f)
	}
	// The gap's identity must not fold in the statement count: the same
	// blind spot growing from 118 to 130 statements is the same finding,
	// and re-fingerprinting it would re-report it on every push.
	bigger := report.FromCoverageGaps([]store.CoverageGap{
		{Path: "internal/worker/queue.go", Stmts: 130, Reason: "no-indexed-symbol"},
	}, 10)
	if got[0].Fingerprint() != bigger[0].Fingerprint() {
		t.Error("gap fingerprint moved with the statement count; every push would re-report it")
	}
}

func TestFromDeadCode_UsesSymbolSpan(t *testing.T) {
	end := 40
	got := report.FromDeadCode([]store.DeadCodeCandidate{{
		Symbol: store.SymbolRow{
			QualifiedName: "internal/legacy.Shim",
			Kind:          shared.KindFunc,
			FilePath:      "internal/legacy/shim.go",
			Line:          7,
			EndLine:       &end,
		},
		EdgeKind: store.EdgeKindImport,
	}})
	if len(got) != 1 {
		t.Fatalf("want 1 finding, got %d", len(got))
	}
	if got[0].Line != 7 || got[0].EndLine != 40 {
		t.Errorf("span = %d..%d, want 7..40", got[0].Line, got[0].EndLine)
	}
	if got[0].RuleID != report.RuleDeadCode {
		t.Errorf("rule = %q", got[0].RuleID)
	}
}

func TestFromDiagnose_HonoursTheConfidenceFloor(t *testing.T) {
	matches := []diagnose.Match{
		{
			Symbol:     shared.Symbol{ID: "internal/api.Handler", Position: shared.FilePosition{Path: "internal/api/handler.go", Line: 20}},
			Confidence: 0.81,
			Reason:     "matched whole symptom 2x in body",
		},
		{
			Symbol:     shared.Symbol{ID: "internal/api.Noise", Position: shared.FilePosition{Path: "internal/api/noise.go", Line: 5}},
			Confidence: 0.10,
			Reason:     "matched 1 symptom token",
		},
	}
	got := report.FromDiagnose(matches, 0.5)
	if len(got) != 1 {
		t.Fatalf("confidence floor should drop the 0.10 match, got %+v", got)
	}
	if got[0].RuleID != report.RuleDiagnosis {
		t.Errorf("rule = %q, want %q", got[0].RuleID, report.RuleDiagnosis)
	}
	// Confidence must stay out of the identity: the score wobbles between
	// runs as the corpus grows, and a wobbling fingerprint defeats dedupe.
	shifted := matches[:1]
	shifted[0].Confidence = 0.93
	again := report.FromDiagnose(shifted, 0.5)
	if got[0].Fingerprint() != again[0].Fingerprint() {
		t.Error("diagnosis fingerprint moved with the confidence score")
	}
}

// producedRules is every rule id an adapter in this package can actually emit.
// One entry per adapter; grep the adapter to check it before adding one.
var producedRules = []string{
	report.RuleFeatureUncovered,     // FromAudit
	report.RuleCoverageUnattributed, // FromCoverageGaps
	report.RuleDeadCode,             // FromDeadCode
	report.RuleDiagnosis,            // FromDiagnose
}

// TestRules_CatalogCoversEveryAdapter keeps the catalog and the adapters from
// drifting apart: a rule an adapter emits but the catalog omits renders a
// SARIF file GitHub silently empties.
func TestRules_CatalogCoversEveryAdapter(t *testing.T) {
	for _, id := range producedRules {
		r, ok := report.LookupRule(id)
		if !ok {
			t.Errorf("rule %q is not in the catalog", id)
			continue
		}
		if r.Name == "" || r.ShortDescription == "" || r.FullDescription == "" {
			t.Errorf("rule %q has empty metadata: %+v", id, r)
		}
	}
}

// TestRules_CatalogHasNoRuleWithoutAProducer is the other direction, and it is
// the one that rots quietly. A catalogued-and-documented rule nothing can emit
// invites a team to write a gate against it; the gate then passes forever,
// which is indistinguishable from the rule never finding anything.
func TestRules_CatalogHasNoRuleWithoutAProducer(t *testing.T) {
	produced := make(map[string]bool, len(producedRules))
	for _, id := range producedRules {
		produced[id] = true
	}
	for _, r := range report.Rules() {
		if !produced[r.ID] {
			t.Errorf("catalog rule %q has no producer in this package; either add the adapter "+
				"or drop the rule (and its docs section) — a rule that can never fire is a "+
				"gate that can never fail", r.ID)
		}
	}
}
