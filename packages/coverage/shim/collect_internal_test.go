package shim

import (
	"errors"
	"strings"
	"testing"
)

// fakeSuite stands in for *testing.M. It records what the collector asked it
// to run, in order, so the tests can assert on the choreography — clear
// BEFORE the test, snapshot AFTER it — rather than only on the end state.
type fakeSuite struct {
	names  []string
	listed string
	listFn func(pattern string) ([]string, error)
	codes  map[string]int
	log    *[]string
}

func (f *fakeSuite) List(pattern string) ([]string, error) {
	f.listed = pattern
	if f.listFn != nil {
		return f.listFn(pattern)
	}
	return f.names, nil
}

func (f *fakeSuite) Run(pattern string) int {
	*f.log = append(*f.log, "run("+pattern+")")
	return f.codes[pattern]
}

// fakeCounters stands in for runtime/coverage.
type fakeCounters struct {
	mode     string
	clearErr error
	clears   int
	log      *[]string
}

func (f *fakeCounters) Mode() string { return f.mode }

func (f *fakeCounters) Clear() error {
	f.clears++
	*f.log = append(*f.log, "clear")
	if f.clearErr != nil {
		return f.clearErr
	}
	return nil
}

func (f *fakeCounters) WriteCounters(dir string) error {
	*f.log = append(*f.log, "counters("+dir+")")
	return nil
}

func (f *fakeCounters) WriteMeta(dir string) error {
	*f.log = append(*f.log, "meta("+dir+")")
	return nil
}

const testPkg = "github.com/example/repo/billing"

func TestCollect(t *testing.T) {
	cases := []struct {
		name      string
		want      Mode
		reason    string
		suite     fakeSuite
		counters  fakeCounters
		filter    string
		wantMode  Mode
		wantCode  int
		wantOrder []string
		wantWhy   string // substring the degradation reason must carry
	}{
		{
			name:     "per-test clears between tests and snapshots after each",
			want:     ModePerTest,
			suite:    fakeSuite{names: []string{"TestA", "TestB"}},
			counters: fakeCounters{mode: "atomic"},
			wantMode: ModePerTest,
			wantOrder: []string{
				"run(^$)",
				"meta(/root/meta/" + testPkg + ")",
				"clear",
				"run(^TestA$)",
				"counters(/root/counters/" + testPkg + "/TestA)",
				"clear",
				"run(^TestB$)",
				"counters(/root/counters/" + testPkg + "/TestB)",
			},
		},
		{
			name:     "a failing test decides the exit code",
			want:     ModePerTest,
			suite:    fakeSuite{names: []string{"TestA", "TestB"}, codes: map[string]int{"^TestA$": 1}},
			counters: fakeCounters{mode: "atomic"},
			wantMode: ModePerTest,
			wantCode: 1,
		},
		{
			name:     "non-atomic counters cannot be cleared, so per-package",
			want:     ModePerTest,
			suite:    fakeSuite{names: []string{"TestA"}},
			counters: fakeCounters{mode: "set"},
			wantMode: ModePerPackage,
			wantWhy:  "covermode",
			wantOrder: []string{
				"run(^$)",
				"run()",
				"counters(/root/counters/" + testPkg + "/" + PackageCounterName + ")",
				"meta(/root/meta/" + testPkg + ")",
			},
		},
		{
			name:     "the plan can demand per-package, and the reason survives",
			want:     ModePerPackage,
			reason:   "3 tests call t.Parallel()",
			suite:    fakeSuite{names: []string{"TestA"}},
			counters: fakeCounters{mode: "atomic"},
			wantMode: ModePerPackage,
			wantWhy:  "t.Parallel()",
		},
		{
			name:     "a package with no tests has nothing to attribute",
			want:     ModePerTest,
			suite:    fakeSuite{names: nil},
			counters: fakeCounters{mode: "atomic"},
			wantMode: ModePerPackage,
			wantWhy:  "no top-level tests",
		},
		{
			name:     "an unenumerable suite degrades instead of guessing",
			want:     ModePerTest,
			suite:    fakeSuite{listFn: func(string) ([]string, error) { return nil, errors.New("boom") }},
			counters: fakeCounters{mode: "atomic"},
			wantMode: ModePerPackage,
			wantWhy:  "boom",
		},
		{
			name:     "a clear that fails mid-suite invalidates per-test evidence",
			want:     ModePerTest,
			suite:    fakeSuite{names: []string{"TestA", "TestB"}},
			counters: fakeCounters{mode: "atomic", clearErr: errors.New("counters wedged")},
			wantMode: ModePerPackage,
			wantWhy:  "counters wedged",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var log []string
			suite, counters := tc.suite, tc.counters
			suite.log, counters.log = &log, &log

			code, rep := collect(collectArgs{
				Root:   "/root",
				Pkg:    testPkg,
				Mode:   tc.want,
				Reason: tc.reason,
				Filter: tc.filter,
			}, &suite, &counters)

			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d", code, tc.wantCode)
			}
			if rep.Mode != tc.wantMode {
				t.Errorf("mode = %q, want %q (reason %q)", rep.Mode, tc.wantMode, rep.Reason)
			}
			if tc.wantWhy != "" && !strings.Contains(rep.Reason, tc.wantWhy) {
				t.Errorf("reason = %q, want it to mention %q", rep.Reason, tc.wantWhy)
			}
			if tc.wantMode == ModePerPackage && rep.Reason == "" {
				t.Errorf("degraded to per-package with no reason — that is the diagnostic")
			}
			if tc.wantOrder != nil && !equalStrings(log, tc.wantOrder) {
				t.Errorf("call order =\n  %v\nwant\n  %v", log, tc.wantOrder)
			}
		})
	}
}

// The user's own -run filter has to survive: a shim that quietly widens the
// selection runs tests the developer excluded on purpose.
func TestCollect_HonoursTheCallersRunFilter(t *testing.T) {
	var log []string
	suite := fakeSuite{names: []string{"TestA"}, log: &log}
	counters := fakeCounters{mode: "atomic", log: &log}

	_, _ = collect(collectArgs{Root: "/root", Pkg: testPkg, Mode: ModePerTest, Filter: "TestA"},
		&suite, &counters)

	if suite.listed != "TestA" {
		t.Fatalf("listed with %q, want the caller's -run pattern", suite.listed)
	}
}

func TestCollect_PerPackageKeepsTheCallersFilter(t *testing.T) {
	var log []string
	suite := fakeSuite{names: []string{"TestA"}, log: &log}
	counters := fakeCounters{mode: "set", log: &log}

	_, _ = collect(collectArgs{Root: "/root", Pkg: testPkg, Mode: ModePerTest, Filter: "TestA"},
		&suite, &counters)

	if !containsString(log, "run(TestA)") {
		t.Fatalf("per-package run lost the -run filter: %v", log)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsString(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
