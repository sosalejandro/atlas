package cli

import (
	"runtime/debug"
	"strings"
	"testing"
)

// withLdflagsVars swaps the package-level Version/Commit/BuildDate for the
// duration of a sub-test and restores them on cleanup. Tests touching
// these globals MUST NOT use t.Parallel() — same constraint as
// NewRootCmd (documented on that function).
func withLdflagsVars(t *testing.T, v, c, b string) {
	t.Helper()
	origV, origC, origB := Version, Commit, BuildDate
	Version, Commit, BuildDate = v, c, b
	t.Cleanup(func() {
		Version, Commit, BuildDate = origV, origC, origB
	})
}

// TestResolveBuildInfo_LdflagsWins covers the release-build pathway: when
// CI stamps a real Version via -ldflags, resolveBuildInfo must return the
// stamped triple verbatim and NOT consult runtime/debug. This protects the
// existing release contract (changing it would silently re-route prod
// binaries through ReadBuildInfo).
func TestResolveBuildInfo_LdflagsWins(t *testing.T) {
	withLdflagsVars(t, "v9.9.9", "deadbee", "2026-05-19T00:00:00Z")
	v, c, b := resolveBuildInfo()
	if v != "v9.9.9" || c != "deadbee" || b != "2026-05-19T00:00:00Z" {
		t.Fatalf("ldflags override not respected: got (%q,%q,%q)", v, c, b)
	}
}

// TestResolveBuildInfo_PartialLdflagsStillWins documents the "any field
// stamped → ldflags mode" contract. If CI stamps only Version, we keep
// the other slots as ldflags defaults rather than mixing in ReadBuildInfo
// data (which would surface a confusing hybrid).
func TestResolveBuildInfo_PartialLdflagsStillWins(t *testing.T) {
	withLdflagsVars(t, "v0.0.1", defaultCommit, defaultBuildDate)
	v, c, b := resolveBuildInfo()
	if v != "v0.0.1" || c != defaultCommit || b != defaultBuildDate {
		t.Fatalf("partial ldflags should still pin to ldflags mode: got (%q,%q,%q)", v, c, b)
	}
}

// TestResolveFromBuildInfo_DevelMapping verifies the local-tree case:
// a `go build ./...` produces Main.Version == "(devel)". We map that to
// "dev" so end users don't see the Go-internal sentinel.
func TestResolveFromBuildInfo_DevelMapping(t *testing.T) {
	bi := &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
	}
	v, c, b := resolveFromBuildInfo(bi)
	if v != defaultVersion {
		t.Errorf("(devel) should map to %q; got %q", defaultVersion, v)
	}
	if c != defaultCommit {
		t.Errorf("missing vcs.revision should fall back to %q; got %q", defaultCommit, c)
	}
	if b != defaultBuildDate {
		t.Errorf("missing vcs.time should fall back to %q; got %q", defaultBuildDate, b)
	}
}

// TestResolveFromBuildInfo_TaggedInstall covers the headline use case:
// `go install ...@v0.1.3` surfaces "v0.1.3" via Main.Version. The
// vcs.revision is stamped by the toolchain and must be shortened to 7
// chars; vcs.time passes through unchanged.
func TestResolveFromBuildInfo_TaggedInstall(t *testing.T) {
	bi := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.1.3"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abcdef1234567890abcdef1234567890abcdef12"},
			{Key: "vcs.time", Value: "2026-05-18T09:30:00Z"},
			{Key: "GOOS", Value: "linux"}, // noise; must be ignored
		},
	}
	v, c, b := resolveFromBuildInfo(bi)
	if v != "v0.1.3" {
		t.Errorf("version: got %q want %q", v, "v0.1.3")
	}
	if c != "abcdef1" {
		t.Errorf("commit: got %q want short SHA %q", c, "abcdef1")
	}
	if len(c) != shortSHALen {
		t.Errorf("commit length: got %d want %d", len(c), shortSHALen)
	}
	if b != "2026-05-18T09:30:00Z" {
		t.Errorf("builtAt: got %q want passthrough RFC3339", b)
	}
}

// TestResolveFromBuildInfo_PseudoVersion verifies the @main install case:
// Go synthesises a pseudo-version like "v0.1.4-0.YYYYMMDDhhmmss-hash"
// that must pass through unchanged — it is more informative than "dev".
func TestResolveFromBuildInfo_PseudoVersion(t *testing.T) {
	pseudo := "v0.1.4-0.20260518093000-abc123def456"
	bi := &debug.BuildInfo{Main: debug.Module{Version: pseudo}}
	v, _, _ := resolveFromBuildInfo(bi)
	if v != pseudo {
		t.Errorf("pseudo-version should pass through; got %q want %q", v, pseudo)
	}
}

// TestShortenSHA exercises the boundary conditions of the SHA truncation
// helper: long → truncated, exact-length → unchanged, short → unchanged
// (defensive for synthetic test values), empty → defaultCommit sentinel.
func TestShortenSHA(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"abcdef1234567890abcdef1234567890abcdef12", "abcdef1"},
		{"abcdef1", "abcdef1"},
		{"abc", "abc"},
		{"", defaultCommit},
	}
	for _, tc := range cases {
		if got := shortenSHA(tc.in); got != tc.want {
			t.Errorf("shortenSHA(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// TestNormaliseModuleVersion locks in the three branches of the version
// projection: empty and "(devel)" both map to "dev"; anything else passes
// through unchanged.
func TestNormaliseModuleVersion(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", defaultVersion},
		{"(devel)", defaultVersion},
		{"v0.1.3", "v0.1.3"},
		{"v1.0.0-rc.1", "v1.0.0-rc.1"},
	}
	for _, tc := range cases {
		if got := normaliseModuleVersion(tc.in); got != tc.want {
			t.Errorf("normaliseModuleVersion(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// TestRootCmd_VersionStringFormat asserts the wired cobra Version field
// renders in the documented "X (commit Y, built Z)" shape. This guards
// against accidental format drift when refactoring resolveBuildInfo or
// the fmt.Sprintf call in NewRootCmd.
func TestRootCmd_VersionStringFormat(t *testing.T) {
	withLdflagsVars(t, "v1.2.3", "cafebab", "2026-05-19T00:00:00Z")
	root := NewRootCmd()
	want := "v1.2.3 (commit cafebab, built 2026-05-19T00:00:00Z)"
	if root.Version != want {
		t.Fatalf("root.Version = %q; want %q", root.Version, want)
	}
	if !strings.Contains(root.Version, "v1.2.3") {
		t.Fatalf("rendered version missing tag; got %q", root.Version)
	}
}

// TestResolveBuildInfo_StampedReleaseAllThreeBaked is the dedicated guard
// for issue #43's release-please pre-tag stamping pathway. When the
// release-please workflow bakes literal Version/Commit/BuildDate into
// internal/cli/root.go (replacing the `= defaultX` initialisers with
// quoted string literals), `go install ...@vX.Y.Z` users compile a
// binary whose package vars are ALL non-default at startup. This test
// pins that contract: resolveBuildInfo must return the three stamped
// values verbatim, in order, with no consultation of runtime/debug —
// which is what makes the install-from-module-proxy path fully reflect
// the tag's stamp instead of falling through to the "unknown" sentinels.
//
// This is intentionally a separate test from TestResolveBuildInfo_LdflagsWins
// even though both exercise the ldflags-mode path: that test documents
// the -ldflags injection contract for CI binary builds; this one
// documents the source-baked contract for `go install` users. Keeping
// them separate means a future refactor that breaks ONE of the two
// pathways will surface a targeted failure name rather than a generic
// "ldflags broken" signal.
func TestResolveBuildInfo_StampedReleaseAllThreeBaked(t *testing.T) {
	const (
		stampedVersion   = "v0.2.0"
		stampedCommit    = "1a849a5"
		stampedBuildDate = "2026-05-22T13:00:36Z"
	)
	withLdflagsVars(t, stampedVersion, stampedCommit, stampedBuildDate)

	v, c, b := resolveBuildInfo()
	if v != stampedVersion {
		t.Errorf("stamped version: got %q want %q", v, stampedVersion)
	}
	if c != stampedCommit {
		t.Errorf("stamped commit: got %q want %q", c, stampedCommit)
	}
	if b != stampedBuildDate {
		t.Errorf("stamped buildDate: got %q want %q", b, stampedBuildDate)
	}

	// Also assert the user-visible rendering matches the acceptance
	// criterion from issue #43: `atlas version vX.Y.Z (commit <sha>,
	// built <date>)` — no "unknown" anywhere.
	root := NewRootCmd()
	want := stampedVersion + " (commit " + stampedCommit + ", built " + stampedBuildDate + ")"
	if root.Version != want {
		t.Fatalf("rendered version: got %q want %q", root.Version, want)
	}
	if strings.Contains(root.Version, "unknown") {
		t.Fatalf("rendered version must not contain 'unknown' once stamped: %q", root.Version)
	}
	if strings.Contains(root.Version, "dev") {
		t.Fatalf("rendered version must not contain 'dev' once stamped: %q", root.Version)
	}
}
