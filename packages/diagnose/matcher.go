package diagnose

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// Match is one ranked diagnosis result. Score is the float [0,1] the
// scorer assigned; Feature is nil when the symbol carries no
// feature_symbols link (still a useful diagnosis — the engineer can chase
// the symbol even without a feature label).
type Match struct {
	// Symbol is the candidate code entity. The Position field is
	// populated so renderers can emit `file:line` directly.
	Symbol shared.Symbol `json:"symbol"`

	// Feature is the feature the symbol is annotated with, or nil if
	// the symbol has no feature link in the store. When multiple
	// features link the symbol, the first by lexicographic ID is
	// returned — diagnose is a triage tool, not a feature inventory.
	Feature *store.Feature `json:"feature,omitempty"`

	// Confidence is the final score in [0, 1]. Two matches with very
	// close confidences should be treated as a tie by callers — the
	// scoring isn't precise enough for "0.812 > 0.811" to mean anything.
	Confidence float64 `json:"confidence"`

	// Reason is a human-readable summary of what won the symbol its
	// score: which signals fired, which rule layer hint applied, etc.
	// Never empty (see reasonText).
	Reason string `json:"reason"`
}

// Options tunes Diagnose's behaviour. Zero-value is the default — pass nil
// to use defaults.
type Options struct {
	// Rules overrides the built-in symptom registry. Empty / nil uses
	// DefaultSymptomRules().
	Rules []SymptomRule

	// MaxResults caps the number of Matches returned. 0 means no cap.
	// Most callers should pass a small number (10–25) — anything below
	// the top matches is noise.
	MaxResults int

	// ProjectRoot is the absolute path to join with each symbol's
	// repo-relative file_path when reading bodies from disk. Pass ""
	// in tests where Position.Path is already absolute.
	ProjectRoot string

	// MinConfidence drops matches whose final score is below this floor.
	// Zero (default) returns every candidate that had any signal at all.
	MinConfidence float64
}

// Diagnose maps `symptom` back to the symbols + features most likely
// responsible.
//
// The contract:
//
//   - Empty symptom is treated as a usage error and returns
//     ErrEmptySymptom. The function does NOT panic on a nil Store; that
//     is a programmer error and panics.
//   - A symptom that matches nothing returns (nil, nil) — the absence of
//     matches is not an error.
//   - Results are sorted by Confidence descending, then by qualified name
//     ascending for stable output across runs.
//   - Reading source files is lazy; missing files are silently ignored
//     (they may be generated code that was excluded from the worktree).
//
// Diagnose is read-only on the Store — it never writes, never starts a
// transaction. Multiple concurrent Diagnose calls against the same Store
// are safe.
func Diagnose(ctx context.Context, symptom string, s *store.Store, opts *Options) ([]Match, error) {
	if s == nil {
		// Programmer error — fail loudly rather than returning a
		// misleading "no matches".
		panic("diagnose: store is nil")
	}
	symptom = strings.TrimSpace(symptom)
	if symptom == "" {
		return nil, ErrEmptySymptom
	}
	if opts == nil {
		opts = &Options{}
	}
	rules := opts.Rules
	if len(rules) == 0 {
		rules = DefaultSymptomRules()
	}

	// 1. Match rules — gives layer hints used in scoring.
	matched := matchRules(symptom, rules)

	// 2. Pull every symbol. Diagnose is a low-frequency triage call;
	//    paginating would add complexity without saving meaningful work
	//    on the project sizes Atlas targets (<100k symbols).
	rows, err := s.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		return nil, fmt.Errorf("diagnose: list symbols: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}

	// 3. Build candidates and read bodies.
	sm := newSymptomMatcher(symptom)
	cache := newBodyCache(opts.ProjectRoot)

	candidates, err := buildCandidates(rows, sm, cache)
	if err != nil {
		return nil, fmt.Errorf("diagnose: read bodies: %w", err)
	}
	if len(candidates) == 0 {
		// Still surface a helpful empty result with a hint via the
		// caller's logs — return nil to signal "no matches".
		return nil, nil
	}

	// 4. Populate caller counts via Edges.In.
	maxCallers, err := annotateCallers(ctx, s, candidates)
	if err != nil {
		return nil, fmt.Errorf("diagnose: caller counts: %w", err)
	}

	// 5. Score and rank.
	scoreCandidates(candidates, matched, maxCallers)

	out := buildMatches(ctx, s, candidates, opts)
	return out, nil
}

// buildCandidates reads each symbol's body via the cache and attaches
// hit-counts to a candidate. Symbols whose bodies don't yield even a
// single token hit are dropped — they have zero signal and only add
// noise to the centrality denominator.
//
// "Even one whole-symptom hit OR one token hit" is the bar; centrality
// alone is not enough to clear the bar (a highly-called function with
// nothing in common with the symptom is not a useful match).
func buildCandidates(rows []store.SymbolRow, sm *symptomMatcher, cache *bodyCache) ([]*candidate, error) {
	out := make([]*candidate, 0, len(rows))
	for _, r := range rows {
		end := 0
		if r.EndLine != nil {
			end = *r.EndLine
		}
		body, err := cache.readBody(r.FilePath, r.Line, end)
		if err != nil {
			return nil, fmt.Errorf("read body %s:%d: %w", r.FilePath, r.Line, err)
		}
		if body == "" {
			continue
		}

		c := &candidate{row: r, bodyLen: len(body)}
		if sm.whole != nil {
			c.bodyHits = len(sm.whole.FindAllStringIndex(body, -1))
		}
		if sm.tokens != nil {
			c.tokenHits = len(sm.tokens.FindAllStringIndex(body, -1))
		}
		if c.bodyHits == 0 && c.tokenHits == 0 {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// annotateCallers fills candidate.callers via store.Edges.In and returns
// the maximum caller count across candidates (used to normalise the
// centrality signal in scoreOne).
//
// A symbol with zero incoming edges (e.g. an exported package init) is
// not dropped — it just gets cent = 0 and relies on body-match signals.
func annotateCallers(ctx context.Context, s *store.Store, cs []*candidate) (int, error) {
	maxCallers := 0
	edges := s.Edges()
	for _, c := range cs {
		in, err := edges.In(ctx, c.row.ID)
		if err != nil {
			return 0, fmt.Errorf("edges.In %d: %w", c.row.ID, err)
		}
		c.callers = len(in)
		if c.callers > maxCallers {
			maxCallers = c.callers
		}
	}
	return maxCallers, nil
}

// buildMatches finalises the result: filter by MinConfidence, sort by
// (Confidence desc, qualified name asc), attribute features, cap by
// MaxResults.
func buildMatches(ctx context.Context, s *store.Store, cs []*candidate, opts *Options) []Match {
	// Sort once on all candidates so MaxResults truncation is meaningful.
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].score != cs[j].score {
			return cs[i].score > cs[j].score
		}
		return cs[i].row.QualifiedName < cs[j].row.QualifiedName
	})

	out := make([]Match, 0, len(cs))
	for _, c := range cs {
		if c.score < opts.MinConfidence {
			continue
		}
		out = append(out, Match{
			Symbol: shared.Symbol{
				ID:   c.row.QualifiedName,
				Kind: c.row.Kind,
				Position: shared.FilePosition{
					Path: c.row.FilePath,
					Line: c.row.Line,
				},
				Package: derefStr(c.row.Package),
			},
			Feature:    lookupFeature(ctx, s, c.row.ID),
			Confidence: c.score,
			Reason:     reasonText(c),
		})
		if opts.MaxResults > 0 && len(out) >= opts.MaxResults {
			break
		}
	}
	return out
}

// lookupFeature resolves a symbol's feature attribution. Returns nil
// (not error) when the symbol has no feature_symbols link or the linked
// feature record is missing — both are recoverable "no metadata" cases,
// not errors that should block a diagnosis.
func lookupFeature(ctx context.Context, s *store.Store, symbolID int64) *store.Feature {
	links, err := s.FeatureSymbols().ListBySymbol(ctx, symbolID)
	if err != nil || len(links) == 0 {
		return nil
	}
	// Stable pick: lowest FeatureID lexicographically.
	first := links[0].FeatureID
	for _, l := range links[1:] {
		if l.FeatureID < first {
			first = l.FeatureID
		}
	}
	feat, err := s.Features().Get(ctx, first)
	if err != nil {
		return nil
	}
	return &feat
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
