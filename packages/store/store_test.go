package store

import (
	"context"
	"path/filepath"
	"testing"
)

// openTestStore returns a *Store backed by a fresh tempfile DB. The store
// is closed automatically when the test exits.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "atlas-state.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpen_AppliesMigrations(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	v, err := s.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	// Schema version tracks the highest applied migration. Bumped to 2 in
	// Phase 6e (annotation kind set extension), 3 in Phase 6f
	// (symbols.pattern_matches column), 4 in Phase 6b (snapshots), and
	// 5 in Phase 6a (audit_snapshot_runs table).
	const expected = 5
	if v != expected {
		t.Fatalf("schema_version = %d, want %d", v, expected)
	}
}

func TestOpen_PathRequired(t *testing.T) {
	if _, err := Open(context.Background(), ""); err == nil {
		t.Fatal("Open(\"\") expected error, got nil")
	}
}

func TestReopen_IdempotentNoNewRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atlas-state.db")
	ctx := context.Background()

	s1, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	var beforeCount int
	if err := s1.sqlDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&beforeCount); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close #1: %v", err)
	}

	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open #2: %v", err)
	}
	defer s2.Close()

	var afterCount int
	if err := s2.sqlDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&afterCount); err != nil {
		t.Fatalf("count schema_migrations #2: %v", err)
	}
	if afterCount != beforeCount {
		t.Fatalf("schema_migrations row count changed across reopens: before=%d after=%d", beforeCount, afterCount)
	}
}

func TestOpen_AllTablesCreated(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	want := []string{
		"config", "features", "symbols", "edges", "feature_symbols",
		"file_hashes", "coverage_runs", "coverage_results",
		"audit_snapshots", "audit_snapshot_runs", "annotations",
		"schema_migrations", "snapshots",
	}

	rows, err := s.sqlDB().QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer rows.Close()

	seen := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seen[name] = true
	}
	for _, w := range want {
		if !seen[w] {
			t.Errorf("missing table %q", w)
		}
	}
}

func TestOpen_PragmasApplied(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	var journal string
	if err := s.sqlDB().QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if journal != "wal" {
		t.Errorf("journal_mode = %q, want \"wal\"", journal)
	}

	var fkOn int
	if err := s.sqlDB().QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fkOn); err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}
	if fkOn != 1 {
		t.Errorf("foreign_keys = %d, want 1", fkOn)
	}
}
