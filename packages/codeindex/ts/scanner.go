// Package tsscan is the Atlas TypeScript scanner. It orchestrates the
// embedded scanner.ts (a TypeScript-compiler-API walker) via a Node.js
// subprocess and returns its discoveries in Atlas's canonical
// shared.Symbol + graph.Edge shapes.
//
// Design rules (mirrors packages/codeindex/go/):
//
//   - Node is OPTIONAL at runtime. If `node` isn't on PATH the Scanner
//     returns an empty Result with a single warning, never an error.
//     This keeps the Go binary self-contained for backend-only users.
//   - scanner.ts is embedded at build time via //go:embed. We write it
//     to a per-process tempfile on first call and reuse it for the
//     lifetime of the Scanner instance.
//   - All file paths in the Result are repo-relative (forward-slash) per
//     shared.FilePosition rules. The scanner.ts walker already emits in
//     that shape; this layer only re-bases when --root is absolute.
//   - Logging via shared.Logger / log/slog. NopLogger is the test default.
//
// Phase 2 replaces nothing in Phase 1 — Phase 1's Go AST scanner runs in
// parallel via codeindex.IndexProject orchestration.
package tsscan

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

	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// ScannerSource is the embedded TypeScript scanner. It ships as a string so
// downstream tools (debug commands, unit tests) can reach in without going
// through the filesystem.
//
//go:embed scanner.ts
var ScannerSource string

// Options configures Scanner. Zero value is safe — Scan auto-detects every
// supported router under the project root and walks default frontend layouts
// (apps/web*, apps/mobile, src/, etc.).
//
// Pattern mirrors goscan.Options: every field is an optional hook the caller
// can wire to override defaults. Adding a field is non-breaking.
type Options struct {
	// NodeBin is the Node.js executable to invoke. Defaults to "node" on
	// $PATH. Set to an absolute path for hermetic builds.
	NodeBin string

	// TsconfigPath, when non-empty, is forwarded to scanner.ts as
	// --tsconfig <path>. Currently advisory — the embedded scanner doesn't
	// consume the tsconfig (it uses ts.createSourceFile directly), but the
	// flag is reserved for future type-aware passes (Phase 6 contract).
	TsconfigPath string

	// Include is a list of project-root-relative directories to walk. When
	// empty, the scanner walks the project root plus every direct child of
	// apps/ and packages/. This mirrors the testreg .testreg.yaml
	// frontend_roots config but moves it into Go where Phase 4's .atlas.yaml
	// loader can populate it.
	Include []string

	// Exclude is a list of glob patterns to skip during the walk. Reserved
	// — the embedded scanner accepts the flag but currently only honours
	// the built-in DEFAULT_SKIP_DIRS set.
	Exclude []string

	// Routers narrows the scan to specific router frameworks. Empty means
	// auto-detect every supported router (the recommended default for
	// monorepos with multiple frontends).
	Routers []RouterKind

	// NodeModulesPaths is appended to NODE_PATH so scanner.ts (which imports
	// the typescript module) can resolve dependencies that don't live in the
	// scanned project's own node_modules. Useful when atlas runs against a
	// minimal fixture or a backend-only repo. Each entry must be an absolute
	// directory ending in `node_modules`; non-conforming entries are dropped
	// with a warning rather than failing the scan.
	NodeModulesPaths []string

	// Logger receives scan-time warnings. Defaults to shared.NopLogger.
	Logger shared.Logger
}

// Scanner is the long-lived orchestrator. Hold one per atlas process; it
// caches the extracted scanner.ts tempfile across Scan calls.
type Scanner struct {
	Options Options

	logger shared.Logger

	// scriptOnce ensures scanner.ts is materialised to a tempfile exactly
	// once per Scanner instance. scriptPath holds the resolved path;
	// scriptErr captures any failure so subsequent Scan calls fail fast.
	scriptOnce sync.Once
	scriptPath string
	scriptErr  error
}

// NewScanner returns a Scanner configured with opts. The constructor does
// NOT extract scanner.ts (that's deferred to first Scan) so cheap
// instantiation in init paths remains safe.
func NewScanner(opts Options) *Scanner {
	logger := opts.Logger
	if logger == nil {
		logger = shared.NopLogger{}
	}
	return &Scanner{Options: opts, logger: logger}
}

// Scan runs scanner.ts against rootDir and returns the discovered
// frontend symbols + edges. rootDir SHOULD be the project root (the
// directory containing package.json / tsconfig.json); the embedded
// walker normalises paths relative to it.
//
// If the Node.js runtime is unavailable, Scan returns a Result with a
// single warning and no error — see the package doc comment for rationale.
func (s *Scanner) Scan(ctx context.Context, rootDir string) (*Result, error) {
	if rootDir == "" {
		return nil, fmt.Errorf("tsscan: rootDir is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	abs, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, fmt.Errorf("tsscan: abs rootDir: %w", err)
	}

	resolvedNode, ok := resolveNodeBin(ctx, s.logger, s.Options.NodeBin)
	if !ok {
		return &Result{
			Warnings: []string{fmt.Sprintf(
				"tsscan: %q not found on PATH; skipping TypeScript scan",
				fallbackNodeBin(s.Options.NodeBin))},
		}, nil
	}

	scriptPath, err := s.ensureScript()
	if err != nil {
		return nil, fmt.Errorf("tsscan: extract embedded scanner: %w", err)
	}

	args, err := buildScannerArgs(scriptPath, abs, s.Options)
	if err != nil {
		return nil, fmt.Errorf("tsscan: build args: %w", err)
	}

	// Make sure scanner.ts can resolve `typescript` even when the scanned
	// project has no node_modules. We bridge by synthesising a node_modules
	// next to the tempdir copy of scanner.ts that links to the first
	// available typescript install (project root → caller-supplied paths).
	if err := s.bridgeTypescript(ctx, abs); err != nil {
		s.logger.Debug(ctx, "typescript bridge unavailable", "err", err.Error())
	}

	cmd, err := newNodeCommand(ctx, resolvedNode, args)
	if err != nil {
		return nil, err
	}
	cmd.Dir = abs
	cmd.Env = buildScannerEnv(ctx, s.logger, abs, s.Options.NodeModulesPaths)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	s.logger.Debug(ctx, "running ts scanner", "node", resolvedNode, "argc", len(args))
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("tsscan: scanner.ts exited %d: %s",
				exitErr.ExitCode(), strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("tsscan: invoke node: %w; stderr: %s",
			err, strings.TrimSpace(stderr.String()))
	}

	out, err := decodeOutput(stdout.Bytes())
	if err != nil {
		return nil, fmt.Errorf("tsscan: decode scanner.ts output: %w; stderr: %s",
			err, strings.TrimSpace(stderr.String()))
	}

	res := s.mapToResult(out)
	s.logger.Info(ctx, "ts scan complete",
		slog.Int("files", out.Stats.FilesScanned),
		slog.Int("routes", out.Stats.RoutesFound),
		slog.Int("apiCalls", out.Stats.APICallsFound),
		slog.Int("warnings", len(res.Warnings)),
	)
	return res, nil
}

// buildScannerArgs assembles the Node argv for the scanner.ts subprocess.
//
// Defense-in-depth: every user-influenced value (Include / Exclude patterns,
// TsconfigPath, Router enum) is run through validateScannerArg to reject
// shell metacharacters, newlines, and leading dashes. The Node call form
// itself is shell-free (exec.Command, not exec.Command("sh", "-c", ...))
// so an injection vector requires both (a) bypassing this validator AND
// (b) finding a Node CLI flag that re-invokes the shell — neither of which
// has a known exploit path here. The validator exists to satisfy static
// analysis and provide a single chokepoint if Node ever grows one.
func buildScannerArgs(scriptPath, projectRoot string, opts Options) ([]string, error) {
	if err := validateScannerArg(scriptPath); err != nil {
		return nil, fmt.Errorf("scriptPath: %w", err)
	}
	if err := validateScannerArg(projectRoot); err != nil {
		return nil, fmt.Errorf("projectRoot: %w", err)
	}
	args := []string{"--experimental-strip-types", scriptPath, "--root", projectRoot}
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
	for _, rk := range opts.Routers {
		switch rk {
		case ReactRouter, TanStackRouter, ExpoRouter:
			args = append(args, "--router", string(rk))
		default:
			return nil, fmt.Errorf("router: unknown kind %q", rk)
		}
	}
	if opts.TsconfigPath != "" {
		if err := validateScannerArg(opts.TsconfigPath); err != nil {
			return nil, fmt.Errorf("tsconfigPath: %w", err)
		}
		args = append(args, "--tsconfig", opts.TsconfigPath)
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

// ensureScript materialises scanner.ts to a tempfile (idempotent — first
// call wins). Subsequent calls reuse the same path; the file lives until
// the OS reaps it.
func (s *Scanner) ensureScript() (string, error) {
	s.scriptOnce.Do(func() {
		if ScannerSource == "" {
			s.scriptErr = errors.New("tsscan: embedded scanner.ts is empty")
			return
		}
		dir, err := os.MkdirTemp("", "atlas-tsscan-*")
		if err != nil {
			s.scriptErr = err
			return
		}
		p := filepath.Join(dir, "scanner.ts")
		if err := os.WriteFile(p, []byte(ScannerSource), 0o600); err != nil {
			s.scriptErr = err
			return
		}
		s.scriptPath = p
	})
	return s.scriptPath, s.scriptErr
}

// bridgeTypescript symlinks a real typescript install next to the extracted
// scanner.ts. Node's ESM loader resolves bare imports via walking up the
// filesystem looking for node_modules/<name> — NODE_PATH is CommonJS-only.
//
// We try (in order):
//
//  1. <projectRoot>/node_modules/typescript  — the scanned project's own dep
//  2. <NodeModulesPaths>/typescript          — caller-supplied fallback
//
// On the first hit we create scanner-dir/node_modules/typescript → source
// (symlink). Idempotent: subsequent calls short-circuit. Errors are
// non-fatal (we surface them to the caller via the debug log) because the
// scanner.ts will still run if Node can find typescript via some other
// resolution path; only if BOTH the scanner.ts dir and the project dir
// lack a typescript module does the scan fail — at which point the user
// sees the genuine ERR_MODULE_NOT_FOUND from Node.
func (s *Scanner) bridgeTypescript(_ context.Context, projectRoot string) error {
	if s.scriptPath == "" {
		return errors.New("scriptPath not set")
	}
	scriptDir := filepath.Dir(s.scriptPath)
	bridgeDir := filepath.Join(scriptDir, "node_modules", "typescript")
	if _, err := os.Stat(bridgeDir); err == nil {
		return nil // already bridged
	}

	candidates := []string{filepath.Join(projectRoot, "node_modules", "typescript")}
	for _, nm := range s.Options.NodeModulesPaths {
		candidates = append(candidates, filepath.Join(nm, "typescript"))
	}
	var source string
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			source = c
			break
		}
	}
	if source == "" {
		return errors.New("no typescript install found in project or NodeModulesPaths")
	}
	if err := os.MkdirAll(filepath.Dir(bridgeDir), 0o755); err != nil {
		return fmt.Errorf("mkdir bridge: %w", err)
	}
	if err := os.Symlink(source, bridgeDir); err != nil {
		// Symlink may fail on filesystems / platforms that disallow it (e.g.
		// some Windows configs without admin). Fall back to a copy — slow but
		// correct.
		if copyErr := copyDir(source, bridgeDir); copyErr != nil {
			return fmt.Errorf("symlink typescript (%v); copy fallback: %w", err, copyErr)
		}
	}
	return nil
}

// copyDir is a minimal recursive copier used only as a Windows fallback for
// the typescript bridge. Skips symlinks in the source tree to avoid
// dependency-graph cycles.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		defer out.Close()
		buf := make([]byte, 32*1024)
		for {
			n, rerr := in.Read(buf)
			if n > 0 {
				if _, werr := out.Write(buf[:n]); werr != nil {
					return werr
				}
			}
			if rerr != nil {
				if rerr.Error() == "EOF" {
					return nil
				}
				return rerr
			}
		}
	})
}

// decodeOutput tolerates trailing newlines / log noise on stdout but not
// arbitrary text — if the JSON doesn't decode we surface the raw blob in
// the error so the caller can troubleshoot.
func decodeOutput(b []byte) (*rawScannerOutput, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return nil, errors.New("scanner.ts produced empty stdout")
	}
	var out rawScannerOutput
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		// Retry without strict mode so a future additive scanner.ts field
		// doesn't break older atlas binaries.
		out = rawScannerOutput{}
		if err2 := json.Unmarshal(b, &out); err2 != nil {
			return nil, fmt.Errorf("%w (lenient: %v)", err, err2)
		}
	}
	return &out, nil
}

// mapToResult converts the JSON envelope into Atlas's canonical types.
//
// Note: scanner.ts emits an "endpoint" node for the join key on backend
// HTTP routes (e.g. "POST /api/v1/auth/login"). The Go orchestrator
// (codeindex.IndexProject) is what decides whether to keep that node or
// dedupe against the Go AST scanner's own endpoint node — here we just
// surface what the walker found.
func (s *Scanner) mapToResult(raw *rawScannerOutput) *Result {
	res := &Result{
		Symbols:  make([]shared.Symbol, 0, len(raw.Nodes)),
		Edges:    make([]graph.Edge, 0, len(raw.Edges)),
		Files:    raw.Files,
		Warnings: append([]string(nil), raw.Warnings...),
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
	for _, e := range raw.Edges {
		res.Edges = append(res.Edges, graph.Edge{
			From: shared.SymbolID(e.From),
			To:   shared.SymbolID(e.To),
		})
	}
	return res
}

// buildScannerEnv assembles the child-process env for the Node subprocess.
//
// We inherit the parent env (preserves PATH, HOME, PROXY, etc.) but compose
// a NODE_PATH that the scanner.ts walker uses to locate the `typescript`
// package. Order of precedence (highest wins because Node walks NODE_PATH
// left→right):
//
//  1. <projectRoot>/node_modules — for monorepos the typical layout
//  2. opts.NodeModulesPaths — caller-supplied fallbacks (e.g. atlas's own
//     node_modules in test fixtures)
//  3. any pre-existing NODE_PATH from the parent env
//
// Non-existent entries are dropped silently; entries that don't end in
// "node_modules" or aren't absolute paths are dropped with a warning.
func buildScannerEnv(ctx context.Context, logger shared.Logger, projectRoot string, extra []string) []string {
	env := os.Environ()
	// Strip existing NODE_PATH so we can reconstruct it in the order above.
	var preserved string
	out := env[:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "NODE_PATH=") {
			preserved = strings.TrimPrefix(kv, "NODE_PATH=")
			continue
		}
		out = append(out, kv)
	}
	var parts []string
	// 1. project-local node_modules
	projectNM := filepath.Join(projectRoot, "node_modules")
	if st, err := os.Stat(projectNM); err == nil && st.IsDir() {
		parts = append(parts, projectNM)
	}
	// 2. caller-supplied extras
	for _, p := range extra {
		if !filepath.IsAbs(p) || filepath.Base(p) != "node_modules" {
			logger.Warn(ctx, "ignored NodeModulesPaths entry (must be absolute path ending in node_modules)",
				"path", p)
			continue
		}
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			logger.Warn(ctx, "ignored NodeModulesPaths entry (not a directory)", "path", p)
			continue
		}
		parts = append(parts, p)
	}
	// 3. pre-existing
	if preserved != "" {
		parts = append(parts, preserved)
	}
	if len(parts) > 0 {
		out = append(out, "NODE_PATH="+strings.Join(parts, string(os.PathListSeparator)))
	}
	return out
}
