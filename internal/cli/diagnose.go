package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/diagnose"
	"github.com/sosalejandro/atlas/packages/store"
)

// newDiagnoseCmd implements `atlas diagnose <symptom>` — symptom → symbol
// matcher.
func newDiagnoseCmd() *cobra.Command {
	var (
		maxResults    int
		minConfidence float64
	)
	cmd := &cobra.Command{
		Use:   "diagnose <symptom>",
		Short: "Match an error / log line to candidate symbols",
		Long: `diagnose walks the persisted symbol bodies in the SQLite store and
returns the symbols most likely to produce the given symptom string.

Useful triage tool when you have a stack-trace-free error and need to
find "where in the code would this come from". Results are ranked by
confidence; --max-results caps the output and --min-confidence drops
weaker matches.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDiagnose(cmd, args[0], maxResults, minConfidence)
		},
	}
	cmd.Flags().IntVar(&maxResults, "max-results", 10,
		"cap the number of matches returned (0 = no cap)")
	cmd.Flags().Float64Var(&minConfidence, "min-confidence", 0.0,
		"drop matches below this confidence floor (0..1)")
	return cmd
}

// diagnoseResult is the JSON payload for `atlas diagnose`.
type diagnoseResult struct {
	Matches []diagnose.Match `json:"matches"`
}

func runDiagnose(cmd *cobra.Command, symptom string, maxResults int, minConfidence float64) error {
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
		return fmt.Errorf("diagnose: open store %s: %w", dbPath, err)
	}
	defer func() { _ = s.Close() }()

	matches, err := diagnose.Diagnose(ctx, symptom, s, &diagnose.Options{
		MaxResults:    maxResults,
		MinConfidence: minConfidence,
		ProjectRoot:   loaded.repoRoot,
	})
	if err != nil {
		return fmt.Errorf("diagnose: %w", err)
	}

	res := diagnoseResult{Matches: matches}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "diagnose",
			map[string]any{
				"symptom":        symptom,
				"max_results":    maxResults,
				"min_confidence": minConfidence,
			}, res, nil)
	}
	printDiagnoseText(cmd, matches)
	return nil
}

func printDiagnoseText(cmd *cobra.Command, ms []diagnose.Match) {
	if len(ms) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "diagnose: no candidates matched the symptom")
		return
	}
	for _, m := range ms {
		feat := "-"
		if m.Feature != nil {
			feat = string(m.Feature.ID)
		}
		fmt.Fprintf(cmd.OutOrStdout(),
			"  %0.3f  %-50s  %s:%d  [feature=%s]\n    %s\n",
			m.Confidence, m.Symbol.ID, m.Symbol.Position.Path, m.Symbol.Position.Line, feat, m.Reason)
	}
}
