package app

import (
	"fmt"

	"github.com/sosalejandro/atlas/internal/domain"
	"github.com/sosalejandro/atlas/internal/ports"
)

// StatusFilter specifies how to filter the status output.
type StatusFilter struct {
	Domain   string          // filter by domain name, empty = all
	Priority domain.Priority // filter by priority, empty = all
	Status   domain.Status   // filter by status, empty = all
}

// StatusResult holds the computed status data for rendering.
type StatusResult struct {
	Metrics    domain.Metrics
	Features   []domain.Feature
	DomainData []DomainStatusRow
}

// DomainStatusRow holds summary data for one row in the status table.
type DomainStatusRow struct {
	Domain             string
	Total              int
	UnitCovered        int
	IntegrationCovered int
	E2ECovered         int
}

// GetStatusUseCase computes coverage metrics from the registry.
type GetStatusUseCase struct {
	reader ports.RegistryReader
}

// NewGetStatusUseCase creates a new GetStatusUseCase.
func NewGetStatusUseCase(reader ports.RegistryReader) *GetStatusUseCase {
	return &GetStatusUseCase{reader: reader}
}

// Execute loads the registry, applies filters, and computes metrics.
func (uc *GetStatusUseCase) Execute(registryDir string, filter StatusFilter) (*StatusResult, error) {
	registry, err := uc.reader.LoadAll(registryDir)
	if err != nil {
		return nil, fmt.Errorf("loading registry from %s: %w", registryDir, err)
	}

	result := &StatusResult{}

	// Compute full metrics first
	result.Metrics = registry.ComputeMetrics()

	// Build domain rows
	for _, d := range registry.Domains {
		if d.Domain == "_unmapped" {
			continue
		}
		if filter.Domain != "" && d.Domain != filter.Domain {
			continue
		}

		row := DomainStatusRow{
			Domain: d.Domain,
			Total:  len(d.Features),
		}

		for _, f := range d.Features {
			if filter.Priority != "" && f.Priority != filter.Priority {
				continue
			}
			if filter.Status != "" && !featureHasStatus(f, filter.Status) {
				continue
			}

			result.Features = append(result.Features, f)

			if hasAnyCoveredUnit(f) {
				row.UnitCovered++
			}
			if hasAnyCoveredIntegration(f) {
				row.IntegrationCovered++
			}
			if hasAnyCoveredE2E(f) {
				row.E2ECovered++
			}
		}

		if filter.Priority != "" || filter.Status != "" {
			row.Total = len(result.Features)
		}

		result.DomainData = append(result.DomainData, row)
	}

	return result, nil
}

func featureHasStatus(f domain.Feature, status domain.Status) bool {
	entries := f.AllCoverageEntries()
	for _, s := range entries {
		if s == status {
			return true
		}
	}
	return false
}

func hasAnyCoveredUnit(f domain.Feature) bool {
	return (f.Coverage.Unit.Backend != nil && f.Coverage.Unit.Backend.Status.IsCovered()) ||
		(f.Coverage.Unit.Web != nil && f.Coverage.Unit.Web.Status.IsCovered()) ||
		(f.Coverage.Unit.Mobile != nil && f.Coverage.Unit.Mobile.Status.IsCovered())
}

func hasAnyCoveredIntegration(f domain.Feature) bool {
	return (f.Coverage.Integration.Backend != nil && f.Coverage.Integration.Backend.Status.IsCovered()) ||
		(f.Coverage.Integration.Mobile != nil && f.Coverage.Integration.Mobile.Status.IsCovered())
}

func hasAnyCoveredE2E(f domain.Feature) bool {
	return (f.Coverage.E2E.Web != nil && f.Coverage.E2E.Web.Status.IsCovered()) ||
		(f.Coverage.E2E.Mobile != nil && f.Coverage.E2E.Mobile.Status.IsCovered())
}
