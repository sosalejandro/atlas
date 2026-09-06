// Package persistence holds the two implementations of the order
// repository port declared in internal/services/orders.
package persistence

import "example.com/orderd/internal/services/orders"

// The port lives with its consumer (see orders.OrderRepository); these
// assertions are what tie the two implementations to it at compile time.
// They also give the type checker a reason to record both concrete types
// as implementations of the interface, which is what makes the
// interface-dispatch edges in the golden snapshot resolvable.
var (
	_ orders.OrderRepository = (*MemoryOrderRepository)(nil)
	_ orders.OrderRepository = (*PostgresOrderRepository)(nil)
)
