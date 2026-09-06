package shim_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/atlas/packages/coverage/shim"
)

// The layout is a contract between two processes that never talk: the shim
// inside the test binary writes it, the CLI walking the tree reads it back.
// Pin both directions on the same table so a change to one has to break the
// other's test too.
func TestLayoutRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		pkg  string
		test string
		dir  func(root, pkg, test string) string
	}{
		{
			name: "per-test counter dir",
			pkg:  "github.com/example/repo/billing",
			test: "TestCheckout",
			dir:  func(root, pkg, test string) string { return shim.CounterDir(root, pkg, test) },
		},
		{
			name: "per-package counter dir",
			pkg:  "github.com/example/repo/billing",
			test: shim.PackageCounterName,
			dir:  func(root, pkg, _ string) string { return shim.PackageCounterDir(root, pkg) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.dir("/root", tc.pkg, tc.test)
			pkg, test, ok := shim.SplitCounterDir("/root", dir)
			if !ok {
				t.Fatalf("SplitCounterDir(%q) not recognised as a counter dir", dir)
			}
			if pkg != tc.pkg || test != tc.test {
				t.Fatalf("round trip = (%q, %q), want (%q, %q)", pkg, test, tc.pkg, tc.test)
			}
		})
	}
}

func TestSplitCounterDir_RejectsForeignPaths(t *testing.T) {
	for _, dir := range []string{
		"/root/meta/github.com/example/repo/billing",
		"/root/reports/github.com/example/repo/billing",
		"/elsewhere/counters/pkg/TestX",
		"/root/counters/TestX", // no package segment
	} {
		if _, _, ok := shim.SplitCounterDir("/root", dir); ok {
			t.Fatalf("SplitCounterDir(%q) = ok, want rejected", dir)
		}
	}
}

func TestPlanFor_FallsBackToTheDefaultMode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "plan.json")
	want := shim.Plan{
		Version:     1,
		DefaultMode: shim.ModePerTest,
		Packages: map[string]shim.PackagePlan{
			"github.com/example/repo/racy": {Mode: shim.ModePerPackage, Reason: "2 tests call t.Parallel()"},
		},
	}
	if err := shim.WritePlan(path, want); err != nil {
		t.Fatalf("WritePlan: %v", err)
	}
	got, err := shim.LoadPlan(path)
	if err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}

	racy := got.For("github.com/example/repo/racy")
	if racy.Mode != shim.ModePerPackage || racy.Reason == "" {
		t.Fatalf("For(racy) = %+v, want per-package with a reason", racy)
	}
	if other := got.For("github.com/example/repo/billing"); other.Mode != shim.ModePerTest {
		t.Fatalf("For(unlisted) = %+v, want the default mode", other)
	}
}

// A missing plan is not an error: the shim is usable with nothing but
// ATLAS_COV_DIR set, and then every package is per-test.
func TestLoadPlan_MissingFileIsPerTest(t *testing.T) {
	got, err := shim.LoadPlan(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("LoadPlan(absent): %v", err)
	}
	if got.For("anything").Mode != shim.ModePerTest {
		t.Fatalf("absent plan gave mode %q, want per-test", got.For("anything").Mode)
	}
}

func TestWriteReport_IsReadableByTheRunner(t *testing.T) {
	root := t.TempDir()
	want := shim.Report{
		Package:   "github.com/example/repo/billing",
		Mode:      shim.ModePerTest,
		CoverMode: "atomic",
		Tests:     []string{"TestA", "TestB"},
	}
	if err := shim.WriteReport(root, want); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	if _, err := os.Stat(shim.ReportPath(root, want.Package)); err != nil {
		t.Fatalf("report not at ReportPath: %v", err)
	}
	got, err := shim.ReadReports(root)
	if err != nil {
		t.Fatalf("ReadReports: %v", err)
	}
	if len(got) != 1 || got[0].Package != want.Package || len(got[0].Tests) != 2 {
		t.Fatalf("ReadReports = %+v, want the one report back", got)
	}
}
