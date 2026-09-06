package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
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

// buildSettings is what `atlas version` can honestly say about how the
// running binary was compiled, read back out of the table the Go linker
// embeds (runtime/debug.BuildInfo.Settings).
//
// Two of these fields are load-bearing rather than decorative:
//
//   - Trimpath. Without -trimpath the binary embeds the absolute path of
//     the checkout it was built from, so the same commit built in
//     /home/a/atlas and /home/b/atlas yields different bytes. Nobody can
//     reproduce such a build without also reproducing the directory layout.
//   - CGOEnabled. atlas stores state in modernc.org/sqlite specifically so
//     no C toolchain is involved. CGO_ENABLED=0 is what makes the
//     linux/darwin/windows x amd64/arm64 matrix buildable from one host,
//     and a cgo build is additionally tied to the host's libc. Losing this
//     silently is the failure this field exists to expose.
type buildSettings struct {
	GoVersion  string
	OS         string
	Arch       string
	Trimpath   bool
	CGOEnabled bool
	// ReproducibleFlags reports whether the two build-flag preconditions
	// for a byte-identical rebuild hold. It is deliberately NOT called
	// "Reproducible": these are necessary conditions, not proof. The only
	// proof is rebuilding the same commit and comparing digests, which is
	// what docs/install.md documents and what CI runs on every push.
	ReproducibleFlags bool
}

// resolveBuildSettings projects a *debug.BuildInfo onto the fields
// `atlas version` reports. Split out as a pure function, and taking `ok`
// explicitly rather than calling ReadBuildInfo itself, so tests can drive
// the stripped-binary branch (ok=false) without a stripped binary.
//
// GOOS/GOARCH appear in Settings for an ordinary `go build`, but the
// runtime/debug contract does not promise them, so they fall back to the
// runtime constants — which are correct by construction for the process
// doing the reporting. GoVersion falls back to runtime.Version() for the
// same reason. The build FLAGS have no such fallback: when the table is
// unreadable we know nothing about them, and reporting an unknown as
// "false" is the honest rendering (it denies the reproducibility claim
// rather than granting it).
func resolveBuildSettings(bi *debug.BuildInfo, ok bool) buildSettings {
	bs := buildSettings{
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
	if !ok || bi == nil {
		return bs
	}
	if bi.GoVersion != "" {
		bs.GoVersion = bi.GoVersion
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "-trimpath":
			bs.Trimpath = s.Value == "true"
		case "CGO_ENABLED":
			bs.CGOEnabled = s.Value == "1"
		case "GOOS":
			if s.Value != "" {
				bs.OS = s.Value
			}
		case "GOARCH":
			if s.Value != "" {
				bs.Arch = s.Value
			}
		}
	}
	bs.ReproducibleFlags = bs.Trimpath && !bs.CGOEnabled
	return bs
}

// versionResult is the `atlas version --json` payload. Field names are a
// contract: a CI job that checks "the binary I downloaded is the
// reproducible one" reads them, so renaming one is a breaking change to
// that job (docs/architecture.md §6: additive within a major version).
type versionResult struct {
	Version           string `json:"version"`
	Commit            string `json:"commit"`
	BuildDate         string `json:"build_date"`
	GoVersion         string `json:"go_version"`
	OS                string `json:"os"`
	Arch              string `json:"arch"`
	Trimpath          bool   `json:"trimpath"`
	CGOEnabled        bool   `json:"cgo_enabled"`
	ReproducibleFlags bool   `json:"reproducible_flags"`
}

// collectVersionResult assembles the payload from the two independent
// sources: the ldflags/module stamps (who this binary claims to be) and
// the linker's build-settings table (how it was compiled).
func collectVersionResult() versionResult {
	version, commit, builtAt := resolveBuildInfo()
	bi, ok := debug.ReadBuildInfo()
	bs := resolveBuildSettings(bi, ok)
	return versionResult{
		Version:           version,
		Commit:            commit,
		BuildDate:         builtAt,
		GoVersion:         bs.GoVersion,
		OS:                bs.OS,
		Arch:              bs.Arch,
		Trimpath:          bs.Trimpath,
		CGOEnabled:        bs.CGOEnabled,
		ReproducibleFlags: bs.ReproducibleFlags,
	}
}

// newVersionCmd implements `atlas version`.
//
// The root command's `--version` flag already renders the one-line
// "vX.Y.Z (commit ..., built ...)" string. This subcommand exists because
// that line answers "which atlas is this?" and says nothing about "can I
// check that this binary is the one the release claims?" — which is the
// question a supply-chain tool has to be able to answer about itself.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version and build provenance",
		Long: `version prints the release stamps and the build flags the running
binary was compiled with.

trimpath and cgo_enabled are reported because they are the two
preconditions for a byte-identical rebuild: without -trimpath the binary
embeds the absolute path of the checkout, and a cgo build is tied to the
host's C library. Both are also what keeps cross-compilation working.

reproducible_flags means "the preconditions hold", NOT "this binary has
been verified reproducible". The only thing that proves reproducibility
is rebuilding the same commit and comparing digests; docs/install.md
carries that recipe, and CI runs it on every push.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runVersion(cmd)
		},
	}
}

func runVersion(cmd *cobra.Command) error {
	res := collectVersionResult()
	w := stdoutOrJSON(cmd)
	if flags.JSON {
		return emitJSON(w, "version", nil, res, nil)
	}
	fmt.Fprintf(w, "atlas %s\n", res.Version)
	fmt.Fprintf(w, "  commit:      %s\n", res.Commit)
	fmt.Fprintf(w, "  built:       %s\n", res.BuildDate)
	fmt.Fprintf(w, "  go:          %s\n", res.GoVersion)
	fmt.Fprintf(w, "  platform:    %s/%s\n", res.OS, res.Arch)
	fmt.Fprintf(w, "  trimpath:    %t\n", res.Trimpath)
	fmt.Fprintf(w, "  cgo:         %t\n", res.CGOEnabled)
	fmt.Fprintf(w, "  build flags allow a reproducible rebuild: %t\n", res.ReproducibleFlags)
	fmt.Fprintf(w, "  (not a verification — to prove it, rebuild and compare digests: docs/install.md)\n")
	return nil
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
