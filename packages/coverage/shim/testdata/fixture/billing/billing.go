// Package billing is production code for the shim fixture. Each function is
// exercised by exactly one test, so a per-test profile that mixes them up is
// visibly wrong.
package billing

// Total normalises an order total.
func Total(cents int) int {
	if cents < 0 {
		return 0
	}
	return cents
}

// Refund is exercised by a different test than Total.
func Refund(cents int) int {
	return -Total(cents)
}

// Unused is exercised by no test at all: it has to appear in every profile
// with a zero count, never as executed.
func Unused() string {
	return "nobody calls me"
}
