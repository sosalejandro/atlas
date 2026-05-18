package diagnose

import (
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

func TestKindMatches_DirectKindEquality(t *testing.T) {
	t.Parallel()

	if !kindMatches("handler", "pkg.LoginHandler", "handler") {
		t.Fatal("expected direct kind=\"handler\" to match rule \"handler\"")
	}
	if kindMatches("type", "pkg.Foo", "handler") {
		t.Fatal("type kind should not match handler rule")
	}
}

func TestKindMatches_QualifiedNameSubstring(t *testing.T) {
	t.Parallel()

	// Stored kind is "func" (normalised) but the qualified name ends
	// in "Handler" — the rule should still fire on the substring hint.
	if !kindMatches("func", "pkg.LoginHandler", "handler") {
		t.Fatal("expected func+LoginHandler to match handler rule via name substring")
	}
	if !kindMatches("method", "pkg.UserRepo.FindByEmail", "repository") {
		t.Fatal("expected method+UserRepo to match repository rule via name substring")
	}
	if kindMatches("func", "pkg.Foo", "repository") {
		t.Fatal("plain func+Foo should not match repository rule")
	}
}

func TestScoreOne_NoSignals(t *testing.T) {
	t.Parallel()

	c := &candidate{row: store.SymbolRow{QualifiedName: "pkg.Foo", Kind: shared.KindFunc}}
	got := scoreOne(c, nil, 0)
	if got != 0 {
		t.Fatalf("zero-signal candidate should score 0; got %f", got)
	}
}

func TestScoreOne_WholeMatchBeatsTokenMatch(t *testing.T) {
	t.Parallel()

	whole := &candidate{
		row:      store.SymbolRow{QualifiedName: "pkg.A", Kind: shared.KindFunc},
		bodyHits: 1,
	}
	tok := &candidate{
		row:       store.SymbolRow{QualifiedName: "pkg.B", Kind: shared.KindFunc},
		tokenHits: 1,
	}
	if scoreOne(whole, nil, 0) <= scoreOne(tok, nil, 0) {
		t.Fatal("a single whole-symptom match should outscore a single token match")
	}
}

func TestScoreOne_CentralityBoost(t *testing.T) {
	t.Parallel()

	low := &candidate{
		row:      store.SymbolRow{QualifiedName: "pkg.A", Kind: shared.KindFunc},
		bodyHits: 1,
		callers:  0,
	}
	high := &candidate{
		row:      store.SymbolRow{QualifiedName: "pkg.B", Kind: shared.KindFunc},
		bodyHits: 1,
		callers:  5,
	}
	if scoreOne(high, nil, 5) <= scoreOne(low, nil, 5) {
		t.Fatal("higher callers should produce a higher score with equal body signal")
	}
}

func TestScoreOne_KindBonusFromMatchedRule(t *testing.T) {
	t.Parallel()

	c := &candidate{
		row:      store.SymbolRow{QualifiedName: "pkg.LoginHandler", Kind: shared.KindFunc},
		bodyHits: 1,
	}
	rules := []matchedRule{
		{rule: SymptomRule{
			Pattern:    "irrelevant",
			Layer:      "backend-auth",
			CheckOrder: []string{"handler", "service"},
			Confidence: 0.9,
		}},
	}
	baseline := scoreOne(c, nil, 0)
	c2 := &candidate{
		row:      store.SymbolRow{QualifiedName: "pkg.LoginHandler", Kind: shared.KindFunc},
		bodyHits: 1,
	}
	bonused := scoreOne(c2, rules, 0)

	if bonused <= baseline {
		t.Fatalf("kind-bonused candidate should outscore the unbonused one; got %.3f vs %.3f",
			bonused, baseline)
	}
	if !strings.Contains(reasonText(c2), "layer hint") {
		t.Fatalf("reason text should mention the layer hint; got %q", reasonText(c2))
	}
}

func TestScoreOne_CapsAt1(t *testing.T) {
	t.Parallel()

	c := &candidate{
		row:       store.SymbolRow{QualifiedName: "pkg.LoginHandler", Kind: shared.KindFunc},
		bodyHits:  100,
		tokenHits: 100,
		callers:   100,
	}
	rules := []matchedRule{
		{rule: SymptomRule{
			Pattern:    "irrelevant",
			Layer:      "backend-auth",
			CheckOrder: []string{"handler"},
			Confidence: 1.0,
		}},
	}
	got := scoreOne(c, rules, 100)
	if got > 1.0 {
		t.Fatalf("score should be capped at 1.0; got %.3f", got)
	}
}

func TestReasonText_NeverEmpty(t *testing.T) {
	t.Parallel()

	c := &candidate{row: store.SymbolRow{QualifiedName: "pkg.A"}}
	if reasonText(c) == "" {
		t.Fatal("reasonText must never return empty string")
	}
}
