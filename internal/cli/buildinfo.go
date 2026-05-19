package cli

import (
	"runtime/debug"
)

// Build-info defaults. These are the sentinel values that mean "the linker
// did not stamp a real value here." They are also the absolute last-resort
// fallback when runtime/debug.ReadBuildInfo() reports ok=false (e.g. a
// stripped binary built without module info).
const (
	defaultVersion   = "dev"
	defaultCommit    = "unknown"
	defaultBuildDate = "unknown"
)

// shortSHALen is the conventional Git short-hash width. ReadBuildInfo
// surfaces the full 40-char vcs.revision; we truncate so the rendered
// --version string stays terse.
const shortSHALen = 7

// resolveBuildInfo returns the effective (version, commit, builtAt) triple
// for `atlas --version`.
//
// Resolution order — first non-default source wins:
//
//  1. Ldflags-injected package vars (Version / Commit / BuildDate). Any
//     one of them being non-default flips the entire triple into
//     "ldflags mode" so a CI build that stamps all three keeps its
//     contract intact. Individual unstamped fields fall back to the
//     ldflags defaults (`dev` / `unknown` / `unknown`) rather than
//     mixing sources, which would surface a confusing hybrid.
//  2. runtime/debug.ReadBuildInfo() — the path that makes
//     `go install github.com/sosalejandro/atlas/cmd/atlas@v0.1.3`
//     produce a real "v0.1.3" instead of "dev". `Main.Version == "(devel)"`
//     is normalised to `"dev"` so a local-tree `go build` stays readable.
//     vcs.revision is truncated to a 7-char short SHA; vcs.time is
//     surfaced verbatim (RFC 3339 from the Go toolchain).
//  3. Hard-coded defaults — only when ReadBuildInfo reports ok=false.
//
// The function is exported via the package vars Version/Commit/BuildDate
// at init() time; tests drive the pure helper resolveFromBuildInfo via
// synthetic *debug.BuildInfo values without poking the real runtime.
func resolveBuildInfo() (version, commit, builtAt string) {
	if ldflagsStamped() {
		return Version, Commit, BuildDate
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return defaultVersion, defaultCommit, defaultBuildDate
	}
	return resolveFromBuildInfo(bi)
}

// ldflagsStamped reports whether any of the three build-info package
// vars has been overridden via `-ldflags="-X ..."`. Treating "any one"
// as the trigger keeps release-build behaviour unchanged: if CI stamps
// Version=v0.7.0 but forgets BuildDate, we still emit
// "v0.7.0 (commit unknown, built unknown)" — never a hybrid of ldflags
// + ReadBuildInfo data, which would be confusing to debug.
func ldflagsStamped() bool {
	return Version != defaultVersion ||
		Commit != defaultCommit ||
		BuildDate != defaultBuildDate
}

// resolveFromBuildInfo is the pure projection from a *debug.BuildInfo
// value to the (version, commit, builtAt) triple. Split out so tests
// can feed synthetic BuildInfo structs without invoking the real
// runtime/debug machinery.
//
// Contract:
//   - bi.Main.Version == "(devel)" maps to "dev" (Go uses "(devel)" for
//     a local-tree build with no module-version stamp).
//   - bi.Main.Version == "" falls back to defaultVersion.
//   - vcs.revision is truncated to shortSHALen chars; a shorter or
//     missing value passes through (zero-length stays as defaultCommit).
//   - vcs.time passes through verbatim; absence falls back to
//     defaultBuildDate.
func resolveFromBuildInfo(bi *debug.BuildInfo) (version, commit, builtAt string) {
	version = normaliseModuleVersion(bi.Main.Version)
	commit, builtAt = defaultCommit, defaultBuildDate
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			commit = shortenSHA(s.Value)
		case "vcs.time":
			if s.Value != "" {
				builtAt = s.Value
			}
		}
	}
	return version, commit, builtAt
}

// normaliseModuleVersion maps the raw bi.Main.Version string to the
// public-facing version slot.
//
//   - "(devel)" → "dev"   (Go's local-tree marker — useless to end users)
//   - ""         → "dev"   (defensive; should not occur in practice)
//   - anything else passes through (real tag like "v0.1.3", or the
//     pseudo-version "v0.1.4-0.YYYYMMDDhhmmss-hash" for @main installs).
func normaliseModuleVersion(v string) string {
	if v == "" || v == "(devel)" {
		return defaultVersion
	}
	return v
}

// shortenSHA truncates a Git revision to the conventional 7-char short
// form. Inputs shorter than shortSHALen pass through unchanged so a
// stub/synthetic value in tests still renders deterministically. An
// empty input returns the defaultCommit sentinel.
func shortenSHA(s string) string {
	if s == "" {
		return defaultCommit
	}
	if len(s) <= shortSHALen {
		return s
	}
	return s[:shortSHALen]
}
