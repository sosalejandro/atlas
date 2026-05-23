// Package pyscan is the Atlas Python scanner. It orchestrates the embedded
// scanner.py (a stdlib `ast`-based walker) via a python3 subprocess and
// returns its discoveries in Atlas's canonical shared.Symbol + graph.Edge
// shapes.
//
// Design rules (mirror packages/codeindex/ts/):
//
//   - python3 is OPTIONAL at runtime. If `python3` isn't on PATH the
//     Scanner returns an empty Result with a single warning, never an
//     error. This keeps the Go binary self-contained for users whose
//     projects have no Python.
//   - scanner.py is embedded at build time via //go:embed. We write it to
//     a per-process tempfile on first call and reuse it for the lifetime
//     of the Scanner instance.
//   - All file paths in the Result are repo-relative (forward-slash) per
//     shared.FilePosition rules. The scanner.py walker already emits in
//     that shape; this layer only re-bases when --root is absolute.
//   - Logging via shared.Logger / log/slog. NopLogger is the test default.
//
// Two entry points, mirroring packages/codeindex/ts/:
//
//   - Scan(ctx, root, opts) — package-level convenience for one-shot
//     callers. Internally constructs a Scanner, defers Close, runs once.
//   - NewScanner(opts) + (*Scanner).Scan + (*Scanner).Close — for
//     long-lived callers that invoke Scan repeatedly and want to amortise
//     the cost of extracting scanner.py across calls.
//
// The scanner uses ONLY Python stdlib (`ast`, `json`, `sys`); there are
// no pip dependencies and no per-project virtualenv to manage.
package pyscan

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sosalejandro/atlas/packages/codeindex/annotations"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// ScannerSource is the embedded Python scanner. It ships as a string so
// downstream tools (debug commands, unit tests) can reach in without going
// through the filesystem.
//
//go:embed scanner.py
var ScannerSource string

// Options configures Scanner. Zero value is safe — Scan walks the project
// root for `.py` files using stdlib defaults.
//
// Pattern mirrors tsscan.Options: every field is an optional hook the
// caller can wire to override defaults. Adding a field is non-breaking.
type Options struct {
	// PythonBin is the python3 executable to invoke. Defaults to
	// "python3" on $PATH. Set to an absolute path for hermetic builds.
	PythonBin string

	// Include is a list of project-root-relative directories to walk.
	// When empty, the scanner walks the entire project root.
	Include []string

	// Exclude is a list of directory names to skip during the walk.
	// Always-skipped (in scanner.py): .git, .venv, venv, __pycache__,
	// node_modules, .tox, .mypy_cache, .pytest_cache, dist, build.
	Exclude []string

	// Timeout, when > 0, bounds a single Scan call. The caller's ctx is
	// wrapped with context.WithTimeout(ctx, Timeout) before the Python
	// subprocess is started, so a deadlocked scanner.py (pathological
	// input) cannot hang atlas forever even when the caller passes
	// context.Background().
	//
	// Zero value (the default) means no package-internal timeout — the
	// scan only ends when the caller's ctx is done or scanner.py exits.
	Timeout time.Duration

	// Logger receives scan-time warnings. Defaults to shared.NopLogger.
	Logger shared.Logger
}

// Scanner is the long-lived orchestrator. Hold one per atlas process; it
// caches the extracted scanner.py tempfile across Scan calls.
type Scanner struct {
	Options Options

	logger shared.Logger

	// scriptOnce ensures scanner.py is materialised to a tempfile exactly
	// once per Scanner instance. scriptPath holds the resolved path;
	// scriptErr captures any failure so subsequent Scan calls fail fast.
	scriptOnce sync.Once
	scriptPath string
	scriptErr  error
}

// Scan is a convenience wrapper for one-shot callers. It constructs a
// Scanner with opts, defers Close, runs Scan once against rootDir, and
// returns the Result.
//
// Mirrors the goscan.Scan + tsscan.Scan API shape so callers can swap
// between sub-scanners without reshaping their orchestration code.
func Scan(ctx context.Context, rootDir string, opts Options) (*Result, error) {
	s := NewScanner(opts)
	defer func() { _ = s.Close() }()
	return s.Scan(ctx, rootDir)
}

// NewScanner returns a Scanner configured with opts. The constructor does
// NOT extract scanner.py (that's deferred to first Scan) so cheap
// instantiation in init paths remains safe.
//
// Callers MUST defer Close() to release the tempfile that holds the
// extracted scanner.py.
func NewScanner(opts Options) *Scanner {
	logger := opts.Logger
	if logger == nil {
		logger = shared.NopLogger{}
	}
	return &Scanner{Options: opts, logger: logger}
}

// Close releases the tempdir that holds the extracted scanner.py.
// Idempotent — calling more than once is safe. Safe to call even if
// Scan was never invoked (no-op in that case).
func (s *Scanner) Close() error {
	if s.scriptPath == "" {
		return nil
	}
	dir := filepath.Dir(s.scriptPath)
	s.scriptPath = ""
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("pyscan: remove scanner.py tempdir: %w", err)
	}
	return nil
}

// Scan runs scanner.py against rootDir and returns the discovered Python
// symbols + edges. rootDir SHOULD be the project root; the embedded
// walker normalises paths relative to it.
//
// If python3 is unavailable, Scan returns a Result with a single warning
// and no error — see the package doc comment for rationale.
func (s *Scanner) Scan(ctx context.Context, rootDir string) (*Result, error) {
	if rootDir == "" {
		return nil, fmt.Errorf("pyscan: rootDir is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Apply package-internal timeout so a deadlocked scanner.py can't hang
	// atlas forever when the caller passed context.Background(). Zero =
	// opt-out (defer to caller ctx).
	if s.Options.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.Options.Timeout)
		defer cancel()
	}
	abs, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, fmt.Errorf("pyscan: abs rootDir: %w", err)
	}

	resolvedPy, ok := resolvePythonBin(ctx, s.logger, s.Options.PythonBin)
	if !ok {
		return &Result{
			Warnings: []string{fmt.Sprintf(
				"pyscan: %q not found in PATH (atlas py scanner requires Python 3.8+); install via your package manager or skip py files via --skip-py",
				fallbackPythonBin(s.Options.PythonBin))},
		}, nil
	}

	scriptPath, err := s.ensureScript()
	if err != nil {
		return nil, fmt.Errorf("pyscan: extract embedded scanner: %w", err)
	}

	args, err := buildScannerArgs(scriptPath, abs, s.Options)
	if err != nil {
		return nil, fmt.Errorf("pyscan: build args: %w", err)
	}

	cmd, err := newPythonCommand(ctx, resolvedPy, args)
	if err != nil {
		return nil, err
	}
	cmd.Dir = abs
	cmd.Env = buildScannerEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	s.logger.Debug(ctx, "running py scanner", "python", resolvedPy, "argc", len(args))
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("pyscan: scanner.py exited %d: %s",
				exitErr.ExitCode(), strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("pyscan: invoke python3: %w; stderr: %s",
			err, strings.TrimSpace(stderr.String()))
	}

	out, err := decodeOutput(stdout.Bytes())
	if err != nil {
		return nil, fmt.Errorf("pyscan: decode scanner.py output: %w; stderr: %s",
			err, strings.TrimSpace(stderr.String()))
	}

	res := s.mapToResult(out)
	s.logger.Info(ctx, "py scan complete",
		slog.Int("files", out.Stats.FilesScanned),
		slog.Int("symbols", out.Stats.SymbolsFound),
		slog.Int("edges", out.Stats.EdgesFound),
		slog.Int("syntax_failures", out.Stats.SyntaxFailures),
		slog.Int("warnings", len(res.Warnings)),
	)
	return res, nil
}

// buildScannerArgs assembles the python3 argv for the scanner.py
// subprocess.
//
// Defense-in-depth: every user-influenced value (Include / Exclude
// patterns) is run through validateScannerArg to reject shell
// metacharacters, newlines, and leading dashes. The Python call form
// itself is shell-free (exec.Command, not exec.Command("sh", "-c", ...))
// so an injection vector requires both (a) bypassing this validator AND
// (b) finding a python3 CLI flag that re-invokes the shell — neither of
// which has a known exploit path here.
func buildScannerArgs(scriptPath, projectRoot string, opts Options) ([]string, error) {
	if err := validateScannerArg(scriptPath); err != nil {
		return nil, fmt.Errorf("scriptPath: %w", err)
	}
	if err := validateScannerArg(projectRoot); err != nil {
		return nil, fmt.Errorf("projectRoot: %w", err)
	}
	args := []string{scriptPath, "--root", projectRoot}
	for _, inc := range opts.Include {
		if err := validateScannerArg(inc); err != nil {
			return nil, fmt.Errorf("include[%q]: %w", inc, err)
		}
		args = append(args, "--include", inc)
	}
	for _, exc := range opts.Exclude {
		if err := validateScannerArg(exc); err != nil {
			return nil, fmt.Errorf("exclude[%q]: %w", exc, err)
		}
		args = append(args, "--exclude", exc)
	}
	return args, nil
}

// validateScannerArg rejects strings that contain shell metacharacters,
// newlines, NUL bytes, or look like flag injections. The set is
// intentionally broad — none of these belong in a file path or glob, and
// rejecting them costs nothing.
func validateScannerArg(s string) error {
	if s == "" {
		return errors.New("empty argument")
	}
	if strings.HasPrefix(s, "-") {
		return fmt.Errorf("leading dash not allowed (got %q) — possible flag injection", s)
	}
	for _, r := range s {
		switch r {
		case '\x00', '\n', '\r':
			return fmt.Errorf("control character in argument %q", s)
		case '`', '$', ';', '|', '&', '<', '>', '\\', '"', '\'':
			return fmt.Errorf("shell metacharacter %q in argument %q", r, s)
		}
	}
	return nil
}

// ensureScript materialises scanner.py to a tempfile (idempotent — first
// call wins). Subsequent calls reuse the same path; the file lives until
// Close is called or the OS reaps it.
func (s *Scanner) ensureScript() (string, error) {
	s.scriptOnce.Do(func() {
		if ScannerSource == "" {
			s.scriptErr = errors.New("pyscan: embedded scanner.py is empty")
			return
		}
		dir, err := os.MkdirTemp("", "atlas-pyscan-*")
		if err != nil {
			s.scriptErr = err
			return
		}
		p := filepath.Join(dir, "scanner.py")
		if err := os.WriteFile(p, []byte(ScannerSource), 0o600); err != nil {
			s.scriptErr = err
			return
		}
		s.scriptPath = p
	})
	return s.scriptPath, s.scriptErr
}

// decodeOutput tolerates trailing newlines / log noise on stdout but not
// arbitrary text — if the JSON doesn't decode we surface the raw blob in
// the error so the caller can troubleshoot.
func decodeOutput(b []byte) (*rawScannerOutput, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return nil, errors.New("scanner.py produced empty stdout")
	}
	var out rawScannerOutput
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		// Retry without strict mode so a future additive scanner.py field
		// doesn't break older atlas binaries.
		out = rawScannerOutput{}
		if err2 := json.Unmarshal(b, &out); err2 != nil {
			// Include a bounded prefix of the raw payload so a caller
			// can see whether scanner.py emitted log noise, a panic
			// trace, or genuine-looking JSON that just didn't match the
			// envelope. 256 bytes is enough to fingerprint the failure
			// mode without blowing up a stderr log.
			return nil, fmt.Errorf("decode strict (%w); decode lenient (%w); first 256 bytes: %q",
				err, err2, b[:min(len(b), 256)])
		}
	}
	return &out, nil
}

// mapToResult converts the JSON envelope into Atlas's canonical types.
//
// Syntax-error files surface as Warnings AND as FileMeta entries with
// SyntaxError set, so callers that care can drill in (e.g. the future
// `atlas lint` verb) without re-walking the result.
//
// Annotations are mapped through annotations.Kinds so an unknown kind
// from a future scanner.py degrades into a counted warning rather than
// a crash. Records flagged Source=SourceAtlas so the materialise step
// treats them identically to comment-grammar hits found by the Go-side
// parser; the store's idempotent upserts collapse any duplicates that
// arise when a project annotates with both forms simultaneously.
func (s *Scanner) mapToResult(raw *rawScannerOutput) *Result {
	res := &Result{
		Symbols:     make([]shared.Symbol, 0, len(raw.Nodes)),
		Edges:       make([]graph.Edge, 0, len(raw.Edges)),
		Annotations: make([]shared.Annotation, 0, len(raw.Annotations)),
		Files:       raw.Files,
		Warnings:    append([]string(nil), raw.Warnings...),
	}
	for _, n := range raw.Nodes {
		kind := rawKindToSymbolKind(n.Kind)
		res.Symbols = append(res.Symbols, shared.Symbol{
			ID:   shared.SymbolID(n.ID),
			Kind: kind,
			Position: shared.FilePosition{
				Path: filepath.ToSlash(n.File),
				Line: n.Line,
			},
			Doc: n.Doc,
		})
	}
	// Build an in-module name resolver so unqualified `to` targets
	// emitted by scanner.py (e.g. `helper`, `Base`) can be promoted to
	// the fully-qualified symbol id when one exists in the same module.
	// Without this step, every Python edge whose target is a same-file
	// reference would be dropped at ingest time (the store's symbol
	// lookup is keyed by qualified_name, and `helper` does not match
	// `sample.helper`).
	//
	// Scanner.py emits both qualified module nodes (`sample`) and
	// qualified definitions (`sample.helper`). For every emitted symbol
	// we record the basename → qualified-id mapping scoped by the
	// declaring module — see pyEdgeResolver for the lookup semantics +
	// collision handling (last-write-wins is fine here because Python
	// module-level names cannot legally collide).
	resolver := newPyEdgeResolver(raw.Nodes, raw.Edges)
	knownIDs := make(map[shared.SymbolID]bool, len(res.Symbols))
	for _, sym := range res.Symbols {
		knownIDs[sym.ID] = true
	}
	// externalSeen dedupes synthetic stub symbols emitted for unresolved
	// edge targets (e.g. `typing.List` for `from typing import List`).
	// Without stubs these edges would be silently dropped at ingest
	// time — the store's edges table mandates a FK into symbols, so an
	// edge to an undeclared target has nowhere to land.
	externalSeen := make(map[shared.SymbolID]bool)
	for _, e := range raw.Edges {
		from := shared.SymbolID(e.From)
		to := resolver.resolve(from, e.To)
		if !knownIDs[to] && !externalSeen[to] && to != "" {
			res.Symbols = append(res.Symbols, externalSymbolStub(to))
			externalSeen[to] = true
		}
		res.Edges = append(res.Edges, graph.Edge{
			From: from,
			To:   to,
			Kind: e.Kind,
		})
	}
	skippedKinds := map[string]int{}
	for _, a := range raw.Annotations {
		kind, ok := annotations.Kinds[a.Kind]
		if !ok {
			skippedKinds[a.Kind]++
			continue
		}
		if a.ID == "" {
			continue
		}
		res.Annotations = append(res.Annotations, shared.Annotation{
			Kind: kind,
			IDs:  []string{a.ID},
			Position: shared.FilePosition{
				Path: filepath.ToSlash(a.File),
				Line: a.Line,
			},
			Source: shared.SourceAtlas,
			Raw:    a.Raw,
		})
	}
	for k, n := range skippedKinds {
		res.Warnings = append(res.Warnings,
			fmt.Sprintf("pyscan: skipped %d annotation(s) of unknown kind %q", n, k))
	}
	// Surface syntax-error files as warnings so the CLI prints them.
	for _, f := range raw.Files {
		if f.SyntaxError != "" {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("pyscan: %s: %s", f.Path, f.SyntaxError))
		}
	}
	return res
}

// buildScannerEnv assembles the child-process env for the Python
// subprocess. We inherit the parent env (preserves PATH, HOME, PROXY,
// etc.) but set PYTHONIOENCODING=utf-8 + PYTHONDONTWRITEBYTECODE=1 so:
//
//   - stdout is unambiguously UTF-8 (matches the JSON envelope contract);
//   - the scanner does NOT pollute the scanned project with __pycache__
//     directories (we are READING files, not running them).
//
// PYTHONPATH is intentionally NOT augmented — scanner.py uses ONLY stdlib.
func buildScannerEnv() []string {
	env := os.Environ()
	out := env[:0]
	for _, kv := range env {
		// Strip any pre-existing PYTHONIOENCODING / PYTHONDONTWRITEBYTECODE
		// so our values win deterministically.
		if strings.HasPrefix(kv, "PYTHONIOENCODING=") ||
			strings.HasPrefix(kv, "PYTHONDONTWRITEBYTECODE=") {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "PYTHONIOENCODING=utf-8")
	out = append(out, "PYTHONDONTWRITEBYTECODE=1")
	return out
}
