package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
)

// Framework matches the CHECK constraint on `coverage_runs.framework`.
type Framework string

const (
	FrameworkGoTest     Framework = "go-test"
	FrameworkPlaywright Framework = "playwright"
	FrameworkVitest     Framework = "vitest"
	FrameworkJest       Framework = "jest"
	FrameworkMaestro    Framework = "maestro"
)

// CoverageStatus matches the CHECK constraint on `coverage_results.status`.
type CoverageStatus string

const (
	StatusPass CoverageStatus = "pass"
	StatusFail CoverageStatus = "fail"
	StatusSkip CoverageStatus = "skip"
)

// CoverageRun is one row of the `coverage_runs` table (docs/schema-v1.md §5.8).
type CoverageRun struct {
	ID          int64     `json:"id"`
	Framework   Framework `json:"framework"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
	RawPath     *string   `json:"raw_path,omitempty"`
	SummaryJSON string    `json:"summary_json"`
}

// CoverageResult is one row of the `coverage_results` table (§5.9).
//
// SymbolID and FeatureID are nullable: Playwright/Maestro tests don't map
// to a Go symbol, and legacy tests without an annotation don't map to a
// feature. Use pointers so the zero value is "unset".
type CoverageResult struct {
	ID         int64             `json:"id"`
	RunID      int64             `json:"run_id"`
	SymbolID   *int64            `json:"symbol_id,omitempty"`
	FeatureID  *shared.FeatureID `json:"feature_id,omitempty"`
	Status     CoverageStatus    `json:"status"`
	DurationMS int64             `json:"duration_ms"`
	Message    *string           `json:"message,omitempty"`
}

// Coverage is the narrow port for the `coverage_runs` + `coverage_results`
// tables. A single ingest is one transactional unit — InsertRun returns
// the surrogate id, then InsertResults batches the per-test rows under it.
type Coverage interface {
	InsertRun(ctx context.Context, r CoverageRun) (int64, error)
	GetRun(ctx context.Context, id int64) (CoverageRun, error)
	ListRuns(ctx context.Context, framework Framework) ([]CoverageRun, error)

	InsertResults(ctx context.Context, runID int64, results []CoverageResult) error
	ListResults(ctx context.Context, runID int64) ([]CoverageResult, error)
}

var _ Coverage = (*coverageStore)(nil)

// Coverage returns the Store's Coverage port.
func (s *Store) Coverage() Coverage { return &coverageStore{db: s} }

type coverageStore struct{ db *Store }

const coverageRunsSelectCols = `id, framework, started_at, finished_at, raw_path, summary_json`

func scanCoverageRun(row interface{ Scan(...any) error }) (CoverageRun, error) {
	var (
		r       CoverageRun
		fw      string
		rawPath sql.NullString
	)
	if err := row.Scan(&r.ID, &fw, &r.StartedAt, &r.FinishedAt, &rawPath, &r.SummaryJSON); err != nil {
		return CoverageRun{}, err
	}
	r.Framework = Framework(fw)
	r.RawPath = ptrString(rawPath)
	return r, nil
}

func (c *coverageStore) InsertRun(ctx context.Context, r CoverageRun) (int64, error) {
	if r.Framework == "" {
		return 0, fmt.Errorf("coverage InsertRun: framework required")
	}
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now().UTC()
	}
	if r.FinishedAt.IsZero() {
		r.FinishedAt = r.StartedAt
	}
	if r.SummaryJSON == "" {
		r.SummaryJSON = "{}"
	}

	res, err := c.db.sqlDB().ExecContext(ctx, `
		INSERT INTO coverage_runs (framework, started_at, finished_at, raw_path, summary_json)
		VALUES (?, ?, ?, ?, ?)
	`, string(r.Framework), r.StartedAt, r.FinishedAt, nullStringPtr(r.RawPath), r.SummaryJSON)
	if err != nil {
		return 0, fmt.Errorf("coverage InsertRun: %w", err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func (c *coverageStore) GetRun(ctx context.Context, id int64) (CoverageRun, error) {
	row := c.db.sqlDB().QueryRowContext(ctx,
		`SELECT `+coverageRunsSelectCols+` FROM coverage_runs WHERE id = ?`, id)
	r, err := scanCoverageRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return CoverageRun{}, shared.ErrNotFound
	}
	if err != nil {
		return CoverageRun{}, fmt.Errorf("coverage GetRun %d: %w", id, err)
	}
	return r, nil
}

func (c *coverageStore) ListRuns(ctx context.Context, framework Framework) ([]CoverageRun, error) {
	var (
		q    = `SELECT ` + coverageRunsSelectCols + ` FROM coverage_runs`
		args []any
	)
	if framework != "" {
		q += ` WHERE framework = ?`
		args = append(args, string(framework))
	}
	q += ` ORDER BY finished_at DESC, id DESC`

	rows, err := c.db.sqlDB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("coverage ListRuns: %w", err)
	}
	defer rows.Close()

	var out []CoverageRun
	for rows.Next() {
		r, err := scanCoverageRun(rows)
		if err != nil {
			return nil, fmt.Errorf("coverage runs scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (c *coverageStore) InsertResults(ctx context.Context, runID int64, results []CoverageResult) error {
	if runID == 0 {
		return fmt.Errorf("coverage InsertResults: run_id required")
	}
	if len(results) == 0 {
		return nil
	}

	tx, err := c.db.sqlDB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("coverage InsertResults begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO coverage_results (run_id, symbol_id, feature_id, status, duration_ms, message)
		VALUES (?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("coverage InsertResults prepare: %w", err)
	}
	defer stmt.Close()

	for i, r := range results {
		if r.Status == "" {
			return fmt.Errorf("coverage InsertResults: result %d has empty status", i)
		}
		var (
			featureID sql.NullString
			symbolID  sql.NullInt64
		)
		if r.FeatureID != nil && *r.FeatureID != "" {
			featureID = sql.NullString{String: string(*r.FeatureID), Valid: true}
		}
		if r.SymbolID != nil && *r.SymbolID != 0 {
			symbolID = sql.NullInt64{Int64: *r.SymbolID, Valid: true}
		}
		if _, err := stmt.ExecContext(ctx,
			runID, symbolID, featureID, string(r.Status), r.DurationMS, nullStringPtr(r.Message),
		); err != nil {
			return fmt.Errorf("coverage InsertResults exec row %d: %w", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("coverage InsertResults commit: %w", err)
	}
	return nil
}

func (c *coverageStore) ListResults(ctx context.Context, runID int64) ([]CoverageResult, error) {
	rows, err := c.db.sqlDB().QueryContext(ctx, `
		SELECT id, run_id, symbol_id, feature_id, status, duration_ms, message
		FROM coverage_results WHERE run_id = ? ORDER BY id
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("coverage ListResults: %w", err)
	}
	defer rows.Close()

	var out []CoverageResult
	for rows.Next() {
		var (
			r         CoverageResult
			symbolID  sql.NullInt64
			featureID sql.NullString
			status    string
			message   sql.NullString
		)
		if err := rows.Scan(&r.ID, &r.RunID, &symbolID, &featureID, &status, &r.DurationMS, &message); err != nil {
			return nil, fmt.Errorf("coverage results scan: %w", err)
		}
		if symbolID.Valid {
			v := symbolID.Int64
			r.SymbolID = &v
		}
		if featureID.Valid {
			v := shared.FeatureID(featureID.String)
			r.FeatureID = &v
		}
		r.Status = CoverageStatus(status)
		r.Message = ptrString(message)
		out = append(out, r)
	}
	return out, rows.Err()
}
