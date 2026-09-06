package orders

import "context"

// OrderRepository is the port the order service depends on.
//
// It is declared here, beside its consumer, rather than beside its two
// implementations in internal/persistence. That is not decoration: the
// implementations need orders.Order in their signatures, so a port
// declared in the adapter package makes orders and persistence import
// each other, and Go rejects the cycle. The fixture used to have exactly
// that cycle and got away with it only because nothing ever compiled it
// -- which stopped being true when issue #87 made the corpus a
// type-checked module.
//
// Both implementations still live in internal/persistence, so a call
// through this interface still has two concrete callees and no single
// answer a name heuristic could pick.
type OrderRepository interface {
	Save(ctx context.Context, o *Order) error
	Find(ctx context.Context, id string) (*Order, error)
}
