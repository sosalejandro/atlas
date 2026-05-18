package contract

import (
	"strings"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/shared"
)

// extractTSFuncs walks idx.Symbols and emits one KindFunc ContractDef per
// TypeScript symbol that the codeindex/ts scanner classified as a hook,
// component, api-service, endpoint, or generic exported function.
//
// The TS scanner output does NOT carry a fully-typed signature today
// (signature is empty unless the scanner.ts walker upgrades it in a
// future phase). When Signature is empty the extractor still emits a
// record but the Signature column reads "<TS func>" so downstream
// consumers can distinguish a "no-signature TS symbol we know about"
// from a "we never saw this symbol".
//
// Overload handling: the TS scanner emits one Symbol per *named* export,
// not per overload signature. If a TS file exports three overloads of
// the same name, the scanner collapses them to a single Symbol; this
// extractor follows suit (one ContractDef). The trade-off matches the
// scope of Phase 6c — full overload fidelity would require structured
// TypeScript type extraction, which is Phase 8+ territory.
func (e *Extractor) extractTSFuncs(idx *codeindex.Index, annIdx *annotationIndex) ([]ContractDef, []string) {
	if idx == nil {
		return nil, nil
	}
	var defs []ContractDef
	for _, sym := range idx.Symbols {
		lang, hasLang := idx.SymbolLangs[sym.ID]
		if !hasLang || lang != "ts" {
			continue
		}
		if sym.Position.Path == "" {
			continue
		}
		if !isContractWorthyTSKind(sym.Kind) {
			continue
		}
		sig := sym.Signature
		if sig == "" {
			// Fallback so the Signature column never empty — keeps
			// golden output readable + avoids tooling treating "" as
			// "missing field".
			sig = "<ts func>"
		}
		def := ContractDef{
			Name:      tsSymbolDisplayName(string(sym.ID)),
			Kind:      KindFunc,
			Language:  LangTS,
			Signature: sig,
			FilePath:  sym.Position.Path,
			Line:      sym.Position.Line,
			Symbols:   []shared.SymbolID{sym.ID},
			Source:    "ts-funcs",
		}
		if fid, ok := annIdx.findFor(sym.Position.Path, sym.Position.Line); ok {
			def.FeatureID = ptrFeatureID(fid)
		}
		defs = append(defs, def)
	}
	return defs, nil
}

// isContractWorthyTSKind returns true for the subset of TS symbol kinds
// that are useful as standalone contracts. Components and routes are
// emitted because pages-as-contracts is a useful frontend audit; raw
// "ts variable" nodes (e.g. constants) are not.
func isContractWorthyTSKind(k shared.SymbolKind) bool {
	switch k {
	case shared.KindHook,
		shared.KindComponent,
		shared.KindService,
		shared.KindEndpoint,
		shared.KindFunc,
		shared.KindMethod:
		return true
	}
	return false
}

// tsSymbolDisplayName returns the human-readable trailing name of a TS
// SymbolID. The TS scanner uses "<file>::<exported-name>" so we just
// take everything after the LAST "::".
//
// Robust to IDs without the separator (legacy or future shapes).
func tsSymbolDisplayName(id string) string {
	if idx := strings.LastIndex(id, "::"); idx >= 0 {
		return id[idx+2:]
	}
	if idx := strings.LastIndex(id, "."); idx >= 0 {
		return id[idx+1:]
	}
	return id
}
