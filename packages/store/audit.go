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

// AuditSnapshot is one row of the `audit_snapshots` table
// (docs/schema-v1.md §5.10).
//
// LayerScoresJSON / BlockingFindingsJSON carry serialized JSON blobs in
// SQL — the schema deliberately keeps them as TEXT so the layer-weighting
// algorithm in packages/audit can evolve without per-version migrations.
type AuditSnapshot struct {
	ID                   int64            `json:"id"`
	TakenAt              time.Time        `json:"taken_at"`
	FeatureID            shared.FeatureID `json:"feature_id"`
	Score                int              `json:"score"`
	LayerScoresJSON      string           `json:"layer_scores_json"`
	BlockingFindingsJSON string           `json:"blocking_findings_json"`
}

// AuditSnapshots is the narrow port for the `audit_snapshots` table.
type AuditSnapshots interface {
	Insert(ctx context.Context, s AuditSnapshot) (int64, error)
	ListByFeature(ctx context.Context, featureID shared.FeatureID, limit int) ([]AuditSnapshot, error)
}

var _ AuditSnapshots = (*auditSnapshotsStore)(nil)

// AuditSnapshots returns the Store's AuditSnapshots port.
func (s *Store) AuditSnapshots() AuditSnapshots {
	return &auditSnapshotsStore{q: s.queries()}
}

type auditSnapshotsStore struct{ q *sqlc.Queries }

func (a *auditSnapshotsStore) Insert(ctx context.Context, s AuditSnapshot) (int64, error) {
	if s.FeatureID == "" {
		return 0, fmt.Errorf("audit_snapshots Insert: feature_id required")
	}
	if s.LayerScoresJSON == "" {
		s.LayerScoresJSON = "{}"
	}
	if s.BlockingFindingsJSON == "" {
		s.BlockingFindingsJSON = "[]"
	}
	if s.TakenAt.IsZero() {
		// Let SQLite supply CURRENT_TIMESTAMP by omitting the column.
		res, err := a.q.InsertAuditSnapshot(ctx, sqlc.InsertAuditSnapshotParams{
			FeatureID:            string(s.FeatureID),
			Score:                int64(s.Score),
			LayerScoresJson:      s.LayerScoresJSON,
			BlockingFindingsJson: s.BlockingFindingsJSON,
		})
		if err != nil {
			return 0, fmt.Errorf("audit_snapshots Insert: %w", err)
		}
		id, _ := res.LastInsertId()
		return id, nil
	}

	res, err := a.q.InsertAuditSnapshotWithTime(ctx, sqlc.InsertAuditSnapshotWithTimeParams{
		TakenAt:              s.TakenAt,
		FeatureID:            string(s.FeatureID),
		Score:                int64(s.Score),
		LayerScoresJson:      s.LayerScoresJSON,
		BlockingFindingsJson: s.BlockingFindingsJSON,
	})
	if err != nil {
		return 0, fmt.Errorf("audit_snapshots Insert: %w", err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func (a *auditSnapshotsStore) ListByFeature(ctx context.Context, featureID shared.FeatureID, limit int) ([]AuditSnapshot, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := a.q.ListAuditSnapshotsByFeature(ctx, sqlc.ListAuditSnapshotsByFeatureParams{
		FeatureID: string(featureID),
		Limit:     int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("audit_snapshots ListByFeature: %w", err)
	}
	out := make([]AuditSnapshot, 0, len(rows))
	for _, r := range rows {
		out = append(out, AuditSnapshot{
			ID:                   r.ID,
			TakenAt:              r.TakenAt,
			FeatureID:            shared.FeatureID(r.FeatureID),
			Score:                int(r.Score),
			LayerScoresJSON:      r.LayerScoresJson,
			BlockingFindingsJSON: r.BlockingFindingsJson,
		})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// audit_snapshot_runs — whole-project JSON-blob snapshots (Phase 6a).
//
// Distinct from the per-feature `audit_snapshots` table above: this stores
// ONE row per snapshot run with the full FeatureHealth slice JSON-encoded
// in score_json. Used by packages/audit which serialises its own type — the
// store layer treats the blob as opaque.
// ---------------------------------------------------------------------------

// AuditSnapshotRun is one row of the `audit_snapshot_runs` table.
//
// ScoreJSON is the JSON-encoded `[]audit.FeatureHealth` blob produced by
// packages/audit. The store deliberately holds it as a raw string —
// bumping the audit/ data shape doesn't require a schema migration.
type AuditSnapshotRun struct {
	ID         int64     `json:"id"`
	ComputedAt time.Time `json:"computed_at"`
	ScoreJSON  string    `json:"score_json"`
}

// AuditSnapshotRuns is the narrow port for the `audit_snapshot_runs` table.
type AuditSnapshotRuns interface {
	// Insert writes one snapshot row. The returned id is the row PK.
	// When run.ComputedAt is zero, SQLite supplies CURRENT_TIMESTAMP.
	Insert(ctx context.Context, run AuditSnapshotRun) (int64, error)

	// Get returns the snapshot row by id, or shared.ErrNotFound.
	Get(ctx context.Context, id int64) (AuditSnapshotRun, error)

	// Latest returns the most recently computed snapshot row, or
	// shared.ErrNotFound when no snapshot has been persisted yet.
	Latest(ctx context.Context) (AuditSnapshotRun, error)

	// List returns the most recent N snapshot rows in descending
	// computed_at order. A limit <= 0 defaults to 20.
	List(ctx context.Context, limit int) ([]AuditSnapshotRun, error)
}

var _ AuditSnapshotRuns = (*auditSnapshotRunsStore)(nil)

// AuditSnapshotRuns returns the Store's AuditSnapshotRuns port.
func (s *Store) AuditSnapshotRuns() AuditSnapshotRuns {
	return &auditSnapshotRunsStore{q: s.queries()}
}

type auditSnapshotRunsStore struct{ q *sqlc.Queries }

func (a *auditSnapshotRunsStore) Insert(ctx context.Context, run AuditSnapshotRun) (int64, error) {
	if run.ScoreJSON == "" {
		run.ScoreJSON = "[]"
	}
	if run.ComputedAt.IsZero() {
		res, err := a.q.InsertAuditSnapshotRun(ctx, run.ScoreJSON)
		if err != nil {
			return 0, fmt.Errorf("audit_snapshot_runs Insert: %w", err)
		}
		id, _ := res.LastInsertId()
		return id, nil
	}
	res, err := a.q.InsertAuditSnapshotRunWithTime(ctx, sqlc.InsertAuditSnapshotRunWithTimeParams{
		ComputedAt: run.ComputedAt,
		ScoreJson:  run.ScoreJSON,
	})
	if err != nil {
		return 0, fmt.Errorf("audit_snapshot_runs Insert (with time): %w", err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func (a *auditSnapshotRunsStore) Get(ctx context.Context, id int64) (AuditSnapshotRun, error) {
	row, err := a.q.GetAuditSnapshotRun(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return AuditSnapshotRun{}, shared.ErrNotFound
	}
	if err != nil {
		return AuditSnapshotRun{}, fmt.Errorf("audit_snapshot_runs Get %d: %w", id, err)
	}
	return AuditSnapshotRun{
		ID:         row.ID,
		ComputedAt: row.ComputedAt,
		ScoreJSON:  row.ScoreJson,
	}, nil
}

func (a *auditSnapshotRunsStore) Latest(ctx context.Context) (AuditSnapshotRun, error) {
	row, err := a.q.LatestAuditSnapshotRun(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return AuditSnapshotRun{}, shared.ErrNotFound
	}
	if err != nil {
		return AuditSnapshotRun{}, fmt.Errorf("audit_snapshot_runs Latest: %w", err)
	}
	return AuditSnapshotRun{
		ID:         row.ID,
		ComputedAt: row.ComputedAt,
		ScoreJSON:  row.ScoreJson,
	}, nil
}

func (a *auditSnapshotRunsStore) List(ctx context.Context, limit int) ([]AuditSnapshotRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := a.q.ListAuditSnapshotRuns(ctx, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("audit_snapshot_runs List: %w", err)
	}
	out := make([]AuditSnapshotRun, 0, len(rows))
	for _, r := range rows {
		out = append(out, AuditSnapshotRun{
			ID:         r.ID,
			ComputedAt: r.ComputedAt,
			ScoreJSON:  r.ScoreJson,
		})
	}
	return out, nil
}
