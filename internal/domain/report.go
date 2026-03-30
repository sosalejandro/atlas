package domain

// Report holds the full coverage report data, ready for rendering.
type Report struct {
	GeneratedAt string
	ProjectRoot string
	Metrics     Metrics
	Domains     []DomainReport
}

// DomainReport holds report data for a single domain.
type DomainReport struct {
	Name        string
	Description string
	Features    []FeatureReport
}

// FeatureReport holds report data for a single feature.
type FeatureReport struct {
	ID       string
	Name     string
	Priority Priority
	Status   map[string]Status // "unit.backend" -> "covered", "e2e.web" -> "missing"
	Gaps     []string          // human-readable gap descriptions
}
