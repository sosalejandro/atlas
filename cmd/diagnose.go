package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/sosalejandro/atlas/internal/adapters"
	"github.com/sosalejandro/atlas/internal/app"
	"github.com/sosalejandro/atlas/internal/domain"
	"github.com/spf13/cobra"
)

var (
	diagnoseSymptom string
	diagnoseJSON    bool
)

var diagnoseCmd = &cobra.Command{
	Use:   "diagnose <feature-id>",
	Short: "Diagnose a feature failure from an error symptom",
	Long: `Matches an error symptom against known failure patterns and traces
the feature's dependency graph to identify which files to check first.
The diagnosis shows the matched rule, the likely failure layer, and
an ordered list of files to investigate.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		metrics := adapters.NewMetrics(metricsEnabled)
		defer metrics.Print(os.Stderr)

		featureID := args[0]

		if diagnoseSymptom == "" {
			return fmt.Errorf("--symptom is required")
		}

		// Load graph configuration.
		graphSection, err := adapters.LoadGraphConfig(resolvedProjectRoot())
		if err != nil {
			return fmt.Errorf("loading graph config: %w", err)
		}

		config := graphSection.ToPortsConfig()
		config.ProjectRoot = resolvedProjectRoot()

		// Create dependencies.
		store := adapters.NewYAMLStore()
		builder := adapters.NewGraphBuilder(config)
		traceUC := app.NewTraceFeatureUseCase(store, builder)
		useCase := app.NewDiagnoseFeatureUseCase(traceUC)

		result, err := useCase.Execute(resolvedRegistryDir(), featureID, diagnoseSymptom, config)
		if err != nil {
			return fmt.Errorf("diagnosing feature %q: %w", featureID, err)
		}

		if diagnoseJSON {
			return outputDiagnoseJSON(result)
		}

		return outputDiagnoseTerminal(result)
	},
}

func init() {
	diagnoseCmd.Flags().StringVar(&diagnoseSymptom, "symptom", "", "Error symptom to diagnose (required)")
	diagnoseCmd.Flags().BoolVar(&diagnoseJSON, "json", false, "Output as JSON")
	rootCmd.AddCommand(diagnoseCmd)
}

// diagnoseJSONOutput is the JSON-serializable representation of a diagnosis.
type diagnoseJSONOutput struct {
	FeatureID  string              `json:"feature_id"`
	Symptom    string              `json:"symptom"`
	BestMatch  *diagnoseRuleJSON   `json:"best_match,omitempty"`
	AllMatches []diagnoseRuleJSON  `json:"all_matches,omitempty"`
	CheckFiles []string            `json:"check_files,omitempty"`
}

type diagnoseRuleJSON struct {
	Layer       string   `json:"layer"`
	Confidence  float64  `json:"confidence"`
	Description string   `json:"description"`
	CheckOrder  []string `json:"check_order"`
}

func outputDiagnoseJSON(result *app.DiagnoseOutput) error {
	out := diagnoseJSONOutput{
		FeatureID:  result.FeatureID,
		Symptom:    result.Symptom,
		CheckFiles: result.CheckFiles,
	}

	if result.Rule != nil {
		out.BestMatch = &diagnoseRuleJSON{
			Layer:       result.Rule.Layer,
			Confidence:  result.Rule.Confidence,
			Description: result.Rule.Description,
			CheckOrder:  result.Rule.CheckOrder,
		}
	}

	for _, r := range result.AllRules {
		out.AllMatches = append(out.AllMatches, diagnoseRuleJSON{
			Layer:       r.Layer,
			Confidence:  r.Confidence,
			Description: r.Description,
			CheckOrder:  r.CheckOrder,
		})
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(out)
}

func outputDiagnoseTerminal(result *app.DiagnoseOutput) error {
	renderer := adapters.NewGraphRenderer()

	// Extract the first domain.TraceResult for rendering, if available.
	var firstTrace *domain.TraceResult
	if result.Trace != nil && len(result.Trace.Traces) > 0 {
		firstTrace = result.Trace.Traces[0]
	}

	renderer.RenderDiagnosisMulti(result.FeatureID, result.Symptom, result.Rule, result.AllRules, firstTrace, os.Stdout)

	if len(result.CheckFiles) > 0 {
		fmt.Println("  Files to check (ordered by likelihood):")
		for i, f := range result.CheckFiles {
			fmt.Printf("    %d. %s\n", i+1, f)
		}
		fmt.Println()
	}

	return nil
}
