package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/sosalejandro/atlas/packages/codeindex/patterns"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// FeatureHealth is the per-feature health record produced by Audit.ScoreFeature.
//
// Score is in 0..100 (weighted blend of available signals). Components carries
// each signal's individual score, also in 0..100. Reasons holds the top
// human-readable reasons why Score isn't 100 — the audit package emits at
// most 3.
type FeatureHealth struct {
	FeatureID  shared.FeatureID   `json:"feature_id"`
	Score      float64            `json:"score"`
	Components map[string]float64 `json:"components"`
	Reasons    []string           `json:"reasons,omitempty"`
	SampledAt  time.Time          `json:"sampled_at"`
}

// Signal names — closed enum used as keys in FeatureHealth.Components.
//
// Keys are stable identifiers; the human-readable Reasons explain the score.
const (
	SignalCoverage           = "coverage"
	SignalAnnotationFresh    = "annotation_freshness"
	SignalPatternCompliance  = "pattern_compliance"
	SignalContractDrift      = "contract_drift"
)

// Options tunes the audit algorithm.
//
// The defaults match the Phase 6a spec (40/15/25/20 weights, 30-day freshness
// windows). Override individual fields; zero/unset fields fall back to the
// default.
type Options struct {
	// Weights blend the four component signals. Must sum to a positive
	// value; the implementation re-normalises when a signal is unavailable.
	Weights map[string]float64

	// FreshnessWindow defines the cut-off for the annotation_freshness
	// signal. An annotation's source line is "fresh" when the latest git
	// author-date for that line is within this window. Default: 30 days.
	FreshnessWindow time.Duration

	// ContractDriftWindow defines the cut-off for the contract_drift
	// signal. A contract's `features.updated_at` must fall within this
	// window for the contract to count as "current". Default: 30 days.
	ContractDriftWindow time.Duration

	// GitBlame is the adapter that returns the latest git author-date for
	// a (filePath, line) pair. Nil = no annotation freshness signal
	// available; the score recomputes without it.
	//
	// Production wires audit.NewGitBlame(projectRoot); tests pass a stub.
	GitBlame GitBlameSource

	// Now overrides time.Now() for deterministic tests. Zero = use real time.
	Now func() time.Time
}

// defaultWeights returns the spec-default signal weights.
func defaultWeights() map[string]float64 {
	return map[string]float64{
		SignalCoverage:          0.40,
		SignalAnnotationFresh:   0.15,
		SignalPatternCompliance: 0.25,
		SignalContractDrift:     0.20,
	}
}

// applyDefaults fills zero-valued Options fields with the package defaults.
func (o Options) applyDefaults() Options {
	if o.Weights == nil {
		o.Weights = defaultWeights()
	}
	if o.FreshnessWindow <= 0 {
		o.FreshnessWindow = 30 * 24 * time.Hour
	}
	if o.ContractDriftWindow <= 0 {
		o.ContractDriftWindow = 30 * 24 * time.Hour
	}
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	return o
}

// Audit is the package's public API. ScoreFeature / ScoreAll compute
// FeatureHealth records; PersistSnapshot / LoadSnapshot round-trip the
// scoring output through the `audit_snapshot_runs` table.
type Audit interface {
	ScoreFeature(ctx context.Context, id shared.FeatureID) (FeatureHealth, error)
	ScoreAll(ctx context.Context) ([]FeatureHealth, error)
	PersistSnapshot(ctx context.Context, scores []FeatureHealth) (int64, error)
	LoadSnapshot(ctx context.Context, snapshotID int64) ([]FeatureHealth, error)
}

// auditImpl is the concrete Audit backed by a packages/store.Store and the
// Options-supplied signal sources.
type auditImpl struct {
	store *store.Store
	opts  Options

	// symbolCache lazily holds every symbol row indexed by surrogate id.
	// Populated on first lookup; reused for the lifetime of the Audit
	// instance. The cache is per-Audit (not per-call) so ScoreAll's N-feature
	// loop pays the single Symbols.List cost ONCE, not N times.
	//
	// nil = not yet populated.
	symbolCache map[int64]store.SymbolRow
}

// New returns an Audit backed by the given store, using the supplied
// Options. Pass Options{} to take all defaults.
func New(s *store.Store, opts Options) Audit {
	if s == nil {
		// Caller error — return an Audit that always fails fast so users see
		// the wiring bug immediately.
		return &nilStoreAudit{}
	}
	return &auditImpl{store: s, opts: opts.applyDefaults()}
}

// nilStoreAudit is the failing-fast Audit returned by New(nil, _). Every
// method returns a typed error so misuse is caught at the first call.
type nilStoreAudit struct{}

var errNilStore = errors.New("audit: store is nil")

func (*nilStoreAudit) ScoreFeature(context.Context, shared.FeatureID) (FeatureHealth, error) {
	return FeatureHealth{}, errNilStore
}
func (*nilStoreAudit) ScoreAll(context.Context) ([]FeatureHealth, error) { return nil, errNilStore }
func (*nilStoreAudit) PersistSnapshot(context.Context, []FeatureHealth) (int64, error) {
	return 0, errNilStore
}
func (*nilStoreAudit) LoadSnapshot(context.Context, int64) ([]FeatureHealth, error) {
	return nil, errNilStore
}

// ScoreFeature computes the FeatureHealth record for one feature.
//
// Returns shared.ErrFeatureNotFound when no feature row exists for id.
func (a *auditImpl) ScoreFeature(ctx context.Context, id shared.FeatureID) (FeatureHealth, error) {
	feat, err := a.store.Features().Get(ctx, id)
	if err != nil {
		return FeatureHealth{}, fmt.Errorf("audit ScoreFeature %q: %w", id, err)
	}
	return a.scoreOne(ctx, feat)
}

// ScoreAll computes FeatureHealth for every feature in the store. Results
// are sorted ascending by Score (worst first) — the natural ordering for a
// "what's most broken" view.
//
// Returns an empty slice when the store has no features (no error).
func (a *auditImpl) ScoreAll(ctx context.Context) ([]FeatureHealth, error) {
	feats, err := a.store.Features().List(ctx, store.FeatureFilter{})
	if err != nil {
		return nil, fmt.Errorf("audit ScoreAll: list features: %w", err)
	}
	if len(feats) == 0 {
		return []FeatureHealth{}, nil
	}
	// Cache the latest coverage run once per ScoreAll call — looking it up
	// per-feature would multiply DB chatter by O(features).
	latest, hasCov, err := a.latestCoverageRun(ctx)
	if err != nil {
		return nil, fmt.Errorf("audit ScoreAll: latest coverage: %w", err)
	}

	out := make([]FeatureHealth, 0, len(feats))
	for _, feat := range feats {
		health, err := a.scoreFromFeature(ctx, feat, latest, hasCov)
		if err != nil {
			return nil, fmt.Errorf("audit ScoreAll: %q: %w", feat.ID, err)
		}
		out = append(out, health)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score < out[j].Score
		}
		return out[i].FeatureID < out[j].FeatureID
	})
	return out, nil
}

// scoreOne wraps scoreFromFeature with a per-call coverage lookup. Used by
// ScoreFeature where there is no batch-amortisation opportunity.
func (a *auditImpl) scoreOne(ctx context.Context, feat store.Feature) (FeatureHealth, error) {
	latest, hasCov, err := a.latestCoverageRun(ctx)
	if err != nil {
		return FeatureHealth{}, fmt.Errorf("latest coverage: %w", err)
	}
	return a.scoreFromFeature(ctx, feat, latest, hasCov)
}

// PersistSnapshot serialises scores as JSON and writes one row into the
// `audit_snapshot_runs` table. Returns the snapshot's row id, which
// LoadSnapshot consumes verbatim.
//
// Empty scores → still writes a row (with `[]` body). Callers that want
// "don't write empty" should branch before calling.
func (a *auditImpl) PersistSnapshot(ctx context.Context, scores []FeatureHealth) (int64, error) {
	body, err := json.Marshal(scores)
	if err != nil {
		return 0, fmt.Errorf("audit PersistSnapshot: marshal: %w", err)
	}
	id, err := a.store.AuditSnapshotRuns().Insert(ctx, store.AuditSnapshotRun{
		ComputedAt: a.opts.Now(),
		ScoreJSON:  string(body),
	})
	if err != nil {
		return 0, fmt.Errorf("audit PersistSnapshot: insert: %w", err)
	}
	return id, nil
}

// LoadSnapshot reads back the FeatureHealth slice for a snapshot id.
// Returns shared.ErrNotFound when no snapshot with that id exists.
func (a *auditImpl) LoadSnapshot(ctx context.Context, snapshotID int64) ([]FeatureHealth, error) {
	row, err := a.store.AuditSnapshotRuns().Get(ctx, snapshotID)
	if err != nil {
		return nil, fmt.Errorf("audit LoadSnapshot %d: %w", snapshotID, err)
	}
	var out []FeatureHealth
	if row.ScoreJSON == "" {
		return []FeatureHealth{}, nil
	}
	if err := json.Unmarshal([]byte(row.ScoreJSON), &out); err != nil {
		return nil, fmt.Errorf("audit LoadSnapshot %d: unmarshal: %w", snapshotID, err)
	}
	if out == nil {
		out = []FeatureHealth{}
	}
	return out, nil
}

// latestCoverageRun returns the latest coverage_run.ID and a `hasCov` bool
// indicating whether any coverage run has been ingested. The "latest" choice
// is the most recent finished_at across all frameworks — Atlas treats a
// project as having ONE current coverage frontier, even when multiple
// frameworks contribute to it.
//
// When no coverage runs exist, returns (0, false, nil) — NOT an error.
func (a *auditImpl) latestCoverageRun(ctx context.Context) (int64, bool, error) {
	runs, err := a.store.Coverage().ListRuns(ctx, "")
	if err != nil {
		return 0, false, fmt.Errorf("list coverage runs: %w", err)
	}
	if len(runs) == 0 {
		return 0, false, nil
	}
	// ListRuns returns rows in stored order — pick the row with the highest
	// finished_at to be framework-agnostic.
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

// patternsCanonicalServiceName is exported indirectly: we import
// codeindex/patterns to use the same string constant the recogniser
// emits. Keeps audit/ in lockstep with patterns/ — if PatternCanonicalService
// is ever renamed, this fails to compile.
var patternsCanonicalServiceName = patterns.PatternCanonicalService
