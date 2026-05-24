package store

import "testing"

// TestIsValidEdgeMeta locks in the kind-scoped vocabulary for the
// edge_meta column added in migration 0008 (issue #16). Only Python
// `import` edges carry a meta value today; everything else must
// reject any non-empty value so a scanner bug surfaces as a hard
// validation failure rather than silently landing junk.
func TestIsValidEdgeMeta(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		kind EdgeKind
		meta string
		want bool
	}{
		// Empty is always valid — the column is NULLable.
		{"empty meta on call", EdgeKindCall, "", true},
		{"empty meta on import", EdgeKindImport, "", true},

		// Every documented import scope must be accepted.
		{"module scope", EdgeKindImport, EdgeMetaImportScopeModule, true},
		{"function scope", EdgeKindImport, EdgeMetaImportScopeFunction, true},
		{"conditional scope", EdgeKindImport, EdgeMetaImportScopeConditional, true},
		{"type_checking scope", EdgeKindImport, EdgeMetaImportScopeTypeChecking, true},
		{"try_guard scope", EdgeKindImport, EdgeMetaImportScopeTryGuard, true},

		// Unknown values on import are rejected even if syntactically
		// similar to the canonical set.
		{"unknown import scope", EdgeKindImport, "function_body", false},
		{"empty after trim", EdgeKindImport, "  ", false},

		// Non-import kinds have no defined vocabulary — anything
		// non-empty is rejected (the scope tags don't apply to
		// inheritance / decorator / call).
		{"call with scope-like meta", EdgeKindCall, "module", false},
		{"inheritance with scope", EdgeKindInheritance, "module", false},
		{"decorator with scope", EdgeKindDecorator, "module", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := IsValidEdgeMeta(tc.kind, tc.meta)
			if got != tc.want {
				t.Errorf("IsValidEdgeMeta(%q, %q) = %v, want %v",
					tc.kind, tc.meta, got, tc.want)
			}
		})
	}
}

// TestNormalizeEdgeMeta sanitises invalid values to empty (NULL).
// This is the defensive layer between a scanner that might emit a
// future scope tag we don't recognise and the SQLite column — we'd
// rather lose the qualifier than corrupt the column.
func TestNormalizeEdgeMeta(t *testing.T) {
	t.Parallel()

	cases := []struct {
		kind EdgeKind
		raw  string
		want string
	}{
		{EdgeKindImport, "module", "module"},
		{EdgeKindImport, "function", "function"},
		{EdgeKindImport, "unknown_scope", ""},
		{EdgeKindCall, "module", ""},
		{EdgeKindImport, "", ""},
	}
	for _, tc := range cases {
		got := NormalizeEdgeMeta(tc.kind, tc.raw)
		if got != tc.want {
			t.Errorf("NormalizeEdgeMeta(%q, %q) = %q, want %q",
				tc.kind, tc.raw, got, tc.want)
		}
	}
}

// TestEdgesInsert_PersistsAndNormalisesMeta exercises the Insert
// path end-to-end against a real SQLite store: valid meta lands
// in the column, invalid meta lands as NULL.
func TestEdgesInsert_PersistsAndNormalisesMeta(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	ctx := t.Context()

	// Seed two symbols so Insert's FK constraints are satisfied.
	from, err := s.Symbols().Insert(ctx, SymbolRow{
		QualifiedName: "mod_a",
		Kind:          "module",
		FilePath:      "mod_a.py",
		Line:          1,
	})
	if err != nil {
		t.Fatalf("seed from-symbol: %v", err)
	}
	to, err := s.Symbols().Insert(ctx, SymbolRow{
		QualifiedName: "os",
		Kind:          "module",
		FilePath:      "external:py",
		Line:          1,
	})
	if err != nil {
		t.Fatalf("seed to-symbol: %v", err)
	}

	// Valid: import edge with a real scope tag.
	if _, err := s.Edges().Insert(ctx, EdgeRow{
		FromID:   from,
		ToID:     to,
		Kind:     EdgeKindImport,
		FilePath: "mod_a.py",
		Line:     1,
		Meta:     EdgeMetaImportScopeFunction,
	}); err != nil {
		t.Fatalf("Insert valid: %v", err)
	}

	// Invalid: call edge with a meta value — must be normalised to
	// NULL rather than rejected, so a scanner sending a stray Meta
	// can't break ingest.
	if _, err := s.Edges().Insert(ctx, EdgeRow{
		FromID:   from,
		ToID:     to,
		Kind:     EdgeKindCall,
		FilePath: "mod_a.py",
		Line:     2,
		Meta:     "garbage",
	}); err != nil {
		t.Fatalf("Insert invalid-meta: %v", err)
	}

	// Read back the import edge — should preserve the scope tag.
	got, err := s.Edges().Out(ctx, from)
	if err != nil {
		t.Fatalf("Out: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 edges, got %d (%+v)", len(got), got)
	}
	var sawImport, sawCall bool
	for _, e := range got {
		switch e.Kind {
		case EdgeKindImport:
			sawImport = true
			if e.Meta != EdgeMetaImportScopeFunction {
				t.Errorf("import edge Meta = %q, want %q",
					e.Meta, EdgeMetaImportScopeFunction)
			}
		case EdgeKindCall:
			sawCall = true
			if e.Meta != "" {
				t.Errorf("call edge Meta = %q, want empty (normalised)", e.Meta)
			}
		}
	}
	if !sawImport || !sawCall {
		t.Errorf("expected both edge kinds round-tripped; sawImport=%v sawCall=%v",
			sawImport, sawCall)
	}
}
