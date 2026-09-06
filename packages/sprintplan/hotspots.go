package sprintplan

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/sosalejandro/atlas/packages/audit"
	"github.com/sosalejandro/atlas/packages/churn"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// Hotspot is one entry in the churn x gap ranking.
//
// Score is the product, but Gap, Health and Churn are all carried
// alongside it on purpose: a composite score nobody can decompose is a
// score nobody trusts, and the whole argument for this ranking is that a
// reader can see WHICH of the two factors put an item at the top.
type Hotspot struct {
	FeatureID shared.FeatureID `json:"feature_id"`

	// Score is Gap * Churn.Score / 100, so it stays in 0..100 and is
	// directly comparable to the audit and priority scores elsewhere.
	Score float64 `json:"score"`

	// Health is the audit score; Gap is its deficit, 100 - Health. Both
	// are reported because "62% healthy" and "38 points of gap" are the
	// two ways people actually say it.
	Health float64 `json:"health"`
	Gap    float64 `json:"gap"`

	// Churn is the change-frequency factor, including its own
	// decomposition (hot file, commit count, author count, status).
	Churn churn.FeatureChurn `json:"churn"`

	// Cost is the same S/M/L bucket Rank reports.
	Cost string `json:"cost"`

	// Reasons are the audit's own top explanations, verbatim.
	Reasons []string `json:"reasons,omitempty"`
}

// errNoChurn is returned when Hotspots is called on a planner that was
// never given a churn report. It is an error rather than an empty result
// because "no hotspots" and "we never looked at git" must not be confused.
var errNoChurn = errors.New("sprintplan: Hotspots requires Options.Churn")

// Hotspots ranks every feature by change frequency x health deficit.
//
// The two factors multiply rather than add, which is the point of the
// model: a feature nobody has touched in two years contributes a churn
// factor near zero and drops out of the backlog however broken it is,
// while a merely-mediocre feature that changes weekly rises. Adding the
// terms would keep the dead-and-broken code near the top, which is exactly
// the backlog this ranking exists to replace.
func (p *planner) Hotspots(ctx context.Context) ([]Hotspot, error) {
	if p.opts.Churn == nil {
		return nil, errNoChurn
	}
	healths, err := p.audit.ScoreAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("sprintplan Hotspots: %w", err)
	}

	out := make([]Hotspot, 0, len(healths))
	for _, h := range healths {
		if !shared.IsValidFeatureID(h.FeatureID) {
			continue
		}
		links, err := p.store.FeatureSymbols().ListByFeature(ctx, h.FeatureID)
		if err != nil {
			return nil, fmt.Errorf("sprintplan Hotspots %q: %w", h.FeatureID, err)
		}
		fc, err := p.featureChurn(ctx, links)
		if err != nil {
			return nil, fmt.Errorf("sprintplan Hotspots %q: %w", h.FeatureID, err)
		}
		gap := 100 - h.Score
		out = append(out, Hotspot{
			FeatureID: h.FeatureID,
			Score:     gap * fc.Score / 100,
			Health:    h.Score,
			Gap:       gap,
			Churn:     fc,
			Cost:      costBucket(len(links)),
			Reasons:   topReasons(h),
		})
	}
	sortHotspots(out)
	return out, nil
}

// sortHotspots orders worst-first, breaking ties by feature id so repeated
// runs over unchanged data produce byte-identical output.
func sortHotspots(in []Hotspot) {
	sort.SliceStable(in, func(i, j int) bool {
		if in[i].Score != in[j].Score {
			return in[i].Score > in[j].Score
		}
		return in[i].FeatureID < in[j].FeatureID
	})
}

// topReasons trims the audit's explanations to the two that fit a line.
func topReasons(h audit.FeatureHealth) []string {
	if len(h.Reasons) > 2 {
		return h.Reasons[:2]
	}
	return h.Reasons
}

// featureChurn rolls the churn report up over the feature's files.
//
// Two filters shape the input set, and both are load-bearing:
//
// Test-role files are dropped. A feature whose tests churn weekly while
// its implementation is frozen is a test being stabilised, not a hotspot,
// and counting the churn would put every flaky test at the top of the
// sprint. When a feature has ONLY test links there is nothing else to
// measure, so those files are used rather than reporting a hole.
//
// The paths themselves come from the symbol table, which is the scanner's
// output. Generated files are absent from it because the scanner declined
// them under the issue #96 rules, so their constant churn cannot reach a
// score here — the determination is reused rather than reimplemented.
func (p *planner) featureChurn(ctx context.Context, links []store.FeatureSymbolLink) (churn.FeatureChurn, error) {
	impl, all := make([]string, 0, len(links)), make([]string, 0, len(links))
	seenImpl, seenAll := map[string]bool{}, map[string]bool{}
	for _, l := range links {
		path, err := p.symbolFilePath(ctx, l.SymbolID)
		if err != nil {
			return churn.FeatureChurn{}, err
		}
		if path == "" {
			continue
		}
		if !seenAll[path] {
			seenAll[path] = true
			all = append(all, path)
		}
		if l.Role != store.RoleTest && !seenImpl[path] {
			seenImpl[path] = true
			impl = append(impl, path)
		}
	}
	if len(impl) > 0 {
		return p.opts.Churn.ForFiles(impl), nil
	}
	return p.opts.Churn.ForFiles(all), nil
}
