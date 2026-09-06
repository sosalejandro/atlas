package affected

import (
	"reflect"
	"testing"
)

// The hunk header is the only part of `diff --unified=0` output the selector
// reads, and its shapes are easy to get subtly wrong: the count is optional
// (meaning 1), and a pure deletion carries a ZERO count on the new side, where
// the naive reading yields the empty range [c, c-1] and silently attributes
// nothing.
func TestParseUnifiedDiff_HunkShapes(t *testing.T) {
	const out = `diff --git a/billing/checkout.go b/billing/checkout.go
index 1111111..2222222 100644
--- a/billing/checkout.go
+++ b/billing/checkout.go
@@ -12 +12 @@ func Checkout(ctx context.Context) error {
-	return nil
+	return errors.New("boom")
@@ -40,0 +41,3 @@ func Checkout(ctx context.Context) error {
+	// three added lines
+	//
+	//
diff --git a/billing/refund.go b/billing/refund.go
--- a/billing/refund.go
+++ b/billing/refund.go
@@ -30,4 +29,0 @@ func Refund() {
-	a()
-	b()
-	c()
-	d()
`

	got, err := parseUnifiedDiff(out)
	if err != nil {
		t.Fatalf("parseUnifiedDiff: %v", err)
	}
	want := map[string][]LineRange{
		"billing/checkout.go": {{Start: 12, End: 12}, {Start: 41, End: 43}},
		// A pure deletion has no post-image lines of its own. It is anchored
		// on the surviving line either side of the cut so the symbol the code
		// was deleted FROM is still attributed.
		"billing/refund.go": {{Start: 29, End: 30}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranges =\n%+v\nwant\n%+v", got, want)
	}
}

// A file deleted outright leaves `@@ -1,20 +0,0 @@`: the anchor clamps to line
// 1 rather than producing a zero or negative line number the span lookup would
// silently never match.
func TestParseUnifiedDiff_WholeFileDeletionClampsToLineOne(t *testing.T) {
	const out = `diff --git a/billing/gone.go b/billing/gone.go
--- a/billing/gone.go
+++ /dev/null
@@ -1,20 +0,0 @@
`
	got, err := parseUnifiedDiff(out)
	if err != nil {
		t.Fatalf("parseUnifiedDiff: %v", err)
	}
	want := map[string][]LineRange{"billing/gone.go": {{Start: 1, End: 1}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranges = %+v, want %+v", got, want)
	}
}

// Renames arrive with a b/ path that differs from a/; the post-image path is
// what the symbol index is keyed on.
func TestParseUnifiedDiff_UsesThePostImagePath(t *testing.T) {
	const out = `diff --git a/old/name.go b/new/name.go
similarity index 90%
rename from old/name.go
rename to new/name.go
--- a/old/name.go
+++ b/new/name.go
@@ -5,2 +5,2 @@ func F() {
-	x()
-	y()
+	z()
+	w()
`
	got, err := parseUnifiedDiff(out)
	if err != nil {
		t.Fatalf("parseUnifiedDiff: %v", err)
	}
	if _, ok := got["new/name.go"]; !ok {
		t.Fatalf("ranges = %+v, want the post-image path new/name.go", got)
	}
	if _, ok := got["old/name.go"]; ok {
		t.Errorf("ranges = %+v, must not key on the pre-image path", got)
	}
}

func TestParseUnifiedDiff_RejectsAHunkWithNoFile(t *testing.T) {
	if _, err := parseUnifiedDiff("@@ -1 +1 @@\n"); err == nil {
		t.Fatal("a hunk with no preceding +++ header must error, not be attributed to nothing")
	}
}
