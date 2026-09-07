package codeindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	goscan "github.com/sosalejandro/atlas/packages/codeindex/go"
	"github.com/sosalejandro/atlas/packages/codeindex/patterns"
	pyscan "github.com/sosalejandro/atlas/packages/codeindex/py"
	tsscan "github.com/sosalejandro/atlas/packages/codeindex/ts"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// Index is the merged output of a single IndexProject run.
//
// It carries everything Phase 4's SQLite store needs to populate its
// schema_v1 tables (docs/schema-v1.md §5):
//
//   - Graph          → symbols + edges tables
//   - Annotations    → annotations + (after resolution) feature_symbols
//   - FileHashes     → file_hashes table
//   - Symbols        → denormalised view of Graph.Nodes
//   - SymbolLangs    → per-symbol language tag (go|ts), so the trace verb
//     can label chain hops without re-parsing the file extension. Populated
//     by both the Go and TS sub-scanners.
//   - PatternMatches → per-symbol parser-based EDA pattern hits (Phase 6f).
//     Keyed by SymbolID; values are the recogniser hits. Empty when
//     Options.SkipPatternRecognizers is true.
//   - SkippedFiles   → the Go sub-scanner's exclusion ledger (generated
//     code, ignored packages). Carried through so `doctor` / `cov sync`
//     can attribute otherwise-unexplained executed statements to policy
//     rather than reporting them as unattributable.
//   - Warnings       → surfaced by `atlas scan` to stderr
type Index struct {
	Root           string                               `json:"root"`
	GeneratedAt    time.Time                            `json:"generated_at"`
	Graph          *graph.Graph                         `json:"graph"`
	Symbols        []shared.Symbol                      `json:"symbols"`
	Annotations    []shared.Annotation                  `json:"annotations"`
	FileHashes     map[string]FileHash                  `json:"file_hashes"`
	SymbolLangs    map[shared.SymbolID]string           `json:"symbol_langs,omitempty"`
	PatternMatches map[shared.SymbolID][]patterns.Match `json:"pattern_matches,omitempty"`
	SkippedFiles   []goscan.SkippedFile                 `json:"skipped_files,omitempty"`
	Warnings       []string                             `json:"warnings,omitempty"`
}

// FileHash is the per-file record fed into the future
// docs/schema-v1.md §5.7 file_hashes table.
type FileHash struct {
	Path        string    `json:"path"`         // repo-relative
	SHA256      string    `json:"sha256"`       // hex digest
	ModTime     time.Time `json:"mtime"`        // file mtime at scan
	LastScanned time.Time `json:"last_scanned"` // wallclock at scan
}

// Options configures IndexProject.
type Options struct {
	// GoOptions is the goscan.Options forwarded to the Go sub-scanner.
	// Zero value means "scan everything under rootDir with default rules".
	GoOptions goscan.Options

	// TSOptions is the tsscan.Options forwarded to the TS sub-scanner.
	// The TS scanner is invoked when the project has a tsconfig.json or
	// package.json. If `node` isn't on PATH, the TS scan is skipped
	// gracefully (a single warning is appended to Index.Warnings; the Go
	// sub-scanner still runs and the index is still returned).
	TSOptions tsscan.Options

	// SkipTS disables the TS sub-scanner unconditionally. Use this in
	// pure-backend audits where running Node would just add latency. When
	// false (the default) the orchestrator auto-detects TS presence and
	// runs the scanner only if it finds something to do.
	SkipTS bool

	// PYOptions is the pyscan.Options forwarded to the Python sub-scanner.
	// The Python scanner is invoked when the project contains any .py
	// file. If `python3` isn't on PATH, the Python scan is skipped
	// gracefully (a single warning is appended to Index.Warnings; the Go
	// + TS sub-scanners still run and the index is still returned).
	PYOptions pyscan.Options

	// SkipPY disables the Python sub-scanner unconditionally. Use this in
	// pure-Go-or-TS audits where running python3 would just add latency.
	// When false (the default) the orchestrator auto-detects Python
	// presence and runs the scanner only if it finds something to do.
	SkipPY bool

	// AnnotationExts lists the file extensions the annotations sub-scanner
	// should walk. Defaults to .go, .ts, .tsx, .js, .jsx, .py, .md.
	AnnotationExts []string

	// SkipDirs lists directory names to skip during the annotation file
	// walk (in addition to the always-skipped vendor/, node_modules/, and
	// hidden directories).
	SkipDirs []string

	// IncludeNestedRepos indexes git repositories nested inside the scan
	// root -- clones, submodules, worktrees -- as part of this codebase.
	//
	// The default (false) is not a performance choice. A nested repository's
	// files cannot be covered by this repo's test run, so counting them
	// dilutes every coverage denominator, and its annotations materialise
	// features here that belong to another product. See nestedrepo.go.
	// Teams who consider a submodule part of their product can set this.
	IncludeNestedRepos bool

	// HashFiles, when true, computes a SHA-256 of every annotation-bearing
	// or Go source file scanned. Disabled by default in tests; the future
	// `atlas scan` CLI defaults this to true.
	HashFiles bool

	// SkipPatternRecognizers disables the Phase 6f parser-based EDA pattern
	// recognisers. Use this for pure-Go scans where the audit/diagnose
	// pipeline doesn't care about EDA shape findings (saves an extra Go
	// parse per file). When false (the default) every Go file is re-parsed
	// once and the resulting matches are stored on Index.PatternMatches.
	SkipPatternRecognizers bool

	// PatternConfig tunes the codeindex/patterns recognisers (UoW method
	// names, EventRecorder type names, etc). Zero value uses the canonical
	// nutrition-v2-go defaults.
	PatternConfig patterns.Config

	// Logger receives orchestration-level warnings. Defaults to NopLogger.
	Logger shared.Logger

	// Jobs bounds the worker count for the orchestrator's per-file passes —
	// the pattern recognisers and the annotation/hash walk. Zero or negative
	// means GOMAXPROCS; 1 means strictly serial.
	//
	// GOMAXPROCS rather than a constant because the right number is a
	// property of the machine and not of this code, and 1 is kept a
	// first-class value rather than "a pool that happens to have one
	// worker": the determinism suite runs at Jobs=1 and diffs the result
	// against every other setting, so the serial path has to be a path
	// somebody actually takes. See packages/codeindex/parallel.go.
	//
	// It does NOT reach the Go sub-scanner, which is still single-pass —
	// docs/performance.md measures what these passes are worth before
	// anyone assumes raising this fixes a slow scan.
	Jobs int
}

// defaultAnnotationExts matches docs/annotations.md per-language support.
var defaultAnnotationExts = []string{".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".md"}

// IndexProject runs every Phase-1 sub-scanner on rootDir and returns a
// merged Index.
//
// rootDir SHOULD be the project root (the directory containing go.mod /
// package.json). Sub-scanners normalise their paths to repo-relative form
// using this root.
func IndexProject(ctx context.Context, rootDir string, opts Options) (*Index, error) {
	if rootDir == "" {
		return nil, fmt.Errorf("codeindex: rootDir is required")
	}
	abs, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, fmt.Errorf("codeindex: abs rootDir: %w", err)
	}
	if opts.Logger == nil {
		opts.Logger = shared.NopLogger{}
	}
	if len(opts.AnnotationExts) == 0 {
		opts.AnnotationExts = defaultAnnotationExts
	}
	skipDirs := map[string]bool{
		"vendor": true, "node_modules": true,
	}
	for _, d := range opts.SkipDirs {
		skipDirs[d] = true
	}

	idx := &Index{
		Root:           abs,
		GeneratedAt:    time.Now().UTC(),
		FileHashes:     make(map[string]FileHash),
		SymbolLangs:    make(map[shared.SymbolID]string),
		PatternMatches: make(map[shared.SymbolID][]patterns.Match),
	}

	// Phase A: Go AST scan.
	goRes, err := goscan.Scan(ctx, abs, goScanOptions(opts))
	if err != nil {
		return nil, fmt.Errorf("go scan: %w", err)
	}
	mergeGoResult(idx, goRes)

	// Phase A.5: Parser-based EDA pattern recognition (Phase 6f).
	// We re-parse Go files here rather than threading AST handles out of
	// goscan because (a) goscan's funcInfo cache is internal, and (b) the
	// recognisers walk a different shape (struct embeds, closures) than
	// the call-graph builder.
	if !opts.SkipPatternRecognizers {
		excluded := make(map[string]bool, len(idx.SkippedFiles))
		for _, sf := range idx.SkippedFiles {
			excluded[sf.Path] = true
		}
		patMatches, patWarnings := runPatternRecognizers(ctx, abs, opts, excluded)
		for sym, ms := range patMatches {
			idx.PatternMatches[sym] = ms
		}
		idx.Warnings = append(idx.Warnings, patWarnings...)
	}

	// Phase B: Annotations walk across all supported languages.
	if err := runAnnotationPhase(ctx, abs, opts, skipDirs, idx); err != nil {
		return nil, err
	}

	// Phase C: TS AST scan via Node subprocess. Auto-skipped when the
	// project has no TS surface (no tsconfig.json + no package.json) or
	// when SkipTS is set; degrades to a warning if Node isn't on PATH.
	if !opts.SkipTS && projectHasTS(abs) {
		tsOpts := opts.TSOptions
		tsOpts.IncludeNestedRepos = opts.IncludeNestedRepos
		if tsOpts.Logger == nil {
			tsOpts.Logger = opts.Logger
		}
		tsScanner := tsscan.NewScanner(tsOpts)
		// Release the extracted scanner.ts tempdir (+ bridged typescript
		// copy, if any) before IndexProject returns. Without this, every
		// invocation leaks ~50MB on the bridge-copy fallback path.
		defer func() {
			if cerr := tsScanner.Close(); cerr != nil {
				idx.Warnings = append(idx.Warnings,
					fmt.Sprintf("ts scanner close: %v", cerr))
			}
		}()
		tsRes, tsErr := tsScanner.Scan(ctx, abs)
		if tsErr != nil {
			// Surface as warning rather than fatal — TS scan must never
			// block Go-only audits. Real failures (corrupt scanner.ts,
			// node crash) still surface via the warning.
			idx.Warnings = append(idx.Warnings,
				fmt.Sprintf("ts scan: %v", tsErr))
		} else if tsRes != nil {
			mergeTSResult(idx, tsRes)
		}
	}

	// Phase D: Python AST scan via python3 subprocess. Auto-skipped when
	// the project has no .py files or when SkipPY is set; degrades to a
	// warning if python3 isn't on PATH (same contract as Phase C).
	runPythonScanPhase(ctx, abs, skipDirs, opts, idx)

	return idx, nil
}

// runPythonScanPhase encapsulates Phase D (Python AST sub-scan) so the
// IndexProject body stays under the funlen ceiling. Same auto-skip
// semantics as Phase C: if SkipPY is set OR no .py files live under abs,
// the phase is a no-op; if python3 is missing the sub-scanner returns a
// warning rather than an error.
//
// idx is mutated in place: discovered symbols / edges are appended via
// mergePYResult, and any sub-scan failure surfaces as a warning.
func runPythonScanPhase(
	ctx context.Context,
	abs string,
	skipDirs map[string]bool,
	opts Options,
	idx *Index,
) {
	if opts.SkipPY || !projectHasPY(abs, skipDirs) {
		return
	}
	pyOpts := opts.PYOptions
	if pyOpts.Logger == nil {
		pyOpts.Logger = opts.Logger
	}
	pyOpts.IncludeNestedRepos = opts.IncludeNestedRepos
	pyScanner := pyscan.NewScanner(pyOpts)
	defer func() {
		if cerr := pyScanner.Close(); cerr != nil {
			idx.Warnings = append(idx.Warnings,
				fmt.Sprintf("py scanner close: %v", cerr))
		}
	}()
	pyRes, pyErr := pyScanner.Scan(ctx, abs)
	if pyErr != nil {
		idx.Warnings = append(idx.Warnings, fmt.Sprintf("py scan: %v", pyErr))
		return
	}
	if pyRes != nil {
		mergePYResult(idx, pyRes)
	}
}

// projectHasTS returns true if rootDir looks like it might contain
// TypeScript. Two layers:
//
//  1. Cheap manifest probes (tsconfig.json, package.json, apps/, packages/).
//     A hit here covers every well-formed JS/TS project and short-circuits
//     before any filesystem walk.
//  2. Fallback file walk (.ts/.tsx/.mts/.cts) for the long tail — single-file
//     fixtures, partial monorepos, repos where someone dropped a stray .ts
//     file alongside Go code. Pre-fix this case silently bypassed the TS
//     scanner entirely (no warning, no symbols) — see
//     sosalejandro/atlas-internal#19 for the Minca-AI bug report.
//
// The walk honours node_modules / vendor / .git / hidden-dir skips so a
// pure-Go project with deps doesn't pay the cost of descending into a 100k-
// file dependency tree just to confirm "no TS here".
func projectHasTS(rootDir string) bool {
	probes := []string{
		"tsconfig.json",
		"package.json",
		"apps",
		"packages",
	}
	for _, p := range probes {
		if _, err := os.Stat(filepath.Join(rootDir, p)); err == nil {
			return true
		}
	}
	// Fallback: walk for at least one .ts/.tsx file. Stops at the first hit
	// so the cost is O(directories until the first TS file) — pure-Go
	// projects pay only the walk startup, not a full tree sweep.
	return hasAnyTSFile(rootDir)
}

// hasAnyTSFile walks rootDir and returns true on the first .ts/.tsx/.mts/.cts
// it finds (excluding .d.ts declarations, which appear in dependency trees
// even for pure-Go projects with `@types/*` installed). Skips the standard
// dependency / hidden directories.
func hasAnyTSFile(rootDir string) bool {
	skip := map[string]bool{
		"node_modules": true,
		"vendor":       true,
		"dist":         true,
		"build":        true,
		"out":          true,
		".next":        true,
		".turbo":       true,
	}
	found := false
	_ = filepath.WalkDir(rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if path != rootDir && (skip[name] || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, ".d.ts") {
			return nil
		}
		if strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".tsx") ||
			strings.HasSuffix(name, ".mts") || strings.HasSuffix(name, ".cts") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// projectHasPY returns true if rootDir contains at least one .py file.
// This is the cheap pre-check that avoids spinning up python3 when
// there's nothing to scan. The walk honours skipDirs so vendor/,
// node_modules/, and any user-configured deny-list don't trigger a
// false positive.
//
// We short-circuit on the first hit so the cost is O(directories until
// the first .py file) — pure-Go projects pay only the filesystem-walk
// startup, not the full project sweep.
func projectHasPY(rootDir string, skipDirs map[string]bool) bool {
	found := false
	_ = filepath.WalkDir(rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if path != rootDir && (skipDirs[name] || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".py") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// mergeGoResult seeds the Index from the Go sub-scan. The Go scanner runs
// first and owns the graph outright, so this is assignment rather than the
// conflict-resolving merge the TS and Python phases need.
//
// SkippedFiles rides along because the files the scanner declined to index
// are exactly the files whose executed statements would otherwise surface
// as unattributable coverage.
// runAnnotationPhase walks the tree for annotations and file hashes and
// folds the result into idx, including the note about any nested repository
// the walk declined to enter.
func runAnnotationPhase(
	ctx context.Context,
	abs string,
	opts Options,
	skipDirs map[string]bool,
	idx *Index,
) error {
	anns, hashes, nestedWarnings, err := walkAnnotations(ctx, abs, opts, skipDirs)
	if err != nil {
		return fmt.Errorf("annotation walk: %w", err)
	}
	idx.Annotations = anns
	for k, v := range hashes {
		idx.FileHashes[k] = v
	}
	idx.Warnings = append(idx.Warnings, nestedWarnings...)
	return nil
}

func mergeGoResult(idx *Index, goRes *goscan.Result) {
	idx.Graph = goRes.Graph
	idx.Symbols = goRes.Symbols
	idx.SkippedFiles = goRes.SkippedFiles
	idx.Warnings = append(idx.Warnings, goRes.Warnings...)
	for _, sym := range goRes.Symbols {
		idx.SymbolLangs[sym.ID] = "go"
	}
}

// mergeTSResult folds a tsscan.Result into the orchestrator's Index. Symbol
// IDs that already exist in the Go graph are NOT overwritten — the Go
// scanner wins (it has stronger guarantees about source-of-truth for
// backend endpoints). Symbols that are new are appended to both
// idx.Graph.Nodes and the denormalised idx.Symbols list.
//
// Edges are appended verbatim. This may create cross-language edges
// (TS hook → endpoint → Go handler) which is exactly what `atlas chain`
// needs to render a frontend-to-backend chain.
func mergeTSResult(idx *Index, res *tsscan.Result) {
	for _, sym := range res.Symbols {
		if _, exists := idx.Graph.Nodes[sym.ID]; exists {
			// Skip duplicate IDs but record the lang for any node the Go
			// scanner emitted as a placeholder (Position.Path == "").
			if existing := idx.Graph.Nodes[sym.ID]; existing.Position.Path == "" && sym.Position.Path != "" {
				existing.Symbol = sym
				idx.SymbolLangs[sym.ID] = "ts"
				// Keep idx.Symbols (denormalised view of Graph.Nodes) in sync
				// with the upgraded node. Linear scan by ID — placeholder
				// upgrades are rare so the cost is fine.
				for i := range idx.Symbols {
					if idx.Symbols[i].ID == sym.ID {
						idx.Symbols[i] = sym
						break
					}
				}
			}
			continue
		}
		node := &graph.Node{Symbol: sym}
		idx.Graph.AddNode(node)
		idx.Symbols = append(idx.Symbols, sym)
		idx.SymbolLangs[sym.ID] = "ts"
	}
	for _, e := range res.Edges {
		// Use the line-aware overload to forward the per-edge line
		// when the TS scanner supplies one. Today scanner.ts emits
		// Line=0 so this is functionally identical to AddEdgeKind;
		// staying on the line-aware overload keeps the ingest path
		// uniform and future-proofs the TS scanner's eventual
		// per-edge anchors.
		//
		// e.Tier is forwarded rather than chosen: the orchestrator has
		// no idea how the TS scanner resolved anything, and a tier
		// picked here would be a claim about work it did not watch
		// (issue #146). An unset one fails at ingest, by name.
		idx.Graph.AddEdgeKindLineTier(e.From, e.To, e.Kind, e.Line, e.Tier)
	}
	// Surface TS scanner warnings to the orchestrator output.
	idx.Warnings = append(idx.Warnings, res.Warnings...)
}

// mergePYResult folds a pyscan.Result into the orchestrator's Index.
// Symbol IDs that already exist in the Go or TS graph are NOT
// overwritten — earlier scanners win because they have stronger
// guarantees about source-of-truth for symbols in their own language.
// New symbols are appended to both idx.Graph.Nodes and the denormalised
// idx.Symbols list, tagged with SymbolLangs["py"] for cross-language
// `atlas chain`.
//
// Edges are appended verbatim, which may create cross-language edges
// (a Python integration calling out to a Go binary via subprocess, for
// example) — those are exactly what trace needs to render polyglot
// chains.
//
// Annotations surfaced by the AST walker — decorator-form hits and
// class-level propagation records — are appended to idx.Annotations so
// the standard ingest/materialise pipeline links them to
// feature_symbols. Comment-form hits also surface here, but the
// file-walker pass in walkAnnotations sees the same lines independently;
// the store's idempotent feature_symbols upsert collapses any
// duplicates so dual emission is harmless.
func mergePYResult(idx *Index, res *pyscan.Result) {
	for _, sym := range res.Symbols {
		if _, exists := idx.Graph.Nodes[sym.ID]; exists {
			continue
		}
		node := &graph.Node{Symbol: sym}
		idx.Graph.AddNode(node)
		idx.Symbols = append(idx.Symbols, sym)
		idx.SymbolLangs[sym.ID] = "py"
	}
	for _, e := range res.Edges {
		// Preserve both the per-edge line (issue #17, PR #68) and
		// the kind-specific Meta qualifier (issue #16) emitted by
		// scanner.py. Without AddEdgeKindLineMeta either signal
		// gets dropped here and the store-side ingest persists a
		// less-faithful row — line falls back to the from-symbol's
		// declaration line (always 1 for module-level imports) and
		// Meta lands as NULL even when scanner.py supplied a scope
		// tag. Empty Line/Meta on the call site means "back-compat
		// with pre-fix producers" — the graph layer treats them as
		// the zero value.
		//
		// e.Tier joins them for the same reason and with the opposite
		// tolerance: an empty tier is NOT back-compat, it is an edge
		// whose producer never said how it resolved the target, and
		// the store refuses it rather than filing it beside the ones
		// that did (issue #146).
		idx.Graph.AddEdgeKindLineMetaTier(e.From, e.To, e.Kind, e.Line, e.Meta, e.Tier)
	}
	idx.Annotations = append(idx.Annotations, res.Annotations...)
	idx.Warnings = append(idx.Warnings, res.Warnings...)
}

// hashBytes builds the incremental cache's record for a file the caller has
// already read, from those bytes and the stat that read had to do anyway.
//
// It replaces a hashFile that opened the file a second time and streamed it
// through io.Copy (issue #156). Two things went with that:
//
//   - The read itself — one of the six a .go file used to cost per scan.
//   - io.Copy's 32 KB staging buffer, allocated fresh per file because
//     neither a *os.File nor a hash.Hash implements the ReaderFrom /
//     WriterTo fast path. That is the 28.4 MB the scan profile attributed
//     to io.copyBuffer, and it was never the hashing; sha256 itself is
//     about 1% of scan time and stays exactly as it was.
//
// The digest must remain byte-identical to indexfresh.hashFile's — SHA-256
// of the file's bytes, hex-encoded — or every file classifies stale forever
// and both incremental callers fall back permanently. sha256.Sum256 over
// the whole slice is the same function as Write-then-Sum over the same
// bytes; nothing about the digest changed here.
func hashBytes(relPath string, content []byte, info fs.FileInfo) FileHash {
	sum := sha256.Sum256(content)
	return FileHash{
		Path:        relPath,
		SHA256:      hex.EncodeToString(sum[:]),
		ModTime:     info.ModTime().UTC(),
		LastScanned: time.Now().UTC(),
	}
}

// EncodePatternMatches serialises a per-symbol slice of Match records to
// the JSON form persisted by store.Symbols.SetPatternMatches. Empty input
// returns "" — callers should treat that as "clear the column" (matching
// the store layer's contract).
//
// This helper lives here (not in patterns/) so the patterns package stays
// pure-AST and free of JSON deps; the orchestrator owns the wire shape.
func EncodePatternMatches(matches []patterns.Match) (string, error) {
	if len(matches) == 0 {
		return "", nil
	}
	b, err := json.Marshal(matches)
	if err != nil {
		return "", fmt.Errorf("encode pattern matches: %w", err)
	}
	return string(b), nil
}
