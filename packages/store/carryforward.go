package store

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store/sqlc"
)

// Carryforward: what a build did not re-measure (issue #136).
//
// A run group unions the runs of one build. When a job inside that build
// fails, times out or is skipped, its results never land, and every symbol
// only that job measures disappears from the frontier. Absence is then read as
// "not part of the picture" rather than as "not measured", so the
// line-weighted score sums a smaller denominator and coverage goes UP because
// testing went DOWN. That is issue #85's failure mode one level higher, and it
// is the shape a broken CI job takes when nobody is watching the job list.
//
// The fix is to fill the hole from the last build that DID measure those
// symbols, marked as carried rather than observed, so that:
//
//   - the denominator is stable across builds, which is what makes two
//     consecutive coverage numbers comparable at all;
//   - a reader can always tell how much of a number is this build's
//     measurement and how much is last build's, because a number assembled
//     from two builds must not present itself as one.
//
// Only GROUPED frontiers carry. A run group is the operator's declaration
// that a set of syncs is one build (`cov sync --run-group`); without it
// "the previous build" is undefined, an ungrouped sync is by construction a
// standalone measurement, and every store ingested before run groups keeps
// scoring exactly as it did.

// The staleness window. These bound how far back a measurement may come from
// and still count as EVIDENCE that the code ran.
//
// DefaultCarryBuilds = 3. The carry exists for the failed-job case, and a
// failed job is resolved in a build or two: the next push, or the retry. Three
// builds spans "it failed, someone retried it, it failed again" without
// spanning a working week. A window of 1 would be too tight to survive the
// retry, and a window of ten is the case the issue names outright -- a result
// from ten builds ago is closer to a lie than to evidence, because ten builds
// is long enough for the code under it to have been rewritten.
//
// DefaultCarryMaxAge = 72h is the backstop for repos that do not build often,
// where "three builds back" can be a month. Three days spans a weekend, which
// is the longest gap a healthy pipeline produces on its own.
//
// Both bounds apply; the tighter one wins. Falling outside them does NOT drop
// the symbol -- see CarryDenominator.
const (
	DefaultCarryBuilds = 3
	DefaultCarryMaxAge = 72 * time.Hour
)

// carryLookbackHorizon bounds the SQL scan, not the policy. Nothing measured
// longer ago than this is looked at at all: the span check would almost
// certainly reject it, its statement count describes code a month of commits
// has moved on from, and scanning the whole history of coverage_results on
// every feature's audit is a cost with no matching benefit.
const carryLookbackHorizon = 30 * 24 * time.Hour

// CarryMode says how much of a carried measurement is being believed.
//
// The distinction is the answer to "does a carried result count as covered for
// a gate?". It does not. A CI gate exists to catch the build that stopped
// measuring, so a carry that satisfies the gate defeats the gate. What a carry
// legitimately does is hold the symbol's PLACE:
//
//   - CarryEvidence: the measurement is recent and the symbol still occupies
//     the span it was measured in, so its covered/total statements stand in
//     for this build's reading of that symbol. The audit scores it; a gate
//     reads the observed-only fraction beside it.
//
//   - CarryDenominator: the measurement is too old, or the symbol has moved,
//     so nothing is credited -- covered is zeroed and the status becomes
//     `fail`. Only the statement TOTAL survives, as the symbol's last known
//     size. This is the conservative reading of "we do not know whether this
//     code ran": it can push a score down, never up, which is the only
//     direction that cannot hide a pipeline that stopped measuring.
//
// The binary (non-statement) coverage model already treats an unmeasured
// linked symbol this way -- it counts in the denominator and not in the
// numerator. CarryDenominator is that same rule extended to the
// statement-weighted model, where an unmeasured symbol otherwise vanishes
// from the sum entirely. That is the actual bug in #136.
type CarryMode string

const (
	CarryEvidence    CarryMode = "evidence"
	CarryDenominator CarryMode = "denominator"
)

// CarryReason records why a carry was downgraded to denominator-only, so the
// downgrade is reportable rather than mysterious.
type CarryReason string

const (
	// CarryFresh: inside the window and span-verified.
	CarryFresh CarryReason = "fresh"
	// CarryStale: outside the build or wall-clock window.
	CarryStale CarryReason = "stale"
	// CarrySpanChanged: the symbol's file/line/end_line differs from the span
	// recorded when it was measured (schema 0017). A symbol whose span changed
	// is not the symbol that was measured.
	CarrySpanChanged CarryReason = "span-changed"
	// CarryUnverifiable: no span snapshot exists for that measurement -- it
	// predates schema 0017. Unverifiable is not the same as invalid, but it is
	// not evidence either, so it holds the denominator and credits nothing.
	CarryUnverifiable CarryReason = "unverifiable"
)

// CarryOptions tunes the window. The zero value is "carry, with the defaults"
// so that a caller who has not thought about carryforward gets the behaviour
// that does not silently inflate coverage.
type CarryOptions struct {
	// Disabled turns carryforward off entirely: the resolved pool is exactly
	// the frontier's own results, which is the pre-#136 reading.
	Disabled bool

	// MaxBuilds is how many grouped frontiers back a measurement may come from
	// and still count as evidence. <= 0 takes DefaultCarryBuilds.
	MaxBuilds int

	// MaxAge is the wall-clock equivalent. <= 0 takes DefaultCarryMaxAge.
	MaxAge time.Duration

	// Now overrides the clock for deterministic tests. Nil = time.Now().UTC().
	Now func() time.Time
}

func (o CarryOptions) applyDefaults() CarryOptions {
	if o.MaxBuilds <= 0 {
		o.MaxBuilds = DefaultCarryBuilds
	}
	if o.MaxAge <= 0 {
		o.MaxAge = DefaultCarryMaxAge
	}
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	return o
}

// CarriedResult is one result the current frontier did not produce, taken from
// the last build that did. The embedded CoverageResult is already adjusted for
// the mode (denominator-only carries arrive with covered zeroed), so a caller
// that pools observed and carried results together does not have to remember
// the policy at every read site.
type CarriedResult struct {
	CoverageResult

	Mode   CarryMode   `json:"mode"`
	Reason CarryReason `json:"reason"`

	// FromRunID / FromGroup / MeasuredAt identify the measurement, so a note
	// or a --json consumer can say WHICH build the reading came from.
	FromRunID  int64     `json:"from_run_id"`
	FromGroup  string    `json:"from_group"`
	MeasuredAt time.Time `json:"measured_at"`

	// BuildsBack is how many grouped frontiers separate that build from this
	// one; 0 means the source build is further back than the window looked.
	BuildsBack int `json:"builds_back"`
}

// ResolvedCoverage is a frontier's reading: what this build measured, plus
// what it did not and had to inherit.
type ResolvedCoverage struct {
	Frontier CoverageFrontier `json:"frontier"`
	Observed []CoverageResult `json:"-"`
	Carried  []CarriedResult  `json:"carried,omitempty"`

	// Window is the window actually in force, defaults applied. Reported
	// rather than assumed: a tunable whose value is invisible is a tunable
	// nobody can reason about when the number looks wrong.
	Window CarryOptions `json:"-"`
}

// Pool returns observed and carried results as the single slice the scoring
// code consumes. Observed first, then carried, so a reader stepping through it
// sees this build before last build's.
func (r ResolvedCoverage) Pool() []CoverageResult {
	out := make([]CoverageResult, 0, len(r.Observed)+len(r.Carried))
	out = append(out, r.Observed...)
	for _, c := range r.Carried {
		out = append(out, c.CoverageResult)
	}
	return out
}

// CarriedBySymbol indexes the carries for callers that need to say which part
// of a per-feature reading is inherited.
func (r ResolvedCoverage) CarriedBySymbol() map[int64]CarriedResult {
	out := make(map[int64]CarriedResult, len(r.Carried))
	for _, c := range r.Carried {
		if c.SymbolID != nil {
			out[*c.SymbolID] = c
		}
	}
	return out
}

// CoverageCarryPort resolves a frontier into observed + carried results.
//
// It is a port of its own rather than another method on Coverage because
// carryforward is a POLICY over the coverage tables, not a fact in them: it
// has a window, a validity rule and an off switch, and folding it into
// ListFrontierResults would make every existing reader silently adopt all
// three.
type CoverageCarryPort interface {
	Resolve(ctx context.Context, f CoverageFrontier, opts CarryOptions) (ResolvedCoverage, error)
}

// CoverageCarry returns the Store's carryforward port.
func (s *Store) CoverageCarry() CoverageCarryPort {
	return &carryStore{db: s, cov: &coverageStore{db: s, q: s.queries()}}
}

type carryStore struct {
	db  *Store
	cov *coverageStore
}

var _ CoverageCarryPort = (*carryStore)(nil)

// Resolve reads the frontier's own results and, for every symbol no run in the
// frontier measured, the newest prior measurement inside the lookback horizon.
func (c *carryStore) Resolve(ctx context.Context, f CoverageFrontier, opts CarryOptions) (ResolvedCoverage, error) {
	opts = opts.applyDefaults()
	observed, err := c.cov.ListFrontierResults(ctx, f)
	if err != nil {
		return ResolvedCoverage{}, err
	}
	out := ResolvedCoverage{Frontier: f, Observed: observed, Window: opts}
	if opts.Disabled || f.Empty() || f.Group == nil || *f.Group == "" {
		return out, nil
	}

	measured := make(map[int64]bool, len(observed))
	for _, r := range observed {
		if r.SymbolID != nil {
			measured[*r.SymbolID] = true
		}
	}

	frontierAt := frontierFinishedAt(f)
	window := carryWindow{
		currentGroup: *f.Group,
		frontierAt:   frontierAt,
		horizonAt:    frontierAt.Add(-carryLookbackHorizon),
	}

	sources, err := c.carrySources(ctx, window, measured)
	if err != nil {
		return ResolvedCoverage{}, err
	}
	if len(sources) == 0 {
		return out, nil
	}
	totals, err := c.carryTotals(ctx, window)
	if err != nil {
		return ResolvedCoverage{}, err
	}
	ordinals, err := c.buildOrdinals(ctx, window, opts.MaxBuilds)
	if err != nil {
		return ResolvedCoverage{}, err
	}
	runTimes, err := c.runFinishedAt(ctx, sources)
	if err != nil {
		return ResolvedCoverage{}, err
	}

	now := opts.Now()
	for _, src := range sources {
		carried, ok := buildCarry(src, totals, ordinals, runTimes, opts, now)
		if !ok {
			continue
		}
		out.Carried = append(out.Carried, carried)
	}
	// Deterministic order: the pool feeds a score, and two runs of the same
	// audit over the same store must produce byte-identical output.
	sort.SliceStable(out.Carried, func(i, j int) bool {
		return *out.Carried[i].SymbolID < *out.Carried[j].SymbolID
	})
	return out, nil
}

// carryWindow is the (group, time-range) triple every carry query shares.
type carryWindow struct {
	currentGroup string
	frontierAt   time.Time
	horizonAt    time.Time
}

// frontierFinishedAt is the frontier's own timestamp: the newest finish across
// its runs. Prior builds are everything at or before it, which is what makes a
// re-ingest of an OLD build (a backfill landing late) unable to pull the
// frontier's carry sources forward past it.
func frontierFinishedAt(f CoverageFrontier) time.Time {
	var newest time.Time
	for _, r := range f.Runs {
		if r.FinishedAt.After(newest) {
			newest = r.FinishedAt
		}
	}
	return newest
}

// carrySourceKey is one symbol's newest prior measurement, span included.
type carrySource struct {
	symbolID    int64
	runID       int64
	group       string
	featureID   *shared.FeatureID
	spanKnown   bool
	spanMatches bool
}

func (c *carryStore) carrySources(ctx context.Context, w carryWindow, measured map[int64]bool) ([]carrySource, error) {
	rows, err := c.db.queries().ListCarrySources(ctx, listCarrySourcesArgs(w))
	if err != nil {
		return nil, fmt.Errorf("coverage carryforward: list sources: %w", err)
	}
	out := make([]carrySource, 0, len(rows))
	for _, r := range rows {
		if r.SymbolID == nil || measured[*r.SymbolID] {
			continue
		}
		src := carrySource{symbolID: *r.SymbolID, runID: r.RunID}
		if r.RunGroup != nil {
			src.group = *r.RunGroup
		}
		if r.FeatureID != nil {
			fid := shared.FeatureID(*r.FeatureID)
			src.featureID = &fid
		}
		// A snapshot exists only for measurements taken after schema 0017.
		// Missing is "unverifiable", which is deliberately NOT treated as
		// matching: see CarryUnverifiable.
		if r.SpanFilePath != nil && r.SpanLine != nil {
			src.spanKnown = true
			src.spanMatches = *r.SpanFilePath == r.CurrentFilePath &&
				*r.SpanLine == r.CurrentLine &&
				sameEndLine(r.SpanEndLine, r.CurrentEndLine)
		}
		out = append(out, src)
	}
	return out, nil
}

// sameEndLine compares two nullable end_line values. NULL means "the scanner
// could not pin the closing line" (issue #120 pinned it for Go); two NULLs are
// equally unpinned, and a NULL on one side is a real change in what is known
// about the span, so it does not match.
func sameEndLine(a, b *int64) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}

// carryTotal is one (run, symbol) statement rollup.
type carryTotal struct {
	covered int
	total   int
	status  CoverageStatus
}

func (c *carryStore) carryTotals(ctx context.Context, w carryWindow) (map[[2]int64]carryTotal, error) {
	rows, err := c.db.queries().ListCarrySymbolTotals(ctx, listCarryTotalsArgs(w))
	if err != nil {
		return nil, fmt.Errorf("coverage carryforward: list totals: %w", err)
	}
	out := make(map[[2]int64]carryTotal, len(rows))
	for _, r := range rows {
		if r.SymbolID == nil {
			continue
		}
		// The status rollup mirrors classifyCoverageResults: any pass wins,
		// a skip-only symbol stays a skip (so it keeps dropping out of the
		// denominator), everything else is uncovered.
		status := CoverageStatus(StatusFail)
		switch {
		case r.AnyPass == 1:
			status = StatusPass
		case r.AnySkip == 1:
			status = StatusSkip
		}
		out[[2]int64{r.RunID, *r.SymbolID}] = carryTotal{
			covered: int(r.CoveredStmts),
			total:   int(r.TotalStmts),
			status:  status,
		}
	}
	return out, nil
}

// buildOrdinals maps a run group to how many grouped builds back it sits from
// the current one. Only maxBuilds+1 frontiers are read: the current build fills
// one slot, so a group missing from the answer is by construction outside the
// window and needs no further arithmetic.
func (c *carryStore) buildOrdinals(ctx context.Context, w carryWindow, maxBuilds int) (map[string]int, error) {
	rows, err := c.db.queries().ListRecentRunGroups(ctx, listRecentRunGroupsArgs(w, maxBuilds))
	if err != nil {
		return nil, fmt.Errorf("coverage carryforward: list recent groups: %w", err)
	}
	out := make(map[string]int, len(rows))
	back := 0
	for _, g := range rows {
		if g == nil || *g == w.currentGroup {
			continue
		}
		back++
		out[*g] = back
	}
	return out, nil
}

// runFinishedAt reads the wall-clock finish of each distinct source run.
// Read through GetRun rather than off the grouped query because an aggregate
// column comes back untyped from the driver, and a timestamp decoded by string
// shape is exactly the kind of quiet breakage a staleness bound must not have.
func (c *carryStore) runFinishedAt(ctx context.Context, sources []carrySource) (map[int64]time.Time, error) {
	out := make(map[int64]time.Time, len(sources))
	for _, s := range sources {
		if _, ok := out[s.runID]; ok {
			continue
		}
		run, err := c.cov.GetRun(ctx, s.runID)
		if err != nil {
			return nil, fmt.Errorf("coverage carryforward: source run %d: %w", s.runID, err)
		}
		out[s.runID] = run.FinishedAt
	}
	return out, nil
}

// buildCarry applies the policy to one candidate: within the window and
// span-verified is evidence, anything else holds the denominator only.
func buildCarry(
	src carrySource,
	totals map[[2]int64]carryTotal,
	ordinals map[string]int,
	runTimes map[int64]time.Time,
	opts CarryOptions,
	now time.Time,
) (CarriedResult, bool) {
	tot, ok := totals[[2]int64{src.runID, src.symbolID}]
	if !ok {
		return CarriedResult{}, false
	}
	measuredAt := runTimes[src.runID]
	back := ordinals[src.group]

	reason := carryReasonFor(src, back, measuredAt, opts, now)
	sid := src.symbolID
	res := CoverageResult{
		RunID:        src.runID,
		SymbolID:     &sid,
		FeatureID:    src.featureID,
		Status:       tot.status,
		CoveredStmts: tot.covered,
		TotalStmts:   tot.total,
	}
	mode := CarryEvidence
	if reason != CarryFresh {
		// Hold the place, credit nothing. `fail` is the status that means
		// exactly that to classifyCoverageResults: in the denominator, not in
		// the numerator.
		mode = CarryDenominator
		res.Status = StatusFail
		res.CoveredStmts = 0
	}
	return CarriedResult{
		CoverageResult: res,
		Mode:           mode,
		Reason:         reason,
		FromRunID:      src.runID,
		FromGroup:      src.group,
		MeasuredAt:     measuredAt,
		BuildsBack:     back,
	}, true
}

// carryReasonFor decides which of the four states a candidate is in. Span
// first: a symbol whose span moved is not the symbol that was measured, and
// that verdict does not become truer for being recent.
func carryReasonFor(src carrySource, back int, measuredAt time.Time, opts CarryOptions, now time.Time) CarryReason {
	if !src.spanKnown {
		return CarryUnverifiable
	}
	if !src.spanMatches {
		return CarrySpanChanged
	}
	if back == 0 || back > opts.MaxBuilds {
		return CarryStale
	}
	if age := now.Sub(measuredAt); age > opts.MaxAge {
		return CarryStale
	}
	return CarryFresh
}

// The three carry queries take the same window; these keep the conversion in
// one place so a bound can never be applied to one query and forgotten in
// another, which would let a symbol be sourced from a run whose totals were
// not read.
func listCarrySourcesArgs(w carryWindow) sqlc.ListCarrySourcesParams {
	g := w.currentGroup
	return sqlc.ListCarrySourcesParams{CurrentGroup: &g, FrontierAt: w.frontierAt, HorizonAt: w.horizonAt}
}

func listCarryTotalsArgs(w carryWindow) sqlc.ListCarrySymbolTotalsParams {
	g := w.currentGroup
	return sqlc.ListCarrySymbolTotalsParams{CurrentGroup: &g, FrontierAt: w.frontierAt, HorizonAt: w.horizonAt}
}

func listRecentRunGroupsArgs(w carryWindow, maxBuilds int) sqlc.ListRecentRunGroupsParams {
	// maxBuilds+1: the current build occupies one of the returned slots.
	return sqlc.ListRecentRunGroupsParams{FrontierAt: w.frontierAt, MaxGroups: int64(maxBuilds) + 1}
}
