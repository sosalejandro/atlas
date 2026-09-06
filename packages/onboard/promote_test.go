package onboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, rel, body string) string {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return abs
}

func capWithAnchor(id, file string, line int) Capability {
	return Capability{
		ID: id, Provisional: true,
		Anchor: &Anchor{FilePath: file, Line: line, Annotation: "@atlas:feature " + id},
	}
}

// Promotion is the ONLY path from a proposal into the registry, and it goes
// through the source file rather than the database: what it writes is the
// same annotation a human would have written, and it reaches the features
// table through the same ingest. This is the test that the write lands where
// the scanner will read it.
func TestPromote_InsertsAnnotationAboveTheDeclaration(t *testing.T) {
	root := t.TempDir()
	abs := writeFile(t, root, "internal/orders/place.go",
		"package orders\n\n// Place records an order.\nfunc Place() {}\n")

	res, err := Promote(root, capWithAnchor("orders.place", "internal/orders/place.go", 4), true)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if !res.Applied {
		t.Fatalf("Promote reported nothing applied: %+v", res)
	}
	got, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	want := "package orders\n\n// Place records an order.\n// @atlas:feature orders.place\nfunc Place() {}\n"
	if string(got) != want {
		t.Errorf("file after promote:\n%q\nwant:\n%q", got, want)
	}
}

// A dry run is the default for anything that edits source. It must produce
// the exact line it would write and leave the tree untouched.
func TestPromote_DryRunTouchesNothing(t *testing.T) {
	root := t.TempDir()
	body := "package orders\n\nfunc Place() {}\n"
	abs := writeFile(t, root, "internal/orders/place.go", body)

	res, err := Promote(root, capWithAnchor("orders.place", "internal/orders/place.go", 3), false)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if res.Applied {
		t.Error("a dry run reported itself applied")
	}
	if !strings.Contains(res.Text, "@atlas:feature orders.place") {
		t.Errorf("dry run did not show the line it would write: %q", res.Text)
	}
	after, _ := os.ReadFile(abs)
	if string(after) != body {
		t.Errorf("dry run modified the file:\n%q", after)
	}
}

// Existing annotations are adopted, never overwritten or duplicated. A
// second promote of the same anchor must be a no-op with a reason, not a
// second annotation.
func TestPromote_SkipsAlreadyAnnotated(t *testing.T) {
	root := t.TempDir()
	body := "package orders\n\n// @atlas:feature orders.other\nfunc Place() {}\n"
	abs := writeFile(t, root, "internal/orders/place.go", body)

	res, err := Promote(root, capWithAnchor("orders.place", "internal/orders/place.go", 4), true)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if res.Applied || res.Skipped == "" {
		t.Errorf("expected a skip with a reason, got %+v", res)
	}
	after, _ := os.ReadFile(abs)
	if string(after) != body {
		t.Errorf("an already-annotated declaration was rewritten:\n%q", after)
	}
}

// The comment syntax has to match the language or the annotation is a
// syntax error in the user's repository.
func TestPromote_UsesTheLanguageCommentSyntax(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "svc/orders.py", "def place():\n    pass\n")

	res, err := Promote(root, capWithAnchor("orders.place", "svc/orders.py", 1), true)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(res.Text), "#") {
		t.Errorf("python annotation is not a python comment: %q", res.Text)
	}
}

// Indented declarations (a method on a class, a nested function) must keep
// their indentation or the file no longer parses.
func TestPromote_PreservesIndentation(t *testing.T) {
	root := t.TempDir()
	abs := writeFile(t, root, "svc/orders.py", "class Orders:\n    def place(self):\n        pass\n")

	if _, err := Promote(root, capWithAnchor("orders.place", "svc/orders.py", 2), true); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	got, _ := os.ReadFile(abs)
	want := "class Orders:\n    # @atlas:feature orders.place\n    def place(self):\n        pass\n"
	if string(got) != want {
		t.Errorf("file after promote:\n%q\nwant:\n%q", got, want)
	}
}

// A capability with no anchor cannot be promoted, and an anchor pointing
// past the end of its file means the index is stale. Both must fail loudly
// rather than write to the wrong line.
func TestPromote_RefusesUnanchoredAndStale(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.go", "package a\n")

	if _, err := Promote(root, Capability{ID: "a.b", Provisional: true}, true); err == nil {
		t.Error("promoted a capability with no anchor")
	}
	if _, err := Promote(root, capWithAnchor("a.b", "a.go", 99), true); err == nil {
		t.Error("promoted against a line that does not exist")
	}
}

// Escaping the repository root through an anchor path is a path-traversal
// write. The index never produces one, which is exactly why the check has to
// be here rather than in the caller.
func TestPromote_RefusesPathsOutsideTheRoot(t *testing.T) {
	root := t.TempDir()
	if _, err := Promote(root, capWithAnchor("a.b", "../escape.go", 1), true); err == nil {
		t.Error("promote wrote outside the repository root")
	}
}
