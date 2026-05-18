package store

import (
	"context"
	"fmt"

	"github.com/sosalejandro/atlas/packages/shared"
)

// FeatureSymbolRole matches the CHECK constraint on `feature_symbols.role`.
type FeatureSymbolRole string

const (
	RoleTest     FeatureSymbolRole = "test"
	RoleImpl     FeatureSymbolRole = "impl"
	RoleContract FeatureSymbolRole = "contract"
)

// FeatureSymbolSource matches the CHECK constraint on `feature_symbols.source`.
type FeatureSymbolSource string

const (
	SourceAnnotation FeatureSymbolSource = "annotation"
	SourceInferred   FeatureSymbolSource = "inferred"
)

// FeatureSymbolLink is one row of the `feature_symbols` table
// (docs/schema-v1.md §5.6).
type FeatureSymbolLink struct {
	FeatureID shared.FeatureID    `json:"feature_id"`
	SymbolID  int64               `json:"symbol_id"`
	Role      FeatureSymbolRole   `json:"role"`
	Source    FeatureSymbolSource `json:"source"`
}

// FeatureSymbols is the narrow port for the `feature_symbols` link table.
type FeatureSymbols interface {
	// Link inserts a (feature, symbol, role) row. INSERT OR IGNORE — the
	// composite PK is the uniqueness invariant.
	Link(ctx context.Context, l FeatureSymbolLink) error

	// ListByFeature returns every link row for a feature, ordered by role
	// then symbol_id.
	ListByFeature(ctx context.Context, featureID shared.FeatureID) ([]FeatureSymbolLink, error)

	// ListBySymbol returns every link row for a symbol.
	ListBySymbol(ctx context.Context, symbolID int64) ([]FeatureSymbolLink, error)

	// Unlink removes a single link row. Returns shared.ErrNotFound if the
	// row didn't exist.
	Unlink(ctx context.Context, featureID shared.FeatureID, symbolID int64, role FeatureSymbolRole) error
}

var _ FeatureSymbols = (*featureSymbolsStore)(nil)

// FeatureSymbols returns the Store's FeatureSymbols port.
func (s *Store) FeatureSymbols() FeatureSymbols { return &featureSymbolsStore{db: s} }

type featureSymbolsStore struct{ db *Store }

const featureSymbolsSelectCols = `feature_id, symbol_id, role, source`

func scanFeatureSymbolLink(row interface{ Scan(...any) error }) (FeatureSymbolLink, error) {
	var (
		l      FeatureSymbolLink
		role   string
		source string
	)
	if err := row.Scan(&l.FeatureID, &l.SymbolID, &role, &source); err != nil {
		return FeatureSymbolLink{}, err
	}
	l.Role = FeatureSymbolRole(role)
	l.Source = FeatureSymbolSource(source)
	return l, nil
}

func (s *featureSymbolsStore) Link(ctx context.Context, l FeatureSymbolLink) error {
	if l.FeatureID == "" {
		return fmt.Errorf("feature_symbols link: feature_id required")
	}
	if l.SymbolID == 0 {
		return fmt.Errorf("feature_symbols link: symbol_id required")
	}
	if l.Role == "" {
		return fmt.Errorf("feature_symbols link: role required")
	}
	if l.Source == "" {
		l.Source = SourceAnnotation
	}

	_, err := s.db.sqlDB().ExecContext(ctx, `
		INSERT OR IGNORE INTO feature_symbols (feature_id, symbol_id, role, source)
		VALUES (?, ?, ?, ?)
	`, string(l.FeatureID), l.SymbolID, string(l.Role), string(l.Source))
	if err != nil {
		return fmt.Errorf("feature_symbols link: %w", err)
	}
	return nil
}

func (s *featureSymbolsStore) ListByFeature(ctx context.Context, featureID shared.FeatureID) ([]FeatureSymbolLink, error) {
	rows, err := s.db.sqlDB().QueryContext(ctx,
		`SELECT `+featureSymbolsSelectCols+` FROM feature_symbols
		 WHERE feature_id = ? ORDER BY role, symbol_id`, string(featureID))
	if err != nil {
		return nil, fmt.Errorf("feature_symbols by-feature: %w", err)
	}
	defer rows.Close()

	var out []FeatureSymbolLink
	for rows.Next() {
		l, err := scanFeatureSymbolLink(rows)
		if err != nil {
			return nil, fmt.Errorf("feature_symbols scan: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *featureSymbolsStore) ListBySymbol(ctx context.Context, symbolID int64) ([]FeatureSymbolLink, error) {
	rows, err := s.db.sqlDB().QueryContext(ctx,
		`SELECT `+featureSymbolsSelectCols+` FROM feature_symbols
		 WHERE symbol_id = ? ORDER BY feature_id, role`, symbolID)
	if err != nil {
		return nil, fmt.Errorf("feature_symbols by-symbol: %w", err)
	}
	defer rows.Close()

	var out []FeatureSymbolLink
	for rows.Next() {
		l, err := scanFeatureSymbolLink(rows)
		if err != nil {
			return nil, fmt.Errorf("feature_symbols scan: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *featureSymbolsStore) Unlink(ctx context.Context, featureID shared.FeatureID, symbolID int64, role FeatureSymbolRole) error {
	res, err := s.db.sqlDB().ExecContext(ctx,
		`DELETE FROM feature_symbols WHERE feature_id = ? AND symbol_id = ? AND role = ?`,
		string(featureID), symbolID, string(role))
	if err != nil {
		return fmt.Errorf("feature_symbols unlink: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return shared.ErrNotFound
	}
	return nil
}
