package adapters

import (
	"fmt"

	"github.com/sosalejandro/atlas/internal/domain"
	"github.com/sosalejandro/atlas/internal/ports"
)

// StubGraphBuilder is a placeholder implementation of ports.GraphBuilder that
// returns an empty graph with a warning. It exists so that the trace, graph,
// and diagnose commands can function before the real Go AST scanner is
// implemented. Replace this with GoASTScanner once it is ready.
type StubGraphBuilder struct{}

// NewStubGraphBuilder creates a new StubGraphBuilder.
func NewStubGraphBuilder() *StubGraphBuilder {
	return &StubGraphBuilder{}
}

// Build returns an empty graph with a warning that the real scanner is not yet implemented.
func (s *StubGraphBuilder) Build(_ string, _ ports.GraphConfig) (*domain.Graph, error) {
	fmt.Println("WARNING: using stub graph builder — Go AST scanner not yet implemented. Trace data will be empty.")
	return domain.NewGraph(), nil
}

// BuildFrom returns an empty graph with placeholder nodes for each entry point.
// This allows the trace command to show the feature structure even without real
// call graph data.
func (s *StubGraphBuilder) BuildFrom(_ string, entryPoints []string, _ ports.GraphConfig) (*domain.Graph, error) {
	fmt.Println("WARNING: using stub graph builder — Go AST scanner not yet implemented. Trace data will be empty.")

	g := domain.NewGraph()
	for _, ep := range entryPoints {
		g.AddNode(&domain.Node{
			ID:   ep,
			Kind: domain.NodeEndpoint,
			Doc:  "stub entry point (scanner not implemented)",
		})
	}
	return g, nil
}
