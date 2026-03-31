package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var (
	registryDir    string
	projectRoot    string
	metricsEnabled bool
)

var rootCmd = &cobra.Command{
	Use:   "testreg",
	Short: "Test registry — track test coverage across platforms",
	Long: `testreg maintains a YAML registry of feature-to-test mappings across
Go, TypeScript, Playwright, Jest, and Maestro. It scans for test files,
ingests test results, and generates coverage dashboards.`,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&registryDir, "registry-dir", "docs/testing/registry", "Path to registry YAML files")
	rootCmd.PersistentFlags().StringVar(&projectRoot, "project-root", "", "Project root (auto-detected from git root if empty)")
	rootCmd.PersistentFlags().BoolVar(&metricsEnabled, "metrics", false, "Show performance metrics after command execution")

	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if projectRoot == "" {
			detected, err := detectProjectRoot()
			if err != nil {
				projectRoot = "."
			} else {
				projectRoot = detected
			}
		}

		// Resolve registry dir relative to project root if not absolute
		if !filepath.IsAbs(registryDir) {
			registryDir = filepath.Join(projectRoot, registryDir)
		}

		return nil
	}
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

// detectProjectRoot finds the git root directory by running `git rev-parse --show-toplevel`.
func detectProjectRoot() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("detecting git root: %w (are you inside a git repository?)", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// resolvedRegistryDir returns the fully resolved registry directory path.
func resolvedRegistryDir() string {
	return registryDir
}

// resolvedProjectRoot returns the fully resolved project root path.
func resolvedProjectRoot() string {
	return projectRoot
}

// exitOnError prints an error message to stderr and exits with code 1.
func exitOnError(msg string, err error) {
	fmt.Fprintf(os.Stderr, "Error: %s: %s\n", msg, err)
	os.Exit(1)
}
