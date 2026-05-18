-- Phase 6b: snapshots table.
--
-- A snapshot captures the full set of indexed data at a given git ref:
-- the serialised codeindex.Index (symbols, edges, annotations, pattern
-- matches) plus an optional audit slice (the per-feature health rows the
-- Phase 6a audit/ package will produce; nullable when audit hasn't run
-- at that ref yet).
--
-- This is separate from `audit_snapshots` (per-feature, per-snapshot
-- history rows) — that table answers "what did feature X look like at
-- time T", whereas `snapshots` answers "what did the WHOLE project look
-- like at git ref R, so I can diff it against ref R+1". The diff/
-- package reads this table; the audit/ package writes to
-- `audit_snapshots` (and, when integrated with snapshots, the audit_json
-- column here).
--
-- Schema considerations:
--
--   * git_ref is freeform TEXT — Atlas does not fork git itself; the
--     caller is responsible for re-running `atlas scan` against the
--     desired ref BEFORE calling Snapshots.Capture. Values are typically
--     full SHAs ("a1b2c3d4...") but can also be ref-names ("main",
--     "release/v2") or arbitrary tags ("pre-migration"). The index
--     on git_ref is non-unique on purpose: capturing the same ref
--     repeatedly (each with a different note) is a supported workflow.
--   * index_json carries the codeindex view as JSON. TEXT, unbounded
--     (modernc/sqlite has no hard upper); typical compressed size on a
--     1500-file project is < 2 MiB pre-compression.
--   * audit_json is nullable for "audit not yet computed at this ref".
--     When present, it carries the JSON-marshalled []FeatureHealth
--     slice (whatever shape audit/ ships in Phase 6a — schema stays
--     out of the way deliberately).
--   * notes is a small free-form human-readable label ("pre-migration",
--     "post-huma-cutover"). Optional.
CREATE TABLE snapshots (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  git_ref     TEXT NOT NULL,
  captured_at TIMESTAMP NOT NULL,
  index_json  TEXT NOT NULL,
  audit_json  TEXT,
  notes       TEXT
);

CREATE INDEX idx_snapshots_git_ref ON snapshots(git_ref);
