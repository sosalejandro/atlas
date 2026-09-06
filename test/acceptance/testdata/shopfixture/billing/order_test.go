package billing

import "testing"

// Exercises Total (and through it normalize's happy branch). Pay and Paid are
// deliberately untouched so the acceptance layer has a symbol it KNOWS must
// report zero covered statements.
func TestOrderTotal(t *testing.T) {
	o := &Order{total: 7}
	if got := o.Total(); got != 7 {
		t.Fatalf("Total() = %d, want 7", got)
	}
}
