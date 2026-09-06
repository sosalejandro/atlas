package audit

import (
	"context"
	"fmt"
	"sort"

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
// The pool is resolved per feature, which is how ListFrontierResults was
// already being read before this change -- carryforward adds two grouped reads
// over the same window on top, and does not change the shape of the cost.
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
func (a *auditImpl) resolveCoveragePool(ctx context.Context, frontier store.CoverageFrontier) (coveragePool, error) {
	resolved, err := a.store.CoverageCarry().Resolve(ctx, frontier, store.CarryOptions{Now: a.opts.Now})
	if err != nil {
		return coveragePool{}, fmt.Errorf("resolve coverage frontier: %w", err)
	}
	return coveragePool{
		results:  resolved.Pool(),
		carried:  resolved.CarriedBySymbol(),
		resolved: resolved,
	}, nil
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
}

// share computes the accounting over the symbols this feature is scored on.
// Restricting to `wanted` matters: a frontier-wide carry percentage attached to
// a feature whose every symbol was freshly measured would be a true statement
// about the wrong thing.
func (p coveragePool) share(wanted map[int64]bool) carryShare {
	var out carryShare
	// The named build is the NEWEST carry in this feature's share -- the
	// closest thing to "and the rest is from build X". Ordering by symbol id
	// keeps the choice deterministic when several carries tie.
	ids := make([]int64, 0, len(p.carried))
	for sid := range p.carried {
		if wanted[sid] {
			ids = append(ids, sid)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var newest store.CarriedResult
	for i, sid := range ids {
		c := p.carried[sid]
		out.symbols++
		out.stmts += c.TotalStmts
		if c.Mode == store.CarryEvidence {
			out.evidence += c.TotalStmts
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
func (p coveragePool) annotate(res signalResult, wanted map[int64]bool) signalResult {
	sh := p.share(wanted)
	if sh.symbols == 0 {
		return res
	}
	denom := sh.obsTotal + sh.stmts
	suffix := ""
	if denom > 0 {
		observed := 100.0 * float64(sh.obsCover) / float64(denom)
		suffix = fmt.Sprintf(
			"; %d/%d statements carried from build %q (%s), observed %d/%d (%.0f%%)",
			sh.stmts, denom, sh.fromGroup, buildsBackLabel(sh.backBuild),
			sh.obsCover, denom, observed)
	} else {
		// No statement data anywhere in this feature's reading: the score came
		// from the binary symbol model, so the share is stated in symbols.
		suffix = fmt.Sprintf("; %d symbol(s) carried from build %q (%s), not re-measured in this build",
			sh.symbols, sh.fromGroup, buildsBackLabel(sh.backBuild))
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

// buildsBackLabel renders the distance to the source build. Zero means the
// build sits further back than the window looked at all, which is a different
// statement from "one build back" and must not render as "0 builds back".
func buildsBackLabel(back int) string {
	switch back {
	case 0:
		return "beyond the carry window"
	case 1:
		return "1 build back"
	default:
		return fmt.Sprintf("%d builds back", back)
	}
}
