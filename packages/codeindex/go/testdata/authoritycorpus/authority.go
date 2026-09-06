// Package authority holds call sites the type checker resolves to
// declarations atlas does not index, next to the local declarations the
// name ladder would reach for if it were allowed a second opinion.
package authority

import (
	"strings"
	"sync"
)

// Builder shares a name and a method with strings.Builder and nothing
// else. That coincidence is the whole point: fuzzyResolveMethod matches
// on a lowercased substring of the receiver type name, so given the
// rendered field type "strings.Builder" it will happily land on this
// declaration, in this package, which Report.Add never calls.
type Builder struct{ n int }

// WriteString is indexed as "Builder.WriteString".
func (b *Builder) WriteString(s string) { b.n += len(s) }

// Report calls two standard-library methods through fields.
//
//   - r.buf.WriteString binds, by type, to strings.Builder.WriteString.
//     Not indexed, so the typed resolver emits nothing. The name ladder
//     would emit Report.Add -> Builder.WriteString: a wrong edge into a
//     real symbol, which is worse than no edge because it is queryable.
//   - r.mu.Lock binds to sync.Mutex.Lock, which no indexed declaration
//     shares a name with. The name ladder would keep the rendered
//     "sync.Mutex.Lock" and synthesise an `external` stub node for it.
type Report struct {
	buf strings.Builder
	mu  sync.Mutex
}

// Add is the call site under test.
func (r *Report) Add(line string) {
	r.mu.Lock()
	r.buf.WriteString(line)
	r.mu.Unlock()
	r.total()
}

// total is the positive control: an indexed callee reached from the same
// body, so a fixture that quietly stopped type-checking fails the tests
// here instead of satisfying them by resolving nothing at all.
func (r *Report) total() int { return r.buf.Len() }
