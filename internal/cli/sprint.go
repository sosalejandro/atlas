package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/audit"
	"github.com/sosalejandro/atlas/packages/churn"
	"github.com/sosalejandro/atlas/packages/sprintplan"
	"github.com/sosalejandro/atlas/packages/store"
)

// Accepted values for `atlas sprint --rank`.
//
// "gap" is the default and stays the default: it is the ordering every
// existing script and habit is built on, and a ranking model that changes
// under a team without them asking for it is worse than a ranking model
// they have to opt into.
const (
	rankGap   = "gap"
	rankChurn = "churn"
)

// newSprintCmd implements `atlas sprint [--top N] [--rank gap|churn]`.
func newSprintCmd() *cobra.Command {
	var top int
	var rank string
	cmd := &cobra.Command{
		Use:   "sprint",
		Short: "Ranked backlog (gap-weighted feature priority)",
		Long: `sprint composes audit + sprintplan and emits a prioritised backlog.

By default the full backlog is returned. Pass --top N to cap to the
top-N items; defaults configured via .atlas.yaml > sprint.default_top_n
are applied when --top is unset.

--rank churn multiplies each item's priority by how often its files
actually change, so code that is broken AND moving sorts above code that
is merely broken. It is the same model 'atlas hotspots' shows, and the
churn factor is emitted alongside the priority so the ordering can be
decomposed. The default ranking is unchanged.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSprint(cmd, top, rank)
		},
	}
	cmd.Flags().IntVar(&top, "top", 0,
		"cap output to the top-N items (0 = full backlog or config default)")
	cmd.Flags().StringVar(&rank, "rank", rankGap,
		"ranking model: gap (health deficit only) or churn (deficit x change frequency)")
	return cmd
}

// sprintResult is the JSON payload for `atlas sprint`.
type sprintResult struct {
	Items []sprintplan.SprintItem `json:"items"`
}

func runSprint(cmd *cobra.Command, top int, rank string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	rep, warnings, err := sprintChurn(ctx, rank)
	if err != nil {
		return err
	}

	dbPath, err := resolveDBPath(loaded, flags.DBPath)
	if err != nil {
		return err
	}
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("sprint: open store %s: %w", dbPath, err)
	}
	defer func() { _ = s.Close() }()

	a := audit.New(s, audit.Options{
		FreshnessWindow:     loaded.freshnessWindow(),
		ContractDriftWindow: loaded.contractDriftWindow(),
		GitBlame:            audit.NewGitBlame(loaded.repoRoot),
	})
	p := sprintplan.New(s, a, sprintplan.Options{
		GitBlame: audit.NewGitBlame(loaded.repoRoot),
		Churn:    rep,
	})

	cap := top
	if cap == 0 {
		cap = loaded.Sprint.DefaultTopN
	}

	var items []sprintplan.SprintItem
	if cap > 0 {
		items, err = p.TopN(ctx, cap)
	} else {
		items, err = p.Rank(ctx)
	}
	if err != nil {
		return fmt.Errorf("sprint: rank: %w", err)
	}

	res := sprintResult{Items: items}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "sprint",
			map[string]any{"top": cap, "rank": rank}, res, warnings)
	}
	printSprintText(cmd, items, warnings)
	return nil
}

// sprintChurn mines churn when --rank churn asked for it, and validates the
// flag. A nil report is what switches sprintplan back to the gap ordering,
// so "no churn" and "churn we failed to mine" must not look alike: a
// mining failure is an error, not a silent fall-back to the old ranking.
func sprintChurn(ctx context.Context, rank string) (*churn.Report, []string, error) {
	switch rank {
	case rankGap:
		return nil, nil, nil
	case rankChurn:
		rep, err := churn.Mine(ctx, churn.Options{Repo: loaded.repoRoot})
		if err != nil {
			return nil, nil, fmt.Errorf("sprint: mine churn: %w", err)
		}
		return rep, rep.Warnings, nil
	default:
		return nil, nil, fmt.Errorf("sprint: unknown --rank %q (want %q or %q)",
			rank, rankGap, rankChurn)
	}
}

func printSprintText(cmd *cobra.Command, items []sprintplan.SprintItem, warnings []string) {
	for _, w := range warnings {
		fmt.Fprintf(cmd.OutOrStdout(), "WARN: %s\n", w)
	}
	if len(items) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "sprint: no items in the backlog (run 'atlas init' / 'atlas scan' first)")
		return
	}
	for i, it := range items {
		fmt.Fprintf(cmd.OutOrStdout(), "%2d. %-50s  priority=%6.2f cost=%s%s\n",
			i+1, it.FeatureID, it.Priority, it.Cost, weightedSuffix(it))
		for _, r := range it.Reasons {
			fmt.Fprintf(cmd.OutOrStdout(), "    - %s\n", r)
		}
	}
}

// weightedSuffix appends the churn-weighted score to the headline when the
// backlog was ranked on it, so the number the list is SORTED by is visible
// next to the number it is labelled with.
func weightedSuffix(it sprintplan.SprintItem) string {
	if it.Churn == nil {
		return ""
	}
	return fmt.Sprintf(" churn=%6.2f weighted=%6.2f", it.Churn.Score, it.WeightedPriority)
}
