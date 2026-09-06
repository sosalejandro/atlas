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

// The bug this pins corrupted the user's source. Promote addresses its
// target by line number against the file as it stands, so applying one
// annotation shifts every declaration below it; a caller looping over
// Promote in map order wrote its second annotation into that file one line
// off its declaration, its third two lines off, and said nothing. PromoteAll
// orders the writes per file bottom-up, so no anchor a later write needs has
// moved.
func TestPromoteAll_SeveralAnnotationsInOneFileEachLandOnTheirOwnDeclaration(t *testing.T) {
	root := t.TempDir()
	rel := "internal/orders/orders.go"
	abs := writeFile(t, root, rel,
		"package orders\n\nfunc Alpha() {}\n\nfunc Beta() {}\n\nfunc Gamma() {}\n")

	// Ascending line order is the order the provisional map hands them over
	// in, and the order that used to break.
	caps := []Capability{
		capWithAnchor("orders.alpha", rel, 3),
		capWithAnchor("orders.beta", rel, 5),
		capWithAnchor("orders.gamma", rel, 7),
	}
	res, err := PromoteAll(root, caps, true)
	if err != nil {
		t.Fatalf("PromoteAll: %v", err)
	}
	if len(res) != len(caps) {
		t.Fatalf("got %d results, want %d", len(res), len(caps))
	}
	for i, r := range res {
		// Results come back in the caller's order, not in the order the
		// writes were applied.
		if r.ID != caps[i].ID {
			t.Errorf("result %d is %s, want %s -- results must keep the caller's order",
				i, r.ID, caps[i].ID)
		}
		if !r.Applied {
			t.Errorf("%s reported nothing applied: %+v", r.ID, r)
		}
		if r.Line != caps[i].Anchor.Line {
			t.Errorf("%s reported line %d, want %d (the line as the user's file numbers it)",
				r.ID, r.Line, caps[i].Anchor.Line)
		}
	}

	got, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	want := "package orders\n\n" +
		"// @atlas:feature orders.alpha\nfunc Alpha() {}\n\n" +
		"// @atlas:feature orders.beta\nfunc Beta() {}\n\n" +
		"// @atlas:feature orders.gamma\nfunc Gamma() {}\n"
	if string(got) != want {
		t.Errorf("file after promoting three capabilities:\n%q\nwant:\n%q", got, want)
	}
}

// The ordering must not cost anything else: promotions spanning several
// files all land, and a dry run still touches nothing.
func TestPromoteAll_SpansFilesAndHonoursDryRun(t *testing.T) {
	root := t.TempDir()
	bodyA := "package a\n\nfunc One() {}\n\nfunc Two() {}\n"
	bodyB := "package b\n\nfunc Three() {}\n"
	absA := writeFile(t, root, "a/a.go", bodyA)
	absB := writeFile(t, root, "b/b.go", bodyB)
	caps := []Capability{
		capWithAnchor("a.one", "a/a.go", 3),
		capWithAnchor("b.three", "b/b.go", 3),
		capWithAnchor("a.two", "a/a.go", 5),
	}

	dry, err := PromoteAll(root, caps, false)
	if err != nil {
		t.Fatalf("PromoteAll dry run: %v", err)
	}
	for _, r := range dry {
		if r.Applied {
			t.Errorf("%s reported itself applied on a dry run", r.ID)
		}
	}
	if a, _ := os.ReadFile(absA); string(a) != bodyA {
		t.Errorf("dry run modified a/a.go:\n%q", a)
	}

	if _, err := PromoteAll(root, caps, true); err != nil {
		t.Fatalf("PromoteAll apply: %v", err)
	}
	gotA, _ := os.ReadFile(absA)
	wantA := "package a\n\n// @atlas:feature a.one\nfunc One() {}\n\n// @atlas:feature a.two\nfunc Two() {}\n"
	if string(gotA) != wantA {
		t.Errorf("a/a.go after promote:\n%q\nwant:\n%q", gotA, wantA)
	}
	gotB, _ := os.ReadFile(absB)
	wantB := "package b\n\n// @atlas:feature b.three\nfunc Three() {}\n"
	if string(gotB) != wantB {
		t.Errorf("b/b.go after promote:\n%q\nwant:\n%q", gotB, wantB)
	}
}
