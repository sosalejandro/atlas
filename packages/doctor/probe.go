package doctor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/sosalejandro/atlas/packages/shared"
	"os"

	// The "sqlite" driver name is registered by modernc.org/sqlite's init.
	// packages/store gets it transitively through golang-migrate; doctor
	// imports it directly so this file keeps working if that chain is ever
	// re-plumbed -- a diagnostic that silently loses its driver would
	// report "cannot read the schema" on a perfectly healthy store.
	_ "modernc.org/sqlite"
)

// probe is a second, read-only handle on the state database file.
//
// Every check reads through a store port where a port exists. Two facts
// have none, and deliberately so:
//
//   - golang-migrate's (version, dirty) bookkeeping. No port exposes it
//     because application code has no business writing that table, and
//     the dirty flag is only ever observable when store.Open has ALREADY
//     refused -- migrate.Up will not run against a dirty schema, so the
//     one moment the flag matters is the moment there is no Store to ask.
//
//   - the whole-table annotation sweep. The Annotations port reads one
//     file at a time, which is the right shape for the ingest and the
//     wrong shape for "find every annotation pointing at a feature that
//     no longer exists".
//
// Widening those ports for a diagnostic would be the wrong trade: doctor
// should not be able to change the thing it is diagnosing. So it opens
// its own connection with mode=ro, where SQLite itself enforces that.
type probe struct {
	db *sql.DB
}

// escapeDBPath makes path safe inside a `file:` DSN. See
// shared.EscapeSQLitePath for why this matters and how it was found.
func escapeDBPath(path string) string { return shared.EscapeSQLitePath(path) }

// openReadOnly attaches a read-only handle to path. A missing file is
// reported as such rather than created: sql.Open on a nonexistent SQLite
// path would happily produce an empty database, and a doctor that
// conjures the store it was asked to inspect is worse than no doctor.
func openReadOnly(path string) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("doctor: no state database path given")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("doctor: state database %s: %w", path, err)
	}
	// mode=ro plus a busy timeout: the store's own connection may hold a
	// WAL write lock while doctor reads, and failing the schema check on
	// a transient SQLITE_BUSY would be a false alarm.
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(5000)", escapeDBPath(path))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("doctor: open %s read-only: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("doctor: ping %s read-only: %w", path, err)
	}
	return db, nil
}

// IsAtlasStore reports whether path holds a database atlas has already
// migrated -- and answers WITHOUT writing to it.
//
// It exists because "does the file exist?" is not the same question.
// That was the CLI's whole guard: os.Stat succeeds, so hand the path to
// store.Open. But store.Open runs the embedded migrations, so anything
// that happened to be sitting at the state path -- a 0-byte placeholder
// left by an interrupted init, an unrelated SQLite file, a stray
// download -- got an atlas schema written into it by the one command
// whose stated contract is that a diagnostic must not conjure the state
// it was asked to inspect.
//
// The test is golang-migrate's own bookkeeping table, because its
// presence is exactly what makes a SQLite file an atlas store. A file
// that has it is safe to open read-write: at worst store.Open applies
// pending migrations to a store that was already ours. A file that does
// not is reported here, so the schema check can say "this is not an
// atlas store" instead of the migrator quietly making it into one.
func IsAtlasStore(ctx context.Context, path string) error {
	db, err := openReadOnly(path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	present, err := hasSchemaMigrations(ctx, db)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf(
			"doctor: %s is not an atlas state database (it carries no schema_migrations table)", path)
	}
	return nil
}

// hasSchemaMigrations asks whether the migration bookkeeping table
// exists.
//
// Asked of sqlite_master rather than by SELECTing the table and reading
// the failure: "no such table" reaches Go only as a driver-formatted
// string, and matching on that would break the first time the driver
// reworded it.
func hasSchemaMigrations(ctx context.Context, db *sql.DB) (bool, error) {
	var name string
	err := db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`,
	).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("doctor: read sqlite_master: %w", err)
	}
	return true, nil
}

func (e *Env) openProbe() error {
	db, err := openReadOnly(e.DBPath)
	if err != nil {
		return err
	}
	e.probe = &probe{db: db}
	return nil
}

func (e *Env) closeProbe() {
	if e.probe != nil {
		_ = e.probe.db.Close()
		e.probe = nil
	}
}

// migrationState reads golang-migrate's bookkeeping row.
//
// The table holds at most one row. Neither a missing row nor a missing
// TABLE is an error here: both mean no migration has ever been applied,
// which for an atlas store means the file is not an atlas store at all
// -- reported as version 0, which the schema check turns into a fail
// rather than pretending it is merely behind.
func (p *probe) migrationState(ctx context.Context) (version int, dirty bool, err error) {
	present, err := hasSchemaMigrations(ctx, p.db)
	if err != nil {
		return 0, false, err
	}
	if !present {
		return 0, false, nil
	}
	row := p.db.QueryRowContext(ctx,
		`SELECT version, dirty FROM schema_migrations LIMIT 1`)
	if err := row.Scan(&version, &dirty); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("doctor: read schema_migrations: %w", err)
	}
	return version, dirty, nil
}

// annotationRef is one feature/contract annotation as recorded, before
// its payload is split into ids.
type annotationRef struct {
	FilePath string
	Line     int
	Value    string
}

// listFeatureAnnotations returns every feature/contract annotation row.
// The kind filter matches store's materialization pass -- the other
// annotation kinds (owner, deprecated, since) never create a feature, so
// they cannot dangle.
func (p *probe) listFeatureAnnotations(ctx context.Context) ([]annotationRef, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT file_path, line, value FROM annotations `+
			`WHERE kind IN ('feature', 'contract') ORDER BY file_path, line`)
	if err != nil {
		return nil, fmt.Errorf("doctor: list feature annotations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]annotationRef, 0)
	for rows.Next() {
		var a annotationRef
		if err := rows.Scan(&a.FilePath, &a.Line, &a.Value); err != nil {
			return nil, fmt.Errorf("doctor: scan annotation row: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("doctor: iterate annotation rows: %w", err)
	}
	return out, nil
}
