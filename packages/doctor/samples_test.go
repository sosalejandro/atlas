package doctor

import (
	"encoding/json"
	"testing"
)

// An empty sample list must marshal as [] rather than null: a CI
// consumer reading details.missing_files should be able to iterate it
// without a null check.
func TestSamples_EmptyMarshalsAsArray(t *testing.T) {
	b, err := json.Marshal(map[string]any{"files": samples(nil)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(b); got != `{"files":[]}` {
		t.Errorf("marshalled %s, want {\"files\":[]}", got)
	}
}

// The cap keeps a report readable; the counts beside it stay exact, so
// truncation costs nothing actionable.
func TestSamples_SortsAndCaps(t *testing.T) {
	in := make([]string, 0, maxSamples+5)
	for i := maxSamples + 5; i > 0; i-- {
		in = append(in, string(rune('a'+i%26))+"/f.go")
	}
	out := samples(in)
	if len(out) != maxSamples {
		t.Fatalf("len = %d, want %d", len(out), maxSamples)
	}
	for i := 1; i < len(out); i++ {
		if out[i-1] > out[i] {
			t.Fatalf("not sorted at %d: %q > %q", i, out[i-1], out[i])
		}
	}
}
