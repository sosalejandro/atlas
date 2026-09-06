package handlers

import (
	"context"
	"testing"
)

// @atlas:feature orders.create #real
//
// TestOrderHandler_Create pins the HTTP edge of the create-order feature.
func TestOrderHandler_Create(t *testing.T) {
	handler := NewOrderHandler(nil)
	if err := handler.Create(context.Background(), nil); err == nil {
		t.Fatal("want error from a nil service")
	}
}
