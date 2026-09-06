package cli

import (
	"context"
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/audit"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// newHealthCmd implements `atlas health [--feature <id>] [--worst N]`.
//
// The verb was `atlas audit` until issue #112. Three words named one number:
// the command said audit, the type said FeatureHealth, and the docs said
// score, which is how a reader concludes there are three numbers. `health` is
// the one that already matched the type. The old verb stays as an alias --
// and not only for the deprecation window's sake: the compliance reading of
// that word is exactly what a regulated buyer searches for, so it is worth
// keeping reachable.
func newHealthCmd() *cobra.Command {
	var (
		feature string
		worst   int
	)
	cmd := &cobra.Command{
		Use:     "health",
		Aliases: aliasesFor("health"),
		Short:   "Health scores per feature (worst first by default)",
		Long: `health computes the per-feature health score from the SQLite store.

Renamed from 'atlas audit' by issue #112 -- the old verb still works for one
minor version.

Without --feature, every feature is scored; results are ordered
worst-first. With --feature, only that feature is returned (or an
error if it doesn't exist).

--worst N caps the output to the worst-scoring N rows.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			noteIfRenamed(cmd)
			return runHealth(cmd, feature, worst)
		},
	}
	cmd.Flags().StringVar(&feature, "feature", "",
		"score only this feature id (default: every feature)")
	cmd.Flags().IntVar(&worst, "worst", 0,
		"cap output to the worst-N scoring features (0 = no cap)")
	return cmd
}

// healthResult is the JSON payload for `atlas health`.
type healthResult struct {
	Features []audit.FeatureHealth `json:"features"`
}

func runHealth(cmd *cobra.Command, feature string, worst int) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	dbPath, err := resolveDBPath(loaded, flags.DBPath)
	if err != nil {
		return err
	}
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("health: open store %s: %w", dbPath, err)
	}
	defer func() { _ = s.Close() }()

	a := audit.New(s, audit.Options{
		FreshnessWindow:     loaded.freshnessWindow(),
		ContractDriftWindow: loaded.contractDriftWindow(),
		GitBlame:            audit.NewGitBlame(loaded.repoRoot),
	})

	var healths []audit.FeatureHealth
	if feature != "" {
		h, err := a.ScoreFeature(ctx, shared.FeatureID(feature))
		if err != nil {
			return fmt.Errorf("health: score %q: %w", feature, err)
		}
		healths = []audit.FeatureHealth{h}
	} else {
		healths, err = a.ScoreAll(ctx)
		if err != nil {
			return fmt.Errorf("health: score all: %w", err)
		}
	}

	// The audit package returns worst-first already; --worst caps after that.
	if worst > 0 && worst < len(healths) {
		healths = healths[:worst]
	}

	res := healthResult{Features: healths}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "health",
			map[string]any{"feature": feature, "worst": worst}, res, nil)
	}
	printHealthText(cmd, healths)
	return nil
}

func printHealthText(cmd *cobra.Command, hs []audit.FeatureHealth) {
	if len(hs) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "health: no features in the store yet")
		return
	}
	// Components are emitted in stable component-name order so the
	// human-readable view doesn't reshuffle between runs.
	for _, h := range hs {
		fmt.Fprintf(cmd.OutOrStdout(), "%-50s  score=%6.2f\n", h.FeatureID, h.Score)
		keys := make([]string, 0, len(h.Components))
		for k := range h.Components {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(cmd.OutOrStdout(), "    %-22s %6.2f\n", k, h.Components[k])
		}
		if line := decisionLine(h.Decision); line != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "    %s\n", line)
		}
		for _, r := range h.Reasons {
			fmt.Fprintf(cmd.OutOrStdout(), "    - %s\n", r)
		}
	}
}

// decisionLine renders the decision-coverage reading underneath the component
// list. Empty when nothing on the feature's surface was ever measured.
//
// The unavailable case is the one that has to be printed. It carries no
// component — an unjudgeable signal must not be scored — so without this line
// an operator who had just run `atlas flow` over the feature would see no
// trace of it and conclude the run did nothing. And the undetermined count is
// printed even at 100%, because "every branch we could judge was taken, and
// four we could not judge at all" is not the same report as "every branch was
// taken".
func decisionLine(d *audit.DecisionCoverageReport) string {
	if d == nil {
		return ""
	}
	symbols := fmt.Sprintf("%d/%d symbols measured",
		d.SymbolsMeasured, d.SymbolsMeasured+d.SymbolsUnmeasured)
	if !d.Available {
		return fmt.Sprintf("decision: not scored - no decidable branch outcome (%d undetermined, %s)",
			d.OutcomesUndetermined, symbols)
	}
	return fmt.Sprintf("decision: %d/%d decidable outcomes taken, %d undetermined, %s",
		d.OutcomesTaken, d.OutcomesDecidable, d.OutcomesUndetermined, symbols)
}
