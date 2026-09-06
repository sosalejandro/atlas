package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/codeindex"
	goscan "github.com/sosalejandro/atlas/packages/codeindex/go"
	"github.com/sosalejandro/atlas/packages/shared"
)

func TestSkippedFiles_ReplaceIsNotAppend(t *testing.T) {
	s := openTestStore(t)
	sk := s.SkippedFiles()
	ctx := context.Background()

	first := []SkippedFileRow{
		{FilePath: "api/schema.pb.go", Rule: "generated-glob", Detail: "**/*.pb.go"},
		{FilePath: "db/queries.sql.go", Rule: "generated-header"},
	}
	if _, err := sk.Replace(ctx, first); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	// The glob that caught schema.pb.go is deleted from the config, so the
	// next scan indexes it. The ledger must forget it entirely — a ledger
	// that accumulates is a record of every rule ever tried, and answers
	// "why is this file not indexed?" with a file that IS indexed.
	second := []SkippedFileRow{
		{FilePath: "db/queries.sql.go", Rule: "generated-header"},
	}
	n, err := sk.Replace(ctx, second)
	if err != nil {
		t.Fatalf("Replace second: %v", err)
	}
	if n != 1 {
		t.Errorf("Replace wrote %d rows, want 1", n)
	}

	rows, err := sk.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].FilePath != "db/queries.sql.go" {
		t.Fatalf("List after replace = %+v, want only db/queries.sql.go", rows)
	}
	if _, err := sk.Get(ctx, "api/schema.pb.go"); !errors.Is(err, shared.ErrNotFound) {
		t.Errorf("Get(api/schema.pb.go) err = %v, want ErrNotFound", err)
	}
}

func TestSkippedFiles_GetAnswersWhyAFileIsNotIndexed(t *testing.T) {
	s := openTestStore(t)
	sk := s.SkippedFiles()
	ctx := context.Background()

	if _, err := sk.Replace(ctx, []SkippedFileRow{
		{FilePath: "internal/token_gen.go", Rule: "generated-glob", Detail: "**/*_gen.go"},
	}); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	got, err := sk.Get(ctx, "internal/token_gen.go")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Rule != "generated-glob" {
		t.Errorf("Rule = %q, want generated-glob", got.Rule)
	}
	// The pattern is the whole point: "generated" tells an operator nothing,
	// "**/*_gen.go" tells them which line of atlas.yaml to narrow.
	if got.Detail != "**/*_gen.go" {
		t.Errorf("Detail = %q, want **/*_gen.go", got.Detail)
	}
	if got.ScannedAt.IsZero() {
		t.Error("ScannedAt is zero; the ledger must date itself")
	}
}

func TestSkippedFiles_ReplaceRejectsIncompleteRows(t *testing.T) {
	sk := openTestStore(t).SkippedFiles()
	ctx := context.Background()

	if _, err := sk.Replace(ctx, []SkippedFileRow{{Rule: "generated-dir"}}); err == nil {
		t.Error("Replace with an empty file_path: want error, got nil")
	}
	if _, err := sk.Replace(ctx, []SkippedFileRow{{FilePath: "a.go"}}); err == nil {
		t.Error("Replace with an empty rule: want error, got nil")
	}
}

// Ingest owns the ledger because the exclusion is a fact about the scan that
// wrote the rest of the index. These tests pin that it lands, that it is
// replaced rather than appended, and that the rule recorded is the one the
// scanner actually applied.
func TestIngest_WritesSkippedLedger(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	idx := buildTestIndex(t)
	idx.SkippedFiles = []goscan.SkippedFile{
		{Path: "api/schema.pb.go", Reason: goscan.SkipGeneratedGlob},
		{Path: "generated/legacy.go", Reason: goscan.SkipGeneratedDir},
	}

	stats, err := s.Ingest(ctx, idx, IngestOptions{
		GeneratedGlobs: []string{"mocks/", "**/*.pb.go"},
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if stats.SkippedFilesRecorded != 2 {
		t.Errorf("SkippedFilesRecorded = %d, want 2", stats.SkippedFilesRecorded)
	}

	rows, err := s.SkippedFiles().List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byPath := map[string]SkippedFileRow{}
	for _, r := range rows {
		byPath[r.FilePath] = r
	}
	pb, ok := byPath["api/schema.pb.go"]
	if !ok {
		t.Fatalf("api/schema.pb.go missing from the ledger: %+v", rows)
	}
	if pb.Rule != string(goscan.SkipGeneratedGlob) {
		t.Errorf("pb.go rule = %q, want %q", pb.Rule, goscan.SkipGeneratedGlob)
	}
	// Only the glob that actually matches the path may be named. Recording
	// "mocks/" here would send the operator to the wrong config line.
	if pb.Detail != "**/*.pb.go" {
		t.Errorf("pb.go detail = %q, want **/*.pb.go", pb.Detail)
	}
	if dir := byPath["generated/legacy.go"]; dir.Rule != string(goscan.SkipGeneratedDir) || dir.Detail != "generated" {
		t.Errorf("legacy.go = %+v, want rule generated-dir detail generated", dir)
	}
}

// A file that matches two rules must record the ONE that claimed it. The
// strongest-signal-first ordering (issue #96) is invisible everywhere else:
// the file is simply absent from the index either way. This ledger is the
// only place the ordering leaves a trace, so it is the only place a
// regression in it can be caught.
func TestIngest_TwoRulesRecordTheClaimingRule(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	idx := buildTestIndex(t)
	// db/queries.sql.go carries the "Code generated ... DO NOT EDIT." header
	// AND matches the configured glob. The scanner checked the header first,
	// so that is what it reports.
	idx.SkippedFiles = []goscan.SkippedFile{
		{Path: "db/queries.sql.go", Reason: goscan.SkipGeneratedHeader},
	}
	if _, err := s.Ingest(ctx, idx, IngestOptions{
		GeneratedGlobs: []string{"**/*.sql.go"},
	}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	got, err := s.SkippedFiles().Get(ctx, "db/queries.sql.go")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Rule != string(goscan.SkipGeneratedHeader) {
		t.Fatalf("rule = %q, want %q — the ledger must report the rule that "+
			"claimed the file, not the strongest-looking one that also matches",
			got.Rule, goscan.SkipGeneratedHeader)
	}
	// The glob matched too, but it did not claim the file, so naming it
	// would point the operator at a config line whose removal changes
	// nothing.
	if got.Detail != "" {
		t.Errorf("detail = %q, want empty: the glob did not claim this file", got.Detail)
	}
}

func TestIngest_SkippedLedgerReplacedPerScan(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	idx := buildTestIndex(t)
	idx.SkippedFiles = []goscan.SkippedFile{
		{Path: "api/schema.pb.go", Reason: goscan.SkipGeneratedGlob},
	}
	if _, err := s.Ingest(ctx, idx, IngestOptions{
		GeneratedGlobs: []string{"**/*.pb.go"},
	}); err != nil {
		t.Fatalf("first ingest: %v", err)
	}

	// Second scan with --include-generated: nothing was skipped, so nothing
	// may remain on the ledger.
	idx2 := buildTestIndex(t)
	idx2.SkippedFiles = nil
	if _, err := s.Ingest(ctx, idx2); err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	rows, err := s.SkippedFiles().List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("ledger after a scan that skipped nothing = %+v, want empty", rows)
	}
}

// The ledger has to describe the scan that finished, so its write is part
// of the ingest transaction rather than a separate statement around it.
// This drives the in-transaction writer directly and rolls the transaction
// back: an ingest that dies half-way must leave the PREVIOUS scan's ledger
// standing, not a ledger describing a scan that never completed.
func TestSkippedLedger_RollsBackWithItsTransaction(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	seed := []SkippedFileRow{{
		FilePath:  "api/schema.pb.go",
		Rule:      "generated-glob",
		Detail:    "**/*.pb.go",
		ScannedAt: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
	}}
	if _, err := s.SkippedFiles().Replace(ctx, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tx, err := s.conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	n, err := replaceSkippedLedgerTx(ctx, s.q.WithTx(tx), []SkippedFileRow{
		{FilePath: "generated/legacy.go", Rule: "generated-dir", Detail: "generated"},
	})
	if err != nil {
		t.Fatalf("replaceSkippedLedgerTx: %v", err)
	}
	if n != 1 {
		t.Errorf("wrote %d rows, want 1", n)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	rows, err := s.SkippedFiles().List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].FilePath != "api/schema.pb.go" {
		t.Fatalf("ledger after a rolled-back ingest = %+v, want the previous scan's row", rows)
	}
}

// The ledger names the glob that claimed a file by re-matching the scan's
// configured patterns (see matchGeneratedGlob's godoc for why that logic is
// duplicated in this package). This runs the real scanner over the shared
// fixture and checks the two agree: every file the SCANNER attributed to the
// glob rule must get a pattern from this package, and no other rule may get
// one. Drift in either matcher shows up here rather than as a ledger that
// names the wrong config line.
func TestSkippedLedger_GlobAttributionMatchesTheScanner(t *testing.T) {
	globs := []string{"**/*.pb.go", "gen/", "**/*.sql.go"}
	res, err := goscan.Scan(context.Background(),
		"../codeindex/go/testdata/generatedproject",
		goscan.Options{GeneratedGlobs: globs})
	if err != nil {
		t.Fatalf("goscan.Scan: %v", err)
	}
	if len(res.SkippedFiles) == 0 {
		t.Fatal("fixture skipped nothing; the drift check would be vacuous")
	}

	rows := skippedLedgerRows(res.SkippedFiles, globs, time.Now().UTC())
	sawGlob := false
	for _, r := range rows {
		if r.Rule != string(goscan.SkipGeneratedGlob) {
			if r.Rule == string(goscan.SkipGeneratedHeader) && r.Detail != "" {
				t.Errorf("%s: header rule carries detail %q", r.FilePath, r.Detail)
			}
			continue
		}
		sawGlob = true
		if r.Detail == "" {
			t.Errorf("%s: the scanner claimed it by glob but the ledger names "+
				"no pattern; the two matchers have drifted apart", r.FilePath)
			continue
		}
		found := false
		for _, g := range globs {
			if g == r.Detail {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: detail %q is not one of the configured globs %v",
				r.FilePath, r.Detail, globs)
		}
	}
	if !sawGlob {
		t.Fatal("no file was claimed by the glob rule; the fixture or the globs changed")
	}
}

// A nil index must not be mistaken for "the scan skipped nothing".
func TestIngest_NilIndexLeavesLedgerAlone(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.SkippedFiles().Replace(ctx, []SkippedFileRow{
		{FilePath: "generated/legacy.go", Rule: "generated-dir"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var idx *codeindex.Index
	if _, err := s.Ingest(ctx, idx); err == nil {
		t.Fatal("Ingest(nil): want error, got nil")
	}
	rows, _ := s.SkippedFiles().List(ctx)
	if len(rows) != 1 {
		t.Fatalf("ledger = %+v, want the seeded row untouched", rows)
	}
}
