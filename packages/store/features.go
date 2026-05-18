package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store/sqlc"
)

// FeatureKind matches the CHECK constraint on `features.kind`.
type FeatureKind string

const (
	FeatureKindFeature  FeatureKind = "feature"
	FeatureKindContract FeatureKind = "contract"
)

// Feature is one row of the `features` table (docs/schema-v1.md §5.3).
//
// DeprecatedSince / IntroducedIn are pointer-to-string because the column
// is nullable in SQL. CreatedAt + UpdatedAt are populated by SQLite.
type Feature struct {
	ID              shared.FeatureID `json:"id"`
	Title           string           `json:"title"`
	Owner           *string          `json:"owner,omitempty"`
	Kind            FeatureKind      `json:"kind"`
	DeprecatedSince *string          `json:"deprecated_since,omitempty"`
	IntroducedIn    *string          `json:"introduced_in,omitempty"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

// FeatureFilter narrows List queries. Zero-value returns every feature.
type FeatureFilter struct {
	Kind *FeatureKind
	IDs  []shared.FeatureID
}

// Features is the narrow port for the `features` table.
type Features interface {
	Get(ctx context.Context, id shared.FeatureID) (Feature, error)
	List(ctx context.Context, f FeatureFilter) ([]Feature, error)
	Upsert(ctx context.Context, feat Feature) error
	Delete(ctx context.Context, id shared.FeatureID) error
}

var _ Features = (*featuresStore)(nil)

// Features returns the Store's Features port.
func (s *Store) Features() Features { return &featuresStore{db: s, q: s.queries()} }

type featuresStore struct {
	db *Store
	q  *sqlc.Queries
}

// fromSQLCFeature converts a sqlc.Feature row to the store-facing Feature
// domain type. The sqlc layer carries strings + pointers; this is the only
// place that knows the field-name mapping.
func fromSQLCFeature(r sqlc.Feature) Feature {
	return Feature{
		ID:              shared.FeatureID(r.ID),
		Title:           r.Title,
		Owner:           r.Owner,
		Kind:            FeatureKind(r.Kind),
		DeprecatedSince: r.DeprecatedSince,
		IntroducedIn:    r.IntroducedIn,
		CreatedAt:       r.CreatedAt,
		UpdatedAt:       r.UpdatedAt,
	}
}

func (s *featuresStore) Get(ctx context.Context, id shared.FeatureID) (Feature, error) {
	row, err := s.q.GetFeature(ctx, string(id))
	if errors.Is(err, sql.ErrNoRows) {
		return Feature{}, shared.ErrFeatureNotFound
	}
	if err != nil {
		return Feature{}, fmt.Errorf("features get %q: %w", id, err)
	}
	return fromSQLCFeature(row), nil
}

// List dispatches across sqlc's two static query variants
// (ListAllFeatures / ListFeaturesByKind) and falls back to a raw query
// when the caller passes an IDs filter — sqlc's sqlite engine does not yet
// support `sqlc.slice()` placeholders cleanly, so the IN(...) case stays as
// a tiny hand-written SELECT.
func (s *featuresStore) List(ctx context.Context, f FeatureFilter) ([]Feature, error) {
	// Fast paths: nothing or only Kind via sqlc.
	if len(f.IDs) == 0 {
		var rows []sqlc.Feature
		var err error
		if f.Kind != nil {
			rows, err = s.q.ListFeaturesByKind(ctx, string(*f.Kind))
		} else {
			rows, err = s.q.ListAllFeatures(ctx)
		}
		if err != nil {
			return nil, fmt.Errorf("features list: %w", err)
		}
		out := make([]Feature, 0, len(rows))
		for _, r := range rows {
			out = append(out, fromSQLCFeature(r))
		}
		return out, nil
	}

	// IDs filter — issue a single SELECT with IN(...) bound via positional ?.
	placeholders := strings.Repeat("?,", len(f.IDs))
	placeholders = placeholders[:len(placeholders)-1]
	q := `SELECT id, title, owner, kind, deprecated_since, introduced_in, created_at, updated_at
	      FROM features WHERE id IN (` + placeholders + `)`
	args := make([]any, 0, len(f.IDs)+1)
	for _, id := range f.IDs {
		args = append(args, string(id))
	}
	if f.Kind != nil {
		q += " AND kind = ?"
		args = append(args, string(*f.Kind))
	}
	q += " ORDER BY id"

	rows, err := s.db.sqlDB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("features list (IDs): %w", err)
	}
	defer rows.Close()

	var out []Feature
	for rows.Next() {
		var (
			id, title, kind string
			owner, dep, intr sql.NullString
			created, updated time.Time
		)
		if err := rows.Scan(&id, &title, &owner, &kind, &dep, &intr, &created, &updated); err != nil {
			return nil, fmt.Errorf("features scan: %w", err)
		}
		out = append(out, Feature{
			ID:              shared.FeatureID(id),
			Title:           title,
			Owner:           nullStringToPtr(owner),
			Kind:            FeatureKind(kind),
			DeprecatedSince: nullStringToPtr(dep),
			IntroducedIn:    nullStringToPtr(intr),
			CreatedAt:       created,
			UpdatedAt:       updated,
		})
	}
	return out, rows.Err()
}

// Upsert inserts a new feature or updates the metadata of an existing one.
// updated_at is refreshed on every call; created_at is preserved.
//
// Kind defaults to FeatureKindFeature when empty so callers can rely on the
// CHECK constraint without having to set it explicitly.
func (s *featuresStore) Upsert(ctx context.Context, feat Feature) error {
	if feat.ID == "" {
		return fmt.Errorf("features upsert: id required")
	}
	if feat.Title == "" {
		// The CHECK constraint doesn't enforce NOT NULL meaningfully —
		// fail loudly here instead of letting an empty title slip in.
		return fmt.Errorf("features upsert %q: title required", feat.ID)
	}
	kind := feat.Kind
	if kind == "" {
		kind = FeatureKindFeature
	}

	err := s.q.UpsertFeature(ctx, sqlc.UpsertFeatureParams{
		ID:              string(feat.ID),
		Title:           feat.Title,
		Owner:           feat.Owner,
		Kind:            string(kind),
		DeprecatedSince: feat.DeprecatedSince,
		IntroducedIn:    feat.IntroducedIn,
	})
	if err != nil {
		return fmt.Errorf("features upsert %q: %w", feat.ID, err)
	}
	return nil
}

func (s *featuresStore) Delete(ctx context.Context, id shared.FeatureID) error {
	n, err := s.q.DeleteFeature(ctx, string(id))
	if err != nil {
		return fmt.Errorf("features delete %q: %w", id, err)
	}
	if n == 0 {
		return shared.ErrFeatureNotFound
	}
	return nil
}
