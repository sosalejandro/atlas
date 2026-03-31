package ports

import "github.com/sosalejandro/testreg/internal/domain"

// GraphBuilder constructs the call graph from source code.
type GraphBuilder interface {
	// Build constructs the full call graph for the project.
	Build(projectRoot string, config GraphConfig) (*domain.Graph, error)

	// BuildFrom constructs a partial graph starting from specific entry points.
	// This is the lazy/efficient version for trace commands.
	BuildFrom(projectRoot string, entryPoints []string, config GraphConfig) (*domain.Graph, error)
}
