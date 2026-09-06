package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/audit"
	"github.com/sosalejandro/atlas/packages/churn"
	"github.com/sosalejandro/atlas/packages/sprintplan"
	"github.com/sosalejandro/atlas/packages/store"
)

// hotspotsFlags holds the cobra-bound state for `atlas hotspots`. A struct
// rather than file-scope vars so repeat invocations in tests cannot bleed
// values into one another.
type hotspotsFlags struct {
	Top                 int
	WindowDays          int
	HalfLifeDays        int
	MaxFilesPerCommit   int
	ExcludeMessage      []string
	NoDefaultExclusions bool
	NoAuthorDiversity   bool
}

// newHotspotsCmd implements `atlas hotspots`.
func newHotspotsCmd() *cobra.Command {
	hf := &hotspotsFlags{}
	cmd := &cobra.Command{
		Use:   "hotspots",
		Short: "Rank features by change frequency x health gap",
		Long: `hotspots ranks the backlog by how often code changes multiplied by
how unhealthy it is, rather than by the gap alone.

A gap in code nobody has touched in two years and a gap in the file three
people edited last week score the same under 'atlas health'. Only the
second is worth a sprint. Change frequency is what separates them, and it
is already in git.

Both factors are printed next to the product, because a composite score
nobody can decompose is a score nobody trusts: gap comes from the audit
health score, churn from the commit history of the feature's files.

Churn deliberately does NOT count pure file moves, commits touching more
than --max-files-per-commit files (reformats, licence sweeps, dependency
bumps), or commits whose subject matches an exclusion pattern. Recent
commits count for more than old ones, halving every --half-life-days.

Files git has no history for -- brand new, or truncated away by a shallow
clone, which is the usual CI checkout -- are reported as UNKNOWN churn and
scored neutrally. They are never scored zero: that would silently drop the
newest code in the repository out of the backlog.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runHotspots(cmd, hf)
		},
	}
	cmd.Flags().IntVar(&hf.Top, "top", 0,
		"cap output to the top-N hotspots (0 = all)")
	registerChurnFlags(cmd, hf)
	return cmd
}

// churnFlagNames are the mining flags `atlas hotspots` and `atlas sprint
// --rank churn` share. Named once so the "you set a mining flag but did
// not ask for the churn ranking" check cannot drift from the registration.
var churnFlagNames = []string{
	"window-days", "half-life-days", "max-files-per-commit",
	"exclude-message", "no-default-exclusions", "no-author-diversity",
}

// registerChurnFlags binds the mining flags onto cmd.
//
// Shared with `atlas sprint` because the two commands document themselves
// as running the IDENTICAL weighting: a tuning flag that changes the
// ranking under one verb and is silently unavailable under the other makes
// that claim false.
func registerChurnFlags(cmd *cobra.Command, hf *hotspotsFlags) {
	cmd.Flags().IntVar(&hf.WindowDays, "window-days", int(churn.DefaultWindow/(24*time.Hour)),
		"how far back to mine commit history")
	cmd.Flags().IntVar(&hf.HalfLifeDays, "half-life-days", int(churn.DefaultHalfLife/(24*time.Hour)),
		"age at which a commit counts half as much (recency decay)")
	cmd.Flags().IntVar(&hf.MaxFilesPerCommit, "max-files-per-commit", churn.DefaultMaxFilesPerCommit,
		"drop commits touching more files than this (a sweep, not a change); negative disables")
	cmd.Flags().StringArrayVar(&hf.ExcludeMessage, "exclude-message", nil,
		"extra regexp matched against commit subjects; matching commits are not churn (repeatable)")
	cmd.Flags().BoolVar(&hf.NoDefaultExclusions, "no-default-exclusions", false,
		"do not apply the built-in chore/style/formatter subject exclusions")
	cmd.Flags().BoolVar(&hf.NoAuthorDiversity, "no-author-diversity", false,
		"score on commit frequency alone, ignoring how many people touch the file")
}

// changedChurnFlags names the mining flags the user actually set.
func changedChurnFlags(cmd *cobra.Command) []string {
	var out []string
	for _, n := range churnFlagNames {
		if f := cmd.Flags().Lookup(n); f != nil && f.Changed {
			out = append(out, "--"+n)
		}
	}
	return out
}

// validate rejects flag values churn.Options cannot represent.
//
// churn.Options spells "use the default" as the zero value, so
// `--window-days 0` would be silently replaced by 365 while the args block
// echoed the 0 the user typed. The churn-meta block exists precisely to
// make the score interpretable, so a flag it cannot report faithfully is
// an error rather than a quiet substitution.
func (hf *hotspotsFlags) validate() error {
	switch {
	case hf.WindowDays <= 0:
		return fmt.Errorf("--window-days must be at least 1 (got %d): "+
			"a zero-length history window has no churn to mine", hf.WindowDays)
	case hf.HalfLifeDays <= 0:
		return fmt.Errorf("--half-life-days must be at least 1 (got %d): "+
			"a zero half-life would weight every past commit at zero", hf.HalfLifeDays)
	case hf.MaxFilesPerCommit == 0:
		return fmt.Errorf("--max-files-per-commit 0 would drop every commit; " +
			"pass a positive limit, or a negative value to disable the rule")
	}
	return nil
}

// churnOptions turns the flags into a churn.Options.
//
// The exclusion patterns compose rather than replace: --exclude-message
// adds to the built-in set, and --no-default-exclusions is the separate,
// explicit way to drop it. Making the first flag silently replace the
// defaults would let one added pattern quietly re-admit every dependency
// bump in the history.
func (hf *hotspotsFlags) churnOptions(repoRoot string) churn.Options {
	opts := churn.Options{
		Repo:              repoRoot,
		Window:            time.Duration(hf.WindowDays) * 24 * time.Hour,
		HalfLife:          time.Duration(hf.HalfLifeDays) * 24 * time.Hour,
		MaxFilesPerCommit: hf.MaxFilesPerCommit,
	}
	if hf.NoAuthorDiversity {
		opts.AuthorBonus = -1
	}
	// Non-nil-but-empty is how churn.Options spells "no message exclusions
	// at all"; nil means "use the defaults".
	patterns := []string{}
	if !hf.NoDefaultExclusions {
		patterns = append(patterns, churn.DefaultExcludeMessages...)
	}
	opts.ExcludeMessages = append(patterns, hf.ExcludeMessage...)
	return opts
}

// hotspotsResult is the JSON payload for `atlas hotspots`.
type hotspotsResult struct {
	Items []sprintplan.Hotspot `json:"items"`
	Churn churnMeta            `json:"churn"`
}

// churnMeta describes the mining pass the scores came from. A churn score
// without the window and half-life it was taken under is not interpretable,
// and the skip counters are how a user finds out that the sweep they care
// about was filtered (or that their own real commits were).
type churnMeta struct {
	WindowDays            int     `json:"window_days"`
	HalfLifeDays          int     `json:"half_life_days"`
	MaxFilesPerCommit     int     `json:"max_files_per_commit"`
	Shallow               bool    `json:"shallow"`
	CommitsScanned        int     `json:"commits_scanned"`
	CommitsSkippedBulk    int     `json:"commits_skipped_bulk"`
	CommitsSkippedMessage int     `json:"commits_skipped_message"`
	FilesWithChurn        int     `json:"files_with_churn"`
	UnknownScore          float64 `json:"unknown_score"`
}

func newChurnMeta(rep *churn.Report, maxFiles int) churnMeta {
	return churnMeta{
		WindowDays:            int(rep.Window / (24 * time.Hour)),
		HalfLifeDays:          int(rep.HalfLife / (24 * time.Hour)),
		MaxFilesPerCommit:     maxFiles,
		Shallow:               rep.Shallow,
		CommitsScanned:        rep.CommitsScanned,
		CommitsSkippedBulk:    rep.CommitsSkippedBulk,
		CommitsSkippedMessage: rep.CommitsSkippedMessage,
		FilesWithChurn:        len(rep.Files),
		UnknownScore:          rep.UnknownScore(),
	}
}

func runHotspots(cmd *cobra.Command, hf *hotspotsFlags) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := hf.validate(); err != nil {
		return fmt.Errorf("hotspots: %w", err)
	}
	rep, err := churn.Mine(ctx, hf.churnOptions(loaded.repoRoot))
	if err != nil {
		return fmt.Errorf("hotspots: mine churn: %w", err)
	}

	s, p, rep, err := openPlanner(ctx, rep)
	if err != nil {
		return fmt.Errorf("hotspots: %w", err)
	}
	defer func() { _ = s.Close() }()

	spots, err := p.Hotspots(ctx)
	if err != nil {
		return fmt.Errorf("hotspots: rank: %w", err)
	}
	if hf.Top > 0 && hf.Top < len(spots) {
		spots = spots[:hf.Top]
	}

	res := hotspotsResult{Items: spots, Churn: newChurnMeta(rep, hf.MaxFilesPerCommit)}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "hotspots",
			map[string]any{
				"top":            hf.Top,
				"window_days":    hf.WindowDays,
				"half_life_days": hf.HalfLifeDays,
			}, res, rep.Warnings)
	}
	printHotspotsText(cmd, res, rep.Warnings)
	return nil
}

// openPlanner opens the store and wires an audit + churn-aware planner.
// Shared with `atlas sprint --rank churn`, which needs the identical
// wiring — the point of the flag is that it is the SAME ranking.
//
// The returned report is the one the planner was given: alignChurn may
// have rebased it, or appended a warning to it, and the caller has to
// print the report the numbers actually came from.
func openPlanner(
	ctx context.Context, rep *churn.Report,
) (*store.Store, sprintplan.Planner, *churn.Report, error) {
	dbPath, err := resolveDBPath(loaded, flags.DBPath)
	if err != nil {
		return nil, nil, nil, err
	}
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open store %s: %w", dbPath, err)
	}
	rep, err = alignChurn(ctx, s, rep)
	if err != nil {
		_ = s.Close()
		return nil, nil, nil, err
	}
	a := audit.New(s, audit.Options{
		FreshnessWindow:     loaded.freshnessWindow(),
		ContractDriftWindow: loaded.contractDriftWindow(),
		GitBlame:            audit.NewGitBlame(loaded.repoRoot),
	})
	return s, sprintplan.New(s, a, sprintplan.Options{
		GitBlame: audit.NewGitBlame(loaded.repoRoot),
		Churn:    rep,
	}), rep, nil
}

// alignChurn reconciles the two path namespaces the roll-up joins.
//
// Churn is mined at the git top level, so its paths are relative to that.
// Symbol file_path is relative to the SCAN root, and `atlas scan --root
// <subdir>` makes those two different directories. Joining them by string
// equality then misses every file and the ranking degrades to "every
// feature's churn is unknown" without ever saying why.
//
// So the mismatch is detected: when one unambiguous sub-directory maps the
// indexed paths onto tracked files, the report is rebased onto them; when
// no such mapping exists, the roll-up is reported as un-computable. Both
// beat a confident ranking built on an empty join.
func alignChurn(ctx context.Context, s *store.Store, rep *churn.Report) (*churn.Report, error) {
	if rep == nil {
		return nil, nil
	}
	rows, err := s.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		return nil, fmt.Errorf("list indexed files: %w", err)
	}
	paths := distinctFilePaths(rows)
	if len(paths) == 0 {
		return rep, nil
	}
	prefix, ok := rep.AlignTo(paths)
	switch {
	case ok && prefix == "":
		return rep, nil
	case ok:
		out := rep.Rebase(prefix)
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"churn was mined at the repository root but the index was scanned from %q; "+
				"the churn paths were rebased onto %q so the two join. Files outside it "+
				"are not part of this ranking.", prefix, prefix))
		return out, nil
	default:
		rep.Warnings = append(rep.Warnings, fmt.Sprintf(
			"churn cannot be joined to the index: none of the %d indexed file paths are "+
				"tracked by git under %s, and no single sub-directory maps them there. "+
				"Every churn factor below is UNKNOWN, not measured — re-run 'atlas scan' "+
				"from the repository root for a real ranking.", len(paths), loaded.repoRoot))
		return rep, nil
	}
}

// distinctFilePaths reduces the symbol table to the file paths behind it.
func distinctFilePaths(rows []store.SymbolRow) []string {
	seen := make(map[string]bool, len(rows))
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.FilePath == "" || seen[r.FilePath] {
			continue
		}
		seen[r.FilePath] = true
		out = append(out, r.FilePath)
	}
	return out
}

func printHotspotsText(cmd *cobra.Command, res hotspotsResult, warnings []string) {
	out := cmd.OutOrStdout()
	for _, w := range warnings {
		fmt.Fprintf(out, "WARN: %s\n", w)
	}
	fmt.Fprintf(out,
		"hotspots: churn x gap over %dd of history, half-life %dd "+
			"(%d commits counted, %d skipped as bulk, %d by subject)\n",
		res.Churn.WindowDays, res.Churn.HalfLifeDays, res.Churn.CommitsScanned,
		res.Churn.CommitsSkippedBulk, res.Churn.CommitsSkippedMessage)
	if len(res.Items) == 0 {
		fmt.Fprintln(out, "  no hotspots (run 'atlas init' / 'atlas scan' first)")
		return
	}
	for i, h := range res.Items {
		fmt.Fprintf(out, "%2d. %-46s hotspot=%6.2f  gap=%6.2f  churn=%6.2f  cost=%s\n",
			i+1, h.FeatureID, h.Score, h.Gap, h.Churn.Score, h.Cost)
		fmt.Fprintf(out, "    churn: %s\n", churnDetail(h))
		for _, r := range h.Reasons {
			fmt.Fprintf(out, "    gap:   %s\n", r)
		}
	}
}

// churnDetail is the one-line decomposition of the churn factor.
func churnDetail(h sprintplan.Hotspot) string {
	c := h.Churn
	if c.Status != churn.StatusKnown {
		return fmt.Sprintf(
			"UNKNOWN (%d of %d files have no history) - scored a neutral %.0f, not 0",
			c.FilesUnknown, c.FilesUnknown+c.FilesKnown, c.Score)
	}
	if c.Commits == 0 {
		return "0 commits in the window - this code is not moving"
	}
	return fmt.Sprintf("%d commits by %d author(s), last %s, hottest %s",
		c.Commits, c.Authors, c.LastCommit.Format("2006-01-02"), c.HotFile)
}
