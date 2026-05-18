package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
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
func (s *Store) Features() Features { return &featuresStore{db: s} }

type featuresStore struct{ db *Store }

const featuresSelectCols = `id, title, owner, kind, deprecated_since, introduced_in, created_at, updated_at`

func scanFeature(row interface{ Scan(...any) error }) (Feature, error) {
	var (
		f               Feature
		owner           sql.NullString
		kind            string
		deprecatedSince sql.NullString
		introducedIn    sql.NullString
	)
	if err := row.Scan(&f.ID, &f.Title, &owner, &kind, &deprecatedSince, &introducedIn, &f.CreatedAt, &f.UpdatedAt); err != nil {
		return Feature{}, err
	}
	f.Owner = ptrString(owner)
	f.Kind = FeatureKind(kind)
	f.DeprecatedSince = ptrString(deprecatedSince)
	f.IntroducedIn = ptrString(introducedIn)
	return f, nil
}

func (s *featuresStore) Get(ctx context.Context, id shared.FeatureID) (Feature, error) {
	row := s.db.sqlDB().QueryRowContext(ctx,
		`SELECT `+featuresSelectCols+` FROM features WHERE id = ?`, string(id))
	f, err := scanFeature(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Feature{}, shared.ErrFeatureNotFound
	}
	if err != nil {
		return Feature{}, fmt.Errorf("features get %q: %w", id, err)
	}
	return f, nil
}

func (s *featuresStore) List(ctx context.Context, f FeatureFilter) ([]Feature, error) {
	var (
		where []string
		args  []any
	)
	if f.Kind != nil {
		where = append(where, "kind = ?")
		args = append(args, string(*f.Kind))
	}
	if len(f.IDs) > 0 {
		placeholders := strings.Repeat("?,", len(f.IDs))
		placeholders = placeholders[:len(placeholders)-1]
		where = append(where, "id IN ("+placeholders+")")
		for _, id := range f.IDs {
			args = append(args, string(id))
		}
	}

	q := `SELECT ` + featuresSelectCols + ` FROM features`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY id"

	rows, err := s.db.sqlDB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("features list: %w", err)
	}
	defer rows.Close()

	var out []Feature
	for rows.Next() {
		feat, err := scanFeature(rows)
		if err != nil {
			return nil, fmt.Errorf("features scan: %w", err)
		}
		out = append(out, feat)
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

	_, err := s.db.sqlDB().ExecContext(ctx, `
		INSERT INTO features (id, title, owner, kind, deprecated_since, introduced_in, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
		  title            = excluded.title,
		  owner            = excluded.owner,
		  kind             = excluded.kind,
		  deprecated_since = excluded.deprecated_since,
		  introduced_in    = excluded.introduced_in,
		  updated_at       = CURRENT_TIMESTAMP
	`,
		string(feat.ID), feat.Title, nullStringPtr(feat.Owner), string(kind),
		nullStringPtr(feat.DeprecatedSince), nullStringPtr(feat.IntroducedIn),
	)
	if err != nil {
		return fmt.Errorf("features upsert %q: %w", feat.ID, err)
	}
	return nil
}

func (s *featuresStore) Delete(ctx context.Context, id shared.FeatureID) error {
	res, err := s.db.sqlDB().ExecContext(ctx, `DELETE FROM features WHERE id = ?`, string(id))
	if err != nil {
		return fmt.Errorf("features delete %q: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return shared.ErrFeatureNotFound
	}
	return nil
}
