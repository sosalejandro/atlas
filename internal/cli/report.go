package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/audit"
	"github.com/sosalejandro/atlas/packages/diff"
	"github.com/sosalejandro/atlas/packages/report"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// atlasInformationURI is the tool home page SARIF consumers link the alert's
// tool chip to.
const atlasInformationURI = "https://github.com/sosalejandro/atlas"

// The --include producer tokens. Dead-code is deliberately absent from the
// default set: FindDead's output is a candidate list with documented false
// positives (dynamic dispatch, plugin entry points), and putting candidates on
// a PR as if they were findings is how a team learns to ignore the bot.
const (
	includeAudit    = "audit"
	includeCoverage = "coverage"
	includeDead     = "dead"
)

const defaultInclude = includeAudit + "," + includeCoverage

// reportOpts are the flags shared by every `atlas report` subcommand.
type reportOpts struct {
	include     string
	warnBelow   float64
	errorBelow  float64
	minGapStmts int
	out         string
}

// newReportCmd builds the `atlas report` verb group: the three formats CI can
// actually render, from the state atlas already has.
//
// Splitting by subcommand rather than a `--format` flag on the existing verbs
// is what lets one collection pass feed all three renderings, and it keeps the
// per-format flags (--base for the PR comment) off the commands that have no
// use for them.
func newReportCmd() *cobra.Command {
	opts := &reportOpts{}

	cmd := &cobra.Command{
		Use:   "report",
		Short: "Render findings for CI: SARIF, GitHub annotations, or a sticky PR comment",
		Long: `report turns atlas's findings into the three formats a pull request
displays, so the output lands where the decision is made instead of in a
terminal nobody reads on a PR.

  atlas report sarif   SARIF 2.1.0 for github/codeql-action/upload-sarif.
                       Findings appear inline on the Files view and GitHub
                       dedupes them across pushes by fingerprint.
  atlas report github  ::warning file=,line=:: workflow commands. No upload
                       step and no extra permission, but no dedupe either.
  atlas report pr      The sticky-comment markdown, including the HTML marker
                       a workflow greps for to update the comment in place.

atlas never calls the GitHub API. Pipe the output:

  atlas report sarif --out atlas.sarif
  atlas report pr --base origin/main | gh pr comment --body-file - --edit-last

See docs/commands/report.md for a copy-paste workflow.`,
	}

	cmd.PersistentFlags().StringVar(&opts.include, "include", defaultInclude,
		"comma-separated producers to collect (audit|coverage|dead)")
	cmd.PersistentFlags().Float64Var(&opts.warnBelow, "warn-below", 70,
		"feature health scores below this are reported as warnings (0 disables)")
	cmd.PersistentFlags().Float64Var(&opts.errorBelow, "error-below", 30,
		"feature health scores below this are reported as errors (0 disables)")
	cmd.PersistentFlags().IntVar(&opts.minGapStmts, "min-gap-stmts", 10,
		"ignore coverage gaps smaller than this many statements")
	cmd.PersistentFlags().StringVar(&opts.out, "out", "",
		"write the rendering to this file instead of stdout")

	cmd.AddCommand(newReportSARIFCmd(opts))
	cmd.AddCommand(newReportGitHubCmd(opts))
	cmd.AddCommand(newReportPRCmd(opts))
	return cmd
}

// reportResult is the JSON envelope payload every report subcommand emits.
//
// `body` carries the rendering itself rather than a re-modelled findings list:
// the point of the command is the exact bytes CI consumes, and a consumer that
// re-serialises a structured form would not be uploading what atlas rendered.
type reportResult struct {
	Format       string   `json:"format"`
	Body         string   `json:"body"`
	Marker       string   `json:"marker,omitempty"`
	FindingCount int      `json:"finding_count"`
	Rules        []string `json:"rules,omitempty"`
	OutPath      string   `json:"out_path,omitempty"`
}

func newReportSARIFCmd(opts *reportOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "sarif",
		Short: "Emit SARIF 2.1.0 for GitHub code scanning",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			bundle, err := collectFindings(cmd, opts)
			if err != nil {
				return err
			}
			version, _, _ := resolveBuildInfo()
			var buf bytes.Buffer
			err = report.RenderSARIF(&buf, report.Tool{
				Name:            "atlas",
				InformationURI:  atlasInformationURI,
				SemanticVersion: version,
			}, bundle.findings)
			if err != nil {
				return fmt.Errorf("report sarif: %w", err)
			}
			return emitRendering(cmd, opts, "report.sarif", "sarif", "", buf.String(), bundle)
		},
	}
}

func newReportGitHubCmd(opts *reportOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "github",
		Short: "Emit GitHub Actions workflow annotations (::warning file=,line=::)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			bundle, err := collectFindings(cmd, opts)
			if err != nil {
				return err
			}
			var buf bytes.Buffer
			if err := report.RenderGitHub(&buf, bundle.findings); err != nil {
				return fmt.Errorf("report github: %w", err)
			}
			return emitRendering(cmd, opts, "report.github", "github", "", buf.String(), bundle)
		},
	}
}

func newReportPRCmd(opts *reportOpts) *cobra.Command {
	var (
		base    string
		head    string
		title   string
		maxRows int
	)
	cmd := &cobra.Command{
		Use:   "pr",
		Short: "Emit the sticky PR comment body (marker included; posting is gh's job)",
		Long: `pr renders the markdown body for a single PR comment that a workflow
updates in place on every push.

The body leads with an HTML comment marker. The workflow finds its previous
comment by that marker and edits it, instead of appending a new comment per
push and burying the review conversation.

--base names a git ref (or a numeric snapshot id) to compare against. When no
snapshot exists for it the comment still renders, without the delta section: a
first push on a new branch has nothing to compare to, and failing there would
break the workflow on exactly the commit that introduces it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			bundle, err := collectFindings(cmd, opts)
			if err != nil {
				return err
			}
			delta, warns := resolveDelta(cmd, base, head)
			bundle.warnings = append(bundle.warnings, warns...)

			var buf bytes.Buffer
			err = report.RenderComment(&buf, report.CommentInput{
				Title:          title,
				Summary:        bundle.summary,
				Delta:          delta,
				Findings:       bundle.findings,
				MaxRowsPerRule: maxRows,
			})
			if err != nil {
				return fmt.Errorf("report pr: %w", err)
			}
			return emitRendering(cmd, opts, "report.pr", "pr-comment",
				report.StickyMarker, buf.String(), bundle)
		},
	}
	cmd.Flags().StringVar(&base, "base", "",
		"git ref or snapshot id to compare against (omit for no delta section)")
	cmd.Flags().StringVar(&head, "head", "",
		"git ref or snapshot id for the current side (default: the newest snapshot)")
	cmd.Flags().StringVar(&title, "title", "",
		"comment heading (default: Atlas report)")
	cmd.Flags().IntVar(&maxRows, "max-rows", report.DefaultCommentRows,
		"findings listed per rule before the section collapses into a count")
	return cmd
}

// emitRendering writes the rendering where the caller asked for it: --out to a
// file, --json into the envelope, otherwise stdout.
//
// Warnings ride the envelope in --json mode and go to stderr otherwise, never
// to stdout: `atlas report github` output is parsed line-by-line by the Actions
// runner, and a warning printed on stdout would be echoed into the build log as
// if it were part of the report.
func emitRendering(cmd *cobra.Command, opts *reportOpts,
	command, format, marker, body string, bundle reportBundle,
) error {
	res := reportResult{
		Format:       format,
		Body:         body,
		Marker:       marker,
		FindingCount: len(bundle.findings),
		Rules:        distinctRules(bundle.findings),
		OutPath:      opts.out,
	}

	if opts.out != "" {
		if err := os.WriteFile(opts.out, []byte(body), 0o600); err != nil {
			return fmt.Errorf("%s: write %s: %w", command, opts.out, err)
		}
	}

	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), command, map[string]any{
			"include":       opts.include,
			"warn_below":    opts.warnBelow,
			"error_below":   opts.errorBelow,
			"min_gap_stmts": opts.minGapStmts,
			"out":           opts.out,
		}, res, bundle.warnings)
	}

	// Diagnostics use the spoken form of the verb ("atlas report sarif"),
	// not the envelope's dotted tag, so a line copied out of a build log is
	// a command the reader can actually run.
	label := "atlas " + strings.ReplaceAll(command, ".", " ")
	for _, w := range bundle.warnings {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s: %s\n", label, w)
	}
	if opts.out != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s: wrote %d findings to %s\n",
			label, len(bundle.findings), opts.out)
		return nil
	}
	fmt.Fprint(cmd.OutOrStdout(), body)
	return nil
}

func distinctRules(findings []report.Finding) []string {
	seen := map[string]bool{}
	out := make([]string, 0, 4)
	for _, f := range findings {
		if !seen[f.RuleID] {
			seen[f.RuleID] = true
			out = append(out, f.RuleID)
		}
	}
	sort.Strings(out)
	return out
}

// reportBundle is one collection pass: the findings every renderer shares, the
// summary rows the PR comment leads with, and the warnings that explain what
// did NOT make it in.
type reportBundle struct {
	findings []report.Finding
	summary  []report.SummaryRow
	warnings []string
}

// collectFindings runs the requested producers against the store and returns
// their findings with repo-relative paths.
func collectFindings(cmd *cobra.Command, opts *reportOpts) (reportBundle, error) {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	want, err := parseIncludes(opts.include)
	if err != nil {
		return reportBundle{}, err
	}

	s, err := openStoreForRead(ctx)
	if err != nil {
		return reportBundle{}, err
	}
	defer func() { _ = s.Close() }()

	var b reportBundle
	if want[includeAudit] {
		if err := b.collectAudit(ctx, s, opts); err != nil {
			return reportBundle{}, err
		}
	}
	if want[includeCoverage] {
		if err := b.collectCoverage(ctx, s, opts); err != nil {
			return reportBundle{}, err
		}
	}
	if want[includeDead] {
		if err := b.collectDead(ctx, s); err != nil {
			return reportBundle{}, err
		}
	}

	// Normalise last, once, over everything: the repo root is the same for
	// every producer, and a path that cannot be made repo-relative would
	// vanish from the PR without a trace otherwise.
	kept, dropped := report.NormalizePaths(loaded.repoRoot, b.findings)
	b.findings = report.Sort(kept)
	if len(dropped) > 0 {
		b.warnings = append(b.warnings, fmt.Sprintf(
			"%d findings were dropped because their path could not be made repo-relative "+
				"(e.g. %q); coverage reports that name files by import path are the usual cause",
			len(dropped), dropped[0].Path))
	}
	return b, nil
}

// parseIncludes turns the --include CSV into a set. Unknown tokens are an
// error rather than a silent no-op: a typo in a CI pipeline that quietly
// reports fewer findings reads as a clean run.
func parseIncludes(csv string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, raw := range strings.Split(csv, ",") {
		tok := strings.ToLower(strings.TrimSpace(raw))
		switch tok {
		case "":
			continue
		case includeAudit, includeCoverage, includeDead:
			out[tok] = true
		case "all":
			return map[string]bool{includeAudit: true, includeCoverage: true, includeDead: true}, nil
		default:
			return nil, fmt.Errorf(
				"report: unknown --include value %q; allowed: audit|coverage|dead|all", raw)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("report: --include parsed to an empty producer list")
	}
	return out, nil
}

func (b *reportBundle) collectAudit(ctx context.Context, s *store.Store, opts *reportOpts) error {
	a := audit.New(s, audit.Options{
		FreshnessWindow:     loaded.freshnessWindow(),
		ContractDriftWindow: loaded.contractDriftWindow(),
		GitBlame:            audit.NewGitBlame(loaded.repoRoot),
	})
	healths, err := a.ScoreAll(ctx)
	if err != nil {
		return fmt.Errorf("report: score features: %w", err)
	}

	anchors, err := featureAnchors(ctx, s)
	if err != nil {
		return err
	}
	findings := report.FromAudit(healths, anchors, report.AuditThresholds{
		ErrorBelow: opts.errorBelow,
		WarnBelow:  opts.warnBelow,
	})
	b.findings = append(b.findings, findings...)

	// "Features below the floor" counts the features, NOT the findings.
	//
	// They are different numbers: FromAudit drops every below-floor feature
	// with no anchor symbol, so len(findings) is the count atlas could
	// annotate, which is smaller. Putting it under this label under-reports
	// a compliance number on a PR comment — the one failure this command
	// cannot be allowed to have — so the shortfall gets its own row rather
	// than being folded silently into the headline.
	below := belowFloor(healths, reportFloor(opts))
	unanchored := unanchoredCount(below, anchors)
	b.summary = append(b.summary,
		report.SummaryRow{Label: "Features scored", Value: fmt.Sprintf("%d", len(healths))},
		report.SummaryRow{Label: "Features below the floor", Value: fmt.Sprintf("%d", len(below))})
	if unanchored > 0 {
		b.summary = append(b.summary, report.SummaryRow{
			Label: "…of those, not annotated",
			Value: fmt.Sprintf("%d (no linked symbol to anchor to)", unanchored),
		})
		// Also as a warning: the summary table is the PR comment's, and
		// the same fact has to reach `report sarif` and `report github`,
		// which render no table. Saying it is the difference between
		// "atlas found nothing" and "atlas had nowhere to put it".
		b.warnings = append(b.warnings, fmt.Sprintf(
			"%d low-scoring features have no linked symbol to anchor an annotation to; "+
				"they appear in no format. Add an @atlas:feature annotation to their implementation",
			unanchored))
	}
	return nil
}

// reportFloor is the score below which a feature would have been reported: the
// higher of the two bands, since a score under EITHER one produces a finding.
// A run with --warn-below 0 still has an error band to measure against.
//
// Zero means both bands are disabled, and then nothing is below the floor.
func reportFloor(opts *reportOpts) float64 {
	floor := opts.warnBelow
	if opts.errorBelow > floor {
		floor = opts.errorBelow
	}
	return floor
}

// belowFloor returns the features that scored under the floor — every feature
// the "below the floor" label claims to count, anchored or not.
func belowFloor(healths []audit.FeatureHealth, floor float64) []audit.FeatureHealth {
	if floor <= 0 {
		// Both bands disabled: no feature is "below the floor", because
		// there is no floor. Counting them all here would report every
		// feature in the repo as failing a gate nobody enabled.
		return nil
	}
	out := make([]audit.FeatureHealth, 0, len(healths))
	for _, h := range healths {
		if h.Score < floor {
			out = append(out, h)
		}
	}
	return out
}

// unanchoredCount is how many of the below-floor features FromAudit had to
// drop: a feature whose annotation links to no indexed symbol has no line to
// hang the finding on.
func unanchoredCount(below []audit.FeatureHealth,
	anchors map[shared.FeatureID]report.Anchor,
) int {
	var n int
	for _, h := range below {
		if a, ok := anchors[h.FeatureID]; !ok || a.Path == "" {
			n++
		}
	}
	return n
}

// featureAnchors picks the source position each feature's findings are reported
// at: the first implementation symbol linked to it, by (path, line).
//
// The symbol table is read once and indexed rather than queried per feature —
// the Symbols port has no get-by-id, and a query per feature would be one round
// trip per feature on a table this command already needs most of.
func featureAnchors(ctx context.Context, s *store.Store) (map[shared.FeatureID]report.Anchor, error) {
	syms, err := s.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		return nil, fmt.Errorf("report: list symbols: %w", err)
	}
	byID := make(map[int64]store.SymbolRow, len(syms))
	for _, sym := range syms {
		byID[sym.ID] = sym
	}

	feats, err := s.Features().List(ctx, store.FeatureFilter{})
	if err != nil {
		return nil, fmt.Errorf("report: list features: %w", err)
	}

	out := make(map[shared.FeatureID]report.Anchor, len(feats))
	for _, f := range feats {
		links, err := s.FeatureSymbols().ListByFeature(ctx, f.ID)
		if err != nil {
			return nil, fmt.Errorf("report: list feature symbols for %s: %w", f.ID, err)
		}
		if a, ok := pickAnchor(links, byID); ok {
			out[f.ID] = a
		}
	}
	return out, nil
}

// pickAnchor prefers an impl-role symbol and breaks ties by (path, line) so the
// annotation lands on the same line across runs. A fingerprint is line-free, but
// a wandering annotation still reads as churn in the Files view.
func pickAnchor(links []store.FeatureSymbolLink, byID map[int64]store.SymbolRow) (report.Anchor, bool) {
	var best store.SymbolRow
	var bestRole store.FeatureSymbolRole
	var found bool
	for _, l := range links {
		sym, ok := byID[l.SymbolID]
		if !ok {
			continue
		}
		if !found || betterAnchor(l.Role, sym, bestRole, best) {
			best, bestRole, found = sym, l.Role, true
		}
	}
	if !found {
		return report.Anchor{}, false
	}
	end := 0
	if best.EndLine != nil {
		end = *best.EndLine
	}
	return report.Anchor{Path: best.FilePath, Line: best.Line, EndLine: end}, true
}

func betterAnchor(role store.FeatureSymbolRole, sym store.SymbolRow,
	bestRole store.FeatureSymbolRole, best store.SymbolRow,
) bool {
	implNow, implBest := role == store.RoleImpl, bestRole == store.RoleImpl
	if implNow != implBest {
		return implNow
	}
	if sym.FilePath != best.FilePath {
		return sym.FilePath < best.FilePath
	}
	return sym.Line < best.Line
}

func (b *reportBundle) collectCoverage(ctx context.Context, s *store.Store, opts *reportOpts) error {
	frontier, err := s.Coverage().LatestFrontier(ctx)
	if err != nil {
		return fmt.Errorf("report: latest coverage frontier: %w", err)
	}
	if frontier.Empty() {
		b.warnings = append(b.warnings,
			"no coverage has been ingested, so no coverage findings are reported (run `atlas cov sync`)")
		return nil
	}

	var attributed, unattributed int
	var gaps []store.CoverageGap
	for _, run := range frontier.Runs {
		attributed += run.StmtsAttributed
		unattributed += run.StmtsUnattributed
		rows, err := s.CoverageGaps().List(ctx, run.ID)
		if err != nil {
			return fmt.Errorf("report: list gaps for run %d: %w", run.ID, err)
		}
		gaps = append(gaps, rows...)
		if run.GapsTruncated > 0 {
			b.warnings = append(b.warnings, fmt.Sprintf(
				"run %d's gap list was capped: %d more files have unattributed statements than are listed",
				run.ID, run.GapsTruncated))
		}
	}

	b.findings = append(b.findings, report.FromCoverageGaps(gaps, opts.minGapStmts)...)
	if total := attributed + unattributed; total > 0 {
		b.summary = append(b.summary, report.SummaryRow{
			Label: "Statements attributed",
			Value: fmt.Sprintf("%d / %d (%.1f%%)", attributed, total,
				100*float64(attributed)/float64(total)),
		})
	}
	return nil
}

// collectDead mirrors `atlas codebase dead`'s defaults (import edges, module +
// conditional scopes) so the two verbs cannot disagree about what is dead.
func (b *reportBundle) collectDead(ctx context.Context, s *store.Store) error {
	candidates, err := s.Symbols().FindDead(ctx, store.DeadCodeFilter{
		EdgeKind: store.EdgeKindImport,
		ScopeFilter: []string{
			store.EdgeMetaImportScopeModule,
			store.EdgeMetaImportScopeConditional,
		},
	})
	if err != nil {
		return fmt.Errorf("report: find dead code: %w", err)
	}
	b.findings = append(b.findings, report.FromDeadCode(candidates)...)
	return nil
}

// resolveDelta computes the base-to-head audit movement for the PR comment.
//
// Every failure here degrades to "no delta section" plus a warning rather than
// an error: the comment's job is to render on every push, and the pushes with
// no usable base (a brand-new branch, a repo that has never run
// `atlas snapshot`) are exactly the ones where a hard failure would be most
// confusing.
func resolveDelta(cmd *cobra.Command, base, head string) (*report.Delta, []string) {
	if base == "" {
		return nil, nil
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	s, err := openStoreForRead(ctx)
	if err != nil {
		return nil, []string{fmt.Sprintf("delta skipped: %v", err)}
	}
	defer func() { _ = s.Close() }()

	idA, err := resolveSnapshotID(ctx, s, base)
	if err != nil {
		return nil, []string{fmt.Sprintf(
			"no snapshot for --base %q, so the comment carries no delta: %v", base, err)}
	}
	idB, warns := resolveHeadSnapshotID(ctx, s, head)
	if warns != nil {
		return nil, warns
	}

	engine := diff.NewEngine(s, diff.Options{
		AuditScoreNoiseFloor:       diff.DefaultAuditScoreNoiseFloor,
		CoveragePassRateNoiseFloor: diff.DefaultCoveragePassRateNoiseFloor,
	})
	d, err := engine.ComputeFromStore(ctx, idA, idB)
	if err != nil {
		return nil, []string{fmt.Sprintf("delta skipped: diff %d..%d: %v", idA, idB, err)}
	}
	return auditDeltaToReport(base, head, d.Audit)
}

func resolveHeadSnapshotID(ctx context.Context, s *store.Store, head string) (int64, []string) {
	if head != "" {
		id, err := resolveSnapshotID(ctx, s, head)
		if err != nil {
			return 0, []string{fmt.Sprintf("no snapshot for --head %q: %v", head, err)}
		}
		return id, nil
	}
	rows, err := s.Snapshots().List(ctx, "")
	if err != nil {
		return 0, []string{fmt.Sprintf("delta skipped: list snapshots: %v", err)}
	}
	if len(rows) == 0 {
		return 0, []string{"delta skipped: no snapshots in the store (run `atlas snapshot --audit`)"}
	}
	// List returns newest-first per the Snapshots port doc.
	return rows[0].ID, nil
}

// auditDeltaToReport splits the differ's score changes into the two lists the
// comment renders. Regressions come first and sorted worst-first: a reviewer
// reads the top of the table and stops.
//
// It also returns the warnings for what the comparison could NOT compare. The
// differ reports that through MissingOnA / MissingOnB, and reading only
// Changed/Added/Removed renders a delta section that looks complete and is not:
// "no regressions" and "no audit data on the base side to find regressions in"
// are the same table otherwise. Every other failure in this path degrades to a
// warning; a partial comparison is no different.
func auditDeltaToReport(baseRef, headRef string, d diff.AuditDelta) (*report.Delta, []string) {
	out := &report.Delta{BaseRef: baseRef, HeadRef: headRef}
	for _, c := range d.Changed {
		row := report.ScoreChange{
			FeatureID: string(c.FeatureID),
			Before:    c.Before,
			After:     c.After,
			Delta:     c.Delta,
		}
		if c.Delta < 0 {
			out.Regressed = append(out.Regressed, row)
		} else {
			out.Improved = append(out.Improved, row)
		}
	}
	sort.SliceStable(out.Regressed, func(i, j int) bool {
		return out.Regressed[i].Delta < out.Regressed[j].Delta
	})
	sort.SliceStable(out.Improved, func(i, j int) bool {
		return out.Improved[i].Delta > out.Improved[j].Delta
	})
	for _, f := range d.Added {
		out.NewFeatures = append(out.NewFeatures, string(f.FeatureID))
	}
	for _, f := range d.Removed {
		out.RemovedFeatures = append(out.RemovedFeatures, string(f.FeatureID))
	}
	sort.Strings(out.NewFeatures)
	sort.Strings(out.RemovedFeatures)
	return out, missingAuditWarnings(baseRef, headRef, d)
}

// missingAuditWarnings turns the differ's MissingOnA / MissingOnB into the
// sentences that keep the delta section honest about its own coverage.
//
// The two sides mean different things to a reviewer, so they are reported
// separately: missing on the base is "this PR has nothing to be compared
// against", missing on the head is "this PR's own run did not score them".
func missingAuditWarnings(baseRef, headRef string, d diff.AuditDelta) []string {
	var out []string
	if n := len(d.MissingOnA); n > 0 {
		out = append(out, fmt.Sprintf(
			"the delta is partial: %s have no audit score in the base snapshot (%s), so any "+
				"regression in them is not in the table (e.g. %s); run `atlas snapshot --audit` "+
				"on the base ref",
			pluralFeatures(n), refLabel(baseRef, "base"), d.MissingOnA[0]))
	}
	if n := len(d.MissingOnB); n > 0 {
		out = append(out, fmt.Sprintf(
			"the delta is partial: %s have no audit score in the head snapshot (%s), so their "+
				"movement is not in the table (e.g. %s)",
			pluralFeatures(n), refLabel(headRef, "newest snapshot"), d.MissingOnB[0]))
	}
	return out
}

// refLabel names a side of the comparison, falling back to what the flag
// defaulted to when the caller passed no ref.
func refLabel(ref, fallback string) string {
	if ref == "" {
		return fallback
	}
	return ref
}

func pluralFeatures(n int) string {
	if n == 1 {
		return "1 feature"
	}
	return fmt.Sprintf("%d features", n)
}
