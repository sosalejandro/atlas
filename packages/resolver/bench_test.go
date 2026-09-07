package resolver

import (
	"context"
	"fmt"
	"go/token"
	"go/types"
	"os"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// selfRepo is the atlas checkout these benchmarks run on. Issue #152's
// numbers were taken on this tree, and a resolver benchmark on a synthetic
// fixture would price a different program: the cost being measured here is
// dominated by how many packages import each other, which the golden
// corpus (four packages) does not have.
const selfRepo = "../.."

// reportPeakRSS attaches this process's resident high-water mark to the
// benchmark line.
//
// It is here because issue #152 exists partly to stop B/op being read as
// memory usage. B/op is CUMULATIVE ALLOCATION over the run: every byte
// the allocator ever handed out, including everything the GC took back.
// VmHWM is RESIDENT PEAK: the most the kernel ever had mapped in at one
// instant. On a resolver load the first is several times the second, and
// which one a reader means changes the answer to "does this fit in CI".
//
// The number includes the test binary itself and anything a previous
// benchmark in the same process left resident, so it is only meaningful
// with -benchtime 1x -count 1 and one benchmark selected. Linux only;
// elsewhere it reports 0 rather than guessing.
func reportPeakRSS(b *testing.B) {
	b.Helper()
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		b.ReportMetric(0, "peakRSS_MB")
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(line, "VmHWM:")
		if !ok {
			continue
		}
		kb, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "kB")))
		if err != nil {
			break
		}
		b.ReportMetric(float64(kb)/1024, "peakRSS_MB")
		return
	}
	b.ReportMetric(0, "peakRSS_MB")
}

// BenchmarkLoad prices one whole resolver load of this repository: the
// go/packages load, type checking, SSA construction and CHA.
//
// Run it as
//
//	go test ./packages/resolver -run NONE -bench BenchmarkLoad$ \
//	  -benchtime 1x -count 3 -benchmem
//
// and take the MEDIAN. A single sample of this benchmark is worth very
// little — during issue #152 one sample reported an 18% regression that
// three samples showed did not exist.
func BenchmarkLoad(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		p, err := Load(ctx, selfRepo, Options{IncludeTests: true})
		if err != nil {
			b.Fatal(err)
		}
		if !p.Status().CallGraph {
			b.Fatal("no call graph; the measurement would be of a different program")
		}
	}
	b.StopTimer()
	reportPeakRSS(b)
}

// BenchmarkPackagesLoad prices everything Load does BEFORE
// buildCallGraph: `go list`, parsing, type checking and types.Info. The
// gap between it and BenchmarkLoad is what SSA construction and CHA cost,
// which is the figure issue #152 cause 3 is arguing about.
//
// It duplicates two lines of Load rather than adding a knob to Options,
// because a production flag that exists only so a benchmark can subtract
// would be a worse thing to own than a duplicated Config literal.
func BenchmarkPackagesLoad(b *testing.B) {
	cfg := &packages.Config{
		Mode:    loadMode,
		Dir:     selfRepo,
		Tests:   true,
		Context: context.Background(),
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := packages.Load(cfg, "./..."); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	reportPeakRSS(b)
}

// BenchmarkCallGraphScope is the census behind issue #152 cause 3. It is
// a benchmark rather than a test because it loads and SSA-builds this
// whole repository, which `go test ./...` runs under -race in CI and
// should not be made to pay for.
//
// Run it as
//
//	go test ./packages/resolver -run NONE -bench BenchmarkCallGraphScope \
//	  -benchtime 1x -count 3 -benchmem
//
// The metrics answer the two questions the issue left open.
//
// bodiesOutside is the number of SSA function bodies built for packages
// atlas does not index. It is 0: ssautil.Packages hands syntax only to
// the packages it was given, so a dependency becomes an ssa.Package of
// declarations with no code. The ~99 MB the issue attributed to
// dependency bodies is the scanned tree's own bodies.
//
// The dedup arm prices the one duplication that IS left. With
// IncludeTests, a package with in-package tests is loaded twice — plainly
// and as its test variant — and roughly half this repository's files are
// SSA-built under both. Dropping the plain variant is the obvious saving
// and it is not available: the arm reports how many interface call sites
// survive it. The two variants are distinct types.Packages, so a concrete
// type from the test variant does not satisfy an interface declared in
// the plain one; and a package created without syntax contributes no
// roots to ssautil.AllFunctions and materialises no runtime types, so CHA
// never learns what its types implement.
func BenchmarkCallGraphScope(b *testing.B) {
	checked := loadForRecheck(b)

	b.Run("all", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			census(b, checked, checked)
		}
	})
	b.Run("dedup-test-variants", func(b *testing.B) {
		kept := withoutCoveredVariants(checked)
		b.ReportAllocs()
		for range b.N {
			census(b, kept, checked)
		}
	})
}

// census builds SSA and CHA over input and reports what came out. all is
// the full package list, used only to count how much of the tree input
// covers.
func census(b *testing.B, input, all []*packages.Package) {
	b.Helper()
	scanned := map[*types.Package]bool{}
	for _, pkg := range input {
		scanned[pkg.Types] = true
	}

	prog, _ := ssautil.Packages(input, ssa.BuilderMode(0))
	prog.Build()

	shells := 0
	for _, ssaPkg := range prog.AllPackages() {
		if !scanned[ssaPkg.Pkg] {
			shells++
		}
	}
	bodies, outside := 0, 0
	for fn := range ssautil.AllFunctions(prog) {
		if len(fn.Blocks) == 0 || fn.Pkg == nil {
			continue
		}
		bodies++
		if !scanned[fn.Pkg.Pkg] {
			outside++
		}
	}

	sites := map[token.Pos]bool{}
	for _, node := range cha.CallGraph(prog).Nodes {
		for _, edge := range node.Out {
			if pos, _, ok := invokeTarget(edge); ok {
				sites[pos] = true
			}
		}
	}

	b.StopTimer()
	b.ReportMetric(float64(len(input)), "pkgsIn")
	b.ReportMetric(float64(len(prog.AllPackages())), "ssaPkgs")
	b.ReportMetric(float64(shells), "shells")
	b.ReportMetric(float64(bodies), "bodies")
	b.ReportMetric(float64(outside), "bodiesOutside")
	compiled, twice := fileCensus(all)
	b.ReportMetric(float64(compiled), "filesCompiled")
	b.ReportMetric(float64(twice), "filesBuiltTwice")
	b.ReportMetric(float64(len(sites)), "invokeSites")
	b.StartTimer()
}

// fileCensus returns how many distinct source files the loaded packages
// compile, and how many of those are compiled into more than one of them —
// in practice, the files a package shares with its own test variant, which
// are SSA-built once per variant.
func fileCensus(pkgs []*packages.Package) (compiled, twice int) {
	count := map[string]int{}
	for _, pkg := range pkgs {
		for _, f := range pkg.CompiledGoFiles {
			count[f]++
		}
	}
	for _, n := range count {
		if n > 1 {
			twice++
		}
	}
	return len(count), twice
}

// withoutCoveredVariants drops every package whose compiled file set is a
// strict subset of another's — exactly the plain package when its
// in-package test variant is also loaded.
func withoutCoveredVariants(pkgs []*packages.Package) []*packages.Package {
	files := make([]map[string]bool, len(pkgs))
	for i, pkg := range pkgs {
		set := make(map[string]bool, len(pkg.CompiledGoFiles))
		for _, f := range pkg.CompiledGoFiles {
			set[f] = true
		}
		files[i] = set
	}
	kept := make([]*packages.Package, 0, len(pkgs))
	for i := range pkgs {
		if !coveredBy(files, i) {
			kept = append(kept, pkgs[i])
		}
	}
	return kept
}

func coveredBy(files []map[string]bool, i int) bool {
	for j := range files {
		if j == i || len(files[j]) <= len(files[i]) {
			continue
		}
		subset := true
		for f := range files[i] {
			if !files[j][f] {
				subset = false
				break
			}
		}
		if subset {
			return true
		}
	}
	return false
}

// BenchmarkTypeCheckInfoFields is the prototype issue #152 cause 2 asked
// for, kept as a benchmark because the finding it produced is a price, not
// a bug.
//
// The claim under test: go/types.(*Checker).recordTypeAndValue was the
// largest single allocator in a scan (100.22 MB, 12.5%) filling
// types.Info.Types, which atlas never reads. Both sub-benchmarks
// type-check the same syntax with the same checker and differ only in
// whether Types is allocated, so the difference between them is exactly
// what leaving it nil would save.
//
// What it does NOT show is a saving that can be taken:
// TestSSA_RequiresTypesInfoTypes shows go/ssa reads the map, so the
// without-Types arm produces a program the call graph cannot be built
// over. Two further costs are invisible here and matter at least as much
// as the bytes. Driving types.Config.Check directly means reimplementing
// what packages.Load does with per-package errors — the degradation path
// issue #87 built, which is what lets atlas run mid-edit — and it gives
// up go/packages' export-data caching, which is what makes the load 0.5 s
// instead of 3.6 s (see doc.go).
func BenchmarkTypeCheckInfoFields(b *testing.B) {
	pkgs := loadForRecheck(b)

	b.Run("all-fields", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			recheck(b, pkgs, fullInfo)
		}
	})
	b.Run("without-Types", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			recheck(b, pkgs, func() *types.Info {
				info := fullInfo()
				info.Types = nil
				return info
			})
		}
	})
}

// loadForRecheck loads the tree once, outside the timed region, so the
// benchmark prices type checking rather than `go list`.
func loadForRecheck(b *testing.B) []*packages.Package {
	b.Helper()
	cfg := &packages.Config{
		Mode:    loadMode,
		Dir:     selfRepo,
		Tests:   true,
		Context: context.Background(),
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		b.Fatal(err)
	}
	var out []*packages.Package
	for _, pkg := range pkgs {
		if isSyntheticTestMain(pkg) || packageError(pkg) != "" {
			continue
		}
		out = append(out, pkg)
	}
	if len(out) == 0 {
		b.Fatal("nothing type-checked; the benchmark would measure nothing")
	}
	return out
}

// recheck type-checks every package's syntax again into a fresh Info.
//
// Imports are served from the types the first load already produced, so
// the arm being measured is the checker's own work on this tree and not a
// second trip through export data.
func recheck(b *testing.B, pkgs []*packages.Package, newInfo func() *types.Info) {
	b.Helper()
	for _, pkg := range pkgs {
		imports := map[string]*types.Package{}
		for path, dep := range pkg.Imports {
			if dep.Types != nil {
				imports[path] = dep.Types
			}
		}
		cfg := &types.Config{
			// Errors are swallowed: a re-check of a package whose
			// import graph is served from a map cannot be held to the
			// same standard as the real load, and the point is the
			// allocation profile, not the diagnostics.
			Error: func(error) {},
			Importer: importerFunc(func(path string) (*types.Package, error) {
				if p, ok := imports[path]; ok {
					return p, nil
				}
				return nil, fmt.Errorf("resolver bench: %q not preloaded", path)
			}),
		}
		target := types.NewPackage(pkg.PkgPath, pkg.Name)
		_ = types.NewChecker(cfg, pkg.Fset, target, newInfo()).Files(pkg.Syntax)
	}
}

type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }
