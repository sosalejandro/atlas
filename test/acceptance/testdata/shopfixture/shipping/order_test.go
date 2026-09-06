package shipping

import "testing"

// Only the light branch of rate is exercised, so rate reports a PARTIAL
// statement fraction rather than a binary pass — which is the whole point of
// the statement-coverage track.
func TestOrderTotalLight(t *testing.T) {
	if got := New(4).Total(); got != 4 {
		t.Fatalf("Total() = %d, want 4", got)
	}
}

// Surcharge is exercised so its statements are present in the profile AND
// partially executed. A gap that is entirely unexecuted would be
// indistinguishable from code the compiler never instrumented.
func TestSurchargeLight(t *testing.T) {
	if got := Surcharge(4); got != 0 {
		t.Fatalf("Surcharge(4) = %d, want 0", got)
	}
}
