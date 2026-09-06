package persistence

import (
	"context"
	"strings"

	"example.com/orderd/internal/services/orders"
)

// MemoryOrderRepository is the in-process OrderRepository used by tests
// and by the dev daemon.
type MemoryOrderRepository struct {
	rows map[string]*orders.Order
}

// NewMemoryOrderRepository returns an empty in-memory repository.
func NewMemoryOrderRepository() *MemoryOrderRepository {
	return &MemoryOrderRepository{rows: map[string]*orders.Order{}}
}

// Save stores o under its normalised key.
func (r *MemoryOrderRepository) Save(_ context.Context, o *orders.Order) error {
	r.rows[r.key(o.ID)] = o
	return nil
}

// Find returns the order stored under id, or orders.ErrNotFound.
func (r *MemoryOrderRepository) Find(_ context.Context, id string) (*orders.Order, error) {
	row, ok := r.rows[r.key(id)]
	if !ok {
		return nil, orders.ErrNotFound
	}
	return row, nil
}

// key normalises an id into a map key.
func (r *MemoryOrderRepository) key(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}
