package store

import "testing"

// TestBCPathFor pins the conventions docs/architecture.md §3.7 and
// schema-v1.md §5.4 rely on: anything matching src/contexts/<bc>/...
// maps to "src/contexts/<bc>"; nothing else maps to anything.
func TestBCPathFor(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"happy path single-segment bc", "src/contexts/alpha/foo.go", "src/contexts/alpha"},
		{"happy path nested file under bc", "src/contexts/beta/sub/dir/x.go", "src/contexts/beta"},
		{"another bc with deep nesting", "src/contexts/messaging/application/services/conversation.go", "src/contexts/messaging"},
		{"non-contexts path returns empty", "src/shared/logger.go", ""},
		{"contexts but no bc segment yet returns empty", "src/contexts/", ""},
		{"contexts with bc but no trailing file returns empty (no slash after bc)", "src/contexts/alpha", ""},
		{"non-src prefix returns empty", "internal/foo.go", ""},
		{"empty input returns empty", "", ""},
		{"close-but-not-quite prefix returns empty", "src/context/alpha/foo.go", ""},
		{"leading slash is not normalized — strict prefix match", "/src/contexts/alpha/foo.go", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bcPathFor(tc.in)
			if got != tc.want {
				t.Errorf("bcPathFor(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
