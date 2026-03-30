package app

import (
	"fmt"

	"github.com/sosalejandro/testreg/internal/domain"
	"github.com/sosalejandro/testreg/internal/ports"
)

// CheckResult holds the detailed status of a single feature.
type CheckResult struct {
	Feature      *domain.Feature
	DomainName   string
	Entries      map[string]EntryDetail
	Gaps         []string
	Suggestions  []string
	FullyCovered bool
}

// EntryDetail describes one coverage slot for a feature.
type EntryDetail struct {
	Status   domain.Status
	Files    []string
	Mocked   bool
	PassRate float64
	LastRun  string
}

// CheckFeatureUseCase retrieves detailed coverage information for a single feature.
type CheckFeatureUseCase struct {
	reader ports.RegistryReader
}

// NewCheckFeatureUseCase creates a new CheckFeatureUseCase.
func NewCheckFeatureUseCase(reader ports.RegistryReader) *CheckFeatureUseCase {
	return &CheckFeatureUseCase{reader: reader}
}

// Execute loads the feature by ID and computes detailed coverage analysis.
func (uc *CheckFeatureUseCase) Execute(registryDir, featureID string) (*CheckResult, error) {
	registry, err := uc.reader.LoadAll(registryDir)
	if err != nil {
		return nil, fmt.Errorf("loading registry from %s: %w", registryDir, err)
	}

	// Find the feature and its domain
	var foundFeature *domain.Feature
	var domainName string
	for _, d := range registry.Domains {
		for i := range d.Features {
			if d.Features[i].ID == featureID {
				foundFeature = &d.Features[i]
				domainName = d.Domain
				break
			}
		}
		if foundFeature != nil {
			break
		}
	}

	if foundFeature == nil {
		return nil, fmt.Errorf("feature %q not found in registry at %s", featureID, registryDir)
	}

	result := &CheckResult{
		Feature:    foundFeature,
		DomainName: domainName,
		Entries:    make(map[string]EntryDetail),
	}

	// Build entry details
	if e := foundFeature.Coverage.Unit.Backend; e != nil {
		result.Entries["unit.backend"] = EntryDetail{
			Status: e.Status, Files: e.Files, Mocked: e.Mocked,
		}
	}
	if e := foundFeature.Coverage.Unit.Web; e != nil {
		result.Entries["unit.web"] = EntryDetail{
			Status: e.Status, Files: e.Files, Mocked: e.Mocked,
		}
	}
	if e := foundFeature.Coverage.Unit.Mobile; e != nil {
		result.Entries["unit.mobile"] = EntryDetail{
			Status: e.Status, Files: e.Files, Mocked: e.Mocked,
		}
	}
	if e := foundFeature.Coverage.Integration.Backend; e != nil {
		result.Entries["integration.backend"] = EntryDetail{
			Status: e.Status, Files: e.Files, Mocked: e.Mocked,
		}
	}
	if e := foundFeature.Coverage.Integration.Mobile; e != nil {
		result.Entries["integration.mobile"] = EntryDetail{
			Status: e.Status, Files: e.Files, Mocked: e.Mocked,
		}
	}
	if e := foundFeature.Coverage.E2E.Web; e != nil {
		result.Entries["e2e.web"] = EntryDetail{
			Status: e.Status, Files: e.Files,
			PassRate: e.PassRate, LastRun: e.LastRun,
		}
	}
	if e := foundFeature.Coverage.E2E.Mobile; e != nil {
		result.Entries["e2e.mobile"] = EntryDetail{
			Status: e.Status, Files: e.Files,
			PassRate: e.PassRate, LastRun: e.LastRun,
		}
	}

	// Compute gaps
	result.Gaps = foundFeature.Gaps()

	// Generate suggestions
	result.Suggestions = generateSuggestions(foundFeature)

	// Check if fully covered
	result.FullyCovered = len(result.Gaps) == 0

	return result, nil
}

// generateSuggestions returns actionable recommendations for improving coverage.
func generateSuggestions(f *domain.Feature) []string {
	var suggestions []string

	// Unit test suggestions
	if f.Coverage.Unit.Backend == nil || f.Coverage.Unit.Backend.Status.IsMissing() {
		if len(f.Surfaces.API) > 0 {
			suggestions = append(suggestions, "Add Go unit tests for the service/handler layer")
		}
	}
	if f.Coverage.Unit.Web == nil || f.Coverage.Unit.Web.Status.IsMissing() {
		if f.Surfaces.Web != nil {
			suggestions = append(suggestions,
				fmt.Sprintf("Add Vitest test for %s component", f.Surfaces.Web.Component))
		}
	}
	if f.Coverage.Unit.Mobile == nil || f.Coverage.Unit.Mobile.Status.IsMissing() {
		if f.Surfaces.Mobile != nil {
			suggestions = append(suggestions,
				fmt.Sprintf("Add Jest test for %s screen", f.Surfaces.Mobile.Screen))
		}
	}

	// Integration test suggestions
	if f.Coverage.Integration.Backend == nil || f.Coverage.Integration.Backend.Status.IsMissing() {
		if len(f.Surfaces.API) > 0 {
			suggestions = append(suggestions,
				fmt.Sprintf("Add integration test for %s %s endpoint", f.Surfaces.API[0].Method, f.Surfaces.API[0].Path))
		}
	}

	// E2E test suggestions
	if f.Coverage.E2E.Web == nil || f.Coverage.E2E.Web.Status.IsMissing() {
		if f.Surfaces.Web != nil {
			suggestions = append(suggestions,
				fmt.Sprintf("Add Playwright spec for %s route", f.Surfaces.Web.Route))
		}
	}
	if f.Coverage.E2E.Mobile == nil || f.Coverage.E2E.Mobile.Status.IsMissing() {
		if f.Surfaces.Mobile != nil {
			suggestions = append(suggestions,
				fmt.Sprintf("Add Maestro flow for %s screen", f.Surfaces.Mobile.Screen))
		}
	}

	// Mocked test warnings
	if e := f.Coverage.Unit.Backend; e != nil && e.Mocked {
		suggestions = append(suggestions, "Consider adding real integration tests — backend unit tests use mocks")
	}

	// Failing test suggestions
	if e := f.Coverage.E2E.Web; e != nil && e.Status.IsFailing() {
		suggestions = append(suggestions,
			fmt.Sprintf("Fix failing web E2E tests (pass rate: %.0f%%)", e.PassRate*100))
	}
	if e := f.Coverage.E2E.Mobile; e != nil && e.Status.IsFailing() {
		suggestions = append(suggestions,
			fmt.Sprintf("Fix failing mobile E2E tests (pass rate: %.0f%%)", e.PassRate*100))
	}

	return suggestions
}
