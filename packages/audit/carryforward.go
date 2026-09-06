package audit

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// Carryforward at the scoring layer (issue #136).
//
// The audit reads a coverage frontier -- the runs of one CI build, unioned by
// run group (#86). When a job in that build fails or is skipped, the symbols
// only it measures are absent from the frontier, and the line-weighted score
// sums a smaller denominator: coverage rises because testing stopped. The
// store's carry port fills those holes from the last build that measured them
// (see packages/store/carryforward.go for the window and the validity rule);
// this file is where the filled pool meets the score, and where the resulting
// number is made to say out loud how much of itself is inherited.

// coveragePool is a frontier's results as the scorer sees them: this build's
// observations with last build's standing in for what this build did not
// measure. `carried` indexes the substitutions so a per-feature note can name
// its own share rather than the frontier's.
type coveragePool struct {
	results  []store.CoverageResult
	carried  map[int64]store.CarriedResult
	resolved store.ResolvedCoverage
}

// coverageSignal resolves the pool and scores against it.
//
// The pool is a property of the FRONTIER, not of the feature, so it is
// resolved once and reused (see resolveCoveragePool).
func (a *auditImpl) coverageSignal(
	ctx context.Context,
	featureID shared.FeatureID,
	links []store.FeatureSymbolLink,
	frontier store.CoverageFrontier,
) (signalResult, bool, error) {
	pool, err := a.resolveCoveragePool(ctx, frontier)
	if err != nil {
		return signalResult{}, false, err
	}
	return a.coverageSignalPooled(ctx, featureID, links, frontier, pool)
}

// resolveCoveragePool asks the store for the frontier's reading. The audit's
// clock is passed through so a test that pins Now pins the staleness window
// with it; a staleness bound that reads a different clock than the scores
// around it is a bound nobody can reproduce.
//
// The answer is MEMOISED per frontier for the life of the Audit instance.
// Resolution is a property of the frontier and of the window, neither of which
// varies by feature, but it costs three grouped scans over the whole carry
// window plus one GetRun per source run. Paying that once per feature made
// ScoreAll's cost O(features x window) for an answer that is the same every
// time, which on a repo with hundreds of features is the difference between a
// second and a minute. Keyed by the frontier's own identity so a caller that
// hands over a different frontier gets a fresh resolution rather than the
// previous one's.
func (a *auditImpl) resolveCoveragePool(ctx context.Context, frontier store.CoverageFrontier) (coveragePool, error) {
	key := coveragePoolKey(frontier)
	if a.covPoolReady && a.covPoolKey == key {
		return a.covPool, nil
	}
	resolved, err := a.store.CoverageCarry().Resolve(ctx, frontier, store.CarryOptions{Now: a.opts.Now})
	if err != nil {
		return coveragePool{}, fmt.Errorf("resolve coverage frontier: %w", err)
	}
	pool := coveragePool{
		results:  resolved.Pool(),
		carried:  resolved.CarriedBySymbol(),
		resolved: resolved,
	}
	a.covPool, a.covPoolKey, a.covPoolReady = pool, key, true
	return pool, nil
}

// coveragePoolKey identifies a frontier by everything the resolution depends
// on: the run group it was read under and the exact set of runs in it.
func coveragePoolKey(f store.CoverageFrontier) string {
	var b strings.Builder
	if f.Group != nil {
		b.WriteString(*f.Group)
	}
	b.WriteByte('\x00')
	fmt.Fprintf(&b, "%d", f.Newest)
	for _, id := range f.RunIDs() {
		fmt.Fprintf(&b, ",%d", id)
	}
	return b.String()
}

// surfaceFrontier is the frontier the SURFACE resolution must be shown: this
// build's runs plus the runs the carries came from.
//
// Without this, carryforward does not fire in the case it exists for. The
// preferred surface tier (SurfaceDynamic, #104) derives a feature's symbol set
// from the per-test evidence of the frontier's OWN runs, and the scorer then
// keeps only results whose symbol is in that set. When a framework's job dies,
// its evidence leaves the frontier, the surface shrinks by exactly the symbols
// that job measured, and the carried results standing in for them are filtered
// straight back out -- so the denominator shrinks anyway and coverage rises,
// which is issue #136 unfixed.
//
// The carry sources are added as bare run ids because that is all the surface
// derivation reads off a frontier (CoverageFrontier.RunIDs). Ubiquity is still
// computed per run, so a carried run's shared-runtime cutoff is its own.
func (p coveragePool) surfaceFrontier(frontier store.CoverageFrontier) store.CoverageFrontier {
	sources := p.resolved.CarrySourceRunIDs()
	if len(sources) == 0 {
		return frontier
	}
	present := make(map[int64]bool, len(frontier.Runs))
	for _, r := range frontier.Runs {
		present[r.ID] = true
	}
	out := frontier
	out.Runs = append([]store.CoverageRun(nil), frontier.Runs...)
	for _, id := range sources {
		if present[id] {
			continue
		}
		out.Runs = append(out.Runs, store.CoverageRun{ID: id})
	}
	return out
}

// carryShare is one feature's slice of the carry: how much of ITS denominator
// came from another build.
type carryShare struct {
	symbols   int
	stmts     int
	evidence  int // statements carried as evidence (the rest credit nothing)
	obsCover  int
	obsTotal  int
	fromGroup string
	backBuild int

	// testSymbols / testPasses count carries on this feature's TEST-role
	// symbols. They are not in `wanted` -- a test symbol is never part of the
	// implementation denominator -- but classifyCoverageResults reads them as
	// "the feature's test passed", which credits the feature outright under
	// the gotest pass/fail model (#82). A carried pass there moves the score
	// exactly as an observed one would, so leaving it out of the accounting
	// let a number assembled from two builds present itself as one.
	testSymbols int
	testPasses  int
}

// any reports whether anything this feature's score consumes was carried:
// either a symbol in the scored denominator, or one of the feature's test
// symbols, which credits the feature through the pass/fail model.
func (s carryShare) any() bool { return s.symbols > 0 || s.testSymbols > 0 }

// share computes the accounting over the symbols this feature's score actually
// consumes. Restricting to those matters: a frontier-wide carry percentage
// attached to a feature whose every symbol was freshly measured would be a true
// statement about the wrong thing.
func (p coveragePool) share(wanted, testSyms map[int64]bool) carryShare {
	var out carryShare
	// The named build is the NEWEST carry in this feature's share -- the
	// closest thing to "and the rest is from build X". Ordering by symbol id
	// keeps the choice deterministic when several carries tie.
	ids := make([]int64, 0, len(p.carried))
	for sid := range p.carried {
		if wanted[sid] || testSyms[sid] {
			ids = append(ids, sid)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var newest store.CarriedResult
	for i, sid := range ids {
		c := p.carried[sid]
		if testSyms[sid] {
			out.testSymbols++
			if c.Status == store.StatusPass {
				out.testPasses++
			}
		}
		if wanted[sid] {
			out.symbols++
			out.stmts += c.TotalStmts
			if c.Mode == store.CarryEvidence {
				out.evidence += c.TotalStmts
			}
		}
		if i == 0 || c.MeasuredAt.After(newest.MeasuredAt) {
			newest = c
		}
	}
	out.fromGroup = newest.FromGroup
	out.backBuild = newest.BuildsBack
	for _, r := range p.resolved.Observed {
		if r.SymbolID == nil || !wanted[*r.SymbolID] || r.TotalStmts <= 0 {
			continue
		}
		out.obsCover += r.CoveredStmts
		out.obsTotal += r.TotalStmts
	}
	return out
}

// annotate rewrites the coverage note so the number cannot present itself as
// one measurement when it was assembled from two.
//
// It reports three things, and each earns its place:
//
//   - how much of the denominator is carried, so a reader can size the
//     inheritance;
//   - which build it came from and how far back, so "carried" is checkable
//     rather than a disclaimer;
//   - the OBSERVED fraction: this build's covered statements over the whole
//     (stable) denominator. That is the number a CI gate should read. The
//     score itself may not move at all when a job dies -- that is the point of
//     carrying -- so a gate reading the score would sail through the build
//     that stopped measuring. The observed fraction collapses instead.
func (p coveragePool) annotate(res signalResult, wanted, testSyms map[int64]bool) signalResult {
	sh := p.share(wanted, testSyms)
	if !sh.any() {
		return res
	}
	denom := sh.obsTotal + sh.stmts
	suffix := ""
	switch {
	case sh.symbols > 0 && denom > 0:
		observed := 100.0 * float64(sh.obsCover) / float64(denom)
		suffix = fmt.Sprintf(
			"; %d/%d statements carried from build %q (%s), observed %d/%d (%.0f%%)",
			sh.stmts, denom, sh.fromGroup, buildsBackLabel(sh.backBuild),
			sh.obsCover, denom, observed)
	case sh.symbols > 0:
		// No statement data anywhere in this feature's reading: the score came
		// from the binary symbol model, so the share is stated in symbols.
		suffix = fmt.Sprintf("; %d symbol(s) carried from build %q (%s), not re-measured in this build",
			sh.symbols, sh.fromGroup, buildsBackLabel(sh.backBuild))
	default:
		// Nothing in the denominator is carried, but a test result is -- see
		// carryShare.testSymbols. Saying only "0 carried" here would be the
		// same lie in the other direction.
		suffix = fmt.Sprintf("; %d test result(s) carried from build %q (%s)",
			sh.testSymbols, sh.fromGroup, buildsBackLabel(sh.backBuild))
	}
	if sh.testPasses > 0 {
		suffix += fmt.Sprintf("; %d carried test result(s) still credit this feature, not re-run in this build",
			sh.testPasses)
	}
	if sh.evidence < sh.stmts {
		// Part of the carry credits nothing: it is too old, the symbol moved,
		// or the measurement predates the span snapshot. Saying so is the
		// difference between "we inherited a reading" and "we inherited a
		// hole we are holding open".
		suffix += fmt.Sprintf("; %d of those statements hold the denominator only", sh.stmts-sh.evidence)
	}

	if res.note.message == "" {
		// A fully-passing signal carries no note, so a 100% reading assembled
		// mostly from another build would say nothing at all. Weight it by the
		// carried share: it does not change the score, it changes where the
		// explanation ranks among the feature's reasons.
		weight := 100.0
		if denom > 0 {
			weight = 100.0 * float64(sh.stmts) / float64(denom)
		}
		res.note = signalNote{
			weight:  weight,
			message: fmt.Sprintf("coverage: %.0f%% but part of the reading is carried%s", res.score, suffix),
		}
		return res
	}
	res.note.message += suffix
	return res
}

// buildsBackLabel renders the distance to the source build.
// store.CarryBuildsBackBeyondWindow means the build sits further back than the
// window looked at all, which is a different statement from "one build back"
// and must not render as a distance.
func buildsBackLabel(back int) string {
	switch {
	case back < 1:
		return "beyond the carry window"
	case back == 1:
		return "1 build back"
	default:
		return fmt.Sprintf("%d builds back", back)
	}
}
