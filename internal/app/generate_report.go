package app

import (
	"fmt"
	"time"

	"github.com/sosalejandro/atlas/internal/domain"
	"github.com/sosalejandro/atlas/internal/ports"
)

// GenerateReportUseCase builds a complete coverage report from registry state.
type GenerateReportUseCase struct {
	reader ports.RegistryReader
}

// NewGenerateReportUseCase creates a new GenerateReportUseCase.
func NewGenerateReportUseCase(reader ports.RegistryReader) *GenerateReportUseCase {
	return &GenerateReportUseCase{reader: reader}
}

// Execute loads the registry and builds a Report suitable for rendering.
func (uc *GenerateReportUseCase) Execute(registryDir, projectRoot string) (*domain.Report, error) {
	registry, err := uc.reader.LoadAll(registryDir)
	if err != nil {
		return nil, fmt.Errorf("loading registry from %s: %w", registryDir, err)
	}

	report := &domain.Report{
		GeneratedAt: time.Now().Format("2006-01-02 15:04:05"),
		ProjectRoot: projectRoot,
		Metrics:     registry.ComputeMetrics(),
	}

	for _, d := range registry.Domains {
		if d.Domain == "_unmapped" {
			continue
		}

		dr := domain.DomainReport{
			Name:        d.Domain,
			Description: d.Description,
		}

		for _, f := range d.Features {
			fr := domain.FeatureReport{
				ID:       f.ID,
				Name:     f.Name,
				Priority: f.Priority,
				Status:   f.AllCoverageEntries(),
				Gaps:     f.Gaps(),
			}
			dr.Features = append(dr.Features, fr)
		}

		report.Domains = append(report.Domains, dr)
	}

	return report, nil
}
