package main

import (
	"github.com/spf13/cobra"
)

// newRootCmd builds the top-level `atlas` command. Subcommands are wired
// here; their actual implementations live in sibling files.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "atlas",
		Short:         "Atlas — code graph, coverage, and audit toolkit",
		Long:          "Atlas indexes your codebase via AST + annotations and answers questions about coverage, drift, and impact. Phase 1 ships `trace`.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newTraceCmd())
	root.AddCommand(newScanCoverageCmd())
	return root
}
