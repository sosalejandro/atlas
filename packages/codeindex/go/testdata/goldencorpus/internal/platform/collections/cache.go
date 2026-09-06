// Package collections holds the fixture's one generic type.
//
// Generics are here because they are a symbol-identity hazard, not
// because the corpus needs a cache. A method on a generic receiver is
// declared once and type-checked once per instantiation, so a resolver
// that keys on the instantiated *types.Func sees Cache[string,*Order].Put
// and Cache[int,string].Put as two different callees and fragments the
// symbol table. The typed resolver maps every instantiation back to its
// generic declaration before looking up an id.
package collections

// Cache is a tiny generic map wrapper.
type Cache[K comparable, V any] struct {
	items map[K]V
}

// NewCache returns an empty cache.
func NewCache[K comparable, V any]() *Cache[K, V] {
	return &Cache[K, V]{items: map[K]V{}}
}

// Put stores v under k.
func (c *Cache[K, V]) Put(k K, v V) {
	c.items[k] = v
}

// Get returns the value stored under k.
func (c *Cache[K, V]) Get(k K) (V, bool) {
	v, ok := c.items[k]
	return v, ok
}

// Len reports how many entries the cache holds.
func (c *Cache[K, V]) Len() int {
	return len(c.items)
}
