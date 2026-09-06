// Package sound compiles cleanly and does not import the broken one, so
// a per-package degradation still resolves its calls exactly.
package sound

// Ledger appends entries.
type Ledger struct {
	entries []string
}

// Append records one entry through the unexported helper.
func (l *Ledger) Append(entry string) {
	l.push(entry)
}

func (l *Ledger) push(entry string) {
	l.entries = append(l.entries, entry)
}
