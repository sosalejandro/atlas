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

// newAuditCmd implements `atlas audit [--feature <id>] [--worst N]`.
func newAuditCmd() *cobra.Command {
	var (
		feature string
		worst   int
	)
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Health scores per feature (worst first by default)",
		Long: `audit computes the per-feature health score from the SQLite store.

Without --feature, every feature is scored; results are ordered
worst-first. With --feature, only that feature is returned (or an
error if it doesn't exist).

--worst N caps the output to the worst-scoring N rows.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAudit(cmd, feature, worst)
		},
	}
	cmd.Flags().StringVar(&feature, "feature", "",
		"score only this feature id (default: every feature)")
	cmd.Flags().IntVar(&worst, "worst", 0,
		"cap output to the worst-N scoring features (0 = no cap)")
	return cmd
}

// auditResult is the JSON payload for `atlas audit`.
type auditResult struct {
	Features []audit.FeatureHealth `json:"features"`
}

func runAudit(cmd *cobra.Command, feature string, worst int) error {
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
		return fmt.Errorf("audit: open store %s: %w", dbPath, err)
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
			return fmt.Errorf("audit: score %q: %w", feature, err)
		}
		healths = []audit.FeatureHealth{h}
	} else {
		healths, err = a.ScoreAll(ctx)
		if err != nil {
			return fmt.Errorf("audit: score all: %w", err)
		}
	}

	// audit returns worst-first already; --worst caps after that.
	if worst > 0 && worst < len(healths) {
		healths = healths[:worst]
	}

	res := auditResult{Features: healths}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "audit",
			map[string]any{"feature": feature, "worst": worst}, res, nil)
	}
	printAuditText(cmd, healths)
	return nil
}

func printAuditText(cmd *cobra.Command, hs []audit.FeatureHealth) {
	if len(hs) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "audit: no features in the store yet")
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
