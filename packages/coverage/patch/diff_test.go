package patch

import (
	"reflect"
	"strings"
	"testing"
)

// find returns the FileChange for path, or fails the test.
func find(t *testing.T, changes []FileChange, path string) FileChange {
	t.Helper()
	for _, c := range changes {
		if c.Path == path {
			return c
		}
	}
	t.Fatalf("no FileChange for %q in %+v", path, changes)
	return FileChange{}
}

// absent asserts no FileChange exists for path.
func absent(t *testing.T, changes []FileChange, path string) {
	t.Helper()
	for _, c := range changes {
		if c.Path == path {
			t.Fatalf("FileChange for %q should not be reported: %+v", path, c)
		}
	}
}

const mixedDiff = `diff --git a/pkg/a.go b/pkg/a.go
index 1111111..2222222 100644
--- a/pkg/a.go
+++ b/pkg/a.go
@@ -10,0 +11,3 @@ func Existing() {
+	if err != nil {
+		return err
+	}
@@ -30,2 +33,2 @@ func Other() {
-	old one
-	old two
+	new one
+	new two
@@ -50,3 +52,0 @@ func Gone() {
-	deleted one
-	deleted two
-	deleted three
diff --git a/pkg/gone.go b/pkg/gone.go
deleted file mode 100644
index 3333333..0000000
--- a/pkg/gone.go
+++ /dev/null
@@ -1,2 +0,0 @@
-package pkg
-
diff --git a/pkg/new.go b/pkg/new.go
new file mode 100644
index 0000000..4444444
--- /dev/null
+++ b/pkg/new.go
@@ -0,0 +1,2 @@
+package pkg
+
`

func TestParseDiff_AddedAndModifiedRanges(t *testing.T) {
	changes, err := ParseDiff(strings.NewReader(mixedDiff))
	if err != nil {
		t.Fatalf("ParseDiff: %v", err)
	}
	got := find(t, changes, "pkg/a.go").Ranges
	want := []LineRange{{Start: 11, End: 13}, {Start: 33, End: 34}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pkg/a.go ranges = %+v, want %+v", got, want)
	}
	if n := find(t, changes, "pkg/a.go").Lines(); n != 5 {
		t.Errorf("pkg/a.go changed lines = %d, want 5", n)
	}
	if got := find(t, changes, "pkg/new.go").Ranges; !reflect.DeepEqual(got, []LineRange{{Start: 1, End: 2}}) {
		t.Errorf("pkg/new.go ranges = %+v", got)
	}
}

// Deleted lines are not patch coverage: a hunk with a zero-length post-image
// contributes nothing, and a file that only lost lines never appears at all.
// Reporting either would demand coverage for source that no longer exists.
func TestParseDiff_DeletionsContributeNothing(t *testing.T) {
	changes, err := ParseDiff(strings.NewReader(mixedDiff))
	if err != nil {
		t.Fatalf("ParseDiff: %v", err)
	}
	absent(t, changes, "pkg/gone.go")
	for _, r := range find(t, changes, "pkg/a.go").Ranges {
		if r.Start >= 52 {
			t.Errorf("deletion-only hunk produced a range: %+v", r)
		}
	}
}

// An added line whose CONTENT begins with "++ " renders as "+++ ..." in the
// diff stream, byte-identical to a file header. Recognising headers by prefix
// alone rather than by hunk arithmetic silently re-points every following
// hunk at an unlucky -- or deliberately chosen -- path.
func TestParseDiff_AddedLineLookingLikeAHeaderIsContent(t *testing.T) {
	const tricky = `diff --git a/pkg/tricky.go b/pkg/tricky.go
--- a/pkg/tricky.go
+++ b/pkg/tricky.go
@@ -1,0 +2,2 @@
+++ b/evil.go
+@@ -900,0 +900,5 @@
@@ -20,0 +23,1 @@
+	real line
`
	changes, err := ParseDiff(strings.NewReader(tricky))
	if err != nil {
		t.Fatalf("ParseDiff: %v", err)
	}
	absent(t, changes, "evil.go")
	got := find(t, changes, "pkg/tricky.go").Ranges
	want := []LineRange{{Start: 2, End: 3}, {Start: 23, End: 23}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ranges = %+v, want %+v", got, want)
	}
}

// A rename is scored at the path the lines now live at -- that is the path
// the symbol index and the coverage frontier both know them by.
func TestParseDiff_RenameUsesTheNewPath(t *testing.T) {
	const renamed = `diff --git a/old/name.go b/new/name.go
similarity index 90%
rename from old/name.go
rename to new/name.go
--- a/old/name.go
+++ b/new/name.go
@@ -5 +6 @@ func F() {
-	was
+	now
`
	changes, err := ParseDiff(strings.NewReader(renamed))
	if err != nil {
		t.Fatalf("ParseDiff: %v", err)
	}
	absent(t, changes, "old/name.go")
	if got := find(t, changes, "new/name.go").Ranges; !reflect.DeepEqual(got, []LineRange{{Start: 6, End: 6}}) {
		t.Errorf("ranges = %+v", got)
	}
}

// A binary file has no hunks; it must not surface as a zero-range entry that
// every downstream consumer then has to special-case.
func TestParseDiff_BinaryFileIsSkipped(t *testing.T) {
	const bin = `diff --git a/assets/logo.png b/assets/logo.png
index 1111111..2222222 100644
Binary files a/assets/logo.png and b/assets/logo.png differ
`
	changes, err := ParseDiff(strings.NewReader(bin))
	if err != nil {
		t.Fatalf("ParseDiff: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("binary-only diff produced %+v", changes)
	}
}

// A path git had to quote (non-ASCII, a space, a control byte) arrives
// C-quoted. Leaving the quotes on makes the path match no indexed file, so a
// real changed file silently becomes an unknown one.
func TestParseDiff_QuotedPathIsUnquoted(t *testing.T) {
	const quoted = "diff --git \"a/pkg/caf\\303\\251.go\" \"b/pkg/caf\\303\\251.go\"\n" +
		"--- \"a/pkg/caf\\303\\251.go\"\n" +
		"+++ \"b/pkg/caf\\303\\251.go\"\n" +
		"@@ -0,0 +1 @@\n" +
		"+package pkg\n"
	changes, err := ParseDiff(strings.NewReader(quoted))
	if err != nil {
		t.Fatalf("ParseDiff: %v", err)
	}
	find(t, changes, "pkg/café.go")
}

func TestParseDiff_EmptyDiffIsNotAnError(t *testing.T) {
	changes, err := ParseDiff(strings.NewReader(""))
	if err != nil {
		t.Fatalf("ParseDiff: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("empty diff produced %+v", changes)
	}
}
