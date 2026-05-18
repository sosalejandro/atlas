package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/sosalejandro/atlas/internal/adapters"
	"github.com/sosalejandro/atlas/internal/domain"
	"github.com/spf13/cobra"
)

var (
	runPlatform string
	runType     string
	runDryRun   bool
	runFailing  bool
	runPriority string
)

var runCmd = &cobra.Command{
	Use:   "run [feature-id]",
	Short: "Execute tests for a feature or set of features",
	Long: `Runs tests associated with a feature in the registry. Collects all
run commands from the matched feature's coverage entries and executes
them sequentially. Supports filtering by platform, test type, and
priority level. Use --dry-run to preview commands without executing.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		metrics := adapters.NewMetrics(metricsEnabled)
		defer metrics.Print(os.Stderr)

		if len(args) == 0 && !runFailing && runPriority == "" {
			return fmt.Errorf("specify a feature-id, --failing, or --priority")
		}

		store := adapters.NewYAMLStore()
		registry, err := store.LoadAll(resolvedRegistryDir())
		if err != nil {
			return fmt.Errorf("loading registry: %w", err)
		}

		var commands []string

		switch {
		case runFailing:
			// Run all failing features
			features := registry.AllFeatures()
			commands = domain.CollectFailingRunCommands(features, runPlatform, runType)
			if len(commands) == 0 {
				fmt.Println("No failing features found.")
				return nil
			}

		case runPriority != "":
			// Run by priority level
			p := domain.Priority(runPriority)
			if err := p.Validate(); err != nil {
				return err
			}
			features := registry.AllFeatures()
			commands = domain.CollectRunCommandsByPriority(features, p, runPlatform, runType)
			if len(commands) == 0 {
				fmt.Printf("No tests found for priority %q.\n", runPriority)
				return nil
			}

		default:
			// Run a specific feature
			featureID := args[0]
			feature, findErr := registry.GetFeature(featureID)
			if findErr != nil {
				return fmt.Errorf("feature %q not found in registry", featureID)
			}
			commands = domain.CollectRunCommands(feature, runPlatform, runType)
			if len(commands) == 0 {
				fmt.Printf("No run commands found for feature %q", featureID)
				if runPlatform != "" {
					fmt.Printf(" (platform=%s)", runPlatform)
				}
				if runType != "" {
					fmt.Printf(" (type=%s)", runType)
				}
				fmt.Println(".")
				fmt.Println("Run 'testreg scan' first to populate run commands from @testreg annotations.")
				return nil
			}
		}

		// Deduplicate commands (same command can appear from multiple features)
		commands = deduplicateCommands(commands)

		if runDryRun {
			fmt.Printf("Dry run: %d command(s) would be executed:\n\n", len(commands))
			for i, c := range commands {
				fmt.Printf("  [%d] %s\n", i+1, c)
			}
			return nil
		}

		// Execute commands
		fmt.Printf("Running %d test command(s)...\n\n", len(commands))

		passed := 0
		failed := 0
		var failures []string

		for i, c := range commands {
			fmt.Printf("[%d/%d] %s\n", i+1, len(commands), c)

			exitCode := executeCommand(c, resolvedProjectRoot())
			if exitCode == 0 {
				passed++
				fmt.Println("  PASSED")
			} else {
				failed++
				failures = append(failures, c)
				fmt.Printf("  FAILED (exit code %d)\n", exitCode)
			}
			fmt.Println()
		}

		// Print summary
		fmt.Println(strings.Repeat("-", 60))
		fmt.Printf("Summary: %d passed, %d failed, %d total\n", passed, failed, len(commands))

		if len(failures) > 0 {
			fmt.Println("\nFailed commands:")
			for _, f := range failures {
				fmt.Printf("  %s\n", f)
			}
			return fmt.Errorf("%d test command(s) failed", failed)
		}

		return nil
	},
}

func init() {
	runCmd.Flags().StringVar(&runPlatform, "platform", "", "Filter by platform (backend, web, mobile)")
	runCmd.Flags().StringVar(&runType, "type", "", "Filter by test type (unit, integration, e2e)")
	runCmd.Flags().BoolVar(&runDryRun, "dry-run", false, "Show commands without executing")
	runCmd.Flags().BoolVar(&runFailing, "failing", false, "Run only failing features")
	runCmd.Flags().StringVar(&runPriority, "priority", "", "Run tests by priority level (critical, high, medium, low)")
	rootCmd.AddCommand(runCmd)
}

// executeCommand runs a shell command in the given working directory
// and returns the exit code.
func executeCommand(command, workDir string) int {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return 1
	}

	// For commands with quoted arguments (like npx vitest -t 'test name'),
	// we need to use shell execution
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = workDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		return 1
	}

	return 0
}

// deduplicateCommands removes duplicate commands while preserving order.
func deduplicateCommands(commands []string) []string {
	seen := make(map[string]bool, len(commands))
	result := make([]string, 0, len(commands))
	for _, c := range commands {
		if !seen[c] {
			seen[c] = true
			result = append(result, c)
		}
	}
	return result
}
