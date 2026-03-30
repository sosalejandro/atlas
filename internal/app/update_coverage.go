package app

import (
	"fmt"
	"time"

	"github.com/sosalejandro/testreg/internal/domain"
	"github.com/sosalejandro/testreg/internal/ports"
)

// UpdateCoverageUseCase merges test results into the registry.
type UpdateCoverageUseCase struct {
	reader ports.RegistryReader
	writer ports.RegistryWriter
}

// UpdateResult summarizes changes made by the update operation.
type UpdateResult struct {
	Processed int
	Updated   int
	Unmapped  int
	Failures  int
	Details   []UpdateDetail
}

// UpdateDetail describes one change made during the update.
type UpdateDetail struct {
	FeatureID string
	EntryPath string // e.g., "e2e.web"
	OldStatus domain.Status
	NewStatus domain.Status
	PassRate  float64
}

// NewUpdateCoverageUseCase creates a new UpdateCoverageUseCase.
func NewUpdateCoverageUseCase(reader ports.RegistryReader, writer ports.RegistryWriter) *UpdateCoverageUseCase {
	return &UpdateCoverageUseCase{reader: reader, writer: writer}
}

// Execute parses test results and merges them into the registry.
func (uc *UpdateCoverageUseCase) Execute(registryDir string, results []ports.TestResult, platform, testType string) (*UpdateResult, error) {
	registry, err := uc.reader.LoadAll(registryDir)
	if err != nil {
		return nil, fmt.Errorf("loading registry from %s: %w", registryDir, err)
	}

	ur := &UpdateResult{Processed: len(results)}
	today := time.Now().Format("2006-01-02")

	// Group results by feature
	byFeature := make(map[string][]ports.TestResult)
	for _, r := range results {
		if r.FeatureID == "" {
			ur.Unmapped++
			continue
		}
		byFeature[r.FeatureID] = append(byFeature[r.FeatureID], r)
	}

	// Apply results to each feature
	for featureID, featureResults := range byFeature {
		feature, findErr := registry.GetFeature(featureID)
		if findErr != nil {
			ur.Unmapped += len(featureResults)
			continue
		}

		// Calculate aggregate pass rate for this feature
		totalTests := len(featureResults)
		passedTests := 0
		var files []string
		for _, r := range featureResults {
			if r.Passed {
				passedTests++
			} else {
				ur.Failures++
			}
			files = append(files, r.FilePath)
		}
		passRate := float64(passedTests) / float64(totalTests)

		// Determine new status based on pass rate
		var newStatus domain.Status
		switch {
		case passRate >= 1.0:
			newStatus = domain.StatusCovered
		case passRate > 0:
			newStatus = domain.StatusFailing
		default:
			newStatus = domain.StatusFailing
		}

		// Apply to the correct coverage slot
		detail := applyResults(feature, platform, testType, newStatus, passRate, today, files)
		if detail != nil {
			ur.Updated++
			ur.Details = append(ur.Details, *detail)
		}
	}

	// Persist changes
	if err := uc.writer.SaveAll(registryDir, registry); err != nil {
		return nil, fmt.Errorf("saving updated registry: %w", err)
	}

	return ur, nil
}

// applyResults updates the specific coverage entry on a feature and returns the change detail.
func applyResults(f *domain.Feature, platform, testType string, newStatus domain.Status, passRate float64, date string, files []string) *UpdateDetail {
	entryPath := testType + "." + platform

	switch {
	case testType == "unit" && platform == "backend":
		return applyToCoverageEntry(&f.Coverage.Unit.Backend, entryPath, newStatus, files)
	case testType == "unit" && platform == "web":
		return applyToCoverageEntry(&f.Coverage.Unit.Web, entryPath, newStatus, files)
	case testType == "unit" && platform == "mobile":
		return applyToCoverageEntry(&f.Coverage.Unit.Mobile, entryPath, newStatus, files)
	case testType == "integration" && platform == "backend":
		return applyToCoverageEntry(&f.Coverage.Integration.Backend, entryPath, newStatus, files)
	case testType == "integration" && platform == "mobile":
		return applyToCoverageEntry(&f.Coverage.Integration.Mobile, entryPath, newStatus, files)
	case testType == "e2e" && platform == "web":
		return applyToE2EEntry(&f.Coverage.E2E.Web, entryPath, newStatus, passRate, date, files)
	case testType == "e2e" && platform == "mobile":
		return applyToE2EEntry(&f.Coverage.E2E.Mobile, entryPath, newStatus, passRate, date, files)
	}

	return nil
}

func applyToCoverageEntry(entry **domain.CoverageEntry, entryPath string, newStatus domain.Status, files []string) *UpdateDetail {
	var oldStatus domain.Status
	if *entry == nil {
		*entry = &domain.CoverageEntry{Status: domain.StatusMissing}
		oldStatus = domain.StatusMissing
	} else {
		oldStatus = (*entry).Status
	}

	(*entry).Status = newStatus
	(*entry).Files = deduplicateFiles(append((*entry).Files, files...))

	if oldStatus == newStatus {
		return nil
	}

	return &UpdateDetail{
		EntryPath: entryPath,
		OldStatus: oldStatus,
		NewStatus: newStatus,
	}
}

func applyToE2EEntry(entry **domain.E2ECoverageEntry, entryPath string, newStatus domain.Status, passRate float64, date string, files []string) *UpdateDetail {
	var oldStatus domain.Status
	if *entry == nil {
		*entry = &domain.E2ECoverageEntry{Status: domain.StatusMissing}
		oldStatus = domain.StatusMissing
	} else {
		oldStatus = (*entry).Status
	}

	(*entry).Status = newStatus
	(*entry).PassRate = passRate
	(*entry).LastRun = date
	(*entry).Files = deduplicateFiles(append((*entry).Files, files...))

	if oldStatus == newStatus {
		return nil
	}

	return &UpdateDetail{
		EntryPath: entryPath,
		OldStatus: oldStatus,
		NewStatus: newStatus,
		PassRate:  passRate,
	}
}

func deduplicateFiles(files []string) []string {
	seen := make(map[string]bool, len(files))
	result := make([]string, 0, len(files))
	for _, f := range files {
		if !seen[f] {
			seen[f] = true
			result = append(result, f)
		}
	}
	return result
}
