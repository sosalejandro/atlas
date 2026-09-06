// Package sink holds one interface with two implementations, only one of
// which this scan indexes.
package sink

// Writer is the dispatch point.
type Writer interface {
	Write(s string) int
}

// Direct is the hand-written implementation. It is indexed.
type Direct struct{ n int }

// Write records s.
func (d *Direct) Write(s string) int {
	d.n += len(s)
	return d.n
}

// Pipe holds a Writer and is the caller under test.
type Pipe struct{ w Writer }

// Send dispatches through the interface. Class-hierarchy analysis names
// BOTH implementations here; the scan can only emit an edge to the one it
// indexed, and the edge it does emit must not claim to be the only
// possibility.
func (p *Pipe) Send(s string) int { return p.w.Write(s) }

// New keeps both implementations reachable from a constructor, so neither
// is dead code the SSA builder could drop.
func New(buffered bool) *Pipe {
	if buffered {
		return &Pipe{w: &Buffered{}}
	}
	return &Pipe{w: &Direct{}}
}
