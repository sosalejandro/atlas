package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store/sqlc"
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
func (s *Store) Coverage() Coverage { return &coverageStore{db: s, q: s.queries()} }

type coverageStore struct {
	db *Store
	q  *sqlc.Queries
}

func fromSQLCCoverageRun(r sqlc.CoverageRun) CoverageRun {
	return CoverageRun{
		ID:          r.ID,
		Framework:   Framework(r.Framework),
		StartedAt:   r.StartedAt,
		FinishedAt:  r.FinishedAt,
		RawPath:     r.RawPath,
		SummaryJSON: r.SummaryJson,
	}
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

	res, err := c.q.InsertCoverageRun(ctx, sqlc.InsertCoverageRunParams{
		Framework:   string(r.Framework),
		StartedAt:   r.StartedAt,
		FinishedAt:  r.FinishedAt,
		RawPath:     r.RawPath,
		SummaryJson: r.SummaryJSON,
	})
	if err != nil {
		return 0, fmt.Errorf("coverage InsertRun: %w", err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func (c *coverageStore) GetRun(ctx context.Context, id int64) (CoverageRun, error) {
	row, err := c.q.GetCoverageRun(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return CoverageRun{}, shared.ErrNotFound
	}
	if err != nil {
		return CoverageRun{}, fmt.Errorf("coverage GetRun %d: %w", id, err)
	}
	return fromSQLCCoverageRun(row), nil
}

func (c *coverageStore) ListRuns(ctx context.Context, framework Framework) ([]CoverageRun, error) {
	var (
		rows []sqlc.CoverageRun
		err  error
	)
	if framework == "" {
		rows, err = c.q.ListAllCoverageRuns(ctx)
	} else {
		rows, err = c.q.ListCoverageRunsByFramework(ctx, string(framework))
	}
	if err != nil {
		return nil, fmt.Errorf("coverage ListRuns: %w", err)
	}
	out := make([]CoverageRun, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromSQLCCoverageRun(r))
	}
	return out, nil
}

// InsertResults batches per-test rows in a single transaction. We wrap the
// shared sqlc.Queries via WithTx and call the generated InsertCoverageResult
// per row — the wrapper keeps the prepared-statement-ish behaviour with no
// per-call SQL parsing overhead.
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

	qtx := c.q.WithTx(tx)
	for i, r := range results {
		if r.Status == "" {
			return fmt.Errorf("coverage InsertResults: result %d has empty status", i)
		}
		var (
			symbolID  *int64
			featureID *string
		)
		if r.SymbolID != nil && *r.SymbolID != 0 {
			v := *r.SymbolID
			symbolID = &v
		}
		if r.FeatureID != nil && *r.FeatureID != "" {
			v := string(*r.FeatureID)
			featureID = &v
		}
		if err := qtx.InsertCoverageResult(ctx, sqlc.InsertCoverageResultParams{
			RunID:      runID,
			SymbolID:   symbolID,
			FeatureID:  featureID,
			Status:     string(r.Status),
			DurationMs: r.DurationMS,
			Message:    r.Message,
		}); err != nil {
			return fmt.Errorf("coverage InsertResults exec row %d: %w", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("coverage InsertResults commit: %w", err)
	}
	return nil
}

func (c *coverageStore) ListResults(ctx context.Context, runID int64) ([]CoverageResult, error) {
	rows, err := c.q.ListCoverageResults(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("coverage ListResults: %w", err)
	}
	out := make([]CoverageResult, 0, len(rows))
	for _, r := range rows {
		var fid *shared.FeatureID
		if r.FeatureID != nil {
			f := shared.FeatureID(*r.FeatureID)
			fid = &f
		}
		out = append(out, CoverageResult{
			ID:         r.ID,
			RunID:      r.RunID,
			SymbolID:   r.SymbolID,
			FeatureID:  fid,
			Status:     CoverageStatus(r.Status),
			DurationMS: r.DurationMs,
			Message:    r.Message,
		})
	}
	return out, nil
}
