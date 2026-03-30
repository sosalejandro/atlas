package ports

import "github.com/sosalejandro/testreg/internal/domain"

// Reporter renders a coverage report to an output destination.
type Reporter interface {
	// Render outputs the given report. The output destination is implementation-specific
	// (stdout for terminal, file for markdown, etc.).
	Render(report *domain.Report) error
}
