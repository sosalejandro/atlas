package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/audit"
	"github.com/sosalejandro/atlas/packages/sprintplan"
	"github.com/sosalejandro/atlas/packages/store"
)

// newSprintCmd implements `atlas sprint [--top N]`.
func newSprintCmd() *cobra.Command {
	var top int
	cmd := &cobra.Command{
		Use:   "sprint",
		Short: "Ranked backlog (gap-weighted feature priority)",
		Long: `sprint composes audit + sprintplan and emits a prioritised backlog.

By default the full backlog is returned. Pass --top N to cap to the
top-N items; defaults configured via .atlas.yaml > sprint.default_top_n
are applied when --top is unset.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSprint(cmd, top)
		},
	}
	cmd.Flags().IntVar(&top, "top", 0,
		"cap output to the top-N items (0 = full backlog or config default)")
	return cmd
}

// sprintResult is the JSON payload for `atlas sprint`.
type sprintResult struct {
	Items []sprintplan.SprintItem `json:"items"`
}

func runSprint(cmd *cobra.Command, top int) error {
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
			map[string]any{"top": cap}, res, nil)
	}
	printSprintText(cmd, items)
	return nil
}

func printSprintText(cmd *cobra.Command, items []sprintplan.SprintItem) {
	if len(items) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "sprint: no items in the backlog (run 'atlas init' / 'atlas scan' first)")
		return
	}
	for i, it := range items {
		fmt.Fprintf(cmd.OutOrStdout(), "%2d. %-50s  priority=%6.2f cost=%s\n",
			i+1, it.FeatureID, it.Priority, it.Cost)
		for _, r := range it.Reasons {
			fmt.Fprintf(cmd.OutOrStdout(), "    - %s\n", r)
		}
	}
}
