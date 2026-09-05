package billing

// Order is a duplicated type name: shipping declares an Order too. This is
// the shape that used to make a whole file invisible to atlas.
type Order struct {
	total int
	paid  bool
}

// Total returns the order total.
func (o *Order) Total() int {
	return normalize(o.total)
}

// Pay marks the order paid. Never exercised by the fixture's test, so it
// must show up as executed=0.
func (o *Order) Pay() {
	o.paid = true
}

// normalize is package-private: instrumented by the compiler, so its
// statements have to land on a symbol of their own.
func normalize(total int) int {
	if total < 0 {
		return 0
	}
	return total
}
