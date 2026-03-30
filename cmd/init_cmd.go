package cmd

import (
	"fmt"

	"github.com/sosalejandro/testreg/internal/adapters"
	"github.com/sosalejandro/testreg/internal/app"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Bootstrap the test registry with template domain files",
	Long: `Creates the registry directory and populates it with template YAML files
containing feature definitions. If files already exist, new features are
merged without overwriting manual edits. This operation is idempotent.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		store := adapters.NewYAMLStore()
		useCase := app.NewInitRegistryUseCase(store, store)

		dir := resolvedRegistryDir()
		if err := useCase.Execute(dir); err != nil {
			return fmt.Errorf("initializing registry: %w", err)
		}

		fmt.Printf("Registry initialized at %s\n", dir)
		fmt.Println("Edit the YAML files to add your project's features and coverage data.")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
}
