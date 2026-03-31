// @testreg scan.annotation-parser
package adapters

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
	return path
}

func TestParseAnnotatedFile_GoFileLevel(t *testing.T) {
	dir := t.TempDir()
	content := `package services

// @testreg auth.login #mocked

func TestLoginSuccess(t *testing.T) {
	// test body
}

func TestLoginFailure(t *testing.T) {
	// test body
}
`
	path := writeTestFile(t, dir, "auth_test.go", content)
	result, err := ParseAnnotatedFile(path, "src/services/auth_test.go")
	if err != nil {
		t.Fatalf("ParseAnnotatedFile() error = %v", err)
	}
	if result == nil {
		t.Fatal("Expected non-nil result for annotated file")
	}

	if len(result.FeatureIDs) != 1 || result.FeatureIDs[0] != "auth.login" {
		t.Errorf("FeatureIDs = %v, want [auth.login]", result.FeatureIDs)
	}
	if len(result.Flags) != 1 || result.Flags[0] != "#mocked" {
		t.Errorf("Flags = %v, want [#mocked]", result.Flags)
	}
	if len(result.Functions) != 2 {
		t.Fatalf("Functions count = %d, want 2", len(result.Functions))
	}
	if result.Functions[0].Name != "TestLoginSuccess" {
		t.Errorf("Functions[0].Name = %q, want TestLoginSuccess", result.Functions[0].Name)
	}
	if result.Functions[1].Name != "TestLoginFailure" {
		t.Errorf("Functions[1].Name = %q, want TestLoginFailure", result.Functions[1].Name)
	}

	// Both functions should inherit file-level annotation
	for i, fn := range result.Functions {
		if len(fn.FeatureIDs) != 1 || fn.FeatureIDs[0] != "auth.login" {
			t.Errorf("Functions[%d].FeatureIDs = %v, want [auth.login]", i, fn.FeatureIDs)
		}
	}

	if result.TestType != "unit" {
		t.Errorf("TestType = %q, want unit", result.TestType)
	}
	if result.Platform != "backend" {
		t.Errorf("Platform = %q, want backend", result.Platform)
	}
	if result.Framework != "go" {
		t.Errorf("Framework = %q, want go", result.Framework)
	}
}

func TestParseAnnotatedFile_GoTestLevel(t *testing.T) {
	dir := t.TempDir()
	content := `package services

// @testreg auth.login #mocked
func TestLoginSuccess(t *testing.T) {}

// @testreg auth.register #real
func TestRegisterUser(t *testing.T) {}
`
	path := writeTestFile(t, dir, "auth_test.go", content)
	result, err := ParseAnnotatedFile(path, "src/services/auth_test.go")
	if err != nil {
		t.Fatalf("ParseAnnotatedFile() error = %v", err)
	}
	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	if len(result.Functions) != 2 {
		t.Fatalf("Functions count = %d, want 2", len(result.Functions))
	}

	fn0 := result.Functions[0]
	if fn0.Name != "TestLoginSuccess" {
		t.Errorf("Functions[0].Name = %q, want TestLoginSuccess", fn0.Name)
	}
	if len(fn0.FeatureIDs) != 1 || fn0.FeatureIDs[0] != "auth.login" {
		t.Errorf("Functions[0].FeatureIDs = %v, want [auth.login]", fn0.FeatureIDs)
	}
	if len(fn0.Flags) != 1 || fn0.Flags[0] != "#mocked" {
		t.Errorf("Functions[0].Flags = %v, want [#mocked]", fn0.Flags)
	}

	fn1 := result.Functions[1]
	if fn1.Name != "TestRegisterUser" {
		t.Errorf("Functions[1].Name = %q, want TestRegisterUser", fn1.Name)
	}
	if len(fn1.FeatureIDs) != 1 || fn1.FeatureIDs[0] != "auth.register" {
		t.Errorf("Functions[1].FeatureIDs = %v, want [auth.register]", fn1.FeatureIDs)
	}
}

func TestParseAnnotatedFile_GoE2EBuildTag(t *testing.T) {
	dir := t.TempDir()
	content := `//go:build e2e

package e2e

// @testreg auth.login
func TestLoginE2E(t *testing.T) {}
`
	path := writeTestFile(t, dir, "auth_e2e_test.go", content)
	result, err := ParseAnnotatedFile(path, "src/e2e/auth_e2e_test.go")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("Expected non-nil result")
	}
	if result.TestType != "e2e" {
		t.Errorf("TestType = %q, want e2e", result.TestType)
	}
}

func TestParseAnnotatedFile_TypeScript(t *testing.T) {
	dir := t.TempDir()
	content := `// @testreg auth.login #real

test('should login as patient', async () => {
  // test body
});

test('should show error on invalid password', async () => {
  // test body
});
`
	path := writeTestFile(t, dir, "login.test.ts", content)
	result, err := ParseAnnotatedFile(path, "apps/web/tests/login.test.ts")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	if len(result.FeatureIDs) != 1 || result.FeatureIDs[0] != "auth.login" {
		t.Errorf("FeatureIDs = %v, want [auth.login]", result.FeatureIDs)
	}
	if len(result.Functions) != 2 {
		t.Fatalf("Functions count = %d, want 2", len(result.Functions))
	}
	if result.Functions[0].Name != "should login as patient" {
		t.Errorf("Functions[0].Name = %q", result.Functions[0].Name)
	}
	if result.Functions[1].Name != "should show error on invalid password" {
		t.Errorf("Functions[1].Name = %q", result.Functions[1].Name)
	}
	if result.TestType != "unit" {
		t.Errorf("TestType = %q, want unit", result.TestType)
	}
	if result.Platform != "web" {
		t.Errorf("Platform = %q, want web", result.Platform)
	}
	if result.Framework != "vitest" {
		t.Errorf("Framework = %q, want vitest", result.Framework)
	}
}

func TestParseAnnotatedFile_PlaywrightSpec(t *testing.T) {
	dir := t.TempDir()
	content := `// @testreg auth.login

test.describe('Login Flow', () => {
  test('should login successfully', async () => {});
  test('should redirect after login', async () => {});
});
`
	path := writeTestFile(t, dir, "login.spec.ts", content)
	result, err := ParseAnnotatedFile(path, "apps/web/e2e/login.spec.ts")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	if result.TestType != "e2e" {
		t.Errorf("TestType = %q, want e2e", result.TestType)
	}
	if result.Platform != "web" {
		t.Errorf("Platform = %q, want web", result.Platform)
	}
	if result.Framework != "playwright" {
		t.Errorf("Framework = %q, want playwright", result.Framework)
	}

	// Should find describe + 2 test() calls
	if len(result.Functions) != 3 {
		t.Errorf("Functions count = %d, want 3", len(result.Functions))
		for i, fn := range result.Functions {
			t.Logf("  fn[%d]: %q line=%d", i, fn.Name, fn.Line)
		}
	}
}

func TestParseAnnotatedFile_JestIt(t *testing.T) {
	dir := t.TempDir()
	content := `// @testreg profile.view

it('renders profile screen', () => {
  // test body
});

it('displays user name', () => {
  // test body
});
`
	path := writeTestFile(t, dir, "ProfileScreen.test.tsx", content)
	result, err := ParseAnnotatedFile(path, "apps/mobile/src/__tests__/ProfileScreen.test.tsx")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	if result.TestType != "unit" {
		t.Errorf("TestType = %q, want unit", result.TestType)
	}
	if result.Platform != "mobile" {
		t.Errorf("Platform = %q, want mobile", result.Platform)
	}
	if result.Framework != "jest" {
		t.Errorf("Framework = %q, want jest", result.Framework)
	}
	if len(result.Functions) != 2 {
		t.Fatalf("Functions count = %d, want 2", len(result.Functions))
	}
	if result.Functions[0].Name != "renders profile screen" {
		t.Errorf("Functions[0].Name = %q", result.Functions[0].Name)
	}
}

func TestParseAnnotatedFile_MobileIntegration(t *testing.T) {
	dir := t.TempDir()
	content := `// @testreg auth.login #real

test('integration login flow', async () => {});
`
	path := writeTestFile(t, dir, "login.test.ts", content)
	result, err := ParseAnnotatedFile(path, "apps/mobile/src/__tests__/integration/login.test.ts")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("Expected non-nil result")
	}
	if result.TestType != "integration" {
		t.Errorf("TestType = %q, want integration", result.TestType)
	}
	if result.Platform != "mobile" {
		t.Errorf("Platform = %q, want mobile", result.Platform)
	}
}

func TestParseAnnotatedFile_MaestroYAML(t *testing.T) {
	dir := t.TempDir()
	content := `# @testreg auth.login
appId: com.example.app
---
- launchApp
- tapOn: "Login"
`
	path := writeTestFile(t, dir, "login-flow.yaml", content)
	result, err := ParseAnnotatedFile(path, "apps/mobile/e2e/login-flow.yaml")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	if result.TestType != "e2e" {
		t.Errorf("TestType = %q, want e2e", result.TestType)
	}
	if result.Platform != "mobile" {
		t.Errorf("Platform = %q, want mobile", result.Platform)
	}
	if result.Framework != "maestro" {
		t.Errorf("Framework = %q, want maestro", result.Framework)
	}
	if len(result.Functions) != 1 {
		t.Fatalf("Functions count = %d, want 1", len(result.Functions))
	}
	if result.Functions[0].Name != "login-flow" {
		t.Errorf("Functions[0].Name = %q, want login-flow", result.Functions[0].Name)
	}
}

func TestParseAnnotatedFile_NoAnnotations(t *testing.T) {
	dir := t.TempDir()
	content := `package services

func TestSomeFunction(t *testing.T) {
	// no annotations
}
`
	path := writeTestFile(t, dir, "plain_test.go", content)
	result, err := ParseAnnotatedFile(path, "src/services/plain_test.go")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	// Should return nil for files without annotations
	if result != nil {
		t.Errorf("Expected nil result for file without annotations, got %+v", result)
	}
}

func TestParseAnnotatedFile_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "empty_test.go", "")
	result, err := ParseAnnotatedFile(path, "src/empty_test.go")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Error("Expected nil result for empty file")
	}
}

func TestParseAnnotatedFile_BinaryFile(t *testing.T) {
	dir := t.TempDir()
	// Write a file with unsupported extension
	path := writeTestFile(t, dir, "binary.exe", "\x00\x01\x02")
	result, err := ParseAnnotatedFile(path, "binary.exe")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Error("Expected nil result for unsupported file type")
	}
}

func TestParseAnnotatedFile_MultipleFeatureIDs(t *testing.T) {
	dir := t.TempDir()
	content := `package services

// @testreg auth.login,auth.session #mocked #wip

func TestLoginWithSession(t *testing.T) {}
`
	path := writeTestFile(t, dir, "auth_test.go", content)
	result, err := ParseAnnotatedFile(path, "src/services/auth_test.go")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	if len(result.FeatureIDs) != 2 {
		t.Fatalf("FeatureIDs count = %d, want 2", len(result.FeatureIDs))
	}
	if result.FeatureIDs[0] != "auth.login" || result.FeatureIDs[1] != "auth.session" {
		t.Errorf("FeatureIDs = %v", result.FeatureIDs)
	}
	if len(result.Flags) != 2 {
		t.Fatalf("Flags count = %d, want 2", len(result.Flags))
	}
}

func TestParseAnnotatedFile_MixedAnnotations(t *testing.T) {
	dir := t.TempDir()
	content := `package services

// @testreg auth.login #mocked

// @testreg auth.login #real
func TestLoginSuccess(t *testing.T) {}

func TestLoginFailure(t *testing.T) {}

// @testreg auth.register
func TestRegisterUser(t *testing.T) {}
`
	path := writeTestFile(t, dir, "auth_test.go", content)
	result, err := ParseAnnotatedFile(path, "src/services/auth_test.go")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	if len(result.Functions) != 3 {
		t.Fatalf("Functions count = %d, want 3", len(result.Functions))
	}

	// TestLoginSuccess should get the test-level annotation (auth.login #real)
	fn0 := result.Functions[0]
	if fn0.Name != "TestLoginSuccess" {
		t.Errorf("fn[0].Name = %q", fn0.Name)
	}
	if len(fn0.FeatureIDs) != 1 || fn0.FeatureIDs[0] != "auth.login" {
		t.Errorf("fn[0].FeatureIDs = %v, want [auth.login]", fn0.FeatureIDs)
	}
	if len(fn0.Flags) != 1 || fn0.Flags[0] != "#real" {
		t.Errorf("fn[0].Flags = %v, want [#real]", fn0.Flags)
	}

	// TestLoginFailure has no direct annotation, should inherit file-level (auth.login #mocked)
	fn1 := result.Functions[1]
	if fn1.Name != "TestLoginFailure" {
		t.Errorf("fn[1].Name = %q", fn1.Name)
	}
	if len(fn1.FeatureIDs) != 1 || fn1.FeatureIDs[0] != "auth.login" {
		t.Errorf("fn[1].FeatureIDs = %v, want [auth.login]", fn1.FeatureIDs)
	}
	if len(fn1.Flags) != 1 || fn1.Flags[0] != "#mocked" {
		t.Errorf("fn[1].Flags = %v, want [#mocked]", fn1.Flags)
	}

	// TestRegisterUser should get its own annotation
	fn2 := result.Functions[2]
	if fn2.Name != "TestRegisterUser" {
		t.Errorf("fn[2].Name = %q", fn2.Name)
	}
	if len(fn2.FeatureIDs) != 1 || fn2.FeatureIDs[0] != "auth.register" {
		t.Errorf("fn[2].FeatureIDs = %v, want [auth.register]", fn2.FeatureIDs)
	}
}

func TestParseAnnotatedFileForUnmapped(t *testing.T) {
	dir := t.TempDir()
	content := `package services

func TestSomething(t *testing.T) {}
func TestOther(t *testing.T) {}
`
	path := writeTestFile(t, dir, "something_test.go", content)
	result, err := ParseAnnotatedFileForUnmapped(path, "src/services/something_test.go")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("Expected non-nil result for unmapped file with functions")
	}
	if len(result.Functions) != 2 {
		t.Fatalf("Functions count = %d, want 2", len(result.Functions))
	}
	if result.Functions[0].Name != "TestSomething" {
		t.Errorf("Functions[0].Name = %q", result.Functions[0].Name)
	}
}

func TestParseAnnotatedFileForUnmapped_HasAnnotation(t *testing.T) {
	dir := t.TempDir()
	content := `package services

// @testreg auth.login
func TestLogin(t *testing.T) {}
`
	path := writeTestFile(t, dir, "login_test.go", content)
	result, err := ParseAnnotatedFileForUnmapped(path, "src/services/login_test.go")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	// Should return nil because file HAS annotations (not unmapped)
	if result != nil {
		t.Error("Expected nil result for annotated file")
	}
}

func TestClassifyFromPath(t *testing.T) {
	tests := []struct {
		name           string
		relPath        string
		lang           string
		hasE2EBuildTag bool
		wantType       string
		wantPlatform   string
	}{
		{"go unit", "src/services/auth_test.go", "go", false, "unit", "backend"},
		{"go e2e build tag", "src/e2e/auth_test.go", "go", true, "e2e", "backend"},
		{"go e2e filename", "src/auth_e2e_test.go", "go", false, "e2e", "backend"},
		{"go integration", "src/auth_integration_test.go", "go", false, "integration", "backend"},
		{"web unit", "apps/web/tests/login.test.ts", "js", false, "unit", "web"},
		{"web e2e", "apps/web/e2e/login.spec.ts", "js", false, "e2e", "web"},
		{"mobile unit", "apps/mobile/src/__tests__/Login.test.tsx", "js", false, "unit", "mobile"},
		{"mobile integration", "apps/mobile/src/__tests__/integration/login.test.ts", "js", false, "integration", "mobile"},
		{"mobile e2e", "apps/mobile/e2e/login.yaml", "maestro", false, "e2e", "mobile"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotPlatform := classifyFromPath(tt.relPath, tt.lang, tt.hasE2EBuildTag)
			if gotType != tt.wantType {
				t.Errorf("testType = %q, want %q", gotType, tt.wantType)
			}
			if gotPlatform != tt.wantPlatform {
				t.Errorf("platform = %q, want %q", gotPlatform, tt.wantPlatform)
			}
		})
	}
}

func TestDetectLanguage(t *testing.T) {
	tests := []struct {
		ext  string
		want string
	}{
		{".go", "go"},
		{".ts", "js"},
		{".tsx", "js"},
		{".js", "js"},
		{".jsx", "js"},
		{".yaml", "maestro"},
		{".yml", "maestro"},
		{".py", ""},
		{".rs", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.ext, func(t *testing.T) {
			got := detectLanguage(tt.ext)
			if got != tt.want {
				t.Errorf("detectLanguage(%q) = %q, want %q", tt.ext, got, tt.want)
			}
		})
	}
}

func TestParseAnnotationPayload(t *testing.T) {
	tests := []struct {
		name       string
		payload    string
		wantIDs    []string
		wantFlags  []string
	}{
		{
			name:      "single ID",
			payload:   "auth.login",
			wantIDs:   []string{"auth.login"},
			wantFlags: nil,
		},
		{
			name:      "ID with flag",
			payload:   "auth.login #mocked",
			wantIDs:   []string{"auth.login"},
			wantFlags: []string{"#mocked"},
		},
		{
			name:      "multiple flags",
			payload:   "auth.login #mocked #wip",
			wantIDs:   []string{"auth.login"},
			wantFlags: []string{"#mocked", "#wip"},
		},
		{
			name:      "comma-separated IDs",
			payload:   "auth.login,auth.session #mocked",
			wantIDs:   []string{"auth.login", "auth.session"},
			wantFlags: []string{"#mocked"},
		},
		{
			name:      "space-separated IDs with flags",
			payload:   "auth.login meals.log #real",
			wantIDs:   []string{"auth.login", "meals.log"},
			wantFlags: []string{"#real"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ann := parseAnnotationPayload(tt.payload, 1)
			if len(ann.featureIDs) != len(tt.wantIDs) {
				t.Fatalf("featureIDs = %v, want %v", ann.featureIDs, tt.wantIDs)
			}
			for i, id := range tt.wantIDs {
				if ann.featureIDs[i] != id {
					t.Errorf("featureIDs[%d] = %q, want %q", i, ann.featureIDs[i], id)
				}
			}
			if len(ann.flags) != len(tt.wantFlags) {
				t.Fatalf("flags = %v, want %v", ann.flags, tt.wantFlags)
			}
			for i, f := range tt.wantFlags {
				if ann.flags[i] != f {
					t.Errorf("flags[%d] = %q, want %q", i, ann.flags[i], f)
				}
			}
		})
	}
}

func TestFrameworkFromLang(t *testing.T) {
	tests := []struct {
		lang    string
		relPath string
		want    string
	}{
		{"go", "src/auth_test.go", "go"},
		{"js", "apps/web/e2e/login.spec.ts", "playwright"},
		{"js", "apps/web/tests/login.test.ts", "vitest"},
		{"js", "apps/mobile/src/__tests__/Login.test.tsx", "jest"},
		{"maestro", "apps/mobile/e2e/login.yaml", "maestro"},
	}

	for _, tt := range tests {
		t.Run(tt.relPath, func(t *testing.T) {
			got := frameworkFromLang(tt.lang, tt.relPath)
			if got != tt.want {
				t.Errorf("frameworkFromLang(%q, %q) = %q, want %q", tt.lang, tt.relPath, got, tt.want)
			}
		})
	}
}

func TestDedup(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{"nil", nil, nil},
		{"empty", []string{}, nil},
		{"no duplicates", []string{"a", "b"}, []string{"a", "b"}},
		{"with duplicates", []string{"a", "b", "a", "c", "b"}, []string{"a", "b", "c"}},
		{"all same", []string{"a", "a", "a"}, []string{"a"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dedup(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("dedup() = %v, want %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("dedup()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// @api annotation parsing tests
// ---------------------------------------------------------------------------

func TestParseAnnotatedSource_SingleAPI(t *testing.T) {
	dir := t.TempDir()
	content := `package handlers

// @api POST /api/v1/auth/login
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	// handler body
}
`
	path := writeTestFile(t, dir, "auth_handler.go", content)
	result, err := ParseAnnotatedSource(path, "src/handlers/auth_handler.go")
	if err != nil {
		t.Fatalf("ParseAnnotatedSource() error = %v", err)
	}
	if result == nil {
		t.Fatal("Expected non-nil result for file with @api annotation")
	}

	apis, ok := result.FuncAPIs["AuthHandler.Login"]
	if !ok {
		t.Fatalf("Expected FuncAPIs to contain AuthHandler.Login, got keys: %v", funcAPIKeys(result))
	}
	if len(apis) != 1 {
		t.Fatalf("Expected 1 API annotation, got %d", len(apis))
	}
	if apis[0].Method != "POST" {
		t.Errorf("Method = %q, want POST", apis[0].Method)
	}
	if apis[0].Path != "/api/v1/auth/login" {
		t.Errorf("Path = %q, want /api/v1/auth/login", apis[0].Path)
	}
}

func TestParseAnnotatedSource_MultipleAPIsOnOneFunction(t *testing.T) {
	dir := t.TempDir()
	content := `package handlers

// @api GET /api/v1/subjects
// @api GET /api/v1/subjects/{id}
func (h *SubjectHandler) GetByID(w http.ResponseWriter, r *http.Request) {
	// handler body
}
`
	path := writeTestFile(t, dir, "subject_handler.go", content)
	result, err := ParseAnnotatedSource(path, "src/handlers/subject_handler.go")
	if err != nil {
		t.Fatalf("ParseAnnotatedSource() error = %v", err)
	}
	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	apis, ok := result.FuncAPIs["SubjectHandler.GetByID"]
	if !ok {
		t.Fatalf("Expected FuncAPIs to contain SubjectHandler.GetByID, got keys: %v", funcAPIKeys(result))
	}
	if len(apis) != 2 {
		t.Fatalf("Expected 2 API annotations, got %d", len(apis))
	}
	if apis[0].Method != "GET" || apis[0].Path != "/api/v1/subjects" {
		t.Errorf("apis[0] = %+v, want GET /api/v1/subjects", apis[0])
	}
	if apis[1].Method != "GET" || apis[1].Path != "/api/v1/subjects/{id}" {
		t.Errorf("apis[1] = %+v, want GET /api/v1/subjects/{id}", apis[1])
	}
}

func TestParseAnnotatedSource_MixedTestregAndAPI(t *testing.T) {
	// This tests that @api annotations on a source file don't interfere with
	// @testreg parsing (they're separate parsers). The @api parser only looks
	// for @api and function declarations.
	dir := t.TempDir()
	content := `package handlers

// @api POST /api/v1/auth/login
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {}

// @api DELETE /api/v1/auth/session
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {}
`
	path := writeTestFile(t, dir, "auth_handler.go", content)
	result, err := ParseAnnotatedSource(path, "src/handlers/auth_handler.go")
	if err != nil {
		t.Fatalf("ParseAnnotatedSource() error = %v", err)
	}
	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	if len(result.FuncAPIs) != 2 {
		t.Fatalf("Expected 2 functions with APIs, got %d: %v", len(result.FuncAPIs), funcAPIKeys(result))
	}

	loginAPIs := result.FuncAPIs["AuthHandler.Login"]
	if len(loginAPIs) != 1 || loginAPIs[0].Method != "POST" {
		t.Errorf("Login APIs = %+v, want [{POST /api/v1/auth/login}]", loginAPIs)
	}

	logoutAPIs := result.FuncAPIs["AuthHandler.Logout"]
	if len(logoutAPIs) != 1 || logoutAPIs[0].Method != "DELETE" {
		t.Errorf("Logout APIs = %+v, want [{DELETE /api/v1/auth/session}]", logoutAPIs)
	}
}

func TestParseAnnotatedSource_AllHTTPMethods(t *testing.T) {
	methods := []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"}
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			dir := t.TempDir()
			content := `package handlers

// @api ` + method + ` /api/v1/resource
func (h *Handler) Do(w http.ResponseWriter, r *http.Request) {}
`
			path := writeTestFile(t, dir, "handler.go", content)
			result, err := ParseAnnotatedSource(path, "src/handlers/handler.go")
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if result == nil {
				t.Fatal("Expected non-nil result")
			}
			apis := result.FuncAPIs["Handler.Do"]
			if len(apis) != 1 || apis[0].Method != method {
				t.Errorf("Method = %q, want %q", apis[0].Method, method)
			}
		})
	}
}

func TestParseAnnotatedSource_PlainFunction(t *testing.T) {
	dir := t.TempDir()
	content := `package handlers

// @api GET /api/v1/health
func HealthCheck(w http.ResponseWriter, r *http.Request) {}
`
	path := writeTestFile(t, dir, "health.go", content)
	result, err := ParseAnnotatedSource(path, "src/handlers/health.go")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	apis, ok := result.FuncAPIs["HealthCheck"]
	if !ok {
		t.Fatalf("Expected FuncAPIs to contain HealthCheck, got keys: %v", funcAPIKeys(result))
	}
	if len(apis) != 1 || apis[0].Method != "GET" || apis[0].Path != "/api/v1/health" {
		t.Errorf("apis = %+v, want [{GET /api/v1/health}]", apis)
	}
}

func TestParseAnnotatedSource_PointerReceiver(t *testing.T) {
	dir := t.TempDir()
	content := `package handlers

// @api PUT /api/v1/users/{id}
func (h *UserHandler) Update(w http.ResponseWriter, r *http.Request) {}
`
	path := writeTestFile(t, dir, "user_handler.go", content)
	result, err := ParseAnnotatedSource(path, "src/handlers/user_handler.go")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	apis, ok := result.FuncAPIs["UserHandler.Update"]
	if !ok {
		t.Fatalf("Expected FuncAPIs to contain UserHandler.Update, got keys: %v", funcAPIKeys(result))
	}
	if len(apis) != 1 || apis[0].Method != "PUT" {
		t.Errorf("apis = %+v, want [{PUT /api/v1/users/{id}}]", apis)
	}
}

func TestParseAnnotatedSource_NoAPIAnnotations(t *testing.T) {
	dir := t.TempDir()
	content := `package handlers

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {}
`
	path := writeTestFile(t, dir, "auth_handler.go", content)
	result, err := ParseAnnotatedSource(path, "src/handlers/auth_handler.go")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Error("Expected nil result for file without @api annotations")
	}
}

func TestParseAnnotatedSource_NonGoFile(t *testing.T) {
	dir := t.TempDir()
	content := `// @api GET /api/v1/test
test('something', () => {});`
	path := writeTestFile(t, dir, "handler.ts", content)
	result, err := ParseAnnotatedSource(path, "src/handlers/handler.ts")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Error("Expected nil result for non-Go file")
	}
}

func TestParseAnnotatedSource_APINotInComment(t *testing.T) {
	dir := t.TempDir()
	content := `package handlers

var route = "@api GET /api/v1/test"

func (h *Handler) Do(w http.ResponseWriter, r *http.Request) {}
`
	path := writeTestFile(t, dir, "handler.go", content)
	result, err := ParseAnnotatedSource(path, "src/handlers/handler.go")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Error("Expected nil result when @api is not in a comment")
	}
}

func TestParseAnnotatedSource_OrphanedAPIAnnotation(t *testing.T) {
	dir := t.TempDir()
	// @api annotation followed by a non-func line (type declaration) should be discarded.
	content := `package handlers

// @api GET /api/v1/orphan
type Handler struct{}

// @api POST /api/v1/real
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {}
`
	path := writeTestFile(t, dir, "handler.go", content)
	result, err := ParseAnnotatedSource(path, "src/handlers/handler.go")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// Only Handler.Create should have an API annotation.
	if len(result.FuncAPIs) != 1 {
		t.Fatalf("Expected 1 function with APIs, got %d: %v", len(result.FuncAPIs), funcAPIKeys(result))
	}
	apis, ok := result.FuncAPIs["Handler.Create"]
	if !ok {
		t.Fatalf("Expected Handler.Create, got keys: %v", funcAPIKeys(result))
	}
	if apis[0].Method != "POST" {
		t.Errorf("Method = %q, want POST", apis[0].Method)
	}
}

func TestParseAPIAnnotationLine(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantNil    bool
		wantMethod string
		wantPath   string
	}{
		{
			name:       "basic GET",
			line:       "// @api GET /api/v1/users",
			wantMethod: "GET",
			wantPath:   "/api/v1/users",
		},
		{
			name:       "POST with path params",
			line:       "// @api POST /api/v1/auth/login",
			wantMethod: "POST",
			wantPath:   "/api/v1/auth/login",
		},
		{
			name:       "DELETE with braces",
			line:       "// @api DELETE /api/v1/users/{id}",
			wantMethod: "DELETE",
			wantPath:   "/api/v1/users/{id}",
		},
		{
			name:    "not in comment",
			line:    `@api GET /api/v1/users`,
			wantNil: true,
		},
		{
			name:    "unsupported method",
			line:    "// @api CONNECT /api/v1/tunnel",
			wantNil: true,
		},
		{
			name:    "no path",
			line:    "// @api GET",
			wantNil: true,
		},
		{
			name:       "block comment",
			line:       "* @api PATCH /api/v1/users/{id}",
			wantMethod: "PATCH",
			wantPath:   "/api/v1/users/{id}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := parseAPIAnnotationLine(tt.line, 1)
			if tt.wantNil {
				if api != nil {
					t.Errorf("Expected nil, got %+v", api)
				}
				return
			}
			if api == nil {
				t.Fatal("Expected non-nil result")
			}
			if api.method != tt.wantMethod {
				t.Errorf("method = %q, want %q", api.method, tt.wantMethod)
			}
			if api.path != tt.wantPath {
				t.Errorf("path = %q, want %q", api.path, tt.wantPath)
			}
		})
	}
}

// funcAPIKeys returns the function IDs from an AnnotatedSource for test diagnostics.
func funcAPIKeys(src *AnnotatedSource) []string {
	if src == nil {
		return nil
	}
	keys := make([]string, 0, len(src.FuncAPIs))
	for k := range src.FuncAPIs {
		keys = append(keys, k)
	}
	return keys
}

func TestAnnotationNotInComment(t *testing.T) {
	dir := t.TempDir()
	// @testreg in non-comment context should be ignored
	content := `package services

var annotation = "@testreg auth.login"

func TestSomething(t *testing.T) {}
`
	path := writeTestFile(t, dir, "fake_test.go", content)
	result, err := ParseAnnotatedFile(path, "src/services/fake_test.go")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	// No comment-based annotations, so result should be nil
	if result != nil {
		t.Error("Expected nil result when @testreg is not in a comment")
	}
}

func TestParseAnnotatedFile_GoIntegrationPath(t *testing.T) {
	dir := t.TempDir()
	content := `package integration

// @testreg auth.login #real
func TestLoginIntegration(t *testing.T) {}
`
	path := writeTestFile(t, dir, "auth_integration_test.go", content)
	result, err := ParseAnnotatedFile(path, "src/auth_integration_test.go")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("Expected non-nil result")
	}
	if result.TestType != "integration" {
		t.Errorf("TestType = %q, want integration", result.TestType)
	}
}

func TestParseAnnotatedFile_BenchmarkFunction(t *testing.T) {
	dir := t.TempDir()
	content := `package services

// @testreg auth.login
func BenchmarkLogin(b *testing.B) {}
`
	path := writeTestFile(t, dir, "bench_test.go", content)
	result, err := ParseAnnotatedFile(path, "src/services/bench_test.go")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("Expected non-nil result")
	}
	if len(result.Functions) != 1 {
		t.Fatalf("Functions count = %d, want 1", len(result.Functions))
	}
	if result.Functions[0].Name != "BenchmarkLogin" {
		t.Errorf("Functions[0].Name = %q, want BenchmarkLogin", result.Functions[0].Name)
	}
}
