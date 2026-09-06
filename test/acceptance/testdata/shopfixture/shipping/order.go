// Package shipping is the second bounded context. Its Order collides with
// billing.Order by short name, and its Total collides with billing's Total.
package shipping

// Order collides with billing.Order by short name.
type Order struct {
	weight int
}

// New builds a shipping order.
//
// @atlas:feature shipping.quote
func New(weight int) *Order {
	return &Order{weight: weight}
}

// Total returns the shipping cost. "Order.Total" is the id billing's method
// also computes, so exactly one of the two keeps it and the other is indexed
// package-qualified. Both must survive.
//
// @atlas:feature shipping.quote
func (o *Order) Total() int {
	return rate(o.weight)
}

// rate is package-private and partially covered: the fixture's test exercises
// only the light branch, so this symbol is where a partial statement fraction
// has to show up.
func rate(weight int) int {
	if weight > 10 {
		return weight * 2
	}
	return weight
}

// Surcharge is a function VALUE, not a function declaration, and it is
// deliberately the last thing in the file.
//
// The Go compiler instruments its body like any other code, so its statements
// appear in the coverprofile. The atlas scanner indexes FuncDecls, so there is
// no symbol for them to be charged to. That makes this the fixture's known
// blind spot, and it is here on purpose: it is the only shape in the fixture
// that can tell a REAL end_line from a guessed one.
//
// With rate's true end_line, these statements fall outside every span and are
// reported as an "outside-symbol-spans" gap — honestly unattributed. Without
// it, rate becomes the last symbol in the file, is given a span reaching to
// end-of-file, and silently absorbs them: rate's percentage then describes
// code rate does not contain.
var Surcharge = func(weight int) int {
	if weight > 100 {
		return 25
	}
	return 0
}
