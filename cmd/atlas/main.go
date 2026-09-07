// Command atlas is the single-binary CLI entry point for the Atlas toolkit.
//
// Per docs/architecture.md §5, every subcommand is a ~50 LOC adapter that
// parses flags, calls into a packages/<x>/ library, and formats the result.
// No business logic lives in cmd/.
//
// Phase 7 wires every library package (codeindex, store, audit, sprintplan,
// diff, contract, diagnose, coverage, …) into the cobra dispatch tree. The
// per-verb implementations live in internal/cli/<verb>.go; this file is the
// thin os.Args entry shim.
package main

import (
	"fmt"
	"os"

	"github.com/sosalejandro/atlas/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		// Not a literal 1. The status distinguishes "I checked and the
		// gate does not hold" from "I could not check", which a CI job
		// needs in order to tell a real finding from a broken run. The
		// contract, and why this repository paid to learn it, is in
		// internal/cli/exitcode.go.
		os.Exit(cli.ExitCodeFor(err))
	}
}
