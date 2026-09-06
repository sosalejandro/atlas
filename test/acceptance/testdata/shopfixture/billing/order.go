// Package billing is one of two bounded contexts in the fixture. It declares
// an Order, and so does shipping — the collision is the point.
package billing

// Order is a duplicated type name: shipping declares an Order too. Two
// packages whose methods share a "Recv.Method" short id is the shape that
// used to make one of the two files invisible to the index (issue #85), and
// every statement in the invisible one was charged to nobody.
type Order struct {
	total int
	paid  bool
}

// Total returns the order total, floored at zero.
//
// @atlas:feature checkout.total
func (o *Order) Total() int {
	return normalize(o.total)
}

// Pay marks the order paid.
//
// The fixture's test never calls this, so it is the acceptance layer's
// known-uncovered symbol: a run that reports it as covered is reporting
// coverage the test suite did not produce.
//
// @atlas:feature checkout.pay
func (o *Order) Pay() {
	o.paid = true
}

// Paid reports whether the order has been paid.
//
// @atlas:feature checkout.pay
func (o *Order) Paid() bool {
	return o.paid
}

// normalize is package-private. The Go compiler instruments it for coverage
// like any other function, so it needs a symbol of its own — otherwise its
// statements land inside whichever neighbouring span happens to swallow them.
func normalize(total int) int {
	if total < 0 {
		return 0
	}
	return total
}
