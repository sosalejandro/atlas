// Package orders holds the order aggregate and the service that mutates it.
package orders

// Order is the aggregate root.
type Order struct {
	ID     string
	Items  []Item
	Status string
}

// Item is one line on an order. The inventory package declares an Item
// too; the two share a struct-field namespace inside the scanner, which
// is exactly the kind of aliasing the determinism suite pins.
type Item struct {
	SKU   string
	Qty   int
	Price int
}

// Subtotal sums the order's line totals.
func (o *Order) Subtotal() int {
	total := 0
	for _, item := range o.Items {
		total += item.LineTotal()
	}
	return total
}

// LineTotal is the extended price for one line.
func (i Item) LineTotal() int {
	return i.Qty * i.Price
}
