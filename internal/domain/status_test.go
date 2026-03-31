// @testreg registry.domain-model
package domain

import "testing"

func TestStatusValidate(t *testing.T) {
	tests := []struct {
		name    string
		status  Status
		wantErr bool
	}{
		{name: "covered is valid", status: StatusCovered, wantErr: false},
		{name: "partial is valid", status: StatusPartial, wantErr: false},
		{name: "missing is valid", status: StatusMissing, wantErr: false},
		{name: "failing is valid", status: StatusFailing, wantErr: false},
		{name: "not-applicable is valid", status: StatusNotApplicable, wantErr: false},
		{name: "empty is invalid", status: Status(""), wantErr: true},
		{name: "unknown is invalid", status: Status("unknown"), wantErr: true},
		{name: "COVERED case-sensitive", status: Status("COVERED"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.status.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Status(%q).Validate() error = %v, wantErr %v", tt.status, err, tt.wantErr)
			}
		})
	}
}

func TestStatusPredicates(t *testing.T) {
	tests := []struct {
		status    Status
		isCovered bool
		isMissing bool
		isFailing bool
	}{
		{StatusCovered, true, false, false},
		{StatusMissing, false, true, false},
		{StatusFailing, false, false, true},
		{StatusPartial, false, false, false},
		{StatusNotApplicable, false, false, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := tt.status.IsCovered(); got != tt.isCovered {
				t.Errorf("IsCovered() = %v, want %v", got, tt.isCovered)
			}
			if got := tt.status.IsMissing(); got != tt.isMissing {
				t.Errorf("IsMissing() = %v, want %v", got, tt.isMissing)
			}
			if got := tt.status.IsFailing(); got != tt.isFailing {
				t.Errorf("IsFailing() = %v, want %v", got, tt.isFailing)
			}
		})
	}
}

func TestPriorityValidate(t *testing.T) {
	tests := []struct {
		name     string
		priority Priority
		wantErr  bool
	}{
		{name: "critical is valid", priority: PriorityCritical, wantErr: false},
		{name: "high is valid", priority: PriorityHigh, wantErr: false},
		{name: "medium is valid", priority: PriorityMedium, wantErr: false},
		{name: "low is valid", priority: PriorityLow, wantErr: false},
		{name: "empty is invalid", priority: Priority(""), wantErr: true},
		{name: "urgent is invalid", priority: Priority("urgent"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.priority.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Priority(%q).Validate() error = %v, wantErr %v", tt.priority, err, tt.wantErr)
			}
		})
	}
}
