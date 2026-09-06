// Package persistence holds the order repository port and its two
// implementations.
package persistence

import (
	"context"

	"example.com/orderd/internal/services/orders"
)

// OrderRepository is the port the order service depends on. It has two
// implementations in this package, which is why interface-typed calls
// through it cannot resolve to a single concrete callee.
type OrderRepository interface {
	Save(ctx context.Context, o *orders.Order) error
	Find(ctx context.Context, id string) (*orders.Order, error)
}
