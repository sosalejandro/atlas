-- 0016_skipped_files: the exclusion ledger (issue #137).
--
-- Exclusion is silent by design: a file the scanner declined to index does
-- not show up as uncovered, unlinked or missing, it shows up as nothing at
-- all. Without this table the only record of the decision is the terminal
-- output of the scan that made it, so an over-broad glob removes real code
-- from every coverage and audit number with no trace.
--
-- This is a scan-time fact, so it sits next to file_hashes: both describe
-- what the walk did, not what the tests found.
CREATE TABLE skipped_files (
  file_path  TEXT PRIMARY KEY,
  rule       TEXT NOT NULL,
  detail     TEXT,
  scanned_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- file_path alone is the key, not (file_path, rule). The rules are applied
-- strongest-signal-first and exactly one of them claims a file; a composite
-- key would let the same file sit under two rules at once, which is the one
-- thing this ledger exists to disprove.
CREATE INDEX skipped_files_rule_idx ON skipped_files(rule);
