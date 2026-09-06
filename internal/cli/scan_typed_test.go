package cli

import (
	"context"
	"testing"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/graph"
)

// Typed Go resolution (issue #87) is per package and can fail for reasons
// that have nothing to do with the code: no toolchain on the machine, a
// build that needs credentials to resolve modules, a latency budget that
// cannot absorb the load. docs/languages/go.md named
// Options.SkipTypedResolution as the way out and pointed at "the library
// API" -- which is no recourse at all for someone running the binary.
//
// These pin the flag: that it exists on both commands that scan, and that
// setting it actually reaches goscan rather than being accepted and
// dropped.
func TestSkipTypedResolution_FlagExistsOnBothScanningCommands(t *testing.T) {
	for name, cmd := range map[string]*cobra.Command{
		"init": newInitCmd(),
		"scan": newScanCmd(),
	} {
		f := cmd.Flags().Lookup("skip-typed-resolution")
		if f == nil {
			t.Errorf("atlas %s has no --skip-typed-resolution flag; the docs' escape hatch "+
				"is reachable only by editing code", name)
			continue
		}
		if f.DefValue != "false" {
			t.Errorf("atlas %s --skip-typed-resolution defaults to %q; type checking is the "+
				"default and the flag may only turn it off", name, f.DefValue)
		}
	}
}

// The wiring, end to end through indexProjectFromConfig: with the override
// the Go scanner produces no typed edges, and without it the same tree
// does. The second half is what stops this from passing on a corpus that
// was never type-checked in the first place.
func TestSkipTypedResolution_ReachesTheGoScanner(t *testing.T) {
	const root = "../../packages/codeindex/go/testdata/goldencorpus"
	loaded = Config{repoRoot: root}

	tiers := func(t *testing.T, skip bool) map[graph.ResolutionTier]int {
		t.Helper()
		idx, _, err := indexProjectFromConfig(context.Background(), root, false, nil, false,
			withSkipTypedResolution(skip))
		if err != nil {
			t.Fatalf("indexProjectFromConfig(skip=%v): %v", skip, err)
		}
		out := map[graph.ResolutionTier]int{}
		for _, e := range idx.Graph.Edges {
			out[e.Tier]++
		}
		return out
	}

	typed := tiers(t, false)
	if typed[graph.TierTyped] == 0 {
		t.Fatal("no typed edges with the flag unset; this corpus is not exercising the " +
			"resolver at all, so the assertion below would prove nothing")
	}

	skipped := tiers(t, true)
	if skipped[graph.TierTyped] != 0 {
		t.Errorf("--skip-typed-resolution left %d typed edges; the flag did not reach goscan",
			skipped[graph.TierTyped])
	}
	if skipped[graph.TierNameResolved] == 0 {
		t.Error("no name_resolved edges with typed resolution off; the AST fallback did not run")
	}
}
