package diagnose

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// withTempStore opens a fresh on-disk Store for a test. The store is
// closed automatically on test cleanup.
func withTempStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "atlas-state.db")
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// writeFile writes content to dir/relPath, creating parent dirs as
// needed, and returns the absolute path.
func writeFile(t *testing.T, dir, relPath, content string) string {
	t.Helper()
	abs := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", abs, err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", abs, err)
	}
	return abs
}

// buildSyntheticProject lays down a tiny on-disk project + populates the
// Store via codeindex.Index ingestion. The project shape:
//
//	src/handler.go    — pkg.LoginHandler (calls LoginService, FormatError)
//	src/service.go    — pkg.LoginService (calls UserRepo.FindByEmail)
//	src/repo.go       — pkg.UserRepo.FindByEmail
//	src/util.go       — pkg.FormatError  (the "shared" panic-emitter)
//
// The panic format string lives in FormatError — so a "panic: nil
// pointer deref at handler.go:42" symptom should ideally rank FormatError
// near the top (it emits the string + is called by multiple sites).
//
//nolint:funlen // a test fixture is necessarily long; the project's
// .golangci.yml intends to exclude _test.go from funlen via the v1
// issues.exclude-rules schema, but that schema is invalid under v2 in
// the version we're running. Once .golangci.yml moves to
// linters.exclusions.rules this directive can be removed.
func buildSyntheticProject(t *testing.T, s *store.Store) string {
	t.Helper()

	dir := t.TempDir()

	writeFile(t, dir, "src/handler.go", `package pkg

// LoginHandler handles POST /api/v1/auth/login.
func LoginHandler() error {
	if err := LoginService(); err != nil {
		return FormatError("login failed: %v", err)
	}
	return nil
}
`)
	writeFile(t, dir, "src/service.go", `package pkg

// LoginService runs the auth flow.
func LoginService() error {
	return UserRepo_FindByEmail("x@y.z")
}
`)
	writeFile(t, dir, "src/repo.go", `package pkg

// UserRepo_FindByEmail looks up a user by email.
func UserRepo_FindByEmail(email string) error {
	return nil
}
`)
	writeFile(t, dir, "src/util.go", `package pkg

// FormatError wraps an error with a panic-shaped format string.
// This is the literal hot spot: every nil-pointer panic in the
// service comes through here.
func FormatError(format string, args ...any) error {
	return errorsNew("panic: nil pointer deref at handler.go: " + format)
}

func errorsNew(s string) error { return nil }
`)

	// Build the in-memory Index manually so we don't depend on the
	// AST scanner finding everything (the test files above are tiny
	// and the scanner's exact behaviour is its own package's concern).
	g := graph.New()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	mk := func(id shared.SymbolID, file string, line int) *graph.Node {
		return &graph.Node{Symbol: shared.Symbol{
			ID:       id,
			Kind:     shared.KindFunc,
			Position: shared.FilePosition{Path: file, Line: line},
			Package:  "github.com/example/pkg",
		}}
	}

	handler := mk("pkg.LoginHandler", "src/handler.go", 4)
	service := mk("pkg.LoginService", "src/service.go", 4)
	repo := mk("pkg.UserRepo_FindByEmail", "src/repo.go", 4)
	formatErr := mk("pkg.FormatError", "src/util.go", 6)

	for _, n := range []*graph.Node{handler, service, repo, formatErr} {
		g.AddNode(n)
	}
	g.AddEdge("pkg.LoginHandler", "pkg.LoginService")
	g.AddEdge("pkg.LoginHandler", "pkg.FormatError")
	g.AddEdge("pkg.LoginService", "pkg.UserRepo_FindByEmail")
	// A second caller of FormatError to bump its centrality.
	g.AddEdge("pkg.UserRepo_FindByEmail", "pkg.FormatError")

	idx := &codeindex.Index{
		Root:        dir,
		GeneratedAt: now,
		Graph:       g,
		Symbols: []shared.Symbol{
			handler.Symbol, service.Symbol, repo.Symbol, formatErr.Symbol,
		},
		Annotations: []shared.Annotation{
			{
				Kind:     shared.AnnFeature,
				IDs:      []string{"auth.login"},
				Source:   shared.SourceAtlas,
				Position: shared.FilePosition{Path: "src/handler.go", Line: 3},
				Raw:      "auth.login",
			},
		},
		FileHashes: map[string]codeindex.FileHash{
			"src/handler.go": {Path: "src/handler.go", SHA256: "h1", ModTime: now, LastScanned: now},
			"src/service.go": {Path: "src/service.go", SHA256: "h2", ModTime: now, LastScanned: now},
			"src/repo.go":    {Path: "src/repo.go", SHA256: "h3", ModTime: now, LastScanned: now},
			"src/util.go":    {Path: "src/util.go", SHA256: "h4", ModTime: now, LastScanned: now},
		},
	}

	if _, err := s.Ingest(context.Background(), idx); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	// Upsert the feature record so feature attribution works (Ingest
	// records annotations but does NOT auto-create feature rows for
	// dangling annotation IDs).
	if err := s.Features().Upsert(context.Background(), store.Feature{
		ID:    "auth.login",
		Title: "User Login",
		Kind:  store.FeatureKindFeature,
	}); err != nil {
		t.Fatalf("Features.Upsert: %v", err)
	}
	// Link the feature → LoginHandler symbol manually.
	syms := s.Symbols()
	handlerRow, err := syms.FindByQualifiedName(context.Background(), "pkg.LoginHandler")
	if err != nil {
		t.Fatalf("find handler row: %v", err)
	}
	if err := s.FeatureSymbols().Link(context.Background(), store.FeatureSymbolLink{
		FeatureID: "auth.login",
		SymbolID:  handlerRow.ID,
		Role:      store.RoleImpl,
		Source:    store.SourceAnnotation,
	}); err != nil {
		t.Fatalf("FeatureSymbols.Link: %v", err)
	}

	return dir
}

func TestDiagnose_EmptySymptomReturnsSentinel(t *testing.T) {
	t.Parallel()

	s := withTempStore(t)
	_, err := Diagnose(context.Background(), "", s, nil)
	if !errors.Is(err, ErrEmptySymptom) {
		t.Fatalf("expected ErrEmptySymptom, got %v", err)
	}

	_, err = Diagnose(context.Background(), "   \t\n  ", s, nil)
	if !errors.Is(err, ErrEmptySymptom) {
		t.Fatalf("expected ErrEmptySymptom on whitespace-only symptom, got %v", err)
	}
}

func TestDiagnose_NoSymbolsReturnsEmpty(t *testing.T) {
	t.Parallel()

	s := withTempStore(t)
	got, err := Diagnose(context.Background(), "panic: nil pointer", s, nil)
	if err != nil {
		t.Fatalf("Diagnose against empty store: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil matches against empty store, got %d", len(got))
	}
}

// Integration test: build a real project + Store, then assert the
// top-scored Match points to FormatError (the symbol that owns the
// panic format string AND has the highest call count).
func TestDiagnose_NilPointerPanicRanksFormatErrorFirst(t *testing.T) {
	t.Parallel()

	s := withTempStore(t)
	root := buildSyntheticProject(t, s)

	got, err := Diagnose(context.Background(),
		"panic: nil pointer deref at handler.go:42",
		s, &Options{ProjectRoot: root})
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected at least one match")
	}

	top := got[0]
	if top.Symbol.ID != "pkg.FormatError" {
		t.Fatalf("top match = %q (confidence %.3f); want pkg.FormatError\nAll matches:\n%s",
			top.Symbol.ID, top.Confidence, dumpMatches(got))
	}
	if top.Confidence <= 0.30 {
		t.Fatalf("top confidence %.3f is suspiciously low; want > 0.30", top.Confidence)
	}
	if top.Reason == "" {
		t.Fatal("top match Reason must be non-empty")
	}
}

// Edge-case dimension 1: symptom with regex metachars that would crash a
// naïve regex compile if we forgot QuoteMeta.
func TestDiagnose_RegexMetacharsInSymptom(t *testing.T) {
	t.Parallel()

	s := withTempStore(t)
	root := buildSyntheticProject(t, s)

	// Symptom contains parens, brackets, plus signs, asterisks, dot.
	got, err := Diagnose(context.Background(),
		"panic: runtime error (*nil)+[deref] at handler.go.",
		s, &Options{ProjectRoot: root})
	if err != nil {
		t.Fatalf("Diagnose with metachar symptom: %v", err)
	}
	// Tokenisation should still pick up "panic" and "handler.go", which
	// appear in FormatError.
	if len(got) == 0 {
		t.Fatal("expected at least one match via token fallback")
	}
}

// Edge-case dimension 2: symptom that matches NOTHING in the project.
func TestDiagnose_NoMatchReturnsEmptySliceNoError(t *testing.T) {
	t.Parallel()

	s := withTempStore(t)
	root := buildSyntheticProject(t, s)

	got, err := Diagnose(context.Background(),
		"xyzzy_quux_obscure_token_99999",
		s, &Options{ProjectRoot: root})
	if err != nil {
		t.Fatalf("Diagnose with no-match symptom: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty matches; got %d:\n%s", len(got), dumpMatches(got))
	}
}

// Edge-case dimension 3: high-noise symptom (single common word) — the
// scorer should still produce a finite, ranked result and not panic /
// loop forever on the saturation calc.
func TestDiagnose_LowSignalSymptom(t *testing.T) {
	t.Parallel()

	s := withTempStore(t)
	root := buildSyntheticProject(t, s)

	// "error" alone is a stop word — should still produce zero matches
	// (token filter drops it before scoring).
	got, err := Diagnose(context.Background(), "error", s, &Options{ProjectRoot: root})
	if err != nil {
		t.Fatalf("Diagnose low-signal: %v", err)
	}
	// With "error" as the symptom: tokens filter drops the word
	// (it's a stop word), so only the whole-symptom regex runs.
	// FormatError's body contains "panic: nil pointer..." which itself
	// contains "error" as a substring — so we DO expect ≥1 match here.
	if len(got) == 0 {
		t.Fatal("expected at least one whole-symptom match for 'error'")
	}
	for _, m := range got {
		if m.Confidence < 0 || m.Confidence > 1 {
			t.Fatalf("confidence out of range: %.3f for %s", m.Confidence, m.Symbol.ID)
		}
	}
}

// Edge-case dimension 4: MaxResults caps output.
func TestDiagnose_MaxResultsCapsOutput(t *testing.T) {
	t.Parallel()

	s := withTempStore(t)
	root := buildSyntheticProject(t, s)

	got, err := Diagnose(context.Background(),
		"panic",
		s, &Options{ProjectRoot: root, MaxResults: 1})
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("MaxResults=1 should cap to 1; got %d", len(got))
	}
}

func TestDiagnose_FeatureAttribution(t *testing.T) {
	t.Parallel()

	s := withTempStore(t)
	root := buildSyntheticProject(t, s)

	// Symptom that uniquely matches LoginHandler's body
	// ("LoginService" is referenced in its source).
	got, err := Diagnose(context.Background(),
		"call to LoginService failed",
		s, &Options{ProjectRoot: root})
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected at least one match")
	}
	// Find the LoginHandler match (it should be the only one with a
	// feature attribution).
	var handlerMatch *Match
	for i := range got {
		if got[i].Symbol.ID == "pkg.LoginHandler" {
			handlerMatch = &got[i]
			break
		}
	}
	if handlerMatch == nil {
		t.Fatalf("expected pkg.LoginHandler in matches; got:\n%s", dumpMatches(got))
	}
	if handlerMatch.Feature == nil {
		t.Fatal("expected Feature attribution on LoginHandler match")
	}
	if handlerMatch.Feature.ID != "auth.login" {
		t.Fatalf("feature ID = %q, want \"auth.login\"", handlerMatch.Feature.ID)
	}
}

// dumpMatches is a test-only helper that produces a human-readable dump
// of the match list, used in failure messages so you can see why an
// assertion fired.
func dumpMatches(ms []Match) string {
	out := ""
	for _, m := range ms {
		out += fmt.Sprintf("  %s [conf=%.3f] — %s\n", m.Symbol.ID, m.Confidence, m.Reason)
	}
	return out
}
