package cfg_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/atlas/packages/cfg"
)

// detect runs the N+1 detector over one fixture function.
func detect(t *testing.T, fn string, opts cfg.QueryLoopOptions) []cfg.QueryInLoop {
	t.Helper()
	fset := token.NewFileSet()
	path := filepath.Join("testdata", "flowfixture", "queries.go")
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil || fd.Name.Name != fn {
			continue
		}
		g, err := cfg.Build(fset, fd)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		return cfg.DetectQueryInLoop(fset, fd, g, opts)
	}
	t.Fatalf("fixture has no func %s", fn)
	return nil
}

// TestDetectQueryInLoop_FiresOnRange is the acceptance criterion: a query
// inside a range over a collection is reported, with both sites.
func TestDetectQueryInLoop_FiresOnRange(t *testing.T) {
	got := detect(t, "LoadUsers", cfg.QueryLoopOptions{})
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1: %+v", len(got), got)
	}
	f := got[0]
	if f.LoopLine != 18 {
		t.Errorf("LoopLine = %d, want 18", f.LoopLine)
	}
	if f.QueryLine != 19 {
		t.Errorf("QueryLine = %d, want 19", f.QueryLine)
	}
	if !f.OverCollection {
		t.Error("the loop ranges over a slice; OverCollection should be true")
	}
	if f.Guarded {
		t.Error("the query runs on every iteration; it is not guarded")
	}
	if f.Confidence != cfg.ConfidenceMedium {
		t.Errorf("confidence = %q, want %q (name heuristic only)", f.Confidence, cfg.ConfidenceMedium)
	}
	if f.Caveat == "" {
		t.Error("every finding must carry the smell-not-proof caveat")
	}
}

// TestDetectQueryInLoop_SilentWhenHoisted is the other half of the criterion:
// the same query, issued once outside the loop, produces nothing. A detector
// that fires here is one nobody will leave switched on.
func TestDetectQueryInLoop_SilentWhenHoisted(t *testing.T) {
	if got := detect(t, "LoadUsersHoisted", cfg.QueryLoopOptions{}); len(got) != 0 {
		t.Errorf("findings = %+v, want none", got)
	}
}

// TestDetectQueryInLoop_KnownQuerySiteRaisesConfidence: when the caller can
// prove the call reaches a query (a graph edge to a sql: node), the finding is
// no longer a guess about a method name.
func TestDetectQueryInLoop_KnownQuerySiteRaisesConfidence(t *testing.T) {
	got := detect(t, "LoadUsers", cfg.QueryLoopOptions{KnownQueryLines: map[int]bool{19: true}})
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1", len(got))
	}
	if got[0].Confidence != cfg.ConfidenceHigh {
		t.Errorf("confidence = %q, want %q", got[0].Confidence, cfg.ConfidenceHigh)
	}
	if got[0].Evidence != cfg.EvidenceGraphEdge {
		t.Errorf("evidence = %q, want %q", got[0].Evidence, cfg.EvidenceGraphEdge)
	}
}

// TestDetectQueryInLoop_CacheMissLowersConfidence: a query the loop can skip
// is exactly what a correct cache looks like, so the finding stands but says
// so. This is the difference between a report a team acts on and one they mute.
func TestDetectQueryInLoop_CacheMissLowersConfidence(t *testing.T) {
	got := detect(t, "LoadUsersCached", cfg.QueryLoopOptions{})
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1: %+v", len(got), got)
	}
	f := got[0]
	if f.QueryLine != 53 {
		t.Errorf("QueryLine = %d, want 53", f.QueryLine)
	}
	if !f.Guarded {
		t.Error("the query is skipped on a cache hit; Guarded should be true")
	}
	if f.Confidence != cfg.ConfidenceLow {
		t.Errorf("confidence = %q, want %q", f.Confidence, cfg.ConfidenceLow)
	}
}

// TestDetectQueryInLoop_NonCollectionLoopIsWeaker: a query in a retry loop is
// not an N+1 — the loop is not per-row — so the confidence drops.
func TestDetectQueryInLoop_NonCollectionLoopIsWeaker(t *testing.T) {
	src := `package p
func retry(db D, id int) {
	for i := 0; i < 3; i++ {
		_, _ = db.GetUser(id)
	}
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "snippet.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fd := file.Decls[0].(*ast.FuncDecl)
	g, err := cfg.Build(fset, fd)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := cfg.DetectQueryInLoop(fset, fd, g, cfg.QueryLoopOptions{})
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1", len(got))
	}
	if got[0].OverCollection {
		t.Error("a counted for-loop is not a collection")
	}
	if got[0].Confidence != cfg.ConfidenceLow {
		t.Errorf("confidence = %q, want %q", got[0].Confidence, cfg.ConfidenceLow)
	}
}

// TestDetectQueryInLoop_LoopThatCannotRepeat: a `for` whose body always
// breaks executes once, so a query in it is not an N+1 and must not be
// reported as one.
func TestDetectQueryInLoop_LoopThatCannotRepeat(t *testing.T) {
	src := `package p
func once(db D, id int) {
	for {
		_, _ = db.GetUser(id)
		break
	}
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "snippet.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fd := file.Decls[0].(*ast.FuncDecl)
	g, err := cfg.Build(fset, fd)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := cfg.DetectQueryInLoop(fset, fd, g, cfg.QueryLoopOptions{}); len(got) != 0 {
		t.Errorf("findings = %+v, want none: the loop has no back edge", got)
	}
}
