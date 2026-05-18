package diagnose

import (
	"fmt"
	"strings"

	"github.com/sosalejandro/atlas/packages/store"
)

// scoringWeights are the linear-combination coefficients. Names are
// intentionally explicit (no magic numbers in the scorer body). Tuning
// these is a single-file change; the scorer itself stays declarative.
//
// Constraints the weights satisfy:
//
//  1. wholeMatchWeight  > tokenMatchWeight — an exact substring is a
//     stronger signal than a token match.
//  2. wholeMatchWeight + tokenMatchWeight + centralityWeight ≤ 1.0 — the
//     base score, before rule kind-bonus, never exceeds 1.0 so the final
//     Confidence stays in a reasonable range.
//  3. kindBonusMax is added on top and capped via min(...) so the final
//     Confidence never exceeds 1.0 — see scoreCandidate.
//
// These were picked empirically: a "panic: nil pointer" symptom against a
// project with one handler that emits exactly that string and many
// callers should score ~0.85, while a random utility func with one token
// in common should score < 0.3.
const (
	wholeMatchWeight = 0.55
	tokenMatchWeight = 0.25
	centralityWeight = 0.20
	kindBonusMax     = 0.15
)

// candidate is the scorer's working unit — one symbol + the precomputed
// signals that go into its score.
type candidate struct {
	row       store.SymbolRow
	bodyHits  int     // number of whole-symptom matches in body
	tokenHits int     // number of token matches in body
	bodyLen   int     // body length in bytes (used to normalise hit counts)
	callers   int     // count of incoming edges (graph centrality)
	score     float64 // final Confidence after combining all signals
	reasons   []string
}

// scoreCandidates produces ranked Match results for the given candidates.
// Inputs:
//
//   - cs          — populated with bodyHits, tokenHits, callers
//   - rules       — matched SymptomRules from matchRules; cs are bonused
//     when their kind appears in any rule's CheckOrder
//   - maxCallers  — denominator for centrality normalisation; pass the
//     project-wide maximum incoming-edge count
//
// The caller owns sorting by Confidence — scoreCandidates only assigns
// the score and the Reason text.
func scoreCandidates(cs []*candidate, rules []matchedRule, maxCallers int) {
	for _, c := range cs {
		c.score = scoreOne(c, rules, maxCallers)
	}
}

// scoreOne is the per-candidate scoring function — pulled out so unit
// tests can exercise the math without spinning up a Store.
//
// Result is a float in [0, 1] (clamped). The component breakdown is
// stashed in c.reasons in human-readable form so Match.Reason can echo
// "matched whole symptom 3x; high callers; +handler kind bonus".
func scoreOne(c *candidate, rules []matchedRule, maxCallers int) float64 {
	c.reasons = c.reasons[:0]

	// 1. Whole-symptom body match — saturating so 100 hits doesn't
	//    swamp the centrality and kind signals. log1p-ish via a simple
	//    bounded ratio: min(1, hits/3).
	whole := 0.0
	if c.bodyHits > 0 {
		whole = float64(c.bodyHits) / 3.0
		if whole > 1.0 {
			whole = 1.0
		}
		c.reasons = append(c.reasons,
			fmt.Sprintf("matched whole symptom %dx in body", c.bodyHits))
	}

	// 2. Token-level match — same saturation curve, denominator larger
	//    because token matches are individually weaker signals. A token
	//    saturation at 6 keeps "every paragraph has one of these words"
	//    from dominating.
	tok := 0.0
	if c.tokenHits > 0 {
		tok = float64(c.tokenHits) / 6.0
		if tok > 1.0 {
			tok = 1.0
		}
		c.reasons = append(c.reasons,
			fmt.Sprintf("matched %d symptom tokens", c.tokenHits))
	}

	// 3. Graph centrality — normalised against the project-wide max so
	//    a project with one super-hub doesn't make every helper look
	//    insignificant on a smaller project. maxCallers can be 0 when
	//    the graph is empty; guard against div-by-zero.
	cent := 0.0
	if maxCallers > 0 && c.callers > 0 {
		cent = float64(c.callers) / float64(maxCallers)
		if cent > 1.0 {
			cent = 1.0
		}
		if c.callers >= 3 {
			c.reasons = append(c.reasons,
				fmt.Sprintf("called from %d sites", c.callers))
		}
	}

	base := wholeMatchWeight*whole + tokenMatchWeight*tok + centralityWeight*cent
	bonus := kindBonus(c, rules)

	final := base + bonus
	if final > 1.0 {
		final = 1.0
	}
	if final < 0 {
		final = 0
	}
	return final
}

// kindBonus computes the best kind-bonus across matched rules and appends
// a reason line for the winning rule. Extracted from scoreOne so each
// function stays under the funlen floor and so unit tests can target
// the bonus calculation in isolation.
//
// Kind bonus rule:
//   - For each matched rule, walk its CheckOrder; the first kind hint
//     that matches the candidate contributes kindBonusMax * rule.Confidence
//     scaled by 1/(idx+1).
//   - Across rules, the maximum candidate-bonus wins.
//   - At most one "layer hint" reason is recorded — the one tied to
//     the winning bonus.
func kindBonus(c *candidate, rules []matchedRule) float64 {
	bonus := 0.0
	winningReason := ""
	for _, mr := range rules {
		for idx, k := range mr.rule.CheckOrder {
			if !kindMatches(string(c.row.Kind), string(c.row.QualifiedName), k) {
				continue
			}
			// position 0 → full bonus; position N-1 → 1/N
			posScale := 1.0 / float64(idx+1)
			candidateBonus := kindBonusMax * mr.rule.Confidence * posScale
			if candidateBonus > bonus {
				bonus = candidateBonus
				winningReason = fmt.Sprintf(
					"layer hint %q matched (kind=%s, position=%d, rule confidence=%.2f)",
					mr.rule.Layer, k, idx, mr.rule.Confidence)
			}
			break // one bonus per rule
		}
	}
	if winningReason != "" {
		c.reasons = append(c.reasons, winningReason)
	}
	return bonus
}

// kindMatches decides whether a stored symbol's kind + qualified name
// satisfies a rule's CheckOrder hint.
//
// The rule grammar uses the broader testreg-era kind taxonomy
// ("handler", "service", "repository", …) but the SQLite schema only
// persists the narrowed CHECK-constraint set ("func", "method", "type",
// "interface", "var", "const") — see store.normalizeKind. So
// equality-on-Kind alone would never fire for stored rows.
//
// Resolution: when the stored Kind doesn't directly match the rule kind,
// fall back to a *qualified-name substring hint* — handlers usually end
// in "Handler", repositories live in /repo*, etc. False positives here
// only contribute a small bonus; false negatives lose the rule's domain
// knowledge entirely. The asymmetry argues for permissive matching.
//
// kindMatches is a thin façade that lowercases its inputs once and
// dispatches to matchKindHint. Kept separate so tests can exercise the
// pure-string matcher without sprinkling ToLower across every test case.
func kindMatches(storedKind, qualifiedName, ruleKind string) bool {
	return matchKindHint(
		strings.ToLower(storedKind),
		strings.ToLower(qualifiedName),
		strings.ToLower(ruleKind),
	)
}

// matchKindHint is the pure substring/keyword check, separated from
// kindMatches so the conversion + lowercase work happens once per call.
func matchKindHint(storedLower, nameLower, ruleKind string) bool {
	switch ruleKind {
	case "handler":
		return storedLower == "handler" ||
			strings.Contains(nameLower, "handler")
	case "service":
		return storedLower == "service" ||
			strings.Contains(nameLower, "service")
	case "repository":
		return storedLower == "repository" ||
			strings.Contains(nameLower, "repo")
	case "query":
		return storedLower == "query" ||
			strings.Contains(nameLower, "query")
	case "component":
		return storedLower == "component" ||
			strings.Contains(nameLower, "component")
	case "hook":
		return storedLower == "hook" ||
			strings.Contains(nameLower, "hook")
	case "endpoint":
		return storedLower == "endpoint" ||
			strings.Contains(nameLower, "route") ||
			strings.Contains(nameLower, "endpoint")
	case "external":
		return storedLower == "external" ||
			strings.Contains(nameLower, "client") ||
			strings.Contains(nameLower, "external") ||
			strings.Contains(nameLower, "provider")
	}
	return false
}

// reasonText collapses a candidate's reasons into a single human-readable
// string for Match.Reason. Empty reasons collapse to a generic stub so
// the field is never empty (callers can rely on a non-empty Reason in
// renderers/logs).
func reasonText(c *candidate) string {
	if len(c.reasons) == 0 {
		return "weak match — no strong signals"
	}
	return strings.Join(c.reasons, "; ")
}
