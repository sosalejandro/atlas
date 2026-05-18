// Command atlas is the single-binary CLI entry point for the Atlas toolkit.
//
// Per docs/architecture.md §5, every subcommand is a ~50 LOC adapter that
// parses flags, calls into a packages/<x>/ library, and formats the result.
// No business logic lives in cmd/.
//
// Phase 1 ships exactly one subcommand: `atlas trace`. Future phases add
// init / scan / cov / audit / sprint / diff / contract / diagnose /
// migrate-annotations per the architecture doc.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
