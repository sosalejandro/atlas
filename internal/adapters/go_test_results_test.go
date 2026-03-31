// @testreg workflow.update
package adapters

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGoTestResultParserParse(t *testing.T) {
	tmpDir := t.TempDir()

	// Write go test -json output
	jsonLines := `{"Time":"2026-03-30T10:00:00Z","Action":"run","Package":"github.com/user/project/src/services","Test":"TestLogin"}
{"Time":"2026-03-30T10:00:01Z","Action":"output","Package":"github.com/user/project/src/services","Test":"TestLogin","Output":"--- PASS: TestLogin (0.50s)\n"}
{"Time":"2026-03-30T10:00:01Z","Action":"pass","Package":"github.com/user/project/src/services","Test":"TestLogin","Elapsed":0.5}
{"Time":"2026-03-30T10:00:01Z","Action":"run","Package":"github.com/user/project/src/services","Test":"TestRegister"}
{"Time":"2026-03-30T10:00:02Z","Action":"output","Package":"github.com/user/project/src/services","Test":"TestRegister","Output":"FAIL: Expected 200, got 401\n"}
{"Time":"2026-03-30T10:00:02Z","Action":"fail","Package":"github.com/user/project/src/services","Test":"TestRegister","Elapsed":1.0}
{"Time":"2026-03-30T10:00:03Z","Action":"run","Package":"github.com/user/project/src/services","Test":"TestLogout"}
{"Time":"2026-03-30T10:00:03Z","Action":"skip","Package":"github.com/user/project/src/services","Test":"TestLogout"}
`

	resultFile := filepath.Join(tmpDir, "test-output.json")
	if err := os.WriteFile(resultFile, []byte(jsonLines), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	parser := NewGoTestResultParser()

	if parser.Name() != "Go Test JSON Parser" {
		t.Errorf("Name() = %q, want %q", parser.Name(), "Go Test JSON Parser")
	}

	results, err := parser.Parse(resultFile)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	// Should have 2 results (TestLogin passed, TestRegister failed, TestLogout skipped)
	if len(results) != 2 {
		t.Fatalf("Parse() returned %d results, want 2", len(results))
	}

	// Check that we got a pass and a fail
	passCount := 0
	failCount := 0
	for _, r := range results {
		if r.Passed {
			passCount++
		} else {
			failCount++
		}
	}

	if passCount != 1 {
		t.Errorf("Expected 1 passed test, got %d", passCount)
	}

	if failCount != 1 {
		t.Errorf("Expected 1 failed test, got %d", failCount)
	}
}

func TestGoTestResultParserEmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	resultFile := filepath.Join(tmpDir, "empty.json")

	if err := os.WriteFile(resultFile, []byte(""), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	parser := NewGoTestResultParser()
	results, err := parser.Parse(resultFile)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if len(results) != 0 {
		t.Errorf("Expected 0 results for empty file, got %d", len(results))
	}
}

func TestSplitCamelToLower(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"TestLogin", "login"},
		{"TestRegisterUser", "register"},
		{"TestAuthHandler", "auth"},
		{"Test_Login", "login"},
		{"TestHandler", ""}, // "handler" is noise
		{"TestService", ""}, // "service" is noise
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitCamelToLower(tt.name)
			if got != tt.want {
				t.Errorf("splitCamelToLower(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestPackageToFilePath(t *testing.T) {
	tests := []struct {
		pkg  string
		want string
	}{
		{"github.com/user/project/src/services", "src/services"},
		{"github.com/user/project/internal/handlers", "internal/handlers"},
		{"github.com/user/project/cmd/server", "cmd/server"},
		{"github.com/user/project/pkg/utils", "pkg/utils"},
		{"mypackage", "mypackage"},
	}

	for _, tt := range tests {
		t.Run(tt.pkg, func(t *testing.T) {
			got := packageToFilePath(tt.pkg)
			if got != tt.want {
				t.Errorf("packageToFilePath(%q) = %q, want %q", tt.pkg, got, tt.want)
			}
		})
	}
}
