package store

import (
	"context"
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
