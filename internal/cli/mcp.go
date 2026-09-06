package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/indexfresh"
	"github.com/sosalejandro/atlas/packages/mcp"
	"github.com/sosalejandro/atlas/packages/store"
)

// newMCPCmd implements `atlas mcp` — the Model Context Protocol server, on
// stdio, exposing the index read-only to coding agents.
//
// The command is not interactive: an MCP client launches it as a subprocess
// and speaks JSON-RPC over its stdin/stdout. That is also why it takes almost
// no flags. Anything an operator would tune belongs in the client's server
// config, where it lives next to the command line that starts this process.
func newMCPCmd() *cobra.Command {
	var limits mcp.Limits
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the feature/symbol graph to coding agents over MCP (stdio)",
		Long: `mcp runs an MCP server on stdio, exposing the atlas index to a coding
agent as tools it can call directly instead of shelling out to the CLI and
parsing --json.

The server is READ-ONLY by construction. No tool writes to the store, runs a
scan or executes anything: the handlers are wired to interfaces that declare
only reads, so a hallucinated tool call cannot corrupt the index.

Tools: find_feature, feature_surface, symbol_info, callers, callees,
tests_covering, coverage_for. Run ` + "`atlas mcp --json`" + ` to print the full
catalog with each tool's input schema.

Wire it into a client by pointing its server config at this command, e.g.

    {"mcpServers": {"atlas": {"command": "atlas", "args": ["mcp"]}}}

stdout carries the protocol and nothing else; diagnostics go to stderr. See
docs/commands/mcp.md.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runMCP(cmd, limits)
		},
	}
	cmd.Flags().IntVar(&limits.MaxFeatures, "max-features", 0,
		"cap on features returned by find_feature (0 = server default)")
	cmd.Flags().IntVar(&limits.MaxSymbols, "max-symbols", 0,
		"cap on symbols returned by feature_surface (0 = server default)")
	cmd.Flags().IntVar(&limits.MaxEdges, "max-edges", 0,
		"cap on call edges returned by callers/callees (0 = server default)")
	cmd.Flags().IntVar(&limits.MaxTests, "max-tests", 0,
		"cap on tests returned by tests_covering (0 = server default)")
	return cmd
}

func runMCP(cmd *cobra.Command, limits mcp.Limits) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	dbPath, err := resolveDBPath(loaded, flags.DBPath)
	if err != nil {
		return err
	}
	// Opening (and migrating) an absent store is deliberate: an MCP client
	// starts this process when the editor session starts, which is routinely
	// before anyone has run `atlas scan`. Failing there would present the user
	// with a dead server; an empty store answers every tool with a structured
	// "no data — run atlas scan", which is the useful thing to say.
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("mcp: open store %s: %w", dbPath, err)
	}
	defer func() { _ = s.Close() }()

	index := mcp.FromStore(s)
	version, _, _ := resolveBuildInfo()
	srv, err := mcp.New(mcp.Options{
		Name:      "atlas",
		Version:   version,
		Graph:     index,
		Coverage:  index,
		Scorer:    index.Scorer(),
		Freshness: freshnessHook(s, loaded.repoRoot),
		Limits:    limits,
	})
	if err != nil {
		return fmt.Errorf("mcp: build server: %w", err)
	}

	if flags.JSON {
		// --json describes the server; it does NOT serve. See Server.Describe:
		// an envelope written onto the transport stream would corrupt it.
		return emitJSON(stdoutOrJSON(cmd), "mcp",
			map[string]any{"db_path": dbPath}, srv.Describe(limits), nil)
	}

	// The client shuts this down by closing our stdin, which Serve reads as
	// EOF. The signal context is the belt to that braces: a client that dies
	// without closing the pipe would otherwise leave this process reading a
	// stdin nobody will ever write to again.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(cmd.ErrOrStderr(),
		"atlas mcp: serving %s read-only on stdio (protocol %s)\n", dbPath, mcp.PreferredProtocolVersion)
	if err := srv.Serve(ctx, cmd.InOrStdin(), stdoutOrJSON(cmd)); err != nil {
		return fmt.Errorf("mcp: serve: %w", err)
	}
	return nil
}

// freshnessHook lets the server say whether the spans it is about to hand an
// agent still describe the files on disk.
//
// It is a closure rather than a port passed into packages/mcp because
// indexfresh.Classify takes the WRITABLE store.FileHashes port, and handing
// that to the tool layer would re-open the write path that layer exists to
// keep shut. The write methods stay on this side of the seam.
func freshnessHook(s *store.Store, repoRoot string) mcp.FreshnessFunc {
	if repoRoot == "" {
		return nil // no root to resolve paths against: report nothing rather than guess
	}
	return func(ctx context.Context, paths []string) (map[string]string, error) {
		report, err := indexfresh.Classify(ctx, s.FileHashes(), repoRoot, paths)
		if err != nil {
			return nil, fmt.Errorf("classify index freshness: %w", err)
		}
		out := make(map[string]string, len(report.States))
		for path, state := range report.States {
			out[path] = string(state)
		}
		return out, nil
	}
}
