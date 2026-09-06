package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/affected"
)

// newAffectedGit and affectedExit are the two seams this verb needs to be
// testable end-to-end.
//
// The exit seam exists because `--fallback-exit-code` has to produce an
// arbitrary process status, and cmd/atlas/main.go maps every RunE error to
// exit 1. Returning an error would collapse "I bailed, run everything" onto
// the same code as "the command failed", which is precisely the distinction
// the flag exists to draw.
var (
	newAffectedGit = affected.NewGit
	affectedExit   = os.Exit
)

// affectedKind selects what the human output leads with. All three views are
// always present in --json; the flag only decides what a person reads first.
type affectedKind string

const (
	kindTest    affectedKind = "test"
	kindPackage affectedKind = "package"
	kindFeature affectedKind = "feature"
)

func parseAffectedKind(raw string) (affectedKind, error) {
	switch affectedKind(strings.ToLower(strings.TrimSpace(raw))) {
	case "", kindTest:
		return kindTest, nil
	case kindPackage:
		return kindPackage, nil
	case kindFeature:
		return kindFeature, nil
	default:
		return "", fmt.Errorf("affected: unknown --kind %q; allowed: test|package|feature", raw)
	}
}

func newAffectedCmd() *cobra.Command {
	var (
		since            string
		kindFlag         string
		fallbackExitCode int
	)

	cmd := &cobra.Command{
		Use:   "affected --since <ref>",
		Short: "Select the tests a diff can actually affect",
		Long: `affected maps the changes between <ref> and HEAD onto the symbols
atlas has indexed, then names the tests that recorded executing those
symbols (the per-test coverage evidence written by ` + "`atlas cov sync --per-test`" + `).

The selection is only ever a subset when atlas can justify one. A changed
file it holds no symbols for, a dependency or CI input, shared test
scaffolding, or a coverage frontier with no per-test rows all produce
"run everything" as an EXPLICIT outcome -- never as an empty list, which a
runner cannot tell apart from "nothing needs testing".

Two numbers make the trade-off visible: the reduction (how much of the
suite this skips) and the age of the evidence it was computed from. The
coverage was measured at an earlier commit than the diff; that gap is
reported on every invocation because it bounds how much the answer can be
trusted.

Pair with --fallback-exit-code so CI can branch on "run this subset" versus
"I bailed" without parsing the text output.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			kind, err := parseAffectedKind(kindFlag)
			if err != nil {
				return err
			}
			return runAffected(cmd, affectedArgs{
				since:            since,
				kind:             kind,
				fallbackExitCode: fallbackExitCode,
			})
		},
	}

	cmd.Flags().StringVar(&since, "since", "",
		"git ref to diff against (diffed as <ref>...HEAD, i.e. against the merge base)")
	cmd.Flags().StringVar(&kindFlag, "kind", string(kindTest),
		"what the human output leads with (test|package|feature)")
	cmd.Flags().IntVar(&fallbackExitCode, "fallback-exit-code", 0,
		"exit with this status when the answer is \"run everything\" (0 disables, which is the default so opting in never breaks an existing pipeline)")

	return cmd
}

type affectedArgs struct {
	since            string
	kind             affectedKind
	fallbackExitCode int
}

// affectedResult is the --json payload. It embeds the library's Selection
// verbatim and adds only the two derived values a consumer would otherwise
// have to recompute: the ready-made -run pattern and the reduction.
type affectedResult struct {
	affected.Selection
	RunPattern string  `json:"run_pattern"`
	Reduction  float64 `json:"reduction"`

	// Caveat is emitted unconditionally, including on the run-all path. The
	// evidence and the diff are from different commits, and a consumer reading
	// only the JSON should not have to know that to interpret the result.
	Caveat string `json:"caveat"`
}

// evidenceCaveat is the standing warning about the gap between when coverage
// was measured and what is being diffed now. It is a constant rather than a
// computed sentence so a downstream tool can match on it exactly.
const evidenceCaveat = "coverage evidence was measured at an earlier commit than this diff; a symbol whose callers changed since then may reach tests this selection does not name"

func runAffected(cmd *cobra.Command, args affectedArgs) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if args.since == "" {
		return fmt.Errorf("affected: --since <ref> is required; there is no safe default to diff against")
	}

	s, err := openStoreForRead(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	frontier, err := s.Coverage().LatestFrontier(ctx)
	if err != nil {
		return fmt.Errorf("affected: resolve coverage frontier: %w", err)
	}

	sel, err := affected.Select(ctx, affected.Inputs{
		Git:      newAffectedGit(loaded.repoRoot),
		Symbols:  s.Symbols(),
		Evidence: s.TestCoverage(),
		Features: s.FeatureSymbols(),
		Frontier: frontier,
		Since:    args.since,
	})
	if err != nil {
		return fmt.Errorf("affected: %w", err)
	}

	result := affectedResult{
		Selection:  sel,
		RunPattern: sel.RunPattern(),
		Reduction:  sel.Reduction(),
		Caveat:     evidenceCaveat,
	}

	if flags.JSON {
		if err := emitJSON(stdoutOrJSON(cmd), "affected",
			map[string]any{
				"since":              args.since,
				"kind":               string(args.kind),
				"fallback_exit_code": args.fallbackExitCode,
			},
			result, affectedWarnings(sel)); err != nil {
			return err
		}
	} else {
		renderAffectedHuman(stdoutOrJSON(cmd), result, args.kind)
	}

	// Done last: the exit hook does not return in production, so every byte of
	// output has to be on its way out before it fires.
	if sel.RunAll() && args.fallbackExitCode != 0 {
		affectedExit(args.fallbackExitCode)
	}
	return nil
}

// affectedWarnings lifts the two things a consumer most needs to notice out of
// the result body and into the envelope's warnings array.
func affectedWarnings(sel affected.Selection) []string {
	var out []string
	if sel.RunAll() {
		out = append(out, "atlas could not narrow this diff; run the whole suite")
	}
	if n := len(sel.UncoveredSymbols); n > 0 {
		out = append(out, fmt.Sprintf("%s have no test recorded executing them", plural(n, "changed symbol")))
	}
	return out
}

func renderAffectedHuman(w io.Writer, r affectedResult, kind affectedKind) {
	fmt.Fprintln(w)
	if r.Outcome == affected.OutcomeRunAll {
		renderAffectedFallback(w, r)
	} else {
		renderAffectedSelection(w, r, kind)
	}
	renderAffectedUncovered(w, r)
	renderAffectedEvidence(w, r)
}

func renderAffectedFallback(w io.Writer, r affectedResult) {
	fmt.Fprintf(w, "  RUN EVERYTHING — atlas could not narrow this diff (%s)\n\n",
		plural(len(r.ChangedFiles), "changed file"))
	fmt.Fprintln(w, "  why:")
	for _, f := range r.Fallbacks {
		if f.Path != "" {
			fmt.Fprintf(w, "    [%s] %s\n", f.Reason, f.Path)
		} else {
			fmt.Fprintf(w, "    [%s]\n", f.Reason)
		}
		fmt.Fprintf(w, "      %s\n", f.Detail)
	}
	fmt.Fprintln(w)
}

func renderAffectedSelection(w io.Writer, r affectedResult, kind affectedKind) {
	if r.Outcome == affected.OutcomeNoChanges {
		fmt.Fprintf(w, "  nothing changed between %s and HEAD — no tests to run\n\n", r.Since)
		return
	}
	fmt.Fprintf(w, "  %d of %d tests selected (%.0f%% of the suite skipped)\n",
		len(r.SelectedTests), r.TotalTests, r.Reduction*100)
	fmt.Fprintf(w, "  since %s · %s · %s\n\n",
		r.Since, plural(len(r.ChangedFiles), "changed file"), plural(len(r.ChangedSymbols), "changed symbol"))

	renderAffectedChangedSymbols(w, r)

	switch kind {
	case kindPackage:
		renderAffectedPackages(w, r)
	case kindFeature:
		renderAffectedFeatures(w, r)
	case kindTest:
		renderAffectedTests(w, r)
	}

	for _, wd := range r.Widenings {
		fmt.Fprintf(w, "  widened to the %s of %s:\n    %s\n\n", wd.Scope, wd.Path, wd.Detail)
	}
}

func renderAffectedChangedSymbols(w io.Writer, r affectedResult) {
	if len(r.ChangedSymbols) == 0 {
		return
	}
	fmt.Fprintln(w, "  changed symbols:")
	for _, cs := range r.ChangedSymbols {
		suffix := ""
		if cs.Widened {
			suffix = "  (whole-package widening)"
		}
		fmt.Fprintf(w, "    %-44s %s:%d%s\n", cs.QualifiedName, cs.FilePath, cs.Line, suffix)
	}
	fmt.Fprintln(w)
}

func renderAffectedTests(w io.Writer, r affectedResult) {
	if len(r.SelectedTests) == 0 {
		fmt.Fprintln(w, "  no test in the index recorded executing any changed symbol.")
		fmt.Fprintln(w)
		return
	}
	fmt.Fprintln(w, "  selected tests:")
	for _, t := range r.SelectedTests {
		flag := ""
		if t.NoHistory {
			flag = "  [no execution history — new or never run]"
		}
		fmt.Fprintf(w, "    %-44s %s%s\n", t.RunName, t.FilePath, flag)
		for _, why := range t.Why {
			fmt.Fprintf(w, "      %s\n", why)
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  go test -run '%s' %s\n\n", r.RunPattern, strings.Join(goTestPackageArgs(r), " "))
}

func renderAffectedPackages(w io.Writer, r affectedResult) {
	if len(r.Packages) == 0 {
		fmt.Fprintln(w, "  no package holds a test that recorded executing a changed symbol.")
		fmt.Fprintln(w)
		return
	}
	fmt.Fprintln(w, "  packages to test:")
	for _, p := range goTestPackageArgs(r) {
		fmt.Fprintf(w, "    %s\n", p)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  go test -run '%s' %s\n\n", r.RunPattern, strings.Join(goTestPackageArgs(r), " "))
}

func renderAffectedFeatures(w io.Writer, r affectedResult) {
	if len(r.Features) == 0 {
		fmt.Fprintln(w, "  no annotated feature claims any changed symbol.")
		fmt.Fprintln(w)
		return
	}
	fmt.Fprintln(w, "  features touched:")
	for _, f := range r.Features {
		fmt.Fprintf(w, "    %s\n", f)
	}
	fmt.Fprintln(w)
	renderAffectedTests(w, r)
}

// goTestPackageArgs renders the selected packages as `go test` path arguments.
// The "/..." suffix is deliberate: a test in a package may execute a symbol in
// a subpackage, and go test's own path semantics make the recursive form the
// safe one to hand a runner.
func goTestPackageArgs(r affectedResult) []string {
	if len(r.Packages) == 0 {
		return []string{"./..."}
	}
	out := make([]string, 0, len(r.Packages))
	for _, p := range r.Packages {
		if p == "." || p == "" {
			out = append(out, "./...")
			continue
		}
		out = append(out, "./"+strings.TrimPrefix(p, "./")+"/...")
	}
	return out
}

func renderAffectedUncovered(w io.Writer, r affectedResult) {
	if len(r.UncoveredSymbols) == 0 {
		return
	}
	fmt.Fprintf(w, "  WARN  %s — no test covers them:\n", plural(len(r.UncoveredSymbols), "changed symbol"))
	for _, cs := range r.UncoveredSymbols {
		fmt.Fprintf(w, "    %-44s %s:%d\n", cs.QualifiedName, cs.FilePath, cs.Line)
	}
	fmt.Fprintln(w, "    Nothing was selected for these because nothing executed them in the")
	fmt.Fprintln(w, "    measured run. That is a coverage gap, not a selection failure.")
	fmt.Fprintln(w)
}

func renderAffectedEvidence(w io.Writer, r affectedResult) {
	ev := r.Evidence
	fmt.Fprintln(w, "  evidence:")
	if len(ev.RunIDs) == 0 {
		fmt.Fprintln(w, "    no coverage runs in the store")
		fmt.Fprintln(w)
		return
	}
	label := "run"
	if len(ev.RunIDs) > 1 {
		label = "runs"
	}
	group := ""
	if ev.Group != nil {
		group = fmt.Sprintf(" (group %s)", *ev.Group)
	}
	fmt.Fprintf(w, "    frontier %s %s%s — %s, finished %s ago\n",
		label, joinInt64s(ev.RunIDs), group,
		strings.Join(ev.Frameworks, "+"), roundAge(ev.Age))
	fmt.Fprintf(w, "    %s contributed per-test rows\n", plural(ev.TestsWithEvidence, "test"))
	fmt.Fprintln(w, "    NOTE: that coverage was measured at an earlier commit than this diff.")
	fmt.Fprintln(w, "          A symbol whose callers changed since then may reach tests this")
	fmt.Fprintln(w, "          selection does not name.")
	fmt.Fprintln(w)
}

func joinInt64s(ids []int64) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("%d", id))
	}
	return strings.Join(parts, ",")
}

// plural renders a count with its noun, pluralised by the naive -s rule that
// every noun this file uses happens to follow.
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// roundAge trims the sub-second noise that makes an otherwise stable line
// churn between invocations.
func roundAge(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	return d.Round(time.Minute).String()
}
