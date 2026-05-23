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

// defaultTraceDepth is the depth that --depth defaults to when the caller
// passes neither --depth nor --max-depth. Per issue #61 the default is 3 —
// deep enough to be useful for code exploration, shallow enough that the
// output stays scannable. -1 means "unlimited" (with cycle detection).
const defaultTraceDepth = 3

// newTraceCmd implements `atlas trace <id>` — walk the call graph from a
// feature or symbol id. Supports `saga:<id>` for saga walks via the store's
// EDA query, and `feature:` / `symbol:` prefixes for explicit disambiguation.
//
// As of atlas#29 the default path reads from the cached `.atlas/atlas.db`
// (populated by `atlas init` / `atlas scan`) so repeated invocations land in
// well under a second. The previous re-walk-from-disk behaviour now lives
// behind the opt-in `--fresh` flag — escape hatch only.
//
// As of issue #61 the symbol-trace path renders a recursive indented tree
// up to `--depth N` (default 3, -1 = unlimited with cycle detection)
// instead of the legacy depth-1 chain. The chain flat-output stays in the
// JSON envelope for backward-compatible consumers; the new tree is added
// alongside.
func newTraceCmd() *cobra.Command {
	var (
		root             string
		maxDepth         int
		depth            int
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
'atlas scan' hasn't caught the drift.

--depth caps the recursive walk (default 3). --depth -1 walks unlimited
with cycle detection — duplicate visits stop recursion and are tagged
[cycle] in the output. --depth 0 emits direct callees only (the
pre-issue-#61 behaviour).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// --depth wins over --max-depth when both are passed; the
			// older --max-depth flag is preserved for backward compat
			// with scripts that already set it explicitly.
			effective := pickEffectiveDepth(cmd, depth, maxDepth)
			return runTrace(cmd, args[0], root, effective, nodeModulesPaths, fresh)
		},
	}
	cmd.Flags().StringVar(&root, "root", "",
		"project root for the fresh scan (default: repo root or cwd)")
	cmd.Flags().IntVar(&maxDepth, "max-depth", 10,
		"maximum trace depth (legacy alias for --depth; kept for backward compat)")
	cmd.Flags().IntVar(&depth, "depth", defaultTraceDepth,
		"recursive call-tree depth; -1 = unlimited with cycle detection, 0 = direct callees only")
	cmd.Flags().StringSliceVar(&nodeModulesPaths, "node-modules-path", nil,
		"absolute path to a node_modules dir the TS scanner can borrow typescript from "+
			"(repeatable; useful when the scanned project lacks its own deps)")
	cmd.Flags().BoolVar(&fresh, "fresh", false,
		"re-walk the codebase from disk instead of reading the cached store "+
			"(slow; use only when the cached graph looks wrong)")
	return cmd
}

// pickEffectiveDepth implements the precedence: explicit --depth wins
// (even when the user passes --depth 3 to mean "the default"); else
// explicit --max-depth wins; else the package default.
//
// Both flags carry non-zero defaults so we can't distinguish "user
// passed the default" from "user passed nothing" by value alone —
// cobra's Changed() bit is the load-bearing input here.
func pickEffectiveDepth(cmd *cobra.Command, depth, maxDepth int) int {
	if cmd.Flags().Changed("depth") {
		return depth
	}
	if cmd.Flags().Changed("max-depth") {
		return maxDepth
	}
	return defaultTraceDepth
}

// traceResult is the JSON payload for `atlas trace`.
//
// As of issue #61 it carries both the legacy flat `chain` (preserved so
// downstream consumers keep working) AND a nested `tree` of TraceTreeNode
// for the recursive call-tree shape. depth_reached reports the deepest
// node actually emitted (may be less than requested max_depth if the
// graph terminates earlier).
type traceResult struct {
	Kind         string            `json:"kind"` // "call" | "saga" | "feature"
	Root         shared.SymbolID   `json:"root,omitempty"`
	FeatureID    shared.FeatureID  `json:"feature_id,omitempty"`
	SagaID       string            `json:"saga_id,omitempty"`
	Confidence   float64           `json:"confidence,omitempty"`
	MaxDepth     int               `json:"max_depth,omitempty"`
	DepthReached int               `json:"depth_reached,omitempty"`
	TotalNodes   int               `json:"total_nodes,omitempty"`
	Cycles       []graph.Edge      `json:"cycles,omitempty"`
	CycleNodes   []shared.SymbolID `json:"cycle_nodes,omitempty"`
	Chain        []traceChainEntry `json:"chain,omitempty"`
	Tree         *traceTreeNode    `json:"tree,omitempty"`
	SagaSteps    []traceSagaStep   `json:"saga_steps,omitempty"`
	IndexRoot    string            `json:"index_root,omitempty"`
	Source       string            `json:"source,omitempty"` // "cache" | "fresh"
}

type traceChainEntry struct {
	ID    shared.SymbolID   `json:"id"`
	Kind  shared.SymbolKind `json:"kind"`
	Lang  string            `json:"lang,omitempty"`
	File  string            `json:"file,omitempty"`
	Line  int               `json:"line,omitempty"`
	Depth int               `json:"depth"`
}

// traceTreeNode is the nested-tree form of the call walk. Each node
// records the symbol's location data plus an ordered children slice
// for the recursive shape. IsCycle marks a node that was seen earlier
// in the current chain — recursion stops and downstream consumers
// render it with a `[cycle]` tag.
type traceTreeNode struct {
	Symbol   shared.SymbolID   `json:"symbol"`
	Kind     shared.SymbolKind `json:"kind,omitempty"`
	File     string            `json:"file,omitempty"`
	Line     int               `json:"line,omitempty"`
	Depth    int               `json:"depth"`
	IsCycle  bool              `json:"is_cycle,omitempty"`
	Children []*traceTreeNode  `json:"children,omitempty"`
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
//
// Since issue #61 this walks a tree (Edges.Out per node, recursive) up to
// maxDepth with per-chain cycle detection rather than the legacy flat CTE
// chain. The flat chain is retained as a derived view of the tree so the
// JSON contract remains backward compatible.
func traceBySymbol(cmd *cobra.Command, ctx context.Context, s *store.Store, input string, maxDepth int, warnings []string) error {
	row, found, err := lookupSymbolCached(ctx, s, input)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("trace: no symbol matches %q", input)
	}

	tree, walked, cycleNodes, reached, err := walkSymbolTree(ctx, s, row, maxDepth)
	if err != nil {
		return err
	}
	chain := flattenTree(tree)
	res := traceResult{
		Kind:         "call",
		Root:         row.QualifiedName,
		MaxDepth:     maxDepth,
		DepthReached: reached,
		TotalNodes:   walked,
		Chain:        chain,
		Tree:         tree,
		CycleNodes:   cycleNodes,
		Source:       "cache",
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
//
// Retained for the feature-merge path in traceByFeature, which dedupes
// across multiple root walks. The symbol-trace path uses walkSymbolTree
// instead — it preserves the parent/child shape callers need for the
// indented-tree output (per issue #61).
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

// walkSymbolTree builds a recursive call-tree rooted at `row` up to
// maxDepth, using per-node Edges.Out() + per-chain cycle detection. The
// tree preserves parent/child structure that the flat CTE walk loses.
//
// maxDepth semantics (issue #61):
//
//   - maxDepth >  0 → emit at most that many levels of children below
//                     the root. --depth 3 yields nodes at depths 0..3
//                     (the root plus three layers).
//   - maxDepth == 0 → root only — no children emitted. Intentional
//                     "what does this symbol look like" probe.
//   - maxDepth <  0 → unlimited recursion; cycle detection is what
//                     prevents the walk from running forever.
//
// Cycle detection is per-chain (the set of ancestors of the node we're
// about to visit). A node visited twice down the SAME chain becomes a
// leaf TraceTreeNode with IsCycle=true; visits along DIFFERENT chains
// are allowed (the same callee may appear under multiple parents).
//
// Returns the tree, the total number of unique visited symbols, the
// list of symbols where cycles were detected, the deepest depth
// actually emitted, and any error.
func walkSymbolTree(
	ctx context.Context,
	s *store.Store,
	row store.SymbolRow,
	maxDepth int,
) (*traceTreeNode, int, []shared.SymbolID, int, error) {
	visited := make(map[shared.SymbolID]bool)
	cycleSet := make(map[shared.SymbolID]bool)
	rowCache := map[shared.SymbolID]store.SymbolRow{
		row.QualifiedName: row,
	}
	maxReached := 0

	var walk func(node store.SymbolRow, depth int, ancestors map[shared.SymbolID]bool) (*traceTreeNode, error)
	walk = func(node store.SymbolRow, depth int, ancestors map[shared.SymbolID]bool) (*traceTreeNode, error) {
		visited[node.QualifiedName] = true
		if depth > maxReached {
			maxReached = depth
		}
		tn := &traceTreeNode{
			Symbol: node.QualifiedName,
			Kind:   node.Kind,
			File:   node.FilePath,
			Line:   node.Line,
			Depth:  depth,
		}
		// Depth cap. maxDepth < 0 ⇒ unlimited; cycle detection is the
		// only brake. Otherwise stop recursing once we'd be about to
		// emit a child at depth > maxDepth — i.e. when our own depth is
		// already at maxDepth, we are a leaf in this trace.
		if maxDepth >= 0 && depth >= maxDepth {
			return tn, nil
		}
		// In-degree id 0 means "no surrogate id" — happens when the
		// caller is walking a freshly synthesised root row that hasn't
		// been persisted. Skip the edge query in that case.
		if node.ID == 0 {
			return tn, nil
		}
		outEdges, err := s.Edges().Out(ctx, node.ID)
		if err != nil {
			return nil, fmt.Errorf("trace: out-edges from %q: %w", node.QualifiedName, err)
		}
		// Filter to call edges only — the CTE-based Walk does the same
		// filter inline; we mirror it here so the two paths produce the
		// same shape.
		seenChild := make(map[shared.SymbolID]bool, len(outEdges))
		for _, e := range outEdges {
			if e.Kind != store.EdgeKindCall {
				continue
			}
			childRow, err := resolveByID(ctx, s, e.ToID, rowCache)
			if err != nil {
				return nil, err
			}
			// Dedupe siblings: a callsite that hits the same callee
			// twice in the same function emits two edges. We surface
			// the callee once per parent.
			if seenChild[childRow.QualifiedName] {
				continue
			}
			seenChild[childRow.QualifiedName] = true

			if ancestors[childRow.QualifiedName] {
				// Cycle — emit a marker leaf at depth+1 so the tree
				// shows where the cycle was detected without descending
				// into it.
				cycleSet[childRow.QualifiedName] = true
				cycleDepth := depth + 1
				if cycleDepth > maxReached {
					maxReached = cycleDepth
				}
				tn.Children = append(tn.Children, &traceTreeNode{
					Symbol:  childRow.QualifiedName,
					Kind:    childRow.Kind,
					File:    childRow.FilePath,
					Line:    childRow.Line,
					Depth:   cycleDepth,
					IsCycle: true,
				})
				continue
			}
			// Push the child onto the ancestor set for the recursive
			// call; pop on return so siblings see a clean ancestry.
			ancestors[childRow.QualifiedName] = true
			child, err := walk(childRow, depth+1, ancestors)
			delete(ancestors, childRow.QualifiedName)
			if err != nil {
				return nil, err
			}
			tn.Children = append(tn.Children, child)
		}
		return tn, nil
	}

	ancestors := map[shared.SymbolID]bool{row.QualifiedName: true}
	tree, err := walk(row, 0, ancestors)
	if err != nil {
		return nil, 0, nil, 0, err
	}
	cycleList := make([]shared.SymbolID, 0, len(cycleSet))
	for id := range cycleSet {
		cycleList = append(cycleList, id)
	}
	sort.Slice(cycleList, func(i, j int) bool { return cycleList[i] < cycleList[j] })
	return tree, len(visited), cycleList, maxReached, nil
}

// resolveByID looks up a symbol row by its surrogate id, caching the
// resolution so repeat hits across siblings collapse to one DB query.
// Falls back to a minimal placeholder row when the id doesn't resolve;
// that surfaces dangling edges (a recent file delete) without breaking
// the tree.
func resolveByID(ctx context.Context, s *store.Store, id int64, cache map[shared.SymbolID]store.SymbolRow) (store.SymbolRow, error) {
	// The cache is keyed by qualified name, not surrogate id, so we
	// first check whether any cached row has this id. Linear scan is
	// cheap here — typical trees have tens to hundreds of symbols.
	for _, r := range cache {
		if r.ID == id {
			return r, nil
		}
	}
	rows, err := s.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		return store.SymbolRow{}, fmt.Errorf("trace: list symbols for id resolution: %w", err)
	}
	for _, r := range rows {
		if r.ID == id {
			cache[r.QualifiedName] = r
			return r, nil
		}
	}
	// Missing — synthesise a placeholder so the tree stays connected.
	placeholder := store.SymbolRow{ID: id, QualifiedName: shared.SymbolID(fmt.Sprintf("symbol#%d", id))}
	cache[placeholder.QualifiedName] = placeholder
	return placeholder, nil
}

// flattenTree produces the legacy traceChainEntry slice from the tree.
// Walks depth-first to preserve the pre-issue-#61 chain ordering that
// downstream consumers (testreg migration scripts, atlas's own trace
// validation) already depend on. Cycle markers are NOT re-emitted in
// the chain — they're tree-only metadata.
func flattenTree(root *traceTreeNode) []traceChainEntry {
	if root == nil {
		return nil
	}
	var out []traceChainEntry
	var walk func(n *traceTreeNode)
	walk = func(n *traceTreeNode) {
		if n.IsCycle {
			return
		}
		out = append(out, traceChainEntry{
			ID:    n.Symbol,
			Kind:  n.Kind,
			File:  n.File,
			Line:  n.Line,
			Depth: n.Depth,
		})
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(root)
	return out
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
	// Header — for symbol traces we prefer "depth reached" over the
	// legacy "confidence" gauge; confidence is the --fresh path's
	// concept (the in-memory graph TraceFrom emits it). The cached
	// path leaves confidence at 0 so we don't render that line at all.
	if r.Confidence > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "trace %s (confidence %.2f, %d nodes)\n",
			r.Root, r.Confidence, r.TotalNodes)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "trace %s (depth %d, %d nodes)\n",
			r.Root, r.DepthReached, r.TotalNodes)
	}

	switch {
	case r.Tree != nil:
		printTreeNode(cmd.OutOrStdout(), r.Tree, "", true, true)
	default:
		// Legacy flat-chain rendering kept for the --fresh path and any
		// other callers that don't populate Tree.
		for _, e := range r.Chain {
			indent := strings.Repeat("  ", e.Depth)
			fmt.Fprintf(cmd.OutOrStdout(), "%s%s  [%s] %s:%d\n",
				indent, e.ID, e.Kind, e.File, e.Line)
		}
	}
	for _, c := range r.Cycles {
		fmt.Fprintf(cmd.ErrOrStderr(), "  cycle: %s -> %s\n", c.From, c.To)
	}
	for _, w := range warnings {
		fmt.Fprintf(cmd.ErrOrStderr(), "  warning: %s\n", w)
	}
}

// printTreeNode renders the indented box-drawing tree shape required by
// issue #61. The prefix accumulates `│   ` and `    ` segments as we
// descend so each branch keeps its parent's spine; isLast switches the
// connector between `├─` and `└─`. The root prints without a connector
// so it lines up flush with the header.
//
// Cycle leaves render with a `[cycle]` tag instead of the file:line so
// the reader can tell at a glance which subtree was clipped.
func printTreeNode(w io.Writer, n *traceTreeNode, prefix string, isRoot, isLast bool) {
	if n == nil {
		return
	}
	if isRoot {
		fmt.Fprintf(w, "%s  [%s] %s:%d\n", n.Symbol, n.Kind, n.File, n.Line)
	} else {
		connector := "├─ "
		if isLast {
			connector = "└─ "
		}
		if n.IsCycle {
			fmt.Fprintf(w, "%s%s%s  [cycle]\n", prefix, connector, n.Symbol)
		} else {
			fmt.Fprintf(w, "%s%s%s  [%s] %s:%d\n",
				prefix, connector, n.Symbol, n.Kind, n.File, n.Line)
		}
	}
	if n.IsCycle {
		return
	}
	// Children prefix continues the spine for non-last children, blanks
	// it for the last so deeper grandchildren don't draw a fake spine
	// under a fully-consumed parent.
	childPrefix := prefix
	if !isRoot {
		if isLast {
			childPrefix += "    "
		} else {
			childPrefix += "│   "
		}
	}
	for i, c := range n.Children {
		printTreeNode(w, c, childPrefix, false, i == len(n.Children)-1)
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
