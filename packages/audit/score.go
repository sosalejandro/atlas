package audit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// scoreFromFeature is the core scoring routine. It takes a Feature row
// (already loaded by the caller) plus the current coverage frontier (which
// may be empty) and produces the FeatureHealth record.
//
// Each signal independently reports "available?" — when unavailable, the
// weighted-average step re-normalises over the remaining signals. A feature
// with no aggregates and no contracts gets a fair score from coverage +
// freshness alone (rather than being penalised toward zero).
func (a *auditImpl) scoreFromFeature(
	ctx context.Context,
	feat store.Feature,
	frontier store.CoverageFrontier,
) (FeatureHealth, error) {
	now := a.opts.Now()
	links, err := a.store.FeatureSymbols().ListByFeature(ctx, feat.ID)
	if err != nil {
		return FeatureHealth{}, fmt.Errorf("list feature_symbols: %w", err)
	}

	set := newSignalSet()
	a.lastSurfaceSource = ""
	a.lastSurfaceSymbols = nil

	if !frontier.Empty() && len(links) > 0 {
		cov, ok, err := a.coverageSignal(ctx, feat.ID, links, frontier)
		if err := set.add(SignalCoverage, "coverage", cov, ok, err); err != nil {
			return FeatureHealth{}, err
		}
	}
	// Decision coverage is scored over the SAME surface the statement signal
	// just used (issue #140). It runs whether or not that signal was
	// available: `atlas flow measure` reads a coverprofile off disk, so a repo
	// can have branch verdicts with nothing in coverage_results.
	var decision *DecisionCoverageReport
	if len(links) > 0 {
		dec, rep, ok, err := a.decisionCoverageSignal(ctx, links, frontier, a.lastSurfaceSymbols)
		if err := set.add(SignalDecisionCoverage, "decision coverage", dec, ok, err); err != nil {
			return FeatureHealth{}, err
		}
		decision = rep
	}
	if a.opts.GitBlame != nil && len(links) > 0 {
		fresh, ok, err := a.annotationFreshnessSignal(ctx, links, now)
		if err := set.add(SignalAnnotationFresh, "annotation freshness", fresh, ok, err); err != nil {
			return FeatureHealth{}, err
		}
	}
	pat, ok, err := a.patternComplianceSignal(ctx, feat.ID, links)
	if err := set.add(SignalPatternCompliance, "pattern compliance", pat, ok, err); err != nil {
		return FeatureHealth{}, err
	}
	drift, ok, err := a.contractDriftSignal(ctx, feat.ID, now)
	if err := set.add(SignalContractDrift, "contract drift", drift, ok, err); err != nil {
		return FeatureHealth{}, err
	}
	components, available, notes := set.components, set.available, set.notes

	// --- Annotation presence: a FLOOR, not a re-normalising signal -------
	// weightedAverage re-normalises over available signals, so adding
	// presence=100 as a component would score a presence-ONLY feature 100
	// (masking phantoms and genuine "annotated but untested" gaps). Instead
	// we compute the real signals first, then — only when nothing else is
	// available — apply a low "annotated but unverified" floor equal to the
	// presence weight × 100 (10 by default). A feature whose annotation→
	// symbol link exists (the same condition `atlas trace feature:<id>`
	// uses) thus scores >0 but ranks at the bottom, where it belongs until a
	// coverage run verifies it. See issues #78 / #77.
	score := weightedAverage(components, available, a.blendWeights(available, decision))
	switch {
	case len(available) > 0:
		// Real signals decided the score; presence adds nothing on top.
	case len(links) > 0:
		presenceWeight := a.opts.Weights[SignalAnnotationPresence]
		if presenceWeight <= 0 {
			presenceWeight = defaultWeights()[SignalAnnotationPresence]
		}
		score = presenceWeight * 100
		components[SignalAnnotationPresence] = score
		available[SignalAnnotationPresence] = true
		notes = append(notes, signalNote{
			weight:  100 - score,
			message: "annotated (trace-linked) but no verified coverage/contract/pattern signal yet",
		})
	default:
		// No signal at all — no linked symbols, no coverage, no blame, no
		// aggregates, no contracts. Score stays 0 with an explanatory reason
		// so consumers can tell "0 because broken" from "0 because unknown".
		notes = append(notes, signalNote{
			weight:  100,
			message: "no audit signals available (no coverage, no aggregate, no contract, no annotation source)",
		})
	}

	return FeatureHealth{
		FeatureID:     feat.ID,
		Score:         score,
		Components:    components,
		Reasons:       topReasons(notes, 3),
		SampledAt:     now,
		SurfaceSource: a.lastSurfaceSource,
		Decision:      decision,
	}, nil
}

// signalNote is the per-signal explanatory note used to derive top reasons.
//
// weight is "how much this signal hurt the score" — bigger weight = more
// important. Notes with weight <= 0 are dropped (fully-passing signals).
type signalNote struct {
	weight  float64
	message string
}

// topReasons returns the top-N notes by weight, message-formatted.
func topReasons(notes []signalNote, n int) []string {
	filtered := make([]signalNote, 0, len(notes))
	for _, x := range notes {
		if x.weight > 0 && x.message != "" {
			filtered = append(filtered, x)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].weight != filtered[j].weight {
			return filtered[i].weight > filtered[j].weight
		}
		return filtered[i].message < filtered[j].message
	})
	if len(filtered) > n {
		filtered = filtered[:n]
	}
	out := make([]string, 0, len(filtered))
	for _, x := range filtered {
		out = append(out, x.message)
	}
	return out
}

// signalResult bundles a 0..100 score with its explanatory note.
type signalResult struct {
	score float64
	note  signalNote
}

// weightedAverage blends the available component scores using the supplied
// weights, re-normalising over the active subset. When `available` is empty
// the function returns 0 — the caller is responsible for adding an
// explanatory note in that case.
//
// All scores are 0..100; the output is 0..100.
func weightedAverage(scores map[string]float64, available map[string]bool, weights map[string]float64) float64 {
	if len(available) == 0 {
		return 0
	}
	var sum, totalWeight float64
	for k, ok := range available {
		if !ok {
			continue
		}
		w, present := weights[k]
		if !present || w <= 0 {
			// A signal is available but the operator didn't weight it —
			// give it the default weight so the algorithm still includes it.
			w = defaultWeights()[k]
		}
		totalWeight += w
		sum += w * scores[k]
	}
	if totalWeight == 0 {
		return 0
	}
	out := sum / totalWeight
	if out < 0 {
		return 0
	}
	if out > 100 {
		return 100
	}
	return out
}

// ---------------------------------------------------------------------------
// Coverage signal
// ---------------------------------------------------------------------------

// coverageSignal returns the fraction of the feature's linked symbols that
// have at least one `pass` coverage result on the current frontier.
// "Skip"-only results are treated as NO SIGNAL — they don't count toward the
// denominator. Otherwise a feature whose tests are explicitly disabled in
// CI would always score 0%, which is the wrong reading.
//
// The frontier's runs are read as ONE pool of results (issue #86), which is
// also how a per-symbol disagreement between frameworks resolves:
// classification ORs the buckets, so a symbol the Go suite executed counts as
// covered even when a frontend run in the same group never touched it.
// Merging toward the higher covered fraction is the only direction that
// cannot invent a regression out of another framework's blind spot.
//
// Returns (result, true, nil) when at least one symbol has a usable result.
// Returns (zero, false, nil) when every linked symbol is skip-only or
// completely absent from the frontier.
// The result pool is resolved by the CALLER (coverageSignal, in
// carryforward.go) rather than read here, because what the frontier measured
// is no longer the whole reading: symbols this build did not re-measure are
// carried from the last build that did (issue #136). Passing the pool in keeps
// that policy in one place and keeps this function about scoring.
func (a *auditImpl) coverageSignalPooled(
	ctx context.Context,
	featureID shared.FeatureID,
	links []store.FeatureSymbolLink,
	frontier store.CoverageFrontier,
	pool coveragePool,
) (signalResult, bool, error) {
	testSyms := testSymbolIDs(links)
	// The surface is resolved against the frontier PLUS the runs the carries
	// came from (coveragePool.surfaceFrontier). Resolving it against the
	// frontier alone makes the preferred tier shrink by exactly the symbols a
	// dead job measured, which filters the carried results back out again.
	wanted, useSurface, source, err := a.resolveWantedSet(ctx, links, pool.surfaceFrontier(frontier))
	a.lastSurfaceSource = source
	a.lastSurfaceSymbols = wanted
	if err != nil {
		return signalResult{}, false, err
	}
	if len(wanted) == 0 && len(testSyms) == 0 {
		return signalResult{}, false, nil
	}
	results := pool.results
	pass, skipOnly, stmts, featurePassed, testSeen := classifyCoverageResults(results, wanted, testSyms, featureID)
	if res, done := creditPassingTest(featurePassed, useSurface, wanted, pass); done {
		return pool.annotate(res, wanted, testSyms), true, nil
	}
	denom, numer := coverageRatio(wanted, pass, skipOnly)

	if a.shouldTryPackageAnchor(useSurface, numer, wanted) {
		anchored, pkgSurface, ok, err := a.packageAnchorSignal(ctx, links, results, testSyms, featureID)
		if err != nil {
			return signalResult{}, false, err
		}
		if ok {
			a.lastSurfaceSource = SurfacePackageAnchor
			// The anchor surface, not the (all-test-file) wanted set, is what
			// this score was actually computed over — so it is what decision
			// coverage must be computed over too.
			a.lastSurfaceSymbols = pkgSurface
			return pool.annotate(anchored, wanted, testSyms), true, nil
		}
	}

	if denom == 0 {
		return emptyDenominatorResult(testSeen, useSurface)
	}
	return pool.annotate(scoreCoverage(wanted, pass, skipOnly, stmts, numer, denom, ""), wanted, testSyms), true, nil
}

// creditPassingTest applies the gotest pass/fail credit (#82): with no
// execution profile, a passing annotated test credits the feature. It returns
// done=true only when the feature is scored outright (nothing linked but a
// passing test); otherwise it marks the wanted symbols passed in place and
// leaves scoring to the caller.
//
// With an impl surface the credit does not apply at all — a passing test does
// NOT imply every impl symbol ran, and the executed fraction is the truth.
func creditPassingTest(
	featurePassed, useSurface bool,
	wanted map[int64]bool,
	pass map[int64]bool,
) (signalResult, bool) {
	if !featurePassed || useSurface {
		return signalResult{}, false
	}
	if len(wanted) == 0 {
		return signalResult{score: 100, note: coverageNote(1, 1, 100)}, true
	}
	for sid := range wanted {
		pass[sid] = true
	}
	return signalResult{}, false
}

// shouldTryPackageAnchor reports whether the Tier 2 fallback applies: the
// call-edge surface was empty, the direct-link model credited nothing, and
// every wanted symbol lives in a test file (so it can never appear in a
// production execution profile). See packageAnchorSignal.
func (a *auditImpl) shouldTryPackageAnchor(useSurface bool, numer int, wanted map[int64]bool) bool {
	if useSurface || numer != 0 {
		return false
	}
	return allTestFileWanted(wanted, a.symbolCache)
}

// emptyDenominatorResult decides what "nothing to score" means: a test was
// seen for this feature in the run but nothing it links to executed (score 0,
// a real signal), versus no evidence at all (not available, so the weighted
// average re-normalises over the other signals).
func emptyDenominatorResult(testSeen, useSurface bool) (signalResult, bool, error) {
	if testSeen && !useSurface {
		return signalResult{score: 0, note: coverageNote(0, 1, 0)}, true, nil
	}
	return signalResult{}, false, nil
}

// signalSet accumulates the per-signal outputs of a feature's audit. Every
// signal has the same shape — score, availability, note — and the same error
// handling, so collecting them through one method keeps scoreFromFeature a
// readable list of signals rather than four copies of the same eight lines.
type signalSet struct {
	components map[string]float64
	available  map[string]bool
	notes      []signalNote
}

func newSignalSet() *signalSet {
	return &signalSet{
		components: make(map[string]float64),
		available:  make(map[string]bool),
	}
}

// add records one signal. `what` names the signal in the wrapped error. An
// unavailable signal (ok=false) is not an error: weightedAverage re-normalises
// over the signals that ARE available, so a feature with no aggregates and no
// contracts is scored fairly on coverage and freshness alone.
func (s *signalSet) add(name, what string, res signalResult, ok bool, err error) error {
	if err != nil {
		return fmt.Errorf("%s signal: %w", what, err)
	}
	if !ok {
		return nil
	}
	s.components[name] = res.score
	s.available[name] = true
	s.notes = append(s.notes, res.note)
	return nil
}

// Surface derivations, reported per feature so a coverage number always says
// where its denominator came from.
const (
	// SurfaceDynamic: the union of what the feature's own tests executed,
	// minus shared runtime. Evidence, not inference.
	SurfaceDynamic = "dynamic"
	// SurfaceStatic: a call-edge walk from the annotated test symbols.
	SurfaceStatic = "static"
	// SurfacePackageAnchor: production symbols co-located with the feature's
	// test package (issue #84's tier 2).
	SurfacePackageAnchor = "package-anchor"
	// SurfaceDirectLinks: the annotated symbols themselves, with the gotest
	// pass/fail model on top (issue #82).
	SurfaceDirectLinks = "direct-links"
)

// resolveWantedSet picks the symbol set a feature's coverage is scored over,
// in descending order of evidential strength.
//
// Tier 0 — dynamic. If the run carries per-test evidence (schema 0010), the
// surface is the union of the symbols the feature's OWN tests executed, minus
// symbols nearly every test executes. This is the dynamic feature-location
// technique, and it is the only tier that cannot be fooled by interface
// dispatch, DI containers, reflection or string-routed handlers (issue #104).
//
// Tier 1 — static. A call-edge walk from the annotated test symbols. Only
// reaches what the scanner resolved, which is why an e2e-rooted annotation can
// miss the domain code its feature was thoroughly unit-testing (issue #84).
//
// Fallback. With neither, the caller uses the direct-link + test-pass model
// (issue #82), which `useSurface=false` signals.
func (a *auditImpl) resolveWantedSet(
	ctx context.Context,
	links []store.FeatureSymbolLink,
	frontier store.CoverageFrontier,
) (map[int64]bool, bool, string, error) {
	dynamic, err := a.dynamicImplSurface(ctx, links, frontier)
	if err != nil {
		return nil, false, "", fmt.Errorf("dynamic impl surface: %w", err)
	}
	if len(dynamic) > 0 {
		return dynamic, true, SurfaceDynamic, nil
	}
	surface, err := a.featureImplSurface(ctx, links)
	if err != nil {
		return nil, false, "", fmt.Errorf("impl surface: %w", err)
	}
	if len(surface) > 0 {
		return surface, true, SurfaceStatic, nil
	}
	return wantedSymbolIDs(links), false, SurfaceDirectLinks, nil
}

// dynamicImplSurface derives a feature's implementation from execution
// evidence: every symbol the feature's annotated tests ran, minus the symbols
// that nearly the whole suite runs.
//
// The subtraction is what makes it usable. Without it every surface would
// include the logger, the DI container, config loading and the middleware
// chain — code that runs under every test and belongs to no feature.
// UbiquityCutoff is the fraction of the suite above which a symbol counts as
// shared runtime; it is a tunable with real failure modes in both directions,
// so the value in force is reported rather than hidden.
//
// Returns an empty set (not an error) when no run on the frontier carries
// per-test evidence, so a store ingested the old way behaves exactly as
// before.
//
// Across a frontier the surfaces are derived per run and then unioned, rather
// than pooling every run's evidence into one denominator. Ubiquity is a
// property of a SUITE: a symbol run by 90% of the Go tests is Go's shared
// runtime, and merging the Go and Playwright suites into a single test count
// would dilute both cutoffs by whichever suite happened to be larger.
func (a *auditImpl) dynamicImplSurface(
	ctx context.Context,
	links []store.FeatureSymbolLink,
	frontier store.CoverageFrontier,
) (map[int64]bool, error) {
	tests := testSymbolIDs(links)
	if len(tests) == 0 {
		return nil, nil
	}
	surface := map[int64]bool{}
	for _, runID := range frontier.RunIDs() {
		perRun, err := a.dynamicImplSurfaceForRun(ctx, tests, runID)
		if err != nil {
			return nil, err
		}
		for sid := range perRun {
			surface[sid] = true
		}
	}
	if len(surface) == 0 {
		return nil, nil
	}
	return surface, nil
}

// dynamicImplSurfaceForRun is dynamicImplSurface against a single run, where
// the ubiquity cutoff is meaningful.
func (a *auditImpl) dynamicImplSurfaceForRun(
	ctx context.Context,
	tests map[int64]bool,
	runID int64,
) (map[int64]bool, error) {
	if runID == 0 {
		return nil, nil
	}
	totalTests, err := a.store.TestCoverage().CountTests(ctx, runID)
	if err != nil {
		return nil, err
	}
	if totalTests == 0 {
		return nil, nil
	}
	fanIn, err := a.store.TestCoverage().FanIn(ctx, runID)
	if err != nil {
		return nil, err
	}
	cutoff := a.opts.UbiquityCutoff
	if cutoff <= 0 || cutoff > 1 {
		cutoff = defaultUbiquityCutoff
	}
	// A suite too small for the ratio to mean anything: with three tests, a
	// symbol shared by two is not shared runtime, it is a domain service two
	// features legitimately use.
	maxFanIn := totalTests
	if totalTests >= minTestsForUbiquityCutoff {
		maxFanIn = int(float64(totalTests) * cutoff)
		if maxFanIn < 1 {
			maxFanIn = 1
		}
	}

	surface := map[int64]bool{}
	for testID := range tests {
		rows, err := a.store.TestCoverage().SymbolsExecutedBy(ctx, runID, testID)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if fanIn[r.SymbolID] > maxFanIn {
				continue
			}
			surface[r.SymbolID] = true
		}
	}
	return surface, nil
}

// scoreCoverage turns a classified result set into the signal.
//
// Line-weighted (Tier B) when the run carries per-symbol statement counts
// (gocover ingest, migration 0009): 100 * Σ(covered_stmts) / Σ(total_stmts)
// over the wanted symbols, a real line fraction that tracks
// `go tool cover -func`. Falls back to the binary symbol-pass fraction when no
// statement data is present (older runs, gotest pass/fail mode, e2e
// feature-level credit). `suffix` labels the derivation in the note.
func scoreCoverage(
	wanted map[int64]bool,
	pass map[int64]bool,
	skipOnly map[int64]bool,
	stmts map[int64]stmtCounts,
	numer, denom int,
	suffix string,
) signalResult {
	if lineScore, covered, total, ok := lineWeightedScore(wanted, skipOnly, pass, stmts); ok {
		return signalResult{score: lineScore, note: lineCoverageNote(covered, total, lineScore, suffix)}
	}
	score := 100.0 * float64(numer) / float64(denom)
	return signalResult{score: score, note: coverageNoteWithSuffix(numer, denom, score, suffix)}
}

// packageAnchorSignal is the Tier 2 fallback (issue #84): when the call-edge
// surface is empty AND the direct-link model credited nothing (numer=0 because
// every wanted symbol lives in a _test.go file and so never appears in a
// production execution profile), attribute coverage by Go package
// co-location instead.
//
// This handles the "annotation root is a test stub with zero outgoing call
// edges" case — common for the nopXxx / noopXxx stubs the Go scanner emits as
// impl-linked symbols. The fallback is gated by MaxPackageAnchorSymbols
// (default 200): larger packages are skipped because they are likely shared
// infrastructure whose execution rate reflects many features, not this one.
//
// Reports ok=false when the anchor surface is empty or contributes no
// denominator, leaving the caller's normal path in charge. On ok=true it also
// returns the surface it scored over, because that — not the caller's wanted
// set — is the symbol set this feature's coverage was actually measured on,
// and decision coverage has to be measured on the same one.
func (a *auditImpl) packageAnchorSignal(
	ctx context.Context,
	links []store.FeatureSymbolLink,
	results []store.CoverageResult,
	testSyms map[int64]bool,
	featureID shared.FeatureID,
) (signalResult, map[int64]bool, bool, error) {
	pkgSurface, err := a.featurePackageAnchorSurface(ctx, links, a.opts.MaxPackageAnchorSymbols)
	if err != nil {
		return signalResult{}, nil, false, fmt.Errorf("package-anchor surface: %w", err)
	}
	if len(pkgSurface) == 0 {
		return signalResult{}, nil, false, nil
	}
	pkgPass, pkgSkipOnly, pkgStmts, _, _ := classifyCoverageResults(results, pkgSurface, testSyms, featureID)
	pkgDenom, pkgNumer := coverageRatio(pkgSurface, pkgPass, pkgSkipOnly)
	if pkgDenom == 0 {
		return signalResult{}, nil, false, nil
	}
	res := scoreCoverage(pkgSurface, pkgPass, pkgSkipOnly, pkgStmts, pkgNumer, pkgDenom, " (package-anchor)")
	return res, pkgSurface, true, nil
}

// stmtCounts is a per-symbol statement tally read from coverage_results
// (migration 0009): covered = executed statements, total = total statements.
type stmtCounts struct {
	covered int
	total   int
}

// lineWeightedScore computes the Tier-B line-weighted coverage fraction over
// `wanted`: 100 * Σ(covered_stmts) / Σ(total_stmts), summing only symbols that
// are NOT skip-only (matching coverageRatio's denominator policy) and that
// carry statement data. Returns ok=false when no wanted symbol has any
// statement total — the caller then falls back to the binary symbol-pass
// fraction so older runs and statement-less frameworks behave exactly as
// before.
func lineWeightedScore(wanted, skipOnly, pass map[int64]bool, stmts map[int64]stmtCounts) (score float64, covered, total int, ok bool) {
	for sid := range wanted {
		if skipOnly[sid] && !pass[sid] {
			continue // skip-only symbols drop out of the denominator
		}
		c, present := stmts[sid]
		if !present {
			continue
		}
		covered += c.covered
		total += c.total
	}
	if total == 0 {
		return 0, 0, 0, false
	}
	score = 100.0 * float64(covered) / float64(total)
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	return score, covered, total, true
}

// wantedSymbolIDs returns the set of feature_symbols.symbol_id values that
// should count toward coverage. Test-role rows are excluded — they are the
// tests themselves, not what tests cover.
func wantedSymbolIDs(links []store.FeatureSymbolLink) map[int64]bool {
	wanted := make(map[int64]bool, len(links))
	for _, l := range links {
		if l.Role == store.RoleTest {
			continue
		}
		wanted[l.SymbolID] = true
	}
	return wanted
}

// testSymbolIDs returns the feature's test-role linked symbols. go-test (and
// vitest/jest) coverage results resolve to the TEST function symbol, so a
// passing result keyed to one of these is the bridge from "the annotated test
// passed" to "the feature is covered". See issue #82.
func testSymbolIDs(links []store.FeatureSymbolLink) map[int64]bool {
	m := make(map[int64]bool, len(links))
	for _, l := range links {
		if l.Role == store.RoleTest {
			m[l.SymbolID] = true
		}
	}
	return m
}

// classifyCoverageResults walks `results` and returns three buckets:
//
//   - pass:          symbol_id → at least one passing result
//   - skipOnly:      symbol_id → seen, but only ever skip-status
//   - featurePassed: any non-symbol-keyed `pass` result that matches the
//     feature id (E2E-style credit, applied to ALL wanted symbols by the
//     caller).
func classifyCoverageResults(
	results []store.CoverageResult,
	wanted map[int64]bool,
	testSyms map[int64]bool,
	featureID shared.FeatureID,
) (pass, skipOnly map[int64]bool, stmts map[int64]stmtCounts, featurePassed, testSeen bool) {
	pass = make(map[int64]bool, len(wanted))
	skipOnly = make(map[int64]bool, len(wanted))
	stmts = make(map[int64]stmtCounts, len(wanted))
	for _, r := range results {
		if r.SymbolID == nil {
			// Feature-level result (E2E-style; SymbolID nil, FeatureID set).
			if r.FeatureID != nil && *r.FeatureID == featureID {
				switch r.Status {
				case store.StatusPass:
					featurePassed = true
					testSeen = true
				case store.StatusFail:
					testSeen = true
				}
			}
			continue
		}
		// A result keyed to one of this feature's TEST symbols bridges
		// "test ran/passed" → "feature covered" (issue #82). Skips don't
		// count as evidence either way.
		if testSyms[*r.SymbolID] {
			switch r.Status {
			case store.StatusPass:
				featurePassed = true
				testSeen = true
			case store.StatusFail:
				testSeen = true
			}
		}
		if !wanted[*r.SymbolID] {
			continue
		}
		// Accumulate statement counts (migration 0009). A symbol may have
		// several results across files/blocks in one run; sum them so the
		// line-weighted score reflects the symbol's whole statement footprint.
		if r.TotalStmts > 0 {
			c := stmts[*r.SymbolID]
			c.covered += r.CoveredStmts
			c.total += r.TotalStmts
			stmts[*r.SymbolID] = c
		}
		switch r.Status {
		case store.StatusPass:
			pass[*r.SymbolID] = true
		case store.StatusSkip:
			if !pass[*r.SymbolID] {
				skipOnly[*r.SymbolID] = true
			}
		}
	}
	return pass, skipOnly, stmts, featurePassed, testSeen
}

// coverageRatio collapses the buckets into the (denominator, numerator)
// pair we use for the percentage. Skip-only symbols drop OUT of the
// denominator — they aren't evidence of anything.
func coverageRatio(wanted, pass, skipOnly map[int64]bool) (denom, numer int) {
	for sid := range wanted {
		if skipOnly[sid] && !pass[sid] {
			continue
		}
		denom++
	}
	for sid := range pass {
		if wanted[sid] {
			numer++
		}
	}
	return denom, numer
}

// coverageNote formats the per-feature explanatory note for the coverage
// signal. Score == 100 → no note (empty signalNote with zero weight).
func coverageNote(numer, denom int, score float64) signalNote {
	return coverageNoteWithSuffix(numer, denom, score, "")
}

// coverageNoteWithSuffix is the implementation of coverageNote with an
// optional suffix appended to the message (used by the package-anchor
// fallback to tag its coarser signal so operators can identify it).
func coverageNoteWithSuffix(numer, denom int, score float64, suffix string) signalNote {
	switch {
	case score >= 100:
		return signalNote{}
	case score == 0:
		return signalNote{
			weight:  100,
			message: fmt.Sprintf("coverage: 0/%d symbols passing in latest run%s", denom, suffix),
		}
	default:
		return signalNote{
			weight:  100 - score,
			message: fmt.Sprintf("coverage: %d/%d symbols passing (%.0f%%)%s", numer, denom, score, suffix),
		}
	}
}

// lineCoverageNote formats the per-feature explanatory note for the Tier-B
// line-weighted coverage signal: it reports executed/total STATEMENTS (not
// symbols), matching how the score is actually computed. Score == 100 → no
// note (empty signalNote with zero weight).
func lineCoverageNote(covered, total int, score float64, suffix string) signalNote {
	switch {
	case score >= 100:
		return signalNote{}
	case score == 0:
		return signalNote{
			weight:  100,
			message: fmt.Sprintf("coverage: 0/%d statements executed in latest run%s", total, suffix),
		}
	default:
		return signalNote{
			weight:  100 - score,
			message: fmt.Sprintf("coverage: %d/%d statements executed (%.0f%%)%s", covered, total, score, suffix),
		}
	}
}

// ---------------------------------------------------------------------------
// Annotation freshness signal
// ---------------------------------------------------------------------------

// annotationFreshnessSignal computes the fraction of the feature's
// annotation sites whose latest git author-date is inside the freshness
// window. The "site" is one (file_path, line) pair drawn from the
// annotations rows tied to symbols the feature references.
//
// Available when: opts.GitBlame is wired AND at least one linked symbol's
// file has an annotation whose blame returned a non-zero time.
func (a *auditImpl) annotationFreshnessSignal(
	ctx context.Context,
	links []store.FeatureSymbolLink,
	now time.Time,
) (signalResult, bool, error) {
	seenFile, err := a.filesForLinks(ctx, links)
	if err != nil {
		return signalResult{}, false, err
	}
	if len(seenFile) == 0 {
		return signalResult{}, false, nil
	}
	sites, err := a.collectAnnotationSites(ctx, seenFile)
	if err != nil {
		return signalResult{}, false, err
	}
	if len(sites) == 0 {
		return signalResult{}, false, nil
	}

	fresh, usable := a.tallyFreshness(ctx, sites, now)
	if usable == 0 {
		return signalResult{}, false, nil
	}
	score := 100.0 * float64(fresh) / float64(usable)
	note := signalNote{}
	if score < 100 {
		note = signalNote{
			weight:  (100 - score) * 0.6,
			message: fmt.Sprintf("annotation freshness: %d/%d sites within %s", fresh, usable, a.opts.FreshnessWindow),
		}
	}
	return signalResult{score: score, note: note}, true, nil
}

// annotationSite is a (file, line) annotation reference used by the
// freshness signal. Kept package-private — only the freshness helpers
// consume it.
type annotationSite struct {
	file string
	line int
}

// filesForLinks resolves each link to its symbol's file path and returns a
// deduped set. Missing symbol rows (e.g. position-less synthetic symbols)
// are skipped silently.
func (a *auditImpl) filesForLinks(ctx context.Context, links []store.FeatureSymbolLink) (map[string]bool, error) {
	seenFile := make(map[string]bool, len(links))
	for _, l := range links {
		sym, err := a.lookupSymbolByID(ctx, l.SymbolID)
		if err != nil {
			if errors.Is(err, shared.ErrSymbolNotFound) {
				continue
			}
			return nil, fmt.Errorf("lookup symbol %d: %w", l.SymbolID, err)
		}
		seenFile[sym.FilePath] = true
	}
	return seenFile, nil
}

// collectAnnotationSites loads every annotation in `files` and keeps only
// the feature/contract rows. Owner/deprecated/since rows are skipped: they
// are metadata, not signals that the feature is being actively worked on.
func (a *auditImpl) collectAnnotationSites(ctx context.Context, files map[string]bool) ([]annotationSite, error) {
	var sites []annotationSite
	for f := range files {
		rows, err := a.store.Annotations().ListByFile(ctx, f)
		if err != nil {
			return nil, fmt.Errorf("list annotations %q: %w", f, err)
		}
		for _, r := range rows {
			if r.Kind != shared.AnnFeature && r.Kind != shared.AnnContract {
				continue
			}
			sites = append(sites, annotationSite{file: r.FilePath, line: r.Line})
		}
	}
	return sites, nil
}

// tallyFreshness consults GitBlame per site and returns (fresh, usable).
// Blame errors are per-site soft failures: the site drops out of the
// denominator (not "treated as stale").
func (a *auditImpl) tallyFreshness(ctx context.Context, sites []annotationSite, now time.Time) (fresh, usable int) {
	for _, s := range sites {
		ts, err := a.opts.GitBlame.AuthorDate(ctx, s.file, s.line)
		if err != nil || ts.IsZero() {
			continue
		}
		usable++
		if now.Sub(ts) <= a.opts.FreshnessWindow {
			fresh++
		}
	}
	return fresh, usable
}

// lookupSymbolByID resolves a feature_symbols.symbol_id to a SymbolRow.
//
// The store doesn't expose a per-id lookup directly. We materialise the
// full Symbols table once per Audit instance into a map and serve from
// there — for the typical project (~10k symbols, ~200 features, ~50
// links/feature) this turns ScoreAll's symbol-lookup cost from O(N*M^2)
// to O(M + N*links) where M is the symbol count and N is the feature
// count.
func (a *auditImpl) lookupSymbolByID(ctx context.Context, id int64) (store.SymbolRow, error) {
	if a.symbolCache == nil {
		rows, err := a.store.Symbols().List(ctx, store.SymbolFilter{})
		if err != nil {
			return store.SymbolRow{}, fmt.Errorf("symbols list: %w", err)
		}
		a.symbolCache = make(map[int64]store.SymbolRow, len(rows))
		for _, r := range rows {
			a.symbolCache[r.ID] = r
		}
	}
	row, ok := a.symbolCache[id]
	if !ok {
		return store.SymbolRow{}, shared.ErrSymbolNotFound
	}
	return row, nil
}

// implSurfaceMaxDepth bounds the call-graph walk from a feature's annotated
// symbols to its production footprint. 3 captures "test → entry impl → a hop
// or two of collaborators" without dragging in the whole transitive tree.
const implSurfaceMaxDepth = 3

// callAdjacency lazily loads (once) the whole `call`-edge adjacency.
func (a *auditImpl) callAdjacency(ctx context.Context) (map[int64][]int64, error) {
	if !a.callAdjLoaded {
		adj, err := a.store.Edges().CallAdjacency(ctx)
		if err != nil {
			return nil, fmt.Errorf("call adjacency: %w", err)
		}
		a.callAdj = adj
		a.callAdjLoaded = true
	}
	return a.callAdj, nil
}

// featureImplSurface returns the PRODUCTION symbol ids reachable via `call`
// edges (≤ implSurfaceMaxDepth) from the feature's linked symbols — the
// executable footprint its annotated tests exercise. Used as the coverage
// denominator so coverage reflects real production execution (from a
// coverprofile run) instead of literal test-symbol name matches. Empty when
// no call edges resolve (e.g. e2e-only features), in which case the caller
// falls back to the direct-link / test-pass model. See issue #82.
func (a *auditImpl) featureImplSurface(ctx context.Context, links []store.FeatureSymbolLink) (map[int64]bool, error) {
	adj, err := a.callAdjacency(ctx)
	if err != nil {
		return nil, err
	}
	visited := make(map[int64]bool, len(links))
	frontier := make([]int64, 0, len(links))
	for _, l := range links {
		if !visited[l.SymbolID] {
			visited[l.SymbolID] = true
			frontier = append(frontier, l.SymbolID)
		}
	}
	for d := 0; d < implSurfaceMaxDepth && len(frontier) > 0; d++ {
		var next []int64
		for _, id := range frontier {
			for _, to := range adj[id] {
				if !visited[to] {
					visited[to] = true
					next = append(next, to)
				}
			}
		}
		frontier = next
	}
	impl := map[int64]bool{}
	for id := range visited {
		row, err := a.lookupSymbolByID(ctx, id)
		if err != nil {
			continue
		}
		if isProductionFile(row.FilePath) {
			impl[id] = true
		}
	}
	return impl, nil
}

// featurePackageAnchorSurface is the THIRD-TIER fallback for coverageSignal
// (issue #84). It fires only when featureImplSurface returns empty AND the
// direct wantedSymbolIDs model yields no coverage hits — the case where
// annotation roots are test stubs with no outgoing call edges (nopXxx,
// noopXxx, mockXxx patterns emitted by the Go scanner as zero-edge nodes).
//
// The function:
//  1. Collects the Go package name from each linked symbol via the symbolCache.
//  2. For each distinct non-empty package, counts the production-file symbols
//     in that package (already loaded in symbolCache).
//  3. If a package's production-symbol count is ≤ maxPackageAnchor, adds all
//     those symbols to the surface.
//
// The guard prevents large shared packages (e.g. infrastructure/http/handlers
// with 896 symbols) from producing a flat unrelated score. Only focused domain
// packages (application/services, domain/aggregates, etc.) pass the guard.
//
// Returns an empty map when no linked symbol carries a Package value or every
// matching package exceeds the size limit — the caller then falls back to the
// annotation_presence floor unchanged.
func (a *auditImpl) featurePackageAnchorSurface(ctx context.Context, links []store.FeatureSymbolLink, maxPackageAnchor int) (map[int64]bool, error) {
	if maxPackageAnchor <= 0 {
		return nil, nil
	}
	// Gather the distinct package names from linked symbols.
	// lookupSymbolByID populates a.symbolCache on first call, so the
	// subsequent cache walk is O(N) over already-loaded data.
	pkgNames := make(map[string]bool, len(links))
	for _, l := range links {
		row, err := a.lookupSymbolByID(ctx, l.SymbolID)
		if err != nil {
			continue
		}
		if row.Package != nil && *row.Package != "" {
			pkgNames[*row.Package] = true
		}
	}
	if len(pkgNames) == 0 {
		return nil, nil
	}

	// Build a package → []production-symbol-ids index from the cache.
	// O(N) single pass over the already-loaded symbol cache.
	pkgProdSymbols := make(map[string][]int64, len(pkgNames))
	for id, row := range a.symbolCache {
		if row.Package == nil || *row.Package == "" {
			continue
		}
		if !pkgNames[*row.Package] {
			continue
		}
		if !isProductionFile(row.FilePath) {
			continue
		}
		pkgProdSymbols[*row.Package] = append(pkgProdSymbols[*row.Package], id)
	}

	// Add production symbols from packages that pass the size guard.
	surface := make(map[int64]bool)
	for _, symIDs := range pkgProdSymbols {
		if len(symIDs) > maxPackageAnchor {
			// Package is too large — likely a shared infrastructure monolith;
			// skip to avoid attributing unrelated execution to this feature.
			continue
		}
		for _, id := range symIDs {
			surface[id] = true
		}
	}
	return surface, nil
}

// allTestFileWanted returns true when every symbol in `wanted` is a test-file
// symbol (i.e. !isProductionFile). This is used by the package-anchor fallback
// guard: if any wanted symbol IS a production file, the direct-link model can
// already give meaningful coverage attribution and the fallback should not fire.
//
// An empty `wanted` set returns true (vacuously — the caller gates on numer==0
// which also catches the denom==0 case, so an empty wanted set reaching here
// means something else is providing signal via testSeen).
func allTestFileWanted(wanted map[int64]bool, cache map[int64]store.SymbolRow) bool {
	for sid := range wanted {
		row, ok := cache[sid]
		if !ok {
			continue
		}
		if isProductionFile(row.FilePath) {
			return false // at least one production-file symbol in wanted → don't use fallback
		}
	}
	return true
}

// isProductionFile reports whether a path is non-test production source.
func isProductionFile(p string) bool {
	if p == "" {
		return false
	}
	if strings.Contains(p, "/__tests__/") {
		return false
	}
	for _, suf := range []string{"_test.go", ".test.ts", ".test.tsx", ".spec.ts", ".spec.tsx", ".test.js", ".spec.js"} {
		if strings.HasSuffix(p, suf) {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Pattern compliance signal
// ---------------------------------------------------------------------------

// patternComplianceSignal computes the canonical-service compliance score
// for features that have linked aggregates. The feature has aggregates when
// at least one of its linked symbols' files declares an `@atlas:aggregate`
// AND an `@atlas:aggregate-service` annotation. For each aggregate-service
// site, the matching canonical-service pattern hit (by file path) counts
// toward the numerator; aggregates without a hit count toward the denominator
// but not the numerator.
//
// Returns (zero, false, nil) cleanly when no aggregate is linked — the
// caller skips the signal in that case (no penalty).
func (a *auditImpl) patternComplianceSignal(
	ctx context.Context,
	_ shared.FeatureID,
	links []store.FeatureSymbolLink,
) (signalResult, bool, error) {
	seenFile, err := a.filesForLinks(ctx, links)
	if err != nil {
		return signalResult{}, false, err
	}
	if len(seenFile) == 0 {
		return signalResult{}, false, nil
	}
	svcSites, err := a.collectAggregateServiceSites(ctx, seenFile)
	if err != nil {
		return signalResult{}, false, err
	}
	if len(svcSites) == 0 {
		return signalResult{}, false, nil
	}
	hitByFile, err := a.canonicalServiceHits(ctx)
	if err != nil {
		return signalResult{}, false, err
	}

	denom := len(svcSites)
	numer := 0
	var missing []string
	for _, s := range svcSites {
		if hitByFile[s.file] {
			numer++
			continue
		}
		missing = append(missing, s.id)
	}

	score := 100.0 * float64(numer) / float64(denom)
	return signalResult{score: score, note: patternNote(numer, denom, score, missing)}, true, nil
}

// aggregateServiceSite is a (file, aggregate-id) pair drawn from a single
// `@atlas:aggregate-service <id>` annotation row.
type aggregateServiceSite struct {
	file string
	id   string
}

// collectAggregateServiceSites walks each file's annotations and keeps the
// aggregate-service rows. Multi-id is not yet supported here (matches the
// audit-side simplifying assumption that one annotation declares one id).
func (a *auditImpl) collectAggregateServiceSites(ctx context.Context, files map[string]bool) ([]aggregateServiceSite, error) {
	var out []aggregateServiceSite
	for f := range files {
		rows, err := a.store.Annotations().ListByFile(ctx, f)
		if err != nil {
			return nil, fmt.Errorf("list annotations %q: %w", f, err)
		}
		for _, r := range rows {
			if r.Kind != shared.AnnAggregateService {
				continue
			}
			id := strings.TrimSpace(strings.Fields(r.Value)[0])
			out = append(out, aggregateServiceSite{file: r.FilePath, id: id})
		}
	}
	return out, nil
}

// canonicalServiceHits returns the set of file paths that contain at least
// one `canonical-service` pattern match. Cached per call only — the
// underlying Symbols.FindByPattern is one query.
func (a *auditImpl) canonicalServiceHits(ctx context.Context) (map[string]bool, error) {
	hits, err := a.store.Symbols().FindByPattern(ctx, patternsCanonicalServiceName)
	if err != nil {
		return nil, fmt.Errorf("find canonical-service: %w", err)
	}
	hitByFile := make(map[string]bool, len(hits))
	for _, h := range hits {
		hitByFile[h.FilePath] = true
	}
	return hitByFile, nil
}

// patternNote builds the per-feature explanatory note for the pattern
// signal. Score == 100 → empty note (no weight, no message).
func patternNote(numer, denom int, score float64, missing []string) signalNote {
	if score >= 100 {
		return signalNote{}
	}
	topMissing := missing
	if len(topMissing) > 3 {
		topMissing = topMissing[:3]
	}
	return signalNote{
		weight: 100 - score,
		message: fmt.Sprintf("pattern compliance: %d/%d aggregate-services match canonical pattern (missing: %s)",
			numer, denom, strings.Join(topMissing, ", ")),
	}
}

// ---------------------------------------------------------------------------
// Contract drift signal
// ---------------------------------------------------------------------------

// contractDriftSignal computes the fraction of features-of-kind-contract
// referenced by this feature whose `updated_at` falls inside the
// ContractDriftWindow. "Referenced by" means: the contract feature row has
// a feature_symbols link to at least one of the same symbols this feature
// links to, OR (looser) it shares the prefix of the feature id.
//
// For Phase 6a we use the simpler-and-correct definition: the feature
// itself is of kind "contract" — drift is on contract rows specifically.
// When the audited feature is NOT a contract, we look for contracts that
// share symbols with this feature.
//
// Returns (zero, false, nil) when no contract is linked.
func (a *auditImpl) contractDriftSignal(
	ctx context.Context,
	featureID shared.FeatureID,
	now time.Time,
) (signalResult, bool, error) {
	contractIDs, ok, err := a.contractCandidates(ctx, featureID)
	if err != nil {
		return signalResult{}, false, err
	}
	if !ok || len(contractIDs) == 0 {
		return signalResult{}, false, nil
	}
	ids := make([]shared.FeatureID, 0, len(contractIDs))
	for id := range contractIDs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	contracts, err := a.store.Features().List(ctx, store.FeatureFilter{IDs: ids})
	if err != nil {
		return signalResult{}, false, fmt.Errorf("list contract features: %w", err)
	}
	if len(contracts) == 0 {
		return signalResult{}, false, nil
	}
	numer, denom, stale := tallyContractDrift(contracts, now, a.opts.ContractDriftWindow)
	if denom == 0 {
		return signalResult{}, false, nil
	}
	score := 100.0 * float64(numer) / float64(denom)
	return signalResult{score: score, note: contractDriftNote(numer, denom, score, stale, a.opts.ContractDriftWindow)}, true, nil
}

// contractCandidates returns the set of contract feature ids that should
// be measured for drift on behalf of `featureID`:
//
//   - every contract feature linked to a symbol this feature touches under
//     role=contract.
//   - the feature itself, if it is of kind=contract.
//
// Returns (_, false, nil) when the feature row itself doesn't exist.
func (a *auditImpl) contractCandidates(ctx context.Context, featureID shared.FeatureID) (map[shared.FeatureID]bool, bool, error) {
	links, err := a.store.FeatureSymbols().ListByFeature(ctx, featureID)
	if err != nil {
		return nil, false, fmt.Errorf("list feature_symbols: %w", err)
	}
	contractIDs := make(map[shared.FeatureID]bool)
	for _, l := range links {
		if l.Role != store.RoleContract {
			continue
		}
		rows, err := a.store.FeatureSymbols().ListBySymbol(ctx, l.SymbolID)
		if err != nil {
			return nil, false, fmt.Errorf("list feature_symbols by symbol %d: %w", l.SymbolID, err)
		}
		for _, r := range rows {
			if r.Role == store.RoleContract {
				contractIDs[r.FeatureID] = true
			}
		}
	}
	thisFeat, err := a.store.Features().Get(ctx, featureID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, shared.ErrFeatureNotFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("get feature %q: %w", featureID, err)
	}
	if thisFeat.Kind == store.FeatureKindContract {
		contractIDs[featureID] = true
	}
	return contractIDs, true, nil
}

// tallyContractDrift returns (numer, denom, staleIDs) where numer counts
// contracts fresh within `window`, denom is the total contract count
// (kind == contract), and staleIDs lists the over-window ones.
func tallyContractDrift(contracts []store.Feature, now time.Time, window time.Duration) (numer, denom int, stale []string) {
	for _, c := range contracts {
		if c.Kind != store.FeatureKindContract {
			continue
		}
		denom++
		if now.Sub(c.UpdatedAt) <= window {
			numer++
		} else {
			stale = append(stale, string(c.ID))
		}
	}
	return numer, denom, stale
}

// contractDriftNote formats the explanatory note. Empty note when score
// is 100.
func contractDriftNote(numer, denom int, score float64, stale []string, window time.Duration) signalNote {
	if score >= 100 {
		return signalNote{}
	}
	topStale := stale
	if len(topStale) > 3 {
		topStale = topStale[:3]
	}
	return signalNote{
		weight: (100 - score) * 0.8,
		message: fmt.Sprintf("contract drift: %d/%d contracts validated within %s (stale: %s)",
			numer, denom, window, strings.Join(topStale, ", ")),
	}
}
