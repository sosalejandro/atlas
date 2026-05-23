package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

// Build-time metadata. Linker overrides via -ldflags="-X ...":
//
//	-X github.com/sosalejandro/atlas/internal/cli.Version=v0.7.0
//	-X github.com/sosalejandro/atlas/internal/cli.Commit=$(git rev-parse HEAD)
//	-X github.com/sosalejandro/atlas/internal/cli.BuildDate=$(date -u +%FT%TZ)
//
// When ldflags are absent (e.g. `go install
// github.com/sosalejandro/atlas/cmd/atlas@v0.1.3`, which does not pass
// ldflags), resolveBuildInfo() falls back to runtime/debug.ReadBuildInfo()
// so the version string still reflects the installed module version and
// VCS revision. The defaults below are only the last-resort sentinel
// values — see internal/cli/buildinfo.go for the resolution contract.
var (
	Version   = "v0.4.1"
	Commit    = "d34a0e5"
	BuildDate = "2026-05-23T07:41:42Z"
)

// globalFlags holds the cobra-bound values for the persistent flags every
// subcommand can consult. Populated by cobra at parse time; consumed by
// the per-verb RunE functions via the globals helper.
type globalFlags struct {
	JSON       bool
	DBPath     string
	ConfigPath string
	Verbose    bool
}

// flags is the package-level singleton holding the parsed global flag
// values. cobra populates this before any RunE fires.
var flags globalFlags

// loaded is the package-level cache of the loaded Config. PersistentPreRunE
// fills it once per process; subcommands then read it for free.
var loaded Config

// NewRootCmd builds a fresh root command tree. Exposed for tests that want
// to drive the tree without invoking Execute directly.
//
// NOTE: `flags` and `loaded` are package-level singletons reset at the top
// of this function. That is safe in production (one binary invocation = one
// NewRootCmd call) but unsafe under t.Parallel(): tests that drive
// NewRootCmd MUST NOT use t.Parallel() until these are refactored into
// per-cmd context. Tracked for a future cleanup pass.
func NewRootCmd() *cobra.Command {
	// Reset flags per invocation so repeat test calls don't bleed state.
	flags = globalFlags{}
	loaded = Config{}

	version, commit, builtAt := resolveBuildInfo()
	root := &cobra.Command{
		Use:           "atlas",
		Short:         "Atlas — code graph, coverage, and audit toolkit",
		Long:          atlasLong,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       fmt.Sprintf("%s (commit %s, built %s)", version, commit, builtAt),
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(flags.ConfigPath)
			if err != nil {
				return err
			}
			loaded = cfg
			return nil
		},
	}

	root.PersistentFlags().BoolVar(&flags.JSON, "json", false,
		"emit stable JSON envelope instead of human-friendly text")
	root.PersistentFlags().StringVar(&flags.DBPath, "db-path", "",
		"override the SQLite state path (default: .atlas/atlas.db at repo root)")
	root.PersistentFlags().StringVar(&flags.ConfigPath, "config", "",
		"path to an explicit .atlas.yaml (default: lookup at repo root)")
	root.PersistentFlags().BoolVarP(&flags.Verbose, "verbose", "v", false,
		"enable verbose human-readable output (no effect with --json)")

	root.AddCommand(newInitCmd())
	root.AddCommand(newScanCmd())
	root.AddCommand(newTraceCmd())
	root.AddCommand(newCovCmd())
	root.AddCommand(newAuditCmd())
	root.AddCommand(newSprintCmd())
	root.AddCommand(newDiffCmd())
	root.AddCommand(newSnapshotCmd())
	root.AddCommand(newContractCmd())
	root.AddCommand(newDiagnoseCmd())
	root.AddCommand(newCodebaseCmd())
	root.AddCommand(newMigrateAnnotationsCmd())

	return root
}

// Execute is the single entry point called from cmd/atlas/main.go. Returns
// the cobra command's error so main can pick the correct exit code.
//
// The atlasIO seam (out/err) is wired to os.Stdout / os.Stderr at the
// outer boundary so tests can substitute buffers.
func Execute() error {
	root := NewRootCmd()
	root.SetIn(os.Stdin)
	root.SetOut(os.Stdout)
	root.SetErr(os.Stderr)
	return root.ExecuteContext(context.Background()) //nolint:wrapcheck // cobra's err is already informative.
}

// stdoutOrJSON picks the writer the subcommand should print its primary
// output to. For both --json and human modes the output goes to stdout;
// stderr is reserved for diagnostics. Centralising this keeps the per-verb
// files free of plumbing branches.
func stdoutOrJSON(cmd *cobra.Command) io.Writer { return cmd.OutOrStdout() }

const atlasLong = `Atlas indexes your codebase via AST + annotations and answers
questions about coverage, drift, and impact.

Common workflows:

  atlas init               # first scan + persist state at .atlas/atlas.db
  atlas scan               # incremental re-scan (file-hash based)
  atlas trace <id>         # walk call chain for a feature or symbol
  atlas cov sync           # ingest test framework output
  atlas audit              # health scores per feature
  atlas sprint             # ranked backlog (gap-weighted priority)

Every subcommand accepts --json for a stable structured envelope. See
docs/json-output.md for the schema and docs/api/<verb>.md for per-verb fields.`
