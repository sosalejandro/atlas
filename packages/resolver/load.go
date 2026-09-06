package resolver

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/tools/go/packages"
)

// loadMode is the smallest mode that answers every question this package
// asks. NeedDeps is absent on purpose: dependencies are type-checked from
// export data, which is complete for our purposes and roughly seven times
// cheaper on this repository (see doc.go).
const loadMode = packages.NeedName |
	packages.NeedFiles |
	packages.NeedCompiledGoFiles |
	packages.NeedImports |
	packages.NeedTypes |
	packages.NeedSyntax |
	packages.NeedTypesInfo

// Options configures Load.
type Options struct {
	// IncludeTests loads each package's test variants as well, so
	// declarations in _test.go files are type-checked. It mirrors
	// goscan.Options.SkipTests inverted, because the scanner indexes
	// test files by default and a resolver that could not see them
	// would silently degrade every test symbol to name matching.
	IncludeTests bool

	// MaxDegradedReported caps how many failing packages Status lists
	// individually. Zero means the built-in cap. The total count is
	// always exact; only the per-package detail is capped, so a repo
	// where nothing compiles reports one honest number instead of
	// thousands of lines.
	MaxDegradedReported int
}

const defaultMaxDegradedReported = 20

// LoadError is the failure of the load itself -- no module, no toolchain,
// an unreadable tree -- as opposed to a tree that loaded and does not
// compile.
//
// It exists rather than a plain fmt.Errorf so the message can be made
// repo-relative while the underlying error stays reachable through
// errors.Is / errors.As. `go list` reports absolute paths, and this
// message is carried onto a scan Result that is serialised to JSON and
// compared in golden files.
type LoadError struct {
	msg string
	err error
}

func (e *LoadError) Error() string { return e.msg }

// Unwrap exposes the go/packages error underneath.
func (e *LoadError) Unwrap() error { return e.err }

// PackageStatus names one package that could not be type-checked, with
// the first error the type checker reported for it. The first error is
// the useful one: the rest are usually consequences of it.
type PackageStatus struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// Status is what Load managed to do, in numbers a scan can report.
//
// It exists because "type-checked resolution" is not a yes/no property of
// a repository. A monorepo mid-refactor type-checks most of itself and
// fails two packages, and the honest answer for a call graph over it is
// "these edges are typed, those are guesses" — which requires counting
// both.
type Status struct {
	Packages     int             `json:"packages"`
	TypeChecked  int             `json:"type_checked"`
	Degraded     int             `json:"degraded"`
	DegradedList []PackageStatus `json:"degraded_list,omitempty"`

	// LoadErrors are failures to enumerate packages at all, as opposed
	// to packages that were found and did not compile. A directory
	// outside any module produces one of these and no packages, which is
	// a different fact from "every package here is broken" and reads
	// differently in a report.
	LoadErrors []string `json:"load_errors,omitempty"`

	// Files is the number of source files this program can answer for.
	Files int `json:"files"`

	// CallGraph reports whether class-hierarchy analysis ran. When it is
	// false, static calls still resolve exactly; only interface dispatch
	// degrades to "no answer".
	CallGraph bool `json:"call_graph"`

	// InvokeSites is the number of distinct interface call sites CHA
	// resolved to at least one concrete method.
	InvokeSites int `json:"invoke_sites"`

	LoadDuration      time.Duration `json:"load_duration"`
	CallGraphDuration time.Duration `json:"call_graph_duration"`
}

// Program is a type-checked view of one source tree.
//
// Files are addressed two ways because the two callers address them
// differently: by absolute path while walking the filesystem, and by
// *ast.File pointer once the scanner has adopted the type-checked syntax
// tree. Both indexes point at the same view, so a call resolved through
// one is identical to a call resolved through the other.
type Program struct {
	fset     *token.FileSet
	byPath   map[string]*fileView
	bySyntax map[*ast.File]*fileView
	invokes  map[token.Pos][]*types.Func
	status   Status
}

// fileView is one type-checked file and the package that checked it.
type fileView struct {
	syntax *ast.File
	pkg    *packages.Package
}

// Load type-checks the tree rooted at dir.
//
// It returns an error only when nothing could be loaded at all — no
// module, no toolchain, an unreadable directory. A tree that merely fails
// to COMPILE is not an error here: those packages land on Status.Degraded
// and their files are absent from the returned Program, which is the
// signal the caller uses to fall back per file rather than per repo.
func Load(ctx context.Context, dir string, opts Options) (*Program, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolver: abs %q: %w", dir, err)
	}

	cfg := &packages.Config{
		Mode:    loadMode,
		Dir:     abs,
		Tests:   opts.IncludeTests,
		Context: ctx,
		Env:     loadEnv(abs),
	}

	start := time.Now()
	pkgs, err := packages.Load(cfg, loadPatterns(abs)...)
	if err != nil {
		return nil, &LoadError{msg: "resolver: " + relativise(err.Error(), abs), err: err}
	}
	loadDur := time.Since(start)

	p := &Program{
		byPath:   make(map[string]*fileView),
		bySyntax: make(map[*ast.File]*fileView),
		invokes:  make(map[token.Pos][]*types.Func),
	}
	p.status.LoadDuration = loadDur

	checked := p.accept(pkgs, opts, abs)
	p.indexFiles(checked, abs)
	if len(checked) > 0 {
		p.fset = checked[0].Fset
		p.buildCallGraph(checked)
	}
	return p, nil
}

// loadEnv pins GOWORK off when dir is itself a module root.
//
// Without this, a go.work file anywhere ABOVE dir silently takes over the
// load and fails with "directory not in workspace" — which is exactly what
// happens to a fixture module checked in under an outer repository's
// testdata/. When dir carries its own go.work the workspace is the point,
// so the variable is left alone.
func loadEnv(dir string) []string {
	if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		return nil
	}
	return append(os.Environ(), "GOWORK=off")
}

// loadPatterns is "./..." except in a workspace, where it is one pattern
// per used module: `go list ./...` in a go.work directory lists only the
// modules' union in newer toolchains and nothing in older ones, and a
// monorepo whose modules silently went unloaded is worse than a slow scan.
func loadPatterns(dir string) []string {
	mods := workspaceModules(dir)
	if len(mods) == 0 {
		return []string{"./..."}
	}
	patterns := make([]string, 0, len(mods))
	for _, m := range mods {
		patterns = append(patterns, "./"+m+"/...")
	}
	return patterns
}

// workspaceModules reads the `use` directives out of dir/go.work. It is a
// line reader rather than a golang.org/x/mod/modfile parse because the
// only thing needed is the directory list, and a malformed go.work should
// degrade this to "no workspace" rather than fail the scan.
func workspaceModules(dir string) []string {
	data, err := os.ReadFile(filepath.Join(dir, "go.work"))
	if err != nil {
		return nil
	}
	var mods []string
	inBlock := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "use (":
			inBlock = true
		case inBlock && line == ")":
			inBlock = false
		case inBlock && line != "" && !strings.HasPrefix(line, "//"):
			mods = append(mods, normaliseUse(line))
		case strings.HasPrefix(line, "use "):
			mods = append(mods, normaliseUse(strings.TrimPrefix(line, "use ")))
		}
	}
	sort.Strings(mods)
	return mods
}

func normaliseUse(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"`)
	s = strings.TrimPrefix(s, "./")
	return strings.TrimSuffix(s, "/")
}

// accept partitions the loaded packages into the ones whose conclusions
// can be trusted and the ones that failed, and records the split.
//
// The bar is deliberately all-or-nothing per package: any error at all
// disqualifies it. A package that half type-checks still populates
// types.Info for the half that worked, and using it would produce edges
// indistinguishable from the exact ones — a typed-looking answer derived
// from a broken build is the one outcome worth more than a missing edge.
func (p *Program) accept(pkgs []*packages.Package, opts Options, root string) []*packages.Package {
	limit := opts.MaxDegradedReported
	if limit <= 0 {
		limit = defaultMaxDegradedReported
	}

	sorted := make([]*packages.Package, 0, len(pkgs))
	for _, pkg := range pkgs {
		if isSyntheticTestMain(pkg) {
			continue
		}
		sorted = append(sorted, pkg)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	checked := make([]*packages.Package, 0, len(sorted))
	for _, pkg := range sorted {
		if isPatternError(pkg) {
			p.status.LoadErrors = append(p.status.LoadErrors, relativise(pkg.Errors[0].Error(), root))
			continue
		}
		p.status.Packages++
		if err := packageError(pkg); err != "" {
			p.status.Degraded++
			if len(p.status.DegradedList) < limit {
				p.status.DegradedList = append(p.status.DegradedList,
					PackageStatus{Path: pkg.PkgPath, Error: relativise(err, root)})
			}
			continue
		}
		p.status.TypeChecked++
		checked = append(checked, pkg)
	}
	return checked
}

// isPatternError reports whether pkg is not a package at all but the
// go/packages placeholder for a pattern that could not be resolved --
// "directory prefix . does not contain main module", a typo'd module in
// go.work, an unreadable directory.
//
// It matters because the two failures need different words. "This
// package does not compile" is a fact about the code and the right
// answer is to fall back for its files. "There is no module here" is a
// fact about the environment, and reporting it as a broken package would
// tell an operator to go fix source that is perfectly fine.
func isPatternError(pkg *packages.Package) bool {
	// A package go list actually found always carries at least one Go
	// file, even when every one of them fails to type-check. No files at
	// all means the pattern never resolved to a package.
	return len(pkg.Errors) > 0 &&
		len(pkg.GoFiles) == 0 &&
		len(pkg.CompiledGoFiles) == 0 &&
		len(pkg.Syntax) == 0
}

// relativise strips the scan root out of a type-checker message.
//
// Every position the go tool reports is absolute, and these messages are
// carried on a scan Result that ends up in JSON output, in a store and in
// golden files. An absolute path there makes two scans of the same commit
// from two checkouts disagree, which docs/testing/determinism.md forbids
// -- and leaks the operator's home directory into whatever the report is
// pasted into.
func relativise(msg, root string) string {
	if root == "" {
		return msg
	}
	msg = strings.ReplaceAll(msg, root+string(filepath.Separator), "")
	if resolved, err := filepath.EvalSymlinks(root); err == nil && resolved != root {
		msg = strings.ReplaceAll(msg, resolved+string(filepath.Separator), "")
	}
	return msg
}

// isSyntheticTestMain drops the `pkg.test` main package the go tool
// synthesises for `go test`. Its only file is generated into a temporary
// directory that no scan will ever walk, so indexing it would attribute
// call edges to a file that does not exist in the repository.
func isSyntheticTestMain(pkg *packages.Package) bool {
	return strings.HasSuffix(pkg.PkgPath, ".test")
}

// packageError returns the reason pkg cannot be trusted, or "".
//
// A failing package usually carries several errors saying the same thing
// in different registers: the type checker's diagnostic, and the go
// tool's replay of the compiler output that contains it. The type
// checker's is the one with a position and a specific message, so it is
// preferred; the list error is the fallback for a package that failed
// before type checking started (a missing import, a bad build tag).
func packageError(pkg *packages.Package) string {
	if len(pkg.Errors) > 0 {
		return tidyError(pickError(pkg.Errors))
	}
	if pkg.Types == nil || pkg.TypesInfo == nil {
		return "no type information"
	}
	if len(pkg.Syntax) == 0 {
		return "no syntax"
	}
	return ""
}

func pickError(errs []packages.Error) packages.Error {
	for _, e := range errs {
		if e.Kind == packages.TypeError {
			return e
		}
	}
	return errs[0]
}

// tidyError flattens a go-tool error into one line.
//
// The go tool replays compiler output verbatim, so a single Error can be
// several lines long and lead with a "# <package path>" banner. Left
// alone it wraps raggedly under an indented report and, worse, its first
// line names only the package -- which the report has already printed --
// while the actual diagnostic hides on line two.
func tidyError(e packages.Error) string {
	msg := e.Msg
	if msg == "" {
		msg = e.Error()
	}
	lines := make([]string, 0, 2)
	for _, line := range strings.Split(msg, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	msg = strings.Join(lines, "; ")
	// A position of "-" means "no position", which the go tool prints as
	// a leading "-: ". Carrying it into the report would suggest a file
	// called "-".
	if e.Pos != "" && e.Pos != "-" {
		return e.Pos + ": " + msg
	}
	return msg
}

// indexFiles maps every type-checked file to the package that checked it.
//
// One file can appear in several packages: `go list -test` reports a
// package both plainly and as a test variant that additionally compiles
// its _test.go files. Both check the same declarations, so either would
// answer correctly, but the choice has to be STABLE or two scans of one
// tree could bind the same call through different type-checker runs. The
// variant that sees more of the package wins, ties broken by package ID.
func (p *Program) indexFiles(pkgs []*packages.Package, root string) {
	// Two passes. The first settles WHICH package owns each file; only
	// then is the by-syntax index built, from the winners. Building both
	// in one pass would leave the loser's *ast.File in bySyntax pointing
	// at a package that no longer owns it -- harmless for lookups the
	// scanner performs, but it would make Status.Files count the same
	// source file twice, and a "files resolved with types" number that
	// exceeds the files that exist is worse than no number.
	for _, pkg := range pkgs {
		for _, syntax := range pkg.Syntax {
			path := pkg.Fset.Position(syntax.Pos()).Filename
			if path == "" {
				continue
			}
			if incumbent, ok := p.byPath[path]; ok && !prefer(pkg, incumbent.pkg) {
				continue
			}
			p.byPath[path] = &fileView{syntax: syntax, pkg: pkg}
		}
	}
	p.status.Files = len(p.byPath)

	alias := symlinkAlias(root)
	aliases := map[string]*fileView{}
	for path, view := range p.byPath {
		p.bySyntax[view.syntax] = view
		if a := alias(path); a != "" {
			aliases[a] = view
		}
	}
	for a, view := range aliases {
		p.byPath[a] = view
	}
}

// prefer reports whether candidate should replace incumbent as the owner
// of a file they both compile.
func prefer(candidate, incumbent *packages.Package) bool {
	if len(candidate.Syntax) != len(incumbent.Syntax) {
		return len(candidate.Syntax) > len(incumbent.Syntax)
	}
	return candidate.ID < incumbent.ID
}

// symlinkAlias returns a function that rewrites a file path reported by
// the go tool into the equivalent path under the caller's spelling of the
// root, or "" when the two spellings agree.
//
// The go tool resolves symlinks in the module root; filepath.WalkDir does
// not. On a machine where the scan root reaches through a symlink — a
// macOS temp directory, a /home that is really /export/home — every
// lookup by absolute path would miss, the whole tree would silently
// degrade to name matching, and the only symptom would be a tier
// histogram nobody was watching.
func symlinkAlias(root string) func(string) string {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved == root {
		return func(string) string { return "" }
	}
	return func(path string) string {
		rel, err := filepath.Rel(resolved, path)
		if err != nil || strings.HasPrefix(rel, "..") {
			return ""
		}
		return filepath.Join(root, rel)
	}
}

// Status reports what the load achieved.
func (p *Program) Status() Status { return p.status }

// Fset is the file set every position in this program is relative to.
func (p *Program) Fset() *token.FileSet { return p.fset }
