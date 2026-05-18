package contract

import (
	"strings"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/shared"
)

// extractGoFuncs walks idx.Symbols and emits one KindFunc ContractDef per
// Go symbol that has a non-empty Signature.
//
// Heuristic: codeindex/go's scanner populates Signature on every func
// declaration it discovers (per packages/codeindex/go/scanner.go's
// buildSignature). Symbols without a signature are either synthetic
// (route: / endpoint:) or types/vars — neither of which is a func
// contract. Either way, skip.
//
// FeatureID is pulled from annIdx — see annotationIndex.findFor. Symbols
// whose Position.Path is empty (placeholders, external markers) are
// dropped to keep the output relative-path-clean.
func (e *Extractor) extractGoFuncs(idx *codeindex.Index, annIdx *annotationIndex) ([]ContractDef, []string) {
	if idx == nil {
		return nil, nil
	}
	var (
		defs  []ContractDef
		warns []string
	)
	// Pre-pass: register every Go symbol's declaration line so the
	// annotation index can apply the strict "annotation owns nearest
	// following decl" rule.
	declByFile := make(map[string][]int)
	for _, sym := range idx.Symbols {
		if sym.Position.Path == "" || sym.Position.Line <= 0 {
			continue
		}
		if lang, hasLang := idx.SymbolLangs[sym.ID]; hasLang && lang != "go" {
			continue
		}
		declByFile[sym.Position.Path] = append(declByFile[sym.Position.Path], sym.Position.Line)
	}
	for path, lines := range declByFile {
		annIdx.registerDeclLines(path, lines)
	}

	for _, sym := range idx.Symbols {
		// SymbolLangs is the orchestrator-supplied per-symbol language
		// tag. Empty means "either go or unknown" — we treat empty as Go
		// because the Go scanner runs first and is the source of every
		// pre-TS-merge symbol.
		lang, hasLang := idx.SymbolLangs[sym.ID]
		if hasLang && lang != "go" {
			continue
		}
		if sym.Signature == "" {
			continue
		}
		if sym.Position.Path == "" {
			continue
		}
		// Skip generated SQLC code and other non-contract artefacts. The
		// codeindex/go scanner already filters most generated/, but the
		// orchestrator may have merged in symbols from a vendored path
		// — be defensive.
		if isGeneratedOrTestPath(sym.Position.Path) {
			continue
		}
		def := ContractDef{
			Name:      simpleFuncName(string(sym.ID)),
			Kind:      KindFunc,
			Language:  LangGo,
			Signature: sym.Signature,
			FilePath:  sym.Position.Path,
			Line:      sym.Position.Line,
			Symbols:   []shared.SymbolID{sym.ID},
			Source:    "go-funcs",
		}
		if fid, ok := annIdx.findFor(sym.Position.Path, sym.Position.Line); ok {
			def.FeatureID = ptrFeatureID(fid)
		}
		defs = append(defs, def)
	}
	return defs, warns
}

// simpleFuncName returns the trailing component of a Go SymbolID.
//   "AuthHandler.Login"   -> "Login"
//   "handlers.NewHandler" -> "NewHandler"
//   "auth.helper"         -> "helper"
//   "handler"             -> "handler"   (no separator)
func simpleFuncName(id string) string {
	if idx := strings.LastIndex(id, "."); idx >= 0 {
		return id[idx+1:]
	}
	return id
}

// isGeneratedOrTestPath returns true for paths under /generated/, /mocks/,
// or that end in _test.go / .gen.go — none of which should appear in a
// contract listing.
func isGeneratedOrTestPath(p string) bool {
	pl := strings.ToLower(p)
	switch {
	case strings.Contains(pl, "/generated/"),
		strings.Contains(pl, "/mocks/"),
		strings.HasSuffix(pl, "_test.go"),
		strings.HasSuffix(pl, ".gen.go"):
		return true
	}
	return false
}

func ptrFeatureID(s shared.FeatureID) *shared.FeatureID { return &s }
