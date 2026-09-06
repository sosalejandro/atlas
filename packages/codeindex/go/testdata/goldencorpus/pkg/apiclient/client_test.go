package apiclient

import (
	"context"
	"testing"
)

// @atlas:feature orders.create #real
//
// TestClient_CreateOrder is the client-side half of the create-order
// feature. The annotation sits on the test, not on the transport code it
// exercises — that is the attribution convention Atlas is built around.
func TestClient_CreateOrder(t *testing.T) {
	client := New("http://localhost:8080")
	if err := client.CreateOrder(context.Background(), "ord-1"); err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
}

// TestClient_CreateOrder_RejectsEmptyID carries no annotation on purpose:
// the corpus needs both annotated and unannotated tests.
func TestClient_CreateOrder_RejectsEmptyID(t *testing.T) {
	client := New("http://localhost:8080")
	if err := client.CreateOrder(context.Background(), ""); err == nil {
		t.Fatal("want error for empty id")
	}
}
