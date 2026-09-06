package billing

import "testing"

func TestTotal(t *testing.T) {
	if Total(-1) != 0 {
		t.Fatal("negative total should clamp to zero")
	}
}

func TestRefund(t *testing.T) {
	if Refund(5) != -5 {
		t.Fatal("refund should negate")
	}
	t.Run("subtest", func(t *testing.T) {
		// Parallel SUBtests finish inside their parent's run, so they cost
		// no granularity — the fixture pins that.
		t.Parallel()
		if Refund(0) != 0 {
			t.Fatal("zero refund")
		}
	})
}
