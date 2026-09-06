// Package broken does not type-check.
package broken

// Store is a plausible-looking type whose method calls a helper.
type Store struct{}

// Put is the call site that must fall back to name matching: the
// declaration below it is fine, but the package as a whole is rejected by
// the type checker, so nothing in it may be reported as typed.
func (s *Store) Put(key string) error {
	return s.write(key)
}

// write has a deliberate type error -- it returns a string where its
// signature promises an error. A missing import or a renamed field would
// do just as well; what matters is that go/packages reports an error for
// this package and none for its neighbour.
func (s *Store) write(key string) error {
	return key
}
