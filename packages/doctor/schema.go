package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sosalejandro/atlas/packages/store"
)

// schemaVersion reports what migration level the store is actually at,
// and whether a migration died half-way through.
//
// This is the one check written to work with no *store.Store, because
// the states it exists to report are precisely the states in which
// store.Open refuses: golang-migrate will not advance a dirty schema, so
// the moment the dirty flag matters is the moment every other atlas
// command dies in a wall of migrate output with no suggestion of what to
// do about it.
type schemaVersion struct{}

func (schemaVersion) Name() string { return "store.schema" }

func (schemaVersion) Examines() string {
	return "the applied migration version against the one this binary carries, and the dirty flag"
}

func (c schemaVersion) Run(ctx context.Context, env *Env) (Result, error) {
	if env.probe == nil {
		return c.unreadable(env), nil
	}
	applied, dirty, err := env.probe.migrationState(ctx)
	if err != nil {
		return Result{}, err
	}
	expected, expErr := expectedSchemaVersion(ctx)

	details := map[string]any{
		"applied": applied,
		"dirty":   dirty,
		"db_path": env.DBPath,
	}
	if expErr == nil {
		details["expected"] = expected
	}
	if env.Store == nil && env.StoreErr != nil {
		details["open_error"] = env.StoreErr.Error()
	}

	// Dirty first: it explains every other discrepancy, and it is the one
	// state a user cannot fix by re-running a command.
	if dirty {
		return Result{
			Severity: SeverityFail,
			Finding: fmt.Sprintf(
				"migration %d is marked dirty: it died part-way through, and no further "+
					"migration will run until that is cleared", applied),
			Remediation: fmt.Sprintf("rm %s && atlas init  # the state DB is a rebuildable cache", env.DBPath),
			Details:     details,
		}, nil
	}
	if expErr != nil {
		return Result{
			Severity: SeverityNotApplicable,
			Finding: fmt.Sprintf(
				"the store is at schema %d, but this binary's own expectation could not be "+
					"determined (%v), so the two cannot be compared", applied, expErr),
			Details: details,
		}, nil
	}

	switch {
	case applied == 0:
		return Result{
			Severity:    SeverityFail,
			Finding:     fmt.Sprintf("%s carries no applied migrations: it is not an atlas store", env.DBPath),
			Remediation: "atlas init",
			Details:     details,
		}, nil
	case applied > expected:
		// The dangerous direction, and the quiet one: migrate.Up is a
		// no-op against a schema newer than the binary's, so the store
		// opens cleanly and this binary then reads columns and tables it
		// was never built against.
		return Result{
			Severity: SeverityFail,
			Finding: fmt.Sprintf(
				"the store was migrated by a newer atlas (schema %d) than this binary understands (schema %d)",
				applied, expected),
			Remediation: "upgrade atlas to the version that wrote this store",
			Details:     details,
		}, nil
	case applied < expected:
		return Result{
			Severity: SeverityFail,
			Finding: fmt.Sprintf(
				"the store is at schema %d but this binary expects %d: pending migrations have not been applied",
				applied, expected),
			Remediation: "atlas scan  # opening the store applies pending migrations",
			Details:     details,
		}, nil
	default:
		return Result{
			Severity: SeverityOK,
			Finding: fmt.Sprintf("the store is at schema %d, the version this binary carries, and is not dirty",
				applied),
			Details: details,
		}, nil
	}
}

// unreadable is the verdict when doctor's own read-only handle would not
// attach to the database file.
//
// The severity turns on whether atlas can use the store at all. With a
// working *store.Store the repo is fine and only doctor's second handle
// failed -- that is a limitation of the diagnostic, not a finding about
// the repo, so it reports not-applicable. With no store either, nothing
// in atlas can read this database, and reporting that as "cannot check"
// would let a corrupt or absent state file exit zero -- the silence is
// worse than the lie.
func (c schemaVersion) unreadable(env *Env) Result {
	reason := "the state database could not be read"
	if env.probeErr != nil {
		reason = env.probeErr.Error()
	}
	details := map[string]any{"db_path": env.DBPath}
	if env.StoreErr != nil {
		details["open_error"] = env.StoreErr.Error()
	}
	if env.Store != nil {
		return Result{
			Severity: SeverityNotApplicable,
			Finding: reason + " -- atlas itself opened the store, so this is a limit of the " +
				"check, not a finding about the repo",
			Details: details,
		}
	}
	// A file that is simply absent has never been initialised; one that
	// exists but will not open is corrupt. The two need different first
	// moves, and telling someone to delete a file that is not there is
	// the kind of advice that makes a tool look like it is guessing.
	if _, statErr := os.Stat(env.DBPath); os.IsNotExist(statErr) {
		return Result{
			Severity:    SeverityFail,
			Finding:     fmt.Sprintf("there is no atlas state database at %s: this repo has never been scanned", env.DBPath),
			Remediation: "atlas init",
			Details:     details,
		}
	}
	finding := reason
	if env.StoreErr != nil {
		finding = fmt.Sprintf("%s; opening it as a store failed too: %v", reason, env.StoreErr)
	}
	return Result{
		Severity:    SeverityFail,
		Finding:     finding,
		Remediation: fmt.Sprintf("rm -f %s* && atlas init  # the state DB is a rebuildable cache", env.DBPath),
		Details:     details,
	}
}

// expectedSchemaVersion is the migration level this binary's embedded
// schema produces.
//
// It is measured, not declared. A `const expectedSchema = 12` sitting in
// this file would be one more thing that goes stale silently the next
// time someone adds a migration -- the exact bug class doctor exists to
// close, and it would be embarrassing to ship it inside doctor itself.
// Opening a throwaway store instead asks the only authority there is:
// what does store.Open actually do to an empty database? The cost is one
// temp file and a dozen small DDL statements, which is nothing beside the
// tree walk the index check already pays for.
func expectedSchemaVersion(ctx context.Context) (int, error) {
	dir, err := os.MkdirTemp("", "atlas-doctor-schema-")
	if err != nil {
		return 0, fmt.Errorf("doctor: temp dir for schema probe: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	s, err := store.Open(ctx, filepath.Join(dir, "probe.db"))
	if err != nil {
		return 0, fmt.Errorf("doctor: migrate a fresh store: %w", err)
	}
	defer func() { _ = s.Close() }()

	v, err := s.SchemaVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("doctor: read fresh store schema version: %w", err)
	}
	return v, nil
}
