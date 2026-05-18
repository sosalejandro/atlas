package store

import (
	"context"
	"fmt"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
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
func (s *Store) AuditSnapshots() AuditSnapshots { return &auditSnapshotsStore{db: s} }

type auditSnapshotsStore struct{ db *Store }

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
		res, err := a.db.sqlDB().ExecContext(ctx, `
			INSERT INTO audit_snapshots (feature_id, score, layer_scores_json, blocking_findings_json)
			VALUES (?, ?, ?, ?)
		`, string(s.FeatureID), s.Score, s.LayerScoresJSON, s.BlockingFindingsJSON)
		if err != nil {
			return 0, fmt.Errorf("audit_snapshots Insert: %w", err)
		}
		id, _ := res.LastInsertId()
		return id, nil
	}

	res, err := a.db.sqlDB().ExecContext(ctx, `
		INSERT INTO audit_snapshots (taken_at, feature_id, score, layer_scores_json, blocking_findings_json)
		VALUES (?, ?, ?, ?, ?)
	`, s.TakenAt, string(s.FeatureID), s.Score, s.LayerScoresJSON, s.BlockingFindingsJSON)
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
	rows, err := a.db.sqlDB().QueryContext(ctx, `
		SELECT id, taken_at, feature_id, score, layer_scores_json, blocking_findings_json
		FROM audit_snapshots
		WHERE feature_id = ?
		ORDER BY taken_at DESC
		LIMIT ?
	`, string(featureID), limit)
	if err != nil {
		return nil, fmt.Errorf("audit_snapshots ListByFeature: %w", err)
	}
	defer rows.Close()

	var out []AuditSnapshot
	for rows.Next() {
		var s AuditSnapshot
		if err := rows.Scan(&s.ID, &s.TakenAt, &s.FeatureID, &s.Score, &s.LayerScoresJSON, &s.BlockingFindingsJSON); err != nil {
			return nil, fmt.Errorf("audit_snapshots scan: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
