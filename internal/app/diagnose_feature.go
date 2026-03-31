package app

import (
	"fmt"

	"github.com/sosalejandro/testreg/internal/domain"
	"github.com/sosalejandro/testreg/internal/ports"
)

// DiagnoseOutput holds the result of diagnosing a feature failure.
type DiagnoseOutput struct {
	FeatureID  string
	Symptom    string
	Rule       *domain.SymptomRule
	Trace      *TraceOutput
	CheckFiles []string
}

// DiagnoseFeatureUseCase diagnoses a feature failure based on a symptom.
type DiagnoseFeatureUseCase struct {
	traceUC *TraceFeatureUseCase
}

// NewDiagnoseFeatureUseCase creates a new DiagnoseFeatureUseCase.
func NewDiagnoseFeatureUseCase(traceUC *TraceFeatureUseCase) *DiagnoseFeatureUseCase {
	return &DiagnoseFeatureUseCase{traceUC: traceUC}
}

// Execute diagnoses a feature based on a symptom string.
//  1. Match the symptom to a diagnostic rule.
//  2. Trace the feature graph.
//  3. Filter the trace to show relevant nodes based on the rule's CheckOrder.
//  4. Return the diagnosis with check order and relevant files.
func (uc *DiagnoseFeatureUseCase) Execute(registryDir, featureID, symptom string, config ports.GraphConfig) (*DiagnoseOutput, error) {
	// Match symptom against rules.
	rules := domain.DefaultSymptomRules()
	rule := domain.MatchSymptom(symptom, rules)

	// Trace the feature graph regardless of whether a rule matched.
	traceOutput, err := uc.traceUC.Execute(registryDir, featureID, config)
	if err != nil {
		return nil, fmt.Errorf("tracing feature %q: %w", featureID, err)
	}

	// Extract files to check, ordered by the rule's check order.
	var checkFiles []string
	if rule != nil {
		checkFiles = extractCheckFiles(traceOutput, rule.CheckOrder)
	}

	return &DiagnoseOutput{
		FeatureID:  featureID,
		Symptom:    symptom,
		Rule:       rule,
		Trace:      traceOutput,
		CheckFiles: checkFiles,
	}, nil
}

// extractCheckFiles walks the trace results and returns file paths ordered by
// the check order's node kinds. Files matching the first kind in the check order
// appear first, then files matching the second kind, and so on.
func extractCheckFiles(trace *TraceOutput, checkOrder []string) []string {
	// Collect files grouped by node kind from all traces.
	kindFiles := make(map[string][]string)
	seen := make(map[string]bool)

	for _, tr := range trace.Traces {
		if tr == nil || tr.Root == nil {
			continue
		}
		collectFilesByKind(tr.Root, kindFiles, seen)
	}

	// Build the ordered result following the check order.
	var result []string
	for _, kind := range checkOrder {
		for _, f := range kindFiles[kind] {
			result = append(result, f)
		}
	}

	// Append any remaining files not yet included (from kinds not in the check order).
	remaining := make(map[string]bool)
	for _, f := range result {
		remaining[f] = true
	}
	for _, files := range kindFiles {
		for _, f := range files {
			if !remaining[f] {
				remaining[f] = true
				result = append(result, f)
			}
		}
	}

	return result
}

// collectFilesByKind recursively walks a trace tree and groups file paths by
// their node kind.
func collectFilesByKind(tn *domain.TraceNode, kindFiles map[string][]string, seen map[string]bool) {
	if tn == nil || tn.Node == nil {
		return
	}

	key := string(tn.Node.Kind) + ":" + tn.Node.File
	if tn.Node.File != "" && !seen[key] {
		seen[key] = true
		kindFiles[string(tn.Node.Kind)] = append(kindFiles[string(tn.Node.Kind)], tn.Node.File)
	}

	if tn.IsCycle {
		return
	}

	for _, child := range tn.Children {
		collectFilesByKind(child, kindFiles, seen)
	}
}
