package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	goscan "github.com/sosalejandro/atlas/packages/codeindex/go"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store/sqlc"
)

// SkippedFileRow is one entry of the `skipped_files` exclusion ledger
// (docs/schema-v1.md §5.15) — a file the last scan walked past instead of
// indexing, with the rule that made that call.
//
// Rule is the scanner's own goscan.SkipReason verbatim ("generated-header",
// "generated-glob", "generated-dir", "ignored-package"), not a category. A
// ledger that said "generated" would tell an operator what happened but not
// which line of `.atlas.yaml` to change, which is the only reason they are
// reading it.
//
// Detail carries the rule's parameter when it has one: the glob that matched
// for `generated-glob`, the directory segment for `generated-dir`. It is
// empty for rules that take no parameter, and — deliberately — empty rather
// than guessed when the parameter cannot be established. A wrong pattern here
// sends the operator to a config line whose removal changes nothing.
type SkippedFileRow struct {
	FilePath  string    `json:"file_path"`
	Rule      string    `json:"rule"`
	Detail    string    `json:"detail,omitempty"`
	ScannedAt time.Time `json:"scanned_at"`
}

// SkippedFiles is the narrow port for the `skipped_files` table.
type SkippedFiles interface {
	// Get answers "why is this file not indexed?" for one path, and returns
	// shared.ErrNotFound when the last scan did not exclude it. Absence is
	// an answer too: the file was either indexed or never walked.
	Get(ctx context.Context, filePath string) (SkippedFileRow, error)

	// List returns the whole ledger, ordered by file_path.
	List(ctx context.Context) ([]SkippedFileRow, error)

	// Replace swaps the ledger for the given set and returns the number of
	// rows written. It is a replace and not an append because a file that
	// stops being skipped has to leave: a ledger that accumulates is a
	// record of every rule ever tried, and answers "why is this file not
	// indexed?" with files that are.
	Replace(ctx context.Context, rows []SkippedFileRow) (int, error)
}

var _ SkippedFiles = (*skippedFilesStore)(nil)

// SkippedFiles returns the Store's SkippedFiles port.
func (s *Store) SkippedFiles() SkippedFiles { return &skippedFilesStore{q: s.queries()} }

type skippedFilesStore struct{ q *sqlc.Queries }

func fromSQLCSkippedFile(r sqlc.SkippedFile) SkippedFileRow {
	detail := ""
	if r.Detail != nil {
		detail = *r.Detail
	}
	return SkippedFileRow{
		FilePath:  r.FilePath,
		Rule:      r.Rule,
		Detail:    detail,
		ScannedAt: r.ScannedAt,
	}
}

func (k *skippedFilesStore) Get(ctx context.Context, filePath string) (SkippedFileRow, error) {
	row, err := k.q.GetSkippedFile(ctx, filePath)
	if errors.Is(err, sql.ErrNoRows) {
		return SkippedFileRow{}, shared.ErrNotFound
	}
	if err != nil {
		return SkippedFileRow{}, fmt.Errorf("skipped_files get %q: %w", filePath, err)
	}
	return fromSQLCSkippedFile(row), nil
}

func (k *skippedFilesStore) List(ctx context.Context) ([]SkippedFileRow, error) {
	rows, err := k.q.ListSkippedFiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("skipped_files list: %w", err)
	}
	out := make([]SkippedFileRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromSQLCSkippedFile(r))
	}
	return out, nil
}

// Replace outside a transaction is for callers that hold no ingest of their
// own (tests, repair tooling). `atlas scan` goes through
// replaceSkippedLedgerTx instead, so the ledger commits with the index it
// describes.
func (k *skippedFilesStore) Replace(ctx context.Context, rows []SkippedFileRow) (int, error) {
	return replaceSkippedLedgerTx(ctx, k.q, rows)
}

// replaceSkippedLedgerTx writes the ledger through the caller's *sqlc.Queries
// — which is the whole point of taking one: passed the ingest's WithTx
// handle, the DELETE and the INSERTs land in the same transaction as the
// symbols, so a crash mid-scan cannot leave a ledger describing a scan that
// never finished.
func replaceSkippedLedgerTx(ctx context.Context, q *sqlc.Queries, rows []SkippedFileRow) (int, error) {
	for _, r := range rows {
		if r.FilePath == "" {
			return 0, fmt.Errorf("skipped_files replace: file_path required")
		}
		if r.Rule == "" {
			// A path with no rule is worse than no row at all: it says the
			// file was excluded and refuses to say why.
			return 0, fmt.Errorf("skipped_files replace %q: rule required", r.FilePath)
		}
	}
	if err := q.DeleteAllSkippedFiles(ctx); err != nil {
		return 0, fmt.Errorf("skipped_files clear: %w", err)
	}
	written := 0
	for _, r := range rows {
		scannedAt := r.ScannedAt
		if scannedAt.IsZero() {
			scannedAt = time.Now().UTC()
		}
		var detail *string
		if r.Detail != "" {
			v := r.Detail
			detail = &v
		}
		if err := q.InsertSkippedFile(ctx, sqlc.InsertSkippedFileParams{
			FilePath:  r.FilePath,
			Rule:      r.Rule,
			Detail:    detail,
			ScannedAt: scannedAt,
		}); err != nil {
			return written, fmt.Errorf("skipped_files insert %q: %w", r.FilePath, err)
		}
		written++
	}
	return written, nil
}

// skippedLedgerRows turns the scanner's ledger into storable rows, naming
// the rule's parameter where one can be established.
//
// It never re-decides anything: the rule attached to each file is the
// scanner's, applied strongest-signal-first, and is copied through
// unchanged. All this adds is the parameter the scanner does not report —
// which of the configured globs matched — and only for files the scanner
// itself attributed to the glob rule.
func skippedLedgerRows(skipped []goscan.SkippedFile, globs []string, scannedAt time.Time) []SkippedFileRow {
	rows := make([]SkippedFileRow, 0, len(skipped))
	for _, sf := range skipped {
		if sf.Path == "" || sf.Reason == "" {
			continue
		}
		rows = append(rows, SkippedFileRow{
			FilePath:  sf.Path,
			Rule:      string(sf.Reason),
			Detail:    skipRuleDetail(sf.Reason, sf.Path, globs),
			ScannedAt: scannedAt,
		})
	}
	return rows
}

// generatedDirSegment mirrors the path segment goscan's legacy directory
// rule looks for. It is only ever used to LABEL a decision goscan already
// made, and the label is dropped when the segment is absent, so a rename on
// the scanner side costs a missing detail rather than a wrong one.
const generatedDirSegment = "generated"

// skipRuleDetail names the rule's parameter, or "" when the rule has none
// (a header is a header) or when the parameter cannot be established.
func skipRuleDetail(reason goscan.SkipReason, relPath string, globs []string) string {
	switch reason {
	case goscan.SkipGeneratedGlob:
		return firstMatchingGlob(globs, relPath)
	case goscan.SkipGeneratedDir:
		for _, seg := range strings.Split(relPath, "/") {
			if seg == generatedDirSegment {
				return generatedDirSegment
			}
		}
		return ""
	case goscan.SkipGeneratedHeader, goscan.SkipIgnoredPackage:
		return ""
	default:
		return ""
	}
}

// firstMatchingGlob returns the configured pattern that claimed relPath, or
// "" if none of them does.
//
// First match wins because that is what the scanner does: it walks
// Options.GeneratedGlobs in order and returns on the first hit. Patterns the
// scanner rejected as malformed cannot shift the answer — matchGeneratedGlob
// treats a malformed pattern as a non-match here too.
func firstMatchingGlob(globs []string, relPath string) string {
	for _, g := range globs {
		if matchGeneratedGlob(g, relPath) {
			return g
		}
	}
	return ""
}

// matchGeneratedGlob mirrors goscan's matcher of the same name, whose four
// pattern shapes are documented on goscan.Options.GeneratedGlobs.
//
// It is a copy rather than a call because goscan.SkippedFile reports the RULE
// that claimed a file and not the pattern behind it, and the ledger's whole
// value is naming the pattern: "generated-glob" tells an operator nothing
// they can act on, "**/*_gen.go" tells them which line of `.atlas.yaml`
// swallowed their hand-written token_gen.go. The copy is confined to
// LABELLING files goscan already excluded — it can never exclude one — and
// TestSkippedLedger_GlobAttributionMatchesTheScanner scans the shared
// fixture through both to catch the two drifting apart. Deleting this
// function is the right move the day the scanner reports the pattern itself.
func matchGeneratedGlob(pattern, relPath string) bool {
	if pattern == "" {
		return false
	}
	if dir := strings.TrimSuffix(pattern, "/"); dir != pattern {
		return relPath == dir ||
			strings.HasPrefix(relPath, dir+"/") ||
			strings.Contains(relPath, "/"+dir+"/")
	}
	pattern = strings.TrimPrefix(pattern, "**/")
	if ok, err := path.Match(pattern, relPath); err == nil && ok {
		return true
	}
	if strings.Contains(pattern, "/") {
		return false
	}
	// A pattern naming no directory is a filename convention, so it has to
	// reach files at any depth.
	ok, err := path.Match(pattern, path.Base(relPath))
	return err == nil && ok
}
