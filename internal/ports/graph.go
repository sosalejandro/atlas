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
	CacheDir        string
	MaxDepth        int
}
