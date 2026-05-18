package cli

import (
	"context"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/diff"
	"github.com/sosalejandro/atlas/packages/store"
)

// newDiffCmd implements `atlas diff <ref-a> <ref-b>` — structured delta
// between two persisted snapshots.
func newDiffCmd() *cobra.Command {
	var noiseFloor int
	cmd := &cobra.Command{
		Use:   "diff <ref-a> <ref-b>",
		Short: "Snapshot delta between two persisted refs",
		Long: `diff loads two snapshot rows from the store and emits the structured
delta produced by packages/diff (features, symbols, edges, annotations,
contracts, pattern matches, audit, coverage).

Each <ref-a> / <ref-b> argument can be either:

  - an integer snapshot id (e.g. "12")
  - a git ref string (the latest snapshot row with that git_ref wins)

Use 'atlas snapshot --ref <ref>' to capture a snapshot before running
diff if your CI hasn't already.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDiff(cmd, args[0], args[1], noiseFloor)
		},
	}
	cmd.Flags().IntVar(&noiseFloor, "audit-noise-floor", diff.DefaultAuditScoreNoiseFloor,
		"audit score delta below which Changed entries are suppressed")
	return cmd
}

// diffResult is the JSON payload for `atlas diff`.
type diffResult struct {
	Diff *diff.SnapshotDiff `json:"diff"`
}

func runDiff(cmd *cobra.Command, refA, refB string, noiseFloor int) error {
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
		return fmt.Errorf("diff: open store %s: %w", dbPath, err)
	}
	defer func() { _ = s.Close() }()

	idA, err := resolveSnapshotID(ctx, s, refA)
	if err != nil {
		return fmt.Errorf("diff: resolve ref-a %q: %w", refA, err)
	}
	idB, err := resolveSnapshotID(ctx, s, refB)
	if err != nil {
		return fmt.Errorf("diff: resolve ref-b %q: %w", refB, err)
	}

	engine := diff.NewEngine(s, diff.Options{
		AuditScoreNoiseFloor:       noiseFloor,
		CoveragePassRateNoiseFloor: diff.DefaultCoveragePassRateNoiseFloor,
	})
	d, err := engine.ComputeFromStore(ctx, idA, idB)
	if err != nil {
		return fmt.Errorf("diff compute: %w", err)
	}

	res := diffResult{Diff: d}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "diff",
			map[string]any{"ref_a": refA, "ref_b": refB, "id_a": idA, "id_b": idB,
				"audit_noise_floor": noiseFloor}, res, nil)
	}
	printDiffText(cmd, d)
	return nil
}

// resolveSnapshotID accepts either a numeric snapshot id or a git ref
// string (in which case we look up the most recent snapshot row for
// that ref). Numeric is tried first.
func resolveSnapshotID(ctx context.Context, s *store.Store, ref string) (int64, error) {
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		// Existence check — Get returns shared.ErrNotFound otherwise.
		if _, err := s.Snapshots().Get(ctx, id); err != nil {
			return 0, fmt.Errorf("snapshot id %d: %w", id, err)
		}
		return id, nil
	}
	rows, err := s.Snapshots().List(ctx, ref)
	if err != nil {
		return 0, fmt.Errorf("list snapshots by git_ref %q: %w", ref, err)
	}
	if len(rows) == 0 {
		return 0, fmt.Errorf("no snapshot found for git_ref %q", ref)
	}
	// List returns newest first per the Snapshots port doc.
	return rows[0].ID, nil
}

func printDiffText(cmd *cobra.Command, d *diff.SnapshotDiff) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "diff %s -> %s\n", d.ARef, d.BRef)
	fmt.Fprintf(w, "  features:    +%d / -%d / ~%d\n",
		len(d.Features.Added), len(d.Features.Removed), len(d.Features.Changed))
	fmt.Fprintf(w, "  symbols:     +%d / -%d / ~%d\n",
		len(d.Symbols.Added), len(d.Symbols.Removed), len(d.Symbols.Changed))
	fmt.Fprintf(w, "  edges:       +%d / -%d\n",
		len(d.Edges.Added), len(d.Edges.Removed))
	fmt.Fprintf(w, "  annotations: +%d / -%d / ~%d\n",
		len(d.Annotations.Added), len(d.Annotations.Removed), len(d.Annotations.Changed))
	fmt.Fprintf(w, "  contracts:   +%d / -%d / ~%d\n",
		len(d.Contracts.Added), len(d.Contracts.Removed), len(d.Contracts.Changed))
	fmt.Fprintf(w, "  patterns:    +%d / -%d\n",
		len(d.PatternMatches.Gained), len(d.PatternMatches.Lost))
	fmt.Fprintf(w, "  audit:       +%d / -%d / ~%d  (missing_on_a=%d missing_on_b=%d)\n",
		len(d.Audit.Added), len(d.Audit.Removed), len(d.Audit.Changed),
		len(d.Audit.MissingOnA), len(d.Audit.MissingOnB))
	fmt.Fprintf(w, "  coverage:    +%d / -%d / ~%d\n",
		len(d.Coverage.Added), len(d.Coverage.Removed), len(d.Coverage.Changed))
	if d.IsEmpty() {
		fmt.Fprintln(w, "  (no differences)")
	}
}
