package shipping

import "testing"

func TestRate(t *testing.T) {
	t.Parallel()
	if Rate(2) != 6 {
		t.Fatal("rate")
	}
}

func TestZone(t *testing.T) {
	t.Parallel()
	if Zone("") != "unknown" {
		t.Fatal("zone")
	}
}
