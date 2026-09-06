package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A working tree that matches every recorded hash is the only state that
// earns an "ok" from this check.
func TestIndexFreshness_TreeMatchesIndex_OK(t *testing.T) {
	f := newFixture(t)
	f.indexFile(t, "pkg/a.go", "package pkg\n\nfunc A() {}\n")
	f.indexFile(t, "pkg/b.go", "package pkg\n\nfunc B() {}\n")

	res := runCheck(t, indexFreshness{}, f.env(t))

	assertSeverity(t, res, SeverityOK)
	if got := res.Details["indexed_files"]; got != 2 {
		t.Errorf("indexed_files = %v, want 2", got)
	}
}

// The load-bearing case: a file edited since its scan means every number
// downstream is about content atlas has not read.
func TestIndexFreshness_ChangedFile_Fails(t *testing.T) {
	f := newFixture(t)
	f.indexFile(t, "pkg/a.go", "package pkg\n\nfunc A() {}\n")
	// Same path, different bytes, hash row left alone.
	f.writeFile(t, "pkg/a.go", "package pkg\n\nfunc A() { println(1) }\n")

	res := runCheck(t, indexFreshness{}, f.env(t))

	assertSeverity(t, res, SeverityFail)
	if got := res.Details["changed"]; got != 1 {
		t.Errorf("changed = %v, want 1", got)
	}
	if res.Remediation == "" {
		t.Error("a stale index must name the command that fixes it")
	}
}

// A deleted file leaves symbols in the store that nothing on disk backs.
func TestIndexFreshness_DeletedFile_Fails(t *testing.T) {
	f := newFixture(t)
	f.indexFile(t, "pkg/a.go", "package pkg\n\nfunc A() {}\n")
	f.indexFile(t, "pkg/gone.go", "package pkg\n\nfunc Gone() {}\n")
	if err := os.Remove(filepath.Join(f.root, "pkg", "gone.go")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	res := runCheck(t, indexFreshness{}, f.env(t))

	assertSeverity(t, res, SeverityFail)
	if got := res.Details["missing"]; got != 1 {
		t.Errorf("missing = %v, want 1", got)
	}
	if paths, _ := res.Details["missing_files"].([]string); len(paths) != 1 || paths[0] != "pkg/gone.go" {
		t.Errorf("missing_files = %v, want [pkg/gone.go]", res.Details["missing_files"])
	}
}

// A source file the scan never reached is a hole in the picture, but not
// a lie about what atlas did read -- warn, not fail.
func TestIndexFreshness_UnindexedGoFile_Warns(t *testing.T) {
	f := newFixture(t)
	f.indexFile(t, "pkg/a.go", "package pkg\n\nfunc A() {}\n")
	f.writeFile(t, "pkg/new.go", "package pkg\n\nfunc New() {}\n")

	res := runCheck(t, indexFreshness{}, f.env(t))

	assertSeverity(t, res, SeverityWarn)
	if got := res.Details["unindexed"]; got != 1 {
		t.Errorf("unindexed = %v, want 1", got)
	}
}

// The third bucket. A file atlas recorded, that is still on disk, whose
// bytes it could not re-read is an UNKNOWN -- and an unknown folded into
// "all N indexed files match the working tree" is an unknown reported as
// an ok, which is the one substitution this package exists to prevent.
//
// It warns rather than fails: doctor established nothing about the file
// either way, only that it could not look.
func TestIndexFreshness_UnreadableFile_WarnsAndNamesIt(t *testing.T) {
	f := newFixture(t)
	f.indexFile(t, "pkg/a.go", "package pkg\n\nfunc A() {}\n")
	f.indexFile(t, "pkg/opaque.go", "package pkg\n\nfunc Opaque() {}\n")
	// A recorded path that still exists but cannot be read as a file.
	// (A directory rather than a chmod, so the test means the same thing
	// when it runs as root.)
	opaque := filepath.Join(f.root, "pkg", "opaque.go")
	if err := os.Remove(opaque); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Mkdir(opaque, 0o755); err != nil {
		t.Fatalf("mkdir over the indexed path: %v", err)
	}

	res := runCheck(t, indexFreshness{}, f.env(t))

	assertSeverity(t, res, SeverityWarn)
	if got := res.Details["unreadable"]; got != 1 {
		t.Errorf("unreadable = %v, want 1", got)
	}
	if paths, _ := res.Details["unreadable_files"].([]string); len(paths) != 1 ||
		paths[0] != "pkg/opaque.go" {
		t.Errorf("unreadable_files = %v, want [pkg/opaque.go]", res.Details["unreadable_files"])
	}
	if !strings.Contains(res.Finding, "pkg/opaque.go") {
		t.Errorf("the finding must name the file it could not check: %q", res.Finding)
	}
	if strings.Contains(res.Finding, "all match the working tree") {
		t.Errorf("a file that was never read must not be reported as matching: %q", res.Finding)
	}
}

// The unindexed sweep reads .go and nothing else, which is defensible --
// but the sentence the user reads has to say so, both when it finds
// something and when it does not.
func TestIndexFreshness_FindingsStateTheGoOnlyScope(t *testing.T) {
	f := newFixture(t)
	f.indexFile(t, "pkg/a.go", "package pkg\n\nfunc A() {}\n")

	okRes := runCheck(t, indexFreshness{}, f.env(t))
	assertSeverity(t, okRes, SeverityOK)
	if !strings.Contains(okRes.Finding, ".go") {
		t.Errorf("the clean finding says nothing about what was NOT examined: %q", okRes.Finding)
	}

	f.writeFile(t, "pkg/new.go", "package pkg\n\nfunc New() {}\n")
	warnRes := runCheck(t, indexFreshness{}, f.env(t))
	assertSeverity(t, warnRes, SeverityWarn)
	if !strings.Contains(warnRes.Finding, "Go source files") {
		t.Errorf("the warn finding calls Go files %q: %q",
			"source files", warnRes.Finding)
	}
}

// Excluded-by-policy files are not evidence of a stale index. Reporting
// them would train users to ignore the check.
func TestIndexFreshness_GeneratedAndSkippedFilesAreNotUnindexed(t *testing.T) {
	f := newFixture(t)
	f.indexFile(t, "pkg/a.go", "package pkg\n\nfunc A() {}\n")
	f.writeFile(t, "pkg/zz_gen.go",
		"// Code generated by stringer. DO NOT EDIT.\n\npackage pkg\n")
	f.writeFile(t, "internal/generated/api.go", "package generated\n")
	f.writeFile(t, "vendor/x/x.go", "package x\n")
	f.writeFile(t, ".hidden/h.go", "package h\n")

	env := f.env(t)
	env.SkipDirs = []string{"vendor"}
	res := runCheck(t, indexFreshness{}, env)

	assertSeverity(t, res, SeverityOK)
	if got := res.Details["unindexed"]; got != 0 {
		t.Errorf("unindexed = %v, want 0 (samples: %v)", got, res.Details["unindexed_files"])
	}
}

// A configured scan.generated glob is policy too, and doctor is handed
// the same list the scanner used.
func TestIndexFreshness_ConfiguredGeneratedGlobExcluded(t *testing.T) {
	f := newFixture(t)
	f.indexFile(t, "pkg/a.go", "package pkg\n\nfunc A() {}\n")
	f.writeFile(t, "pkg/api.pb.go", "package pkg\n")

	env := f.env(t)
	env.GeneratedGlobs = []string{"**/*.pb.go"}
	res := runCheck(t, indexFreshness{}, env)

	assertSeverity(t, res, SeverityOK)
}

// A store that has never been scanned cannot answer anything, and saying
// so is more useful than reporting a vacuously clean index.
func TestIndexFreshness_NoIndexAtAll_Fails(t *testing.T) {
	f := newFixture(t)
	f.writeFile(t, "pkg/a.go", "package pkg\n\nfunc A() {}\n")

	res := runCheck(t, indexFreshness{}, f.env(t))

	assertSeverity(t, res, SeverityFail)
	if res.Remediation == "" {
		t.Error("an empty index must name the command that fills it")
	}
}

// `atlas scan --hash-files=false` produces symbols with no hashes. There
// is nothing to compare against, so the honest answer is "cannot tell",
// never "ok" -- this is the exact lie the check set is built to avoid.
func TestIndexFreshness_SymbolsButNoHashes_NotApplicable(t *testing.T) {
	f := newFixture(t)
	f.writeFile(t, "pkg/a.go", "package pkg\n\nfunc A() {}\n")
	f.insertSymbol(t, "pkg.A", "pkg/a.go", 3)

	res := runCheck(t, indexFreshness{}, f.env(t))

	assertSeverity(t, res, SeverityNotApplicable)
}

// A .go file with no hash row but with indexed symbols WAS scanned; only
// its hash is missing. Counting it as unindexed would be a false alarm.
func TestIndexFreshness_FileWithSymbolsIsNotUnindexed(t *testing.T) {
	f := newFixture(t)
	f.indexFile(t, "pkg/a.go", "package pkg\n\nfunc A() {}\n")
	f.writeFile(t, "pkg/b.go", "package pkg\n\nfunc B() {}\n")
	f.insertSymbol(t, "pkg.B", "pkg/b.go", 3)

	res := runCheck(t, indexFreshness{}, f.env(t))

	assertSeverity(t, res, SeverityOK)
}

// Without a store there is nothing to compare the tree to.
func TestIndexFreshness_NoStore_NotApplicable(t *testing.T) {
	f := newFixture(t)
	env := f.env(t)
	env.Store = nil

	res := runCheck(t, indexFreshness{}, env)

	assertSeverity(t, res, SeverityNotApplicable)
}
