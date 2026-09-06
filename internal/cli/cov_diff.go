package cli

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/coverage/patch"
	"github.com/sosalejandro/atlas/packages/indexfresh"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// newCovDiffCmd implements `atlas cov diff --base <ref> [--fail-under N]` —
// patch coverage, and the exit code a CI gate is built on.
func newCovDiffCmd() *cobra.Command {
	var (
		base      string
		failUnder float64
	)
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Coverage of the lines this branch changed, with a CI gate",
		Long: `cov diff scores the lines THIS branch added or modified, rather
than the whole repository.

Whole-repo coverage cannot fail a pull request: no single PR can move it.
Patch coverage can, which is why every coverage product ships it.

The changed lines come from 'git diff --unified=0 <base>...HEAD' -- the
merge-base form, so commits that landed on the base branch after you forked
are not charged to you. Only COMMITTED work is visible. Those lines are then
intersected with the indexed symbol spans and scored against the current
coverage frontier (the same runs 'atlas cov status' and 'atlas audit' read).

Every changed line lands in one of THREE buckets, and the third one is the
point:

  covered / uncovered  lines inside a symbol the frontier measured. These
                       are the known fraction, and the only thing
                       --fail-under decides on.
  unknown              lines atlas cannot score: a file it has no symbol
                       for, a line outside every indexed span, a symbol
                       no run carried statement counts for, or a file whose
                       indexed spans no longer describe what is on disk.
                       This is NOT 0% covered -- gating on files atlas
                       cannot see teaches teams to switch the gate off --
                       so it is reported separately and loudly, and never
                       enters the denominator.

The spans are only joinable against the diff when the index was built at
HEAD. Every changed file is re-hashed against what the scanner recorded;
a file that no longer matches is reported as a STALE INDEX line and its
changed lines go to the unknown bucket rather than being charged to
whichever symbol has since drifted into their line range.

A diff that changed no indexed code has no denominator at all: the command
reports "no measurable change" and exits 0 under any target, because 0% would
fail every docs-only and config-only pull request.

  --fail-under N   exit non-zero when the KNOWN patch fraction is below N.
  --json           the full accounting, every bucket, for a PR comment bot.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var target *float64
			if cmd.Flags().Changed("fail-under") {
				target = &failUnder
			}
			return runCovDiff(cmd, base, target)
		},
	}
	cmd.Flags().StringVar(&base, "base", "",
		"the ref this branch forked from (origin/main, a SHA, a tag); required")
	cmd.Flags().Float64Var(&failUnder, "fail-under", 0,
		"exit non-zero when the known patch fraction is below this percentage")
	return cmd
}

// covDiffResult is the JSON payload for `atlas cov diff`. It embeds the
// scorer's own result so the bucket names are identical in the library and on
// the wire — a consumer reading the JSON is reading patch.Result.
type covDiffResult struct {
	Base string `json:"base"`
	Head string `json:"head"`
	// FailUnder echoes the target so a PR bot can render "79.2% (target 80)"
	// without being told the threshold twice.
	FailUnder *float64 `json:"fail_under,omitempty"`
	// Passed is false only when a target was set AND the known fraction fell
	// below it. An unmeasurable diff passes: see patch.Result.Meets.
	Passed bool `json:"passed"`

	patch.Result
}

func runCovDiff(cmd *cobra.Command, base string, target *float64) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(base) == "" {
		return fmt.Errorf("cov diff: --base is required (the ref this branch forked from, e.g. origin/main)")
	}

	changes, err := patch.Diff(ctx, loaded.repoRoot, base)
	if err != nil {
		return fmt.Errorf("cov diff: %w", err)
	}

	dbPath, err := resolveDBPath(loaded, flags.DBPath)
	if err != nil {
		return err
	}
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("cov diff: open store %s: %w", dbPath, err)
	}
	defer func() { _ = s.Close() }()

	in, err := covDiffInput(ctx, s, changes)
	if err != nil {
		return err
	}

	res := covDiffResult{
		Base:      base,
		Head:      "HEAD",
		FailUnder: target,
		Result:    patch.Score(in),
	}
	res.Passed = target == nil || res.Meets(*target)

	if flags.JSON {
		if err := emitJSON(stdoutOrJSON(cmd), "cov.diff",
			map[string]any{"base": base, "fail_under": target}, res, nil); err != nil {
			return err
		}
	} else {
		printCovDiff(cmd, res)
	}
	// The report is emitted either way: a failing gate that prints nothing
	// forces the reviewer back to CI logs for the lines they need.
	if !res.Passed {
		return fmt.Errorf("cov diff: patch coverage %.1f%% is below --fail-under %g",
			*res.Percent, *target)
	}
	return nil
}

// covDiffInput reads everything the scorer needs out of the store: the symbol
// index, the frontier's per-symbol statement counts, and the feature links
// for the symbols the diff could plausibly touch.
func covDiffInput(ctx context.Context, s *store.Store, changes []patch.FileChange) (patch.Input, error) {
	frontier, err := s.Coverage().LatestFrontier(ctx)
	if err != nil {
		return patch.Input{}, fmt.Errorf("cov diff: resolve frontier: %w", err)
	}
	// Refuse rather than score everything as unknown. With no frontier the
	// known fraction is empty, --fail-under passes unconditionally, and the
	// gate is green for the wrong reason — the single worst outcome for a
	// command whose job is to fail builds.
	if frontier.Empty() {
		return patch.Input{}, fmt.Errorf(
			"cov diff: no coverage runs in the store yet - run 'atlas cov sync' first")
	}
	results, err := s.Coverage().ListFrontierResults(ctx, frontier)
	if err != nil {
		return patch.Input{}, fmt.Errorf("cov diff: list frontier results: %w", err)
	}
	syms, err := s.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		return patch.Input{}, fmt.Errorf("cov diff: list symbols: %w", err)
	}
	features, err := covDiffFeatures(ctx, s, syms, changes)
	if err != nil {
		return patch.Input{}, err
	}
	stale, err := covDiffStaleSpans(ctx, s, changes)
	if err != nil {
		return patch.Input{}, err
	}
	return patch.Input{
		Changes:    changes,
		Symbols:    syms,
		Coverage:   frontierStmtCounts(results),
		Features:   features,
		StaleSpans: stale,
	}, nil
}

// covDiffStaleSpans asks indexfresh which of the diff's files still hash to
// what the scanner recorded.
//
// This is the join's precondition, not a nicety. The line numbers come from
// the working tree at HEAD; the spans come from whenever `atlas scan` last
// ran. When they disagree the failure is silent and directional: insert
// twenty lines near the top of a file and every span below it shifts, so a
// changed line lands inside whichever symbol NOW occupies that range and is
// scored as that symbol's coverage. The number that comes out is confident
// and wrong, which is the failure mode this codebase treats as the worst one.
func covDiffStaleSpans(
	ctx context.Context, s *store.Store, changes []patch.FileChange,
) (map[string]string, error) {
	paths := make([]string, 0, len(changes))
	for _, c := range changes {
		paths = append(paths, c.Path)
	}
	rep, err := indexfresh.Classify(ctx, s.FileHashes(), loaded.repoRoot, paths)
	if err != nil {
		return nil, fmt.Errorf("cov diff: check index freshness: %w", err)
	}
	out := map[string]string{}
	for path, state := range rep.States {
		if state.Trustworthy() {
			continue
		}
		out[path] = string(state)
	}
	return out, nil
}

// frontierStmtCounts pools the frontier's results into per-symbol statement
// counts.
//
// Summing matches what the audit's coverage signal does with the same rows: a
// symbol legitimately draws several results in one run (several profile
// blocks, several files), and the line-weighted score wants its whole
// statement footprint. Results with no statement counts are skipped entirely
// rather than folded in as 0 — a pass/fail framework's row means "a test
// touched this", not "nothing in it ran", and the scorer needs to tell a
// missing measurement from a measured zero.
func frontierStmtCounts(results []store.CoverageResult) map[int64]patch.SymbolCoverage {
	out := make(map[int64]patch.SymbolCoverage, len(results))
	for _, r := range results {
		if r.SymbolID == nil || r.TotalStmts <= 0 {
			continue
		}
		c := out[*r.SymbolID]
		c.Covered += r.CoveredStmts
		c.Total += r.TotalStmts
		out[*r.SymbolID] = c
	}
	return out
}

// covDiffFeatures maps symbol id to the features it implements, for the
// symbols declared in a changed file only.
//
// Narrowed to the diff on purpose: the link table is queried per symbol, and
// a whole-repo sweep would cost one round trip per indexed symbol to answer a
// question about a few dozen of them.
func covDiffFeatures(
	ctx context.Context,
	s *store.Store,
	syms []store.SymbolRow,
	changes []patch.FileChange,
) (map[int64][]shared.FeatureID, error) {
	touchedFiles := make(map[string]bool, len(changes))
	for _, c := range changes {
		touchedFiles[c.Path] = true
	}
	out := map[int64][]shared.FeatureID{}
	for _, sym := range syms {
		if !touchedFiles[sym.FilePath] {
			continue
		}
		links, err := s.FeatureSymbols().ListBySymbol(ctx, sym.ID)
		if err != nil {
			return nil, fmt.Errorf("cov diff: feature links for symbol %d: %w", sym.ID, err)
		}
		if len(links) == 0 {
			continue
		}
		// One symbol can be linked to a feature under several roles (impl and
		// contract, say); dedupe so the rollup does not count its lines twice.
		seen := make(map[shared.FeatureID]bool, len(links))
		ids := make([]shared.FeatureID, 0, len(links))
		for _, l := range links {
			if seen[l.FeatureID] {
				continue
			}
			seen[l.FeatureID] = true
			ids = append(ids, l.FeatureID)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		out[sym.ID] = ids
	}
	return out, nil
}

// --- text rendering -------------------------------------------------------

func printCovDiff(cmd *cobra.Command, res covDiffResult) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "patch coverage  %s...%s\n", res.Base, res.Head)
	fmt.Fprintf(out, "changed files: %d   changed lines: %d\n\n", res.ChangedFiles, res.ChangedLines)

	if !res.Measurable {
		// Deliberately never rendered as 0%: there is no denominator, and a
		// zero here would read as "this diff is untested".
		fmt.Fprintf(out,
			"  no measurable change: none of the %d changed lines is both indexed and measured\n",
			res.ChangedLines)
	} else {
		fmt.Fprintf(out, "  known:   %5d lines   covered %.0f (%.1f%%)\n",
			res.KnownLines, math.Round(res.CoveredLines), *res.Percent)
	}
	printCovDiffUnknownSummary(cmd, res)
	printCovDiffStaleIndex(cmd, res)
	printCovDiffFeatures(cmd, res)
	printCovDiffSpans(cmd, "uncovered changed lines", res.Uncovered, false)
	printCovDiffSpans(cmd,
		"partially covered - atlas knows the symbol's ratio, not which of its lines ran",
		res.Partial, true)
	printCovDiffUnknownSpans(cmd, res)
	printCovDiffVerdict(cmd, res)
}

// printCovDiffUnknownSummary reports the third bucket with the breakdown that
// says what to do about it: each reason has a different remedy.
func printCovDiffUnknownSummary(cmd *cobra.Command, res covDiffResult) {
	if res.UnknownLines == 0 {
		return
	}
	byReason := map[string]int{}
	for _, u := range res.Unknown {
		byReason[u.Reason] += u.Lines
	}
	reasons := make([]string, 0, len(byReason))
	for r := range byReason {
		reasons = append(reasons, r)
	}
	sort.Slice(reasons, func(i, j int) bool {
		if byReason[reasons[i]] != byReason[reasons[j]] {
			return byReason[reasons[i]] > byReason[reasons[j]]
		}
		return reasons[i] < reasons[j]
	})
	parts := make([]string, 0, len(reasons))
	for _, r := range reasons {
		parts = append(parts, fmt.Sprintf("%d %s", byReason[r], r))
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"  unknown: %5d lines   %s  (not scored, not gated)\n",
		res.UnknownLines, strings.Join(parts, ", "))
}

// printCovDiffStaleIndex warns, above the per-symbol detail, that part of the
// diff was excluded because the index does not describe HEAD.
//
// Printed loudly and with the remedy attached because the alternative to
// noticing it is believing a percentage computed from a shrunken denominator.
func printCovDiffStaleIndex(cmd *cobra.Command, res covDiffResult) {
	if len(res.StaleIndexFiles) == 0 {
		return
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out,
		"\n  STALE INDEX: %d of %d changed files are not described by the current index;\n"+
			"               their %d changed lines are unscored. Re-run 'atlas scan' at HEAD.\n",
		len(res.StaleIndexFiles), res.ChangedFiles, res.StaleIndexLines)
	for i, sf := range res.StaleIndexFiles {
		if i == maxGapLines {
			fmt.Fprintf(out, "    ... +%d more\n", len(res.StaleIndexFiles)-maxGapLines)
			break
		}
		fmt.Fprintf(out, "    %-52s %-10s %4d lines\n", sf.Path, sf.State, sf.Lines)
	}
}

func printCovDiffFeatures(cmd *cobra.Command, res covDiffResult) {
	if len(res.Features) == 0 {
		return
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "\n  features touched (by changed lines):\n")
	for i, f := range res.Features {
		if i == maxGapLines {
			fmt.Fprintf(out, "    ... +%d more\n", len(res.Features)-maxGapLines)
			break
		}
		fmt.Fprintf(out, "    %-40s %4d lines   %5.1f%% covered\n",
			f.FeatureID, f.Lines, f.Percent)
	}
}

// printCovDiffSpans lists symbol-level spans as file:line, which is the form
// a reviewer can paste into an editor. A number without the lines is not
// actionable, and that is the whole reason this section exists.
func printCovDiffSpans(cmd *cobra.Command, heading string, spans []patch.SymbolSpan, withRatio bool) {
	if len(spans) == 0 {
		return
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "\n  %s:\n", heading)
	for i, s := range spans {
		if i == maxGapLines {
			fmt.Fprintf(out, "    ... +%d more\n", len(spans)-maxGapLines)
			break
		}
		detail := fmt.Sprintf("(0/%d stmts)", s.Total)
		if withRatio {
			detail = fmt.Sprintf("(%d/%d stmts, %.1f%%)", s.Covered, s.Total, 100*s.Fraction)
		}
		fmt.Fprintf(out, "    %s  %s  %s\n", covDiffLocation(s.Path, s.Ranges), s.Symbol, detail)
	}
}

func printCovDiffUnknownSpans(cmd *cobra.Command, res covDiffResult) {
	if len(res.Unknown) == 0 {
		return
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "\n  unknown changed lines:\n")
	// Biggest blind spot first, so a capped list still shows the worst of it.
	spans := append([]patch.UnknownSpan(nil), res.Unknown...)
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].Lines > spans[j].Lines })
	for i, u := range spans {
		if i == maxGapLines {
			fmt.Fprintf(out, "    ... +%d more\n", len(spans)-maxGapLines)
			break
		}
		fmt.Fprintf(out, "    %s  %s\n", covDiffLocation(u.Path, u.Ranges), u.Reason)
	}
}

func printCovDiffVerdict(cmd *cobra.Command, res covDiffResult) {
	if res.FailUnder == nil {
		return
	}
	out := cmd.OutOrStdout()
	switch {
	case !res.Measurable:
		fmt.Fprintf(out, "\nPASS  no measurable change; --fail-under %g does not apply\n", *res.FailUnder)
	case res.Passed:
		fmt.Fprintf(out, "\nPASS  patch coverage %.1f%% meets --fail-under %g\n", *res.Percent, *res.FailUnder)
	default:
		fmt.Fprintf(out, "\nFAIL  patch coverage %.1f%% is below --fail-under %g\n", *res.Percent, *res.FailUnder)
	}
}

// covDiffLocation renders "path:12-19,44" — one location string per span, so
// a multi-hunk symbol stays on one line instead of repeating its name.
func covDiffLocation(path string, ranges []patch.LineRange) string {
	parts := make([]string, 0, len(ranges))
	for _, r := range ranges {
		parts = append(parts, r.String())
	}
	return path + ":" + strings.Join(parts, ",")
}
