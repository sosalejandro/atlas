// Package diff computes the structured delta between two atlas snapshots.
//
// A snapshot (see Snapshot) captures the full set of indexed data at a
// given git ref: symbols, edges, annotations, contracts, pattern matches,
// audit scores, coverage. Two snapshots taken at different commits can be
// compared via Compute(snapA, snapB) to produce a SnapshotDiff —
// a multi-axis delta that powers the future `atlas diff <a> <b>` CLI verb
// and CI drift-detection gates.
//
// The package is intentionally Library Go-importable: it depends only on
// codeindex, store, contract, and shared. It does NOT take a build-time
// dependency on the audit/ package — Phase 6a's audit/ is still in flight
// at the time this package landed. AuditDelta operates over the
// JSON-marshalled []FeatureHealth slice persisted to
// store.SnapshotRecord.AuditJSON; consumers that already have typed
// audit values can pre-serialise them before calling Compute.
//
// Granularity per delta type:
//
//	Added    — present in B, absent in A
//	Removed  — present in A, absent in B
//	Changed  — present in both with different shape (signature change for
//	           contracts, kind change for annotations, etc.) — granular
//	           field-level diff captured in a Before / After pair
//
// For audit scores, Changed only fires when the absolute delta is ≥ the
// configurable AuditScoreNoiseFloor (default 5 points). Filters out tiny
// coverage-flapping deltas that would otherwise dominate CI gate output.
//
// For coverage, Changed fires when the pass-rate moved by ≥ the configurable
// CoveragePassRateNoiseFloor (default 5 percentage points) OR a previously
// fully-passing feature flipped to non-passing entirely.
//
// Diff symmetry:
//
//	Compute(A, B).Features.Added == Compute(B, A).Features.Removed
//	Compute(A, A) → SnapshotDiff with every delta type empty
//
// Out of scope for this package:
//   - The `atlas diff` CLI subcommand (Phase 7)
//   - History views across more than two snapshots (Horizon 2)
//   - Auto-comment on GitHub PR (Phase 19 / Horizon 3)
//   - Mutating the underlying code or store
package diff
