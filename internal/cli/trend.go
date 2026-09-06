package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/audit"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
	"github.com/sosalejandro/atlas/packages/trend"
)

// trendFlags holds the parsed flag state for `atlas trend`.
//
// maxRegressionSet / denominatorToleranceSet exist because 0 is a MEANINGFUL
// value for both: a team asking for a zero-tolerance gate must not be handed
// the default instead. cobra's Changed() is the only honest source for that
// distinction.
type trendFlags struct {
	feature    string
	since      time.Duration
	limit      int
	compareTo  string
	head       string
	noBackfill bool

	maxRegression           float64
	maxRegressionSet        bool
	denominatorTolerance    float64
	denominatorToleranceSet bool
}

// newTrendCmd implements `atlas trend` plus its `record` subcommand.
func newTrendCmd() *cobra.Command {
	var f trendFlags

	cmd := &cobra.Command{
		Use:   "trend",
		Short: "Coverage/health series over time, with a PR regression gate",
		Long: `trend reads the per-commit measurement series recorded by
'atlas trend record' and answers the question a snapshot cannot:
is this getting better or worse?

With --compare-to <ref-or-sha> it reports the delta against that
commit and EXITS NON-ZERO when the score fell by more than
--max-regression. That gate is adoptable on a legacy codebase because
it never requires agreeing an absolute threshold -- it only asks that
this change did not make things worse.

Three things the output is careful about:

  * A commit with no coverage evidence has NO score, not a score of
    zero. Such points appear in the series as gaps and are never
    compared.
  * A score carries the size of the surface it was measured over.
    When that surface moves between two commits the delta is a fact
    about the measurement rather than about quality, and the report
    says so.
  * The gate tolerates --max-regression points of noise, and trips on
    a single FEATURE falling even when the project average is flat.

--compare-to gates THIS checkout: the head side is the point recorded
for the current HEAD sha (override with --head), never whatever point
happened to be recorded last. A commit with no point of its own is an
error, and a commit that lost a measurement the baseline had fails the
gate -- deleting the coverage step must not read as "no regression".

On a store that has coverage runs but no series, trend backfills the
points it can derive from those runs before reading (--no-backfill
turns that off). See docs/commands/trend.md for what backfill can and
cannot recover.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f.maxRegressionSet = cmd.Flags().Changed("max-regression")
			f.denominatorToleranceSet = cmd.Flags().Changed("denominator-tolerance")
			return runTrend(cmd, f)
		},
	}

	cmd.Flags().StringVar(&f.feature, "feature", "",
		"read the series for one feature id instead of the whole project")
	cmd.Flags().DurationVar(&f.since, "since", 0,
		"only include points measured within this window (e.g. 720h)")
	cmd.Flags().IntVar(&f.limit, "limit", 0,
		"cap the series to the N most recent points (0 = no cap)")
	cmd.Flags().StringVar(&f.compareTo, "compare-to", "",
		"compare the current commit against this git ref or recorded commit sha")
	cmd.Flags().StringVar(&f.head, "head", "",
		"commit whose recorded point is the head side of --compare-to (default: current git HEAD)")
	cmd.Flags().BoolVar(&f.noBackfill, "no-backfill", false,
		"do not derive missing history points from coverage runs already in the store")
	cmd.Flags().Float64Var(&f.maxRegression, "max-regression", trend.DefaultMaxRegression,
		"score drop, in points, tolerated before --compare-to fails")
	cmd.Flags().Float64Var(&f.denominatorTolerance, "denominator-tolerance", trend.DefaultDenominatorTolerance,
		"fractional change in measured surface before a comparison is flagged as not like-for-like")

	cmd.AddCommand(newTrendRecordCmd())
	return cmd
}

// trendResult is the JSON payload for `atlas trend`.
type trendResult struct {
	Series     trend.Series          `json:"series"`
	Comparison *trend.Report         `json:"comparison,omitempty"`
	Backfilled *trend.BackfillResult `json:"backfilled,omitempty"`
}

func runTrend(cmd *cobra.Command, f trendFlags) error {
	ctx := cmdContext(cmd)
	s, closeStore, err := openTrendStore(ctx)
	if err != nil {
		return err
	}
	defer closeStore()

	if err := validateTrendFeature(ctx, s, f.feature); err != nil {
		return err
	}

	res := trendResult{}
	var warnings []string
	if !f.noBackfill {
		filled, err := backfillTrendHistory(ctx, s)
		if err != nil {
			return err
		}
		if filled.Added > 0 {
			res.Backfilled = filled
			warnings = append(warnings, fmt.Sprintf(
				"backfilled %d point(s) from coverage runs already in the store; these are direct-link measurements, not `atlas trend record` points",
				filled.Added))
		}
	}

	series, truncated, err := readTrendSeries(ctx, s, f)
	if err != nil {
		return err
	}
	res.Series = series
	if truncated {
		warnings = append(warnings, fmt.Sprintf(
			"the series was truncated to the %d most recent points; older points exist and are not shown. Pass --limit to choose the window.",
			len(series.Points)))
	}

	if f.compareTo == "" {
		return emitTrend(cmd, f, res, warnings)
	}

	report, err := compareTrend(ctx, s, f)
	if err != nil {
		return err
	}
	res.Comparison = report
	warnings = append(warnings, report.Warnings...)
	if err := emitTrend(cmd, f, res, warnings); err != nil {
		return err
	}
	return trendGateError(report)
}

// trendGateError turns the gate verdict into the command's exit status.
// Non-zero exit is the whole product here: this is what a CI job gates on,
// and the message names the threshold so the log explains itself without the
// reader reconstructing the invocation.
//
// The two failing conditions are reported separately because they have
// different fixes: a score that fell needs tests, a measurement that vanished
// needs the coverage step back.
func trendGateError(report *trend.Report) error {
	switch {
	case report.Regressed:
		return fmt.Errorf("trend: regression gate failed: score fell by more than %.2f points against %s",
			report.MaxRegression, report.BaseCommit)
	case report.Unmeasured:
		return fmt.Errorf("trend: regression gate failed: %s carries no coverage measurement where %s had one; the measurement was lost, not the coverage",
			trendShortSHA(report.HeadCommit), trendShortSHA(report.BaseCommit))
	default:
		return nil
	}
}

// validateTrendFeature refuses an id the project does not have. Without this
// an unknown or misspelled id renders as a real series of gaps — visually
// identical to a feature that exists and has never been measured, which is
// the one reading a human must not be given by accident.
func validateTrendFeature(ctx context.Context, s *store.Store, id string) error {
	if id == "" {
		return nil
	}
	_, err := s.Features().Get(ctx, shared.FeatureID(id))
	if errors.Is(err, shared.ErrFeatureNotFound) || errors.Is(err, shared.ErrNotFound) {
		return fmt.Errorf("trend: no feature %q in this project; `atlas features list` shows the ids that exist", id)
	}
	if err != nil {
		return fmt.Errorf("trend: look up feature %q: %w", id, err)
	}
	return nil
}

// backfillTrendHistory derives the points the series does not have from the
// coverage runs the store already holds, so `atlas trend` says something
// useful on a repo that has been ingesting coverage for a year and has never
// run `atlas trend record`.
func backfillTrendHistory(ctx context.Context, s *store.Store) (*trend.BackfillResult, error) {
	res, err := trend.Backfill(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("trend: backfill: %w", err)
	}
	return &res, nil
}

// readTrendSeries loads the windowed series for the requested scope, and
// reports whether the read hit its cap.
func readTrendSeries(ctx context.Context, s *store.Store, f trendFlags) (trend.Series, bool, error) {
	filter := store.HistoryFilter{Limit: f.limit}
	if f.since > 0 {
		filter.Since = time.Now().UTC().Add(-f.since)
	}
	page, err := s.History().List(ctx, filter)
	if err != nil {
		return trend.Series{}, false, fmt.Errorf("trend: read history: %w", err)
	}
	series := trend.ProjectSeries(page.Points)
	if f.feature != "" {
		series = trend.FeatureSeries(page.Points, shared.FeatureID(f.feature))
	}
	series.Truncated = page.Truncated
	return series, page.Truncated, nil
}

// compareTrend builds the delta report between THIS CHECKOUT and the
// --compare-to baseline.
//
// Both sides are read from the FULL history rather than the windowed series:
// --since and --limit shape what a human reads, and must not silently move
// either end of a gate.
//
// The head side is the point recorded for the current HEAD sha, not the
// newest recorded point. On a CI runner those are routinely different rows —
// the last point written to a shared store may belong to another branch
// entirely — and gating a PR against someone else's measurement is worse than
// not gating at all, because it looks like it worked.
func compareTrend(ctx context.Context, s *store.Store, f trendFlags) (*trend.Report, error) {
	head, err := resolveTrendHead(ctx, s, f)
	if err != nil {
		return nil, err
	}
	base, err := resolveTrendBaseline(ctx, s, f.compareTo)
	if err != nil {
		return nil, err
	}
	if base.CommitSHA == head.CommitSHA {
		return nil, fmt.Errorf("trend: --compare-to %s resolves to the commit under test (%s); nothing to compare",
			f.compareTo, trendShortSHA(head.CommitSHA))
	}

	opts := trend.CompareOptions{}
	if f.maxRegressionSet {
		v := f.maxRegression
		opts.MaxRegression = &v
	}
	if f.denominatorToleranceSet {
		v := f.denominatorTolerance
		opts.DenominatorTolerance = &v
	}
	report := trend.Compare(base, head, opts)
	return &report, nil
}

// resolveTrendHead finds the recorded point for the commit under test.
//
// Failing loudly when there is none is the entire fix for "the gate compared
// whatever was recorded last": a PR that never recorded a point must not be
// waved through on the strength of another commit's number.
func resolveTrendHead(ctx context.Context, s *store.Store, f trendFlags) (store.HistoryPoint, error) {
	ref := f.head
	if ref == "" {
		ref = currentGitRef(loaded.repoRoot)
	}
	if ref == "" {
		return store.HistoryPoint{}, fmt.Errorf(
			"trend: cannot determine the commit under test (not a git repo?); pass --head <sha>")
	}
	point, err := lookupRecordedPoint(ctx, s, ref)
	if errors.Is(err, shared.ErrNotFound) {
		return store.HistoryPoint{}, fmt.Errorf(
			"trend: no measurement recorded for %s, the commit under test; run `atlas trend record` on this commit before gating "+
				"(--compare-to gates THIS checkout, never whatever point was recorded last)", trendShortSHA(ref))
	}
	if err != nil {
		return store.HistoryPoint{}, err
	}
	return point, nil
}

// resolveTrendBaseline turns a user-supplied ref into a recorded point.
func resolveTrendBaseline(ctx context.Context, s *store.Store, ref string) (store.HistoryPoint, error) {
	point, err := lookupRecordedPoint(ctx, s, ref)
	if errors.Is(err, shared.ErrNotFound) {
		return store.HistoryPoint{}, fmt.Errorf(
			"trend: no recorded measurement for %q; `atlas trend` lists what has been recorded", ref)
	}
	if err != nil {
		return store.HistoryPoint{}, err
	}
	return point, nil
}

// lookupRecordedPoint resolves a ref to a recorded point, returning
// shared.ErrNotFound when nothing matches so each caller can say what a miss
// means on its side of the comparison.
//
// Three attempts, narrowest first: an exact recorded sha, whatever `git
// rev-parse` makes of the ref (so `--compare-to main` works), and finally a
// recorded-sha prefix (so a human can type the first seven characters). The
// prefix attempt is last because it is the only ambiguous one.
func lookupRecordedPoint(ctx context.Context, s *store.Store, ref string) (store.HistoryPoint, error) {
	point, err := s.History().Get(ctx, ref)
	if err == nil {
		return point, nil
	}
	if !errors.Is(err, shared.ErrNotFound) {
		return store.HistoryPoint{}, fmt.Errorf("trend: look up %s: %w", ref, err)
	}

	if sha := revParse(loaded.repoRoot, ref); sha != "" {
		if point, err := s.History().Get(ctx, sha); err == nil {
			return point, nil
		}
	}

	sha, err := s.History().Resolve(ctx, ref)
	if errors.Is(err, shared.ErrNotFound) {
		return store.HistoryPoint{}, shared.ErrNotFound
	}
	if err != nil {
		return store.HistoryPoint{}, fmt.Errorf("trend: resolve %q: %w", ref, err)
	}
	point, err = s.History().Get(ctx, sha)
	if err != nil {
		return store.HistoryPoint{}, fmt.Errorf("trend: load %s: %w", sha, err)
	}
	return point, nil
}

// revParse resolves an arbitrary git ref to a sha, or "" when the ref is
// unknown or the directory is not a git repo. Distinct from currentGitRef
// (snapshot.go), which only ever resolves HEAD.
func revParse(dir, ref string) string {
	if dir == "" || ref == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--verify", ref+"^{commit}").Output() //nolint:gosec // ref is a user-supplied git ref passed as an argv element, never a shell string.
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func emitTrend(cmd *cobra.Command, f trendFlags, res trendResult, warnings []string) error {
	if flags.JSON {
		args := map[string]any{
			"feature":     f.feature,
			"since":       f.since.String(),
			"limit":       f.limit,
			"compare_to":  f.compareTo,
			"head":        f.head,
			"no_backfill": f.noBackfill,
		}
		return emitJSON(stdoutOrJSON(cmd), "trend", args, res, warnings)
	}
	w := cmd.OutOrStdout()
	printTrendSeries(w, res.Series)
	if res.Comparison != nil {
		printTrendReport(w, *res.Comparison)
	}
	// One warnings block for the whole command: the report's caveats, the
	// truncation notice and the backfill notice are all things a reader must
	// see before trusting the table above, and splitting them across two
	// sections is how one of them gets skimmed past.
	if len(warnings) > 0 {
		fmt.Fprintln(w, "\nwarnings:")
		for _, warn := range warnings {
			fmt.Fprintf(w, "  - %s\n", warn)
		}
	}
	if res.Comparison != nil {
		printTrendGate(w, *res.Comparison)
	}
	return nil
}

// printTrendSeries renders the series as a compact table. Deliberately not a
// chart: an ASCII sparkline of a dozen points implies a resolution the data
// does not have, and cannot show a gap honestly.
func printTrendSeries(w io.Writer, s trend.Series) {
	if len(s.Points) == 0 {
		fmt.Fprintf(w, "trend: no history recorded for %s; run `atlas trend record`\n", s.Scope)
		return
	}
	truncated := ""
	if s.Truncated {
		truncated = "  (truncated: older points exist)"
	}
	fmt.Fprintf(w, "trend  scope=%s  points=%d%s\n\n", s.Scope, len(s.Points), truncated)
	fmt.Fprintf(w, "%-14s  %-16s  %8s  %8s  %12s\n", "COMMIT", "MEASURED", "SCORE", "SURFACE", "DELTA")

	var prev *float64
	for _, p := range s.Points {
		score, delta := "       -", "no evidence"
		if p.Measured() {
			score = fmt.Sprintf("%8.2f", *p.Score)
			delta = "          -"
			if prev != nil {
				delta = fmt.Sprintf("%+11.2f", *p.Score-*prev)
			}
			prev = p.Score
		}
		fmt.Fprintf(w, "%-14s  %-16s  %8s  %8d  %12s\n",
			trendShortSHA(p.CommitSHA), p.MeasuredAt.UTC().Format("2006-01-02 15:04"),
			score, p.Denominator, delta)
	}
}

// printTrendReport renders the --compare-to delta and the gate verdict.
func printTrendReport(w io.Writer, r trend.Report) {
	fmt.Fprintf(w, "\ncompare %s -> %s\n", r.BaseCommit, r.HeadCommit)
	fmt.Fprintf(w, "  score    %s\n", formatTrendDelta(r.Project))
	fmt.Fprintf(w, "  surface  %d -> %d (%+.1f%%)\n",
		r.Project.BaseDenominator, r.Project.HeadDenominator, r.Project.DenominatorShift*100)

	if moved := trendMovers(r.Features); len(moved) > 0 {
		fmt.Fprintln(w, "\nfeatures (worst first):")
		for _, c := range moved {
			fmt.Fprintf(w, "  %-40s %s\n", c.Scope, formatTrendDelta(c))
		}
	}
}

// printTrendGate is the last line of output, after every caveat, because it
// is the line a reader acts on.
func printTrendGate(w io.Writer, r trend.Report) {
	gate := "PASS"
	if r.Failed {
		gate = "FAIL"
	}
	fmt.Fprintf(w, "\ngate: %s  (max regression %.2f points)\n", gate, r.MaxRegression)
	if r.Unmeasured {
		fmt.Fprintln(w, "  a scope measured at the baseline has no measurement now; a lost measurement fails the gate")
	}
}

// trendMovers keeps the feature rows worth a human's attention: anything that
// actually moved, or that has no delta to show. An unchanged feature in a
// hundred-feature repo is noise that buries the one that regressed.
func trendMovers(cs []trend.Comparison) []trend.Comparison {
	out := make([]trend.Comparison, 0, len(cs))
	for _, c := range cs {
		if c.Verdict != trend.VerdictUnchanged {
			out = append(out, c)
		}
	}
	return out
}

func formatTrendDelta(c trend.Comparison) string {
	delta := fmt.Sprintf("%11s", "-")
	if c.Delta != nil {
		delta = fmt.Sprintf("%+11.2f", *c.Delta)
	}
	line := fmt.Sprintf("%-8s -> %-8s %s  %s",
		formatTrendScore(c.BaseScore), formatTrendScore(c.HeadScore), delta,
		strings.ToUpper(string(c.Verdict)))
	if c.Note != "" {
		line += "  " + c.Note
	}
	return line
}

func formatTrendScore(v *float64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%.2f", *v)
}

func trendShortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func openTrendStore(ctx context.Context) (*store.Store, func(), error) {
	dbPath, err := resolveDBPath(loaded, flags.DBPath)
	if err != nil {
		return nil, nil, err
	}
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("trend: open store %s: %w", dbPath, err)
	}
	return s, func() { _ = s.Close() }, nil
}

// ---------------------------------------------------------------------------
// atlas trend record
// ---------------------------------------------------------------------------

// newTrendRecordCmd implements `atlas trend record`.
func newTrendRecordCmd() *cobra.Command {
	var (
		commit string
		note   string
		retain time.Duration
	)
	cmd := &cobra.Command{
		Use:   "record",
		Short: "Score the project now and append the result to the history series",
		Long: `record runs the audit against the current store and writes one
history point for the current commit.

Recording the same commit twice REPLACES its point rather than
appending a second one, so a CI retry leaves the series intact.

--retain prunes points older than the given window. Atlas keeps the
raw per-commit points and does not roll them up; a team that measures
every commit for years should pass --retain in CI.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTrendRecord(cmd, commit, note, retain)
		},
	}
	cmd.Flags().StringVar(&commit, "commit", "",
		"commit sha to record the point under (default: current HEAD)")
	cmd.Flags().StringVar(&note, "note", "",
		"free-form note stored with the point (a CI run id, a branch name)")
	cmd.Flags().DurationVar(&retain, "retain", 0,
		"after recording, delete points older than this window (0 = keep everything)")
	return cmd
}

// trendRecordResult is the JSON payload for `atlas trend record`.
type trendRecordResult struct {
	ID     int64              `json:"id"`
	Point  store.HistoryPoint `json:"point"`
	Pruned int64              `json:"pruned"`
}

func runTrendRecord(cmd *cobra.Command, commit, note string, retain time.Duration) error {
	ctx := cmdContext(cmd)
	s, closeStore, err := openTrendStore(ctx)
	if err != nil {
		return err
	}
	defer closeStore()

	if commit == "" {
		commit = currentGitRef(loaded.repoRoot)
	}
	if commit == "" {
		return fmt.Errorf("trend record: no commit sha; not a git repo? pass --commit")
	}

	opts := trend.CollectOptions{CommitSHA: commit, MeasuredAt: time.Now().UTC()}
	if note != "" {
		opts.Note = &note
	}
	scorer := audit.New(s, audit.Options{
		FreshnessWindow:     loaded.freshnessWindow(),
		ContractDriftWindow: loaded.contractDriftWindow(),
		GitBlame:            audit.NewGitBlame(loaded.repoRoot),
	})
	point, err := trend.Collect(ctx, s, scorer, opts)
	if err != nil {
		return fmt.Errorf("trend record: %w", err)
	}
	id, err := s.History().Record(ctx, point)
	if err != nil {
		return fmt.Errorf("trend record: persist %s: %w", commit, err)
	}
	point.ID = id

	var pruned int64
	if retain > 0 {
		if pruned, err = s.History().Prune(ctx, time.Now().UTC().Add(-retain)); err != nil {
			return fmt.Errorf("trend record: prune: %w", err)
		}
	}

	res := trendRecordResult{ID: id, Point: point, Pruned: pruned}
	if flags.JSON {
		args := map[string]any{"commit": commit, "note": note, "retain": retain.String()}
		return emitJSON(stdoutOrJSON(cmd), "trend.record", args, res, trendRecordWarnings(point))
	}
	printTrendRecord(cmd.OutOrStdout(), res)
	return nil
}

// trendRecordWarnings tells the operator when the point they just wrote is a
// gap rather than a measurement. Silence here is how a CI pipeline records a
// year of "no coverage" without anyone noticing.
func trendRecordWarnings(p store.HistoryPoint) []string {
	if p.Measured() {
		return nil
	}
	return []string{
		"no coverage evidence for any feature; recorded as a gap, not a zero. Run `atlas cov sync` before `atlas trend record`.",
	}
}

func printTrendRecord(w io.Writer, res trendRecordResult) {
	score := "no evidence"
	if res.Point.Measured() {
		score = fmt.Sprintf("%.2f", *res.Point.Score)
	}
	fmt.Fprintf(w, "trend point recorded  id=%d commit=%s score=%s surface=%d features=%d\n",
		res.ID, trendShortSHA(res.Point.CommitSHA), score, res.Point.Denominator, len(res.Point.Features))
	if !res.Point.Measured() {
		fmt.Fprintln(w, "  warning: no coverage evidence; recorded as a gap, not a zero")
	}
	if res.Pruned > 0 {
		fmt.Fprintf(w, "  pruned %d point(s) outside the retention window\n", res.Pruned)
	}
}
