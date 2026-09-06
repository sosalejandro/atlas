package inventory

import (
	"context"
	"errors"
)

// ErrOutOfStock is returned when a reservation exceeds on-hand stock.
var ErrOutOfStock = errors.New("inventory: out of stock")

// InventoryService is the application service for stock levels.
type InventoryService struct {
	stock map[string]Item
}

// NewInventoryService returns an empty in-memory inventory service.
func NewInventoryService() *InventoryService {
	return &InventoryService{stock: map[string]Item{}}
}

// Reserve decrements on-hand stock for sku.
func (s *InventoryService) Reserve(_ context.Context, sku string, qty int) error {
	if !s.available(sku, qty) {
		return ErrOutOfStock
	}
	item := s.stock[sku]
	item.OnHand -= qty
	s.stock[sku] = item
	return nil
}

// available is an unexported method that fans out to Item.InStock.
func (s *InventoryService) available(sku string, qty int) bool {
	item, ok := s.stock[sku]
	if !ok {
		return false
	}
	return item.InStock(qty)
}
