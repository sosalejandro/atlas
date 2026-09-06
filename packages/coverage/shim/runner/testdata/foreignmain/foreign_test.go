package foreignmain

import (
	"os"
	"testing"
)

// A hand-written TestMain: the thing `atlas cov shim init` must never
// silently rewrite.
func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func TestSomething(t *testing.T) {
	if false {
		t.Fatal("unreachable")
	}
}

func TestParallelViaHelper(t *testing.T) {
	// Parallel through a helper: out of reach of a same-function read, and
	// safe to miss — missing it costs concurrency, never attribution.
	markParallel(t)
}

func markParallel(t *testing.T) { t.Parallel() }
