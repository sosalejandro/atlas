// @testreg registry.domain-model
package domain

import (
	"testing"
)

func TestFeatureAllCoverageEntries(t *testing.T) {
	tests := []struct {
		name     string
		feature  Feature
		wantKeys []string
	}{
		{
			name: "all entries present",
			feature: Feature{
				Coverage: Coverage{
					Unit: UnitCoverage{
						Backend: &CoverageEntry{Status: StatusCovered},
						Web:     &CoverageEntry{Status: StatusMissing},
						Mobile:  &CoverageEntry{Status: StatusPartial},
					},
					Integration: IntegrationCoverage{
						Backend: &CoverageEntry{Status: StatusCovered},
						Mobile:  &CoverageEntry{Status: StatusFailing},
					},
					E2E: E2ECoverage{
						Web:    &E2ECoverageEntry{Status: StatusCovered},
						Mobile: &E2ECoverageEntry{Status: StatusMissing},
					},
				},
			},
			wantKeys: []string{"unit.backend", "unit.web", "unit.mobile", "integration.backend", "integration.mobile", "e2e.web", "e2e.mobile"},
		},
		{
			name: "only backend entries",
			feature: Feature{
				Coverage: Coverage{
					Unit: UnitCoverage{
						Backend: &CoverageEntry{Status: StatusCovered},
					},
					Integration: IntegrationCoverage{
						Backend: &CoverageEntry{Status: StatusMissing},
					},
				},
			},
			wantKeys: []string{"unit.backend", "integration.backend"},
		},
		{
			name:     "no entries",
			feature:  Feature{},
			wantKeys: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := tt.feature.AllCoverageEntries()
			if len(entries) != len(tt.wantKeys) {
				t.Errorf("AllCoverageEntries() returned %d entries, want %d", len(entries), len(tt.wantKeys))
			}
			for _, key := range tt.wantKeys {
				if _, ok := entries[key]; !ok {
					t.Errorf("AllCoverageEntries() missing key %q", key)
				}
			}
		})
	}
}

func TestFeatureGaps(t *testing.T) {
	tests := []struct {
		name     string
		feature  Feature
		wantGaps int
	}{
		{
			name: "fully covered - no gaps",
			feature: Feature{
				Surfaces: Surfaces{
					Web:    &WebSurface{Route: "/test", Component: "TestPage"},
					Mobile: &MobileSurface{Screen: "TestScreen"},
					API:    []APISurface{{Method: "GET", Path: "/api/test"}},
				},
				Coverage: Coverage{
					Unit: UnitCoverage{
						Backend: &CoverageEntry{Status: StatusCovered},
						Web:     &CoverageEntry{Status: StatusCovered},
						Mobile:  &CoverageEntry{Status: StatusCovered},
					},
					Integration: IntegrationCoverage{
						Backend: &CoverageEntry{Status: StatusCovered},
						Mobile:  &CoverageEntry{Status: StatusCovered},
					},
					E2E: E2ECoverage{
						Web:    &E2ECoverageEntry{Status: StatusCovered},
						Mobile: &E2ECoverageEntry{Status: StatusCovered},
					},
				},
			},
			wantGaps: 0,
		},
		{
			name: "all missing",
			feature: Feature{
				Surfaces: Surfaces{
					Web:    &WebSurface{Route: "/test", Component: "TestPage"},
					Mobile: &MobileSurface{Screen: "TestScreen"},
					API:    []APISurface{{Method: "GET", Path: "/api/test"}},
				},
				Coverage: Coverage{
					Unit: UnitCoverage{
						Backend: &CoverageEntry{Status: StatusMissing},
						Web:     &CoverageEntry{Status: StatusMissing},
						Mobile:  &CoverageEntry{Status: StatusMissing},
					},
					Integration: IntegrationCoverage{
						Backend: &CoverageEntry{Status: StatusMissing},
						Mobile:  &CoverageEntry{Status: StatusMissing},
					},
					E2E: E2ECoverage{
						Web:    &E2ECoverageEntry{Status: StatusMissing},
						Mobile: &E2ECoverageEntry{Status: StatusMissing},
					},
				},
			},
			wantGaps: 7,
		},
		{
			name: "nil entries for surfaces that exist",
			feature: Feature{
				Surfaces: Surfaces{
					Web: &WebSurface{Route: "/test", Component: "TestPage"},
					API: []APISurface{{Method: "GET", Path: "/api/test"}},
				},
				Coverage: Coverage{},
			},
			wantGaps: 4, // unit backend, unit web, integration backend, e2e web
		},
		{
			name: "failing E2E includes pass rate in gap description",
			feature: Feature{
				Surfaces: Surfaces{
					Web: &WebSurface{Route: "/test", Component: "TestPage"},
				},
				Coverage: Coverage{
					E2E: E2ECoverage{
						Web: &E2ECoverageEntry{Status: StatusFailing, PassRate: 0.75},
					},
				},
			},
			wantGaps: 2, // missing unit web + failing e2e web
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gaps := tt.feature.Gaps()
			if len(gaps) != tt.wantGaps {
				t.Errorf("Gaps() returned %d gaps, want %d", len(gaps), tt.wantGaps)
				for i, g := range gaps {
					t.Logf("  gap[%d]: %s", i, g)
				}
			}
		})
	}
}
