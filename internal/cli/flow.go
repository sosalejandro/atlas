package cli

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/cfg"
	"github.com/sosalejandro/atlas/packages/coverage/gocover"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// newFlowCmd builds `atlas flow` — control flow INSIDE a symbol.
//
// Every other verb in atlas describes symbols and the edges between them.
// This one opens the box: the branches, the loops, the cyclomatic complexity,
// and — where the execution data can honestly support it — which way each
// branch actually went. That last part is the reason the command exists:
// statement coverage says a line ran, not that the branch was taken both
// ways, and a function with an untested error path can show 90% statement
// coverage while every failure mode in it is unexercised.
func newFlowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "flow",
		Short: "Control flow inside a symbol: CFGs, complexity, decision coverage, N+1 candidates",
		Long: `flow builds a control-flow graph per Go function and derives from it
what a symbol's line range cannot say:

  * cyclomatic complexity, per symbol, joined to the graph
  * DECISION coverage -- which branch outcomes a test run actually
    took -- computed from an existing coverprofile
  * flow.query-in-loop, the N+1 candidate: a query reached from
    inside a loop body
  * flow.unreachable, code no path can reach (a structural fact,
    not an untested one)

Three things this command is careful about.

Decision coverage is NOT statement coverage and is never blended
with it. 'atlas cov' answers "did the line run"; this answers "was
the branch taken both ways", and the two are reported separately
because they are different questions.

Some outcomes cannot be judged from a Go coverprofile at all, and
those are counted and reported rather than divided away. The
denominator is the DECIDABLE outcomes, never the total.

MC/DC is not derivable from Go's instrumentation, full stop. flow
enumerates the conditions MC/DC would need and reports how many are
independently exercisable in principle -- a property of the source.
It will never print an MC/DC verdict, because it cannot have one.

See docs/commands/flow.md.`,
	}
	cmd.AddCommand(newFlowBuildCmd(), newFlowShowCmd(), newFlowFindingsCmd())
	return cmd
}

// --- build ----------------------------------------------------------------

type flowBuildFlags struct {
	root    string
	profile string
}

func newFlowBuildCmd() *cobra.Command {
	var f flowBuildFlags
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build and persist a CFG for every indexed Go symbol",
		Long: `build re-parses the files the indexed symbols name, constructs a
control-flow graph per function, and persists the blocks, edges and
metrics keyed by symbol id so every result joins the graph.

With --profile it also computes decision coverage from an existing
'go test -coverprofile' run. Use -covermode=count: the inference that
recovers the false outcome of an 'if' with no 'else' needs execution
counts, and 'set' mode throws them away.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runFlowBuild(cmd, f) },
	}
	cmd.Flags().StringVar(&f.root, "root", "",
		"repo root the indexed file paths are relative to (default: repo root or cwd)")
	cmd.Flags().StringVar(&f.profile, "profile", "",
		"coverprofile to derive decision coverage from (go test -covermode=count -coverprofile=...)")
	return cmd
}

// flowCoverageSummary is the decision-coverage half of the build payload.
//
// Percent is a POINTER: a run in which nothing was decidable has no
// percentage, and emitting 0 there would be read as "no branch was covered"
// rather than "no branch could be judged".
type flowCoverageSummary struct {
	OutcomesTotal     int `json:"outcomes_total"`
	OutcomesDecidable int `json:"outcomes_decidable"`
	OutcomesTaken     int `json:"outcomes_taken"`
	// Undetermined is the third verdict, carried as its own number rather
	// than left to be recovered by subtraction: these are outcomes the
	// profile could not judge either way, and they are neither taken nor
	// untaken.
	Undetermined int      `json:"outcomes_undetermined"`
	Percent      *float64 `json:"percent"`
	Source       string   `json:"source"`
}

type flowBuildResult struct {
	Root            string         `json:"root"`
	FilesParsed     int            `json:"files_parsed"`
	SymbolsAnalyzed int            `json:"symbols_analyzed"`
	MaxComplexity   int            `json:"max_complexity"`
	MaxComplexityAt string         `json:"max_complexity_symbol,omitempty"`
	Conditions      int            `json:"conditions"`
	Independent     int            `json:"conditions_independent"`
	Findings        map[string]int `json:"findings"`
	// DecisionCoverage is absent (not zero) when no profile was supplied.
	DecisionCoverage *flowCoverageSummary `json:"decision_coverage,omitempty"`
	// MCDC is the standing caveat. It is emitted on every run, with or
	// without a profile, so no consumer of this payload can present the
	// condition counts as an MC/DC result.
	MCDC string `json:"mcdc"`
}

func runFlowBuild(cmd *cobra.Command, f flowBuildFlags) error {
	ctx := cmdContext(cmd)
	root := f.root
	if root == "" {
		root = loaded.repoRoot
	}
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve working directory: %w", err)
		}
		root = wd
	}

	s, err := openStoreForRead(ctx)
	if err != nil {
		return err
	}
	defer s.Close()

	b := &flowBuilder{store: s, root: root, profilePath: f.profile}
	if err := b.loadProfile(); err != nil {
		return err
	}
	if err := b.loadSymbols(ctx); err != nil {
		return err
	}
	if err := b.run(ctx); err != nil {
		return err
	}
	return emitFlowBuild(cmd, f, b)
}

func emitFlowBuild(cmd *cobra.Command, f flowBuildFlags, b *flowBuilder) error {
	res := b.result()
	warnings := b.warnings
	if b.unmodelled > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"%d construct(s) are not modelled exactly (a `goto`, or a function literal with branches of its own, "+
				"whose flow belongs to the closure and not to the enclosing symbol); e.g. %s",
			b.unmodelled, strings.Join(b.unmodelledEg, "; ")))
	}
	if b.gotoSuppressed > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"flow.unreachable was NOT computed for %d function(s) containing a `goto`: the graph does not model "+
				"goto edges, so a label reached only by one has no predecessor in it and would be reported as "+
				"dead code that in fact runs. Nothing is claimed about reachability there — e.g. %s",
			b.gotoSuppressed, strings.Join(b.gotoSuppressedEg, "; ")))
	}
	if res.DecisionCoverage != nil {
		// Said on every run that produces a number, because the single most
		// likely misreading of this output is that it supersedes `atlas cov`.
		warnings = append(warnings,
			"decision coverage is NOT statement coverage: it answers whether each branch was taken both ways, "+
				"not whether each line ran. Read it beside `atlas cov`, never instead of it.")
	}
	if f.profile != "" && res.DecisionCoverage == nil {
		warnings = append(warnings, fmt.Sprintf(
			"the profile %s covers none of the files these symbols live in, so NOTHING was measured. "+
				"No decision-coverage row was written: an absent row means \"not measured\", and recording "+
				"zero-coverage rows from a profile that never looked at this code would read as \"no branch was taken\".",
			f.profile))
	}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "flow.build",
			map[string]any{"root": res.Root, "profile": f.profile}, res, warnings)
	}
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "flow build: %d symbol(s) across %d file(s)\n", res.SymbolsAnalyzed, res.FilesParsed)
	if res.MaxComplexityAt != "" {
		fmt.Fprintf(w, "  most complex: %s (cyclomatic %d)\n", res.MaxComplexityAt, res.MaxComplexity)
	}
	fmt.Fprintf(w, "  conditions: %d (%d independently exercisable in principle)\n",
		res.Conditions, res.Independent)
	writeFlowCoverage(w, res.DecisionCoverage, f.profile)
	writeFlowFindingCounts(w, res.Findings)
	fmt.Fprintf(w, "  MC/DC: %s\n", cfg.MCDCNotDerivable)
	for _, warn := range warnings {
		fmt.Fprintf(w, "  note: %s\n", warn)
	}
	return nil
}

func writeFlowCoverage(w io.Writer, dc *flowCoverageSummary, profile string) {
	if dc == nil {
		if profile != "" {
			fmt.Fprintf(w, "  decision coverage: not measured -- %s covers none of these files\n", profile)
			return
		}
		fmt.Fprintf(w, "  decision coverage: not measured (pass --profile to measure it)\n")
		return
	}
	if dc.Percent == nil {
		fmt.Fprintf(w, "  decision coverage: UNAVAILABLE -- all %d outcome(s) are UNDETERMINED from statement coverage\n",
			dc.OutcomesTotal)
		return
	}
	fmt.Fprintf(w, "  decision coverage: %.1f%% (%d of %d decidable outcomes taken; %d outcome(s) UNDETERMINED)\n",
		*dc.Percent, dc.OutcomesTaken, dc.OutcomesDecidable, dc.Undetermined)
}

func writeFlowFindingCounts(w io.Writer, findings map[string]int) {
	if len(findings) == 0 {
		fmt.Fprintf(w, "  findings: none\n")
		return
	}
	kinds := make([]string, 0, len(findings))
	for k := range findings {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		fmt.Fprintf(w, "  findings: %s x%d\n", k, findings[k])
	}
}

// --- the build itself -----------------------------------------------------

// flowBuilder holds one `flow build` run. It exists so the steps stay small
// and independently readable rather than one long RunE.
type flowBuilder struct {
	store       *store.Store
	root        string
	profilePath string

	profile []gocover.Block
	byFile  map[string][]store.SymbolRow
	// queryTargets are the symbol ids of query nodes (`sql:` qualified names
	// emitted by the sqlc mapper). An edge into one of these PROVES a call
	// site issues a query, which is stronger evidence than the naming
	// heuristic and is graded accordingly.
	queryTargets map[int64]bool

	files    int
	symbols  int
	maxCx    int
	maxCxSym string
	conds    int
	indep    int
	findings map[string]int
	cov      struct {
		total, decidable, taken, undetermined int
		// any is set only by a symbol whose FILE the profile actually
		// covered, so a profile that names other packages produces no
		// summary at all rather than a 0% one.
		any bool
	}
	unmodelled   int
	unmodelledEg []string
	// gotoSuppressed counts the symbols whose reachability analysis was
	// declined because the graph omits their `goto` edges. See
	// notReachabilityAnalysed.
	gotoSuppressed   int
	gotoSuppressedEg []string
	warnings         []string
}

func (b *flowBuilder) loadProfile() error {
	if b.profilePath == "" {
		return nil
	}
	f, err := os.Open(b.profilePath)
	if err != nil {
		return fmt.Errorf("open coverprofile %s: %w", b.profilePath, err)
	}
	defer f.Close()
	blocks, err := gocover.Parse(f)
	if err != nil {
		return fmt.Errorf("parse coverprofile %s: %w", b.profilePath, err)
	}
	b.profile = blocks
	return nil
}

func (b *flowBuilder) loadSymbols(ctx context.Context) error {
	syms, err := b.store.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		return fmt.Errorf("list symbols: %w", err)
	}
	b.byFile = map[string][]store.SymbolRow{}
	b.queryTargets = map[int64]bool{}
	for _, sym := range syms {
		if strings.HasPrefix(string(sym.QualifiedName), "sql:") {
			b.queryTargets[sym.ID] = true
			continue
		}
		if !strings.HasSuffix(sym.FilePath, ".go") {
			continue
		}
		if sym.Kind != shared.KindFunc && sym.Kind != shared.KindMethod {
			continue
		}
		b.byFile[sym.FilePath] = append(b.byFile[sym.FilePath], sym)
	}
	return nil
}

func (b *flowBuilder) run(ctx context.Context) error {
	b.findings = map[string]int{}
	paths := make([]string, 0, len(b.byFile))
	for p := range b.byFile {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, rel := range paths {
		if err := b.runFile(ctx, rel); err != nil {
			return err
		}
	}
	return nil
}

func (b *flowBuilder) runFile(ctx context.Context, rel string) error {
	fset := token.NewFileSet()
	abs := filepath.Join(b.root, filepath.FromSlash(rel))
	file, err := parser.ParseFile(fset, abs, nil, 0)
	if err != nil {
		// A file that no longer parses is a stale index, not a fatal error:
		// report it and keep going, so one bad file cannot cost the run.
		b.warnings = append(b.warnings, fmt.Sprintf("skipped %s: %v", rel, err))
		return nil
	}
	b.files++

	byLine := map[int]store.SymbolRow{}
	for _, sym := range b.byFile[rel] {
		byLine[sym.Line] = sym
	}
	blocks := cfg.ExecBlocksForFile(b.profile, rel)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		sym, ok := byLine[fset.Position(fn.Pos()).Line]
		if !ok {
			// Not every parsed function is indexed (generated files, symbols
			// the scanner skipped). Analysing one anyway would produce a
			// result with no symbol to attach it to.
			continue
		}
		if err := b.runFunc(ctx, fset, fn, sym, blocks); err != nil {
			return err
		}
	}
	return nil
}

func (b *flowBuilder) runFunc(
	ctx context.Context, fset *token.FileSet, fn *ast.FuncDecl,
	sym store.SymbolRow, blocks []cfg.ExecBlock,
) error {
	g, err := cfg.Build(fset, fn)
	if err != nil {
		return fmt.Errorf("build cfg for %s: %w", sym.QualifiedName, err)
	}
	analysis := cfg.Analyze(g, blocks)
	// The second return is a refusal, not a detail. Over a function containing
	// a `goto` the graph is missing edges, so a label reached only by that
	// goto has no predecessor here and would be reported as dead code that in
	// fact runs. Nothing is claimed in that case -- neither the count nor the
	// findings -- and the run says so at the end.
	unreachable, unreachableSound := g.Unreachable()
	if !unreachableSound {
		b.notReachabilityAnalysed(sym)
	}

	if err := b.store.ControlFlow().Replace(ctx, toStoreFlow(sym.ID, g, analysis, len(unreachable))); err != nil {
		return fmt.Errorf("persist cfg for %s: %w", sym.QualifiedName, err)
	}
	// measured is per FILE, not per run. A profile that covers some other
	// package yields no blocks for this file, and writing a zero-coverage row
	// from it would record "no branch was taken" where the truth is "nothing
	// measured this function".
	measured := b.profilePath != "" && len(blocks) > 0
	if err := b.persistDecisionCoverage(ctx, sym.ID, analysis, measured); err != nil {
		return fmt.Errorf("%s: %w", sym.QualifiedName, err)
	}

	findings := b.findingsFor(ctx, fset, fn, g, analysis, sym, unreachable)
	if err := b.store.ControlFlow().ReplaceFindings(ctx, sym.ID, findings); err != nil {
		return fmt.Errorf("persist findings for %s: %w", sym.QualifiedName, err)
	}

	b.tally(sym, g, analysis, findings, measured)
	b.noteUnmodelled(sym, g)
	return nil
}

// notReachabilityAnalysed records a symbol whose `flow.unreachable` analysis
// was declined. Counting them rather than logging each one keeps a large repo
// readable, but the count is always printed: a diagnostic that silently stops
// running for a subset of the code is worse than one that never ran.
func (b *flowBuilder) notReachabilityAnalysed(sym store.SymbolRow) {
	b.gotoSuppressed++
	if len(b.gotoSuppressedEg) < 3 {
		b.gotoSuppressedEg = append(b.gotoSuppressedEg, string(sym.QualifiedName))
	}
}

// noteUnmodelled accumulates the constructs the builder could not represent
// exactly -- a `goto`, or a function literal with branches of its own. They
// are counted rather than logged per symbol (a large repo has thousands) but
// they are never silently dropped: they bound how much the complexity and
// coverage numbers above can be trusted.
func (b *flowBuilder) noteUnmodelled(sym store.SymbolRow, g *cfg.Graph) {
	if len(g.Warnings) == 0 {
		return
	}
	b.unmodelled += len(g.Warnings)
	if len(b.unmodelledEg) < 3 {
		b.unmodelledEg = append(b.unmodelledEg,
			fmt.Sprintf("%s: %s", sym.QualifiedName, g.Warnings[0]))
	}
}

func (b *flowBuilder) tally(
	sym store.SymbolRow, g *cfg.Graph, a cfg.Analysis,
	findings []store.FlowFinding, measured bool,
) {
	b.symbols++
	b.conds += a.Conditions
	b.indep += a.ConditionsIndependent
	if c := g.Complexity(); c > b.maxCx {
		b.maxCx, b.maxCxSym = c, string(sym.QualifiedName)
	}
	for _, f := range findings {
		b.findings[f.Kind]++
	}
	if measured {
		b.cov.any = true
		b.cov.total += a.OutcomesTotal
		b.cov.decidable += a.OutcomesDecidable
		b.cov.taken += a.OutcomesTaken
		b.cov.undetermined += a.OutcomesUndetermined
	}
}

// findingsFor turns the analyses into persisted diagnostics. The three kinds
// are deliberately distinct: unreachable code is a structural fact, an
// untested branch is a fact about the test run, and a query in a loop is a
// suspicion with a confidence attached.
func (b *flowBuilder) findingsFor(
	ctx context.Context, fset *token.FileSet, fn *ast.FuncDecl, g *cfg.Graph,
	a cfg.Analysis, sym store.SymbolRow, unreachable []int,
) []store.FlowFinding {
	var out []store.FlowFinding
	for _, idx := range unreachable {
		out = append(out, store.FlowFinding{
			Kind: store.FindingUnreachable, Confidence: "high",
			Line:   g.Blocks[idx].StartLine,
			Detail: "no path from the function entry reaches this block",
		})
	}
	for _, arm := range a.Arms {
		// Only outcomes the profile can actually judge become findings. An
		// UNDETERMINED outcome is not evidence of an untested branch, and
		// reporting it as one would bury the real ones.
		if !arm.Decidable() || arm.Taken() {
			continue
		}
		out = append(out, store.FlowFinding{
			Kind: store.FindingUntestedBranch, Confidence: "high",
			Line:   arm.Line,
			Detail: fmt.Sprintf("%s outcome %q was never taken (%s)", arm.Kind, arm.Label, arm.Reason),
		})
	}
	for _, q := range cfg.DetectQueryInLoop(fset, fn, g, cfg.QueryLoopOptions{
		KnownQueryLines: b.knownQueryLines(ctx, sym.ID),
	}) {
		out = append(out, store.FlowFinding{
			Kind: store.FindingQueryInLoop, Confidence: string(q.Confidence),
			Line: q.QueryLine, RelatedLine: q.LoopLine,
			Detail: fmt.Sprintf("%s in a %s loop at line %d: %s. %s",
				q.QueryText, q.LoopKind, q.LoopLine, q.Evidence, q.Caveat),
		})
	}
	return out
}

// knownQueryLines are the call sites this symbol has a graph edge from into a
// query node — proof rather than a name match.
func (b *flowBuilder) knownQueryLines(ctx context.Context, symbolID int64) map[int]bool {
	if len(b.queryTargets) == 0 {
		return nil
	}
	edges, err := b.store.Edges().Out(ctx, symbolID)
	if err != nil {
		// A failed edge read costs confidence, not the run: the naming
		// heuristic still applies, and the finding will say so.
		b.warnings = append(b.warnings, fmt.Sprintf("could not read query edges for symbol %d: %v", symbolID, err))
		return nil
	}
	out := map[int]bool{}
	for _, e := range edges {
		if b.queryTargets[e.ToID] {
			out[e.Line] = true
		}
	}
	return out
}

func toStoreFlow(symbolID int64, g *cfg.Graph, a cfg.Analysis, unreachable int) store.SymbolFlow {
	flow := store.SymbolFlow{
		SymbolID: symbolID,
		Metrics: store.FlowMetrics{
			SymbolID:              symbolID,
			Complexity:            g.Complexity(),
			Decisions:             len(g.Decisions),
			BranchArms:            g.BranchArms(),
			Conditions:            a.Conditions,
			ConditionsIndependent: a.ConditionsIndependent,
			Defers:                g.Defers,
			UnreachableBlocks:     unreachable,
		},
	}
	for _, blk := range g.Blocks {
		flow.Blocks = append(flow.Blocks, store.FlowBlock{
			Index: blk.Index, Kind: string(blk.Kind),
			StartLine: blk.StartLine, EndLine: blk.EndLine,
		})
	}
	for i, e := range g.Edges {
		flow.Edges = append(flow.Edges, store.FlowEdge{
			Index: i, From: e.From, To: e.To,
			Kind: string(e.Kind), Condition: e.Condition,
		})
	}
	return flow
}

func (b *flowBuilder) result() flowBuildResult {
	res := flowBuildResult{
		Root:            b.root,
		FilesParsed:     b.files,
		SymbolsAnalyzed: b.symbols,
		MaxComplexity:   b.maxCx,
		MaxComplexityAt: b.maxCxSym,
		Conditions:      b.conds,
		Independent:     b.indep,
		Findings:        b.findings,
		MCDC:            cfg.MCDCNotDerivable,
	}
	if b.cov.any {
		dc := &flowCoverageSummary{
			OutcomesTotal:     b.cov.total,
			OutcomesDecidable: b.cov.decidable,
			OutcomesTaken:     b.cov.taken,
			Undetermined:      b.cov.undetermined,
			Source:            b.profilePath,
		}
		if dc.OutcomesDecidable > 0 {
			pct := 100 * float64(dc.OutcomesTaken) / float64(dc.OutcomesDecidable)
			dc.Percent = &pct
		}
		res.DecisionCoverage = dc
	}
	return res
}

// persistDecisionCoverage writes the per-symbol measurement so `flow show`
// can report one symbol without re-reading the profile. When the symbol's file
// was not measured it writes NOTHING -- an absent row means "not measured",
// which must never be stored as a zero-coverage row and read back as "no
// branch was taken".
//
// `measured` is deliberately per FILE and not per run. Supplying a profile is
// not the same as that profile covering this file: profiling one package,
// running an integration-test profile, or analysing a package with no tests at
// all yields a profile that names other files, and every symbol here would
// then be recorded as measured at 0% on the strength of a file the run never
// looked at.
func (b *flowBuilder) persistDecisionCoverage(
	ctx context.Context, symbolID int64, a cfg.Analysis, measured bool,
) error {
	if !measured {
		return nil
	}
	if err := b.store.ControlFlow().SetDecisionCoverage(ctx, store.DecisionCoverage{
		SymbolID:          symbolID,
		OutcomesTotal:     a.OutcomesTotal,
		OutcomesDecidable: a.OutcomesDecidable,
		OutcomesTaken:     a.OutcomesTaken,
		Source:            b.profilePath,
	}); err != nil {
		return fmt.Errorf("persist decision coverage: %w", err)
	}
	return nil
}

// --- show -----------------------------------------------------------------

type flowShowResult struct {
	Symbol           string                  `json:"symbol"`
	File             string                  `json:"file"`
	Line             int                     `json:"line"`
	Metrics          store.FlowMetrics       `json:"metrics"`
	Blocks           []store.FlowBlock       `json:"blocks"`
	Edges            []store.FlowEdge        `json:"edges"`
	DecisionCoverage *store.DecisionCoverage `json:"decision_coverage,omitempty"`
	// DecisionCoverageStale reports that the stored measurement was taken
	// over a DIFFERENT graph than the one printed beside it: the source
	// changed and was re-analysed without a profile. Printing a coverage
	// number next to a graph it does not describe is worse than printing
	// none, so the mismatch is surfaced rather than smoothed over.
	DecisionCoverageStale bool                `json:"decision_coverage_stale,omitempty"`
	Findings              []store.FlowFinding `json:"findings,omitempty"`
	MCDC                  string              `json:"mcdc"`
}

func newFlowShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <symbol>",
		Short: "Show one symbol's control-flow graph, complexity and decision coverage",
		Long: `show prints the stored CFG for one symbol: its blocks and edges (with
the source condition on each branch), its complexity, its decision
coverage where that was measured, and its findings.

The symbol may be given exactly or as a unique substring of a
qualified name. Run 'atlas flow build' first.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return runFlowShow(cmd, args[0]) },
	}
}

func runFlowShow(cmd *cobra.Command, query string) error {
	ctx := cmdContext(cmd)
	s, err := openStoreForRead(ctx)
	if err != nil {
		return err
	}
	defer s.Close()

	sym, err := resolveFlowSymbol(ctx, s, query)
	if err != nil {
		return err
	}
	flow, err := s.ControlFlow().Get(ctx, sym.ID)
	if err != nil {
		return fmt.Errorf("no control flow recorded for %s (run `atlas flow build`): %w", sym.QualifiedName, err)
	}
	res := flowShowResult{
		Symbol:  string(sym.QualifiedName),
		File:    sym.FilePath,
		Line:    sym.Line,
		Metrics: flow.Metrics,
		Blocks:  flow.Blocks,
		Edges:   flow.Edges,
		MCDC:    cfg.MCDCNotDerivable,
	}
	if dc, err := s.ControlFlow().GetDecisionCoverage(ctx, sym.ID); err == nil {
		res.DecisionCoverage = &dc
		// The outcome count is a property of the graph, so a measurement whose
		// total disagrees with the current graph was taken over a different
		// version of this function.
		res.DecisionCoverageStale = dc.OutcomesTotal != flow.Metrics.BranchArms
	}
	if fs, err := s.ControlFlow().Findings(ctx, ""); err == nil {
		for _, f := range fs {
			if f.SymbolID == sym.ID {
				res.Findings = append(res.Findings, f)
			}
		}
	}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "flow.show", map[string]any{"symbol": query}, res, nil)
	}
	writeFlowShow(cmd.OutOrStdout(), res)
	return nil
}

func writeFlowShow(w io.Writer, res flowShowResult) {
	fmt.Fprintf(w, "%s  %s:%d\n", res.Symbol, res.File, res.Line)
	fmt.Fprintf(w, "  complexity %d   decisions %d   branch arms %d   defers %d\n",
		res.Metrics.Complexity, res.Metrics.Decisions, res.Metrics.BranchArms, res.Metrics.Defers)
	fmt.Fprintf(w, "  conditions %d (%d independently exercisable in principle)\n",
		res.Metrics.Conditions, res.Metrics.ConditionsIndependent)
	writeFlowShowCoverage(w, res.DecisionCoverage, res.Metrics.BranchArms)
	fmt.Fprintf(w, "  MC/DC: %s\n", res.MCDC)
	fmt.Fprintf(w, "  blocks:\n")
	for _, b := range res.Blocks {
		fmt.Fprintf(w, "    %3d  %-7s lines %d-%d\n", b.Index, b.Kind, b.StartLine, b.EndLine)
	}
	fmt.Fprintf(w, "  edges:\n")
	for _, e := range res.Edges {
		line := fmt.Sprintf("    %3d -> %-3d %-11s", e.From, e.To, e.Kind)
		if e.Condition != "" {
			line += "  [" + e.Condition + "]"
		}
		fmt.Fprintf(w, "%s\n", strings.TrimRight(line, " "))
	}
	if len(res.Findings) > 0 {
		fmt.Fprintf(w, "  findings:\n")
		for _, f := range res.Findings {
			writeFlowFinding(w, "    ", "", f)
		}
	}
}

func writeFlowShowCoverage(w io.Writer, dc *store.DecisionCoverage, branchArms int) {
	if dc == nil {
		// No row at all. That is "never measured", which is a different fact
		// from 0% and from UNAVAILABLE, and it is stated as its own sentence
		// so nobody reads a blank as a zero. A profile that did not cover
		// this symbol's file leaves exactly this state.
		fmt.Fprintf(w, "  decision coverage: not measured -- no profile has covered this file "+
			"(run `atlas flow build --profile cover.out` over a profile that includes it)\n")
		return
	}
	pct, ok := dc.Percent()
	if !ok {
		fmt.Fprintf(w, "  decision coverage: UNAVAILABLE -- all %d outcome(s) are UNDETERMINED from statement coverage\n",
			dc.OutcomesTotal)
	} else {
		fmt.Fprintf(w, "  decision coverage: %.1f%% (%d of %d decidable outcomes taken; %d UNDETERMINED)\n",
			pct, dc.OutcomesTaken, dc.OutcomesDecidable, dc.Undetermined())
	}
	if dc.OutcomesTotal != branchArms {
		fmt.Fprintf(w, "  WARNING: that measurement was taken over a different version of this function "+
			"(it counted %d outcome(s); this graph has %d). Re-run `atlas flow build --profile ...`.\n",
			dc.OutcomesTotal, branchArms)
	}
	fmt.Fprintf(w, "  (statement coverage is a different question and is reported by `atlas cov`; the two are never blended)\n")
}

// resolveFlowSymbol accepts an exact qualified name or a unique substring.
// An ambiguous query is an error rather than a silent first-match: showing
// the flow of a symbol the user did not ask for is worse than making them
// type more.
func resolveFlowSymbol(ctx context.Context, s *store.Store, query string) (store.SymbolRow, error) {
	if sym, err := s.Symbols().FindByQualifiedName(ctx, shared.SymbolID(query)); err == nil {
		return sym, nil
	}
	all, err := s.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		return store.SymbolRow{}, fmt.Errorf("list symbols: %w", err)
	}
	var hits []store.SymbolRow
	for _, sym := range all {
		if strings.Contains(string(sym.QualifiedName), query) {
			hits = append(hits, sym)
		}
	}
	switch len(hits) {
	case 0:
		return store.SymbolRow{}, fmt.Errorf("no symbol matches %q", query)
	case 1:
		return hits[0], nil
	default:
		names := make([]string, 0, len(hits))
		for _, h := range hits[:min(len(hits), 5)] {
			names = append(names, string(h.QualifiedName))
		}
		return store.SymbolRow{}, fmt.Errorf("%q matches %d symbols (%s...); use a fully qualified name",
			query, len(hits), strings.Join(names, ", "))
	}
}

// --- findings -------------------------------------------------------------

type flowFindingRow struct {
	Symbol string `json:"symbol"`
	store.FlowFinding
}

type flowFindingsResult struct {
	Findings []flowFindingRow `json:"findings"`
	// Caveat is present only when the list contains a query-in-loop finding,
	// which is the one kind that is a suspicion rather than a fact.
	Caveat string `json:"caveat,omitempty"`
}

func newFlowFindingsCmd() *cobra.Command {
	var kind string
	cmd := &cobra.Command{
		Use:   "findings",
		Short: "List control-flow findings (N+1 candidates, unreachable code, untested branches)",
		Long: `findings lists what 'atlas flow build' recorded, highest confidence
first.

Every row carries a confidence, and the confidence is the point: a
query inside a loop is a smell and not a proof -- the collection may
be tiny, the call may hit a cache -- and a report that does not say
how sure it is gets muted the first time it is wrong.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runFlowFindings(cmd, kind) },
	}
	cmd.Flags().StringVar(&kind, "kind", "",
		"limit to one kind (flow.query-in-loop|flow.unreachable|flow.untested-branch)")
	return cmd
}

func runFlowFindings(cmd *cobra.Command, kind string) error {
	ctx := cmdContext(cmd)
	s, err := openStoreForRead(ctx)
	if err != nil {
		return err
	}
	defer s.Close()

	rows, err := s.ControlFlow().Findings(ctx, kind)
	if err != nil {
		return err
	}
	names, err := flowSymbolNames(ctx, s)
	if err != nil {
		return err
	}
	var res flowFindingsResult
	for _, r := range rows {
		res.Findings = append(res.Findings, flowFindingRow{Symbol: names[r.SymbolID], FlowFinding: r})
		if r.Kind == store.FindingQueryInLoop {
			// Carried only when there is something for it to qualify: a
			// caveat printed under a list it does not apply to teaches the
			// reader to skip caveats.
			res.Caveat = cfg.NPlusOneCaveat
		}
	}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "flow.findings", map[string]any{"kind": kind}, res, nil)
	}
	w := cmd.OutOrStdout()
	if len(res.Findings) == 0 {
		fmt.Fprintf(w, "no control-flow findings (run `atlas flow build` first)\n")
		return nil
	}
	for _, f := range res.Findings {
		writeFlowFinding(w, "", f.Symbol, f.FlowFinding)
	}
	if res.Caveat != "" {
		fmt.Fprintf(w, "\ncaveat: %s\n", res.Caveat)
	}
	return nil
}

func writeFlowFinding(w io.Writer, indent, symbol string, f store.FlowFinding) {
	where := fmt.Sprintf("line %d", f.Line)
	if f.RelatedLine > 0 {
		where += fmt.Sprintf(" (loop at line %d)", f.RelatedLine)
	}
	name := ""
	if symbol != "" {
		name = symbol + "  "
	}
	fmt.Fprintf(w, "%s%s%s  %s  confidence=%s\n", indent, name, f.Kind, where, f.Confidence)
	if f.Detail != "" {
		fmt.Fprintf(w, "%s    %s\n", indent, f.Detail)
	}
}

func flowSymbolNames(ctx context.Context, s *store.Store) (map[int64]string, error) {
	all, err := s.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		return nil, fmt.Errorf("list symbols: %w", err)
	}
	out := make(map[int64]string, len(all))
	for _, sym := range all {
		out[sym.ID] = string(sym.QualifiedName)
	}
	return out, nil
}
