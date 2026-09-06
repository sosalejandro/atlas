package runner

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/sosalejandro/atlas/packages/coverage/shim"
)

// Profile is one coverprofile the run produced.
type Profile struct {
	// Package is the import path the profile came from.
	Package string `json:"package"`
	// Test is the top-level test that produced it, or empty for a
	// package-wide profile (a degraded package's single snapshot).
	Test string `json:"test,omitempty"`
	// Path is the written coverprofile.
	Path string `json:"path"`
}

// counterFilePrefix is what the runtime names a counter-data file. Finding
// one is how a directory is recognised as a snapshot rather than a parent.
const counterFilePrefix = "covcounters."

// Convert turns the counter snapshots under root into coverprofiles under
// outDir, one file per snapshot, via `go tool covdata`.
//
// The reports decide what may be converted, not the directory tree: a
// package that degraded mid-run can leave per-test directories behind that
// its own report has already invalidated, and converting those would
// resurrect exactly the wrong attribution the degradation exists to avoid.
//
// Returns the profiles (sorted, so a run is reproducible) and warnings for
// evidence that was expected and is not there.
func Convert(ctx context.Context, root, outDir string, reports []shim.Report) ([]Profile, []string, error) {
	profiles, warnings, err := findSnapshots(root, outDir, reports)
	if err != nil {
		return nil, nil, err
	}

	// One covdata process per snapshot, run concurrently: on a suite of a
	// thousand tests the conversion is two orders of magnitude more
	// expensive than the suite itself, and it is a thousand independent
	// spawns. The results are filed by index so the output order stays the
	// sorted one whatever order they finish in.
	failures := make([]error, len(profiles))
	limit := make(chan struct{}, runtime.NumCPU())
	var wg sync.WaitGroup
	for i, p := range profiles {
		counters := shim.CounterDir(root, p.Package, p.Test)
		if p.Test == "" {
			counters = shim.PackageCounterDir(root, p.Package)
		}
		wg.Add(1)
		go func(i int, meta, counters, out string) {
			defer wg.Done()
			limit <- struct{}{}
			defer func() { <-limit }()
			failures[i] = textfmt(ctx, meta, counters, out)
		}(i, shim.MetaDir(root, p.Package), counters, p.Path)
	}
	wg.Wait()

	out := make([]Profile, 0, len(profiles))
	for i, p := range profiles {
		if failures[i] != nil {
			warnings = append(warnings, failures[i].Error())
			continue
		}
		out = append(out, p)
	}
	warnings = append(warnings, missingSnapshots(root, reports)...)
	return out, warnings, nil
}

// findSnapshots walks the counter tree and returns the profiles that may be
// produced from it, sorted, plus warnings about what it declined to use.
//
// The reports decide, not the tree: a package that degraded mid-run leaves
// per-test directories its own report has already invalidated.
func findSnapshots(root, outDir string, reports []shim.Report) ([]Profile, []string, error) {
	byPkg := make(map[string]shim.Report, len(reports))
	for _, r := range reports {
		byPkg[r.Package] = r
	}

	countersRoot := shim.CountersRoot(root)
	var (
		profiles []Profile
		warnings []string
		seen     = map[string]bool{}
	)
	err := filepath.WalkDir(countersRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasPrefix(d.Name(), counterFilePrefix) {
			return nil
		}
		dir := filepath.Dir(path)
		if seen[dir] {
			return nil
		}
		seen[dir] = true

		pkg, test, ok := shim.SplitCounterDir(root, dir)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("ignoring unrecognised counter directory %s", dir))
			return nil
		}
		if test == shim.PackageCounterName {
			test = ""
		} else if rep, have := byPkg[pkg]; have && rep.Mode != shim.ModePerTest {
			return nil
		}
		profiles = append(profiles, Profile{Package: pkg, Test: test, Path: profilePath(outDir, pkg, test)})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("runner: walk %s: %w", countersRoot, err)
	}

	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].Package != profiles[j].Package {
			return profiles[i].Package < profiles[j].Package
		}
		return profiles[i].Test < profiles[j].Test
	})
	return profiles, warnings, nil
}

// profilePath mirrors the counter layout so a profile can always be traced
// back to the snapshot it came from.
func profilePath(outDir, pkg, test string) string {
	name := test
	if name == "" {
		name = shim.PackageCounterName
	}
	return filepath.Join(outDir, filepath.FromSlash(pkg), name+".out")
}

// textfmt runs `go tool covdata textfmt`, which is the only supported way to
// turn counter data into a coverprofile: the binary format is internal and
// versioned with the toolchain that wrote it.
func textfmt(ctx context.Context, metaDir, counterDir, out string) error {
	if _, err := os.Stat(metaDir); err != nil {
		return fmt.Errorf("no coverage meta-data at %s (was the suite built with -cover?)", metaDir)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return fmt.Errorf("create profile dir: %w", err)
	}
	cmd := exec.CommandContext(ctx, "go", "tool", "covdata", "textfmt",
		"-i="+metaDir+","+counterDir, "-o="+out)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("covdata textfmt %s: %w: %s", counterDir, err, strings.TrimSpace(string(b)))
	}
	return nil
}

// missingSnapshots names the tests a report claims were collected but whose
// counter directory is not on disk — a test that crashed the process, or a
// snapshot the runtime refused to write. Silence there would read as "this
// test executed nothing", which is the one thing it does not mean.
func missingSnapshots(root string, reports []shim.Report) []string {
	var out []string
	for _, r := range reports {
		for _, e := range r.Errors {
			out = append(out, fmt.Sprintf("%s: %s", r.Package, e))
		}
		if r.Mode != shim.ModePerTest {
			continue
		}
		for _, test := range r.Tests {
			if _, err := os.Stat(shim.CounterDir(root, r.Package, test)); err != nil {
				out = append(out, fmt.Sprintf("%s: no counter snapshot for %s", r.Package, test))
			}
		}
	}
	return out
}
