// Package shipping is the fixture's parallel package: its tests call
// t.Parallel(), which is what per-package degradation is for.
package shipping

// Rate is exercised by the parallel test.
func Rate(kg int) int {
	if kg <= 0 {
		return 0
	}
	return kg * 3
}

// Zone is exercised by the other parallel test.
func Zone(code string) string {
	if code == "" {
		return "unknown"
	}
	return code
}
