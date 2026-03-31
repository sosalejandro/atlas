// @testreg workflow.run
package domain

import (
	"testing"
)

func TestGenerateRunCommand(t *testing.T) {
	tests := []struct {
		name      string
		framework string
		testType  string
		filePath  string
		funcName  string
		want      string
	}{
		{
			name:      "go unit test",
			framework: "go",
			testType:  "unit",
			filePath:  "src/application/services/auth_test.go",
			funcName:  "TestLoginSuccess",
			want:      "go test -run TestLoginSuccess ./src/application/services/...",
		},
		{
			name:      "go e2e test",
			framework: "go",
			testType:  "e2e",
			filePath:  "src/e2e/auth_e2e_test.go",
			funcName:  "TestLoginE2E",
			want:      "go test -tags=e2e -run TestLoginE2E ./src/e2e/...",
		},
		{
			name:      "go integration test",
			framework: "go",
			testType:  "integration",
			filePath:  "src/integration/auth_integration_test.go",
			funcName:  "TestLoginIntegration",
			want:      "go test -run TestLoginIntegration ./src/integration/...",
		},
		{
			name:      "playwright test",
			framework: "playwright",
			testType:  "e2e",
			filePath:  "apps/web/e2e/login.spec.ts",
			funcName:  "should login successfully",
			want:      "npx playwright test -g 'should login successfully'",
		},
		{
			name:      "playwright with regex chars",
			framework: "playwright",
			testType:  "e2e",
			filePath:  "apps/web/e2e/auth.spec.ts",
			funcName:  "login (admin)",
			want:      `npx playwright test -g 'login \(admin\)'`,
		},
		{
			name:      "vitest test",
			framework: "vitest",
			testType:  "unit",
			filePath:  "apps/web/tests/login.test.ts",
			funcName:  "should render login form",
			want:      "npx vitest -t 'should render login form'",
		},
		{
			name:      "jest test",
			framework: "jest",
			testType:  "unit",
			filePath:  "apps/mobile/src/__tests__/Login.test.tsx",
			funcName:  "renders login screen",
			want:      "npx jest -t 'renders login screen'",
		},
		{
			name:      "maestro test",
			framework: "maestro",
			testType:  "e2e",
			filePath:  "apps/mobile/e2e/login-flow.yaml",
			funcName:  "login-flow",
			want:      "maestro test apps/mobile/e2e/login-flow.yaml",
		},
		{
			name:      "unsupported framework",
			framework: "unknown",
			testType:  "unit",
			filePath:  "test.py",
			funcName:  "test_func",
			want:      "# unsupported framework: unknown",
		},
		{
			name:      "go test in root dir",
			framework: "go",
			testType:  "unit",
			filePath:  "main_test.go",
			funcName:  "TestMain",
			want:      "go test -run TestMain ./...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GenerateRunCommand(tt.framework, tt.testType, tt.filePath, tt.funcName)
			if got != tt.want {
				t.Errorf("GenerateRunCommand() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGoPackagePath(t *testing.T) {
	tests := []struct {
		filePath string
		want     string
	}{
		{"src/services/auth_test.go", "./src/services/..."},
		{"src/application/services/auth_test.go", "./src/application/services/..."},
		{"internal/handlers/auth_test.go", "./internal/handlers/..."},
		{"main_test.go", "./..."},
		{"", "./..."},
	}

	for _, tt := range tests {
		t.Run(tt.filePath, func(t *testing.T) {
			got := goPackagePath(tt.filePath)
			if got != tt.want {
				t.Errorf("goPackagePath(%q) = %q, want %q", tt.filePath, got, tt.want)
			}
		})
	}
}

func TestEscapeRegex(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple text", "simple text"},
		{"login (admin)", `login \(admin\)`},
		{"test.name", `test\.name`},
		{"a*b+c?d", `a\*b\+c\?d`},
		{"[bracket]", `\[bracket\]`},
		{"no.special.but.dots", `no\.special\.but\.dots`},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := escapeRegex(tt.input)
			if got != tt.want {
				t.Errorf("escapeRegex(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestCollectRunCommands(t *testing.T) {
	feature := &Feature{
		ID: "auth.login",
		Coverage: Coverage{
			Unit: UnitCoverage{
				Backend: &CoverageEntry{
					Status: StatusCovered,
					Tests: []TestEntry{
						{
							File: "src/auth_test.go",
							Functions: []FunctionEntry{
								{Name: "TestLogin", Run: "go test -run TestLogin ./src/..."},
								{Name: "TestLogout", Run: "go test -run TestLogout ./src/..."},
							},
						},
					},
				},
				Web: &CoverageEntry{
					Status: StatusCovered,
					Tests: []TestEntry{
						{
							File: "apps/web/tests/login.test.ts",
							Functions: []FunctionEntry{
								{Name: "renders login", Run: "npx vitest -t 'renders login'"},
							},
						},
					},
				},
			},
			E2E: E2ECoverage{
				Web: &E2ECoverageEntry{
					Status: StatusCovered,
					Tests: []TestEntry{
						{
							File: "apps/web/e2e/login.spec.ts",
							Functions: []FunctionEntry{
								{Name: "login flow", Run: "npx playwright test -g 'login flow'"},
							},
						},
					},
				},
			},
		},
	}

	t.Run("all commands", func(t *testing.T) {
		cmds := CollectRunCommands(feature, "", "")
		if len(cmds) != 4 {
			t.Fatalf("len = %d, want 4", len(cmds))
		}
	})

	t.Run("filter by platform", func(t *testing.T) {
		cmds := CollectRunCommands(feature, "backend", "")
		if len(cmds) != 2 {
			t.Fatalf("len = %d, want 2", len(cmds))
		}
	})

	t.Run("filter by test type", func(t *testing.T) {
		cmds := CollectRunCommands(feature, "", "e2e")
		if len(cmds) != 1 {
			t.Fatalf("len = %d, want 1", len(cmds))
		}
	})

	t.Run("filter by both", func(t *testing.T) {
		cmds := CollectRunCommands(feature, "web", "unit")
		if len(cmds) != 1 {
			t.Fatalf("len = %d, want 1", len(cmds))
		}
	})

	t.Run("no match", func(t *testing.T) {
		cmds := CollectRunCommands(feature, "mobile", "")
		if len(cmds) != 0 {
			t.Fatalf("len = %d, want 0", len(cmds))
		}
	})
}

func TestHasFlag(t *testing.T) {
	flags := []string{"#mocked", "#wip"}

	if !HasFlag(flags, "#mocked") {
		t.Error("Expected to find #mocked")
	}
	if !HasFlag(flags, "#MOCKED") {
		t.Error("Expected case-insensitive match for #MOCKED")
	}
	if HasFlag(flags, "#real") {
		t.Error("Should not find #real")
	}
	if HasFlag(nil, "#mocked") {
		t.Error("Should not find in nil flags")
	}
}
