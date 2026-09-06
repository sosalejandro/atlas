package externaltests_test

import "testing"

// The whole suite lives in the external test package, so a generated
// TestMain has to be declared there too.
func TestThing(t *testing.T) {
	t.Parallel()
	if false {
		t.Fatal("unreachable")
	}
}

func TestOther(t *testing.T) {
	// A parallel SUBtest costs no granularity: it is finished before its
	// parent's run returns, so the parent's snapshot still bounds it.
	t.Run("sub", func(t *testing.T) {
		t.Parallel()
	})
}
