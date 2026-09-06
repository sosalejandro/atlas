package persistence

import (
	"context"
	"database/sql"

	"example.com/orderd/internal/services/orders"
)

// PostgresOrderRepository is the production OrderRepository.
type PostgresOrderRepository struct {
	db   *sql.DB
	conf Config
}

// NewPostgresOrderRepository returns a repository bound to db.
func NewPostgresOrderRepository(db *sql.DB, conf Config) *PostgresOrderRepository {
	return &PostgresOrderRepository{db: db, conf: conf}
}

// Save upserts o through the generated query layer.
func (r *PostgresOrderRepository) Save(ctx context.Context, o *orders.Order) error {
	return UpsertOrder(ctx, r.db, o.ID, o.Status)
}

// Find loads one order through the generated query layer.
func (r *PostgresOrderRepository) Find(ctx context.Context, id string) (*orders.Order, error) {
	return SelectOrder(ctx, r.db, id)
}
