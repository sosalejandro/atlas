package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/churn"
	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/contract"
	"github.com/sosalejandro/atlas/packages/onboard"
	"github.com/sosalejandro/atlas/packages/sqlops"
	"github.com/sosalejandro/atlas/packages/store"
)

// newOnboardCmd implements `atlas onboard` — the first run on a repository
// nobody has annotated.
//
// It exists because every other verb in this tool is gated behind somebody
// having written annotations first, which makes the first run a symbol count
// and a shrug. onboard runs the whole chain — scan, ingest, SQL inventory,
// route extraction, churn — and ends on a provisional capability map plus
// the findings that map made visible.
//
// Nothing it infers is written to the registry. See packages/onboard.
func newOnboardCmd() *cobra.Command {
	var f onboardFlags
	cmd := &cobra.Command{
		Use:   "onboard",
		Short: "First run: infer a provisional capability map and report what it makes visible",
		Long: `onboard scans the project, builds the SQL inventory, reads the HTTP routes,
mines git history, and derives a PROVISIONAL capability map from all of it —
without requiring a single @atlas:feature annotation.

It then reports what that map made visible: endpoints nothing tests, tables
written from more than one capability, code under active change with no test
reaching it, SQL advisories, dead-code candidates. The run ends with an
explicit account of what atlas could NOT see, and the CI snippet that turns
the whole thing into a gate.

INFERRED IS NOT DECLARED. Everything onboard proposes is namespaced under
"provisional:", is written to .atlas/provisional/capabilities.json, and is
absent from the features table. Atlas's registry is worth something only
because a human wrote every row in it, so the only path from a proposal into
the registry is 'atlas onboard promote', which writes an @atlas:feature
annotation into your source and lets the normal ingest pick it up.

Existing annotations are adopted as they are: a symbol that already belongs
to a declared feature is never re-proposed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runOnboard(cmd, f) },
	}
	cmd.Flags().StringVar(&f.root, "root", "", "project root to scan (default: repo root or cwd)")
	cmd.Flags().BoolVar(&f.skipSQL, "skip-sql", false,
		"skip the SQL inventory pass (capabilities then carry no data footprint)")
	cmd.Flags().BoolVar(&f.skipChurn, "skip-churn", false,
		"skip git history mining (drops the 'changing and untested' finding)")
	cmd.Flags().IntVar(&f.top, "top", 15,
		"how many provisional capabilities to print (0 = all); the full map is always written to disk")
	cmd.AddCommand(newOnboardPromoteCmd())
	return cmd
}

type onboardFlags struct {
	root      string
	skipSQL   bool
	skipChurn bool
	top       int
}

// onboardResult is the --json payload. It embeds the inference result rather
// than restating it so the JSON and the on-disk map cannot drift apart.
type onboardResult struct {
	onboard.Result
	MapPath string `json:"map_path"`
	// SQLScanned says whether the SQL pass ran. Without it the header's
	// "0 operations, 0 unresolved" is an unknown wearing a measurement's
	// clothes, and a JSON consumer reading stats.sql_operations has no way
	// to tell "this project has no queries" from "nobody looked".
	SQLScanned bool           `json:"sql_scanned"`
	Timings    onboardTimings `json:"timings_ms"`
	CISnippet  string         `json:"ci_snippet"`
}

// onboardTimings is the per-phase wall clock. The five-minute claim in the
// docs is only checkable if the tool reports what it actually spent.
type onboardTimings struct {
	Scan  int64 `json:"scan"`
	SQL   int64 `json:"sql"`
	Churn int64 `json:"churn"`
	Infer int64 `json:"infer"`
	Total int64 `json:"total"`
}

func runOnboard(cmd *cobra.Command, f onboardFlags) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	root := f.root
	if root == "" {
		root = loaded.repoRoot
	}
	started := time.Now()

	idx, s, warnings, err := onboardScan(ctx, root)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	timings := onboardTimings{Scan: time.Since(started).Milliseconds()}

	in := onboard.Input{
		Root:            root,
		ScannerWarnings: warnings,
		FilesExcluded:   countExcludedFiles(ctx, s),
		CoverageCommand: coverageNextCommand,
	}
	if err := onboardCollect(ctx, s, idx, root, f, &in, &timings); err != nil {
		return err
	}

	inferStart := time.Now()
	res := onboard.Infer(in)
	timings.Infer = time.Since(inferStart).Milliseconds()
	timings.Total = time.Since(started).Milliseconds()

	mapPath, err := onboard.Save(root, res, time.Now())
	if err != nil {
		return fmt.Errorf("onboard: %w", err)
	}

	out := onboardResult{
		Result: res, MapPath: mapPath, SQLScanned: in.SQLScanned,
		Timings: timings, CISnippet: ciSnippet,
	}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "onboard",
			map[string]any{"root": root, "skip_sql": f.skipSQL, "skip_churn": f.skipChurn},
			out, warnings)
	}
	printOnboard(cmd.OutOrStdout(), out, f.top)
	return nil
}

// onboardScan runs the index and the ingest, which is the only part of
// onboard that mutates the store. Everything after it reads.
func onboardScan(ctx context.Context, root string) (
	*codeindex.Index, *store.Store, []string, error,
) {
	idx, warnings, err := indexProjectFromConfig(ctx, root, true, nil, false)
	if err != nil {
		return nil, nil, nil, err
	}
	dbPath, err := resolveDBPath(loaded, flags.DBPath)
	if err != nil {
		return nil, nil, nil, err
	}
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("onboard: open store %s: %w", dbPath, err)
	}
	if _, err := s.Ingest(ctx, idx, store.IngestOptions{GeneratedGlobs: loaded.Scan.Generated}); err != nil {
		_ = s.Close()
		return nil, nil, nil, fmt.Errorf("onboard: ingest project: %w", err)
	}
	return idx, s, warnings, nil
}

// countExcludedFiles reads the scanner's exclusion ledger — the generated
// files and ignored packages it declined to index.
//
// Deliberately NOT IngestStats.FilesSkipped, which counts files whose hash
// was unchanged since the last scan. Those files ARE fully indexed; calling
// them "what atlas cannot see" would report a warm incremental cache as a
// coverage hole that grows every run.
func countExcludedFiles(ctx context.Context, s *store.Store) int {
	rows, err := s.SkippedFiles().List(ctx)
	if err != nil {
		return 0
	}
	return len(rows)
}

// onboardCollect fills the inference input from the store and the index.
//
// Every signal here is optional by design: a repository with no router, no
// SQL, no coverage and no git history still produces a map, and each missing
// signal turns into a stated limit rather than into silence.
func onboardCollect(
	ctx context.Context, s *store.Store, idx *codeindex.Index,
	root string, f onboardFlags, in *onboard.Input, timings *onboardTimings,
) error {
	symbols, err := s.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		return fmt.Errorf("onboard: list symbols: %w", err)
	}
	in.Symbols = symbols

	if in.Declared, err = collectDeclared(ctx, s); err != nil {
		return err
	}
	in.Routes = collectRoutes(ctx, idx, root, symbols)

	if !f.skipSQL {
		sqlStart := time.Now()
		ops, advisories, err := collectSQL(ctx, s, root)
		if err != nil {
			return err
		}
		in.SQLOps, in.Advisories, in.SQLScanned = ops, advisories, true
		timings.SQL = time.Since(sqlStart).Milliseconds()
	}
	if !f.skipChurn {
		churnStart := time.Now()
		in.Churn = collectChurn(ctx, root)
		timings.Churn = time.Since(churnStart).Milliseconds()
	}
	if in.Coverage, err = collectCoverage(ctx, s); err != nil {
		return err
	}
	in.Dead = collectDead(ctx, s)
	return nil
}

// collectDeclared reads the features a human actually declared, with their
// symbol links, so the inference can stay out of their way.
func collectDeclared(ctx context.Context, s *store.Store) ([]onboard.DeclaredFeature, error) {
	feats, err := s.Features().List(ctx, store.FeatureFilter{})
	if err != nil {
		return nil, fmt.Errorf("onboard: list features: %w", err)
	}
	out := make([]onboard.DeclaredFeature, 0, len(feats))
	for _, feat := range feats {
		links, err := s.FeatureSymbols().ListByFeature(ctx, feat.ID)
		if err != nil {
			return nil, fmt.Errorf("onboard: list feature symbols for %s: %w", feat.ID, err)
		}
		d := onboard.DeclaredFeature{ID: feat.ID, Title: feat.Title}
		for _, l := range links {
			d.SymbolIDs = append(d.SymbolIDs, l.SymbolID)
		}
		out = append(out, d)
	}
	return out, nil
}

// collectRoutes reads HTTP route registrations out of the source with the
// contract extractor, and resolves each handler back to its indexed symbol.
//
// The extractor is used in-memory and its Persist path is deliberately NOT
// called: persisting would write contract features into the registry, which
// is exactly the pollution this command exists to avoid.
func collectRoutes(ctx context.Context, idx *codeindex.Index, root string, symbols []store.SymbolRow) []onboard.Route {
	ext := contract.NewExtractor(contract.Options{
		ProjectRoot: root,
		SkipTS:      loaded.Scan.SkipTS,
		SkipGraphQL: true,
	})
	res, err := ext.Extract(ctx, idx)
	if err != nil || res == nil {
		// A router atlas cannot parse is reported as a limit by the
		// inference (no routes found), which is the honest outcome; failing
		// the whole first run over it would be the wrong trade.
		return nil
	}
	byName := make(map[string]int64, len(symbols))
	for _, sym := range symbols {
		byName[string(sym.QualifiedName)] = sym.ID
	}
	var out []onboard.Route
	for _, d := range res.Defs {
		if d.Kind != contract.KindRoute && d.Kind != contract.KindHumaOp {
			continue
		}
		handler := d.Operation.HandlerSym
		if handler == "" && len(d.Symbols) > 0 {
			handler = string(d.Symbols[0])
		}
		out = append(out, onboard.Route{
			Method: d.Operation.Method, Path: d.Operation.Path,
			HandlerSymbolID: byName[handler], HandlerName: handler,
			FilePath: d.FilePath, Line: d.Line,
		})
	}
	return out
}

// collectSQL builds the SQL inventory, persists it (the inventory is
// declared state about the working tree, not an inference), and computes the
// advisories from the stored rows so the first run and CI read the same
// evidence.
func collectSQL(ctx context.Context, s *store.Store, root string) ([]store.SQLOperationRecord, []sqlops.Advisory, error) {
	rep, err := sqlops.Analyze(sqlops.Options{Root: root, SkipAdvisories: true})
	if err != nil {
		return nil, nil, fmt.Errorf("onboard: sql scan: %w", err)
	}
	if err := s.SQLOps().Replace(ctx, toStoreRecords(rep.Operations)); err != nil {
		return nil, nil, fmt.Errorf("onboard: persist sql operations: %w", err)
	}
	tables, indexes := toStoreSchema(rep.Schema)
	if err := s.SQLOps().ReplaceSchema(ctx, tables, indexes); err != nil {
		return nil, nil, fmt.Errorf("onboard: persist sql schema: %w", err)
	}
	rows, err := s.SQLOps().List(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("onboard: list sql operations: %w", err)
	}
	schema, err := loadStoredSchema(ctx, s)
	if err != nil {
		return nil, nil, err
	}
	advise := sqlops.Advise(toSQLOpsOperations(rows), schema, sqlops.AdviseOptions{})
	return rows, advise.Advisories, nil
}

// collectChurn mines git history, returning nil when it cannot.
//
// The nil is returned explicitly rather than as a typed nil pointer: a
// (*churn.Report)(nil) stored in the interface would compare non-nil at the
// other end and the inference would call ForFiles on it.
func collectChurn(ctx context.Context, root string) onboard.ChurnLookup {
	rep, err := churn.Mine(ctx, churn.Options{Repo: root, ExcludeMessages: churn.DefaultExcludeMessages})
	if err != nil || rep == nil {
		return nil
	}
	return rep
}

// collectCoverage reduces the coverage frontier to two sets: which symbols a
// test actually executed, and which symbols the run reported on at all.
//
// The second set is what lets the inference say "measured and not executed"
// rather than falling back to colocation. Without it, a Go coverprofile
// ingested into a Go+TypeScript repository would make every TS capability
// look measured-and-dead when nothing measured it.
//
// A failure to read the frontier degrades to "no evidence", which the limits
// section then states, rather than failing the run.
func collectCoverage(ctx context.Context, s *store.Store) (onboard.CoverageEvidence, error) {
	frontier, err := s.Coverage().LatestFrontier(ctx)
	if err != nil || frontier.Empty() {
		return onboard.CoverageEvidence{}, nil //nolint:nilerr // absent coverage is a stated limit, not a failure.
	}
	results, err := s.Coverage().ListFrontierResults(ctx, frontier)
	if err != nil {
		return onboard.CoverageEvidence{}, nil //nolint:nilerr // same.
	}
	ev := onboard.CoverageEvidence{
		Available: true,
		Executed:  map[int64]bool{},
		Measured:  map[int64]bool{},
	}
	for _, r := range results {
		if r.SymbolID == nil {
			continue
		}
		ev.Measured[*r.SymbolID] = true
		if r.CoveredStmts > 0 || r.Status == store.StatusPass {
			ev.Executed[*r.SymbolID] = true
		}
	}
	return ev, nil
}

// collectDead asks the strictest question the store can answer: symbols with
// no incoming edge of ANY kind.
//
// This is deliberately narrower than `atlas codebase dead`'s --kind=import
// default. In Go, imports are module-level, so almost no function has an
// incoming import edge and the import view flags most of the codebase — a
// true statement that is useless as a first-run finding, because a list
// nobody can act on teaches the reader to skip the section. "Nothing
// references this at all" is short enough to read and surprising enough to
// be worth reading.
func collectDead(ctx context.Context, s *store.Store) []store.DeadCodeCandidate {
	rows, err := s.Symbols().FindDead(ctx, store.DeadCodeFilter{})
	if err != nil {
		return nil
	}
	return rows
}

// ciSnippet is the gate the first run hands the user. Every command in it is
// a real verb with real flags -- a snippet that does not run is worse than
// no snippet, because it is discovered in CI.
// It ingests coverage through `go test -coverprofile` + `cov sync` rather
// than through `cov run`, which is the richer per-test path but needs the
// shim armed first (`atlas cov shim init`). Without that, `cov run` exits 0
// having ingested nothing — a CI step that silently does nothing is the
// worst thing a starter snippet can contain.
const ciSnippet = `      - run: atlas init
      - run: go test ./... -coverprofile=cover.out -covermode=atomic
      - run: atlas cov sync --framework go-cover --input cover.out
      - run: atlas cov diff --base origin/main --fail-under 70
      - run: atlas audit --worst 10`

// coverageNextCommand is the shortest command sequence that actually gives
// atlas execution evidence. It is one string because it is printed as one
// "run this next" line, and splitting it would leave a reader holding half
// of a step.
const coverageNextCommand = "go test ./... -coverprofile=cover.out -covermode=atomic && " +
	"atlas cov sync --framework go-cover --input cover.out"

// --- rendering -----------------------------------------------------------

func printOnboard(w io.Writer, r onboardResult, top int) {
	printOnboardHeader(w, r)
	printOnboardFindings(w, r.Findings)
	printOnboardLimits(w, r.Limits)
	printOnboardMap(w, r, top)
	printOnboardNext(w, r)
}

func printOnboardHeader(w io.Writer, r onboardResult) {
	st := r.Stats
	fmt.Fprintf(w, "\natlas onboard — %s\n\n", r.Root)
	fmt.Fprintf(w, "  scanned      %d production symbols, %d test symbols   %s\n",
		st.ProductionSymbols, st.TestSymbols, ms(r.Timings.Scan))
	// With --skip-sql the pass never ran, so there is no count to print.
	// "0 operations, 0 unresolved 0.0s" would read as a measurement of a
	// project with no queries, complete with the time it took to find that
	// out; the honest line is the word "skipped".
	if r.SQLScanned {
		fmt.Fprintf(w, "  sql          %d operations, %d unresolved             %s\n",
			st.SQLOperations, st.SQLUnresolved, ms(r.Timings.SQL))
	} else {
		fmt.Fprintf(w, "  sql          skipped (--skip-sql), so no capability below has a data footprint\n")
	}
	fmt.Fprintf(w, "  routes       %d registrations\n", st.Routes)
	fmt.Fprintf(w, "  declared     %d features from annotations, adopted as they are\n", st.DeclaredFeatures)
	// Not printed as a fraction of the undeclared symbols: the directory
	// grouping is the fallback that claims whatever the stronger signals
	// left, so that fraction is 100% by construction and would read as a
	// measurement of something.
	fmt.Fprintf(w, "  PROVISIONAL  %d capabilities across %d domains, over %d undeclared symbols\n",
		st.ProvisionalCapabilities, st.Domains, st.UndeclaredSymbols)
	fmt.Fprintf(w, "  total        %s\n", ms(r.Timings.Total))
}

func printOnboardFindings(w io.Writer, findings []onboard.Finding) {
	fmt.Fprintf(w, "\nWHAT ATLAS FOUND\n")
	if len(findings) == 0 {
		fmt.Fprintf(w, "  Nothing worth flagging from the signals available on this run.\n")
		return
	}
	for i, f := range findings {
		tag := ""
		if f.Provisional {
			tag = "  [over provisional groupings]"
		}
		fmt.Fprintf(w, "\n  %d. [%s] %s%s\n", i+1, f.Severity, f.Title, tag)
		fmt.Fprintf(w, "     %s\n", f.Detail)
		for _, e := range f.Evidence {
			fmt.Fprintf(w, "       · %s%s\n", e.Detail, position(e.File, e.Line))
		}
		if f.Next != "" {
			fmt.Fprintf(w, "     → %s\n", f.Next)
		}
	}
}

func printOnboardLimits(w io.Writer, limits []onboard.Limit) {
	fmt.Fprintf(w, "\nWHAT ATLAS CANNOT SEE\n")
	for _, l := range limits {
		fmt.Fprintf(w, "\n  · %s\n", l.Detail)
		if l.Fix != "" {
			fmt.Fprintf(w, "    → %s\n", l.Fix)
		}
	}
}

// printOnboardMap renders the proposals in two sections rather than one
// ranked list.
//
// Routes rank above everything else — they are the most legible evidence a
// newcomer has — but they are also individually tiny, so a single list
// ordered that way fills the screen with one-symbol endpoints and never
// reaches the packages. Splitting gives each kind of evidence its own budget
// and keeps the section headings doing the explaining.
func printOnboardMap(w io.Writer, r onboardResult, top int) {
	var routes, structural []onboard.Capability
	for _, c := range r.Capabilities {
		if c.Source == onboard.SourceRoute {
			routes = append(routes, c)
			continue
		}
		structural = append(structural, c)
	}
	fmt.Fprintf(w, "\nPROVISIONAL CAPABILITY MAP (%d proposals)\n", len(r.Capabilities))
	printCapabilitySection(w, "from HTTP routes", routes, top)
	printCapabilitySection(w, "from code structure and test names", structural, top)
}

func printCapabilitySection(w io.Writer, heading string, caps []onboard.Capability, top int) {
	if len(caps) == 0 {
		return
	}
	shown := len(caps)
	if top > 0 && top < shown {
		shown = top
	}
	fmt.Fprintf(w, "\n  %s (%d of %d)\n\n", heading, shown, len(caps))
	for _, c := range caps[:shown] {
		fmt.Fprintf(w, "  %-40s %4d symbols  tests:%-16s%s\n",
			c.Ref(), c.Symbols, c.TestEvidence, dataFootprint(c))
		if len(c.Evidence) > 0 {
			e := c.Evidence[0]
			fmt.Fprintf(w, "      %s%s\n", e.Detail, position(e.File, e.Line))
		}
	}
	if shown < len(caps) {
		fmt.Fprintf(w, "\n  … %d more\n", len(caps)-shown)
	}
}

func printOnboardNext(w io.Writer, r onboardResult) {
	fmt.Fprintf(w, "\n  Full map written to %s.\n", r.MapPath)
	fmt.Fprintf(w, "  Nothing above was added to the registry.\n")
	fmt.Fprintf(w, "\nNEXT\n")
	fmt.Fprintf(w, "  atlas onboard promote --all              # preview the annotations that would make these real\n")
	fmt.Fprintf(w, "  atlas onboard promote --id <id> --apply  # accept one proposal\n")
	fmt.Fprintf(w, "  %s\n", coverageNextCommand)
	fmt.Fprintf(w, "                                          # ^ give atlas execution evidence\n")
	fmt.Fprintf(w, "\nCI (GitHub Actions steps):\n\n%s\n\n", r.CISnippet)
}

func dataFootprint(c onboard.Capability) string {
	if len(c.Reads) == 0 && len(c.Writes) == 0 {
		return ""
	}
	parts := make([]string, 0, 2)
	if len(c.Writes) > 0 {
		parts = append(parts, "writes "+strings.Join(c.Writes, ","))
	}
	if len(c.Reads) > 0 {
		parts = append(parts, "reads "+strings.Join(c.Reads, ","))
	}
	out := strings.Join(parts, "  ")
	if c.SQLUnresolved > 0 {
		out += fmt.Sprintf("  (+%d unreadable queries — lower bound)", c.SQLUnresolved)
	}
	return out
}

func position(file string, line int) string {
	switch {
	case file == "":
		return ""
	case line > 0:
		return fmt.Sprintf("  (%s:%d)", file, line)
	default:
		return fmt.Sprintf("  (%s)", file)
	}
}

func ms(v int64) string { return fmt.Sprintf("%6.1fs", float64(v)/1000) }

// --- promote -------------------------------------------------------------

// newOnboardPromoteCmd implements `atlas onboard promote` — the explicit
// user action that turns a proposal into a declaration.
//
// The default is a dry run. Promotion edits source files, and a verb that
// edits source because somebody typed it once is a verb people stop typing.
func newOnboardPromoteCmd() *cobra.Command {
	var (
		root  string
		ids   []string
		all   bool
		apply bool
	)
	cmd := &cobra.Command{
		Use:   "promote",
		Short: "Accept provisional capabilities by writing their @atlas:feature annotations",
		Long: `promote turns one or more provisional capabilities into real ones by
writing an '@atlas:feature <id>' annotation above the anchor declaration
each proposal cites. The annotation then reaches the features table through
the ordinary scan, exactly as a hand-written one would — there is no path
that writes the registry directly.

The default is a dry run: it prints the file, the line and the exact text it
would insert. Pass --apply to write.

A declaration that already carries an @atlas:feature, @atlas:contract or
@testreg annotation is skipped with a reason. Existing annotations are
adopted, never overwritten.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runOnboardPromote(cmd, root, ids, all, apply)
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "project root (default: repo root or cwd)")
	cmd.Flags().StringSliceVar(&ids, "id", nil,
		"provisional capability id to promote (repeatable; the 'provisional:' prefix is optional)")
	cmd.Flags().BoolVar(&all, "all", false, "promote every capability in the provisional map")
	cmd.Flags().BoolVar(&apply, "apply", false, "write the annotations (default is a dry run)")
	return cmd
}

// onboardPromoteResult is the --json payload for `atlas onboard promote`.
type onboardPromoteResult struct {
	Mode     string                  `json:"mode"` // "dry-run" | "apply"
	Promoted []onboard.PromoteResult `json:"promoted"`
	Applied  int                     `json:"applied"`
	Skipped  int                     `json:"skipped"`
}

func runOnboardPromote(cmd *cobra.Command, root string, ids []string, all, apply bool) error {
	if root == "" {
		root = loaded.repoRoot
	}
	if !all && len(ids) == 0 {
		return fmt.Errorf("onboard promote: pass --id <id> (repeatable) or --all")
	}
	doc, err := onboard.Load(root)
	if onboard.IsNotGenerated(err) {
		return fmt.Errorf("%w — run `atlas onboard` first", err)
	}
	if err != nil {
		return err //nolint:wrapcheck // already namespaced by packages/onboard.
	}

	selected, err := selectForPromotion(doc, ids, all)
	if err != nil {
		return err
	}

	res := onboardPromoteResult{Mode: "dry-run"}
	if apply {
		res.Mode = "apply"
	}
	// PromoteAll rather than a loop over Promote: several proposals can
	// anchor in one file, and each insertion moves the lines below it.
	promoted, err := onboard.PromoteAll(root, selected, apply)
	if err != nil {
		return err //nolint:wrapcheck // already namespaced by packages/onboard.
	}
	for _, pr := range promoted {
		switch {
		case pr.Applied:
			res.Applied++
		case pr.Skipped != "":
			res.Skipped++
		}
		res.Promoted = append(res.Promoted, pr)
	}

	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "onboard.promote",
			map[string]any{"root": root, "all": all, "ids": ids, "apply": apply}, res, nil)
	}
	printOnboardPromote(cmd.OutOrStdout(), res)
	return nil
}

// selectForPromotion resolves the requested ids against the map. An id that
// is not in the map is an error rather than a silent skip: in a script, a
// typo would otherwise be a green no-op.
func selectForPromotion(doc onboard.Document, ids []string, all bool) ([]onboard.Capability, error) {
	if all {
		return doc.Capabilities, nil
	}
	out := make([]onboard.Capability, 0, len(ids))
	for _, id := range ids {
		c, ok := doc.Find(id)
		if !ok {
			return nil, fmt.Errorf(
				"onboard promote: %q is not in the provisional map (%d capabilities); re-run `atlas onboard` if it is stale",
				id, len(doc.Capabilities))
		}
		out = append(out, c)
	}
	return out, nil
}

func printOnboardPromote(w io.Writer, res onboardPromoteResult) {
	noun := "capabilities"
	if len(res.Promoted) == 1 {
		noun = "capability"
	}
	fmt.Fprintf(w, "\natlas onboard promote (%s) — %d %s\n\n", res.Mode, len(res.Promoted), noun)
	for _, p := range res.Promoted {
		if p.Skipped != "" {
			fmt.Fprintf(w, "  skip  %-34s %s\n", p.ID, p.Skipped)
			continue
		}
		verb := "would write"
		if p.Applied {
			verb = "wrote"
		}
		fmt.Fprintf(w, "  %-11s %s:%d\n", verb, p.File, p.Line)
		fmt.Fprintf(w, "              %s\n", strings.TrimSpace(p.Text))
	}
	if res.Mode == "dry-run" {
		fmt.Fprintf(w, "\n  Nothing was written. Re-run with --apply to accept these.\n\n")
		return
	}
	fmt.Fprintf(w, "\n  %d annotations written, %d skipped. Re-run `atlas scan` to materialise them.\n\n",
		res.Applied, res.Skipped)
}
