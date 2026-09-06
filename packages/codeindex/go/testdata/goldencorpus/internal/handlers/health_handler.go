package handlers

import "context"

// HealthHandler serves the liveness endpoint.
type HealthHandler struct{}

// NewHealthHandler returns a health handler.
func NewHealthHandler() *HealthHandler {
	return &HealthHandler{}
}

// @api GET /healthz
//
// Check reports process liveness.
func (h *HealthHandler) Check(_ context.Context) error {
	return nil
}
