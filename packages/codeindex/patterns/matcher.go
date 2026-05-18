package patterns

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"sort"

	"github.com/sosalejandro/atlas/packages/shared"
)

// Match is one recogniser hit on a Go source file.
//
// JSON tags match the field names downstream consumers (audit/, diagnose/,
// store/ pattern_matches column) expect — lowerCamel per Atlas v1 contract
// conventions.
type Match struct {
	// Pattern is the recogniser's identifier. Closed enum — extending it is
	// non-breaking, but consumers SHOULD only treat the values listed in
	// KnownPatterns as semantically meaningful.
	Pattern string `json:"pattern"`

	// Symbol is the qualified-name handle of the matched code entity. For
	// outbox-append it is the enclosing function (where the call lives);
	// for event-recorder-embed it is the struct type name; for
	// canonical-service it is the method that owns the UoW closure.
	Symbol shared.SymbolID `json:"symbol"`

	// Position is the source location of the matched construct (repo-rel
	// path + 1-based line). For outbox-append this is the call site; for
	// event-recorder-embed the struct's TypeSpec; for canonical-service
	// the method's FuncDecl.
	Position shared.FilePosition `json:"position"`

	// Detail is a short human-readable description of why the recogniser
	// fired. Surfaced in audit/diagnose report tables — keep it under 80
	// chars and avoid repeating Pattern/Symbol info.
	Detail string `json:"detail,omitempty"`

	// Confidence is the recogniser's self-assessed certainty in 0..1.
	// 1.0 means "exact-shape match"; 0.5 means "matches the heuristic but
	// could be a lookalike". Used by audit/ to weight findings.
	Confidence float64 `json:"confidence"`
}

// Closed enum of recogniser names. Anchoring these here lets consumers
// branch on `switch m.Pattern { case PatternOutboxAppend: ... }` without
// magic strings spreading through the codebase.
const (
	PatternOutboxAppend       = "outbox-append"
	PatternEventRecorderEmbed = "event-recorder-embed"
	PatternCanonicalService   = "canonical-service"
)

// KnownPatterns lists every recogniser this package ships. New recognisers
// MUST be added here so audit/diagnose surface them in their `--pattern`
// flag completions.
var KnownPatterns = []string{
	PatternOutboxAppend,
	PatternEventRecorderEmbed,
	PatternCanonicalService,
}

// Config tunes the recognisers. Zero value is safe — defaults are tuned
// against the nutrition-v2-go canonical service shape.
type Config struct {
	// UoWMethodNames is the closed set of method names treated as the
	// Unit-of-Work entry point in the canonical-service recogniser.
	// Defaults to {"Run"}. Override to {"Run","Do","Execute"} if a project
	// uses a different verb.
	UoWMethodNames []string

	// OutboxAppendMethodNames is the closed set of method names treated
	// as outbox appends. Defaults to {"Append", "AppendFromContext"}.
	// Adding a name here makes BOTH the outbox-append recogniser and the
	// canonical-service recogniser pick the call up.
	OutboxAppendMethodNames []string

	// RepoSaveMethodNames is the closed set of method names treated as
	// repository save calls inside the UoW closure. Defaults to
	// {"Save", "Insert", "Update", "Upsert"}.
	RepoSaveMethodNames []string

	// EventRecorderNames is the closed set of type names treated as the
	// EventRecorder embed marker. Defaults to {"EventRecorder"}.
	EventRecorderNames []string
}

// withDefaults returns a Config with every empty slice filled in.
func (c Config) withDefaults() Config {
	if len(c.UoWMethodNames) == 0 {
		c.UoWMethodNames = []string{"Run"}
	}
	if len(c.OutboxAppendMethodNames) == 0 {
		c.OutboxAppendMethodNames = []string{"Append", "AppendFromContext"}
	}
	if len(c.RepoSaveMethodNames) == 0 {
		c.RepoSaveMethodNames = []string{"Save", "Insert", "Update", "Upsert"}
	}
	if len(c.EventRecorderNames) == 0 {
		c.EventRecorderNames = []string{"EventRecorder"}
	}
	return c
}

func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// FileInput is the per-file payload the matcher consumes. It is the
// orchestrator's job (codeindex.IndexProject) to parse files once and
// hand them to MatchAllFiles; this package never re-parses.
type FileInput struct {
	// File is the parsed AST root.
	File *ast.File
	// FSet is the token.FileSet the File was parsed against. Required so
	// the recogniser can resolve positions.
	FSet *token.FileSet
	// RelPath is the repo-relative file path used in
	// shared.FilePosition.Path. Forward-slash even on Windows.
	RelPath string
}

// MatchAllFiles runs every recogniser over every file and returns the
// merged []Match. Results are stably ordered (by RelPath, Line, Pattern)
// so callers can diff snapshots deterministically.
//
// MatchAllFiles is safe to call with a nil or empty Config — the zero
// value provides the canonical-service heuristic tuned for nutrition-v2-go.
//
// The function honours ctx cancellation between files (cheap check), so
// callers running it across thousands of files can bail early.
func MatchAllFiles(ctx context.Context, cfg Config, files []FileInput) ([]Match, error) {
	cfg = cfg.withDefaults()
	var out []Match
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("patterns: %w", err)
		}
		if f.File == nil || f.FSet == nil {
			continue
		}
		out = append(out, matchOutboxAppend(cfg, f)...)
		out = append(out, matchEventRecorderEmbed(cfg, f)...)
		out = append(out, matchCanonicalService(cfg, f)...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Position.Path != out[j].Position.Path {
			return out[i].Position.Path < out[j].Position.Path
		}
		if out[i].Position.Line != out[j].Position.Line {
			return out[i].Position.Line < out[j].Position.Line
		}
		return out[i].Pattern < out[j].Pattern
	})
	return out, nil
}

// MatchFile is the single-file convenience wrapper. Equivalent to
// MatchAllFiles with a one-element slice.
func MatchFile(ctx context.Context, cfg Config, f FileInput) ([]Match, error) {
	return MatchAllFiles(ctx, cfg, []FileInput{f})
}

// posToFilePosition converts a token.Pos to a repo-relative
// shared.FilePosition. RelPath wins over the AST's internal absolute path
// because shared.FilePosition.Path is contractually repo-relative.
func posToFilePosition(fset *token.FileSet, pos token.Pos, relPath string) shared.FilePosition {
	p := fset.Position(pos)
	path := relPath
	if path == "" {
		path = filepath.ToSlash(p.Filename)
	}
	return shared.FilePosition{
		Path: path,
		Line: p.Line,
		Col:  p.Column,
	}
}

// enclosingFuncSymbol returns the SymbolID-shape of the FuncDecl that
// transitively contains a given AST node. The shape mirrors the Go
// scanner's funcDeclGraphID convention: "ReceiverType.MethodName" for
// methods, "pkg.FuncName" for plain functions. Returns "" if the caller
// is not inside a function (e.g. a struct definition at package scope —
// callers handle that case explicitly).
func enclosingFuncSymbol(file *ast.File, target ast.Node) shared.SymbolID {
	if file == nil || target == nil {
		return ""
	}
	tPos := target.Pos()
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Pos() <= tPos && tPos <= fn.End() {
			return funcDeclSymbol(file, fn)
		}
	}
	return ""
}

// funcDeclSymbol mirrors goscan.funcDeclGraphID — receiver-prefixed for
// methods, package-prefixed for plain functions.
func funcDeclSymbol(file *ast.File, fn *ast.FuncDecl) shared.SymbolID {
	if fn == nil || fn.Name == nil {
		return ""
	}
	if recv := receiverTypeName(fn); recv != "" {
		return shared.SymbolID(recv + "." + fn.Name.Name)
	}
	pkg := ""
	if file != nil && file.Name != nil {
		pkg = file.Name.Name
	}
	return shared.SymbolID(pkg + "." + fn.Name.Name)
}

func receiverTypeName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	t := fn.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	if ident, ok := t.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}
