package codeindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sosalejandro/atlas/packages/codeindex/annotations"
	goscan "github.com/sosalejandro/atlas/packages/codeindex/go"
	tsscan "github.com/sosalejandro/atlas/packages/codeindex/ts"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// Index is the merged output of a single IndexProject run.
//
// It carries everything Phase 4's SQLite store needs to populate its
// schema_v1 tables (docs/schema-v1.md §5):
//
//   - Graph         → symbols + edges tables
//   - Annotations   → annotations + (after resolution) feature_symbols
//   - FileHashes    → file_hashes table
//   - Symbols       → denormalised view of Graph.Nodes
//   - SymbolLangs   → per-symbol language tag (go|ts), so the trace verb
//     can label chain hops without re-parsing the file extension. Populated
//     by both the Go and TS sub-scanners.
//   - Warnings      → surfaced by `atlas scan` to stderr
type Index struct {
	Root        string                          `json:"root"`
	GeneratedAt time.Time                       `json:"generated_at"`
	Graph       *graph.Graph                    `json:"graph"`
	Symbols     []shared.Symbol                 `json:"symbols"`
	Annotations []shared.Annotation             `json:"annotations"`
	FileHashes  map[string]FileHash             `json:"file_hashes"`
	SymbolLangs map[shared.SymbolID]string      `json:"symbol_langs,omitempty"`
	Warnings    []string                        `json:"warnings,omitempty"`
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

	// AnnotationExts lists the file extensions the annotations sub-scanner
	// should walk. Defaults to .go, .ts, .tsx, .js, .jsx, .py, .md.
	AnnotationExts []string

	// SkipDirs lists directory names to skip during the annotation file
	// walk (in addition to the always-skipped vendor/, node_modules/, and
	// hidden directories).
	SkipDirs []string

	// HashFiles, when true, computes a SHA-256 of every annotation-bearing
	// or Go source file scanned. Disabled by default in tests; the future
	// `atlas scan` CLI defaults this to true.
	HashFiles bool

	// Logger receives orchestration-level warnings. Defaults to NopLogger.
	Logger shared.Logger
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
		Root:        abs,
		GeneratedAt: time.Now().UTC(),
		FileHashes:  make(map[string]FileHash),
		SymbolLangs: make(map[shared.SymbolID]string),
	}

	// Phase A: Go AST scan.
	goRes, err := goscan.Scan(ctx, abs, opts.GoOptions)
	if err != nil {
		return nil, fmt.Errorf("go scan: %w", err)
	}
	idx.Graph = goRes.Graph
	idx.Symbols = goRes.Symbols
	idx.Warnings = append(idx.Warnings, goRes.Warnings...)
	for _, sym := range goRes.Symbols {
		idx.SymbolLangs[sym.ID] = "go"
	}

	// Phase B: Annotations walk across all supported languages.
	anns, hashes, walkErr := walkAnnotations(ctx, abs, opts, skipDirs)
	if walkErr != nil {
		return nil, fmt.Errorf("annotation walk: %w", walkErr)
	}
	idx.Annotations = anns
	for k, v := range hashes {
		idx.FileHashes[k] = v
	}

	// Phase C: TS AST scan via Node subprocess. Auto-skipped when the
	// project has no TS surface (no tsconfig.json + no package.json) or
	// when SkipTS is set; degrades to a warning if Node isn't on PATH.
	if !opts.SkipTS && projectHasTS(abs) {
		tsOpts := opts.TSOptions
		if tsOpts.Logger == nil {
			tsOpts.Logger = opts.Logger
		}
		tsScanner := tsscan.NewScanner(tsOpts)
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

	return idx, nil
}

// projectHasTS returns true if rootDir looks like it might contain
// TypeScript — i.e. has either a tsconfig.json or a package.json with the
// typescript dep, or an apps/ or packages/ subdir (monorepo). This is the
// cheap pre-check that avoids spinning up Node when there's nothing to
// scan.
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
	return false
}

// mergeTSResult folds a tsscan.Result into the orchestrator's Index. Symbol
// IDs that already exist in the Go graph are NOT overwritten — the Go
// scanner wins (it has stronger guarantees about source-of-truth for
// backend endpoints). Symbols that are new are appended to both
// idx.Graph.Nodes and the denormalised idx.Symbols list.
//
// Edges are appended verbatim. This may create cross-language edges
// (TS hook → endpoint → Go handler) which is exactly what `atlas trace`
// needs to render a frontend-to-backend chain.
func mergeTSResult(idx *Index, res *tsscan.Result) {
	for _, sym := range res.Symbols {
		if _, exists := idx.Graph.Nodes[sym.ID]; exists {
			// Skip duplicate IDs but record the lang for any node the Go
			// scanner emitted as a placeholder (Position.Path == "").
			if existing := idx.Graph.Nodes[sym.ID]; existing.Position.Path == "" && sym.Position.Path != "" {
				existing.Symbol = sym
				idx.SymbolLangs[sym.ID] = "ts"
			}
			continue
		}
		node := &graph.Node{Symbol: sym}
		idx.Graph.AddNode(node)
		idx.Symbols = append(idx.Symbols, sym)
		idx.SymbolLangs[sym.ID] = "ts"
	}
	for _, e := range res.Edges {
		idx.Graph.AddEdge(e.From, e.To)
	}
	// Surface TS scanner warnings to the orchestrator output.
	idx.Warnings = append(idx.Warnings, res.Warnings...)
}

func walkAnnotations(ctx context.Context, rootAbs string, opts Options, skipDirs map[string]bool) ([]shared.Annotation, map[string]FileHash, error) {
	extSet := make(map[string]bool, len(opts.AnnotationExts))
	for _, e := range opts.AnnotationExts {
		extSet[strings.ToLower(e)] = true
	}

	var out []shared.Annotation
	hashes := make(map[string]FileHash)

	err := filepath.WalkDir(rootAbs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			name := d.Name()
			if skipDirs[name] || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !extSet[ext] {
			return nil
		}
		relPath, _ := filepath.Rel(rootAbs, path)
		relPath = filepath.ToSlash(relPath)

		anns, err := annotations.ParseRelative(ctx, path, relPath)
		if err != nil {
			opts.Logger.Warn(ctx, "annotation parse failed", "path", relPath, "err", err)
			return nil
		}
		if len(anns) > 0 {
			out = append(out, anns...)
		}
		if opts.HashFiles && (len(anns) > 0 || ext == ".go") {
			if fh, hashErr := hashFile(path, relPath); hashErr == nil {
				hashes[relPath] = fh
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return out, hashes, nil
}

func hashFile(absPath, relPath string) (FileHash, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return FileHash{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return FileHash{}, err
	}
	info, err := f.Stat()
	if err != nil {
		return FileHash{}, err
	}
	return FileHash{
		Path:        relPath,
		SHA256:      hex.EncodeToString(h.Sum(nil)),
		ModTime:     info.ModTime().UTC(),
		LastScanned: time.Now().UTC(),
	}, nil
}
