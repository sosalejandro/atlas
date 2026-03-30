package adapters

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPlaywrightResultParserParse(t *testing.T) {
	tmpDir := t.TempDir()

	// Write a Playwright JSON result file
	jsonContent := `{
  "suites": [
    {
      "title": "Auth Tests",
      "file": "e2e/auth.spec.ts",
      "suites": [],
      "specs": [
        {
          "title": "should login successfully",
          "tests": [
            {
              "status": "expected",
              "duration": 1500,
              "results": [
                {
                  "status": "passed",
                  "duration": 1500
                }
              ]
            }
          ]
        },
        {
          "title": "should show error on bad password",
          "tests": [
            {
              "status": "unexpected",
              "duration": 3000,
              "results": [
                {
                  "status": "failed",
                  "duration": 3000,
                  "error": {
                    "message": "Expected element to be visible"
                  }
                }
              ]
            }
          ]
        }
      ]
    }
  ]
}`

	resultFile := filepath.Join(tmpDir, "results.json")
	if err := os.WriteFile(resultFile, []byte(jsonContent), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	parser := NewPlaywrightResultParser()

	if parser.Name() != "Playwright JSON Parser" {
		t.Errorf("Name() = %q, want %q", parser.Name(), "Playwright JSON Parser")
	}

	// Test parsing a file directly
	results, err := parser.Parse(resultFile)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("Parse() returned %d results, want 2", len(results))
	}

	// First test should pass
	if !results[0].Passed {
		t.Error("Expected first test to pass")
	}

	// Second test should fail
	if results[1].Passed {
		t.Error("Expected second test to fail")
	}

	if results[1].Error != "Expected element to be visible" {
		t.Errorf("Error message = %q, want %q", results[1].Error, "Expected element to be visible")
	}
}

func TestPlaywrightResultParserParseDirectory(t *testing.T) {
	tmpDir := t.TempDir()

	jsonContent := `{"suites": [{"title": "test", "file": "test.spec.ts", "suites": [], "specs": [{"title": "passes", "tests": [{"status": "expected", "duration": 100, "results": [{"status": "passed", "duration": 100}]}]}]}]}`

	if err := os.WriteFile(filepath.Join(tmpDir, "results.json"), []byte(jsonContent), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	parser := NewPlaywrightResultParser()
	results, err := parser.Parse(tmpDir)
	if err != nil {
		t.Fatalf("Parse(dir) error = %v", err)
	}

	if len(results) != 1 {
		t.Errorf("Parse(dir) returned %d results, want 1", len(results))
	}
}

func TestPlaywrightResultParserNoJsonFile(t *testing.T) {
	tmpDir := t.TempDir()

	parser := NewPlaywrightResultParser()
	_, err := parser.Parse(tmpDir)
	if err == nil {
		t.Fatal("Expected error when no JSON file found in directory")
	}
}

func TestInferFeatureFromPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"e2e/auth.spec.ts", "auth"},
		{"e2e/meals/log.spec.ts", "meals.log"},
		{"tests/e2e/auth/login.spec.ts", "auth.login"},
		{"specs/profile.spec.ts", "profile"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := inferFeatureFromPath(tt.path)
			if got != tt.want {
				t.Errorf("inferFeatureFromPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
