package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

func TestSymbols_InsertIdempotent(t *testing.T) {
	syms := openTestStore(t).Symbols()
	ctx := context.Background()

	id1, err := syms.Insert(ctx, SymbolRow{
		QualifiedName: "pkg.Foo",
		Kind:          shared.KindFunc,
		FilePath:      "src/foo.go",
		Line:          7,
	})
	if err != nil {
		t.Fatalf("Insert #1: %v", err)
	}
	id2, err := syms.Insert(ctx, SymbolRow{
		QualifiedName: "pkg.Foo",
		Kind:          shared.KindFunc,
		FilePath:      "src/foo.go",
		Line:          7,
	})
	if err != nil {
		t.Fatalf("Insert #2: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("re-Insert returned different surrogate id: %d vs %d", id1, id2)
	}
}

func TestSymbols_NormalizesAuditKindsToFunc(t *testing.T) {
	syms := openTestStore(t).Symbols()
	ctx := context.Background()

	// shared.KindHandler is an audit-layer value not in the §5.4 CHECK set;
	// the adapter must collapse it so the row is acceptable.
	_, err := syms.Insert(ctx, SymbolRow{
		QualifiedName: "pkg.Handler",
		Kind:          shared.KindHandler,
		FilePath:      "src/x.go",
		Line:          1,
	})
	if err != nil {
		t.Fatalf("Insert with KindHandler: %v", err)
	}
	r, err := syms.FindByQualifiedName(ctx, "pkg.Handler")
	if err != nil {
		t.Fatalf("FindByQualifiedName: %v", err)
	}
	if r.Kind != shared.KindFunc {
		t.Errorf("normalized kind = %q, want %q", r.Kind, shared.KindFunc)
	}
}

func TestSymbols_FindByQualifiedName_Missing(t *testing.T) {
	syms := openTestStore(t).Symbols()
	_, err := syms.FindByQualifiedName(context.Background(), "nope")
	if !errors.Is(err, shared.ErrSymbolNotFound) {
		t.Fatalf("FindByQualifiedName(nope) err = %v, want ErrSymbolNotFound", err)
	}
}

func TestSymbols_ListFilter(t *testing.T) {
	syms := openTestStore(t).Symbols()
	ctx := context.Background()

	for _, sym := range []SymbolRow{
		{QualifiedName: "p.A", Kind: shared.KindFunc, FilePath: "src/a.go", Line: 1, Package: mustPtr("pkgA")},
		{QualifiedName: "p.B", Kind: shared.KindFunc, FilePath: "src/b.go", Line: 1, Package: mustPtr("pkgA")},
		{QualifiedName: "p.C", Kind: shared.KindFunc, FilePath: "src/c.go", Line: 1, Package: mustPtr("pkgB")},
	} {
		if _, err := syms.Insert(ctx, sym); err != nil {
			t.Fatalf("Insert %s: %v", sym.QualifiedName, err)
		}
	}

	out, err := syms.List(ctx, SymbolFilter{Package: "pkgA"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("List(pkgA) len = %d, want 2", len(out))
	}
}

// TestSymbols_ListFilter_Permutations is the regression suite for the
// sqlc-narg-style ListSymbols refactor (issue #10/finding-3). It seeds
// a known fixture and exercises every single-field filter + a few
// combined ones — empty fields short-circuit the predicate, populated
// fields narrow.
func TestSymbols_ListFilter_Permutations(t *testing.T) {
	s := openTestStore(t)
	syms := s.Symbols()
	ctx := context.Background()

	// Fixture: three packages × two BCs × mixed kinds across four files.
	fixture := []SymbolRow{
		{QualifiedName: "alpha.A1", Kind: shared.KindFunc, FilePath: "src/contexts/alpha/a.go", Line: 1, Package: mustPtr("alpha"), BCPath: mustPtr("src/contexts/alpha")},
		{QualifiedName: "alpha.A2", Kind: shared.KindMethod, FilePath: "src/contexts/alpha/a.go", Line: 20, Package: mustPtr("alpha"), BCPath: mustPtr("src/contexts/alpha")},
		{QualifiedName: "alpha.B1", Kind: shared.KindFunc, FilePath: "src/contexts/alpha/b.go", Line: 1, Package: mustPtr("alpha"), BCPath: mustPtr("src/contexts/alpha")},
		{QualifiedName: "beta.C1", Kind: shared.KindFunc, FilePath: "src/contexts/beta/c.go", Line: 1, Package: mustPtr("beta"), BCPath: mustPtr("src/contexts/beta")},
		{QualifiedName: "beta.C2", Kind: shared.KindType, FilePath: "src/contexts/beta/c.go", Line: 5, Package: mustPtr("beta"), BCPath: mustPtr("src/contexts/beta")},
		// One row deliberately omits package + bc_path so we can verify
		// nullable-column filters skip it when their predicate is opt-in.
		{QualifiedName: "loose.L1", Kind: shared.KindFunc, FilePath: "src/loose.go", Line: 1},
	}
	for _, row := range fixture {
		if _, err := syms.Insert(ctx, row); err != nil {
			t.Fatalf("Insert %s: %v", row.QualifiedName, err)
		}
	}

	cases := []struct {
		name   string
		filter SymbolFilter
		wantQN []string
	}{
		{
			name:   "no filters returns all rows ordered by file_path,line,qualified_name",
			filter: SymbolFilter{},
			wantQN: []string{"alpha.A1", "alpha.A2", "alpha.B1", "beta.C1", "beta.C2", "loose.L1"},
		},
		{
			name:   "file_path only",
			filter: SymbolFilter{FilePath: "src/contexts/alpha/a.go"},
			wantQN: []string{"alpha.A1", "alpha.A2"},
		},
		{
			name:   "package only",
			filter: SymbolFilter{Package: "alpha"},
			wantQN: []string{"alpha.A1", "alpha.A2", "alpha.B1"},
		},
		{
			name:   "bc_path only",
			filter: SymbolFilter{BCPath: "src/contexts/beta"},
			wantQN: []string{"beta.C1", "beta.C2"},
		},
		{
			name:   "kind only (method narrows past file boundary)",
			filter: SymbolFilter{Kind: shared.KindMethod},
			wantQN: []string{"alpha.A2"},
		},
		{
			name:   "kind=type only",
			filter: SymbolFilter{Kind: shared.KindType},
			wantQN: []string{"beta.C2"},
		},
		{
			name:   "package+kind composed",
			filter: SymbolFilter{Package: "alpha", Kind: shared.KindFunc},
			wantQN: []string{"alpha.A1", "alpha.B1"},
		},
		{
			name:   "file_path+kind composed",
			filter: SymbolFilter{FilePath: "src/contexts/alpha/a.go", Kind: shared.KindFunc},
			wantQN: []string{"alpha.A1"},
		},
		{
			name:   "bc_path+package composed",
			filter: SymbolFilter{BCPath: "src/contexts/alpha", Package: "alpha"},
			wantQN: []string{"alpha.A1", "alpha.A2", "alpha.B1"},
		},
		{
			name:   "all four filters composed (most-specific)",
			filter: SymbolFilter{FilePath: "src/contexts/beta/c.go", Package: "beta", BCPath: "src/contexts/beta", Kind: shared.KindType},
			wantQN: []string{"beta.C2"},
		},
		{
			name:   "no-match returns empty (not nil error)",
			filter: SymbolFilter{Package: "does-not-exist"},
			wantQN: []string{},
		},
		{
			name:   "audit-layer kind is normalized to func before bind",
			filter: SymbolFilter{Kind: shared.KindHandler, Package: "alpha"},
			wantQN: []string{"alpha.A1", "alpha.B1"}, // method A2 excluded; func A1+B1 included after normalize
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := syms.List(ctx, tc.filter)
			if err != nil {
				t.Fatalf("List(%+v): %v", tc.filter, err)
			}
			gotQN := make([]string, 0, len(got))
			for _, r := range got {
				gotQN = append(gotQN, string(r.QualifiedName))
			}
			if len(gotQN) != len(tc.wantQN) {
				t.Fatalf("List(%+v) returned %d rows, want %d\n got: %v\nwant: %v",
					tc.filter, len(gotQN), len(tc.wantQN), gotQN, tc.wantQN)
			}
			for i := range gotQN {
				if gotQN[i] != tc.wantQN[i] {
					t.Errorf("List(%+v)[%d] = %q, want %q (got=%v want=%v)",
						tc.filter, i, gotQN[i], tc.wantQN[i], gotQN, tc.wantQN)
				}
			}
		})
	}
}

func TestSymbols_DeleteByFile_CascadesEdges(t *testing.T) {
	s := openTestStore(t)
	syms := s.Symbols()
	edges := s.Edges()
	ctx := context.Background()

	a, _ := syms.Insert(ctx, SymbolRow{QualifiedName: "p.A", Kind: shared.KindFunc, FilePath: "src/a.go", Line: 1})
	b, _ := syms.Insert(ctx, SymbolRow{QualifiedName: "p.B", Kind: shared.KindFunc, FilePath: "src/b.go", Line: 1})
	if _, err := edges.Insert(ctx, EdgeRow{
		FromID: a, ToID: b, Kind: EdgeKindCall, FilePath: "src/a.go", Line: 2,
	}); err != nil {
		t.Fatalf("edges Insert: %v", err)
	}

	if err := syms.DeleteByFile(ctx, "src/a.go"); err != nil {
		t.Fatalf("DeleteByFile: %v", err)
	}
	if out, _ := edges.Out(ctx, a); len(out) != 0 {
		t.Errorf("edges not cascaded after symbol delete: %+v", out)
	}
}

// TestSymbols_SetAndFindByPattern covers the Phase 6f pattern_matches
// column round-trip: SetPatternMatches persists JSON, FindByPattern
// reads only symbols whose JSON contains the requested pattern token.
func TestSymbols_SetAndFindByPattern(t *testing.T) {
	syms := openTestStore(t).Symbols()
	ctx := context.Background()

	// Three symbols: one outbox-append, one canonical-service, one with
	// no recogniser hits at all.
	for _, sym := range []SymbolRow{
		{QualifiedName: "S.OutboxOnly", Kind: shared.KindMethod, FilePath: "src/a.go", Line: 10},
		{QualifiedName: "S.Canonical", Kind: shared.KindMethod, FilePath: "src/a.go", Line: 30},
		{QualifiedName: "S.NoMatches", Kind: shared.KindMethod, FilePath: "src/b.go", Line: 5},
	} {
		if _, err := syms.Insert(ctx, sym); err != nil {
			t.Fatalf("Insert %s: %v", sym.QualifiedName, err)
		}
	}

	// Persist JSON payloads with the closed-enum pattern tokens.
	outboxJSON := `[{"pattern":"outbox-append","symbol":"S.OutboxOnly","position":{"path":"src/a.go","line":10},"confidence":1.0}]`
	if err := syms.SetPatternMatches(ctx, "S.OutboxOnly", outboxJSON); err != nil {
		t.Fatalf("SetPatternMatches outbox: %v", err)
	}
	canonJSON := `[{"pattern":"canonical-service","symbol":"S.Canonical","position":{"path":"src/a.go","line":30},"confidence":1.0}]`
	if err := syms.SetPatternMatches(ctx, "S.Canonical", canonJSON); err != nil {
		t.Fatalf("SetPatternMatches canonical: %v", err)
	}

	// FindByPattern → outbox-append must return exactly the outbox row.
	outbox, err := syms.FindByPattern(ctx, "outbox-append")
	if err != nil {
		t.Fatalf("FindByPattern outbox: %v", err)
	}
	if len(outbox) != 1 || outbox[0].QualifiedName != "S.OutboxOnly" {
		t.Fatalf("FindByPattern outbox = %+v", outbox)
	}
	if outbox[0].PatternMatches == nil || *outbox[0].PatternMatches != outboxJSON {
		t.Errorf("returned PatternMatches roundtrip mismatch: %+v", outbox[0].PatternMatches)
	}

	canon, err := syms.FindByPattern(ctx, "canonical-service")
	if err != nil {
		t.Fatalf("FindByPattern canonical: %v", err)
	}
	if len(canon) != 1 || canon[0].QualifiedName != "S.Canonical" {
		t.Fatalf("FindByPattern canonical = %+v", canon)
	}

	// Unknown pattern → empty result, no error.
	empty, err := syms.FindByPattern(ctx, "saga-step-order")
	if err != nil {
		t.Fatalf("FindByPattern unknown: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("FindByPattern unknown = %d rows, want 0", len(empty))
	}

	// Empty input clears the column.
	if err := syms.SetPatternMatches(ctx, "S.OutboxOnly", ""); err != nil {
		t.Fatalf("SetPatternMatches clear: %v", err)
	}
	after, err := syms.FindByPattern(ctx, "outbox-append")
	if err != nil {
		t.Fatalf("FindByPattern after clear: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("FindByPattern after clear = %d rows, want 0", len(after))
	}

	// "null" input is also treated as clear (matches json.Marshal(nil) → "null").
	_ = syms.SetPatternMatches(ctx, "S.Canonical", "null")
	stillThere, _ := syms.FindByPattern(ctx, "canonical-service")
	if len(stillThere) != 0 {
		t.Errorf(`SetPatternMatches "null" should clear; got %d rows`, len(stillThere))
	}
}

// TestSymbols_FindByPattern_NoSubstringCollision covers the precision
// pressure dim — `outbox-append` must NOT match a stored
// `outbox-append-extended` payload (the LIKE bound includes the closing
// quote so this is impossible by construction; the test guards the
// contract).
func TestSymbols_FindByPattern_NoSubstringCollision(t *testing.T) {
	syms := openTestStore(t).Symbols()
	ctx := context.Background()

	if _, err := syms.Insert(ctx, SymbolRow{
		QualifiedName: "S.Long",
		Kind:          shared.KindMethod,
		FilePath:      "src/a.go",
		Line:          1,
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// JSON with a longer pattern name — intentionally avoiding the
	// `outbox-append` exact spelling.
	longJSON := `[{"pattern":"outbox-append-extended","symbol":"S.Long","position":{"path":"src/a.go","line":1},"confidence":0.9}]`
	if err := syms.SetPatternMatches(ctx, "S.Long", longJSON); err != nil {
		t.Fatalf("SetPatternMatches: %v", err)
	}

	res, err := syms.FindByPattern(ctx, "outbox-append")
	if err != nil {
		t.Fatalf("FindByPattern: %v", err)
	}
	if len(res) != 0 {
		t.Errorf("substring-collision leak: FindByPattern(outbox-append) returned %d rows", len(res))
	}
}

// TestSymbols_FindByPattern_EmptyPattern guards the input-validation path.
func TestSymbols_FindByPattern_EmptyPattern(t *testing.T) {
	syms := openTestStore(t).Symbols()
	if _, err := syms.FindByPattern(context.Background(), ""); err == nil {
		t.Fatal("FindByPattern(\"\") should error")
	}
}

// TestSymbols_Insert_KindCollapseEmitsWarn covers issue #10/finding-5:
// an unknown SymbolKind on the WRITE path must surface a Warn log record
// so a real parser bug (a kind that silently rewrites to "func" forever)
// is visible in production.
func TestSymbols_Insert_KindCollapseEmitsWarn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atlas-state.db")
	ctx := context.Background()

	buf := &bytes.Buffer{}
	logger := shared.NewSlogLoggerFromHandler(slog.NewJSONHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	s, err := OpenWithLogger(ctx, path, logger)
	if err != nil {
		t.Fatalf("OpenWithLogger: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// shared.KindHandler is an audit-layer value not in the §5.4 CHECK set;
	// the adapter normalizes it to KindFunc AND must log a Warn record.
	if _, err := s.Symbols().Insert(ctx, SymbolRow{
		QualifiedName: "pkg.SomeHandler",
		Kind:          shared.KindHandler,
		FilePath:      "src/x.go",
		Line:          1,
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Parse each JSON log record and assert exactly one Warn with the
	// expected payload appeared.
	var collapses []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("non-JSON log line %q: %v", line, err)
		}
		if rec["level"] == "WARN" && strings.Contains(rec["msg"].(string), "parser drift") {
			collapses = append(collapses, rec)
		}
	}
	if len(collapses) != 1 {
		t.Fatalf("expected exactly 1 parser-drift Warn record, got %d (raw=%q)", len(collapses), buf.String())
	}
	rec := collapses[0]
	if rec["input_kind"] != string(shared.KindHandler) {
		t.Errorf("input_kind = %v, want %q", rec["input_kind"], shared.KindHandler)
	}
	if rec["output_kind"] != string(shared.KindFunc) {
		t.Errorf("output_kind = %v, want %q", rec["output_kind"], shared.KindFunc)
	}
	if rec["qualified_name"] != "pkg.SomeHandler" {
		t.Errorf("qualified_name = %v, want pkg.SomeHandler", rec["qualified_name"])
	}
	if rec["where"] != "symbols.Insert" {
		t.Errorf("where = %v, want symbols.Insert", rec["where"])
	}

	// Canonical kinds must NOT emit a warning — regression guard.
	buf.Reset()
	if _, err := s.Symbols().Insert(ctx, SymbolRow{
		QualifiedName: "pkg.Quiet",
		Kind:          shared.KindFunc,
		FilePath:      "src/y.go",
		Line:          1,
	}); err != nil {
		t.Fatalf("Insert canonical: %v", err)
	}
	if strings.Contains(buf.String(), "parser drift") {
		t.Errorf("canonical KindFunc emitted a drift warning: %q", buf.String())
	}

	// Empty kind defaults silently to KindFunc — the "no kind supplied"
	// path is distinct from "unknown kind supplied" and should NOT warn.
	buf.Reset()
	if _, err := s.Symbols().Insert(ctx, SymbolRow{
		QualifiedName: "pkg.Unspecified",
		Kind:          "",
		FilePath:      "src/z.go",
		Line:          1,
	}); err != nil {
		t.Fatalf("Insert empty kind: %v", err)
	}
	if strings.Contains(buf.String(), "parser drift") {
		t.Errorf("empty kind emitted a drift warning: %q", buf.String())
	}
}
