package diff

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/contract"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// DiffPort is the narrow interface the diff package exposes. The future
// `atlas diff <ref-a> <ref-b>` CLI verb (Phase 7) and CI drift-detection
// gates depend on this interface, not the concrete *Engine — so a test
// double can substitute a recorded fixture without spinning up SQLite.
type DiffPort interface {
	// Compute returns a SnapshotDiff for two in-memory snapshots. The
	// snapshots' Index fields must be non-nil; everything else (Audit,
	// Coverage) is optional.
	Compute(ctx context.Context, snapA, snapB Snapshot) (*SnapshotDiff, error)

	// ComputeFromStore loads both snapshot rows from the Store by id,
	// unmarshals them into Snapshot values, and returns the diff. Useful
	// when both snapshots already persisted via Snapshots.Capture.
	ComputeFromStore(ctx context.Context, idA, idB int64) (*SnapshotDiff, error)
}

// Options tunes the diff computation. Zero value gets the Phase 6b spec
// defaults (5-point audit score noise floor, 5 percentage-point coverage
// noise floor).
type Options struct {
	// AuditScoreNoiseFloor is the minimum absolute score delta required
	// to surface in AuditDelta.Changed. Set to 0 to surface every delta.
	AuditScoreNoiseFloor int

	// CoveragePassRateNoiseFloor is the minimum pass-rate movement (in
	// percentage points, e.g. 5.0 = 5 percent points) required to
	// surface in CoverageDelta.Changed. Pass-rate flip from 1.0 → < 1.0
	// always surfaces regardless of this threshold (see CoverageChange
	// FlippedOff).
	CoveragePassRateNoiseFloor float64
}

const (
	// DefaultAuditScoreNoiseFloor matches the Phase 6b spec: "Changed
	// only if the absolute delta is ≥ 5 points".
	DefaultAuditScoreNoiseFloor = 5

	// DefaultCoveragePassRateNoiseFloor matches the Phase 6b spec:
	// "Changed only if the pass-rate moved by ≥ 5 percentage points".
	DefaultCoveragePassRateNoiseFloor = 5.0
)

// Engine is the concrete DiffPort implementation. Construct with
// NewEngine; the zero-value Engine{} is safe to call but uses Options{}
// with all-zero noise floors (every delta surfaces).
type Engine struct {
	store *store.Store
	opts  Options
}

// NewEngine returns an Engine wired to the given Store + options.
// When s is nil, ComputeFromStore returns an error; Compute still works
// — it does not touch the store.
//
// When opts has zero noise floors, the defaults from the spec apply.
// Pass an explicit Options{AuditScoreNoiseFloor: 0} (with a different
// non-zero field) to force every delta.
func NewEngine(s *store.Store, opts Options) *Engine {
	if opts.AuditScoreNoiseFloor == 0 && opts.CoveragePassRateNoiseFloor == 0 {
		opts.AuditScoreNoiseFloor = DefaultAuditScoreNoiseFloor
		opts.CoveragePassRateNoiseFloor = DefaultCoveragePassRateNoiseFloor
	}
	return &Engine{store: s, opts: opts}
}

// Compute returns the SnapshotDiff for two in-memory snapshots.
//
// The snapshots' Index fields must be non-nil. Audit and Coverage are
// optional — missing data surfaces via AuditDelta.MissingOnA /
// CoverageDelta-equivalent semantics, not as "everything removed".
func (e *Engine) Compute(ctx context.Context, snapA, snapB Snapshot) (*SnapshotDiff, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("diff compute: %w", err)
	}
	if snapA.Index == nil || snapB.Index == nil {
		return nil, errors.New("diff compute: both snapshots must have a non-nil Index")
	}

	out := &SnapshotDiff{
		A:    snapA,
		B:    snapB,
		ARef: snapA.GitRef,
		BRef: snapB.GitRef,
	}

	out.Features = diffFeatures(snapA.Index, snapB.Index)
	out.Symbols = diffSymbols(snapA.Index, snapB.Index)
	out.Edges = diffEdges(snapA.Index, snapB.Index)
	out.Annotations = diffAnnotations(snapA.Index, snapB.Index)
	out.Contracts = diffContracts(snapA, snapB)
	out.PatternMatches = diffPatternMatches(snapA.Index, snapB.Index)
	out.Audit = diffAudit(snapA.Audit, snapB.Audit, e.opts.AuditScoreNoiseFloor)
	out.Coverage = diffCoverage(snapA.Coverage, snapB.Coverage, e.opts.CoveragePassRateNoiseFloor)

	return out, nil
}

// ComputeFromStore loads both snapshots from the Store by id and returns
// the SnapshotDiff. Both snapshots must exist (shared.ErrNotFound is
// returned otherwise).
func (e *Engine) ComputeFromStore(ctx context.Context, idA, idB int64) (*SnapshotDiff, error) {
	if e.store == nil {
		return nil, errors.New("diff ComputeFromStore: engine has no Store")
	}
	snapA, err := loadSnapshot(ctx, e.store, idA)
	if err != nil {
		return nil, fmt.Errorf("diff load snap A (id=%d): %w", idA, err)
	}
	snapB, err := loadSnapshot(ctx, e.store, idB)
	if err != nil {
		return nil, fmt.Errorf("diff load snap B (id=%d): %w", idB, err)
	}
	return e.Compute(ctx, snapA, snapB)
}

// loadSnapshot reads a SnapshotRecord by id and decodes its IndexJSON +
// AuditJSON into Snapshot fields. The store-level row carries the JSON
// payload; this is where it becomes typed.
func loadSnapshot(ctx context.Context, s *store.Store, id int64) (Snapshot, error) {
	row, err := s.Snapshots().Get(ctx, id)
	if err != nil {
		return Snapshot{}, err //nolint:wrapcheck // store error already typed (shared.ErrNotFound) or wrapped.
	}
	var idx codeindex.Index
	if err := json.Unmarshal([]byte(row.IndexJSON), &idx); err != nil {
		return Snapshot{}, fmt.Errorf("decode index_json: %w", err)
	}
	snap := Snapshot{
		GitRef:     row.GitRef,
		CapturedAt: row.CapturedAt,
		Index:      &idx,
	}
	if row.AuditJSON != nil && *row.AuditJSON != "" {
		var fhs []FeatureHealth
		if err := json.Unmarshal([]byte(*row.AuditJSON), &fhs); err != nil {
			return Snapshot{}, fmt.Errorf("decode audit_json: %w", err)
		}
		snap.Audit = fhs
	}
	return snap, nil
}

// EncodeIndexJSON marshals a codeindex.Index for the Snapshots.Capture
// payload. Centralised here so callers (cmd/atlas, tests) don't have to
// know the wire shape.
func EncodeIndexJSON(idx *codeindex.Index) (string, error) {
	if idx == nil {
		return "", errors.New("encode index_json: nil index")
	}
	b, err := json.Marshal(idx)
	if err != nil {
		return "", fmt.Errorf("encode index_json: %w", err)
	}
	return string(b), nil
}

// EncodeAuditJSON marshals a []FeatureHealth slice for the
// Snapshots.Capture payload. Returns ("", nil) for an empty slice so
// callers can pass the result straight to CaptureInput.AuditJSON as
// nil-equivalent.
func EncodeAuditJSON(audit []FeatureHealth) (string, error) {
	if len(audit) == 0 {
		return "", nil
	}
	b, err := json.Marshal(audit)
	if err != nil {
		return "", fmt.Errorf("encode audit_json: %w", err)
	}
	return string(b), nil
}

// ----- Per-dimension diff helpers ---------------------------------------

// diffFeatures derives the FeatureDelta from the annotations on each
// index. A "feature" is any annotation with kind feature OR contract
// that carries at least one ID. The kind comes from the annotation
// (feature/contract); the title is left empty here — title is a
// persistence-side attribute that diff/ doesn't read directly.
//
// FeatureRow identity is the FeatureID. Two annotations that disagree
// on (kind) for the same FeatureID surface as Changed.
func diffFeatures(a, b *codeindex.Index) FeatureDelta {
	left := featureRowsByID(a.Annotations)
	right := featureRowsByID(b.Annotations)

	var delta FeatureDelta
	for id, fr := range left {
		other, ok := right[id]
		if !ok {
			delta.Removed = append(delta.Removed, fr)
			continue
		}
		if fr != other {
			delta.Changed = append(delta.Changed, FeatureRowChange{
				ID:     id,
				Before: fr,
				After:  other,
			})
		}
	}
	for id, fr := range right {
		if _, ok := left[id]; !ok {
			delta.Added = append(delta.Added, fr)
		}
	}
	sort.Slice(delta.Added, func(i, j int) bool { return delta.Added[i].ID < delta.Added[j].ID })
	sort.Slice(delta.Removed, func(i, j int) bool { return delta.Removed[i].ID < delta.Removed[j].ID })
	sort.Slice(delta.Changed, func(i, j int) bool { return delta.Changed[i].ID < delta.Changed[j].ID })
	return delta
}

// featureRowsByID flattens annotations into a per-ID FeatureRow.
// Multiple annotations for the same ID collapse to one row — the last
// one wins on Kind / Title ties; in practice consumers should de-dupe at
// annotation parse time.
func featureRowsByID(anns []shared.Annotation) map[shared.FeatureID]FeatureRow {
	out := make(map[shared.FeatureID]FeatureRow)
	for _, ann := range anns {
		if ann.Kind != shared.AnnFeature && ann.Kind != shared.AnnContract {
			continue
		}
		for _, raw := range ann.IDs {
			id := shared.FeatureID(raw)
			if id == "" {
				continue
			}
			out[id] = FeatureRow{
				ID:   id,
				Kind: string(ann.Kind),
			}
		}
	}
	return out
}

// diffSymbols derives the SymbolDelta. Identity is shared.SymbolID. A
// symbol that exists on both sides with a different Position OR
// Signature OR Kind surfaces as Changed.
func diffSymbols(a, b *codeindex.Index) SymbolDelta {
	left := symbolsByID(a.Symbols)
	right := symbolsByID(b.Symbols)

	var delta SymbolDelta
	for id, sym := range left {
		other, ok := right[id]
		if !ok {
			delta.Removed = append(delta.Removed, sym)
			continue
		}
		if !symbolEqual(sym, other) {
			delta.Changed = append(delta.Changed, SymbolChange{
				ID:     id,
				Before: sym,
				After:  other,
			})
		}
	}
	for id, sym := range right {
		if _, ok := left[id]; !ok {
			delta.Added = append(delta.Added, sym)
		}
	}
	sort.Slice(delta.Added, func(i, j int) bool { return delta.Added[i].ID < delta.Added[j].ID })
	sort.Slice(delta.Removed, func(i, j int) bool { return delta.Removed[i].ID < delta.Removed[j].ID })
	sort.Slice(delta.Changed, func(i, j int) bool { return delta.Changed[i].ID < delta.Changed[j].ID })
	return delta
}

func symbolsByID(syms []shared.Symbol) map[shared.SymbolID]shared.Symbol {
	out := make(map[shared.SymbolID]shared.Symbol, len(syms))
	for _, s := range syms {
		if s.ID == "" {
			continue
		}
		out[s.ID] = s
	}
	return out
}

// symbolEqual treats two symbols as identical when the diff-relevant
// projection matches. Doc / Package are excluded — they shift for
// reasons unrelated to the semantic shape (whitespace edits, package
// renames that don't affect the qualified name).
func symbolEqual(a, b shared.Symbol) bool {
	if a.Kind != b.Kind {
		return false
	}
	if a.Signature != b.Signature {
		return false
	}
	if a.Position.Path != b.Position.Path {
		return false
	}
	if a.Position.Line != b.Position.Line {
		return false
	}
	return true
}

// diffEdges derives the EdgeDelta from the codeindex.Graph.Edges slice
// on each side. Identity is (from, to, kind) — the same call from the
// same caller to the same callee is the same edge regardless of file
// position.
func diffEdges(a, b *codeindex.Index) EdgeDelta {
	left := edgesByKey(a)
	right := edgesByKey(b)

	var delta EdgeDelta
	for k, rec := range left {
		if _, ok := right[k]; !ok {
			delta.Removed = append(delta.Removed, rec)
		}
	}
	for k, rec := range right {
		if _, ok := left[k]; !ok {
			delta.Added = append(delta.Added, rec)
		}
	}
	sortEdges := func(s []EdgeRecord) {
		sort.Slice(s, func(i, j int) bool {
			if s[i].From != s[j].From {
				return s[i].From < s[j].From
			}
			if s[i].To != s[j].To {
				return s[i].To < s[j].To
			}
			return s[i].Kind < s[j].Kind
		})
	}
	sortEdges(delta.Added)
	sortEdges(delta.Removed)
	return delta
}

// edgesByKey indexes a codeindex Graph's edges by their (from, to, kind)
// composite. codeindex.Graph.Edges currently emits only "call" edges, so
// kind is always "call" — but the helper threads it through so future
// edge-kind extensions (Phase 7+ embed / implement edges materialised
// in-graph) work without a second pass.
func edgesByKey(idx *codeindex.Index) map[string]EdgeRecord {
	out := map[string]EdgeRecord{}
	if idx.Graph == nil {
		return out
	}
	for _, e := range idx.Graph.Edges {
		rec := EdgeRecord{From: e.From, To: e.To, Kind: "call"}
		out[edgeKey(rec)] = rec
	}
	return out
}

func edgeKey(e EdgeRecord) string {
	return string(e.From) + "->" + string(e.To) + "|" + e.Kind
}

// diffAnnotations indexes annotations by their (file, line, kind) anchor.
// Two annotations at the same (file, line) with different kinds surface
// as Changed; same kind + different primary ID surfaces as Changed.
// "Moved" annotations (same kind+id, different line) appear as Removed +
// Added — position is part of identity.
func diffAnnotations(a, b *codeindex.Index) AnnotationDelta {
	left := annotationsByAnchor(a.Annotations)
	right := annotationsByAnchor(b.Annotations)

	var delta AnnotationDelta
	for k, ann := range left {
		other, ok := right[k]
		if !ok {
			delta.Removed = append(delta.Removed, ann)
			continue
		}
		if !annotationEqual(ann, other) {
			delta.Changed = append(delta.Changed, AnnotationChange{
				Path:   ann.Position.Path,
				Line:   ann.Position.Line,
				Before: ann,
				After:  other,
			})
		}
	}
	for k, ann := range right {
		if _, ok := left[k]; !ok {
			delta.Added = append(delta.Added, ann)
		}
	}
	sortAnns := func(s []shared.Annotation) {
		sort.SliceStable(s, func(i, j int) bool {
			if s[i].Position.Path != s[j].Position.Path {
				return s[i].Position.Path < s[j].Position.Path
			}
			return s[i].Position.Line < s[j].Position.Line
		})
	}
	sortAnns(delta.Added)
	sortAnns(delta.Removed)
	sort.SliceStable(delta.Changed, func(i, j int) bool {
		if delta.Changed[i].Path != delta.Changed[j].Path {
			return delta.Changed[i].Path < delta.Changed[j].Path
		}
		return delta.Changed[i].Line < delta.Changed[j].Line
	})
	return delta
}

// annotationsByAnchor keys annotations by (file, line). Kind isn't part
// of the key because the spec requires us to surface a kind-change as
// "Changed", not Added+Removed. Two annotations at the same anchor with
// the same kind collapse — the LAST one in the slice wins (the parser
// emits one per directive in order).
func annotationsByAnchor(anns []shared.Annotation) map[string]shared.Annotation {
	out := make(map[string]shared.Annotation, len(anns))
	for _, ann := range anns {
		out[ann.Position.Path+":"+itoaInt(ann.Position.Line)] = ann
	}
	return out
}

// annotationEqual compares two annotations on the diff-relevant fields:
// kind + primary ID + Source. Tags and Raw are excluded so a benign
// whitespace edit in the comment payload doesn't surface as Changed.
func annotationEqual(a, b shared.Annotation) bool {
	if a.Kind != b.Kind {
		return false
	}
	if a.Source != b.Source {
		return false
	}
	la, lb := len(a.IDs), len(b.IDs)
	if la == 0 && lb == 0 {
		return true
	}
	if la == 0 || lb == 0 {
		return false
	}
	return a.IDs[0] == b.IDs[0]
}

// diffContracts indexes contracts by FeatureID; contracts without a
// FeatureID (annotation-free) cannot participate in Changed because
// there's no stable handle to align Before/After.
//
// Two paths:
//
//  1. Pre-extracted (preferred): Snapshot.Contracts carries the full
//     contract.ContractDef list from contract.Extract. Signature-shape
//     deltas (Operation.Method, Path, Signature, GraphQL type/return)
//     surface in ContractDelta.Changed when the FeatureID is stable
//     across sides.
//
//  2. Index-only fallback: when Snapshot.Contracts is empty, we
//     synthesise minimal ContractDefs from the Index's annotations
//     (one per @atlas:contract directive). The fallback can detect
//     contracts added/removed but NOT signature-shape changes — those
//     fields aren't on a raw annotation.
//
// The "moved contract" edge case is preserved across both paths: a
// stable FeatureID at a different file/line is Changed, NEVER
// Removed + Added.
func diffContracts(snapA, snapB Snapshot) ContractDelta {
	leftDefs := contractDefsFor(snapA)
	rightDefs := contractDefsFor(snapB)
	left := contractDefsByFeatureID(leftDefs)
	right := contractDefsByFeatureID(rightDefs)

	var delta ContractDelta
	for id, def := range left {
		other, ok := right[id]
		if !ok {
			delta.Removed = append(delta.Removed, def)
			continue
		}
		if !contractEqual(def, other) {
			delta.Changed = append(delta.Changed, ContractChange{
				FeatureID: id,
				Before:    def,
				After:     other,
			})
		}
	}
	for id, def := range right {
		if _, ok := left[id]; !ok {
			delta.Added = append(delta.Added, def)
		}
	}
	sort.SliceStable(delta.Added, func(i, j int) bool {
		return contractSortKey(delta.Added[i]) < contractSortKey(delta.Added[j])
	})
	sort.SliceStable(delta.Removed, func(i, j int) bool {
		return contractSortKey(delta.Removed[i]) < contractSortKey(delta.Removed[j])
	})
	sort.SliceStable(delta.Changed, func(i, j int) bool {
		return delta.Changed[i].FeatureID < delta.Changed[j].FeatureID
	})
	return delta
}

// contractDefsFor returns the contract def list to diff against for one
// side. Prefers Snapshot.Contracts when present, falls back to the
// annotation-only synthesis otherwise.
func contractDefsFor(snap Snapshot) []contract.ContractDef {
	if len(snap.Contracts) > 0 {
		return snap.Contracts
	}
	return contractDefsFromIndex(snap.Index)
}

// contractDefsFromIndex synthesises a minimal ContractDef per
// @atlas:contract annotation. This is the index-only path used when
// Snapshot.Index did NOT come with pre-extracted ContractDef records.
// The resulting defs carry Kind = KindFunc as a placeholder; downstream
// consumers can pair the FeatureID with the symbols on the same line.
//
// When pre-extracted contracts are persisted alongside the index (the
// richer path), this function is bypassed.
func contractDefsFromIndex(idx *codeindex.Index) []contract.ContractDef {
	var out []contract.ContractDef
	for _, ann := range idx.Annotations {
		if ann.Kind != shared.AnnContract || len(ann.IDs) == 0 {
			continue
		}
		fid := shared.FeatureID(ann.IDs[0])
		out = append(out, contract.ContractDef{
			Name:      string(fid),
			Kind:      contract.KindFunc,
			FilePath:  ann.Position.Path,
			Line:      ann.Position.Line,
			FeatureID: &fid,
		})
	}
	return out
}

func contractDefsByFeatureID(defs []contract.ContractDef) map[shared.FeatureID]contract.ContractDef {
	out := make(map[shared.FeatureID]contract.ContractDef, len(defs))
	for _, d := range defs {
		if d.FeatureID == nil || *d.FeatureID == "" {
			continue
		}
		out[*d.FeatureID] = d
	}
	return out
}

// contractEqual checks the signature-shape fields. Filename + line are
// excluded because a contract moving file is "changed", not removed.
// Kind + Operation.Method/Path/OperationID/GraphQLType/ReturnType +
// Signature all participate; Source is excluded (it's a provenance
// attribute, not a shape attribute).
func contractEqual(a, b contract.ContractDef) bool {
	if a.Kind != b.Kind {
		return false
	}
	if a.Signature != b.Signature {
		return false
	}
	if a.Operation.Method != b.Operation.Method {
		return false
	}
	if a.Operation.Path != b.Operation.Path {
		return false
	}
	if a.Operation.OperationID != b.Operation.OperationID {
		return false
	}
	if a.Operation.GraphQLType != b.Operation.GraphQLType {
		return false
	}
	if a.Operation.ReturnType != b.Operation.ReturnType {
		return false
	}
	return true
}

func contractSortKey(d contract.ContractDef) string {
	id := ""
	if d.FeatureID != nil {
		id = string(*d.FeatureID)
	}
	return string(d.Kind) + "|" + d.FilePath + "|" + itoaInt(d.Line) + "|" + d.Name + "|" + id
}

// diffPatternMatches keys per-symbol Match records by (symbol, pattern).
// Match.Position is excluded from the key — same pattern firing on the
// same symbol at a different line is the SAME match (the line just
// shifted because the file moved). Detail is preserved so renderers can
// surface the "why" for new matches.
func diffPatternMatches(a, b *codeindex.Index) PatternMatchDelta {
	left := patternKeysOf(a)
	right := patternKeysOf(b)

	var delta PatternMatchDelta
	for k, rec := range left {
		if _, ok := right[k]; !ok {
			delta.Lost = append(delta.Lost, rec)
		}
	}
	for k, rec := range right {
		if _, ok := left[k]; !ok {
			delta.Gained = append(delta.Gained, rec)
		}
	}
	sortRecs := func(s []PatternMatchRecord) {
		sort.Slice(s, func(i, j int) bool {
			if s[i].Symbol != s[j].Symbol {
				return s[i].Symbol < s[j].Symbol
			}
			return s[i].Pattern < s[j].Pattern
		})
	}
	sortRecs(delta.Gained)
	sortRecs(delta.Lost)
	return delta
}

func patternKeysOf(idx *codeindex.Index) map[string]PatternMatchRecord {
	out := map[string]PatternMatchRecord{}
	for sym, matches := range idx.PatternMatches {
		for _, m := range matches {
			rec := PatternMatchRecord{
				Symbol:  sym,
				Pattern: m.Pattern,
				Detail:  m.Detail,
			}
			out[string(sym)+"|"+m.Pattern] = rec
		}
	}
	return out
}

// diffAudit derives the AuditDelta. When ONE side has nil/empty audit,
// the other side's features are reported as MissingOnA/MissingOnB
// (not as Added/Removed) — keeping the noise-vs-signal balance
// described in the spec.
//
// When BOTH sides are non-empty:
//   - Added: feature ID present only in B
//   - Removed: feature ID present only in A
//   - Changed: score delta with absolute value ≥ noiseFloor
func diffAudit(a, b []FeatureHealth, noiseFloor int) AuditDelta {
	if len(a) == 0 && len(b) == 0 {
		return AuditDelta{}
	}
	left := healthByID(a)
	right := healthByID(b)

	var delta AuditDelta

	// One-sided audit: report MissingOnA / MissingOnB rather than
	// flooding Added/Removed with the entire feature set on one side.
	if len(a) == 0 {
		ids := make([]shared.FeatureID, 0, len(right))
		for id := range right {
			ids = append(ids, id)
		}
		sortIDs(ids)
		delta.MissingOnA = ids
		return delta
	}
	if len(b) == 0 {
		ids := make([]shared.FeatureID, 0, len(left))
		for id := range left {
			ids = append(ids, id)
		}
		sortIDs(ids)
		delta.MissingOnB = ids
		return delta
	}

	for id, lf := range left {
		rf, ok := right[id]
		if !ok {
			delta.Removed = append(delta.Removed, lf)
			continue
		}
		d := rf.Score - lf.Score
		if abs(d) >= noiseFloor && noiseFloor > 0 {
			delta.Changed = append(delta.Changed, AuditScoreChange{
				FeatureID: id,
				Before:    lf.Score,
				After:     rf.Score,
				Delta:     d,
			})
		} else if noiseFloor <= 0 && d != 0 {
			// Threshold disabled → surface every non-zero movement.
			delta.Changed = append(delta.Changed, AuditScoreChange{
				FeatureID: id,
				Before:    lf.Score,
				After:     rf.Score,
				Delta:     d,
			})
		}
	}
	for id, rf := range right {
		if _, ok := left[id]; !ok {
			delta.Added = append(delta.Added, rf)
		}
	}
	sort.Slice(delta.Added, func(i, j int) bool { return delta.Added[i].FeatureID < delta.Added[j].FeatureID })
	sort.Slice(delta.Removed, func(i, j int) bool { return delta.Removed[i].FeatureID < delta.Removed[j].FeatureID })
	sort.Slice(delta.Changed, func(i, j int) bool { return delta.Changed[i].FeatureID < delta.Changed[j].FeatureID })
	return delta
}

func healthByID(hs []FeatureHealth) map[shared.FeatureID]FeatureHealth {
	out := make(map[shared.FeatureID]FeatureHealth, len(hs))
	for _, h := range hs {
		if h.FeatureID == "" {
			continue
		}
		out[h.FeatureID] = h
	}
	return out
}

// diffCoverage derives the CoverageDelta. PassRate movement above the
// noise floor (in percentage points) OR a pass-rate flip from 1.0 →
// < 1.0 surfaces as Changed.
//
// Features with Total == 0 on a side are treated as "no signal" — they
// can't participate in Changed because PassRate is undefined. Such
// features show up as Added/Removed only when they have Total > 0 on
// exactly one side.
func diffCoverage(a, b []FeatureCoverage, noiseFloorPP float64) CoverageDelta {
	left := coverageByID(a)
	right := coverageByID(b)

	var delta CoverageDelta
	for id, lc := range left {
		rc, ok := right[id]
		if !ok {
			if lc.Total > 0 {
				delta.Removed = append(delta.Removed, lc)
			}
			continue
		}
		classifyCoveragePair(&delta, id, lc, rc, noiseFloorPP)
	}
	for id, rc := range right {
		if _, ok := left[id]; !ok && rc.Total > 0 {
			delta.Added = append(delta.Added, rc)
		}
	}
	sort.Slice(delta.Added, func(i, j int) bool { return delta.Added[i].FeatureID < delta.Added[j].FeatureID })
	sort.Slice(delta.Removed, func(i, j int) bool { return delta.Removed[i].FeatureID < delta.Removed[j].FeatureID })
	sort.Slice(delta.Changed, func(i, j int) bool { return delta.Changed[i].FeatureID < delta.Changed[j].FeatureID })
	return delta
}

// classifyCoveragePair buckets a (left, right) pair into the right delta
// slice. Split out of diffCoverage so the cyclomatic complexity of the
// outer loop stays tractable — the cases here are mutually exclusive
// but there are five of them.
func classifyCoveragePair(delta *CoverageDelta, id shared.FeatureID, lc, rc FeatureCoverage, noiseFloorPP float64) {
	switch {
	case lc.Total == 0 && rc.Total == 0:
		return
	case lc.Total == 0 && rc.Total > 0:
		delta.Added = append(delta.Added, rc)
		return
	case rc.Total == 0 && lc.Total > 0:
		delta.Removed = append(delta.Removed, lc)
		return
	}
	dpp := (rc.PassRate - lc.PassRate) * 100.0
	flippedOff := lc.PassRate >= 1.0 && rc.PassRate < 1.0
	if absFloat(dpp) >= noiseFloorPP || flippedOff {
		delta.Changed = append(delta.Changed, CoverageChange{
			FeatureID:  id,
			Before:     lc,
			After:      rc,
			DeltaPP:    dpp,
			FlippedOff: flippedOff,
		})
	}
}

func coverageByID(cs []FeatureCoverage) map[shared.FeatureID]FeatureCoverage {
	out := make(map[shared.FeatureID]FeatureCoverage, len(cs))
	for _, c := range cs {
		if c.FeatureID == "" {
			continue
		}
		out[c.FeatureID] = c
	}
	return out
}

// ----- Tiny utilities (kept local to avoid a third-party dep) ------------

func abs(i int) int {
	if i < 0 {
		return -i
	}
	return i
}

func absFloat(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func sortIDs(ids []shared.FeatureID) {
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
}

// itoaInt is a small int → string helper for map keys / sort keys. Keep
// local to the package to avoid touching strconv in the diff-internal
// hot paths.
func itoaInt(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	negative := i < 0
	if negative {
		i = -i
	}
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if negative {
		pos--
		buf[pos] = '-'
	}
	return strings.Clone(string(buf[pos:]))
}
