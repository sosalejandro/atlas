package diagnose

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewSymptomMatcher_EscapesMetacharacters(t *testing.T) {
	t.Parallel()

	// The symptom is full of regex metacharacters. If we forget to
	// QuoteMeta, the whole regex either fails to compile or matches
	// every string in the universe.
	sm := newSymptomMatcher("panic: runtime error (nil pointer)*+ [at] {0}")

	body := "the panic: runtime error (nil pointer)*+ [at] {0} log line goes here"
	if !sm.whole.MatchString(body) {
		t.Fatal("whole-symptom regex did not match literal body containing the symptom")
	}

	body2 := "panic: runtime error nil pointer at line 0"
	if sm.whole.MatchString(body2) {
		t.Fatal("whole-symptom regex matched a non-literal body (metachars not escaped)")
	}
}

func TestNewSymptomMatcher_EmptyStringSafe(t *testing.T) {
	t.Parallel()

	sm := newSymptomMatcher("")
	if sm.whole != nil || sm.tokens != nil {
		t.Fatalf("empty symptom should produce nil regexes; got whole=%v tokens=%v",
			sm.whole, sm.tokens)
	}
}

func TestNewSymptomMatcher_WhitespaceOnlySafe(t *testing.T) {
	t.Parallel()

	sm := newSymptomMatcher("   \n\t ")
	if sm.whole != nil || sm.tokens != nil {
		t.Fatalf("whitespace-only symptom should produce nil regexes")
	}
}

func TestNewSymptomMatcher_TokensDropStopWords(t *testing.T) {
	t.Parallel()

	sm := newSymptomMatcher("server error: failed to connect with foo.bar.baz")
	if sm.tokens == nil {
		t.Fatal("expected token regex to be non-nil")
	}
	// "server", "error", "failed", "with" are stop tokens. "connect",
	// "foo.bar.baz" should survive.
	src := sm.tokens.String()
	for _, drop := range []string{"server", "error", "failed", "with"} {
		// Each stop token should NOT appear as an alternative in the
		// regex source — they're filtered out.
		if containsStandalone(src, drop) {
			t.Errorf("stop token %q leaked into tokens regex: %s", drop, src)
		}
	}
}

// containsStandalone is a coarse substring search used by the stop-token
// assertion. It's OK that "errors" would also trigger; the test only ever
// passes literal stop-token strings.
func containsStandalone(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func TestSliceLines_Basics(t *testing.T) {
	t.Parallel()

	content := []byte("line1\nline2\nline3\nline4\n")

	cases := []struct {
		name             string
		start, end       int
		wantContains     string
		wantNotContains  string
	}{
		{"first line", 1, 1, "line1", "line2"},
		{"middle", 2, 3, "line2", "line4"},
		{"overshoot end clamps", 3, 99, "line3", "line1"},
		{"start past EOF empties", 99, 100, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sliceLines(content, tc.start, tc.end)
			if tc.wantContains != "" && !contains(got, tc.wantContains) {
				t.Errorf("expected %q in result; got %q", tc.wantContains, got)
			}
			if tc.wantNotContains != "" && contains(got, tc.wantNotContains) {
				t.Errorf("expected %q NOT in result; got %q", tc.wantNotContains, got)
			}
		})
	}
}

func contains(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func TestBodyCache_ReadsAndCaches(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	abs := filepath.Join(dir, "sample.go")
	const src = "package x\n\nfunc Foo() {\n\tfmt.Println(\"panic: nil pointer deref\")\n}\n"
	if err := os.WriteFile(abs, []byte(src), 0o600); err != nil {
		t.Fatalf("write sample: %v", err)
	}

	c := newBodyCache("")
	got1, err := c.readBody(abs, 3, 5)
	if err != nil {
		t.Fatalf("readBody #1: %v", err)
	}
	if !contains(got1, "panic: nil pointer deref") {
		t.Fatalf("readBody result missing expected text: %q", got1)
	}

	// Delete the file. The cache should still serve from memory.
	if err := os.Remove(abs); err != nil {
		t.Fatalf("remove sample: %v", err)
	}
	got2, err := c.readBody(abs, 3, 5)
	if err != nil {
		t.Fatalf("readBody #2 (cached): %v", err)
	}
	if got2 != got1 {
		t.Fatal("cached read returned different bytes than first read")
	}
}

func TestBodyCache_MissingFileReturnsEmptyNotError(t *testing.T) {
	t.Parallel()

	c := newBodyCache("")
	got, err := c.readBody("/nonexistent/path/somefile.go", 1, 10)
	if err != nil {
		t.Fatalf("missing file should not error; got: %v", err)
	}
	if got != "" {
		t.Fatalf("missing file should produce empty body; got %q", got)
	}
}

func TestBodyCache_RelativePathJoinsRoot(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	rel := "src/file.go"
	abs := filepath.Join(dir, rel)
	if err := os.WriteFile(abs, []byte("a\nb\nc\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	c := newBodyCache(dir)
	got, err := c.readBody(rel, 1, 3)
	if err != nil {
		t.Fatalf("readBody: %v", err)
	}
	if !contains(got, "b") {
		t.Fatalf("expected joined-root read to succeed; got %q", got)
	}
}
