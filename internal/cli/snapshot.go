package cli

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/audit"
	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/diff"
	"github.com/sosalejandro/atlas/packages/store"
)

// newSnapshotCmd implements `atlas snapshot [--ref <ref>] [--note <text>]`.
//
// Captures the current scan + audit state into the `snapshots` table so a
// later `atlas diff` invocation can compare against it.
func newSnapshotCmd() *cobra.Command {
	var (
		ref          string
		note         string
		rootArg      string
		includeAudit bool
	)
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Capture current scan + audit state into the snapshots table",
		Long: `snapshot runs a fresh scan, optionally an audit pass, and persists
the combined view as one row in the snapshots table.

The git ref defaults to HEAD (via 'git rev-parse HEAD'); pass --ref
to override. --note is optional free-form metadata.

With --include-audit (default) the snapshot also carries the
audit-score slice in its audit_json column so later diffs can detect
score regressions.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSnapshot(cmd, rootArg, ref, note, includeAudit)
		},
	}
	cmd.Flags().StringVar(&ref, "ref", "",
		"git ref to record on the snapshot row (default: current HEAD)")
	cmd.Flags().StringVar(&note, "note", "",
		"free-form note recorded alongside the snapshot")
	cmd.Flags().StringVar(&rootArg, "root", "",
		"project root for the fresh scan (default: repo root or cwd)")
	cmd.Flags().BoolVar(&includeAudit, "include-audit", true,
		"compute + persist the audit slice alongside the index")
	return cmd
}

// snapshotResult is the JSON payload for `atlas snapshot`.
type snapshotResult struct {
	ID         int64  `json:"id"`
	GitRef     string `json:"git_ref"`
	Note       string `json:"note,omitempty"`
	IncludesAudit bool `json:"includes_audit"`
}

func runSnapshot(cmd *cobra.Command, rootArg, ref, note string, includeAudit bool) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	rootDir := rootArg
	if rootDir == "" {
		rootDir = loaded.repoRoot
	}
	if ref == "" {
		ref = currentGitRef(rootDir)
	}
	if ref == "" {
		return fmt.Errorf("snapshot: --ref is required when the working dir is not a git repo")
	}

	dbPath, err := resolveDBPath(loaded, flags.DBPath)
	if err != nil {
		return err
	}

	idx, err := codeindex.IndexProject(ctx, rootDir, codeindex.Options{
		SkipTS:    loaded.Scan.SkipTS,
		SkipDirs:  loaded.Scan.SkipDirs,
		HashFiles: true,
	})
	if err != nil {
		return fmt.Errorf("snapshot: index %s: %w", rootDir, err)
	}

	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("snapshot: open store %s: %w", dbPath, err)
	}
	defer func() { _ = s.Close() }()

	// Ingest first so the audit/diff has fresh symbols + annotations to
	// chew on; harmless when the caller is also running `atlas scan`
	// separately.
	if _, err := s.Ingest(ctx, idx); err != nil {
		return fmt.Errorf("snapshot: ingest: %w", err)
	}

	indexJSON, err := diff.EncodeIndexJSON(idx)
	if err != nil {
		return fmt.Errorf("snapshot: encode index: %w", err)
	}

	input := store.CaptureInput{
		GitRef:    ref,
		IndexJSON: indexJSON,
	}
	if note != "" {
		n := note
		input.Notes = &n
	}

	if includeAudit {
		a := audit.New(s, audit.Options{
			FreshnessWindow:     loaded.freshnessWindow(),
			ContractDriftWindow: loaded.contractDriftWindow(),
			GitBlame:            audit.NewGitBlame(loaded.repoRoot),
		})
		healths, err := a.ScoreAll(ctx)
		if err != nil {
			return fmt.Errorf("snapshot: score all: %w", err)
		}
		// Adapt audit.FeatureHealth → diff.FeatureHealth (different shape:
		// integer Score, optional layer scores + blocking findings).
		dh := make([]diff.FeatureHealth, 0, len(healths))
		for _, h := range healths {
			layer := make(map[string]int, len(h.Components))
			for k, v := range h.Components {
				layer[k] = int(v)
			}
			dh = append(dh, diff.FeatureHealth{
				FeatureID:        h.FeatureID,
				Score:            int(h.Score),
				LayerScores:      layer,
				BlockingFindings: h.Reasons,
			})
		}
		auditJSON, err := diff.EncodeAuditJSON(dh)
		if err != nil {
			return fmt.Errorf("snapshot: encode audit: %w", err)
		}
		if auditJSON != "" {
			input.AuditJSON = &auditJSON
		}
	}

	id, err := s.Snapshots().Capture(ctx, input)
	if err != nil {
		return fmt.Errorf("snapshot: capture: %w", err)
	}

	res := snapshotResult{ID: id, GitRef: ref, Note: note, IncludesAudit: includeAudit}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "snapshot",
			map[string]any{"ref": ref, "note": note, "root": rootDir,
				"include_audit": includeAudit}, res, nil)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "snapshot captured  id=%d git_ref=%s%s\n",
		id, ref, fmtNoteSuffix(note))
	return nil
}

func fmtNoteSuffix(n string) string {
	if n == "" {
		return ""
	}
	return "  note=" + n
}

// currentGitRef returns `git rev-parse HEAD` for the working dir, or ""
// when not in a git repo.
func currentGitRef(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
