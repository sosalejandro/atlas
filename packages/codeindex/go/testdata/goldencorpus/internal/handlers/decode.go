package handlers

import "example.com/orderd/internal/services/orders"

// decode turns a request body into an order.
//
// It lives in its own file rather than next to Create because the scanner
// attaches an @api annotation to the next function declared within ten
// lines of it — keeping the unexported helper out of that window keeps the
// golden snapshot free of an endpoint -> decode edge that nobody meant.
func (h *OrderHandler) decode(_ []byte) (*orders.Order, error) {
	return &orders.Order{}, nil
}
