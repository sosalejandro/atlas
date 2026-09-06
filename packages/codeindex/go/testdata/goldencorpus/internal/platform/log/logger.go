// Package log is a deliberately thin logging shim.
package log

import (
	"fmt"
	"os"
)

// StdLogger writes plain lines to stderr.
type StdLogger struct {
	Prefix string
}

// New returns a StdLogger tagged with prefix.
func New(prefix string) *StdLogger {
	return &StdLogger{Prefix: prefix}
}

// Info writes one informational line.
func (l *StdLogger) Info(msg string) {
	l.write("info", msg)
}

// Warn writes one warning line.
func (l *StdLogger) Warn(msg string) {
	l.write("warn", msg)
}

// write is an unexported METHOD — unlike unexported plain functions these
// are kept, because sibling methods on the same receiver call them.
func (l *StdLogger) write(level, msg string) {
	fmt.Fprintf(os.Stderr, "%s %s %s\n", l.Prefix, level, msg)
}
