package goscan

import (
	"context"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

// Two packages declaring the same receiver+method short name must BOTH be
// indexed. Before this test, graph.AddNode's first-wins dedupe silently
// dropped the second declaration, so every symbol in the losing file
// vanished from the store — which is why ~25% of coverprofile files
// reconciled to zero symbols on a 39-module workspace (issue #85).
func TestScan_CollidingShortNames_BothFilesIndexed(t *testing.T) {
	t.Parallel()

	res, err := Scan(context.Background(), "testdata/collisionproject", Options{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	byFile := map[string][]shared.Symbol{}
	for _, s := range res.Symbols {
		byFile[s.Position.Path] = append(byFile[s.Position.Path], s)
	}
	for _, want := range []string{"alpha/chat.go", "beta/chat.go"} {
		if len(byFile[want]) == 0 {
			t.Fatalf("no symbols indexed for %s; symbols: %+v", want, res.Symbols)
		}
	}

	// The first file in walk order keeps the bare short ID; the colliding
	// declaration is package-qualified so both survive.
	if _, ok := res.Graph.Nodes["Chat.MarkLoaded"]; !ok {
		t.Fatalf("expected bare short ID Chat.MarkLoaded; nodes: %v", nodeIDs(res))
	}
	if _, ok := res.Graph.Nodes["beta.Chat.MarkLoaded"]; !ok {
		t.Fatalf("expected package-qualified beta.Chat.MarkLoaded; nodes: %v", nodeIDs(res))
	}
}

// Unexported plain functions are instrumented by the Go compiler, so they
// must be indexed: otherwise their coverage blocks are charged to whichever
// neighbouring symbol's span happens to swallow them.
func TestScan_UnexportedPlainFuncs_Indexed(t *testing.T) {
	t.Parallel()

	res, err := Scan(context.Background(), "testdata/collisionproject", Options{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if _, ok := res.Graph.Nodes["alpha.normalize"]; !ok {
		t.Fatalf("expected unexported func alpha.normalize to be indexed; nodes: %v", nodeIDs(res))
	}

	// ...and opting out must still be possible for graph-only audits.
	res2, err := Scan(context.Background(), "testdata/collisionproject", Options{SkipUnexportedFuncs: true})
	if err != nil {
		t.Fatalf("Scan(SkipUnexportedFuncs): %v", err)
	}
	if _, ok := res2.Graph.Nodes["alpha.normalize"]; ok {
		t.Fatal("SkipUnexportedFuncs=true still indexed alpha.normalize")
	}
}

// Every function symbol must carry its closing brace line. Without EndLine
// the coverage ingest guesses each symbol's span as "up to the next symbol",
// so statements from unindexed neighbours are mis-attributed and the last
// symbol in a file absorbs everything to EOF.
func TestScan_SymbolsCarryEndLine(t *testing.T) {
	t.Parallel()

	res, err := Scan(context.Background(), "testdata/collisionproject", Options{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, s := range res.Symbols {
		if s.Position.Path == "" {
			continue // synthetic (route:/endpoint:) nodes have no source range
		}
		if s.EndLine <= 0 {
			t.Fatalf("symbol %s has no EndLine", s.ID)
		}
		if s.EndLine < s.Position.Line {
			t.Fatalf("symbol %s EndLine %d < Line %d", s.ID, s.EndLine, s.Position.Line)
		}
	}
}

func TestNarrowHandlerMatches(t *testing.T) {
	t.Parallel()

	c := newScanContext(".", ".", Options{})
	c.qualifiedIDs["contexts/identity/handlers.AuthHandler.Login"] = true

	cases := []struct {
		name string
		in   []shared.SymbolID
		want []shared.SymbolID
	}{
		{"single candidate is returned as-is",
			[]shared.SymbolID{"AuthHandler.Login"}, []shared.SymbolID{"AuthHandler.Login"}},
		{"exported wins over a package-private helper of the same name",
			[]shared.SymbolID{"AuthHandler.Login", "auth.login"}, []shared.SymbolID{"AuthHandler.Login"}},
		{"the bare id wins over the package-qualified id of a collision",
			[]shared.SymbolID{"contexts/identity/handlers.AuthHandler.Login", "AuthHandler.Login"},
			[]shared.SymbolID{"AuthHandler.Login"}},
		{"two genuinely different handlers stay ambiguous",
			[]shared.SymbolID{"AuthHandler.Login", "AdminHandler.Login"},
			[]shared.SymbolID{"AuthHandler.Login", "AdminHandler.Login"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := c.narrowHandlerMatches(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}
