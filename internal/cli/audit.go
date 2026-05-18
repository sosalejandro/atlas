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
		for _, r := range h.Reasons {
			fmt.Fprintf(cmd.OutOrStdout(), "    - %s\n", r)
		}
	}
}
