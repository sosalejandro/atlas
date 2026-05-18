package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/codeindex"
	goscan "github.com/sosalejandro/atlas/packages/codeindex/go"
	tsscan "github.com/sosalejandro/atlas/packages/codeindex/ts"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// newTraceCmd implements `atlas trace <id>` — walk the call graph from a
// feature or symbol id. Supports `saga:<id>` for saga walks via the store's
// EDA query, and `feature:` / `symbol:` prefixes for explicit disambiguation.
//
// As of atlas#29 the default path reads from the cached `.atlas/atlas.db`
// (populated by `atlas init` / `atlas scan`) so repeated invocations land in
// well under a second. The previous re-walk-from-disk behaviour now lives
// behind the opt-in `--fresh` flag — escape hatch only.
func newTraceCmd() *cobra.Command {
	var (
		root             string
		maxDepth         int
		nodeModulesPaths []string
		fresh            bool
	)
	cmd := &cobra.Command{
		Use:   "trace <id>",
		Short: "Walk the call graph from a feature, symbol id, or saga",
		Long: `trace walks Atlas's call graph starting from the supplied id and
emits the chain as text (default) or JSON.

The id can be one of:

  - feature-id          ("plans-patient.export-pdf")    — resolves linked
                                                          symbols then walks
                                                          each chain
  - SymbolID            ("auth.AuthHandler.Login")      — direct match
  - feature suffix      ("AuthHandler.Login")            — fuzzy suffix
  - saga:<id>           ("saga:checkout-flow")           — uses store EDA
  - feature:<id>        ("feature:plans-patient.export") — explicit feature
  - symbol:<qn>         ("symbol:auth.Login")            — explicit symbol

For an unprefixed input, trace first tries a feature lookup (the strict-regex
shape that wins most real-world inputs); a hit dispatches to traceByFeature.
On no-feature, it falls back to symbol resolution. When the same id matches
BOTH a feature and a symbol's qualified-name suffix, trace errors and asks
the caller to disambiguate with the explicit prefix.

By default trace reads from the cached SQLite store at .atlas/atlas.db (run
'atlas init' first to populate it). Pass --fresh to re-walk the codebase
from disk — that's the pre-#29 behaviour and costs minutes on real
codebases; only use it when you suspect the cached graph is wrong AND
'atlas scan' hasn't caught the drift.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTrace(cmd, args[0], root, maxDepth, nodeModulesPaths, fresh)
		},
	}
	cmd.Flags().StringVar(&root, "root", "",
		"project root for the fresh scan (default: repo root or cwd)")
	cmd.Flags().IntVar(&maxDepth, "max-depth", 10,
		"maximum trace depth")
	cmd.Flags().StringSliceVar(&nodeModulesPaths, "node-modules-path", nil,
		"absolute path to a node_modules dir the TS scanner can borrow typescript from "+
			"(repeatable; useful when the scanned project lacks its own deps)")
	cmd.Flags().BoolVar(&fresh, "fresh", false,
		"re-walk the codebase from disk instead of reading the cached store "+
			"(slow; use only when the cached graph looks wrong)")
	return cmd
}

// traceResult is the JSON payload for `atlas trace`.
type traceResult struct {
	Kind       string            `json:"kind"` // "call" | "saga" | "feature"
	Root       shared.SymbolID   `json:"root,omitempty"`
	FeatureID  shared.FeatureID  `json:"feature_id,omitempty"`
	SagaID     string            `json:"saga_id,omitempty"`
	Confidence float64           `json:"confidence,omitempty"`
	MaxDepth   int               `json:"max_depth,omitempty"`
	TotalNodes int               `json:"total_nodes,omitempty"`
	Cycles     []graph.Edge      `json:"cycles,omitempty"`
	Chain      []traceChainEntry `json:"chain,omitempty"`
	SagaSteps  []traceSagaStep   `json:"saga_steps,omitempty"`
	IndexRoot  string            `json:"index_root,omitempty"`
	Source     string            `json:"source,omitempty"` // "cache" | "fresh"
}

type traceChainEntry struct {
	ID    shared.SymbolID   `json:"id"`
	Kind  shared.SymbolKind `json:"kind"`
	Lang  string            `json:"lang,omitempty"`
	File  string            `json:"file,omitempty"`
	Line  int               `json:"line,omitempty"`
	Depth int               `json:"depth"`
}

type traceSagaStep struct {
	Order int    `json:"order"`
	File  string `json:"file"`
	Line  int    `json:"line"`
	Value string `json:"value"`
}

// staleStateWarning is emitted (verbatim) when the file_hashes table points
// at a file whose on-disk SHA-256 no longer matches the cached one. Exposed
// as a const so tests can pin the exact wording.
const staleStateWarning = "atlas state may be stale; run 'atlas scan' to refresh"

func runTrace(cmd *cobra.Command, target, rootArg string, maxDepth int, nodeModulesPaths []string, fresh bool) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// Prefix dispatch — these are unambiguous regardless of cache presence.
	switch {
	case strings.HasPrefix(target, "saga:"):
		return runTraceSaga(cmd, ctx, strings.TrimPrefix(target, "saga:"))
	case strings.HasPrefix(target, "feature:"):
		return runTraceCached(cmd, ctx, strings.TrimPrefix(target, "feature:"), maxDepth, traceModeFeature)
	case strings.HasPrefix(target, "symbol:"):
		return runTraceCached(cmd, ctx, strings.TrimPrefix(target, "symbol:"), maxDepth, traceModeSymbol)
	}

	// Default path: the cached store, unless --fresh is set.
	if fresh {
		return runTraceFresh(cmd, ctx, target, rootArg, maxDepth, nodeModulesPaths)
	}
	return runTraceCached(cmd, ctx, target, maxDepth, traceModeAuto)
}

// traceMode picks which lookup runTraceCached performs.
type traceMode int

const (
	traceModeAuto    traceMode = iota // try feature then symbol; error on collision
	traceModeFeature                  // feature only (feature: prefix)
	traceModeSymbol                   // symbol only (symbol: prefix)
)

// runTraceCached is the default trace path. It opens the persisted store and
// dispatches to traceByFeature / traceBySymbol based on the requested mode.
// When mode == traceModeAuto, it checks whether the input matches BOTH a
// feature and a symbol's qualified-name suffix and asks for disambiguation
// if so.
func runTraceCached(cmd *cobra.Command, ctx context.Context, input string, maxDepth int, mode traceMode) error {
	dbPath, err := resolveDBPath(loaded, flags.DBPath)
	if err != nil {
		return err
	}
	// The DB file must exist — we never silently fall back to a re-walk.
	if _, statErr := os.Stat(dbPath); errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("no atlas state found at %s. Run 'atlas init' first", dbPath)
	} else if statErr != nil {
		return fmt.Errorf("stat atlas state %s: %w", dbPath, statErr)
	}

	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("trace: open store %s: %w", dbPath, err)
	}
	defer func() { _ = s.Close() }()

	var warnings []string
	if stale := checkStaleState(ctx, s, loaded.repoRoot); stale {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", staleStateWarning)
		warnings = append(warnings, staleStateWarning)
	}

	switch mode {
	case traceModeFeature:
		return traceByFeature(cmd, ctx, s, shared.FeatureID(input), maxDepth, warnings)
	case traceModeSymbol:
		return traceBySymbol(cmd, ctx, s, input, maxDepth, warnings)
	}

	// traceModeAuto: try feature first, then symbol.
	_, ferr := s.Features().Get(ctx, shared.FeatureID(input))
	featureFound := ferr == nil

	// For the symbol side we want both an exact qualified-name match AND a
	// dotted-suffix match — the latter is what `pickEntryPoint` historically
	// accepted, and we keep that ergonomics for the cached path.
	symRow, symMatch, serr := lookupSymbolCached(ctx, s, input)
	if serr != nil && !errors.Is(serr, shared.ErrSymbolNotFound) {
		return serr
	}

	switch {
	case featureFound && symMatch:
		return fmt.Errorf(
			"input %q matches both feature %q and symbol %q. Disambiguate with 'atlas trace feature:%s' or 'atlas trace symbol:%s'",
			input, input, symRow.QualifiedName, input, input)
	case featureFound:
		return traceByFeature(cmd, ctx, s, shared.FeatureID(input), maxDepth, warnings)
	case symMatch:
		return traceBySymbol(cmd, ctx, s, input, maxDepth, warnings)
	default:
		return fmt.Errorf("trace: no feature or symbol matches %q", input)
	}
}

// lookupSymbolCached resolves an input string to a symbol in the cached
// store. Returns the row, a "found" bool, and any non-NotFound error.
//
// We try the exact qualified-name match first (the fast path), then fall
// back to a dotted-suffix List scan when no exact match exists. The List
// query carries no Filter — symbol counts on real codebases stay in the
// 10k-100k range, well within "in-memory linear scan is fine" territory.
func lookupSymbolCached(ctx context.Context, s *store.Store, input string) (store.SymbolRow, bool, error) {
	row, err := s.Symbols().FindByQualifiedName(ctx, shared.SymbolID(input))
	if err == nil {
		return row, true, nil
	}
	if !errors.Is(err, shared.ErrSymbolNotFound) {
		return store.SymbolRow{}, false, err
	}
	// Suffix scan — accept "AuthHandler.Login" matching "auth.AuthHandler.Login".
	all, err := s.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		return store.SymbolRow{}, false, fmt.Errorf("trace: list symbols: %w", err)
	}
	for _, r := range all {
		if hasDottedSuffix(string(r.QualifiedName), input) {
			return r, true, nil
		}
	}
	return store.SymbolRow{}, false, nil
}

// traceBySymbol walks the call graph from the symbol identified by `input`
// in the cached store. `input` may be a qualified name or a dotted suffix.
func traceBySymbol(cmd *cobra.Command, ctx context.Context, s *store.Store, input string, maxDepth int, warnings []string) error {
	row, found, err := lookupSymbolCached(ctx, s, input)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("trace: no symbol matches %q", input)
	}

	chain, err := walkSymbolChain(ctx, s, row, maxDepth)
	if err != nil {
		return err
	}
	res := traceResult{
		Kind:       "call",
		Root:       row.QualifiedName,
		MaxDepth:   maxDepth,
		TotalNodes: len(chain),
		Chain:      chain,
		Source:     "cache",
	}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "trace",
			map[string]any{"target": string(row.QualifiedName), "max_depth": maxDepth, "source": "cache"},
			res, warnings)
	}
	printTraceCallText(cmd, res, warnings)
	return nil
}

// traceByFeature resolves a feature's linked symbols, walks each chain, and
// merges them deduped on (id, depth). A feature with 0 linked symbols emits
// a clean warning and an empty chain — NOT an error (the feature might be
// annotated in a comment-only file and still count as "known").
func traceByFeature(cmd *cobra.Command, ctx context.Context, s *store.Store, fid shared.FeatureID, maxDepth int, warnings []string) error {
	if _, err := s.Features().Get(ctx, fid); err != nil {
		if errors.Is(err, shared.ErrFeatureNotFound) {
			return fmt.Errorf("trace: feature %q not found", fid)
		}
		return fmt.Errorf("trace: feature %q: %w", fid, err)
	}
	links, err := s.FeatureSymbols().ListByFeature(ctx, fid)
	if err != nil {
		return fmt.Errorf("trace: feature_symbols %q: %w", fid, err)
	}

	if len(links) == 0 {
		msg := fmt.Sprintf("feature %s exists but has no linked symbols (annotation may be in a comment-only file)", fid)
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", msg)
		warnings = append(warnings, msg)
		res := traceResult{Kind: "feature", FeatureID: fid, MaxDepth: maxDepth, Source: "cache"}
		if flags.JSON {
			return emitJSON(stdoutOrJSON(cmd), "trace",
				map[string]any{"feature": string(fid), "max_depth": maxDepth, "source": "cache"},
				res, warnings)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "feature %s (0 linked symbols)\n", fid)
		return nil
	}

	// Resolve every link's symbol_id once via a single List scan, then walk
	// each. The scan is the load-bearing optimisation — without it we'd
	// issue O(links * symbols) lookups via FindByQualifiedName in
	// walkSymbolChain's cache miss path.
	idIndex, err := buildSymbolByID(ctx, s)
	if err != nil {
		return err
	}

	seen := map[string]bool{}
	merged := make([]traceChainEntry, 0)
	for _, link := range links {
		row, ok := idIndex[link.SymbolID]
		if !ok {
			return fmt.Errorf("trace: feature %q references symbol_id %d not in store",
				fid, link.SymbolID)
		}
		chain, err := walkSymbolChain(ctx, s, row, maxDepth)
		if err != nil {
			return err
		}
		for _, e := range chain {
			// Dedupe on a stable (id, depth) key — repeating a node at a
			// different depth in a different sub-walk is fine, but the
			// same (id, depth) row from two roots is the duplicate.
			key := string(e.ID) + "@" + fmt.Sprintf("%d", e.Depth)
			if seen[key] {
				continue
			}
			seen[key] = true
			merged = append(merged, e)
		}
	}
	// Stable order: depth then id, matches the per-symbol path order.
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Depth != merged[j].Depth {
			return merged[i].Depth < merged[j].Depth
		}
		return merged[i].ID < merged[j].ID
	})

	res := traceResult{
		Kind:       "feature",
		FeatureID:  fid,
		MaxDepth:   maxDepth,
		TotalNodes: len(merged),
		Chain:      merged,
		Source:     "cache",
	}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "trace",
			map[string]any{"feature": string(fid), "max_depth": maxDepth, "source": "cache"},
			res, warnings)
	}
	printTraceFeatureText(cmd, res, warnings)
	return nil
}

// walkSymbolChain walks the persisted edges from `row` up to maxDepth,
// returning the chain in the same shape the in-memory walk used to produce.
// Depth 0 is the root symbol itself; the first edge target appears at depth 1.
func walkSymbolChain(ctx context.Context, s *store.Store, row store.SymbolRow, maxDepth int) ([]traceChainEntry, error) {
	chain := []traceChainEntry{{
		ID:    row.QualifiedName,
		Kind:  row.Kind,
		File:  row.FilePath,
		Line:  row.Line,
		Depth: 0,
	}}
	walked, err := s.Edges().Walk(ctx, row.ID, maxDepth)
	if err != nil {
		return nil, fmt.Errorf("trace: walk edges from %q: %w", row.QualifiedName, err)
	}
	// The CTE result carries qualified names but no kind/file/line — we
	// resolve those by name. Cache the per-symbol metadata to avoid an N+1.
	cache := map[shared.SymbolID]store.SymbolRow{
		row.QualifiedName: row,
	}
	for _, w := range walked {
		toRow, ok := cache[w.ToName]
		if !ok {
			r, err := s.Symbols().FindByQualifiedName(ctx, w.ToName)
			if err != nil {
				if errors.Is(err, shared.ErrSymbolNotFound) {
					// Edge references a symbol no longer in the store —
					// emit a minimal entry so the chain stays connected.
					cache[w.ToName] = store.SymbolRow{QualifiedName: w.ToName}
					toRow = cache[w.ToName]
				} else {
					return nil, fmt.Errorf("trace: resolve %q: %w", w.ToName, err)
				}
			} else {
				cache[w.ToName] = r
				toRow = r
			}
		}
		chain = append(chain, traceChainEntry{
			ID:    w.ToName,
			Kind:  toRow.Kind,
			File:  toRow.FilePath,
			Line:  toRow.Line,
			Depth: w.Depth,
		})
	}
	return chain, nil
}

// buildSymbolByID returns an in-memory map keyed by surrogate id over every
// symbol in the store. The cost is one List scan — real codebases sit in
// the 10k–100k symbol range, so this stays in the tens-of-MB at worst.
func buildSymbolByID(ctx context.Context, s *store.Store) (map[int64]store.SymbolRow, error) {
	all, err := s.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		return nil, fmt.Errorf("trace: list symbols: %w", err)
	}
	out := make(map[int64]store.SymbolRow, len(all))
	for _, r := range all {
		out[r.ID] = r
	}
	return out, nil
}

// checkStaleState samples the file_hashes table and returns true if any
// cached hash no longer matches the on-disk SHA-256. We bound the scan to a
// modest sample so the staleness check itself stays cheap even on indices
// with hundreds of thousands of rows.
//
// repoRoot is needed because file_hashes stores repo-relative paths.
func checkStaleState(ctx context.Context, s *store.Store, repoRoot string) bool {
	rows, err := s.FileHashes().List(ctx)
	if err != nil || len(rows) == 0 {
		return false
	}
	const sampleSize = 16
	// Step through the list at a regular stride so we surface drift in any
	// region of the tree, not just the alphabetically-first files.
	stride := len(rows) / sampleSize
	if stride < 1 {
		stride = 1
	}
	checked := 0
	for i := 0; i < len(rows) && checked < sampleSize; i += stride {
		row := rows[i]
		path := row.FilePath
		if !filepath.IsAbs(path) {
			path = filepath.Join(repoRoot, path)
		}
		sum, err := sha256OfFile(path)
		if err != nil {
			// Missing file IS a staleness signal — the index references a
			// file that's been deleted.
			if errors.Is(err, os.ErrNotExist) {
				return true
			}
			// Permission / IO error: skip silently to avoid noisy warnings
			// on partial checkouts.
			checked++
			continue
		}
		if sum != row.ContentHash {
			return true
		}
		checked++
	}
	return false
}

func sha256OfFile(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // path comes from file_hashes, repo-internal.
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// runTraceFresh is the `--fresh` escape hatch: re-walk the codebase from
// disk via codeindex.IndexProject. This is the pre-#29 default behaviour
// and costs minutes on real codebases. Use only when the cached graph is
// suspected wrong.
func runTraceFresh(cmd *cobra.Command, ctx context.Context, target, rootArg string, maxDepth int, nodeModulesPaths []string) error {
	rootDir := rootArg
	if rootDir == "" {
		rootDir = loaded.repoRoot
	}
	idx, err := codeindex.IndexProject(ctx, rootDir, codeindex.Options{
		GoOptions: goscan.Options{},
		TSOptions: tsscan.Options{NodeModulesPaths: nodeModulesPaths},
	})
	if err != nil {
		return fmt.Errorf("trace --fresh: index %s: %w", rootDir, err)
	}

	rootID := pickEntryPoint(idx, target)
	if rootID == "" {
		return fmt.Errorf("trace: no symbol matches %q", target)
	}
	trace := idx.Graph.TraceFrom(rootID, maxDepth)

	res := traceResult{
		Kind:       "call",
		Root:       rootID,
		Confidence: trace.Confidence,
		MaxDepth:   trace.MaxDepth,
		TotalNodes: trace.TotalNodes,
		Cycles:     trace.Cycles,
		Chain:      flattenTraceChain(trace.Root, idx.SymbolLangs),
		IndexRoot:  rootDir,
		Source:     "fresh",
	}

	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "trace",
			map[string]any{"target": target, "root": rootDir, "max_depth": maxDepth, "source": "fresh"},
			res, trace.Warnings)
	}
	printTraceCallText(cmd, res, trace.Warnings)
	return nil
}

func runTraceSaga(cmd *cobra.Command, ctx context.Context, sagaID string) error {
	dbPath, err := resolveDBPath(loaded, flags.DBPath)
	if err != nil {
		return err
	}
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("trace saga: open store %s: %w", dbPath, err)
	}
	defer func() { _ = s.Close() }()

	steps, err := s.EDA().WalkSaga(ctx, sagaID)
	if err != nil {
		return fmt.Errorf("trace saga %q: %w", sagaID, err)
	}
	out := make([]traceSagaStep, 0, len(steps))
	for _, st := range steps {
		out = append(out, traceSagaStep{
			Order: st.Order,
			File:  st.Annotation.FilePath,
			Line:  int(st.Annotation.Line),
			Value: st.Annotation.Value,
		})
	}
	res := traceResult{Kind: "saga", SagaID: sagaID, SagaSteps: out}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "trace",
			map[string]any{"saga_id": sagaID}, res, nil)
	}
	printTraceSagaText(cmd, res)
	return nil
}

func pickEntryPoint(idx *codeindex.Index, feature string) shared.SymbolID {
	want := shared.SymbolID(feature)
	if _, ok := idx.Graph.Nodes[want]; ok {
		return want
	}
	suffix := "." + feature
	for id := range idx.Graph.Nodes {
		if id == want || strings.HasSuffix(string(id), suffix) {
			return id
		}
	}
	return ""
}

func flattenTraceChain(n *graph.TraceNode, langs map[shared.SymbolID]string) []traceChainEntry {
	if n == nil {
		return nil
	}
	var out []traceChainEntry
	walkTraceChain(n, langs, &out)
	return out
}

func walkTraceChain(n *graph.TraceNode, langs map[shared.SymbolID]string, out *[]traceChainEntry) {
	if n == nil {
		return
	}
	lang := ""
	if langs != nil {
		lang = langs[n.Node.ID]
	}
	*out = append(*out, traceChainEntry{
		ID:    n.Node.ID,
		Kind:  n.Node.Kind,
		Lang:  lang,
		File:  n.Node.Position.Path,
		Line:  n.Node.Position.Line,
		Depth: n.Depth,
	})
	for _, c := range n.Children {
		walkTraceChain(c, langs, out)
	}
}

func printTraceCallText(cmd *cobra.Command, r traceResult, warnings []string) {
	fmt.Fprintf(cmd.OutOrStdout(), "trace %s (confidence %.2f, %d nodes)\n",
		r.Root, r.Confidence, r.TotalNodes)
	for _, e := range r.Chain {
		indent := strings.Repeat("  ", e.Depth)
		fmt.Fprintf(cmd.OutOrStdout(), "%s%s  [%s] %s:%d\n",
			indent, e.ID, e.Kind, e.File, e.Line)
	}
	for _, c := range r.Cycles {
		fmt.Fprintf(cmd.ErrOrStderr(), "  cycle: %s -> %s\n", c.From, c.To)
	}
	for _, w := range warnings {
		fmt.Fprintf(cmd.ErrOrStderr(), "  warning: %s\n", w)
	}
}

func printTraceFeatureText(cmd *cobra.Command, r traceResult, warnings []string) {
	fmt.Fprintf(cmd.OutOrStdout(), "trace feature %s (%d nodes)\n", r.FeatureID, r.TotalNodes)
	for _, e := range r.Chain {
		indent := strings.Repeat("  ", e.Depth)
		fmt.Fprintf(cmd.OutOrStdout(), "%s%s  [%s] %s:%d\n",
			indent, e.ID, e.Kind, e.File, e.Line)
	}
	for _, w := range warnings {
		fmt.Fprintf(cmd.ErrOrStderr(), "  warning: %s\n", w)
	}
}

func printTraceSagaText(cmd *cobra.Command, r traceResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "saga %s (%d steps)\n", r.SagaID, len(r.SagaSteps))
	for _, st := range r.SagaSteps {
		fmt.Fprintf(cmd.OutOrStdout(), "  step %d  %s:%d  %s\n",
			st.Order, st.File, st.Line, st.Value)
	}
}
