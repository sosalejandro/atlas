package ports

// GraphConfig holds configuration for the dependency graph scanner and tracer.
type GraphConfig struct {
	ProjectRoot     string
	BackendRoot     string
	RouterFile      string
	WireFile        string
	FxDir           string // Directory containing Uber Fx/Dig provider modules
	SQLCConfig      string
	FrontendRoots   []string
	IgnorePackages  []string
	IgnoreFunctions []string
	CacheDir          string
	MaxDepth          int
	Concurrency       int
	TypeChecking      bool
	GraphQLSchemaDirs []string
	LayerRules        LayerRules
}

// LayerRules holds custom directory name patterns for layer classification.
// These extend the built-in defaults (handler, resolver, service, repository,
// persistence, generated).
type LayerRules struct {
	Handler    []string
	Service    []string
	Repository []string
	Query      []string
}
