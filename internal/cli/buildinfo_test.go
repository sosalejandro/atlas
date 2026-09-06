package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"runtime"
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

// TestResolveBuildInfo_SourceStampsShadowModuleInfo pins the consequence of
// the source-baked stamps that docs/install.md has to describe honestly.
//
// Because release-please bakes literal Version/Commit/BuildDate values into
// internal/cli/root.go on the release commit, ldflagsStamped() is true for
// EVERY build made from that source — including `go install …@main`,
// `go install …@<sha>` and a plain `go build` in a clone. resolveBuildInfo
// short-circuits there, so runtime/debug.ReadBuildInfo is never consulted:
// the reported commit and build date are the release commit's stamps, not
// the ones belonging to the thing you actually installed.
//
// That is a real limitation, not a bug to paper over — but a claim that a
// non-tag install "reports dev" is false, and this test is what stops that
// claim being written back into the docs.
func TestResolveBuildInfo_SourceStampsShadowModuleInfo(t *testing.T) {
	const (
		bakedVersion = "v0.13.0"
		bakedCommit  = "fba0d11"
		bakedDate    = "2026-05-24T01:31:52Z"
	)
	withLdflagsVars(t, bakedVersion, bakedCommit, bakedDate)

	// What the module proxy would report for an install from a later,
	// unstamped ref — a pseudo-version and the real revision.
	proxy := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.13.1-0.20260906074419-189e713abcde"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "189e713abcde0000000000000000000000000000"},
			{Key: "vcs.time", Value: "2026-09-06T07:44:19Z"},
		},
	}
	if v, _, _ := resolveFromBuildInfo(proxy); v == bakedVersion {
		t.Fatalf("fixture is not discriminating: module info also yields %q", v)
	}

	v, c, b := resolveBuildInfo()
	if v != bakedVersion || c != bakedCommit || b != bakedDate {
		t.Errorf("got %q/%q/%q; the source-baked stamps must win over module info", v, c, b)
	}
	if v == defaultVersion {
		t.Error("a build from stamped source never reports the 'dev' sentinel")
	}
}

// ---------------------------------------------------------------------------
// Build-provenance reporting (issue #121).
//
// The release pipeline builds with `-trimpath` and CGO_ENABLED=0. Those two
// flags are the difference between "a binary" and "a binary a third party
// can rebuild byte-for-byte", and CGO_ENABLED=0 is separately the property
// that keeps the cross-compile matrix working at all (the store is
// modernc.org/sqlite precisely so no C toolchain is needed).
//
// Someone holding a downloaded binary cannot see the build command that
// produced it. These tests pin the projection that lets `atlas version
// --json` tell them.
// ---------------------------------------------------------------------------

// TestResolveBuildSettings_ReleaseBuildFlagsAreReported is the happy path:
// a binary produced by the release pipeline reports trimpath on, cgo off,
// and therefore satisfies the necessary conditions for reproducibility.
func TestResolveBuildSettings_ReleaseBuildFlagsAreReported(t *testing.T) {
	bi := &debug.BuildInfo{
		GoVersion: "go1.25.14",
		Settings: []debug.BuildSetting{
			{Key: "-trimpath", Value: "true"},
			{Key: "CGO_ENABLED", Value: "0"},
			{Key: "GOOS", Value: "darwin"},
			{Key: "GOARCH", Value: "arm64"},
		},
	}
	got := resolveBuildSettings(bi, true)
	if got.GoVersion != "go1.25.14" {
		t.Errorf("go version: got %q want %q", got.GoVersion, "go1.25.14")
	}
	if got.OS != "darwin" || got.Arch != "arm64" {
		t.Errorf("platform: got %s/%s want darwin/arm64", got.OS, got.Arch)
	}
	if !got.Trimpath {
		t.Error("trimpath: got false, want true (release builds pass -trimpath)")
	}
	if got.CGOEnabled == nil {
		t.Fatal("cgo: got unknown, want a determined answer (CGO_ENABLED=0 was in the table)")
	}
	if *got.CGOEnabled {
		t.Error("cgo: got enabled, want disabled (modernc.org/sqlite is cgo-free)")
	}
	if !got.ReproducibleFlags {
		t.Error("ReproducibleFlags: want true when trimpath is on and cgo is off")
	}
}

// TestResolveBuildSettings_CGOBuildIsNotFlaggedReproducible guards the
// property the cross-compile matrix depends on. A cgo build links against
// the host's libc, so it is neither reproducible off-host nor
// cross-compilable without a C toolchain. Losing CGO_ENABLED=0 silently is
// a named risk of this work; this is the assertion that makes it loud.
func TestResolveBuildSettings_CGOBuildIsNotFlaggedReproducible(t *testing.T) {
	bi := &debug.BuildInfo{
		GoVersion: "go1.25.14",
		Settings: []debug.BuildSetting{
			{Key: "-trimpath", Value: "true"},
			{Key: "CGO_ENABLED", Value: "1"},
		},
	}
	got := resolveBuildSettings(bi, true)
	if got.CGOEnabled == nil || !*got.CGOEnabled {
		t.Fatal("CGO_ENABLED=1 must be reported as enabled")
	}
	if got.ReproducibleFlags {
		t.Error("a cgo build must never be reported as satisfying the reproducible-build flags")
	}
}

// TestResolveBuildSettings_NoTrimpathIsNotReproducible: without -trimpath
// the binary embeds absolute source paths, so two checkouts in different
// directories produce different bytes. Reporting such a build as
// reproducible would be exactly the unearned claim this work exists to
// stop.
func TestResolveBuildSettings_NoTrimpathIsNotReproducible(t *testing.T) {
	bi := &debug.BuildInfo{
		GoVersion: "go1.25.14",
		Settings:  []debug.BuildSetting{{Key: "CGO_ENABLED", Value: "0"}},
	}
	got := resolveBuildSettings(bi, true)
	if got.Trimpath {
		t.Error("absent -trimpath setting must report false")
	}
	if got.ReproducibleFlags {
		t.Error("a build without -trimpath must not be reported as reproducible")
	}
}

// TestResolveBuildSettings_UnreadableBuildInfoFallsBackToRuntime covers the
// stripped-binary case (ReadBuildInfo ok=false). We still know the platform
// we are executing on from the runtime constants, so report those rather
// than empty strings — but claim nothing about the build flags.
func TestResolveBuildSettings_UnreadableBuildInfoFallsBackToRuntime(t *testing.T) {
	got := resolveBuildSettings(nil, false)
	if got.OS != runtime.GOOS || got.Arch != runtime.GOARCH {
		t.Errorf("platform fallback: got %s/%s want %s/%s",
			got.OS, got.Arch, runtime.GOOS, runtime.GOARCH)
	}
	if got.GoVersion != runtime.Version() {
		t.Errorf("go version fallback: got %q want %q", got.GoVersion, runtime.Version())
	}
	if got.Trimpath || got.ReproducibleFlags {
		t.Error("nothing is known about flags when build info is unreadable; must not claim reproducible")
	}
}

// TestResolveBuildSettings_UnknownCGOIsNotReportedAsDisabled is the
// asymmetry between the two flags, made explicit.
//
// For -trimpath, false is the unfavourable answer, so defaulting an unknown
// to false costs the binary the benefit of the doubt and is safe. For cgo it
// is the other way round: `cgo: false` is what a cgo-free release build
// looks like, so reporting an unreadable build table as `false` publishes
// the reassuring answer on no evidence at all. Unknown must stay unknown.
func TestResolveBuildSettings_UnknownCGOIsNotReportedAsDisabled(t *testing.T) {
	got := resolveBuildSettings(nil, false)
	if got.CGOEnabled != nil {
		t.Errorf("cgo: got a determined %v from an unreadable build table; want unknown", *got.CGOEnabled)
	}
	if cgoText(got.CGOEnabled) != "unknown" {
		t.Errorf("human rendering: got %q want %q", cgoText(got.CGOEnabled), "unknown")
	}
	if got.ReproducibleFlags {
		t.Error("an unknown cgo setting cannot satisfy the reproducible-build preconditions")
	}
}

// TestResolveBuildSettings_AbsentCGOSettingIsUnknown covers the readable-
// but-incomplete table: runtime/debug does not promise CGO_ENABLED is
// present, and its absence is no more evidence of a cgo-free build than an
// unreadable table is.
func TestResolveBuildSettings_AbsentCGOSettingIsUnknown(t *testing.T) {
	bi := &debug.BuildInfo{
		GoVersion: "go1.25.14",
		Settings:  []debug.BuildSetting{{Key: "-trimpath", Value: "true"}},
	}
	got := resolveBuildSettings(bi, true)
	if got.CGOEnabled != nil {
		t.Errorf("cgo: got a determined %v with no CGO_ENABLED setting; want unknown", *got.CGOEnabled)
	}
	if got.ReproducibleFlags {
		t.Error("trimpath alone does not establish the reproducible-build preconditions")
	}
}

// TestCGOText pins the three renderings the human output can produce.
func TestCGOText(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name string
		in   *bool
		want string
	}{
		{"unknown", nil, "unknown"},
		{"cgo off", &no, "false"},
		{"cgo on", &yes, "true"},
	} {
		if got := cgoText(tc.in); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

// TestResolveBuildSettings_MissingPlatformSettingsFallBackToRuntime: the
// GOOS/GOARCH build settings are present in practice but are not part of
// the runtime/debug contract. Fall back rather than render an empty
// platform.
func TestResolveBuildSettings_MissingPlatformSettingsFallBackToRuntime(t *testing.T) {
	got := resolveBuildSettings(&debug.BuildInfo{GoVersion: "go1.25.14"}, true)
	if got.OS != runtime.GOOS || got.Arch != runtime.GOARCH {
		t.Errorf("platform: got %s/%s want runtime %s/%s",
			got.OS, got.Arch, runtime.GOOS, runtime.GOARCH)
	}
}

// TestVersionCmd_RegisteredOnRoot — a command not wired to root is not
// delivered.
func TestVersionCmd_RegisteredOnRoot(t *testing.T) {
	var found bool
	for _, c := range NewRootCmd().Commands() {
		if c.Name() == "version" {
			found = true
		}
	}
	if !found {
		t.Error("atlas version is not registered on the root command")
	}
}

// runVersionCmd drives the real command tree so these tests cover the cobra
// wiring, not just the helper underneath it.
func runVersionCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"version"}, args...))
	err := root.ExecuteContext(context.Background())
	return stdout.String(), err
}

// TestVersionCmd_JSONEnvelopeCarriesProvenanceFields pins the machine-
// readable contract. A CI job asking "is the binary I just downloaded the
// reproducible one?" reads these fields; renaming one breaks that job.
func TestVersionCmd_JSONEnvelopeCarriesProvenanceFields(t *testing.T) {
	withLdflagsVars(t, "v1.4.0", "0badc0d", "2026-06-01T00:00:00Z")
	out, err := runVersionCmd(t, "--json")
	if err != nil {
		t.Fatalf("atlas version --json: %v", err)
	}
	var env struct {
		Command string `json:"command"`
		Result  struct {
			Version           string `json:"version"`
			Commit            string `json:"commit"`
			BuildDate         string `json:"build_date"`
			GoVersion         string `json:"go_version"`
			OS                string `json:"os"`
			Arch              string `json:"arch"`
			Trimpath          bool   `json:"trimpath"`
			CGOEnabled        *bool  `json:"cgo_enabled"`
			ReproducibleFlags bool   `json:"reproducible_flags"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode envelope: %v\nraw:\n%s", err, out)
	}
	if env.Command != "version" {
		t.Errorf("envelope command: got %q want %q", env.Command, "version")
	}
	if env.Result.Version != "v1.4.0" || env.Result.Commit != "0badc0d" {
		t.Errorf("stamps not surfaced: got %+v", env.Result)
	}
	if env.Result.BuildDate != "2026-06-01T00:00:00Z" {
		t.Errorf("build_date: got %q", env.Result.BuildDate)
	}
	if env.Result.OS != runtime.GOOS || env.Result.Arch != runtime.GOARCH {
		t.Errorf("platform: got %s/%s want %s/%s",
			env.Result.OS, env.Result.Arch, runtime.GOOS, runtime.GOARCH)
	}
	if env.Result.GoVersion == "" {
		t.Error("go_version must never be empty")
	}
}

// TestVersionCmd_HumanOutputIsHonestAboutUnverifiedReproducibility: the
// flag check is a necessary-conditions check, not proof. The only proof is
// rebuilding and comparing digests. The human output must not let a reader
// mistake one for the other, so it points at the recipe that does prove it.
func TestVersionCmd_HumanOutputIsHonestAboutUnverifiedReproducibility(t *testing.T) {
	withLdflagsVars(t, "v1.4.0", "0badc0d", "2026-06-01T00:00:00Z")
	out, err := runVersionCmd(t)
	if err != nil {
		t.Fatalf("atlas version: %v", err)
	}
	for _, want := range []string{"v1.4.0", "0badc0d", "2026-06-01T00:00:00Z", runtime.GOOS} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "docs/install.md") {
		t.Errorf("human output should point at the verification recipe; got:\n%s", out)
	}
}
