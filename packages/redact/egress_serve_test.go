package redact

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The `atlas` binary cannot open a socket, and TestAtlasBinary_ImportsNoNetworkPackage
// proves it. `atlas-serve` obviously can -- serving is its whole job -- so the
// import check cannot be the guarantee for it, and dropping the guarantee for
// that binary was not acceptable either: it reads the same proprietary index.
//
// So the property is narrowed rather than abandoned. `atlas-serve` ACCEPTS
// connections and never MAKES one. "Nothing leaves your machine" survives
// verbatim: an inbound listener on loopback cannot exfiltrate, and an
// outbound dial is the thing that could.
//
// This test enforces that by looking for the calls rather than the imports,
// which is strictly more precise than the check it complements -- that one
// would pass a package importing os/exec and shelling out to curl.

// dialers are the standard library calls that OPEN an outbound connection.
// Listeners are deliberately absent: net.Listen and http.Server are what this
// binary exists to use.
var dialers = map[string]map[string]bool{
	"net": {
		"Dial": true, "DialTimeout": true, "DialIP": true, "DialTCP": true,
		"DialUDP": true, "DialUnix": true, "LookupHost": true, "LookupIP": true,
	},
	"http": {
		"Get": true, "Post": true, "PostForm": true, "Head": true,
		"NewRequest": true, "NewRequestWithContext": true,
	},
	"smtp": {"Dial": true, "SendMail": true},
	"rpc":  {"Dial": true, "DialHTTP": true},
}

// dialerTypes are types whose mere construction means an outbound client.
var dialerTypes = map[string]map[string]bool{
	"net":  {"Dialer": true},
	"http": {"Client": true, "Transport": true},
}

func TestServeBinary_NeverDialsOut(t *testing.T) {
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
		offenders = append(offenders, outboundCallsIn(t, pkgPath, dir)...)
		for _, imported := range importsOf(t, dir) {
			if imported == modulePath || strings.HasPrefix(imported, modulePath+"/") {
				walk(imported)
			}
		}
	}
	walk(modulePath + "/cmd/atlas-serve")

	// A floor, not a measurement: if the directory mapping ever broke, the
	// walk would inspect almost nothing and pass vacuously.
	if len(reached) < 10 {
		t.Fatalf("the walk reached only %d packages (%v); it is not exercising the binary",
			len(reached), sortedKeys(reached))
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Errorf("atlas-serve reaches outbound-connection code:\n  %s\n"+
			"atlas-serve accepts connections and must never make one. If this is "+
			"intentional, docs/security.md must stop claiming that nothing leaves "+
			"the machine BEFORE this test is changed.",
			strings.Join(offenders, "\n  "))
	}
}

// outboundCallsIn reports every dialing call or client construction in dir.
func outboundCallsIn(t *testing.T, pkgPath, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read package dir %s: %v", dir, err)
	}
	var out []string
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", filepath.Join(dir, name), perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if fns, ok := dialers[ident.Name]; ok && fns[sel.Sel.Name] {
				out = append(out, pkgPath+" calls "+ident.Name+"."+sel.Sel.Name+" ("+name+")")
			}
			if types, ok := dialerTypes[ident.Name]; ok && types[sel.Sel.Name] {
				out = append(out, pkgPath+" constructs "+ident.Name+"."+sel.Sel.Name+" ("+name+")")
			}
			return true
		})
	}
	return out
}

// The guard on the guard: if the walk stopped resolving directories it would
// pass having inspected nothing, and a planted dial proves it still looks.
func TestServeBinary_TheDialCheckActuallyLooks(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "cmd", "atlas-serve")
	// The binary legitimately calls net.Listen and net.JoinHostPort; neither
	// is a dialer, and finding one here would mean the matcher is too broad.
	if found := outboundCallsIn(t, "probe", dir); len(found) != 0 {
		t.Fatalf("the matcher flags cmd/atlas-serve's own listener calls: %v", found)
	}

	planted := filepath.Join(t.TempDir(), "pkg")
	if err := os.MkdirAll(planted, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src := "package pkg\n\nimport \"net/http\"\n\nfunc ping() { _, _ = http.Get(\"https://example.test\") }\n"
	if err := os.WriteFile(filepath.Join(planted, "x.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if found := outboundCallsIn(t, "probe", planted); len(found) == 0 {
		t.Error("a planted http.Get was not detected; this check cannot fail and is worthless")
	}
}
