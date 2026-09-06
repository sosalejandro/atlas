// Package orders_test is an EXTERNAL test package.
//
// It has to be: the test builds a real repository from
// internal/persistence, and persistence imports orders, so an in-package
// test file would close an import cycle. Go's answer to that is the
// _test package, and the fixture now uses it -- which also gives the
// scanner a shape it never had before, where the file's package clause
// and the package it tests are different names.
package orders_test

import (
	"context"
	"testing"

	"example.com/orderd/internal/persistence"
	"example.com/orderd/internal/services/orders"
)

// @atlas:feature orders.create #real
//
// TestOrderService_Create covers the happy path through the service.
func TestOrderService_Create(t *testing.T) {
	svc := orders.NewOrderService(persistence.NewMemoryOrderRepository())
	order := &orders.Order{ID: "ord-1", Items: []orders.Item{{SKU: "a", Qty: 1, Price: 100}}}
	if err := svc.Create(context.Background(), order); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

// @atlas:feature orders.read
//
// TestOrderService_Get covers the read path.
func TestOrderService_Get(t *testing.T) {
	svc := orders.NewOrderService(persistence.NewMemoryOrderRepository())
	if _, err := svc.Get(context.Background(), "missing"); err == nil {
		t.Fatal("want ErrNotFound")
	}
}
