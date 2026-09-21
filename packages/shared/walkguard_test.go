package shared_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Grunnr walks a user's directory tree from eleven places, and a nested git
// repository is a different codebase in all eleven.
//
// #166 taught four of them -- the annotation walk, the Go scanner, and the
// Python and TypeScript sub-scanners -- and shipped with a doc comment in
// nestedrepo.go predicting exactly what happened next:
//
//	"a boundary honoured by three walkers and missed by the fourth leaks
//	 exactly the symbols the fourth produces"
//
// It was right, and it still was not enough. #180 found seven more walks in
// packages/sqlops, packages/contract and packages/doctor, discovered only
// because the dogfood gate went red with a worktree in the tree and every SQL
// number was EXACTLY DOUBLE: 290 operations against 145, 52 tables against 27.
//
// Patching call sites is what #166 did. Nothing stopped the eighth walk from
// being written without the boundary, and nothing would stop the twelfth. So
// this is a list somebody has to edit: a new walk fails the build until it is
// classified, which makes adding one a DECISION rather than an omission. The
// same shape as verbsWithNoArtifact in internal/cli/security_test.go and the
// egress guard in packages/redact.
//
// The check is FILE-level, not function-level, and deliberately so: proving a
// particular closure honours a predicate needs dataflow analysis, while "this
// file walks a tree and never mentions the boundary" is decidable, cheap, and
// catches the mistake that actually happens -- somebody writing a new walk and
// not thinking about nested repositories at all.

// exemptWalks are files that walk a directory tree and legitimately do not
// honour the repository boundary. Every entry states why, because an
// exemption without a reason is indistinguishable from an oversight.
var exemptWalks = map[string]string{
	// Walks grunnr's OWN output directory, not the user's source. A coverage
	// artifact tree has no repositories nested in it, and skipping one would
	// mean ignoring a profile the user asked to be read.
	"packages/coverage/shim/layout.go":         "walks grunnr's own coverage output, not a source tree",
	"packages/coverage/shim/runner/convert.go": "walks a generated profile directory",

	// Walks the migrations embedded in grunnr itself, which ship inside the
	// binary and cannot contain a nested checkout.
	"internal/cli/migrate.go": "walks grunnr's own embedded migration set",

	// go/packages resolves the module graph itself and never descends into a
	// directory the Go toolchain does not consider part of the module, so a
	// nested repository with its own go.mod is already excluded upstream.
	"packages/resolver/load.go":    "go/packages bounds the walk by module, upstream of us",
	"packages/resolver/pathkey.go": "keys paths already produced by go/packages",

	// A developer tool run by hand against grunnr's own corpora; not part of
	// any user-facing scan.
	"packages/codeindex/patterns/internal/calibrate/calibrate.go": "developer calibration tool, not a scan path",

	// Presence probes: they decide whether to START a sub-scanner by looking
	// for a single file of that language. The scanners they gate honour the
	// boundary, so a nested repository cannot contribute a symbol; the worst
	// case is starting a scanner for a project with none of its own, which
	// costs a subprocess and produces no wrong answers.
	"packages/codeindex/codeindex.go": "presence probes for the TS and Python scanners, both of which honour the boundary",

	// Counts TS files to decide whether to START the TS scanner. The scanner
	// it gates honours the boundary, so a nested repo cannot contribute
	// symbols; at worst this turns the scanner on for a project that has none
	// of its own, which costs a subprocess and no wrong answers.
	"packages/codeindex/ts/probe.go": "presence probe only; the scan it gates honours the boundary",

	// Copies a directory for the Windows TypeScript bridge. It copies what it
	// is given, and what it is given has already been bounded.
	"packages/codeindex/ts/scanner.go": "copies an already-bounded directory for the Windows bridge",
}

// firstPartyRoots are the trees this guard covers: the packages the grunnr
// binary is built from. internal/adapters and cmd/ are testreg-era code that
// cmd/grunnr has never imported -- the same reasoning packages/redact's egress
// guard uses to decide what its own walk covers.
var firstPartyRoots = []string{"packages", "internal/cli"}

func TestEveryTreeWalkHonoursTheRepositoryBoundary(t *testing.T) {
	root := repoRootForWalkGuard(t)
	var (
		offenders []string
		checked   int
	)

	for _, sub := range firstPartyRoots {
		err := filepath.WalkDir(filepath.Join(root, sub), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil //nolint:nilerr // an unreadable subtree is not evidence
			}
			name := d.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			src, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Fatalf("read %s: %v", path, rerr)
			}
			body := string(src)
			if !strings.Contains(body, "filepath.WalkDir") && !strings.Contains(body, "filepath.Walk(") {
				return nil
			}
			checked++
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if _, exempt := exemptWalks[rel]; exempt {
				return nil
			}
			// "NestedRepoRoot" and not "IsNestedRepoRoot": packages/codeindex
			// reaches the predicate through a package-local wrapper
			// (isNestedRepoRoot, lower case) so that all four of its walks
			// share one name, and a capital-letter check called that an
			// offender. The suffix matches both spellings and still cannot
			// match anything that is not the predicate.
			if strings.Contains(body, "NestedRepoRoot") {
				return nil
			}
			// Parse, so a match inside a comment or a string does not pass.
			if _, perr := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution); perr != nil {
				t.Fatalf("parse %s: %v", path, perr)
			}
			offenders = append(offenders, rel)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", sub, err)
		}
	}

	// A floor, not a measurement. If the roots above ever stopped resolving,
	// this test would pass having inspected nothing -- which is the shape of
	// vacuous guard this repository has now shipped four times.
	if checked < 10 {
		t.Fatalf("the guard inspected only %d files containing a walk; it is not exercising the tree", checked)
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Errorf("these files walk a directory tree without honouring the repository boundary:\n  %s\n\n"+
			"A nested git repository is a different codebase. Indexing one inflates every\n"+
			"number grunnr reports about this one -- #180 found every SQL figure exactly\n"+
			"doubled because three walks in packages/sqlops missed it.\n\n"+
			"Either call shared.IsNestedRepoRoot in the walk's directory branch, or add\n"+
			"the file to exemptWalks WITH A REASON.",
			strings.Join(offenders, "\n  "))
	}
}

// The guard on the guard: an exemption for a file that no longer walks
// anything is stale, and a stale exemption is how a real offender gets
// waved through later under a name somebody recognises.
func TestExemptWalks_AreAllStillWalks(t *testing.T) {
	root := repoRootForWalkGuard(t)
	for rel, reason := range exemptWalks {
		if reason == "" {
			t.Errorf("%s is exempt with no reason; an exemption without one is an oversight", rel)
		}
		src, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Errorf("exempt file %s does not exist; remove the exemption", rel)
			continue
		}
		body := string(src)
		if !strings.Contains(body, "filepath.WalkDir") && !strings.Contains(body, "filepath.Walk(") {
			t.Errorf("%s is exempt from the walk guard but no longer walks anything; remove it", rel)
		}
	}
}

func repoRootForWalkGuard(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not locate the module root from the test's working directory")
	return ""
}
