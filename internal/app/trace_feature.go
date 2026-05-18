package app

import (
	"fmt"
	"strings"

	"github.com/sosalejandro/atlas/internal/domain"
	"github.com/sosalejandro/atlas/internal/ports"
)

// TraceOutput holds the combined trace results for a feature.
type TraceOutput struct {
	FeatureID   string
	FeatureName string
	Priority    string
	Traces      []*domain.TraceResult
	APISurfaces []domain.APISurface
	TestFiles   []string
}

// TraceFeatureUseCase traces a feature's dependency graph.
type TraceFeatureUseCase struct {
	registryReader ports.RegistryReader
	graphBuilder   ports.GraphBuilder
}

// NewTraceFeatureUseCase creates a new TraceFeatureUseCase.
func NewTraceFeatureUseCase(reader ports.RegistryReader, builder ports.GraphBuilder) *TraceFeatureUseCase {
	return &TraceFeatureUseCase{
		registryReader: reader,
		graphBuilder:   builder,
	}
}

// BuildGraph constructs the full call graph for the project. Used by
// ExecuteAll to build the graph once and trace each feature against it.
func (uc *TraceFeatureUseCase) BuildGraph(config ports.GraphConfig) (*domain.Graph, error) {
	return uc.graphBuilder.Build(config.ProjectRoot, config)
}

// Execute traces the dependency graph for a feature.
//  1. Load the feature from the registry to get entry points.
//  2. Build the graph from entry points (lazy mode via BuildFrom).
//  3. For each entry point, trace the call tree.
//  4. Return combined trace results.
func (uc *TraceFeatureUseCase) Execute(registryDir, featureID string, config ports.GraphConfig) (*TraceOutput, error) {
	registry, err := uc.registryReader.LoadAll(registryDir)
	if err != nil {
		return nil, fmt.Errorf("loading registry from %s: %w", registryDir, err)
	}

	feature, err := registry.GetFeature(featureID)
	if err != nil {
		return nil, fmt.Errorf("feature %q not found in registry: %w", featureID, err)
	}

	// Derive entry points from API surfaces.
	entryPoints := deriveEntryPoints(feature)
	if len(entryPoints) == 0 {
		return &TraceOutput{
			FeatureID:   feature.ID,
			FeatureName: feature.Name,
			Priority:    string(feature.Priority),
			APISurfaces: feature.Surfaces.API,
			TestFiles:   collectTestFiles(feature),
		}, nil
	}

	// Build a partial graph from entry points for efficiency.
	graph, err := uc.graphBuilder.BuildFrom(config.ProjectRoot, entryPoints, config)
	if err != nil {
		return nil, fmt.Errorf("building graph for feature %q: %w", featureID, err)
	}

	// Determine max depth from config, default to 10.
	maxDepth := config.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 10
	}

	// Derive the feature's web route for matching frontend callers.
	featureRoute := ""
	if feature.Surfaces.Web != nil {
		featureRoute = feature.Surfaces.Web.Route
	}

	// Build ONE deduplicated full-stack trace per API endpoint:
	// 1. Backend trace downward (handler → service → repo → SQL)
	// 2. Single linear frontend chain upward (route → component → hook → api-service)
	// 3. Stitch into one chain — backend appears exactly once.
	traces := make([]*domain.TraceResult, 0, len(entryPoints))
	for _, ep := range entryPoints {
		backendTrace := graph.TraceFrom(ep, maxDepth)

		// Find the linear path from a frontend route to this endpoint.
		frontendChain := graph.FindPathTo(ep, featureRoute, 6)

		if len(frontendChain) > 1 {
			// Build a linear trace: chain[0] → chain[1] → ... → endpoint → backend
			fullTrace := buildLinearFullStackTrace(graph, frontendChain, backendTrace)
			traces = append(traces, fullTrace)
		} else {
			traces = append(traces, backendTrace)
		}
	}

	return &TraceOutput{
		FeatureID:   feature.ID,
		FeatureName: feature.Name,
		Priority:    string(feature.Priority),
		Traces:      traces,
		APISurfaces: feature.Surfaces.API,
		TestFiles:   collectTestFiles(feature),
	}, nil
}

// buildLinearFullStackTrace creates a single linear trace from the frontend
// caller chain down through the backend. The chain is: [route, component, hook,
// apiService, endpoint]. The backend trace is attached below the endpoint.
func buildLinearFullStackTrace(graph *domain.Graph, chain []string, backendTrace *domain.TraceResult) *domain.TraceResult {
	// Build the frontend portion as a linear chain of TraceNodes.
	var root *domain.TraceNode
	var current *domain.TraceNode

	for i, nodeID := range chain {
		node, ok := graph.Nodes[nodeID]
		if !ok {
			node = &domain.Node{ID: nodeID, Kind: domain.NodeComponent}
		}
		tn := &domain.TraceNode{
			Node:  node,
			Depth: i,
		}
		if root == nil {
			root = tn
		}
		if current != nil {
			current.Children = []*domain.TraceNode{tn}
		}
		current = tn
	}

	// The last node in the chain is the endpoint. Attach the backend trace's
	// children (not the endpoint node itself, since it's already in the chain).
	if current != nil && backendTrace.Root != nil {
		if backendTrace.Root.Node.ID == current.Node.ID {
			// Endpoint is already the last chain node — attach backend children directly.
			for _, child := range backendTrace.Root.Children {
				reDepthAll(child, current.Depth+1)
				current.Children = append(current.Children, child)
			}
		} else {
			// Backend root is different from endpoint — attach as child.
			reDepthAll(backendTrace.Root, current.Depth+1)
			current.Children = append(current.Children, backendTrace.Root)
		}
	}

	frontendNodes := len(chain) - 1 // exclude endpoint (counted in backend)
	return &domain.TraceResult{
		Root:       root,
		TotalNodes: backendTrace.TotalNodes + frontendNodes,
		MaxDepth:   backendTrace.MaxDepth + frontendNodes,
		Cycles:     backendTrace.Cycles,
		Confidence: backendTrace.Confidence,
		Warnings:   backendTrace.Warnings,
	}
}

// pickBestCallerChain selects the single most relevant frontend caller chain
// for a given API endpoint. Selection criteria:
//  1. If featureRoute is set (e.g. "/login"), prefer the chain whose root node
//     ID contains that route (e.g. "route:/login").
//  2. Otherwise, prefer the deepest chain (most context).
//  3. If tied, prefer the first one (deterministic).
func pickBestCallerChain(callerTrees []*domain.TraceNode, featureRoute string) *domain.TraceNode {
	if len(callerTrees) == 0 {
		return nil
	}

	// Strategy 1: Match by feature route.
	if featureRoute != "" {
		for _, tree := range callerTrees {
			if tree.Node != nil && strings.Contains(tree.Node.ID, featureRoute) {
				return tree
			}
		}
		// Also check if any root node ID starts with "route:" and contains the route path.
		for _, tree := range callerTrees {
			if tree.Node != nil && strings.HasPrefix(tree.Node.ID, "route:") {
				routePath := strings.TrimPrefix(tree.Node.ID, "route:")
				if routePath == featureRoute || strings.HasPrefix(featureRoute, routePath) {
					return tree
				}
			}
		}
	}

	// Strategy 2: Pick the deepest chain (most context).
	best := callerTrees[0]
	bestDepth := countNodes(best)
	for _, tree := range callerTrees[1:] {
		d := countNodes(tree)
		if d > bestDepth {
			best = tree
			bestDepth = d
		}
	}
	return best
}

// stitchCallerToBackend attaches the backend trace as the deepest child
// of the frontend caller tree. It walks down the caller tree to find the
// leaf node and replaces it with the backend trace root.
func stitchCallerToBackend(callerRoot *domain.TraceNode, backendRoot *domain.TraceNode) *domain.TraceNode {
	if callerRoot == nil {
		return backendRoot
	}

	// Deep copy the caller tree so we don't mutate the original.
	result := &domain.TraceNode{
		Node:    callerRoot.Node,
		Depth:   0,
		IsCycle: callerRoot.IsCycle,
	}

	if len(callerRoot.Children) == 0 {
		// This is the leaf of the caller tree — attach the backend trace here.
		if backendRoot != nil {
			result.Children = []*domain.TraceNode{reDepth(backendRoot, 1)}
		}
		return result
	}

	// Recurse into children, incrementing depth.
	for _, child := range callerRoot.Children {
		stitched := stitchCallerToBackend(child, backendRoot)
		stitched.Depth = result.Depth + 1
		reDepthAll(stitched, result.Depth+1)
		result.Children = append(result.Children, stitched)
	}

	return result
}

// reDepth adjusts the depth of a trace node and all its children.
func reDepth(node *domain.TraceNode, baseDepth int) *domain.TraceNode {
	if node == nil {
		return nil
	}
	result := &domain.TraceNode{
		Node:    node.Node,
		Depth:   baseDepth,
		IsCycle: node.IsCycle,
	}
	for _, child := range node.Children {
		result.Children = append(result.Children, reDepth(child, baseDepth+1))
	}
	return result
}

// reDepthAll adjusts the depth of all nodes in a tree rooted at node.
func reDepthAll(node *domain.TraceNode, depth int) {
	if node == nil {
		return
	}
	node.Depth = depth
	for _, child := range node.Children {
		reDepthAll(child, depth+1)
	}
}

// countNodes counts the total nodes in a trace tree.
func countNodes(node *domain.TraceNode) int {
	if node == nil {
		return 0
	}
	count := 1
	for _, child := range node.Children {
		count += countNodes(child)
	}
	return count
}

// deriveEntryPoints extracts graph entry point identifiers from a feature's
// API surfaces. For HTTP methods (GET, POST, etc.), the convention is
// "METHOD /path" which the graph builder maps to handler node IDs.
// For non-HTTP methods (GRPC, CONSUMER, EVENT), the path is treated as a
// direct function node ID (e.g., "ProductsServer.CreateProduct").
func deriveEntryPoints(f *domain.Feature) []string {
	httpMethods := map[string]bool{
		"GET": true, "POST": true, "PUT": true, "PATCH": true,
		"DELETE": true, "HEAD": true, "OPTIONS": true,
	}

	var points []string
	for _, api := range f.Surfaces.API {
		if httpMethods[api.Method] {
			// HTTP endpoint: "POST /api/v1/auth/login"
			id := fmt.Sprintf("%s %s", api.Method, api.Path)
			points = append(points, id)
		} else if api.Method == "GRAPHQL" {
			// GraphQL resolver: "Mutation.trainingLogSet" → "mutationResolver.TrainingLogSet"
			id := graphqlEntryPoint(api.Path)
			if id != "" {
				points = append(points, id)
			}
		} else {
			// gRPC/Consumer/Event: use the path directly as function node ID
			// e.g., "ProductsServer.CreateProduct"
			points = append(points, api.Path)
		}
	}
	return points
}

// graphqlEntryPoint converts a GraphQL operation path to a Go resolver node ID.
//
//	"Mutation.trainingLogSet"   → "mutationResolver.TrainingLogSet"
//	"Query.trainingSessions"   → "queryResolver.TrainingSessions"
//	"Subscription.onNewSet"    → "subscriptionResolver.OnNewSet"
func graphqlEntryPoint(path string) string {
	parts := strings.SplitN(path, ".", 2)
	if len(parts) != 2 || parts[1] == "" {
		return ""
	}

	operation := parts[0] // "Mutation"
	field := parts[1]     // "trainingLogSet"

	// Go receiver: mutationResolver, queryResolver, subscriptionResolver
	receiver := strings.ToLower(operation[:1]) + operation[1:] + "Resolver"

	// Go method: PascalCase of the field name (first letter uppercased)
	method := strings.ToUpper(field[:1]) + field[1:]

	return receiver + "." + method
}

// collectTestFiles gathers all known test file paths from a feature's coverage entries.
func collectTestFiles(f *domain.Feature) []string {
	seen := make(map[string]bool)
	var files []string

	addFiles := func(paths []string) {
		for _, p := range paths {
			if !seen[p] {
				seen[p] = true
				files = append(files, p)
			}
		}
	}

	if e := f.Coverage.Unit.Backend; e != nil {
		addFiles(e.AllFiles())
	}
	if e := f.Coverage.Unit.Web; e != nil {
		addFiles(e.AllFiles())
	}
	if e := f.Coverage.Unit.Mobile; e != nil {
		addFiles(e.AllFiles())
	}
	if e := f.Coverage.Integration.Backend; e != nil {
		addFiles(e.AllFiles())
	}
	if e := f.Coverage.Integration.Mobile; e != nil {
		addFiles(e.AllFiles())
	}
	if e := f.Coverage.E2E.Web; e != nil {
		addFiles(e.AllFiles())
	}
	if e := f.Coverage.E2E.Mobile; e != nil {
		addFiles(e.AllFiles())
	}

	return files
}
