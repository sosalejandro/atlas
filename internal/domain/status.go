package domain

import "fmt"

// Status represents the test coverage status of a feature entry.
type Status string

const (
	StatusCovered       Status = "covered"
	StatusPartial       Status = "partial"
	StatusMissing       Status = "missing"
	StatusFailing       Status = "failing"
	StatusNotApplicable Status = "not-applicable"
)

// ValidStatuses is the set of all recognized status values.
var ValidStatuses = map[Status]bool{
	StatusCovered:       true,
	StatusPartial:       true,
	StatusMissing:       true,
	StatusFailing:       true,
	StatusNotApplicable: true,
}

// Validate returns an error if the status is not a recognized value.
func (s Status) Validate() error {
	if !ValidStatuses[s] {
		return fmt.Errorf("invalid status %q: must be one of covered, partial, missing, failing, not-applicable", string(s))
	}
	return nil
}

// IsCovered returns true if the status indicates full coverage.
func (s Status) IsCovered() bool {
	return s == StatusCovered
}

// IsMissing returns true if the status indicates no coverage.
func (s Status) IsMissing() bool {
	return s == StatusMissing
}

// IsFailing returns true if the status indicates failing tests.
func (s Status) IsFailing() bool {
	return s == StatusFailing
}

// Priority represents the importance level of a feature.
type Priority string

const (
	PriorityCritical Priority = "critical"
	PriorityHigh     Priority = "high"
	PriorityMedium   Priority = "medium"
	PriorityLow      Priority = "low"
)

// ValidPriorities is the set of all recognized priority values.
var ValidPriorities = map[Priority]bool{
	PriorityCritical: true,
	PriorityHigh:     true,
	PriorityMedium:   true,
	PriorityLow:      true,
}

// Validate returns an error if the priority is not a recognized value.
func (p Priority) Validate() error {
	if !ValidPriorities[p] {
		return fmt.Errorf("invalid priority %q: must be one of critical, high, medium, low", string(p))
	}
	return nil
}
