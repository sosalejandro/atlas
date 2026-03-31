// @testreg registry.domain-model
package domain

import "testing"

func makeTestRegistry() *Registry {
	return &Registry{
		Domains: []DomainFile{
			{
				Domain:      "auth",
				Description: "Auth features",
				Features: []Feature{
					{
						ID:       "auth.login",
						Name:     "Login",
						Priority: PriorityCritical,
						Coverage: Coverage{
							Unit: UnitCoverage{
								Backend: &CoverageEntry{Status: StatusCovered},
								Web:     &CoverageEntry{Status: StatusCovered},
							},
							Integration: IntegrationCoverage{
								Backend: &CoverageEntry{Status: StatusCovered},
							},
							E2E: E2ECoverage{
								Web: &E2ECoverageEntry{Status: StatusCovered, PassRate: 1.0},
							},
						},
					},
					{
						ID:       "auth.register",
						Name:     "Register",
						Priority: PriorityCritical,
						Coverage: Coverage{
							Unit: UnitCoverage{
								Backend: &CoverageEntry{Status: StatusMissing},
							},
							E2E: E2ECoverage{
								Web: &E2ECoverageEntry{Status: StatusMissing},
							},
						},
					},
				},
			},
			{
				Domain:      "meals",
				Description: "Meal features",
				Features: []Feature{
					{
						ID:       "meals.log",
						Name:     "Log Meal",
						Priority: PriorityHigh,
						Coverage: Coverage{
							Unit: UnitCoverage{
								Backend: &CoverageEntry{Status: StatusPartial},
							},
							E2E: E2ECoverage{
								Web: &E2ECoverageEntry{Status: StatusFailing, PassRate: 0.5},
							},
						},
					},
				},
			},
		},
	}
}

func TestRegistryGetFeature(t *testing.T) {
	reg := makeTestRegistry()

	tests := []struct {
		name      string
		featureID string
		wantName  string
		wantErr   bool
	}{
		{name: "existing feature", featureID: "auth.login", wantName: "Login", wantErr: false},
		{name: "another existing feature", featureID: "meals.log", wantName: "Log Meal", wantErr: false},
		{name: "non-existent feature", featureID: "nonexistent.feature", wantErr: true},
		{name: "empty ID", featureID: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := reg.GetFeature(tt.featureID)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetFeature(%q) error = %v, wantErr %v", tt.featureID, err, tt.wantErr)
				return
			}
			if err == nil && f.Name != tt.wantName {
				t.Errorf("GetFeature(%q).Name = %q, want %q", tt.featureID, f.Name, tt.wantName)
			}
		})
	}
}

func TestRegistryGetDomain(t *testing.T) {
	reg := makeTestRegistry()

	tests := []struct {
		name       string
		domainName string
		wantErr    bool
	}{
		{name: "existing domain", domainName: "auth", wantErr: false},
		{name: "another domain", domainName: "meals", wantErr: false},
		{name: "non-existent domain", domainName: "nonexistent", wantErr: true},
		{name: "empty name", domainName: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := reg.GetDomain(tt.domainName)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetDomain(%q) error = %v, wantErr %v", tt.domainName, err, tt.wantErr)
				return
			}
			if err == nil && d.Domain != tt.domainName {
				t.Errorf("GetDomain(%q).Domain = %q, want %q", tt.domainName, d.Domain, tt.domainName)
			}
		})
	}
}

func TestRegistryAllFeatures(t *testing.T) {
	reg := makeTestRegistry()
	all := reg.AllFeatures()

	if len(all) != 3 {
		t.Errorf("AllFeatures() returned %d features, want 3", len(all))
	}
}

func TestRegistryAllFeaturesEmpty(t *testing.T) {
	reg := &Registry{}
	all := reg.AllFeatures()

	if len(all) != 0 {
		t.Errorf("AllFeatures() on empty registry returned %d features, want 0", len(all))
	}
}

func TestRegistryComputeMetrics(t *testing.T) {
	reg := makeTestRegistry()
	m := reg.ComputeMetrics()

	if m.TotalFeatures != 3 {
		t.Errorf("TotalFeatures = %d, want 3", m.TotalFeatures)
	}

	if m.CoveredUnit != 1 {
		t.Errorf("CoveredUnit = %d, want 1", m.CoveredUnit)
	}

	if m.CoveredIntegration != 1 {
		t.Errorf("CoveredIntegration = %d, want 1", m.CoveredIntegration)
	}

	if m.CoveredE2E != 1 {
		t.Errorf("CoveredE2E = %d, want 1", m.CoveredE2E)
	}

	if m.MissingE2E != 1 {
		t.Errorf("MissingE2E = %d, want 1", m.MissingE2E)
	}

	if m.FailingE2E != 1 {
		t.Errorf("FailingE2E = %d, want 1", m.FailingE2E)
	}

	// Check priority breakdown
	critMetrics, ok := m.ByPriority[PriorityCritical]
	if !ok {
		t.Fatal("missing critical priority metrics")
	}
	if critMetrics.Total != 2 {
		t.Errorf("critical Total = %d, want 2", critMetrics.Total)
	}

	// Check domain breakdown
	authMetrics, ok := m.ByDomain["auth"]
	if !ok {
		t.Fatal("missing auth domain metrics")
	}
	if authMetrics.TotalFeatures != 2 {
		t.Errorf("auth TotalFeatures = %d, want 2", authMetrics.TotalFeatures)
	}
}

func TestRegistryComputeMetricsEmpty(t *testing.T) {
	reg := &Registry{}
	m := reg.ComputeMetrics()

	if m.TotalFeatures != 0 {
		t.Errorf("TotalFeatures = %d, want 0", m.TotalFeatures)
	}
}
