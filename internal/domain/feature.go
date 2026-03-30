package domain

import "fmt"

// Feature represents a single testable product feature with its coverage state.
type Feature struct {
	ID          string   `yaml:"id"`
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Roles       []string `yaml:"roles"`
	Priority    Priority `yaml:"priority"`
	Surfaces    Surfaces `yaml:"surfaces"`
	Coverage    Coverage `yaml:"coverage"`
	Notes       string   `yaml:"notes,omitempty"`
}

// Surfaces describes where a feature is exposed to users.
type Surfaces struct {
	Web    *WebSurface    `yaml:"web,omitempty"`
	Mobile *MobileSurface `yaml:"mobile,omitempty"`
	API    []APISurface   `yaml:"api,omitempty"`
}

// WebSurface describes the web route and component for a feature.
type WebSurface struct {
	Route     string `yaml:"route"`
	Component string `yaml:"component"`
}

// MobileSurface describes the mobile screen for a feature.
type MobileSurface struct {
	Screen string `yaml:"screen"`
}

// APISurface describes a single API endpoint for a feature.
type APISurface struct {
	Method string `yaml:"method"`
	Path   string `yaml:"path"`
}

// Coverage holds all test coverage information for a feature, organized by test type.
type Coverage struct {
	Unit        UnitCoverage        `yaml:"unit"`
	Integration IntegrationCoverage `yaml:"integration"`
	E2E         E2ECoverage         `yaml:"e2e"`
}

// UnitCoverage holds unit test coverage entries for each platform.
type UnitCoverage struct {
	Backend *CoverageEntry `yaml:"backend,omitempty"`
	Web     *CoverageEntry `yaml:"web,omitempty"`
	Mobile  *CoverageEntry `yaml:"mobile,omitempty"`
}

// IntegrationCoverage holds integration test coverage entries.
type IntegrationCoverage struct {
	Backend *CoverageEntry `yaml:"backend,omitempty"`
	Mobile  *CoverageEntry `yaml:"mobile,omitempty"`
}

// E2ECoverage holds end-to-end test coverage entries.
type E2ECoverage struct {
	Web    *E2ECoverageEntry `yaml:"web,omitempty"`
	Mobile *E2ECoverageEntry `yaml:"mobile,omitempty"`
}

// CoverageEntry represents a single coverage record for unit or integration tests.
type CoverageEntry struct {
	Status Status   `yaml:"status"`
	Files  []string `yaml:"files,omitempty"`
	Mocked bool     `yaml:"mocked"`
}

// E2ECoverageEntry represents a coverage record for end-to-end tests, including run history.
type E2ECoverageEntry struct {
	Status   Status   `yaml:"status"`
	Files    []string `yaml:"files,omitempty"`
	LastRun  string   `yaml:"last_run,omitempty"`
	PassRate float64  `yaml:"pass_rate,omitempty"`
}

// AllCoverageEntries returns a map of path -> status for every non-nil coverage slot.
// Paths use dotted notation like "unit.backend", "e2e.web".
func (f *Feature) AllCoverageEntries() map[string]Status {
	entries := make(map[string]Status)

	if f.Coverage.Unit.Backend != nil {
		entries["unit.backend"] = f.Coverage.Unit.Backend.Status
	}
	if f.Coverage.Unit.Web != nil {
		entries["unit.web"] = f.Coverage.Unit.Web.Status
	}
	if f.Coverage.Unit.Mobile != nil {
		entries["unit.mobile"] = f.Coverage.Unit.Mobile.Status
	}
	if f.Coverage.Integration.Backend != nil {
		entries["integration.backend"] = f.Coverage.Integration.Backend.Status
	}
	if f.Coverage.Integration.Mobile != nil {
		entries["integration.mobile"] = f.Coverage.Integration.Mobile.Status
	}
	if f.Coverage.E2E.Web != nil {
		entries["e2e.web"] = f.Coverage.E2E.Web.Status
	}
	if f.Coverage.E2E.Mobile != nil {
		entries["e2e.mobile"] = f.Coverage.E2E.Mobile.Status
	}

	return entries
}

// Gaps returns human-readable descriptions of missing or failing coverage.
func (f *Feature) Gaps() []string {
	var gaps []string

	checkEntry := func(label string, entry *CoverageEntry) {
		if entry == nil || entry.Status.IsMissing() {
			gaps = append(gaps, "Missing "+label+" tests")
		} else if entry.Status.IsFailing() {
			gaps = append(gaps, "Failing "+label+" tests")
		} else if entry.Status == StatusPartial {
			gaps = append(gaps, "Partial "+label+" coverage — additional tests needed")
		}
	}

	checkE2E := func(label string, entry *E2ECoverageEntry) {
		if entry == nil || entry.Status.IsMissing() {
			gaps = append(gaps, "Missing "+label+" tests")
		} else if entry.Status.IsFailing() {
			gaps = append(gaps, "Failing "+label+" tests (pass rate: "+formatPercent(entry.PassRate)+")")
		} else if entry.Status == StatusPartial {
			gaps = append(gaps, "Partial "+label+" coverage — additional scenarios needed")
		}
	}

	// Unit tests
	if f.Surfaces.API != nil || f.Coverage.Unit.Backend != nil {
		checkEntry("unit backend", f.Coverage.Unit.Backend)
	}
	if f.Surfaces.Web != nil || f.Coverage.Unit.Web != nil {
		checkEntry("unit web", f.Coverage.Unit.Web)
	}
	if f.Surfaces.Mobile != nil || f.Coverage.Unit.Mobile != nil {
		checkEntry("unit mobile", f.Coverage.Unit.Mobile)
	}

	// Integration tests
	if f.Surfaces.API != nil || f.Coverage.Integration.Backend != nil {
		checkEntry("integration backend", f.Coverage.Integration.Backend)
	}
	if f.Surfaces.Mobile != nil || f.Coverage.Integration.Mobile != nil {
		checkEntry("integration mobile", f.Coverage.Integration.Mobile)
	}

	// E2E tests
	if f.Surfaces.Web != nil || f.Coverage.E2E.Web != nil {
		checkE2E("E2E web", f.Coverage.E2E.Web)
	}
	if f.Surfaces.Mobile != nil || f.Coverage.E2E.Mobile != nil {
		checkE2E("E2E mobile", f.Coverage.E2E.Mobile)
	}

	return gaps
}

func formatPercent(rate float64) string {
	pct := int(rate * 100)
	return fmt.Sprintf("%d%%", pct)
}
