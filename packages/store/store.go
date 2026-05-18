package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "modernc.org/sqlite"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store/sqlc"
)

//go:embed schema/*.sql
var schemaFS embed.FS

// Store is the SQLite-backed handle returned by Open. It holds the
// underlying *sql.DB plus the path it was opened from (useful for
// diagnostics and the "reset" flow described in docs/schema-v1.md §10).
//
// One Store instance per process is the contract — SQLite is single-writer
// and Atlas always runs as a single CLI invocation. Multiple processes
// opening the same DB file are not supported, even though SQLite would not
// outright refuse.
//
// Internally the Store owns a *sqlc.Queries that wraps the *sql.DB. The
// per-table adapters (Features, Symbols, …) are thin Go wrappers around
// that generated Queries type plus a handful of raw queries for the cases
// sqlc's sqlite engine can't express (recursive CTE, dynamic-WHERE List).
type Store struct {
	conn   *sql.DB
	q      *sqlc.Queries
	path   string
	logger shared.Logger
}

// Open initializes (or opens existing) atlas-state.db at path, applies
// pending embedded migrations via golang-migrate, and returns a *Store
// ready for use. The caller is responsible for Close.
//
// DSN pragmas (per docs/schema-v1.md §3):
//
//   - journal_mode=WAL    — concurrent readers don't block on a writer
//   - foreign_keys=1      — Atlas relies on ON DELETE CASCADE
//   - busy_timeout=5000   — five-second wait before SQLITE_BUSY surfaces
//
// Migration tracking lives in golang-migrate's default `schema_migrations`
// table — the runner manages it, the application code never touches it
// directly except via SchemaVersion.
func Open(ctx context.Context, path string) (*Store, error) {
	return OpenWithLogger(ctx, path, shared.NopLogger{})
}

// OpenWithLogger is Open with a caller-supplied Logger. Production code
// uses shared.NewSlogLogger; tests use shared.NopLogger (the default in
// the Open shorthand).
func OpenWithLogger(ctx context.Context, path string, logger shared.Logger) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("store: open path is required")
	}
	if logger == nil {
		logger = shared.NopLogger{}
	}
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)",
		path,
	)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: sqlite open %s: %w", path, err)
	}
	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("store: sqlite ping %s: %w", path, err)
	}

	if err := runMigrations(conn, path); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("store: migrate %s: %w", path, err)
	}

	return &Store{
		conn:   conn,
		q:      sqlc.New(conn),
		path:   path,
		logger: logger,
	}, nil
}

// runMigrations applies pending up-only migrations from the embedded
// schema FS using golang-migrate's sqlite driver. Re-running is a no-op
// (migrate.ErrNoChange is swallowed) so Open is idempotent on re-opens.
//
// Up-only: Atlas does NOT ship `*.down.sql` files. golang-migrate tolerates
// their absence — it just loses the ability to step down, which Atlas
// doesn't need (the DB is a re-derivable cache; rollback is delete + reopen).
func runMigrations(db *sql.DB, path string) error {
	src, err := iofs.New(schemaFS, "schema")
	if err != nil {
		return fmt.Errorf("iofs source: %w", err)
	}
	driver, err := migratesqlite.WithInstance(db, &migratesqlite.Config{})
	if err != nil {
		return fmt.Errorf("sqlite driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, path, driver)
	if err != nil {
		return fmt.Errorf("migrate.NewWithInstance: %w", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate.Up: %w", err)
	}
	return nil
}

// Close releases the underlying *sql.DB. Idempotent.
func (s *Store) Close() error {
	if s == nil || s.conn == nil {
		return nil
	}
	return s.conn.Close()
}

// Path returns the on-disk path the Store was opened from.
func (s *Store) Path() string { return s.path }

// sqlDB exposes the underlying *sql.DB to adapters in this package.
// Intentionally unexported — external consumers must go through a port
// interface, not raw SQL.
func (s *Store) sqlDB() *sql.DB { return s.conn }

// queries exposes the sqlc-generated *sqlc.Queries to adapters in this
// package. Adapters never construct their own sqlc.Queries — they all share
// the Store's instance.
func (s *Store) queries() *sqlc.Queries { return s.q }

// Logger returns the logger this Store was opened with. Adapters use it
// for low-volume warnings (e.g. "edge references unknown symbol").
func (s *Store) Logger() shared.Logger { return s.logger }

// SchemaVersion returns the highest migration version recorded by
// golang-migrate in its `schema_migrations` table. Returns 0 if no
// migrations have been applied (which should never happen for a Store
// opened via Open — runMigrations runs in Open).
//
// A "dirty" schema_migrations row (mid-apply crash) is treated as the
// version having been attempted — callers that need the dirty flag should
// query the table directly.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.conn.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("schema_migrations max: %w", err)
	}
	return v, nil
}
