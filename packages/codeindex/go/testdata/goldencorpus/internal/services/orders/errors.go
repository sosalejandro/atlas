package orders

import "errors"

// ErrNotFound is returned when no order matches the requested id.
var ErrNotFound = errors.New("orders: not found")

// ErrEmpty is returned when an order carries no lines.
var ErrEmpty = errors.New("orders: no items")

// IsNotFound reports whether err is (or wraps) ErrNotFound.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}
