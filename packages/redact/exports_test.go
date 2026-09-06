package redact

import "testing"

func TestExports_EveryEntryIsUsable(t *testing.T) {
	if len(Exports()) == 0 {
		t.Fatal("the export catalogue is empty")
	}
	valid := map[Class]bool{
		ClassPath: true, ClassIdentifier: true, ClassSourceText: true,
		ClassUserText: true, ClassEnum: true, ClassDigest: true,
	}
	for _, e := range Exports() {
		if e.Surface == "" {
			t.Errorf("export %+v has no surface name", e)
		}
		if e.Destination == "" {
			t.Errorf("export %q has no destination", e.Surface)
		}
		if e.Note == "" {
			t.Errorf("export %q has no note; the report prints it", e.Surface)
		}
		for _, c := range e.Classes {
			if !valid[c] {
				t.Errorf("export %q claims unknown class %q", e.Surface, c)
			}
		}
	}
}

func TestExportsFor_MatchesWholeVerbSegments(t *testing.T) {
	if got := ExportsFor(""); len(got) != len(Exports()) {
		t.Errorf("ExportsFor(\"\") returned %d entries, want all %d", len(got), len(Exports()))
	}
	got := ExportsFor("report")
	if len(got) == 0 {
		t.Fatalf("ExportsFor(%q) found nothing", "report")
	}
	for _, e := range got {
		if e.Verb[:len("report ")] != "report " {
			t.Errorf("ExportsFor(\"report\") returned %q", e.Verb)
		}
	}
	if got := ExportsFor("cov r"); len(got) != 0 {
		t.Errorf("ExportsFor(%q) matched mid-segment: %+v", "cov r", got)
	}
	if got := ExportsFor("nosuchverb"); len(got) != 0 {
		t.Errorf("ExportsFor on an unknown verb returned %+v", got)
	}
}

// TestExports_DoNotClaimNetworkDestinations pins the central claim of
// docs/security.md into a test. If a future export ever writes to a URL,
// this fails and the statement has to be rewritten before it ships.
func TestExports_DoNotClaimNetworkDestinations(t *testing.T) {
	for _, e := range Exports() {
		for _, marker := range []string{"http://", "https://", "upload", "api.", "://"} {
			if containsFold(e.Destination, marker) {
				t.Errorf("export %q has destination %q, which looks like egress",
					e.Surface, e.Destination)
			}
		}
	}
}

func containsFold(hay, needle string) bool {
	if len(needle) > len(hay) {
		return false
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		if equalFold(hay[i:i+len(needle)], needle) {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
