package diff

import (
	"time"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/contract"
	"github.com/sosalejandro/atlas/packages/shared"
)

// Snapshot is the input shape Compute consumes. One Snapshot is the full
// state of an atlas-indexed project at a given git ref.
//
// Fields:
//
//   - GitRef       — git SHA, ref-name, or arbitrary tag. Never the empty
//     string for snapshots loaded from the store.
//   - CapturedAt   — wallclock the snapshot was persisted. UTC.
//   - Index        — the codeindex.Index payload. Required for every diff
//     dimension except AuditDelta + CoverageDelta (those use Audit /
//     Coverage below).
//   - Audit        — the per-feature health slice. Nil OR empty when the
//     audit pass had not yet run at this ref; AuditDelta surfaces that
//     state via MissingOnA / MissingOnB rather than as a noisy "all
//     features removed" delta.
//   - Coverage     — per-feature coverage summary. Optional; CoverageDelta
//     reports Missing flags when one side has no coverage data at all.
type Snapshot struct {
	GitRef     string            `json:"git_ref"`
	CapturedAt time.Time         `json:"captured_at"`
	Index      *codeindex.Index  `json:"index"`
	Audit      []FeatureHealth   `json:"audit,omitempty"`
	Coverage   []FeatureCoverage `json:"coverage,omitempty"`

	// Contracts is the pre-extracted contract.ContractDef list for this
	// snapshot, when the persistence pipeline ran contract.Extract before
	// capture. Optional — when empty, ContractDelta falls back to the
	// annotation-only path: FeatureIDs that exist on both sides with the
	// same shape (Kind == KindFunc placeholder) → no Changed. This means
	// contract-signature-shape diffs only fire on the richer path. The
	// caller (cmd/atlas, Phase 7) is expected to populate this field
	// from the per-ref scan output.
	Contracts []contract.ContractDef `json:"contracts,omitempty"`
}

// FeatureHealth is the diff-facing audit record.
//
// This type is intentionally defined HERE rather than imported from
// packages/audit because the audit/ package is still in flight (Phase
// 6a). The shape mirrors what audit/ is expected to ship: a per-feature
// integer score in 0..100 plus optional per-layer breakdown and blocking
// findings. Audit/ will provide a typed FeatureHealth → diff.FeatureHealth
// adapter once it lands; until then callers can populate this slice by
// unmarshalling store.AuditSnapshotRun.ScoreJSON (which audit/ writes as a
// JSON-encoded []FeatureHealth) and projecting into this type.
//
// JSON tags are stable: they back the audit_json column persisted in
// the snapshots table, so changing them is a schema-level event (and
// requires the store-layer audit_json reader to migrate in lockstep).
type FeatureHealth struct {
	FeatureID        shared.FeatureID `json:"feature_id"`
	Score            int              `json:"score"`
	LayerScores      map[string]int   `json:"layer_scores,omitempty"`
	BlockingFindings []string         `json:"blocking_findings,omitempty"`
}

// FeatureCoverage is the diff-facing coverage summary per feature.
//
// PassRate is in [0, 1]. Total is the number of test results that mapped
// to the feature (passed + failed + skipped). PassRate is undefined
// (Total == 0) when no test mapped to the feature — diff/ treats that
// as "no signal" and excludes it from CoverageDelta.Changed.
type FeatureCoverage struct {
	FeatureID shared.FeatureID `json:"feature_id"`
	Passed    int              `json:"passed"`
	Failed    int              `json:"failed"`
	Skipped   int              `json:"skipped"`
	Total     int              `json:"total"`
	PassRate  float64          `json:"pass_rate"`
}

// SnapshotDiff is the structured delta Compute returns.
//
// Every embedded *Delta type carries Added / Removed / Changed slices.
// The slices are nil when the dimension is unchanged — callers can rely
// on len(d.Features.Added) == 0 etc. without nil checks.
type SnapshotDiff struct {
	A, B Snapshot `json:"-"`

	// ARef / BRef surface the git refs for the inputs so consumers that
	// only have a SnapshotDiff (no original Snapshots) can still report
	// "from main → release/v2". Populated from A.GitRef / B.GitRef.
	ARef string `json:"a_ref"`
	BRef string `json:"b_ref"`

	Features       FeatureDelta      `json:"features"`
	Symbols        SymbolDelta       `json:"symbols"`
	Edges          EdgeDelta         `json:"edges"`
	Annotations    AnnotationDelta   `json:"annotations"`
	Contracts      ContractDelta     `json:"contracts"`
	PatternMatches PatternMatchDelta `json:"pattern_matches"`
	Audit          AuditDelta        `json:"audit"`
	Coverage       CoverageDelta     `json:"coverage"`
}

// IsEmpty returns true when every embedded *Delta is empty. Useful in
// CI drift gates that want a single "pass / fail" verdict.
func (d *SnapshotDiff) IsEmpty() bool {
	return d.Features.IsEmpty() &&
		d.Symbols.IsEmpty() &&
		d.Edges.IsEmpty() &&
		d.Annotations.IsEmpty() &&
		d.Contracts.IsEmpty() &&
		d.PatternMatches.IsEmpty() &&
		d.Audit.IsEmpty() &&
		d.Coverage.IsEmpty()
}

// ----- Per-dimension delta types ----------------------------------------

// FeatureDelta captures changes to the `features` row set.
type FeatureDelta struct {
	Added   []FeatureRow       `json:"added,omitempty"`
	Removed []FeatureRow       `json:"removed,omitempty"`
	Changed []FeatureRowChange `json:"changed,omitempty"`
}

// FeatureRow is the diff-facing projection of a feature definition.
// Derived from codeindex.Index's annotations + (when present) the
// contract.ContractDef list.
type FeatureRow struct {
	ID    shared.FeatureID `json:"id"`
	Kind  string           `json:"kind,omitempty"` // "feature" | "contract"
	Title string           `json:"title,omitempty"`
}

// FeatureRowChange is the Before / After pair for a feature whose metadata
// shifted between snapshots (e.g. kind: feature → contract).
type FeatureRowChange struct {
	ID     shared.FeatureID `json:"id"`
	Before FeatureRow       `json:"before"`
	After  FeatureRow       `json:"after"`
}

func (d FeatureDelta) IsEmpty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Changed) == 0
}

// SymbolDelta captures changes to the symbol set.
type SymbolDelta struct {
	Added   []shared.Symbol `json:"added,omitempty"`
	Removed []shared.Symbol `json:"removed,omitempty"`
	Changed []SymbolChange  `json:"changed,omitempty"`
}

// SymbolChange records a symbol that exists on both sides but whose
// position OR signature shifted. ID is constant across sides.
type SymbolChange struct {
	ID     shared.SymbolID `json:"id"`
	Before shared.Symbol   `json:"before"`
	After  shared.Symbol   `json:"after"`
}

func (d SymbolDelta) IsEmpty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Changed) == 0
}

// EdgeDelta captures changes to the call / embed / impl edge set.
type EdgeDelta struct {
	Added   []EdgeRecord `json:"added,omitempty"`
	Removed []EdgeRecord `json:"removed,omitempty"`
}

// EdgeRecord is the diff-facing projection of a single graph edge.
// Kind defaults to "call" for codeindex edges (the only kind the
// in-memory graph emits today; "embed" / "implement" / "construct"
// are persistence-only attributes set during ingest).
type EdgeRecord struct {
	From shared.SymbolID `json:"from"`
	To   shared.SymbolID `json:"to"`
	Kind string          `json:"kind,omitempty"`
}

func (d EdgeDelta) IsEmpty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0
}

// AnnotationDelta captures changes to the raw annotation set.
//
// "Changed" fires when an annotation at the same (file, line) shifted
// kind (e.g. feature → contract) or its primary ID changed. A "moved"
// annotation (same kind/id, different line) reads as Removed + Added —
// position is a load-bearing identity attribute here.
type AnnotationDelta struct {
	Added   []shared.Annotation `json:"added,omitempty"`
	Removed []shared.Annotation `json:"removed,omitempty"`
	Changed []AnnotationChange  `json:"changed,omitempty"`
}

// AnnotationChange records an annotation at a stable (file, line) anchor
// whose kind or primary ID shifted.
type AnnotationChange struct {
	Path   string            `json:"path"`
	Line   int               `json:"line"`
	Before shared.Annotation `json:"before"`
	After  shared.Annotation `json:"after"`
}

func (d AnnotationDelta) IsEmpty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Changed) == 0
}

// ContractDelta captures changes to the contract definition set.
//
// "Changed" fires when a contract identified by FeatureID exists on
// both sides but the signature shape shifted — Operation.Method/Path
// change, Signature change, GraphQL return type change, etc.
//
// IMPORTANT: a contract whose annotation was renamed AND whose handler
// moved — i.e. FeatureID is fresh on each side — reads as
// Removed + Added. To classify a true "signature-shape change" the
// diff requires a stable FeatureID across both snapshots.
type ContractDelta struct {
	Added   []contract.ContractDef `json:"added,omitempty"`
	Removed []contract.ContractDef `json:"removed,omitempty"`
	Changed []ContractChange       `json:"changed,omitempty"`
}

// ContractChange is the Before / After pair for a signature-shape change.
type ContractChange struct {
	FeatureID shared.FeatureID     `json:"feature_id"`
	Before    contract.ContractDef `json:"before"`
	After     contract.ContractDef `json:"after"`
}

func (d ContractDelta) IsEmpty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Changed) == 0
}

// PatternMatchDelta captures changes to the per-symbol pattern-match
// hit set. Phase 6f's parser-based EDA recognisers populate
// codeindex.Index.PatternMatches; diff/ reports gained and lost matches
// keyed by (symbol, pattern).
//
// Gained: present in B for (symbol, pattern), absent in A.
// Lost: present in A for (symbol, pattern), absent in B.
//
// Note: a symbol that gained its FIRST pattern match (no matches in A,
// 3 matches in B) surfaces three Gained entries — one per match. This
// matches the "edge-case-as-pressure-dimension" requirement in the
// Phase 6b spec.
type PatternMatchDelta struct {
	Gained []PatternMatchRecord `json:"gained,omitempty"`
	Lost   []PatternMatchRecord `json:"lost,omitempty"`
}

// PatternMatchRecord is the diff-facing projection of a single
// codeindex/patterns.Match. The minimal subset of fields suffices for
// downstream rendering — full Match records can be retrieved from the
// underlying Index when needed.
type PatternMatchRecord struct {
	Symbol  shared.SymbolID `json:"symbol"`
	Pattern string          `json:"pattern"`
	Detail  string          `json:"detail,omitempty"`
}

func (d PatternMatchDelta) IsEmpty() bool {
	return len(d.Gained) == 0 && len(d.Lost) == 0
}

// AuditDelta captures per-feature audit score deltas above the noise
// floor. The threshold is configurable via Options.AuditScoreNoiseFloor
// (default 5 points; matches the Phase 6b spec).
//
// MissingOnA / MissingOnB carry the FeatureIDs that appear in only ONE
// side's audit slice — distinct from Added / Removed which reserve the
// "feature didn't exist on that side" semantics for missing-from-Index
// cases. A feature gaining audit coverage between A and B will appear
// in MissingOnA AND in Audit.Features.Added on the index side.
type AuditDelta struct {
	// Added are FeatureHealth rows present in B, absent in A. Empty when
	// audit was missing on side A entirely (use MissingOnA for that).
	Added []FeatureHealth `json:"added,omitempty"`

	// Removed are FeatureHealth rows present in A, absent in B.
	Removed []FeatureHealth `json:"removed,omitempty"`

	// Changed are score deltas above the noise floor.
	Changed []AuditScoreChange `json:"changed,omitempty"`

	// MissingOnA / MissingOnB list feature IDs missing audit coverage
	// on the respective side. Independent of Added/Removed — see type doc.
	MissingOnA []shared.FeatureID `json:"missing_on_a,omitempty"`
	MissingOnB []shared.FeatureID `json:"missing_on_b,omitempty"`
}

// AuditScoreChange records a feature whose absolute audit score shifted
// by ≥ AuditScoreNoiseFloor.
type AuditScoreChange struct {
	FeatureID shared.FeatureID `json:"feature_id"`
	Before    int              `json:"before"`
	After     int              `json:"after"`
	Delta     int              `json:"delta"` // After - Before; signed.
}

func (d AuditDelta) IsEmpty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Changed) == 0 &&
		len(d.MissingOnA) == 0 && len(d.MissingOnB) == 0
}

// CoverageDelta captures per-feature pass-rate movement.
//
// A change fires when (a) the pass-rate moved by ≥
// CoveragePassRateNoiseFloor percentage points, OR (b) a feature flipped
// from PassRate == 1.0 (every test passing) to PassRate < 1.0 — even if
// the absolute movement is below the noise floor. The flip rule is the
// canary that catches "one test just regressed in an otherwise-green
// feature" cases that the noise floor would otherwise mask.
type CoverageDelta struct {
	Added   []FeatureCoverage `json:"added,omitempty"`
	Removed []FeatureCoverage `json:"removed,omitempty"`
	Changed []CoverageChange  `json:"changed,omitempty"`
}

// CoverageChange records a feature whose pass-rate moved enough to
// surface above the threshold.
type CoverageChange struct {
	FeatureID  shared.FeatureID `json:"feature_id"`
	Before     FeatureCoverage  `json:"before"`
	After      FeatureCoverage  `json:"after"`
	DeltaPP    float64          `json:"delta_pp"`              // (After.PassRate - Before.PassRate) * 100
	FlippedOff bool             `json:"flipped_off,omitempty"` // was 1.0, now < 1.0
}

func (d CoverageDelta) IsEmpty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Changed) == 0
}
