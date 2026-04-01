package domain

// ContractOutput holds the full API contract for a feature.
type ContractOutput struct {
	FeatureID   string              `json:"feature_id" yaml:"feature_id"`
	FeatureName string              `json:"feature_name" yaml:"feature_name"`
	Priority    string              `json:"priority" yaml:"priority"`
	EntryPoint  string              `json:"entry_point" yaml:"entry_point"`
	Layers      []ContractLayer     `json:"layers" yaml:"layers"`
	TestFiles   []ContractTestEntry `json:"test_files" yaml:"test_files"`
}

// ContractLayer represents one architectural layer in the call chain.
type ContractLayer struct {
	Number     int           `json:"number" yaml:"number"`
	Name       string        `json:"name" yaml:"name"`
	File       string        `json:"file,omitempty" yaml:"file,omitempty"`
	Line       int           `json:"line,omitempty" yaml:"line,omitempty"`
	NodeID     string        `json:"node_id,omitempty" yaml:"node_id,omitempty"`
	Kind       string        `json:"kind,omitempty" yaml:"kind,omitempty"`
	Signature  string        `json:"signature,omitempty" yaml:"signature,omitempty"`
	InputType  *ContractType `json:"input_type,omitempty" yaml:"input_type,omitempty"`
	OutputType *ContractType `json:"output_type,omitempty" yaml:"output_type,omitempty"`
	DelegateTo string        `json:"delegate_to,omitempty" yaml:"delegate_to,omitempty"`
	Notes      []string      `json:"notes,omitempty" yaml:"notes,omitempty"`
}

// ContractType describes a structured input or output type.
type ContractType struct {
	Name   string          `json:"name" yaml:"name"`
	Fields []ContractField `json:"fields,omitempty" yaml:"fields,omitempty"`
}

// ContractField describes a single field within a contract type.
type ContractField struct {
	Name     string `json:"name" yaml:"name"`
	Type     string `json:"type" yaml:"type"`
	Required bool   `json:"required" yaml:"required"`
}

// TypeExtractor is the interface for extracting struct field information.
// Implemented by adapters.TypeExtractor when go/types is available.
type TypeExtractor interface {
	ExtractContractTypes(nodeID string) (signature string, input *ContractType, output *ContractType)
}

// EnrichWithTypes populates InputType/OutputType on each layer using
// the type extractor. Layers that already have types (from GraphQL schema
// parsing) are skipped.
func (c *ContractOutput) EnrichWithTypes(extractor TypeExtractor) {
	if extractor == nil {
		return
	}
	for i := range c.Layers {
		layer := &c.Layers[i]
		if layer.NodeID == "" {
			continue
		}
		// Skip layers that already have types from schema parsing.
		if layer.InputType != nil && len(layer.InputType.Fields) > 0 {
			continue
		}

		sig, inputType, outputType := extractor.ExtractContractTypes(layer.NodeID)
		if sig != "" && layer.Signature == "" {
			layer.Signature = sig
		}
		if inputType != nil {
			layer.InputType = inputType
		}
		if outputType != nil {
			layer.OutputType = outputType
		}
	}
}

// ContractTestEntry links a test file to a specific layer.
type ContractTestEntry struct {
	File   string `json:"file" yaml:"file"`
	Status string `json:"status" yaml:"status"` // "tested", "untested"
	Layer  string `json:"layer" yaml:"layer"`   // which layer this test covers
}
