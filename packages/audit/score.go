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
// (already loaded by the caller) plus the latest coverage_run id (if any)
// and produces the FeatureHealth record.
//
// Each signal independently reports "available?" — when unavailable, the
// weighted-average step re-normalises over the remaining signals. A feature
// with no aggregates and no contracts gets a fair score from coverage +
// freshness alone (rather than being penalised toward zero).
func (a *auditImpl) scoreFromFeature(
	ctx context.Context,
	feat store.Feature,
	latestRun int64,
	hasCov bool,
) (FeatureHealth, error) {
	now := a.opts.Now()
	links, err := a.store.FeatureSymbols().ListByFeature(ctx, feat.ID)
	if err != nil {
		return FeatureHealth{}, fmt.Errorf("list feature_symbols: %w", err)
	}

	components := make(map[string]float64)
	available := make(map[string]bool)
	var notes []signalNote

	// --- Coverage signal -------------------------------------------------
	if hasCov && len(links) > 0 {
		cov, ok, err := a.coverageSignal(ctx, feat.ID, links, latestRun)
		if err != nil {
			return FeatureHealth{}, fmt.Errorf("coverage signal: %w", err)
		}
		if ok {
			components[SignalCoverage] = cov.score
			available[SignalCoverage] = true
			notes = append(notes, cov.note)
		}
	}

	// --- Annotation freshness signal ------------------------------------
	if a.opts.GitBlame != nil && len(links) > 0 {
		fresh, ok, err := a.annotationFreshnessSignal(ctx, links, now)
		if err != nil {
			return FeatureHealth{}, fmt.Errorf("annotation freshness signal: %w", err)
		}
		if ok {
			components[SignalAnnotationFresh] = fresh.score
			available[SignalAnnotationFresh] = true
			notes = append(notes, fresh.note)
		}
	}

	// --- Pattern compliance signal --------------------------------------
	pat, ok, err := a.patternComplianceSignal(ctx, feat.ID, links)
	if err != nil {
		return FeatureHealth{}, fmt.Errorf("pattern compliance signal: %w", err)
	}
	if ok {
		components[SignalPatternCompliance] = pat.score
		available[SignalPatternCompliance] = true
		notes = append(notes, pat.note)
	}

	// --- Contract drift signal ------------------------------------------
	drift, ok, err := a.contractDriftSignal(ctx, feat.ID, now)
	if err != nil {
		return FeatureHealth{}, fmt.Errorf("contract drift signal: %w", err)
	}
	if ok {
		components[SignalContractDrift] = drift.score
		available[SignalContractDrift] = true
		notes = append(notes, drift.note)
	}

	score := weightedAverage(components, available, a.opts.Weights)
	if len(available) == 0 {
		// No signal at all — the feature has no coverage, no blame source,
		// no aggregates, no contracts. Score stays 0 with an explanatory
		// reason so consumers can distinguish "0 because broken" from
		// "0 because we don't know anything yet."
		notes = append(notes, signalNote{
			weight:  100,
			message: "no audit signals available (no coverage, no aggregate, no contract, no annotation source)",
		})
	}

	return FeatureHealth{
		FeatureID:  feat.ID,
		Score:      score,
		Components: components,
		Reasons:    topReasons(notes, 3),
		SampledAt:  now,
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
// have at least one `pass` coverage result in the latest run. "Skip"-only
// results are treated as NO SIGNAL — they don't count toward the
// denominator. Otherwise a feature whose tests are explicitly disabled in
// CI would always score 0%, which is the wrong reading.
//
// Returns (result, true, nil) when at least one symbol has a usable result.
// Returns (zero, false, nil) when every linked symbol is skip-only or
// completely absent from the run.
func (a *auditImpl) coverageSignal(
	ctx context.Context,
	featureID shared.FeatureID,
	links []store.FeatureSymbolLink,
	runID int64,
) (signalResult, bool, error) {
	wanted := wantedSymbolIDs(links)
	if len(wanted) == 0 {
		return signalResult{}, false, nil
	}
	results, err := a.store.Coverage().ListResults(ctx, runID)
	if err != nil {
		return signalResult{}, false, fmt.Errorf("list coverage results: %w", err)
	}
	pass, skipOnly, featurePassed := classifyCoverageResults(results, wanted, featureID)
	if featurePassed {
		// E2E-style coverage applies to the whole feature; credit every
		// linked symbol so the signal reflects "the user-visible feature
		// works" rather than "we couldn't map the test to a symbol."
		for sid := range wanted {
			pass[sid] = true
		}
	}
	denom, numer := coverageRatio(wanted, pass, skipOnly)
	if denom == 0 {
		return signalResult{}, false, nil
	}
	score := 100.0 * float64(numer) / float64(denom)
	return signalResult{score: score, note: coverageNote(numer, denom, score)}, true, nil
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
	featureID shared.FeatureID,
) (pass, skipOnly map[int64]bool, featurePassed bool) {
	pass = make(map[int64]bool, len(wanted))
	skipOnly = make(map[int64]bool, len(wanted))
	for _, r := range results {
		if r.SymbolID == nil {
			if r.FeatureID != nil && *r.FeatureID == featureID && r.Status == store.StatusPass {
				featurePassed = true
			}
			continue
		}
		if !wanted[*r.SymbolID] {
			continue
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
	return pass, skipOnly, featurePassed
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
	switch {
	case score >= 100:
		return signalNote{}
	case score == 0:
		return signalNote{
			weight:  100,
			message: fmt.Sprintf("coverage: 0/%d symbols passing in latest run", denom),
		}
	default:
		return signalNote{
			weight:  100 - score,
			message: fmt.Sprintf("coverage: %d/%d symbols passing (%.0f%%)", numer, denom, score),
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
