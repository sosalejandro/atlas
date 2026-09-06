package codeindex

import (
	"context"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/sosalejandro/atlas/packages/codeindex/annotations"
	"github.com/sosalejandro/atlas/packages/codeindex/patterns"
	"github.com/sosalejandro/atlas/packages/shared"
)

// The orchestrator's two per-file passes, parallelised (issue #109).
//
// # What is parallel, and what deliberately is not
//
// IndexProject reads every source file three times: once in the Go
// sub-scanner, once again in the pattern recognisers (a second parse — see
// runPatternRecognizers' godoc for why the two passes cannot share an AST),
// and once more in the annotation walk, which also hashes it. The second
// and third are pure functions of one file's bytes, so they parallelise
// with nothing shared but the result slice.
//
// The Go sub-scanner does not, and is not touched here. Its phase 2 and
// phase 3 write into shared lookup maps and assign symbol ids by walk
// order, and it lives in packages/codeindex/go. Measure before believing
// that is where the money is: on this repository the two passes below are
// 207ms of a 7.28s IndexProject even run serially, and parallelising them
// moves the end-to-end scan by less than this machine's noise.
// docs/performance.md has the full profile, the numbers, and the reason
// this change is kept anyway — peak parser memory, which used to scale
// with the repository and now scales with the worker count.
//
// # Determinism first
//
// The output of a parallel pass must be byte-identical to the serial one,
// always, not usually — issue #120, and the reason the golden corpus
// exists. Two rules make that structural rather than hopeful:
//
//  1. The FILE LIST is built by a single-threaded filepath.WalkDir, so
//     order is fixed before any worker starts. Workers never discover
//     files.
//  2. Each worker writes result[i] for the input it took, and nothing
//     else. There is no shared accumulator, no mutex around an append,
//     and therefore no way for completion order to leak into the output.
//     The merge is a range over result in index order.
//
// Warnings follow the same rule: a worker returns its warning with its
// result instead of appending to a shared slice, so the warning list is
// in walk order too. A ledger whose order drifts is a ledger that diffs
// against itself.

// jobsFor resolves Options.Jobs to a worker count.
//
// The default is GOMAXPROCS rather than a constant because the right
// number is a property of the machine, not of this code: a constant 4 both
// starves a 32-core CI runner and over-subscribes a 2-core container. A
// non-positive value means "you decide"; 1 means strictly serial, which is
// what the determinism suite runs and what --jobs=1 promises. An explicit
// value above GOMAXPROCS is honoured — over-subscription is a reasonable
// thing to want when the pass is I/O bound, and second-guessing the
// operator here would just make the flag a lie.
func jobsFor(n int) int {
	if n > 0 {
		return n
	}
	return runtime.GOMAXPROCS(0)
}

// mapOrdered applies fn to every element of in using at most jobs
// goroutines, and returns the results IN INPUT ORDER.
//
// Order is not restored by a sort at the end; it is never lost. Worker w
// claims index i and writes out[i], so out is assembled by construction
// and two elements are never contended. That is the whole determinism
// argument, and it is why this function exists instead of a channel of
// results that a merge step would have to re-order.
//
// jobs <= 1 runs fn inline on the calling goroutine — no goroutines are
// started at all, so --jobs=1 is the same code path a single-threaded
// build would take rather than a pool of one.
//
// Cancellation is checked per item. A cancelled run leaves the remaining
// slots at their zero value; callers pass a result type whose zero value
// means "nothing", and check ctx themselves for the error.
func mapOrdered[In, Out any](ctx context.Context, jobs int, in []In, fn func(context.Context, In) Out) []Out {
	out := make([]Out, len(in))
	if len(in) == 0 {
		return out
	}
	workers := jobsFor(jobs)
	if workers > len(in) {
		workers = len(in)
	}
	if workers <= 1 {
		for i := range in {
			if ctx.Err() != nil {
				return out
			}
			out[i] = fn(ctx, in[i])
		}
		return out
	}

	var next atomic.Int64
	done := make(chan struct{}, workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(in) || ctx.Err() != nil {
					return
				}
				out[i] = fn(ctx, in[i])
			}
		}()
	}
	for w := 0; w < workers; w++ {
		<-done
	}
	return out
}

// sourceFile is one file the walk selected, carried as both paths because
// every pass needs the absolute one to read and the repo-relative one to
// report.
type sourceFile struct {
	abs string
	rel string
	ext string
}

// listFiles walks rootAbs once, single-threaded, and returns the files
// accept selected in filepath.WalkDir order.
//
// The walk stays serial on purpose. It is a directory-metadata traversal
// that takes single-digit milliseconds on a repository of this size, and
// it is the thing that DEFINES the order every downstream merge relies on;
// parallelising it would trade the ordering guarantee for nothing
// measurable.
func listFiles(
	ctx context.Context,
	rootAbs string,
	skipDir func(name string) bool,
	accept func(name string) bool,
) ([]sourceFile, error) {
	var out []sourceFile
	err := filepath.WalkDir(rootAbs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if cerr := ctx.Err(); cerr != nil {
			return fmt.Errorf("list %s: %w", rootAbs, cerr)
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !accept(d.Name()) {
			return nil
		}
		rel, relErr := filepath.Rel(rootAbs, path)
		if relErr != nil {
			return nil
		}
		out = append(out, sourceFile{
			abs: path,
			rel: filepath.ToSlash(rel),
			ext: strings.ToLower(filepath.Ext(path)),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", rootAbs, err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Pass A.5: parser-based EDA pattern recognition
// ---------------------------------------------------------------------------

// patternFileResult is one worker's whole contribution: the matches for a
// file, or the reason there are none.
type patternFileResult struct {
	matches []patterns.Match
	warning string
}

// runPatternRecognizers parses every non-test .go file under rootAbs and
// runs codeindex/patterns over it, with opts.Jobs workers.
//
// The same skipDirs the annotation walker uses are honoured so vendor/,
// node_modules/, hidden dirs and generated/ trees don't pollute the
// findings, and `excluded` is the Go scanner's own exclusion ledger rather
// than a second opinion about it — re-deriving that rule here is how the
// two passes end up disagreeing about what the codebase contains.
//
// This is an EXTRA parse: goscan already read these files, but its funcInfo
// cache is unexported and the recognisers walk different AST shapes (struct
// embeds, closures) than the call-graph builder.
//
// Each worker parses one file and matches it immediately, then drops the
// AST. The previous shape collected every *ast.File in the tree into one
// slice before matching any of them, which held the entire repository's
// syntax trees live at once; with N workers the ceiling is N trees instead,
// which is what makes raising --jobs safe on a large tree (issue #109's
// "parser memory" risk).
func runPatternRecognizers(
	ctx context.Context,
	rootAbs string,
	opts Options,
	excluded map[string]bool,
) (map[shared.SymbolID][]patterns.Match, []string) {
	matchesBySym := make(map[shared.SymbolID][]patterns.Match)
	var warnings []string

	skip := map[string]bool{"vendor": true, "node_modules": true}
	for _, d := range opts.SkipDirs {
		skip[d] = true
	}

	files, walkErr := listFiles(ctx, rootAbs,
		func(name string) bool { return skip[name] || strings.HasPrefix(name, ".") },
		func(name string) bool {
			return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
		})
	if walkErr != nil {
		warnings = append(warnings, fmt.Sprintf("pattern walk: %v", walkErr))
	}

	todo := files[:0:0]
	for _, f := range files {
		if !excluded[f.rel] {
			todo = append(todo, f)
		}
	}

	cfg := opts.PatternConfig
	results := mapOrdered(ctx, opts.Jobs, todo,
		func(ctx context.Context, f sourceFile) patternFileResult {
			fset := token.NewFileSet()
			file, perr := parser.ParseFile(fset, f.abs, nil, parser.ParseComments)
			if perr != nil {
				return patternFileResult{warning: fmt.Sprintf("pattern parse %s: %v", f.rel, perr)}
			}
			ms, merr := patterns.MatchFile(ctx, cfg, patterns.FileInput{
				File: file, FSet: fset, RelPath: f.rel,
			})
			if merr != nil {
				return patternFileResult{warning: fmt.Sprintf("pattern matchall: %v", merr)}
			}
			return patternFileResult{matches: ms}
		})

	// Merge in walk order, then apply the same (path, line, pattern) order
	// patterns.MatchAllFiles applies to a whole-tree call. Re-sorting is
	// not belt-and-braces: WalkDir orders "a/c.go" before "a-b/x.go"
	// (it compares directory entries, so "a" < "a-b"), while the matcher's
	// contract orders them by path string, where '-' sorts before '/'. Only
	// one of those is the documented output order.
	var all []patterns.Match
	for _, r := range results {
		if r.warning != "" {
			warnings = append(warnings, r.warning)
			continue
		}
		all = append(all, r.matches...)
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Position.Path != all[j].Position.Path {
			return all[i].Position.Path < all[j].Position.Path
		}
		if all[i].Position.Line != all[j].Position.Line {
			return all[i].Position.Line < all[j].Position.Line
		}
		return all[i].Pattern < all[j].Pattern
	})
	for _, m := range all {
		matchesBySym[m.Symbol] = append(matchesBySym[m.Symbol], m)
	}
	return matchesBySym, warnings
}

// ---------------------------------------------------------------------------
// Pass B: annotations + file hashes
// ---------------------------------------------------------------------------

// annotationFileResult is one worker's contribution to the annotation
// walk. hashed is separate from hash's zero value because a file can
// legitimately be hashed to nothing only if it could not be read, and that
// is a miss rather than an empty digest.
type annotationFileResult struct {
	anns   []shared.Annotation
	hash   FileHash
	hashed bool
	warn   error
	rel    string
}

// walkAnnotations reads every file whose extension opts.AnnotationExts
// lists, parses its annotations, and — when opts.HashFiles is set —
// computes its SHA-256, with opts.Jobs workers.
//
// The returned annotation slice is in walk order. That is load-bearing and
// not cosmetic: store.Ingest materialises a feature by looking up the
// symbol at or after the annotation's line, so a reordered slice re-points
// which declaration a feature attaches to without changing a single count.
func walkAnnotations(
	ctx context.Context,
	rootAbs string,
	opts Options,
	skipDirs map[string]bool,
) ([]shared.Annotation, map[string]FileHash, error) {
	extSet := make(map[string]bool, len(opts.AnnotationExts))
	for _, e := range opts.AnnotationExts {
		extSet[strings.ToLower(e)] = true
	}

	files, err := listFiles(ctx, rootAbs,
		func(name string) bool { return skipDirs[name] || strings.HasPrefix(name, ".") },
		func(name string) bool { return extSet[strings.ToLower(filepath.Ext(name))] })
	if err != nil {
		return nil, nil, err
	}

	hashFiles := opts.HashFiles
	results := mapOrdered(ctx, opts.Jobs, files,
		func(ctx context.Context, f sourceFile) annotationFileResult {
			res := annotationFileResult{rel: f.rel}
			anns, perr := annotations.ParseRelative(ctx, f.abs, f.rel)
			if perr != nil {
				res.warn = perr
				return res
			}
			res.anns = anns
			if hashFiles && (len(anns) > 0 || f.ext == ".go") {
				if fh, herr := hashFile(f.abs, f.rel); herr == nil {
					res.hash, res.hashed = fh, true
				}
			}
			return res
		})

	var out []shared.Annotation
	hashes := make(map[string]FileHash, len(files))
	for _, r := range results {
		if r.warn != nil {
			opts.Logger.Warn(ctx, "annotation parse failed", "path", r.rel, "err", r.warn)
			continue
		}
		if len(r.anns) > 0 {
			out = append(out, r.anns...)
		}
		if r.hashed {
			hashes[r.hash.Path] = r.hash
		}
	}
	return out, hashes, nil
}
