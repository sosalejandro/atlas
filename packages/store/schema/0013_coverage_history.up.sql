-- 0013_coverage_history.up.sql
--
-- The measurement series behind `atlas trend` and the regression gate
-- (issue #92). Every other table in this store answers "what is true now";
-- this one answers "is it getting better or worse", which is the question a
-- coverage tool exists to answer and the only one a legacy codebase can act
-- on without first agreeing an absolute threshold nobody will hit.
--
-- Two tables: the whole-project point, and its per-feature breakdown.
--
--   * `score` is NULLABLE, and that is the single most important decision
--     in this file. Coverage evidence is routinely absent for a commit --
--     the docs-only PR nobody ran the suite on, the CI job that died before
--     `cov sync`. Storing 0 there would make `trend` draw a cliff that never
--     happened and make the regression gate fail an innocent PR. NULL means
--     "not measured"; the reader must skip the point, never plot or compare
--     it as a zero.
--
--   * `denominator` is the size of the surface the score was computed over
--     (the count of feature-linked symbols in scope). A score delta between
--     two points whose denominators differ is not a quality signal: deleting
--     a thousand untested lines raises coverage without a single new test.
--     Recording the denominator alongside the score is what lets the
--     comparison SAY that rather than quietly present the delta.
--
--   * commit_sha is UNIQUE, not (commit_sha, measured_at). The logical key
--     of a point is the pair -- but a commit's score is a function of the
--     commit, so a re-measurement is a correction, not a second data point.
--     A CI job that retries, or a developer who runs `atlas trend record`
--     twice, must leave ONE point behind. The port upserts on this index and
--     replaces the child rows, so last-write-wins is the recorded semantic.
--     measured_at is kept and updated because it orders the series and tells
--     the reader how stale a point is.
--
--   * commit_sha is free text, like snapshots.git_ref: atlas never forks git
--     to validate it, and CI systems legitimately record tags or synthetic
--     ids. The port resolves user-supplied prefixes against this column.
--
-- The store is a re-derivable cache (see store.go runMigrations), so a
-- failure here is recovered by deleting atlas.db -- at the cost of the
-- series, which is the one table here that cannot be re-derived from the
-- working tree. Teams that need the series durable should record it from CI
-- into a committed artifact as well.

CREATE TABLE coverage_history (
  id          INTEGER   PRIMARY KEY AUTOINCREMENT,
  commit_sha  TEXT      NOT NULL,
  measured_at TIMESTAMP NOT NULL,
  score       REAL,
  denominator INTEGER   NOT NULL DEFAULT 0,
  note        TEXT
);

CREATE UNIQUE INDEX coverage_history_commit_idx ON coverage_history(commit_sha);
CREATE INDEX coverage_history_measured_idx ON coverage_history(measured_at);

-- The per-feature breakdown of one point. Same NULL-means-unmeasured rule as
-- the parent: a feature with no coverage evidence at this commit gets a NULL
-- score, so `trend --feature X` shows a gap in the series instead of a drop
-- to zero.
--
-- WITHOUT ROWID because the table is all key and (history_id, feature_id) is
-- the only access path -- the series read is "every feature row for these
-- point ids", which this PK serves index-only.
--
-- ON DELETE CASCADE carries the retention prune: deleting a point drops its
-- breakdown with it, with no second statement to forget.
CREATE TABLE coverage_history_features (
  history_id  INTEGER NOT NULL REFERENCES coverage_history(id) ON DELETE CASCADE,
  feature_id  TEXT    NOT NULL,
  score       REAL,
  denominator INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (history_id, feature_id)
) WITHOUT ROWID;
