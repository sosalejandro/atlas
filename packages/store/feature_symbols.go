package store

import (
	"context"
	"fmt"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store/sqlc"
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
func (s *Store) FeatureSymbols() FeatureSymbols {
	return &featureSymbolsStore{q: s.queries()}
}

type featureSymbolsStore struct{ q *sqlc.Queries }

func fromSQLCFeatureSymbol(r sqlc.FeatureSymbol) FeatureSymbolLink {
	return FeatureSymbolLink{
		FeatureID: shared.FeatureID(r.FeatureID),
		SymbolID:  r.SymbolID,
		Role:      FeatureSymbolRole(r.Role),
		Source:    FeatureSymbolSource(r.Source),
	}
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

	err := s.q.LinkFeatureSymbol(ctx, sqlc.LinkFeatureSymbolParams{
		FeatureID: string(l.FeatureID),
		SymbolID:  l.SymbolID,
		Role:      string(l.Role),
		Source:    string(l.Source),
	})
	if err != nil {
		return fmt.Errorf("feature_symbols link: %w", err)
	}
	return nil
}

func (s *featureSymbolsStore) ListByFeature(ctx context.Context, featureID shared.FeatureID) ([]FeatureSymbolLink, error) {
	rows, err := s.q.ListFeatureSymbolsByFeature(ctx, string(featureID))
	if err != nil {
		return nil, fmt.Errorf("feature_symbols by-feature: %w", err)
	}
	out := make([]FeatureSymbolLink, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromSQLCFeatureSymbol(r))
	}
	return out, nil
}

func (s *featureSymbolsStore) ListBySymbol(ctx context.Context, symbolID int64) ([]FeatureSymbolLink, error) {
	rows, err := s.q.ListFeatureSymbolsBySymbol(ctx, symbolID)
	if err != nil {
		return nil, fmt.Errorf("feature_symbols by-symbol: %w", err)
	}
	out := make([]FeatureSymbolLink, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromSQLCFeatureSymbol(r))
	}
	return out, nil
}

func (s *featureSymbolsStore) Unlink(ctx context.Context, featureID shared.FeatureID, symbolID int64, role FeatureSymbolRole) error {
	n, err := s.q.UnlinkFeatureSymbol(ctx, sqlc.UnlinkFeatureSymbolParams{
		FeatureID: string(featureID),
		SymbolID:  symbolID,
		Role:      string(role),
	})
	if err != nil {
		return fmt.Errorf("feature_symbols unlink: %w", err)
	}
	if n == 0 {
		return shared.ErrNotFound
	}
	return nil
}
