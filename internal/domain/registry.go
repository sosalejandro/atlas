package domain

import "fmt"

// DomainFile represents a single YAML file containing features for one domain.
type DomainFile struct {
	Domain      string    `yaml:"domain"`
	Description string    `yaml:"description"`
	Features    []Feature `yaml:"features"`
}

// Registry is the aggregate root for all domain files in the registry.
type Registry struct {
	Domains []DomainFile
}

// GetFeature searches all domains for a feature with the given ID.
// Returns nil and an error if not found.
func (r *Registry) GetFeature(id string) (*Feature, error) {
	for i := range r.Domains {
		for j := range r.Domains[i].Features {
			if r.Domains[i].Features[j].ID == id {
				return &r.Domains[i].Features[j], nil
			}
		}
	}
	return nil, fmt.Errorf("feature %q not found in registry", id)
}

// GetDomain searches for a domain file by name.
// Returns nil and an error if not found.
func (r *Registry) GetDomain(name string) (*DomainFile, error) {
	for i := range r.Domains {
		if r.Domains[i].Domain == name {
			return &r.Domains[i], nil
		}
	}
	return nil, fmt.Errorf("domain %q not found in registry", name)
}

// AllFeatures returns a flat slice of all features across all domains.
func (r *Registry) AllFeatures() []Feature {
	var all []Feature
	for _, d := range r.Domains {
		all = append(all, d.Features...)
	}
	return all
}

// ComputeMetrics calculates aggregate coverage metrics across the entire registry.
func (r *Registry) ComputeMetrics() Metrics {
	m := Metrics{
		ByPriority: make(map[Priority]PriorityMetrics),
		ByDomain:   make(map[string]DomainMetrics),
	}

	for _, d := range r.Domains {
		dm := DomainMetrics{TotalFeatures: len(d.Features)}

		for _, f := range d.Features {
			m.TotalFeatures++

			// Unit coverage counts
			unitCovered := countCoveredEntries(f.Coverage.Unit.Backend, f.Coverage.Unit.Web, f.Coverage.Unit.Mobile)
			unitMissing := countMissingEntries(f.Coverage.Unit.Backend, f.Coverage.Unit.Web, f.Coverage.Unit.Mobile)
			if unitCovered > 0 {
				m.CoveredUnit++
				dm.CoveredUnit++
			}
			if unitMissing > 0 {
				m.MissingUnit++
				dm.MissingUnit++
			}

			// Integration coverage counts
			integCovered := countCoveredEntries(f.Coverage.Integration.Backend, f.Coverage.Integration.Mobile)
			if integCovered > 0 {
				m.CoveredIntegration++
				dm.CoveredIntegration++
			}

			// E2E coverage counts
			e2eCovered := countCoveredE2EEntries(f.Coverage.E2E.Web, f.Coverage.E2E.Mobile)
			e2eMissing := countMissingE2EEntries(f.Coverage.E2E.Web, f.Coverage.E2E.Mobile)
			e2eFailing := countFailingE2EEntries(f.Coverage.E2E.Web, f.Coverage.E2E.Mobile)
			if e2eCovered > 0 {
				m.CoveredE2E++
				dm.CoveredE2E++
			}
			if e2eMissing > 0 {
				m.MissingE2E++
				dm.MissingE2E++
			}
			if e2eFailing > 0 {
				m.FailingE2E++
				dm.FailingE2E++
			}

			// Priority breakdown
			pm := m.ByPriority[f.Priority]
			pm.Total++
			if unitCovered > 0 {
				pm.CoveredUnit++
			}
			if integCovered > 0 {
				pm.CoveredIntegration++
			}
			if e2eCovered > 0 {
				pm.CoveredE2E++
			}
			if e2eMissing > 0 {
				pm.MissingE2E++
			}
			m.ByPriority[f.Priority] = pm
		}

		m.ByDomain[d.Domain] = dm
	}

	return m
}

func countCoveredEntries(entries ...*CoverageEntry) int {
	count := 0
	for _, e := range entries {
		if e != nil && e.Status.IsCovered() {
			count++
		}
	}
	return count
}

func countMissingEntries(entries ...*CoverageEntry) int {
	count := 0
	for _, e := range entries {
		if e != nil && e.Status.IsMissing() {
			count++
		}
	}
	return count
}

func countCoveredE2EEntries(entries ...*E2ECoverageEntry) int {
	count := 0
	for _, e := range entries {
		if e != nil && e.Status.IsCovered() {
			count++
		}
	}
	return count
}

func countMissingE2EEntries(entries ...*E2ECoverageEntry) int {
	count := 0
	for _, e := range entries {
		if e != nil && e.Status.IsMissing() {
			count++
		}
	}
	return count
}

func countFailingE2EEntries(entries ...*E2ECoverageEntry) int {
	count := 0
	for _, e := range entries {
		if e != nil && e.Status.IsFailing() {
			count++
		}
	}
	return count
}

// Metrics holds aggregate coverage statistics for the entire registry.
type Metrics struct {
	TotalFeatures      int
	CoveredUnit        int
	CoveredIntegration int
	CoveredE2E         int
	MissingUnit        int
	MissingE2E         int
	FailingE2E         int
	ByPriority         map[Priority]PriorityMetrics
	ByDomain           map[string]DomainMetrics
}

// PriorityMetrics holds coverage statistics for features of a specific priority level.
type PriorityMetrics struct {
	Total              int
	CoveredUnit        int
	CoveredIntegration int
	CoveredE2E         int
	MissingE2E         int
}

// DomainMetrics holds coverage statistics for features within a specific domain.
type DomainMetrics struct {
	TotalFeatures      int
	CoveredUnit        int
	CoveredIntegration int
	CoveredE2E         int
	MissingUnit        int
	MissingE2E         int
	FailingE2E         int
}
