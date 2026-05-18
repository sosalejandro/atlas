package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store/sqlc"
)

// SnapshotRecord is one row of the `snapshots` table introduced in
// migration 0004 (Phase 6b).
//
// A snapshot is the JSON-serialised view of a project at a given git ref:
// the codeindex payload (symbols + edges + annotations + pattern matches)
// plus an optional audit slice. The diff/ package reads pairs of these
// rows to compute structured deltas.
//
// IndexJSON is always populated. AuditJSON is nil when the audit pass had
// not yet run at that ref — diff/ treats the nil case as "audit data
// missing on one side" and surfaces it via AuditDelta.MissingOnA /
// MissingOnB rather than as a noisy "all features removed" delta.
type SnapshotRecord struct {
	ID         int64     `json:"id"`
	GitRef     string    `json:"git_ref"`
	CapturedAt time.Time `json:"captured_at"`
	IndexJSON  string    `json:"index_json"`
	AuditJSON  *string   `json:"audit_json,omitempty"`
	Notes      *string   `json:"notes,omitempty"`
}

// CaptureInput is the payload Snapshots.Capture writes. The caller is
// responsible for marshalling the index (and optionally an audit slice)
// into JSON before calling; the store package never imports codeindex or
// audit directly, so it cannot do the marshalling for the caller.
//
// Why the caller marshals: Atlas has a directed dependency graph
// (codeindex → store, audit → store, diff → store + codeindex + audit).
// Putting the marshal logic in store/ would invert that graph.
type CaptureInput struct {
	GitRef     string
	IndexJSON  string
	AuditJSON  *string
	Notes      *string
	CapturedAt time.Time // optional; defaults to time.Now().UTC() when zero
}

// Snapshots is the narrow port for the `snapshots` table.
type Snapshots interface {
	// Capture writes a new snapshot row and returns its surrogate id.
	// CapturedAt defaults to the current UTC time when input.CapturedAt
	// is zero.
	Capture(ctx context.Context, input CaptureInput) (int64, error)

	// Get returns the snapshot row for the given id, or shared.ErrNotFound.
	Get(ctx context.Context, id int64) (SnapshotRecord, error)

	// List returns every snapshot row, newest first. When gitRef is
	// non-empty, only rows with a matching git_ref are returned.
	List(ctx context.Context, gitRef string) ([]SnapshotRecord, error)

	// Delete removes the snapshot row, returning shared.ErrNotFound when
	// no row had the id.
	Delete(ctx context.Context, id int64) error
}

var _ Snapshots = (*snapshotsStore)(nil)

// Snapshots returns the Store's Snapshots port.
func (s *Store) Snapshots() Snapshots { return &snapshotsStore{q: s.queries()} }

type snapshotsStore struct{ q *sqlc.Queries }

func (s *snapshotsStore) Capture(ctx context.Context, input CaptureInput) (int64, error) {
	if input.GitRef == "" {
		return 0, fmt.Errorf("snapshots capture: git_ref required")
	}
	if input.IndexJSON == "" {
		return 0, fmt.Errorf("snapshots capture: index_json required")
	}
	if !json.Valid([]byte(input.IndexJSON)) {
		return 0, fmt.Errorf("snapshots capture: index_json is not valid JSON")
	}
	if input.AuditJSON != nil && *input.AuditJSON != "" && !json.Valid([]byte(*input.AuditJSON)) {
		return 0, fmt.Errorf("snapshots capture: audit_json is not valid JSON")
	}

	when := input.CapturedAt
	if when.IsZero() {
		when = time.Now().UTC()
	}

	res, err := s.q.InsertSnapshot(ctx, sqlc.InsertSnapshotParams{
		GitRef:     input.GitRef,
		CapturedAt: when,
		IndexJson:  input.IndexJSON,
		AuditJson:  input.AuditJSON,
		Notes:      input.Notes,
	})
	if err != nil {
		return 0, fmt.Errorf("snapshots capture: %w", err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func (s *snapshotsStore) Get(ctx context.Context, id int64) (SnapshotRecord, error) {
	row, err := s.q.GetSnapshot(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return SnapshotRecord{}, shared.ErrNotFound
	}
	if err != nil {
		return SnapshotRecord{}, fmt.Errorf("snapshots get %d: %w", id, err)
	}
	return SnapshotRecord{
		ID:         row.ID,
		GitRef:     row.GitRef,
		CapturedAt: row.CapturedAt,
		IndexJSON:  row.IndexJson,
		AuditJSON:  row.AuditJson,
		Notes:      row.Notes,
	}, nil
}

func (s *snapshotsStore) List(ctx context.Context, gitRef string) ([]SnapshotRecord, error) {
	var (
		rows []sqlc.Snapshot
		err  error
	)
	if gitRef == "" {
		rows, err = s.q.ListAllSnapshots(ctx)
	} else {
		rows, err = s.q.ListSnapshotsByGitRef(ctx, gitRef)
	}
	if err != nil {
		return nil, fmt.Errorf("snapshots list: %w", err)
	}
	out := make([]SnapshotRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, SnapshotRecord{
			ID:         r.ID,
			GitRef:     r.GitRef,
			CapturedAt: r.CapturedAt,
			IndexJSON:  r.IndexJson,
			AuditJSON:  r.AuditJson,
			Notes:      r.Notes,
		})
	}
	return out, nil
}

func (s *snapshotsStore) Delete(ctx context.Context, id int64) error {
	n, err := s.q.DeleteSnapshot(ctx, id)
	if err != nil {
		return fmt.Errorf("snapshots delete %d: %w", id, err)
	}
	if n == 0 {
		return shared.ErrNotFound
	}
	return nil
}
