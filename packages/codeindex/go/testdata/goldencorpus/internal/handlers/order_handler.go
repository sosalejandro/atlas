// Package handlers is the HTTP edge of the fixture service.
package handlers

import (
	"context"

	"example.com/orderd/internal/services/orders"
)

// OrderHandler serves the order endpoints.
type OrderHandler struct {
	svc *orders.OrderService
}

// NewOrderHandler returns a handler bound to svc.
func NewOrderHandler(svc *orders.OrderService) *OrderHandler {
	return &OrderHandler{svc: svc}
}

// @api POST /api/v1/orders
//
// Create accepts a new order.
func (h *OrderHandler) Create(ctx context.Context, body []byte) error {
	order, err := h.decode(body)
	if err != nil {
		return err
	}
	return h.svc.Create(ctx, order)
}

// @api GET /api/v1/orders/{id}
//
// Get returns one order.
func (h *OrderHandler) Get(ctx context.Context, id string) (*orders.Order, error) {
	return h.svc.Get(ctx, id)
}
