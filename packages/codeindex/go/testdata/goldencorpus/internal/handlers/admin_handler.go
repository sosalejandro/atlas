package handlers

import "context"

// AdminHandler serves the operator endpoints. It declares a Get method,
// same as OrderHandler — so a route whose handler reference is only known
// by method name has two handler-kind candidates and must stay ambiguous
// rather than picking one.
type AdminHandler struct {
	token string
}

// NewAdminHandler returns an admin handler with no token configured.
func NewAdminHandler() *AdminHandler {
	return &AdminHandler{}
}

// @api GET /admin/orders/{id}
//
// Get returns the operator view of one order.
func (h *AdminHandler) Get(_ context.Context, _ string) (string, error) {
	return h.token, nil
}
