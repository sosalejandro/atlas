package store

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"

	"github.com/sosalejandro/atlas/packages/shared"
)

// Store is the SQLite-backed handle returned by Open. It holds the
// underlying *sql.DB plus the path it was opened from (useful for
// diagnostics and the "reset" flow described in docs/schema-v1.md §10).
//
// One Store instance per process is the contract — SQLite is single-writer
// and Atlas always runs as a single CLI invocation. Multiple processes
// opening the same DB file are not supported, even though SQLite would not
// outright refuse.
//
// All adapters in this package take a *Store and use db.sqlDB() to reach
// the underlying *sql.DB. Adapters never embed sql.DB directly.
type Store struct {
	conn   *sql.DB
	path   string
	logger shared.Logger
}

// Open initializes (or opens existing) atlas-state.db at path, applies
// pending embedded migrations, and returns a *Store ready for use. The
// caller is responsible for Close.
//
// DSN pragmas (per docs/schema-v1.md §3):
//
//   - journal_mode=WAL    — concurrent readers don't block on a writer
//   - foreign_keys=1      — Atlas relies on ON DELETE CASCADE
//   - busy_timeout=5000   — five-second wait before SQLITE_BUSY surfaces
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

	s := &Store{conn: conn, path: path, logger: logger}
	if err := s.migrate(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("store: migrate %s: %w", path, err)
	}
	return s, nil
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

// Logger returns the logger this Store was opened with. Adapters use it
// for low-volume warnings (e.g. "edge references unknown symbol").
func (s *Store) Logger() shared.Logger { return s.logger }
