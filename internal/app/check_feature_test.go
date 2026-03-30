package app

import (
	"path/filepath"
	"testing"

	"github.com/sosalejandro/testreg/internal/adapters"
	"github.com/sosalejandro/testreg/internal/domain"
)

func TestCheckFeatureExecute(t *testing.T) {
	tmpDir := t.TempDir()
	registryDir := filepath.Join(tmpDir, "registry")

	store := adapters.NewYAMLStore()
	initUC := NewInitRegistryUseCase(store, store)
	if err := initUC.Execute(registryDir); err != nil {
		t.Fatalf("init error = %v", err)
	}

	uc := NewCheckFeatureUseCase(store)

	tests := []struct {
		name            string
		featureID       string
		wantErr         bool
		wantGaps        bool
		wantSuggestions bool
	}{
		{
			name:            "existing feature with missing coverage",
			featureID:       "auth.login",
			wantErr:         false,
			wantGaps:        true,
			wantSuggestions: true,
		},
		{
			name:      "non-existent feature",
			featureID: "nonexistent.feature",
			wantErr:   true,
		},
		{
			name:      "empty feature ID",
			featureID: "",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := uc.Execute(registryDir, tt.featureID)
			if (err != nil) != tt.wantErr {
				t.Errorf("Execute(%q) error = %v, wantErr %v", tt.featureID, err, tt.wantErr)
				return
			}

			if err != nil {
				return
			}

			if result.Feature == nil {
				t.Fatal("Expected non-nil Feature")
			}

			if result.Feature.ID != tt.featureID {
				t.Errorf("Feature.ID = %q, want %q", result.Feature.ID, tt.featureID)
			}

			if tt.wantGaps && len(result.Gaps) == 0 {
				t.Error("Expected non-empty Gaps")
			}

			if tt.wantSuggestions && len(result.Suggestions) == 0 {
				t.Error("Expected non-empty Suggestions")
			}
		})
	}
}

func TestCheckFeatureEntries(t *testing.T) {
	tmpDir := t.TempDir()
	registryDir := filepath.Join(tmpDir, "registry")

	store := adapters.NewYAMLStore()
	initUC := NewInitRegistryUseCase(store, store)
	if err := initUC.Execute(registryDir); err != nil {
		t.Fatalf("init error = %v", err)
	}

	uc := NewCheckFeatureUseCase(store)
	result, err := uc.Execute(registryDir, "auth.login")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// auth.login should have entries for unit.backend, unit.web, unit.mobile,
	// integration.backend, e2e.web, e2e.mobile
	expectedEntries := []string{"unit.backend", "unit.web", "unit.mobile", "integration.backend", "e2e.web", "e2e.mobile"}
	for _, key := range expectedEntries {
		entry, ok := result.Entries[key]
		if !ok {
			t.Errorf("Expected entry for %q", key)
			continue
		}
		if entry.Status != domain.StatusMissing {
			t.Errorf("Entry %q status = %q, want %q (initial template should be missing)",
				key, entry.Status, domain.StatusMissing)
		}
	}

	// Feature should not be fully covered since all entries are missing
	if result.FullyCovered {
		t.Error("Expected FullyCovered to be false for feature with all missing entries")
	}
}
