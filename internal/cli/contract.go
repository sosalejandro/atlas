package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/contract"
)

// newContractCmd builds the `atlas contract` command group.
func newContractCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "contract",
		Short: "API contract extraction",
	}
	cmd.AddCommand(newContractListCmd())
	return cmd
}

// newContractListCmd implements `atlas contract list [--kind <k>]`.
func newContractListCmd() *cobra.Command {
	var (
		kind     string
		rootArg  string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List extracted contracts",
		Long: `contract list runs the contract extractor against a fresh codeindex
scan and prints every discovered contract (Huma operations, HTTP routes,
plain Go/TS funcs with annotations, GraphQL operations).

Filter with --kind {huma-op|route|func|graphql}.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runContractList(cmd, rootArg, kind)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "",
		"filter by ContractKind (huma-op|route|func|graphql)")
	cmd.Flags().StringVar(&rootArg, "root", "",
		"project root for the scan (default: repo root or cwd)")
	return cmd
}

// contractListResult is the JSON payload for `atlas contract list`.
type contractListResult struct {
	Contracts []contract.ContractDef `json:"contracts"`
}

func runContractList(cmd *cobra.Command, rootArg, kind string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	rootDir := rootArg
	if rootDir == "" {
		rootDir = loaded.repoRoot
	}

	idx, err := codeindex.IndexProject(ctx, rootDir, codeindex.Options{
		SkipTS:   loaded.Scan.SkipTS,
		SkipDirs: loaded.Scan.SkipDirs,
	})
	if err != nil {
		return fmt.Errorf("contract list: index %s: %w", rootDir, err)
	}

	ex := contract.NewExtractor(contract.Options{
		ProjectRoot: rootDir,
		SkipTS:      loaded.Scan.SkipTS,
	})
	out, err := ex.Extract(ctx, idx)
	if err != nil {
		return fmt.Errorf("contract list: extract: %w", err)
	}

	defs := out.Defs
	if kind != "" {
		filtered := make([]contract.ContractDef, 0, len(defs))
		for _, d := range defs {
			if string(d.Kind) == kind {
				filtered = append(filtered, d)
			}
		}
		defs = filtered
	}

	res := contractListResult{Contracts: defs}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "contract.list",
			map[string]any{"root": rootDir, "kind": kind}, res, out.Warnings)
	}
	printContractListText(cmd, defs, out.Warnings)
	return nil
}

func printContractListText(cmd *cobra.Command, defs []contract.ContractDef, warns []string) {
	fmt.Fprintf(cmd.OutOrStdout(), "contracts: %d\n", len(defs))
	for _, d := range defs {
		fmt.Fprintf(cmd.OutOrStdout(), "  [%s/%s] %s  %s:%d\n",
			d.Kind, d.Language, d.Name, d.FilePath, d.Line)
		if d.Signature != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "    sig: %s\n", d.Signature)
		}
		if d.Operation.Method != "" || d.Operation.Path != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "    op:  %s %s (id=%s)\n",
				d.Operation.Method, d.Operation.Path, d.Operation.OperationID)
		}
	}
	for _, w := range warns {
		fmt.Fprintf(cmd.ErrOrStderr(), "  warning: %s\n", w)
	}
}
