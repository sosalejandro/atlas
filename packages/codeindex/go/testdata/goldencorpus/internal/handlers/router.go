package handlers

import "context"

// Router is the fixture's stand-in for a real mux: it holds the handlers
// as struct fields so the scanner can resolve chained selector calls.
type Router struct {
	order  *OrderHandler
	health *HealthHandler
	admin  *AdminHandler
}

// NewRouter wires the handler set.
func NewRouter(order *OrderHandler, health *HealthHandler, admin *AdminHandler) *Router {
	return &Router{order: order, health: health, admin: admin}
}

// Handle dispatches one request path.
func (r *Router) Handle(path string) error {
	ctx := context.Background()
	switch path {
	case "/healthz":
		return r.health.Check(ctx)
	case "/api/v1/orders":
		return r.order.Create(ctx, nil)
	default:
		_, err := r.admin.Get(ctx, path)
		return err
	}
}
