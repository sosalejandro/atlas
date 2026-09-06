package runner_test

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/coverage/gocover"
	"github.com/sosalejandro/atlas/packages/coverage/shim"
	"github.com/sosalejandro/atlas/packages/coverage/shim/runner"
)

// The acceptance test for the whole shim: a real module, a real `go test`,
// real counter snapshots, real coverprofiles. Everything else in this
// package is a unit test of one step of it.
func TestRun_ProducesPerTestProfiles(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and runs a fixture module")
	}
	res, err := runner.Run(context.Background(), runner.Options{
		Dir:      "../testdata/fixture",
		Packages: []string{"./..."},
		Command:  []string{"go", "test", "./..."},
		Work:     t.TempDir(),
		Stdout:   io.Discard,
		Stderr:   io.Discard,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("fixture suite failed: exit %d", res.ExitCode)
	}

	// billing is sequential, so every test gets its own profile...
	profiles := map[string]string{}
	for _, p := range res.Profiles {
		profiles[p.Package+"."+p.Test] = p.Path
	}
	for _, want := range []string{
		"example.com/covfixture/billing.TestTotal",
		"example.com/covfixture/billing.TestRefund",
	} {
		if _, ok := profiles[want]; !ok {
			t.Fatalf("no profile for %s; got %v", want, keys(profiles))
		}
	}

	// ...and each profile names only what that test ran. TestTotal never
	// enters Refund, and nothing runs Unused.
	total := executedFuncs(t, profiles["example.com/covfixture/billing.TestTotal"])
	refund := executedFuncs(t, profiles["example.com/covfixture/billing.TestRefund"])
	if !total["Total"] || total["Refund"] {
		t.Errorf("TestTotal's profile executed %v, want Total without Refund", total)
	}
	if !refund["Refund"] || !refund["Total"] {
		t.Errorf("TestRefund's profile executed %v, want Refund (which calls Total)", refund)
	}
	if total["Unused"] || refund["Unused"] {
		t.Errorf("a profile claims Unused ran")
	}

	// shipping's tests call t.Parallel(), so it degrades — and says why.
	var shipping *runner.Degradation
	for i := range res.Degraded {
		if res.Degraded[i].Package == "example.com/covfixture/shipping" {
			shipping = &res.Degraded[i]
		}
	}
	if shipping == nil {
		t.Fatalf("shipping did not report a degradation; got %+v", res.Degraded)
	}
	if !strings.Contains(shipping.Reason, "t.Parallel()") {
		t.Errorf("degradation reason = %q, want it to name t.Parallel()", shipping.Reason)
	}
	for _, p := range res.Profiles {
		if p.Package == "example.com/covfixture/shipping" && p.Test != "" {
			t.Errorf("degraded package produced per-test evidence anyway: %+v", p)
		}
	}
	if !hasPackageProfile(res.Profiles, "example.com/covfixture/shipping") {
		t.Errorf("degraded package produced no per-package profile either: %+v", res.Profiles)
	}
}

// --serialize turns the degradation off for packages whose only problem is
// that they asked for concurrency: the attribution is sound because each
// test still owns the counter space, it is the parallelism that is spent.
func TestRun_SerializeCollectsParallelPackagesPerTest(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and runs a fixture module")
	}
	res, err := runner.Run(context.Background(), runner.Options{
		Dir:       "../testdata/fixture",
		Packages:  []string{"./shipping"},
		Command:   []string{"go", "test", "./shipping"},
		Work:      t.TempDir(),
		Serialize: true,
		Stdout:    io.Discard,
		Stderr:    io.Discard,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var got []string
	for _, p := range res.Profiles {
		if p.Package == "example.com/covfixture/shipping" {
			got = append(got, p.Test)
		}
	}
	if len(got) != 2 {
		t.Fatalf("serialised shipping produced %v, want one profile per test", got)
	}
	if len(res.Degraded) != 0 {
		t.Errorf("serialised run still reported degradations: %+v", res.Degraded)
	}
}

// The plan is what the test binary reads, so its contents are a contract.
func TestPlanFor_MarksParallelPackagesPerPackage(t *testing.T) {
	pkgs := []runner.Package{
		{ImportPath: "x/billing", Dir: "../testdata/fixture/billing"},
		{ImportPath: "x/shipping", Dir: "../testdata/fixture/shipping"},
	}
	plan, degraded, err := runner.PlanFor(pkgs, false)
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	if got := plan.For("x/billing").Mode; got != shim.ModePerTest {
		t.Errorf("billing = %q, want per-test", got)
	}
	got := plan.For("x/shipping")
	if got.Mode != shim.ModePerPackage || !strings.Contains(got.Reason, "t.Parallel()") {
		t.Errorf("shipping = %+v, want per-package naming t.Parallel()", got)
	}
	if len(degraded) != 1 || degraded[0].Package != "x/shipping" {
		t.Errorf("degraded = %+v, want just shipping", degraded)
	}
}

// executedFuncs reads a coverprofile and returns the fixture functions with
// at least one executed statement, keyed by the name that appears on the
// line their body starts on.
func executedFuncs(t *testing.T, path string) map[string]bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open profile: %v", err)
	}
	defer func() { _ = f.Close() }()
	blocks, err := gocover.Parse(f)
	if err != nil {
		t.Fatalf("parse profile %s: %v", path, err)
	}
	// The fixture's functions are one per line range; map a block back to
	// its function by the source line it starts on.
	funcs := map[string][2]int{
		"Total":  {6, 11},
		"Refund": {14, 16},
		"Unused": {20, 22},
	}
	out := map[string]bool{}
	for _, b := range blocks {
		if b.Count == 0 || !strings.HasSuffix(b.File, "billing/billing.go") {
			continue
		}
		for name, span := range funcs {
			if b.StartLine >= span[0] && b.StartLine <= span[1] {
				out[name] = true
			}
		}
	}
	return out
}

func hasPackageProfile(ps []runner.Profile, pkg string) bool {
	for _, p := range ps {
		if p.Package == pkg && p.Test == "" {
			return true
		}
	}
	return false
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
