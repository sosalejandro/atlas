package sprintplan

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/sosalejandro/atlas/packages/audit"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// SprintItem is one entry in the prioritised backlog.
//
// Priority is in 0..100; higher = work first. Reasons explains the score in
// plain language. Cost is a discrete S/M/L bucket per the spec.
type SprintItem struct {
	FeatureID shared.FeatureID `json:"feature_id"`
	Priority  float64          `json:"priority"`
	Reasons   []string         `json:"reasons,omitempty"`
	Cost      string           `json:"cost"`
}

// CostBucket constants. These are the only valid Cost values.
const (
	CostS = "S"
	CostM = "M"
	CostL = "L"
)

// Options tunes the planner.
//
// Zero values mean "use defaults" per the package doc — the sprint-plan
// formula's 0.6/0.2/0.2 weights, 7-day bug window, 7/90-day freshness span.
type Options struct {
	// ScoreWeight weights the (100 - audit score) term. Default 0.6.
	ScoreWeight float64

	// BugWeight weights the bug_signal term. Default 0.2.
	BugWeight float64

	// RecencyWeight weights the recency_decay term. Default 0.2.
	RecencyWeight float64

	// BugWindow is the time window for "recent failing coverage" used by
	// bug_signal. Default 7 * 24 * time.Hour.
	BugWindow time.Duration

	// RecencyFreshness is the window inside which an annotation site counts
	// as fully-fresh (recency_decay = 100). Default 7 * 24 * time.Hour.
	RecencyFreshness time.Duration

	// RecencyDecay is the window over which recency_decay drops from 100 to
	// 0 linearly, starting at the end of RecencyFreshness. Default
	// 90 * 24 * time.Hour.
	RecencyDecay time.Duration

	// GitBlame is the source for annotation author-dates. Required for
	// recency_decay to be non-zero; tests pass a stub.
	GitBlame audit.GitBlameSource

	// Now overrides time.Now() for determinism. Zero = real time.
	Now func() time.Time
}

// applyDefaults fills zero-value Options fields with the spec defaults.
func (o Options) applyDefaults() Options {
	if o.ScoreWeight == 0 {
		o.ScoreWeight = 0.6
	}
	if o.BugWeight == 0 {
		o.BugWeight = 0.2
	}
	if o.RecencyWeight == 0 {
		o.RecencyWeight = 0.2
	}
	if o.BugWindow <= 0 {
		o.BugWindow = 7 * 24 * time.Hour
	}
	if o.RecencyFreshness <= 0 {
		o.RecencyFreshness = 7 * 24 * time.Hour
	}
	if o.RecencyDecay <= 0 {
		o.RecencyDecay = 90 * 24 * time.Hour
	}
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	return o
}

// Planner is the public API. Rank returns the full backlog; TopN is a
// shortcut.
type Planner interface {
	Rank(ctx context.Context) ([]SprintItem, error)
	TopN(ctx context.Context, n int) ([]SprintItem, error)
}

// New wires the Planner against a Store and an Audit produced by
// packages/audit. The Audit is the source of feature scores; the Store
// supplies linked-symbol counts (for Cost), coverage results (for
// BugSignal), and annotation file/line pairs (for RecencyDecay).
func New(s *store.Store, a audit.Audit, opts Options) Planner {
	if s == nil || a == nil {
		return &nilPlanner{}
	}
	return &planner{store: s, audit: a, opts: opts.applyDefaults()}
}

// nilPlanner is the failing-fast variant when callers forget to wire deps.
type nilPlanner struct{}

var errNilStore = errors.New("sprintplan: store or audit is nil")

func (*nilPlanner) Rank(context.Context) ([]SprintItem, error) {
	return nil, errNilStore
}
func (*nilPlanner) TopN(context.Context, int) ([]SprintItem, error) {
	return nil, errNilStore
}

type planner struct {
	store *store.Store
	audit audit.Audit
	opts  Options

	// symbolCache mirrors audit's lazy cache. The recency-decay routine
	// looks up file_path by symbol id; without this cache every call would
	// pay an O(N) Symbols.List.
	symbolCache map[int64]string
}

// Rank returns the prioritised backlog for every feature in the store.
//
// Stability: results are deterministic — features with identical priority
// scores break ties by FeatureID. The audit ScoreAll output already orders
// worst-first by Score, but Priority is a different ordering (higher first)
// so we re-sort.
//
// Empty input → empty (non-nil) output; no error.
func (p *planner) Rank(ctx context.Context) ([]SprintItem, error) {
	healths, err := p.audit.ScoreAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("sprintplan Rank: %w", err)
	}
	if len(healths) == 0 {
		return []SprintItem{}, nil
	}

	now := p.opts.Now()
	// Cache the latest coverage_run id ONCE for the whole pass — bug_signal
	// uses it per feature, but the lookup is invariant.
	latestRun, hasCov, err := p.latestRun(ctx)
	if err != nil {
		return nil, fmt.Errorf("sprintplan Rank: %w", err)
	}

	out := make([]SprintItem, 0, len(healths))
	for _, h := range healths {
		item, err := p.itemFor(ctx, h, latestRun, hasCov, now)
		if err != nil {
			return nil, fmt.Errorf("sprintplan Rank %q: %w", h.FeatureID, err)
		}
		out = append(out, item)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].FeatureID < out[j].FeatureID
	})
	return out, nil
}

// TopN returns the top-n items from Rank.
//
// TopN(0) returns an empty slice (no error). TopN(n > len) returns the full
// list. The n bound is interpreted as "at most n" — never an error.
func (p *planner) TopN(ctx context.Context, n int) ([]SprintItem, error) {
	if n == 0 {
		return []SprintItem{}, nil
	}
	all, err := p.Rank(ctx)
	if err != nil {
		return nil, err
	}
	if n >= len(all) {
		return all, nil
	}
	return all[:n], nil
}

// itemFor computes the per-feature SprintItem. The three Priority terms are
// computed independently; the result is bounded to 0..100.
func (p *planner) itemFor(
	ctx context.Context,
	h audit.FeatureHealth,
	latestRun int64,
	hasCov bool,
	now time.Time,
) (SprintItem, error) {
	links, err := p.store.FeatureSymbols().ListByFeature(ctx, h.FeatureID)
	if err != nil {
		return SprintItem{}, fmt.Errorf("list feature_symbols: %w", err)
	}

	// Term 1: (100 - score) * w1
	scoreTerm := (100 - h.Score) * p.opts.ScoreWeight

	// Term 2: bug_signal * w2 (count of failing results in BugWindow, cap at 100)
	bug := 0.0
	if hasCov && len(links) > 0 {
		v, err := p.bugSignal(ctx, h.FeatureID, links, latestRun, now)
		if err != nil {
			return SprintItem{}, fmt.Errorf("bug signal: %w", err)
		}
		bug = v
	}
	bugTerm := bug * p.opts.BugWeight

	// Term 3: recency_decay * w3
	recency, err := p.recencyDecay(ctx, links, now)
	if err != nil {
		return SprintItem{}, fmt.Errorf("recency decay: %w", err)
	}
	recencyTerm := recency * p.opts.RecencyWeight

	priority := scoreTerm + bugTerm + recencyTerm
	if priority < 0 {
		priority = 0
	}
	if priority > 100 {
		priority = 100
	}

	cost := costBucket(len(links))
	reasons := buildReasons(h, bug, recency, cost)

	return SprintItem{
		FeatureID: h.FeatureID,
		Priority:  priority,
		Reasons:   reasons,
		Cost:      cost,
	}, nil
}

// bugSignal returns the count of failing coverage results for this
// feature's symbols in the most-recent run, capped at 100. We use the
// latest run rather than walking ALL runs over the BugWindow — Atlas's
// coverage ingest is "snapshot per run", so the latest run already
// represents "today's failures." Time-windowing across runs would
// double-count the same flaky test.
//
// Future iteration could walk ALL runs whose finished_at is inside
// BugWindow; for Phase 6a we stick with the spec's intent: "this feature's
// tests are failing RIGHT NOW," which the latest run captures exactly.
func (p *planner) bugSignal(
	ctx context.Context,
	_ shared.FeatureID,
	links []store.FeatureSymbolLink,
	latestRun int64,
	now time.Time,
) (float64, error) {
	// Sanity check that the latest run is inside BugWindow — otherwise we
	// can't claim "recent" failures and the signal is zero.
	run, err := p.store.Coverage().GetRun(ctx, latestRun)
	if err != nil {
		return 0, fmt.Errorf("get coverage run %d: %w", latestRun, err)
	}
	if now.Sub(run.FinishedAt) > p.opts.BugWindow {
		return 0, nil
	}

	wanted := make(map[int64]bool, len(links))
	for _, l := range links {
		if l.Role == store.RoleTest {
			continue
		}
		wanted[l.SymbolID] = true
	}
	if len(wanted) == 0 {
		return 0, nil
	}

	results, err := p.store.Coverage().ListResults(ctx, latestRun)
	if err != nil {
		return 0, fmt.Errorf("list coverage results: %w", err)
	}
	fails := 0
	for _, r := range results {
		if r.Status != store.StatusFail {
			continue
		}
		if r.SymbolID != nil && wanted[*r.SymbolID] {
			fails++
		}
	}
	if fails > 100 {
		return 100, nil
	}
	return float64(fails), nil
}

// recencyDecay returns 100 if any annotation in the feature's files was
// touched in the last RecencyFreshness window (per GitBlame), and decays
// linearly to 0 over RecencyDecay starting from the end of the freshness
// window.
//
// Without a wired GitBlame source the function returns 0 (no signal) and
// the priority formula's recency term contributes nothing.
func (p *planner) recencyDecay(
	ctx context.Context,
	links []store.FeatureSymbolLink,
	now time.Time,
) (float64, error) {
	if p.opts.GitBlame == nil || len(links) == 0 {
		return 0, nil
	}

	// Find every annotation site in the same files as the feature's linked
	// symbols. We use the MAX of per-site author-times: "if ANY annotation
	// is fresh, recency reflects that."
	seenFile := make(map[string]bool, len(links))
	for _, l := range links {
		// Re-use the audit-style lookup pattern; we don't have a per-id
		// symbol port so iterate List once. The N here is small.
		// (Cached lookup is a future optimization.)
		path, err := p.symbolFilePath(ctx, l.SymbolID)
		if err != nil {
			continue
		}
		if path != "" {
			seenFile[path] = true
		}
	}

	var latest time.Time
	for f := range seenFile {
		rows, err := p.store.Annotations().ListByFile(ctx, f)
		if err != nil {
			return 0, fmt.Errorf("list annotations %q: %w", f, err)
		}
		for _, r := range rows {
			if r.Kind != shared.AnnFeature && r.Kind != shared.AnnContract {
				continue
			}
			ts, err := p.opts.GitBlame.AuthorDate(ctx, r.FilePath, r.Line)
			if err != nil || ts.IsZero() {
				continue
			}
			if ts.After(latest) {
				latest = ts
			}
		}
	}
	if latest.IsZero() {
		return 0, nil
	}
	return decayCurve(now, latest, p.opts.RecencyFreshness, p.opts.RecencyDecay), nil
}

// decayCurve implements the recency_decay heuristic:
//
//	100              for age <= freshness window
//	linear 100→0     over the (freshness, freshness+decay) range
//	0                for age >= freshness+decay
func decayCurve(now, ts time.Time, fresh, decay time.Duration) float64 {
	age := now.Sub(ts)
	if age <= fresh {
		return 100
	}
	if age >= fresh+decay {
		return 0
	}
	// linear in the decay band
	overage := age - fresh
	ratio := float64(overage) / float64(decay)
	return 100 * (1 - ratio)
}

// symbolFilePath returns the file_path for one symbol surrogate id.
//
// Materialises a per-Planner cache on first call so the recency-decay
// pass across all features pays the Symbols.List cost ONCE, not per
// feature. The cache lives for the planner's lifetime — short-lived
// since Atlas runs as a one-shot CLI.
func (p *planner) symbolFilePath(ctx context.Context, id int64) (string, error) {
	if p.symbolCache == nil {
		rows, err := p.store.Symbols().List(ctx, store.SymbolFilter{})
		if err != nil {
			return "", fmt.Errorf("symbols list: %w", err)
		}
		p.symbolCache = make(map[int64]string, len(rows))
		for _, r := range rows {
			p.symbolCache[r.ID] = r.FilePath
		}
	}
	return p.symbolCache[id], nil
}

// costBucket maps a linked-symbol count to S/M/L per the spec.
//
// S: ≤3 symbols   M: 4–15 symbols   L: >15 symbols
func costBucket(n int) string {
	switch {
	case n <= 3:
		return CostS
	case n <= 15:
		return CostM
	default:
		return CostL
	}
}

// buildReasons assembles the human-readable explanation slice. The output is
// always non-empty: even a perfect-score feature gets one explanatory line.
func buildReasons(h audit.FeatureHealth, bug, recency float64, cost string) []string {
	var out []string
	out = append(out, fmt.Sprintf("Score %.0f (%s), %d linked symbols, cost=%s",
		h.Score, qualScore(h.Score), len(h.Components), cost))
	if bug > 0 {
		out = append(out, fmt.Sprintf("bug signal: %.0f failing results in latest run", bug))
	}
	if recency > 0 {
		out = append(out, fmt.Sprintf("recency decay: %.0f (recent activity boost)", recency))
	}
	// Surface up to two top reasons verbatim — these come straight from
	// audit's per-signal explanation and are already user-readable.
	if len(h.Reasons) > 0 {
		take := h.Reasons
		if len(take) > 2 {
			take = take[:2]
		}
		out = append(out, take...)
	}
	return out
}

// qualScore maps a numeric score to a qualitative bucket.
func qualScore(s float64) string {
	switch {
	case s >= 80:
		return "healthy"
	case s >= 50:
		return "fair"
	case s >= 25:
		return "low"
	default:
		return "critical"
	}
}

// latestRun returns the latest coverage_run id (or (0, false, nil) if none).
func (p *planner) latestRun(ctx context.Context) (int64, bool, error) {
	runs, err := p.store.Coverage().ListRuns(ctx, "")
	if err != nil {
		return 0, false, fmt.Errorf("list coverage runs: %w", err)
	}
	if len(runs) == 0 {
		return 0, false, nil
	}
	var (
		latestID   int64
		latestTime time.Time
	)
	for _, r := range runs {
		if r.FinishedAt.After(latestTime) {
			latestTime = r.FinishedAt
			latestID = r.ID
		}
	}
	return latestID, latestID != 0, nil
}
