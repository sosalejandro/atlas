package orders

import (
	"context"
	"testing"

	"example.com/orderd/internal/persistence"
)

// @atlas:feature orders.create #real
//
// TestOrderService_Create covers the happy path through the service.
func TestOrderService_Create(t *testing.T) {
	svc := NewOrderService(persistence.NewMemoryOrderRepository())
	order := &Order{ID: "ord-1", Items: []Item{{SKU: "a", Qty: 1, Price: 100}}}
	if err := svc.Create(context.Background(), order); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

// @atlas:feature orders.read
//
// TestOrderService_Get covers the read path.
func TestOrderService_Get(t *testing.T) {
	svc := NewOrderService(persistence.NewMemoryOrderRepository())
	if _, err := svc.Get(context.Background(), "missing"); err == nil {
		t.Fatal("want ErrNotFound")
	}
}
