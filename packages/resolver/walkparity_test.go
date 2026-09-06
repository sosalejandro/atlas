package resolver

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTypeChecked_AgreesWithWhatTheScannerWalks is the invariant issue
// #143 actually cares about, stated end to end.
//
// The scanner reaches this package by walking the tree with
// filepath.WalkDir and asking TypeChecked(abs) for each file it finds.
// The index it asks was built from paths `go list` reported. If those two
// spellings disagree, NOTHING fails: the file is simply not type-checked,
// its calls fall back to name matching, and the only trace is that every
// edge from it leaves the `typed` tier — which moves the golden snapshot
// on that platform and nowhere else.
//
// So the assertion is a walk, not a spot check: every non-test .go file
// the corpus contains, addressed exactly as a walk addresses it, must be
// answerable. That is a property the go tool's own view can be checked
// against, and it is the one that silently stopped holding on Windows.
func TestTypeChecked_AgreesWithWhatTheScannerWalks(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs(goldenCorpus)
	if err != nil {
		t.Fatal(err)
	}
	p := load(t, root, Options{IncludeTests: true})
	if p.Status().TypeChecked == 0 {
		t.Fatalf("the corpus type-checked no packages (%+v); this test would then "+
			"prove nothing", p.Status())
	}

	var walked, missed int
	err = filepath.WalkDir(root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// testdata is not part of any package the go tool loads.
			if d.Name() == "testdata" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		walked++
		if !p.TypeChecked(abs) {
			missed++
			t.Errorf("TypeChecked(%q) = false; the walk found this file and the "+
				"load did not answer for it", abs)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if walked == 0 {
		t.Fatal("walked no .go files; the corpus path is wrong")
	}
	if missed > 0 {
		t.Logf("%d of %d walked files were not answerable — every call site in them "+
			"degrades out of the typed tier", missed, walked)
	}
}

// TestTypeChecked_RejectsAFileThatIsNotThere guards the test above from
// the failure mode that would make it vacuous: a lookup that answered
// true for everything would satisfy the walk and be worse than useless.
func TestTypeChecked_RejectsAFileThatIsNotThere(t *testing.T) {
	t.Parallel()
	p := load(t, goldenCorpus, Options{})

	// A real directory with no such file in it, so the EvalSymlinks
	// second chance in lookup is exercised and still has to answer no.
	absent := filepath.Join(t.TempDir(), "definitely_not_indexed.go")
	if _, err := os.Stat(absent); err == nil {
		t.Fatalf("%s unexpectedly exists", absent)
	}
	if p.TypeChecked(absent) {
		t.Errorf("TypeChecked(%q) = true for a file that does not exist", absent)
	}

	// And a file that DOES exist but is outside the loaded tree.
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("package p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p.TypeChecked(outside) {
		t.Errorf("TypeChecked(%q) = true for a file outside the loaded tree", outside)
	}
}
