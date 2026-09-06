package doctor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

// openProbe attaches a read-only handle to e.DBPath. A missing file is
// reported as such rather than created: sql.Open on a nonexistent SQLite
// path would happily produce an empty database, and a doctor that
// conjures the store it was asked to inspect is worse than no doctor.
func (e *Env) openProbe() error {
	if e.DBPath == "" {
		return fmt.Errorf("doctor: no state database path given")
	}
	if _, err := os.Stat(e.DBPath); err != nil {
		return fmt.Errorf("doctor: state database %s: %w", e.DBPath, err)
	}
	// mode=ro plus a busy timeout: the store's own connection may hold a
	// WAL write lock while doctor reads, and failing the schema check on
	// a transient SQLITE_BUSY would be a false alarm.
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(5000)", e.DBPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("doctor: open %s read-only: %w", e.DBPath, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return fmt.Errorf("doctor: ping %s read-only: %w", e.DBPath, err)
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
// The table holds at most one row. Its absence means no migration has
// ever been applied, which for an atlas store means the file is not an
// atlas store at all -- reported as version 0, which the schema check
// turns into a fail rather than pretending it is merely behind.
func (p *probe) migrationState(ctx context.Context) (version int, dirty bool, err error) {
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
