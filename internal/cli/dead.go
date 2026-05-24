package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/store"
)

// newCodebaseDeadCmd builds the `atlas codebase dead` verb. It scans the
// indexed graph for symbols whose qualifying incoming-edge count is zero
// — a triage signal for dead-code sweeps — and surfaces them alongside
// the inherent-false-positive caveats so consumers don't act on the
// output as a literal deletion list.
//
// Per the issue spec (atlas-internal#20) the command is intentionally
// thin: the heavy lifting (kind/scope/path/test predicates) lives in
// store.Symbols().FindDead so other future verbs ("audit dead",
// "snapshot drift") can reuse it without re-implementing the SQL.
func newCodebaseDeadCmd() *cobra.Command {
	var (
		kindFlag         string
		filterFlag       string
		includeTestsFlag bool
		includeScopesCSV string
	)

	cmd := &cobra.Command{
		Use:   "dead [flags]",
		Short: "List symbols with zero qualifying incoming edges (dead-code candidates)",
		Long: `dead reports first-party symbols whose incoming-edge count of the
chosen kind is zero. Useful as a starting point for dead-code sweeps.

The output is a CANDIDATE list, not a verdict. Dynamic dispatch
(Python getattr, importlib, __import__), plugin entry points, and
re-export chains all surface as zero-incoming-edge symbols even when
they're live at runtime. Treat every row as a triage starting point —
the caveats block at the bottom of the output enumerates the known
false-positive sources.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCodebaseDead(cmd, deadCmdArgs{
				kind:          kindFlag,
				filter:        filterFlag,
				includeTests:  includeTestsFlag,
				includeScopes: includeScopesCSV,
			})
		},
	}

	cmd.Flags().StringVar(&kindFlag, "kind", "import",
		"edge kind to check for incoming edges (import|call|all)")
	cmd.Flags().StringVar(&filterFlag, "filter", "",
		"limit candidates to symbols under this path prefix (e.g. services/api)")
	cmd.Flags().BoolVar(&includeTestsFlag, "include-tests", false,
		"count test-file edges toward the 'has importers' total")
	cmd.Flags().StringVar(&includeScopesCSV, "include-scopes", "module,conditional",
		"comma-separated import scopes counted as edges (module|function|conditional|type_checking|try_guard|all)")

	return cmd
}

// deadCmdArgs is the cobra-flag payload, kept as a struct so the RunE
// body stays a thin parse+dispatch shell. Helps the verbose flag-list
// at the call site read top-down without positional argument
// gymnastics.
type deadCmdArgs struct {
	kind          string
	filter        string
	includeTests  bool
	includeScopes string
}

// deadKindToEdgeKind maps the user-facing --kind value onto the store's
// EdgeKind enum. The sentinel "all" produces an empty EdgeKind which
// FindDead interprets as "match any incoming edge regardless of kind".
// Unknown values are rejected with an actionable error rather than
// silently defaulting — a typo'd flag in a CI pipeline is the kind of
// quiet failure that produces "we deleted everything" tickets.
func deadKindToEdgeKind(raw string) (store.EdgeKind, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "import":
		return store.EdgeKindImport, nil
	case "call":
		return store.EdgeKindCall, nil
	case "all", "any":
		return "", nil
	default:
		return "", fmt.Errorf("codebase dead: unknown --kind %q; allowed: import|call|all", raw)
	}
}

// parseDeadScopes turns the --include-scopes CSV into the slice
// FindDead's normalizer accepts. The shortcut "all" expands to every
// known scope so a caller doesn't have to hand-type the full list.
// Empty input collapses to the default module+conditional pair —
// matching the CLI default the issue spec asks for.
//
// Unknown tokens raise an error rather than getting silently dropped
// by normalizeScopeFilter; the CLI should be loud about a typo'd
// scope so the user knows the resulting query isn't what they meant.
func parseDeadScopes(csv string) ([]string, error) {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return []string{
			store.EdgeMetaImportScopeModule,
			store.EdgeMetaImportScopeConditional,
		}, nil
	}
	known := map[string]string{
		store.EdgeMetaImportScopeModule:       store.EdgeMetaImportScopeModule,
		store.EdgeMetaImportScopeFunction:     store.EdgeMetaImportScopeFunction,
		store.EdgeMetaImportScopeConditional:  store.EdgeMetaImportScopeConditional,
		store.EdgeMetaImportScopeTypeChecking: store.EdgeMetaImportScopeTypeChecking,
		store.EdgeMetaImportScopeTryGuard:     store.EdgeMetaImportScopeTryGuard,
	}
	out := make([]string, 0, 5)
	for _, raw := range strings.Split(csv, ",") {
		tok := strings.ToLower(strings.TrimSpace(raw))
		if tok == "" {
			continue
		}
		if tok == "all" {
			return []string{
				store.EdgeMetaImportScopeModule,
				store.EdgeMetaImportScopeFunction,
				store.EdgeMetaImportScopeConditional,
				store.EdgeMetaImportScopeTypeChecking,
				store.EdgeMetaImportScopeTryGuard,
			}, nil
		}
		v, ok := known[tok]
		if !ok {
			return nil, fmt.Errorf(
				"codebase dead: unknown --include-scopes value %q; allowed: module|function|conditional|type_checking|try_guard|all",
				raw)
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("codebase dead: --include-scopes parsed to an empty list")
	}
	return out, nil
}

// deadCodeCaveats is the canonical caveats block surfaced with every
// invocation. The text is hoisted out of the runner so JSON consumers
// can read the identical strings via the envelope payload — a
// downstream tool that wants to render its own report can lift these
// directly without re-translating.
//
// Order is meaningful: dynamic dispatch first (highest-frequency false
// positive in Python), then test-file exclusion (configurable so the
// caller wants to know about it), then plugin entry points (frequent
// in services with click / typer / setuptools entry_points), then the
// re-export caveat (specific to __init__.py-shaped layouts).
var deadCodeCaveats = []string{
	"Python dynamic dispatch (getattr, importlib, __import__) is invisible to static analysis. Symbols marked dead may still be live via runtime mechanisms.",
	"Test-only edges are excluded by default — pass --include-tests to count them.",
	"Plugin entry points (setup.py entry_points, importlib.metadata) are not analyzed; CLI entrypoints and ASGI apps often appear here as false positives.",
	"Re-exports via package __init__.py can hide incoming edges on the defining file — the importer points at the package, not the leaf.",
}

// codebaseDeadResult is the JSON envelope payload. Designed to be
// stable under the v1 envelope contract: every field is additive,
// rename-safe, and lifts the human caveats into the structured
// representation so downstream tools don't have to scrape the text
// output for them.
type codebaseDeadResult struct {
	Kind             string                `json:"kind"`
	Filter           string                `json:"filter,omitempty"`
	IncludeTests     bool                  `json:"include_tests"`
	IncludeScopes    []string              `json:"include_scopes,omitempty"`
	DeadCandidates   []deadCandidateRecord `json:"dead_candidates"`
	TotalCandidates  int                   `json:"total_candidates"`
	Caveats          []string              `json:"caveats"`
	ExternalExcluded bool                  `json:"external_excluded"`
}

// deadCandidateRecord is the per-row JSON shape. Mirrors the human
// output: path, qualified name, kind, incoming count (always 0
// today), and any row-specific caveats (currently always empty —
// reserved for future per-row classifiers, e.g. "likely entry point"
// pattern detection).
type deadCandidateRecord struct {
	Path          string   `json:"path"`
	QualifiedName string   `json:"qualified_name"`
	SymbolKind    string   `json:"symbol_kind"`
	Line          int      `json:"line"`
	IncomingCount int      `json:"incoming_count"`
	Caveats       []string `json:"caveats,omitempty"`
}

// runCodebaseDead is the cobra RunE. Resolves flags → store filter,
// dispatches, then chooses the output formatter based on --json.
//
// Errors from FindDead bubble up unchanged so the cobra error chain
// preserves the SQL-level context — a "no such table: edges" failure
// (i.e. caller forgot to `atlas init`) should surface verbatim.
func runCodebaseDead(cmd *cobra.Command, args deadCmdArgs) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	edgeKind, err := deadKindToEdgeKind(args.kind)
	if err != nil {
		return err
	}
	scopes, err := parseDeadScopes(args.includeScopes)
	if err != nil {
		return err
	}

	s, err := openStoreForRead(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	filter := store.DeadCodeFilter{
		EdgeKind:     edgeKind,
		PathPrefix:   args.filter,
		IncludeTests: args.includeTests,
	}
	// Scope filter only applies to import edges; under --kind=all or
	// --kind=call the store ignores ScopeFilter, but we still echo the
	// caller's intent into the JSON envelope so the output is
	// self-describing.
	if edgeKind == store.EdgeKindImport {
		filter.ScopeFilter = scopes
	}

	candidates, err := s.Symbols().FindDead(ctx, filter)
	if err != nil {
		return fmt.Errorf("codebase dead: %w", err)
	}

	result := codebaseDeadResult{
		Kind:             normalizeKindForOutput(edgeKind),
		Filter:           args.filter,
		IncludeTests:     args.includeTests,
		IncludeScopes:    scopes,
		DeadCandidates:   toDeadRecords(candidates),
		TotalCandidates:  len(candidates),
		Caveats:          deadCodeCaveats,
		ExternalExcluded: true,
	}

	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "codebase.dead",
			map[string]any{
				"kind":           result.Kind,
				"filter":         args.filter,
				"include_tests":  args.includeTests,
				"include_scopes": scopes,
			},
			result, nil)
	}
	renderDeadHuman(cmd, result)
	return nil
}

// normalizeKindForOutput converts the store-side EdgeKind back into the
// user-facing token for the --json `kind` field. Empty (kind=all)
// collapses to the literal "all" rather than leaking the internal
// empty-string sentinel.
func normalizeKindForOutput(k store.EdgeKind) string {
	if k == "" {
		return "all"
	}
	return string(k)
}

// toDeadRecords converts the store rows into the JSON wire shape. The
// per-row caveats slice is currently always empty — kept on the type
// so a future row-classifier (e.g. "matches __main__.py pattern →
// likely entry point") can populate it without breaking consumers.
func toDeadRecords(rows []store.DeadCodeCandidate) []deadCandidateRecord {
	out := make([]deadCandidateRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, deadCandidateRecord{
			Path:          r.Symbol.FilePath,
			QualifiedName: string(r.Symbol.QualifiedName),
			SymbolKind:    string(r.Symbol.Kind),
			Line:          r.Symbol.Line,
			IncomingCount: r.IncomingCount,
		})
	}
	// Deterministic order so successive invocations diff cleanly.
	// FindDead already orders by file_path, line; reapply here as a
	// belt-and-braces in case a future store change loses the order.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].QualifiedName < out[j].QualifiedName
	})
	return out
}

// renderDeadHuman prints the human-friendly text output. The shape
// mirrors the issue spec: a WARN banner with the candidate count, a
// rows block, then the caveats block. Stays cheap on big result sets —
// every line is a single Fprintf, no buffering layer.
func renderDeadHuman(cmd *cobra.Command, r codebaseDeadResult) {
	w := cmd.OutOrStdout()

	header := fmt.Sprintf("  WARN  %d symbols have 0 %s edges (may be false positives — see caveats)\n",
		r.TotalCandidates, r.Kind)
	fmt.Fprintln(w)
	fmt.Fprint(w, header)
	if r.Filter != "" {
		fmt.Fprintf(w, "        filter: path prefix %q\n", r.Filter)
	}
	if r.Kind == "import" && len(r.IncludeScopes) > 0 {
		fmt.Fprintf(w, "        include_scopes: %s\n", strings.Join(r.IncludeScopes, ","))
	}
	if !r.IncludeTests {
		fmt.Fprintln(w, "        excluding test files from importer count (pass --include-tests to count them)")
	}
	fmt.Fprintln(w)

	if len(r.DeadCandidates) == 0 {
		fmt.Fprintln(w, "  (no dead-code candidates under the supplied filters)")
		fmt.Fprintln(w)
		return
	}

	for _, c := range r.DeadCandidates {
		fmt.Fprintf(w, "  %s  [%s]  0 incoming %s\n",
			c.Path, c.SymbolKind, r.Kind)
		fmt.Fprintf(w, "    %s\n", c.QualifiedName)
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Caveats:")
	for _, c := range r.Caveats {
		fmt.Fprintf(w, "    - %s\n", c)
	}
	fmt.Fprintln(w)
}
