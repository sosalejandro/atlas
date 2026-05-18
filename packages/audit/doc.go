// Package audit computes per-feature health scores over the indexed Atlas
// state. The scoring algorithm is a weighted blend of four signals:
//
//   - Coverage (default 40%) — fraction of a feature's linked symbols with at
//     least one `pass` result in the latest coverage_run.
//   - Annotation freshness (default 15%) — fraction of a feature's annotation
//     sites whose latest git author-date falls inside the freshness window
//     (default: 30 days).
//   - Pattern compliance (default 25%) — for features with linked aggregates,
//     the fraction of canonical aggregate-services that match the
//     `canonical-service` pattern recogniser. Skipped cleanly when no
//     aggregate is linked.
//   - Contract drift (default 20%) — for features with linked contracts
//     (`features.kind = 'contract'`), the fraction of contracts whose
//     `updated_at` falls inside the snapshot window (default: 30 days).
//
// When a signal is unavailable — no linked symbols, no aggregates, no
// contracts, no coverage — the weighted average is recomputed over ONLY the
// available signals. A feature with no aggregates and no contracts is NOT
// penalised for those signals.
//
// The output is `FeatureHealth` (per feature) or a sorted-ascending
// `[]FeatureHealth` (whole project). `PersistSnapshot` stores the slice as
// a single JSON blob in the `audit_snapshot_runs` table; `LoadSnapshot`
// returns it verbatim.
//
// The signal weighting + freshness windows are tunable via `Options`. The
// package deliberately exposes only the audit-facing surface — pattern,
// store, and contract internals are hidden behind the `Store` adapter
// interface so callers can wire test doubles.
package audit
