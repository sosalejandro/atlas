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

	// SurfaceSource names how the feature's implementation surface was
	// derived: dynamic (per-test execution evidence), static (call-edge
	// walk), package-anchor, or direct-links. A coverage number whose
	// provenance is invisible is how issue #84 survived three releases, so
	// every score carries it.
	SurfaceSource string `json:"surface_source,omitempty"`
}

// Signal names — closed enum used as keys in FeatureHealth.Components.
//
// Keys are stable identifiers; the human-readable Reasons explain the score.
const (
	SignalCoverage          = "coverage"
	SignalAnnotationFresh   = "annotation_freshness"
	SignalPatternCompliance = "pattern_compliance"
	SignalContractDrift     = "contract_drift"
	// SignalAnnotationPresence fires when the feature has at least one linked
	// symbol in the feature_symbols table. This is the same signal that
	// `atlas trace feature:<id>` consumes — when trace resolves a chain,
	// this signal is present and non-zero. It prevents a feature from scoring
	// 0 with "no annotation source" solely because coverage/git/aggregate/
	// contract data hasn't been ingested yet, even though the feature IS
	// annotated in code. Weight is intentionally low (0.10) so it never
	// dominates the score when the richer signals are available.
	SignalAnnotationPresence = "annotation_presence"
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

	// MaxPackageAnchorSymbols gates the package-anchor fallback introduced in
	// issue #84. When call-edge traversal yields no production impl surface,
	// coverageSignal falls back to all production symbols in the Go packages
	// co-located with the feature's annotated test symbols — but ONLY when
	// those packages contain at most MaxPackageAnchorSymbols production symbols.
	//
	// The guard prevents large shared packages (e.g. infrastructure/http/handlers
	// with 896 symbols) from polluting scores with unrelated execution results.
	// Focused domain packages (application/services, domain/aggregates, etc.) stay
	// well under this threshold and benefit from the fallback.
	//
	// Default: 200. Set to 0 to disable the fallback entirely.
	MaxPackageAnchorSymbols int

	// UbiquityCutoff is the fraction of the test suite above which a symbol
	// counts as shared runtime rather than any feature's implementation, used
	// by the dynamic surface derivation (issue #104). A symbol executed by
	// more than this share of tests is the logger, the DI container or the
	// middleware chain - real code, but not evidence of a relationship to the
	// feature under test.
	//
	// Default: 0.5. Ignored for suites smaller than a handful of tests, where
	// the ratio carries no signal.
	UbiquityCutoff float64
}

// defaultUbiquityCutoff and minTestsForUbiquityCutoff govern the dynamic
// surface's shared-runtime filter. The floor exists because in a five-test
// suite "executed by more than half the tests" describes a shared domain
// service, not framework plumbing.
const (
	defaultUbiquityCutoff     = 0.5
	minTestsForUbiquityCutoff = 8
)

// defaultWeights returns the spec-default signal weights.
func defaultWeights() map[string]float64 {
	return map[string]float64{
		SignalCoverage:           0.40,
		SignalAnnotationFresh:    0.15,
		SignalPatternCompliance:  0.25,
		SignalContractDrift:      0.20,
		SignalAnnotationPresence: 0.10,
	}
}

// defaultMaxPackageAnchorSymbols is the default cap for the package-anchor
// fallback (issue #84). 200 admits focused domain packages while rejecting
// shared infrastructure monoliths like handlers (896+ symbols).
const defaultMaxPackageAnchorSymbols = 200

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
	if o.MaxPackageAnchorSymbols == 0 {
		o.MaxPackageAnchorSymbols = defaultMaxPackageAnchorSymbols
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

	// lastSurfaceSource records how the most recent coverage signal derived
	// its impl surface, so scoreFromFeature can report it. Scoring is
	// sequential per feature, which is what makes this safe; a parallel
	// scorer would carry it through the signal result instead.
	lastSurfaceSource string

	// covPool memoises the carryforward resolution of ONE frontier
	// (issue #136). Resolution depends on the frontier and the window, not on
	// the feature, but it costs three grouped scans over the carry window;
	// resolving it per feature made ScoreAll pay that N times for an identical
	// answer. covPoolKey identifies the frontier the cached pool belongs to,
	// and covPoolReady distinguishes "not resolved yet" from "resolved and
	// empty" — an empty pool is a real answer for a store with no coverage.
	covPool      coveragePool
	covPoolKey   string
	covPoolReady bool

	// callAdj lazily holds the whole `call`-edge adjacency (from→[]to),
	// loaded once for per-feature impl-surface BFS. nil = not yet loaded;
	// callAdjLoaded distinguishes "not loaded" from "loaded but empty".
	callAdj       map[int64][]int64
	callAdjLoaded bool
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
	// Resolve the frontier once per ScoreAll call — looking it up per-feature
	// would multiply DB chatter by O(features).
	frontier, err := a.latestCoverageFrontier(ctx)
	if err != nil {
		return nil, fmt.Errorf("audit ScoreAll: %w", err)
	}

	out := make([]FeatureHealth, 0, len(feats))
	for _, feat := range feats {
		health, err := a.scoreFromFeature(ctx, feat, frontier)
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
	frontier, err := a.latestCoverageFrontier(ctx)
	if err != nil {
		return FeatureHealth{}, err
	}
	return a.scoreFromFeature(ctx, feat, frontier)
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

// latestCoverageFrontier returns the coverage runs the audit scores against.
//
// Atlas treats a project as having ONE current coverage frontier, but a
// polyglot repo builds that frontier out of several runs — one per framework
// per CI build. The store resolves them from the newest run outward: a run
// tagged with a run group brings its whole group along, an untagged run
// stands alone (which is the pre-#86 "newest run wins" behaviour, so every
// store ingested before run groups scores exactly as it did).
//
// An empty frontier means no coverage has been ingested — NOT an error.
func (a *auditImpl) latestCoverageFrontier(ctx context.Context) (store.CoverageFrontier, error) {
	front, err := a.store.Coverage().LatestFrontier(ctx)
	if err != nil {
		return store.CoverageFrontier{}, fmt.Errorf("latest coverage frontier: %w", err)
	}
	return front, nil
}

// patternsCanonicalServiceName is exported indirectly: we import
// codeindex/patterns to use the same string constant the recogniser
// emits. Keeps audit/ in lockstep with patterns/ — if PatternCanonicalService
// is ever renamed, this fails to compile.
var patternsCanonicalServiceName = patterns.PatternCanonicalService
