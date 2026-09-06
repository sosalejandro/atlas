package redact

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// docs/security.md leads with "nothing leaves the machine". That sentence is
// the only reason the rest of the document is worth reading, and a sentence
// is not an enforcement mechanism -- so this test walks the import graph of
// the atlas binary and fails if any first-party package pulls in a standard
// library package that can open a socket.
//
// The check is deliberately bounded, and the document says so in the same
// words: it covers github.com/sosalejandro/atlas packages reachable from
// cmd/atlas, and it does not audit third-party dependencies. Its value is
// that the specific mistake it prevents -- someone adding a telemetry ping,
// an update check or a crash reporter to a CLI verb -- is a first-party
// change, and it cannot land quietly while this test exists.
//
// Reachability matters rather than a whole-tree grep: internal/server does
// import net/http, and it belongs to the legacy `testreg` binary at the
// module root, which cmd/atlas has never imported. A grep would either fail
// on code the atlas binary does not contain or teach the next person to add
// an exception list.

const modulePath = "github.com/sosalejandro/atlas"

// networkPackages are the standard library packages that can open a network
// connection. os/exec is deliberately absent: atlas shells out to git, node
// and python by design, and those are local processes.
var networkPackages = map[string]bool{
	"net":      true,
	"net/http": true,
	"net/rpc":  true,
	"net/smtp": true,
}

func TestAtlasBinary_ImportsNoNetworkPackage(t *testing.T) {
	root := repoRoot(t)
	reached := map[string]bool{}
	var offenders []string

	var walk func(pkgPath string)
	walk = func(pkgPath string) {
		if reached[pkgPath] {
			return
		}
		reached[pkgPath] = true
		dir := filepath.Join(root, strings.TrimPrefix(pkgPath, modulePath+"/"))
		for _, imported := range importsOf(t, dir) {
			if networkPackages[imported] {
				offenders = append(offenders, pkgPath+" imports "+imported)
				continue
			}
			if imported == modulePath || strings.HasPrefix(imported, modulePath+"/") {
				walk(imported)
			}
		}
	}
	walk(modulePath + "/cmd/atlas")

	// A floor, not a measurement. The walk covers the first-party packages
	// `go list -deps ./cmd/atlas` reports, and that number moves with every
	// package split, so no exact figure is asserted here or in
	// docs/security.md. 30 leaves room for consolidation while still failing
	// loudly if the directory mapping ever breaks and the check starts
	// passing because it inspected almost nothing.
	if len(reached) < 30 {
		t.Fatalf("the walk reached only %d packages (%v); it is not exercising the binary",
			len(reached), sortedKeys(reached))
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Errorf("the atlas binary reaches network code:\n  %s\n"+
			"If this is intentional, docs/security.md must stop claiming that "+
			"nothing leaves the machine BEFORE this test is changed.",
			strings.Join(offenders, "\n  "))
	}
}

// TestAtlasBinary_ReachesTheCommandTree is a guard on the guard: if the walk
// above ever stopped resolving package directories it would pass vacuously.
func TestAtlasBinary_ReachesTheCommandTree(t *testing.T) {
	root := repoRoot(t)
	imports := importsOf(t, filepath.Join(root, "cmd", "atlas"))
	want := modulePath + "/internal/cli"
	for _, i := range imports {
		if i == want {
			return
		}
	}
	t.Fatalf("cmd/atlas does not import %s; imports were %v", want, imports)
}

// importsOf returns the import paths of every non-test Go file in dir.
// Sub-directories are separate packages and are reached through their own
// import paths, so the walk never has to guess at directory structure.
func importsOf(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read package dir %s: %v", dir, err)
	}
	seen := map[string]bool{}
	var out []string
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", filepath.Join(dir, name), err)
		}
		for _, spec := range f.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("unquote import %s in %s: %v", spec.Path.Value, name, err)
			}
			if !seen[path] {
				seen[path] = true
				out = append(out, path)
			}
		}
	}
	return out
}

// repoRoot locates the module root from this test file's own path, so the
// test does not depend on the working directory a runner chose.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed; cannot locate the repository root")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file))) // packages/redact -> packages -> root
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("no go.mod at %s: %v", root, err)
	}
	return root
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
