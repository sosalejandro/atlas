package store

import (
	"path"
	"strings"
)

// bcPathFor returns the bounded-context path prefix for a repo-relative
// file path, or "" if the file does not live under src/contexts/<bc>/.
//
// The convention is fixed by docs/architecture.md §3.7 + schema-v1.md §5.4.
// Atlas treats anything matching `src/contexts/<bc>/` as living in that BC.
//
// This is a pure string-shape helper — no DB, no side effects. It lives in
// its own file (separate from ingest.go) so the SRP boundary between
// "transactional batch logic" and "path conventions" stays visible. If
// more BC-path helpers accumulate, they belong here.
func bcPathFor(relPath string) string {
	const prefix = "src/contexts/"
	if !strings.HasPrefix(relPath, prefix) {
		return ""
	}
	rest := relPath[len(prefix):]
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 {
		return ""
	}
	return prefix + rest[:slash]
}

// testFileDirSegments is the set of directory names that conventionally hold
// test files in JS/TS projects (as used by Jest, Vitest, and React Native).
// Files inside these directories are treated as test files by isTestFilePath.
var testFileDirSegments = map[string]bool{
	"__tests__": true,
	"tests":     true,
	"test":      true,
}

// testFileBaseSuffixes are the basename suffixes (before the final extension)
// that identify co-located test files outside a dedicated test directory.
// e.g. "LoginPage.test.tsx" has the base suffix ".test" before ".tsx".
var testFileBaseSuffixes = []string{
	".test.ts",
	".test.tsx",
	".spec.ts",
	".spec.tsx",
	".test.js",
	".test.jsx",
	".spec.js",
	".spec.jsx",
}

// isTestFilePath reports whether a repo-relative path identifies a test file:
//   - any file whose path includes a directory segment from testFileDirSegments
//     (e.g. "apps/web-patient/src/__tests__/LoginPage.test.tsx"), OR
//   - any file whose basename matches one of the testFileBaseSuffixes regardless
//     of directory (e.g. "apps/web-patient/src/pages/LoginPage.test.tsx").
func isTestFilePath(relPath string) bool {
	// Check directory segments.
	dir := path.Dir(relPath)
	for seg := dir; seg != "." && seg != "/"; seg = path.Dir(seg) {
		base := path.Base(seg)
		if testFileDirSegments[base] {
			return true
		}
	}
	// Check co-located test suffixes (basename of the full path).
	base := path.Base(relPath)
	for _, suffix := range testFileBaseSuffixes {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}

// implFileForTestFile derives the implementation file path that corresponds to
// a test file path, by stripping the test suffix (e.g. ".test" or ".spec")
// from the basename, and — for directory-based test files — moving up one
// directory level to the source directory.
//
// Examples:
//
//	"apps/web/src/__tests__/LoginPage.test.tsx" → "apps/web/src/LoginPage.tsx"
//	"apps/web/src/pages/LoginPage.test.tsx"     → "apps/web/src/pages/LoginPage.tsx"
//	"apps/mobile/__tests__/Auth.test.ts"        → "apps/mobile/Auth.ts"
//
// Returns "" when the path cannot be mapped (unknown suffix, non-test file, etc.).
func implFileForTestFile(relPath string) string {
	dir := path.Dir(relPath)
	base := path.Base(relPath)

	// Strip known test suffixes, rebuilding the extension from what remains.
	// We try each suffix in order; the first match wins.
	for _, suffix := range testFileBaseSuffixes {
		if strings.HasSuffix(base, suffix) {
			// Determine the final extension from the suffix (e.g. ".tsx" from ".test.tsx").
			dotIdx := strings.LastIndexByte(suffix, '.')
			if dotIdx < 0 {
				continue
			}
			ext := suffix[dotIdx:]                // e.g. ".tsx"
			implBase := base[:len(base)-len(suffix)] + ext // e.g. "LoginPage.tsx"

			// If this file lives in a test directory, the impl lives one level up.
			parentBase := path.Base(dir)
			if testFileDirSegments[parentBase] {
				dir = path.Dir(dir)
			}
			return path.Join(dir, implBase)
		}
	}
	return ""
}
