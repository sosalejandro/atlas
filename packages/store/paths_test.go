package store

import "testing"

// TestIsTestFilePath covers the isTestFilePath helper introduced for issue #79.
// It must return true for all recognised test-file patterns and false for
// production source files.
func TestIsTestFilePath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		// Directory-based patterns.
		{"__tests__ dir", "apps/web-patient/src/__tests__/LoginPage.test.tsx", true},
		{"tests dir", "apps/web-patient/src/tests/LoginPage.test.tsx", true},
		{"test dir", "apps/web-patient/src/test/LoginPage.test.tsx", true},
		{"mobile __tests__", "apps/mobile/src/__tests__/Auth.test.ts", true},
		// Co-located suffix patterns.
		{"co-located .test.tsx", "apps/web-patient/src/pages/LoginPage.test.tsx", true},
		{"co-located .test.ts", "src/contexts/commerce/application/services/story_13_1_atdd_test.ts", false}, // Go _test.go convention, not TS
		{"co-located .spec.ts", "apps/web-patient/src/hooks/usePatients.spec.ts", true},
		{"co-located .spec.tsx", "apps/web-patient/src/pages/Dashboard.spec.tsx", true},
		{"co-located .test.js", "apps/web-patient/src/utils/format.test.js", true},
		{"co-located .spec.jsx", "apps/web-patient/src/components/Button.spec.jsx", true},
		// Non-test files.
		{"go src file", "src/contexts/commerce/application/services/story_13_1_atdd_test.go", false},
		{"impl tsx file", "apps/web-patient/src/pages/LoginPage.tsx", false},
		{"hook ts file", "apps/web-patient/src/hooks/usePatients.ts", false},
		{"impl go file", "src/contexts/auth/application/services/auth_service.go", false},
		{"markdown file", "docs/architecture/overview.md", false},
		{"empty path", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isTestFilePath(tc.in)
			if got != tc.want {
				t.Errorf("isTestFilePath(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestImplFileForTestFile covers the implFileForTestFile helper introduced for
// issue #79. It maps test file paths to their corresponding impl file paths.
func TestImplFileForTestFile(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Directory-based patterns: impl file moves up one directory.
		{
			name: "__tests__ dir .test.tsx",
			in:   "apps/web-patient/src/__tests__/LoginPage.test.tsx",
			want: "apps/web-patient/src/LoginPage.tsx",
		},
		{
			name: "__tests__ dir .test.ts",
			in:   "apps/mobile/src/__tests__/Auth.test.ts",
			want: "apps/mobile/src/Auth.ts",
		},
		{
			name: "tests dir .spec.ts",
			in:   "apps/web-patient/src/tests/usePatients.spec.ts",
			want: "apps/web-patient/src/usePatients.ts",
		},
		// Co-located patterns: impl file stays in same directory.
		{
			name: "co-located .test.tsx",
			in:   "apps/web-patient/src/pages/LoginPage.test.tsx",
			want: "apps/web-patient/src/pages/LoginPage.tsx",
		},
		{
			name: "co-located .spec.ts",
			in:   "apps/web-patient/src/hooks/usePatients.spec.ts",
			want: "apps/web-patient/src/hooks/usePatients.ts",
		},
		{
			name: "co-located .test.js",
			in:   "apps/web-patient/src/utils/format.test.js",
			want: "apps/web-patient/src/utils/format.js",
		},
		// Non-test files return empty.
		{
			name: "impl tsx file returns empty",
			in:   "apps/web-patient/src/pages/LoginPage.tsx",
			want: "",
		},
		{
			name: "empty path returns empty",
			in:   "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := implFileForTestFile(tc.in)
			if got != tc.want {
				t.Errorf("implFileForTestFile(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestBCPathFor pins the conventions docs/architecture.md §3.7 and
// schema-v1.md §5.4 rely on: anything matching src/contexts/<bc>/...
// maps to "src/contexts/<bc>"; nothing else maps to anything.
func TestBCPathFor(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"happy path single-segment bc", "src/contexts/alpha/foo.go", "src/contexts/alpha"},
		{"happy path nested file under bc", "src/contexts/beta/sub/dir/x.go", "src/contexts/beta"},
		{"another bc with deep nesting", "src/contexts/messaging/application/services/conversation.go", "src/contexts/messaging"},
		{"non-contexts path returns empty", "src/shared/logger.go", ""},
		{"contexts but no bc segment yet returns empty", "src/contexts/", ""},
		{"contexts with bc but no trailing file returns empty (no slash after bc)", "src/contexts/alpha", ""},
		{"non-src prefix returns empty", "internal/foo.go", ""},
		{"empty input returns empty", "", ""},
		{"close-but-not-quite prefix returns empty", "src/context/alpha/foo.go", ""},
		{"leading slash is not normalized — strict prefix match", "/src/contexts/alpha/foo.go", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bcPathFor(tc.in)
			if got != tc.want {
				t.Errorf("bcPathFor(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
