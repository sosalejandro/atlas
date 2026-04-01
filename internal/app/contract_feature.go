package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sosalejandro/testreg/internal/adapters"
	"github.com/sosalejandro/testreg/internal/domain"
	"github.com/sosalejandro/testreg/internal/ports"
)

// ContractTypeExtractor abstracts struct field extraction from go/types
// so the contract use case can optionally enrich layers with type information.
// The method returns the resolved signature and input/output contract types.
type ContractTypeExtractor interface {
	// ExtractContractTypes extracts the function signature, first struct input
	// parameter (skipping context.Context), and first struct return value
	// (skipping error) for the given node ID.
	ExtractContractTypes(nodeID string) (signature string, input *domain.ContractType, output *domain.ContractType)
}

// ContractFeatureUseCase builds a full API contract for a feature by
// combining trace data, registry information, and optional GraphQL schema.
type ContractFeatureUseCase struct {
	traceUC       *TraceFeatureUseCase
	registry      ports.RegistryReader
	typeExtractor ContractTypeExtractor // nil when type_checking is off
}

// NewContractFeatureUseCase creates a new ContractFeatureUseCase.
func NewContractFeatureUseCase(traceUC *TraceFeatureUseCase, registry ports.RegistryReader) *ContractFeatureUseCase {
	return &ContractFeatureUseCase{
		traceUC:  traceUC,
		registry: registry,
	}
}

// SetTypeExtractor attaches a ContractTypeExtractor for enriching contract
// layers with struct field information from go/types. When nil, layers show
// signatures but no field tables.
func (uc *ContractFeatureUseCase) SetTypeExtractor(te ContractTypeExtractor) {
	uc.typeExtractor = te
}

// Execute builds the contract output for a feature.
//  1. Load the feature from registry to get metadata and test files.
//  2. Trace the call chain (existing TraceFeatureUseCase).
//  3. Walk the trace tree and build ContractLayers from each node.
//  4. If entry is GRAPHQL and schema_dirs configured, parse schema and prepend as Layer 0.
//  5. Collect test file information.
func (uc *ContractFeatureUseCase) Execute(registryDir, featureID string, config ports.GraphConfig) (*domain.ContractOutput, error) {
	// Step 1: Load feature from registry for metadata.
	reg, err := uc.registry.LoadAll(registryDir)
	if err != nil {
		return nil, fmt.Errorf("loading registry from %s: %w", registryDir, err)
	}

	feature, err := reg.GetFeature(featureID)
	if err != nil {
		return nil, fmt.Errorf("feature %q not found in registry: %w", featureID, err)
	}

	// Determine the entry point description.
	entryPoint := deriveEntryPointLabel(feature)

	// Step 2: Trace the call chain.
	traceOutput, err := uc.traceUC.Execute(registryDir, featureID, config)
	if err != nil {
		return nil, fmt.Errorf("tracing feature %q: %w", featureID, err)
	}

	// Step 3: Build contract layers from each trace tree.
	var layers []domain.ContractLayer
	for _, tr := range traceOutput.Traces {
		if tr == nil || tr.Root == nil {
			continue
		}
		traceLayers := uc.buildLayersFromTrace(tr.Root)
		layers = append(layers, traceLayers...)
	}

	// Re-number layers sequentially starting from 1.
	for i := range layers {
		layers[i].Number = i + 1
	}

	// Step 4: If entry is GRAPHQL and schema_dirs configured, prepend schema layer.
	if isGraphQLFeature(feature) && len(config.GraphQLSchemaDirs) > 0 {
		schemaLayer, err := uc.buildGraphQLSchemaLayer(feature, config)
		if err == nil && schemaLayer != nil {
			// Shift all existing layers up by 1.
			for i := range layers {
				layers[i].Number++
			}
			layers = append([]domain.ContractLayer{*schemaLayer}, layers...)
		}
	}

	// Step 5: Enrich layers with type information (Phase 2) if available.
	if uc.typeExtractor != nil {
		uc.enrichLayersWithTypes(layers)
	}

	// Step 6: Collect test file information.
	testFiles := uc.collectContractTestEntries(feature, layers)

	return &domain.ContractOutput{
		FeatureID:   feature.ID,
		FeatureName: feature.Name,
		Priority:    string(feature.Priority),
		EntryPoint:  entryPoint,
		Layers:      layers,
		TestFiles:   testFiles,
	}, nil
}

// buildLayersFromTrace walks the trace tree depth-first following the primary
// path (first child at each level). Branches at each node are recorded as
// "Calls" notes in that layer.
func (uc *ContractFeatureUseCase) buildLayersFromTrace(root *domain.TraceNode) []domain.ContractLayer {
	var layers []domain.ContractLayer
	current := root

	for current != nil {
		if current.Node == nil {
			break
		}

		layer := domain.ContractLayer{
			Name:      layerNameFromKind(string(current.Node.Kind)),
			File:      current.Node.File,
			Line:      current.Node.Line,
			NodeID:    current.Node.ID,
			Kind:      string(current.Node.Kind),
			Signature: current.Node.Signature,
		}

		// If this node has a doc comment, add it as a note.
		if current.Node.Doc != "" {
			layer.Notes = append(layer.Notes, current.Node.Doc)
		}

		// Record branches (non-primary children) as notes.
		if len(current.Children) > 1 {
			var calls []string
			for _, child := range current.Children[1:] {
				if child.Node != nil {
					calls = append(calls, child.Node.ID)
				}
			}
			if len(calls) > 0 {
				layer.Notes = append(layer.Notes, "Also calls: "+strings.Join(calls, ", "))
			}
		}

		// DelegateTo is the first child's node ID.
		if len(current.Children) > 0 && current.Children[0].Node != nil {
			layer.DelegateTo = current.Children[0].Node.ID
		}

		layers = append(layers, layer)

		// Follow the primary path (first child).
		if len(current.Children) > 0 {
			current = current.Children[0]
		} else {
			current = nil
		}
	}

	return layers
}

// enrichLayersWithTypes populates InputType and OutputType on each layer
// by extracting the function's parameter and return struct fields via go/types.
func (uc *ContractFeatureUseCase) enrichLayersWithTypes(layers []domain.ContractLayer) {
	for i := range layers {
		layer := &layers[i]

		if layer.NodeID == "" {
			continue
		}

		// Skip layers that already have types (e.g., from GraphQL schema parsing).
		if layer.InputType != nil || layer.OutputType != nil {
			continue
		}

		sig, input, output := uc.typeExtractor.ExtractContractTypes(layer.NodeID)

		if sig != "" {
			layer.Signature = sig
		}
		if input != nil {
			layer.InputType = input
		}
		if output != nil {
			layer.OutputType = output
		}
	}
}

// buildGraphQLSchemaLayer parses GraphQL schema files and builds a Layer 1
// containing the mutation/query definition and its input/output types.
func (uc *ContractFeatureUseCase) buildGraphQLSchemaLayer(feature *domain.Feature, config ports.GraphConfig) (*domain.ContractLayer, error) {
	parser := adapters.NewGraphQLSchemaParser()

	var mergedSchema *adapters.GraphQLSchema
	for _, schemaDir := range config.GraphQLSchemaDirs {
		dir := schemaDir
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(config.ProjectRoot, dir)
		}

		schema, err := parser.ParseDir(dir)
		if err != nil {
			continue
		}

		if mergedSchema == nil {
			mergedSchema = schema
		} else {
			// Merge types, mutations, queries.
			for k, v := range schema.Types {
				mergedSchema.Types[k] = v
			}
			for k, v := range schema.Mutations {
				mergedSchema.Mutations[k] = v
			}
			for k, v := range schema.Queries {
				mergedSchema.Queries[k] = v
			}
		}
	}

	if mergedSchema == nil {
		return nil, fmt.Errorf("no GraphQL schemas found")
	}

	// Find the matching mutation or query from the feature's API surface.
	for _, api := range feature.Surfaces.API {
		if api.Method != "GRAPHQL" {
			continue
		}

		parts := strings.SplitN(api.Path, ".", 2)
		if len(parts) != 2 {
			continue
		}

		operation := parts[0] // "Mutation" or "Query"
		fieldName := parts[1] // "trainingLogSet"

		var field *adapters.GraphQLField
		switch operation {
		case "Mutation":
			field = mergedSchema.Mutations[fieldName]
		case "Query":
			field = mergedSchema.Queries[fieldName]
		}

		if field == nil {
			continue
		}

		layer := &domain.ContractLayer{
			Number: 1,
			Name:   "GraphQL API",
			Kind:   "graphql",
		}

		// Build signature from the field.
		layer.Signature = buildGraphQLSignature(operation, field)

		// Resolve input type from arguments.
		if len(field.Args) > 0 {
			for _, arg := range field.Args {
				typeName := stripGraphQLModifiers(arg.Type)
				if gqlType, ok := mergedSchema.Types[typeName]; ok {
					layer.InputType = graphQLTypeToContract(gqlType)
					break
				}
			}
		}

		// Resolve return type.
		returnTypeName := stripGraphQLModifiers(field.Type)
		if gqlType, ok := mergedSchema.Types[returnTypeName]; ok {
			layer.OutputType = graphQLTypeToContract(gqlType)
		}

		return layer, nil
	}

	return nil, fmt.Errorf("no matching GraphQL operation found")
}

// collectContractTestEntries gathers test files from the feature's coverage
// entries and maps them to layers based on file naming heuristics.
func (uc *ContractFeatureUseCase) collectContractTestEntries(feature *domain.Feature, layers []domain.ContractLayer) []domain.ContractTestEntry {
	var entries []domain.ContractTestEntry

	knownTestFiles := collectTestFiles(feature)
	if len(knownTestFiles) == 0 {
		return entries
	}

	// Map each known test file to the best matching layer.
	for _, testFile := range knownTestFiles {
		bestLayer := ""
		bestKind := ""
		baseName := filepath.Base(testFile)

		// Try to match test file to a layer's file.
		for _, layer := range layers {
			if layer.File == "" {
				continue
			}
			layerDir := filepath.Dir(layer.File)
			testDir := filepath.Dir(testFile)

			// Same directory or the test file name contains the source file name.
			layerBase := filepath.Base(layer.File)
			layerName := strings.TrimSuffix(layerBase, filepath.Ext(layerBase))

			if testDir == layerDir || strings.Contains(baseName, layerName) {
				bestLayer = layer.Name
				bestKind = layer.Kind
				break
			}
		}

		if bestLayer == "" {
			// Guess from test file name.
			bestLayer = guessLayerFromTestFile(baseName)
		}
		if bestKind == "" {
			bestKind = bestLayer
		}

		entries = append(entries, domain.ContractTestEntry{
			File:   testFile,
			Status: "tested",
			Layer:  bestLayer,
		})
	}

	return entries
}

// deriveEntryPointLabel creates a human-readable entry point label.
func deriveEntryPointLabel(f *domain.Feature) string {
	if len(f.Surfaces.API) == 0 {
		return ""
	}

	api := f.Surfaces.API[0]
	if api.Method == "GRAPHQL" {
		return "GRAPHQL " + api.Path
	}
	return api.Method + " " + api.Path
}

// isGraphQLFeature returns true if the feature has at least one GRAPHQL API surface.
func isGraphQLFeature(f *domain.Feature) bool {
	for _, api := range f.Surfaces.API {
		if api.Method == "GRAPHQL" {
			return true
		}
	}
	return false
}

// layerNameFromKind maps a node kind to a human-readable layer name.
func layerNameFromKind(kind string) string {
	switch kind {
	case "handler":
		return "Handler"
	case "service":
		return "Service"
	case "repository":
		return "Repository"
	case "query":
		return "Query"
	case "component":
		return "Component"
	case "hook":
		return "Hook"
	case "endpoint":
		return "Endpoint"
	case "external":
		return "External"
	default:
		if kind == "" {
			return "Unknown"
		}
		return strings.ToUpper(kind[:1]) + kind[1:]
	}
}

// guessLayerFromTestFile tries to determine the layer a test file covers
// based on common naming patterns.
func guessLayerFromTestFile(baseName string) string {
	lower := strings.ToLower(baseName)
	switch {
	case strings.Contains(lower, "handler") || strings.Contains(lower, "resolver"):
		return "Handler"
	case strings.Contains(lower, "service"):
		return "Service"
	case strings.Contains(lower, "repo") || strings.Contains(lower, "repository"):
		return "Repository"
	case strings.Contains(lower, "query") || strings.Contains(lower, "sql"):
		return "Query"
	case strings.Contains(lower, "component"):
		return "Component"
	case strings.Contains(lower, "hook"):
		return "Hook"
	default:
		return "unknown"
	}
}

// buildGraphQLSignature builds a human-readable GraphQL operation signature.
func buildGraphQLSignature(operation string, field *adapters.GraphQLField) string {
	var sb strings.Builder
	sb.WriteString(strings.ToLower(operation[:1]) + operation[1:])
	sb.WriteString(" {\n  ")
	sb.WriteString(field.Name)
	if len(field.Args) > 0 {
		sb.WriteString("(")
		for i, arg := range field.Args {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(arg.Name)
			sb.WriteString(": ")
			sb.WriteString(arg.Type)
		}
		sb.WriteString(")")
	}
	sb.WriteString(": ")
	sb.WriteString(field.Type)
	sb.WriteString("\n}")
	return sb.String()
}

// stripGraphQLModifiers removes non-null (!) and list ([]) markers from a type name.
func stripGraphQLModifiers(typeName string) string {
	s := strings.TrimSpace(typeName)
	s = strings.TrimSuffix(s, "!")
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	s = strings.TrimSuffix(s, "!")
	return s
}

// graphQLTypeToContract converts a parsed GraphQL type to a domain ContractType.
func graphQLTypeToContract(gqlType *adapters.GraphQLType) *domain.ContractType {
	ct := &domain.ContractType{
		Name: gqlType.Name,
	}
	for _, f := range gqlType.Fields {
		ct.Fields = append(ct.Fields, domain.ContractField{
			Name:     f.Name,
			Type:     strings.TrimSuffix(f.Type, "!"),
			Required: f.Required,
		})
	}
	return ct
}
