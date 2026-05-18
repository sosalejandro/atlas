package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed schema/*.sql
var schemaFS embed.FS

// migration is one numbered .up.sql file pulled from the embedded schema dir.
type migration struct {
	version int
	name    string
	sql     string
}

// migrate applies any pending up migrations in numeric order. Each migration
// runs in its own transaction; the schema_version table tracks what has been
// applied so the runner is idempotent on re-open.
//
// Bootstrap: the schema_version table is created out-of-band on the first
// call so that the very first migration (0001) can record its own version
// inside the same transaction that ships its DDL.
//
// Up-only — Atlas does not ship .down.sql files. Per docs/schema-v1.md §10,
// rollback is "delete the file and re-init."
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.conn.ExecContext(ctx, schemaVersionDDL); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}

	pending, err := loadMigrations()
	if err != nil {
		return err
	}

	applied, err := s.appliedVersions(ctx)
	if err != nil {
		return err
	}

	for _, m := range pending {
		if applied[m.version] {
			continue
		}
		if err := s.applyOne(ctx, m); err != nil {
			return fmt.Errorf("apply migration %04d_%s: %w", m.version, m.name, err)
		}
	}
	return nil
}

const schemaVersionDDL = `CREATE TABLE IF NOT EXISTS schema_version (
  version    INTEGER PRIMARY KEY,
  applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);`

// loadMigrations reads schema/*.up.sql from the embedded FS, parses the
// numeric version prefix, and returns them sorted ascending.
//
// Filename grammar: NNNN_<name>.up.sql  (e.g. 0001_initial.up.sql).
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(schemaFS, "schema")
	if err != nil {
		return nil, fmt.Errorf("read embedded schema dir: %w", err)
	}

	var out []migration
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		ver, label, err := parseMigrationName(name)
		if err != nil {
			return nil, fmt.Errorf("malformed migration filename %q: %w", name, err)
		}
		raw, err := fs.ReadFile(schemaFS, "schema/"+name)
		if err != nil {
			return nil, fmt.Errorf("read embedded schema/%s: %w", name, err)
		}
		out = append(out, migration{version: ver, name: label, sql: string(raw)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

func parseMigrationName(filename string) (int, string, error) {
	base := strings.TrimSuffix(filename, ".up.sql")
	parts := strings.SplitN(base, "_", 2)
	if len(parts) != 2 {
		return 0, "", fmt.Errorf("expected NNNN_<name>.up.sql")
	}
	ver, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, "", fmt.Errorf("version prefix not numeric: %w", err)
	}
	return ver, parts[1], nil
}

func (s *Store) appliedVersions(ctx context.Context) (map[int]bool, error) {
	rows, err := s.conn.QueryContext(ctx, `SELECT version FROM schema_version`)
	if err != nil {
		return nil, fmt.Errorf("query schema_version: %w", err)
	}
	defer rows.Close()

	out := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan schema_version: %w", err)
		}
		out[v] = true
	}
	return out, rows.Err()
}

func (s *Store) applyOne(ctx context.Context, m migration) error {
	tx, err := s.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("exec migration sql: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, m.version); err != nil {
		return fmt.Errorf("record schema_version row: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// SchemaVersion returns the highest schema_version recorded in the DB.
// Returns 0 if no migrations have been applied yet (which should never
// happen for a Store opened via Open — migrate runs in Open).
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("schema_version max: %w", err)
	}
	return v, nil
}
