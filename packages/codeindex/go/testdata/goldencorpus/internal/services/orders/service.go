package orders

import "context"

// OrderService is the application service for the order aggregate.
type OrderService struct {
	repo OrderRepository
}

// NewOrderService returns a service backed by repo.
func NewOrderService(repo OrderRepository) *OrderService {
	return &OrderService{repo: repo}
}

// Create validates the order and hands it to the repository.
//
// The repo field is interface-typed and the interface has two
// implementations, so s.repo.Save has no single concrete callee. This is
// the payoff case for issue #87: the AST resolver dropped the call
// entirely (nothing in the source names a receiver type), while the
// typed resolver reports BOTH implementations, marked ambiguous because
// class-hierarchy analysis genuinely cannot tell which one runs.
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
