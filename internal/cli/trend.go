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
	feature   string
	since     time.Duration
	limit     int
	compareTo string

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
    a single FEATURE falling even when the project average is flat.`,
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
		"compare the newest point against this git ref or recorded commit sha")
	cmd.Flags().Float64Var(&f.maxRegression, "max-regression", trend.DefaultMaxRegression,
		"score drop, in points, tolerated before --compare-to fails")
	cmd.Flags().Float64Var(&f.denominatorTolerance, "denominator-tolerance", trend.DefaultDenominatorTolerance,
		"fractional change in measured surface before a comparison is flagged as not like-for-like")

	cmd.AddCommand(newTrendRecordCmd())
	return cmd
}

// trendResult is the JSON payload for `atlas trend`.
type trendResult struct {
	Series     trend.Series  `json:"series"`
	Comparison *trend.Report `json:"comparison,omitempty"`
}

func runTrend(cmd *cobra.Command, f trendFlags) error {
	ctx := cmdContext(cmd)
	s, closeStore, err := openTrendStore(ctx)
	if err != nil {
		return err
	}
	defer closeStore()

	series, err := readTrendSeries(ctx, s, f)
	if err != nil {
		return err
	}

	if f.compareTo == "" {
		return emitTrend(cmd, f, trendResult{Series: series}, nil)
	}

	report, err := compareTrend(ctx, s, f)
	if err != nil {
		return err
	}
	res := trendResult{Series: series, Comparison: report}
	if err := emitTrend(cmd, f, res, report.Warnings); err != nil {
		return err
	}
	if report.Regressed {
		// Non-zero exit is the whole product here: this is what a CI job
		// gates on. The message names the threshold so the log explains
		// itself without the reader reconstructing the invocation.
		return fmt.Errorf("trend: regression gate failed: score fell by more than %.2f points against %s",
			report.MaxRegression, report.BaseCommit)
	}
	return nil
}

// readTrendSeries loads the windowed series for the requested scope.
func readTrendSeries(ctx context.Context, s *store.Store, f trendFlags) (trend.Series, error) {
	filter := store.HistoryFilter{Limit: f.limit}
	if f.since > 0 {
		filter.Since = time.Now().UTC().Add(-f.since)
	}
	points, err := s.History().List(ctx, filter)
	if err != nil {
		return trend.Series{}, fmt.Errorf("trend: read history: %w", err)
	}
	if f.feature != "" {
		return trend.FeatureSeries(points, shared.FeatureID(f.feature)), nil
	}
	return trend.ProjectSeries(points), nil
}

// compareTrend builds the delta report between the newest recorded point and
// the --compare-to baseline.
//
// The baseline and head are read from the FULL history rather than the
// windowed series: --since and --limit shape what a human reads, and must not
// silently move the baseline a gate fires on.
func compareTrend(ctx context.Context, s *store.Store, f trendFlags) (*trend.Report, error) {
	all, err := s.History().List(ctx, store.HistoryFilter{})
	if err != nil {
		return nil, fmt.Errorf("trend: read history: %w", err)
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("trend: no history recorded; run `atlas trend record` first")
	}
	head := all[len(all)-1]

	base, err := resolveTrendBaseline(ctx, s, f.compareTo)
	if err != nil {
		return nil, err
	}
	if base.CommitSHA == head.CommitSHA {
		return nil, fmt.Errorf("trend: --compare-to %s resolves to the newest recorded point (%s); nothing to compare",
			f.compareTo, head.CommitSHA)
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

// resolveTrendBaseline turns a user-supplied ref into a recorded point.
//
// Three attempts, narrowest first: an exact recorded sha, whatever `git
// rev-parse` makes of the ref (so `--compare-to main` works), and finally a
// recorded-sha prefix (so a human can type the first seven characters). The
// prefix attempt is last because it is the only ambiguous one.
func resolveTrendBaseline(ctx context.Context, s *store.Store, ref string) (store.HistoryPoint, error) {
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
		return store.HistoryPoint{}, fmt.Errorf(
			"trend: no recorded measurement for %q; `atlas trend` lists what has been recorded", ref)
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
			"feature":    f.feature,
			"since":      f.since.String(),
			"limit":      f.limit,
			"compare_to": f.compareTo,
		}
		return emitJSON(stdoutOrJSON(cmd), "trend", args, res, warnings)
	}
	w := cmd.OutOrStdout()
	printTrendSeries(w, res.Series)
	if res.Comparison != nil {
		printTrendReport(w, *res.Comparison)
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
	fmt.Fprintf(w, "trend  scope=%s  points=%d\n\n", s.Scope, len(s.Points))
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
	if len(r.Warnings) > 0 {
		fmt.Fprintln(w, "\nwarnings:")
		for _, warn := range r.Warnings {
			fmt.Fprintf(w, "  - %s\n", warn)
		}
	}

	gate := "PASS"
	if r.Regressed {
		gate = "FAIL"
	}
	fmt.Fprintf(w, "\ngate: %s  (max regression %.2f points)\n", gate, r.MaxRegression)
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
