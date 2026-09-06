package affected

import (
	"path"
	"strings"
)

// fileClass is how the selector treats one changed path.
type fileClass int

const (
	// classSource is a path whose symbols the selector can map the change onto.
	classSource fileClass = iota

	// classBuildConfig is a dependency or build/CI input. A version bump in
	// go.mod changes the code every package compiles against, and none of it
	// shows up as an edited line in any indexed file.
	classBuildConfig

	// classTestInfra is shared test scaffolding. Coverage records the
	// PRODUCTION symbols a test executed and nothing about the helpers it
	// called, so there is no evidence connecting a fixture to its consumers.
	classTestInfra

	// classInert is a path that cannot change program behaviour. The set is
	// deliberately tiny and enumerated below rather than expressed as a broad
	// "ignore" glob: every entry is a promise that editing such a file cannot
	// break a test, and that promise should be auditable at a glance.
	classInert
)

// buildConfigBases are the exact file names that force a full run. Matching on
// the base name rather than a prefix keeps `internal/tools/makefile_test.go`
// out of the set.
var buildConfigBases = map[string]bool{
	"go.mod": true, "go.sum": true, "go.work": true, "go.work.sum": true,
	"Makefile": true, "makefile": true, "GNUmakefile": true,
	"Dockerfile": true, "docker-compose.yml": true, "docker-compose.yaml": true,
	".golangci.yml": true, ".golangci.yaml": true,
	"package.json": true, "package-lock.json": true, "pnpm-lock.yaml": true, "yarn.lock": true,
	"tsconfig.json":  true,
	"pyproject.toml": true, "poetry.lock": true, "requirements.txt": true,
	".gitlab-ci.yml": true, ".tool-versions": true,
}

// testInfraDirs are directory names whose contents are test scaffolding rather
// than tests. "testdata" is the Go toolchain's own convention; the rest are
// the conventional names for a shared helper package.
var testInfraDirs = map[string]bool{
	"testdata":    true,
	"testutil":    true,
	"testutils":   true,
	"testhelper":  true,
	"testhelpers": true,
	"testfixture": true,
}

// inertExts / inertBases enumerate what atlas is willing to ignore outright.
// Prose and images have no compiled representation, so no test outcome can
// depend on them.
var inertExts = map[string]bool{
	".md": true, ".markdown": true, ".rst": true, ".txt": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true, ".ico": true,
}

var inertBases = map[string]bool{
	"LICENSE": true, "LICENCE": true, "NOTICE": true, "COPYING": true,
	"CODEOWNERS": true, ".gitignore": true, ".gitattributes": true,
}

// classify decides how a changed path is treated.
//
// Order matters and is chosen so the conservative rules win: a markdown file
// under .github/ is a CI input before it is prose, and anything under a
// testdata directory is scaffolding whatever its extension.
func classify(p string) fileClass {
	base := path.Base(p)
	segments := strings.Split(p, "/")

	if buildConfigBases[base] || strings.HasPrefix(p, ".github/") || strings.HasPrefix(base, "Dockerfile.") {
		return classBuildConfig
	}
	if base == "conftest.py" {
		return classTestInfra
	}
	for _, seg := range segments[:max(len(segments)-1, 0)] {
		if testInfraDirs[seg] {
			return classTestInfra
		}
	}
	if inertBases[base] || inertExts[strings.ToLower(path.Ext(base))] {
		return classInert
	}
	return classSource
}

// classifyDetail is the human sentence that goes with a fallback class. It
// says what atlas cannot rule out, not what the file is — the reader already
// has the path.
func classifyDetail(c fileClass) (reason, detail string) {
	switch c {
	case classBuildConfig:
		return ReasonBuildConfig, "a dependency or build input changed; it can alter the behaviour of any package, and no symbol-level edit records that"
	case classTestInfra:
		return ReasonTestInfra, "shared test scaffolding changed; coverage records which production symbols a test ran, never which helpers it called, so its consumers are unknown"
	case classSource, classInert:
		return "", ""
	default:
		return "", ""
	}
}
