package orders

import (
	"context"

	"example.com/orderd/internal/persistence"
)

// OrderService is the application service for the order aggregate.
type OrderService struct {
	repo persistence.OrderRepository
}

// NewOrderService returns a service backed by repo.
func NewOrderService(repo persistence.OrderRepository) *OrderService {
	return &OrderService{repo: repo}
}

// Create validates the order and hands it to the repository.
//
// The repo field is interface-typed and the interface has two
// implementations in the same package, so the scanner cannot pick a
// concrete callee. It does not fall back to an ambiguous edge either: the
// call is dropped, and the golden snapshot pins that absence.
func (s *OrderService) Create(ctx context.Context, o *Order) error {
	if err := s.validate(o); err != nil {
		return err
	}
	return s.repo.Save(ctx, o)
}

// Get loads one order by id.
func (s *OrderService) Get(ctx context.Context, id string) (*Order, error) {
	return s.repo.Find(ctx, id)
}

// validate is an unexported method, so the scanner keeps it and wires the
// Create -> validate edge.
func (s *OrderService) validate(o *Order) error {
	if len(o.Items) == 0 {
		return ErrEmpty
	}
	if o.Subtotal() <= 0 {
		return ErrEmpty
	}
	return nil
}
