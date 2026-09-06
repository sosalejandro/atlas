// Package inventory tracks on-hand stock per SKU.
package inventory

// Item is inventory's own notion of a line item — same type name as
// orders.Item, different fields and different methods.
type Item struct {
	SKU    string
	OnHand int
}

// InStock reports whether at least qty units are available.
func (i Item) InStock(qty int) bool {
	return i.OnHand >= qty
}
