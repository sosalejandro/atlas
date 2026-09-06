package doctor

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sosalejandro/atlas/packages/store"
)

// indexFreshness compares `file_hashes` against the working tree.
//
// This is the load-bearing check. Everything else atlas prints -- a
// coverage percentage, an audit score, a sprint ranking -- is computed
// over symbols that came out of a scan, so an index that has drifted
// makes every one of those numbers a confident statement about a repo
// that no longer exists. Nothing else in atlas notices: a stale row is a
// valid row.
type indexFreshness struct{}

func (indexFreshness) Name() string { return "index.freshness" }

func (indexFreshness) Examines() string {
	return "the recorded file index against the files on disk"
}

func (c indexFreshness) Run(ctx context.Context, env *Env) (Result, error) {
	if res, ok := env.requireStore(); !ok {
		return res, nil
	}
	rows, err := env.Store.FileHashes().List(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("doctor index: list file hashes: %w", err)
	}
	if len(rows) == 0 {
		return c.emptyIndex(ctx, env)
	}

	drift := compareHashes(env.Root, rows)
	unindexed, err := unindexedSources(ctx, env, rows)
	if err != nil {
		return Result{}, err
	}

	details := map[string]any{
		"indexed_files":   len(rows),
		"missing":         len(drift.missing),
		"changed":         len(drift.changed),
		"unreadable":      len(drift.unreadable),
		"unindexed":       len(unindexed),
		"missing_files":   samples(drift.missing),
		"changed_files":   samples(drift.changed),
		"unindexed_files": samples(unindexed),
	}
	if len(drift.unreadable) > 0 {
		details["unreadable_files"] = samples(drift.unreadable)
	}

	// missing and changed are statements atlas has already made about
	// content it can no longer see; unindexed is only content it has not
	// seen yet. The first pair invalidates existing answers, the second
	// merely bounds them, so only the first pair fails.
	switch {
	case len(drift.missing)+len(drift.changed) > 0:
		return Result{
			Severity: SeverityFail,
			Finding: fmt.Sprintf(
				"the index is stale: of %d indexed files, %d changed on disk and %d no longer exist",
				len(rows), len(drift.changed), len(drift.missing)),
			Remediation: "atlas scan",
			Details:     details,
		}, nil
	case len(unindexed) > 0:
		return Result{
			Severity: SeverityWarn,
			Finding: fmt.Sprintf(
				"%d indexed files all match the working tree, but %d source files under %s were never indexed",
				len(rows), len(unindexed), env.Root),
			Remediation: "atlas scan",
			Details:     details,
		}, nil
	default:
		return Result{
			Severity: SeverityOK,
			Finding: fmt.Sprintf("%d indexed files all match the working tree",
				len(rows)),
			Details: details,
		}, nil
	}
}

// emptyIndex splits the two very different states that both present as
// "no file_hashes rows".
//
// A store with symbols but no hashes was scanned with --hash-files=false:
// there is simply nothing to compare the tree against, and the honest
// answer is that freshness cannot be determined. Calling that "ok" would
// be the exact lie this package exists to prevent -- it would report a
// repo as verified when nothing was verified.
func (c indexFreshness) emptyIndex(ctx context.Context, env *Env) (Result, error) {
	syms, err := env.Store.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		return Result{}, fmt.Errorf("doctor index: list symbols: %w", err)
	}
	if len(syms) > 0 {
		return Result{
			Severity: SeverityNotApplicable,
			Finding: fmt.Sprintf(
				"the store holds %d symbols but no file hashes, so freshness cannot be determined "+
					"(the last scan ran with --hash-files=false)", len(syms)),
			Remediation: "atlas scan --hash-files",
			Details:     map[string]any{"indexed_files": 0, "symbols": len(syms)},
		}, nil
	}
	return Result{
		Severity: SeverityFail,
		Finding: fmt.Sprintf("the store holds no index of %s at all: "+
			"every number atlas prints would be computed over nothing", env.Root),
		Remediation: "atlas init",
		Details:     map[string]any{"indexed_files": 0, "symbols": 0},
	}, nil
}

// hashDrift is the per-row verdict of comparing the index to disk.
type hashDrift struct {
	missing    []string // recorded, but gone from the working tree
	changed    []string // still there, different bytes
	unreadable []string // still there, could not be read
}

// compareHashes re-hashes every indexed file. It reads the whole tree's
// worth of indexed content, which is the same work `atlas scan` does to
// decide what to skip -- doctor being as expensive as one scan is the
// price of an answer that is not itself a guess (an mtime comparison
// would be cheaper and would miss a restored-then-edited file).
func compareHashes(root string, rows []store.FileHashRow) hashDrift {
	var d hashDrift
	for _, row := range rows {
		abs := filepath.Join(root, filepath.FromSlash(row.FilePath))
		sum, err := sha256File(abs)
		switch {
		case os.IsNotExist(err):
			d.missing = append(d.missing, row.FilePath)
		case err != nil:
			d.unreadable = append(d.unreadable, row.FilePath)
		case sum != row.ContentHash:
			d.changed = append(d.changed, row.FilePath)
		}
	}
	return d
}

func sha256File(abs string) (string, error) {
	f, err := os.Open(abs)
	if err != nil {
		return "", err //nolint:wrapcheck // the caller branches on os.IsNotExist.
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("doctor: hash %s: %w", abs, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// unindexedSources walks the tree for Go files atlas has no record of.
//
// Restricted to .go on purpose. codeindex hashes every .go file it walks
// but hashes a .ts / .py file only when that file carries an annotation,
// so a perfectly well-indexed TypeScript file legitimately has no
// file_hashes row. Counting those as unindexed would report a wall of
// non-problems, and a check that cries wolf is a check that gets muted.
// The cost is that this bucket is Go-only; the finding and the docs say
// so rather than letting a reader assume otherwise.
//
// A file with no hash row but with indexed symbols was scanned -- only
// its hash is missing -- so it is not counted either.
func unindexedSources(ctx context.Context, env *Env, rows []store.FileHashRow) ([]string, error) {
	known := make(map[string]bool, len(rows))
	for _, r := range rows {
		known[r.FilePath] = true
	}
	syms, err := env.Store.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		return nil, fmt.Errorf("doctor index: list symbols: %w", err)
	}
	for _, s := range syms {
		known[s.FilePath] = true
	}

	skip := make(map[string]bool, len(env.SkipDirs))
	for _, d := range env.SkipDirs {
		skip[d] = true
	}
	// vendor/ and node_modules/ are excluded by the scanner itself, not by
	// config, so doctor excludes them unconditionally too.
	skip["vendor"] = true
	skip["node_modules"] = true

	var out []string
	walkErr := filepath.WalkDir(env.Root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable subtree is not evidence about the index.
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if skip[d.Name()] || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		rel, relErr := filepath.Rel(env.Root, abs)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if known[rel] || looksGenerated(abs, rel, env.GeneratedGlobs) {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("doctor index: walk %s: %w", env.Root, walkErr)
	}
	return out, nil
}

// generatedHeaderRe is Go's own marker for machine-written source
// (https://go.dev/s/generatedcode).
var generatedHeaderRe = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// maxHeaderLines bounds the header probe, matching the scanner's.
const maxHeaderLines = 32

// looksGenerated re-applies the three exclusion rules
// codeindex/go/scanner.go uses (header marker, configured glob,
// `generated` path segment) so that files the scanner declined by policy
// are not reported as gaps in the index.
//
// This is a deliberate duplicate of unexported scanner logic, and it can
// drift from it. That drift is bounded on purpose: a rule doctor gets
// wrong can only add or remove entries from the `unindexed` bucket, which
// is warn-only and never fails a build. Widening the scanner's API for a
// diagnostic would trade that bounded inaccuracy for a permanent coupling.
func looksGenerated(abs, rel string, globs []string) bool {
	if hasGeneratedHeader(abs) {
		return true
	}
	for _, g := range globs {
		if matchGeneratedGlob(g, rel) {
			return true
		}
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "generated" {
			return true
		}
	}
	return false
}

func hasGeneratedHeader(abs string) bool {
	f, err := os.Open(abs)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for i := 0; i < maxHeaderLines && sc.Scan(); i++ {
		line := sc.Text()
		if strings.HasPrefix(line, "package ") {
			return false
		}
		if generatedHeaderRe.MatchString(line) {
			return true
		}
	}
	return false
}

// matchGeneratedGlob mirrors the scanner's glob semantics: a trailing "/"
// names a directory anywhere in the tree, a leading "**/" is stripped, and
// a pattern with no "/" in it is a filename convention that matches at any
// depth.
func matchGeneratedGlob(pattern, rel string) bool {
	if pattern == "" {
		return false
	}
	if dir := strings.TrimSuffix(pattern, "/"); dir != pattern {
		return rel == dir ||
			strings.HasPrefix(rel, dir+"/") ||
			strings.Contains(rel, "/"+dir+"/")
	}
	pattern = strings.TrimPrefix(pattern, "**/")
	if ok, err := path.Match(pattern, rel); err == nil && ok {
		return true
	}
	if strings.Contains(pattern, "/") {
		return false
	}
	ok, err := path.Match(pattern, path.Base(rel))
	return err == nil && ok
}
