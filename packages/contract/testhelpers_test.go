package contract

import (
	"github.com/sosalejandro/atlas/packages/shared"
)

// newSymbolForPathTest is a tiny test helper that fabricates a Go symbol
// sitting at the given repo-relative path so individual extractor tests
// can exercise the path-filter branches without spinning up a full
// codeindex run.
func newSymbolForPathTest(path string) shared.Symbol {
	return shared.Symbol{
		ID:        shared.SymbolID("pkg.Func"),
		Kind:      shared.KindFunc,
		Signature: "func Func()",
		Position:  shared.FilePosition{Path: path, Line: 1},
	}
}

