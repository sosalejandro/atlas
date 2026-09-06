package persistence

import (
	"context"
	"strings"

	"example.com/orderd/internal/platform/collections"
	"example.com/orderd/internal/services/orders"
)

// MemoryOrderRepository is the in-process OrderRepository used by tests
// and by the dev daemon.
//
// The rows field is a generic type on purpose. `r.rows.Put(...)` is a
// chained selector whose receiver type the AST resolver renders as "?"
// (typeExprString has no case for an instantiated type), so it dropped
// the call outright. The typed resolver reads the field's type from
// go/types, lands on Cache[string,*orders.Order].Put, and maps that
// instantiation back to the generic declaration the scanner indexed.
type MemoryOrderRepository struct {
	rows *collections.Cache[string, *orders.Order]
}

// NewMemoryOrderRepository returns an empty in-memory repository.
func NewMemoryOrderRepository() *MemoryOrderRepository {
	return &MemoryOrderRepository{rows: collections.NewCache[string, *orders.Order]()}
}

// Save stores o under its normalised key.
func (r *MemoryOrderRepository) Save(_ context.Context, o *orders.Order) error {
	r.rows.Put(r.key(o.ID), o)
	return nil
}

// Find returns the order stored under id, or orders.ErrNotFound.
func (r *MemoryOrderRepository) Find(_ context.Context, id string) (*orders.Order, error) {
	row, ok := r.rows.Get(r.key(id))
	if !ok {
		return nil, orders.ErrNotFound
	}
	return row, nil
}

// key normalises an id into a map key.
func (r *MemoryOrderRepository) key(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}
