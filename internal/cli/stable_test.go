package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The property #111 and #161 both need: the same command over the same code
// produces the same bytes. Atlas had never had it, and the way that went
// unnoticed is worth pinning as much as the property itself -- `generated_at`
// has second granularity, so a quick loop of runs lands inside one second and
// looks stable. Every test here that compares two runs therefore puts a real
// second between them.

func mustSecondBoundary() { time.Sleep(1100 * time.Millisecond) }

func runStable(t *testing.T, f *doctorFixture, args ...string) string {
	t.Helper()
	root := NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"doctor", "--db-path", f.dbPath, "--root", f.root}, args...))
	// The error is deliberately ignored. doctor emits its report to stdout
	// and THEN gates, precisely so a CI job sees the findings rather than
	// only a red X -- so a tripped gate still leaves the output this test
	// exists to compare. A failure to produce output is caught below.
	_ = root.ExecuteContext(context.Background())
	if stdout.Len() == 0 {
		t.Fatalf("doctor %v produced no output: %s", args, stderr.String())
	}
	return stdout.String()
}

// The headline. Without --stable this fails; that is the bug.
func TestStable_TwoRunsAcrossASecondProduceIdenticalBytes(t *testing.T) {
	f := newDoctorFixture(t)

	first := runStable(t, f, "--stable")
	mustSecondBoundary()
	second := runStable(t, f, "--stable")

	if first != second {
		t.Errorf("--stable output differs between runs:\n--- first\n%s\n--- second\n%s", first, second)
	}
	if first == "" {
		t.Fatal("--stable produced no output; the comparison would be vacuous")
	}
}

// The same pair WITHOUT --stable must differ, or the test above proves
// nothing -- it would pass on a build where generated_at had been dropped
// entirely and --stable did nothing.
func TestStable_WithoutTheFlagOutputIsNotComparable(t *testing.T) {
	f := newDoctorFixture(t)

	first := runStable(t, f, "--json")
	mustSecondBoundary()
	second := runStable(t, f, "--json")

	if first == second {
		t.Skip("this build emits no wall-clock stamp; --stable has nothing to remove here")
	}
	// Confirm it is the stamp and not something else moving underneath.
	if !strings.Contains(first, "generated_at") {
		t.Errorf("output differs but carries no generated_at; something else is unstable:\n%s", first)
	}
}

func TestStable_ImpliesJSON(t *testing.T) {
	f := newDoctorFixture(t)
	out := runStable(t, f, "--stable")
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("--stable did not emit JSON: %v\n%s", err, out)
	}
	if env["schema_version"] != schemaVersion {
		t.Errorf("schema_version = %v, want %q", env["schema_version"], schemaVersion)
	}
}

func TestStable_DropsTheEnvelopeStampAndEveryVolatileField(t *testing.T) {
	f := newDoctorFixture(t)
	out := runStable(t, f, "--stable")

	var env any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := volatileKeysIn(env); len(got) != 0 {
		t.Errorf("--stable output still carries volatile keys: %v", got)
	}
	if strings.Contains(out, "generated_at") {
		t.Error("--stable output carries generated_at")
	}
}

// Stripping must not eat the answer. A --stable mode that removed the
// findings would satisfy every test above and be useless.
func TestStable_KeepsTheContent(t *testing.T) {
	f := newDoctorFixture(t)
	stable := runStable(t, f, "--stable")
	full := runStable(t, f, "--json")

	for _, want := range []string{"index.freshness", "store.schema", "checks", "counts"} {
		if !strings.Contains(stable, want) {
			t.Errorf("--stable dropped %q, which is content, not provenance", want)
		}
	}
	// The stable form must not be trivially shorter -- if it is a fraction of
	// the full output, something structural was removed.
	if len(stable)*2 < len(full) {
		t.Errorf("--stable output is %d bytes against %d full; too much was removed",
			len(stable), len(full))
	}
}

func TestIsVolatileKey(t *testing.T) {
	for _, k := range []string{
		"generated_at", "sampled_at", "parsed_at", "last_scanned",
		"db_path", "root", "project_root", "input",
		"load_ms", "scan_ms", "call_graph_ms", "duration_ms", "elapsed_ns",
	} {
		if !isVolatileKey(k) {
			t.Errorf("%q is not treated as volatile", k)
		}
	}
	// Content that merely looks temporal must survive: these are properties
	// of the code, not of the run.
	for _, k := range []string{
		"line", "end_line", "symbols", "edges", "tier", "name", "severity",
		"resolved_fraction", "unresolved", "coverage", "features",
	} {
		if isVolatileKey(k) {
			t.Errorf("%q was stripped; it is content, not provenance", k)
		}
	}
}

func TestStripVolatile_RecursesIntoNestedStructures(t *testing.T) {
	in := map[string]any{
		"generated_at": "2026-09-07T00:00:00Z",
		"checks": []any{
			map[string]any{"name": "index.freshness", "duration_ms": 12, "severity": "ok"},
		},
		"nested": map[string]any{"db_path": "/home/someone/.atlas/atlas.db", "count": 3},
	}
	out, err := stripVolatile(in)
	if err != nil {
		t.Fatalf("stripVolatile: %v", err)
	}
	if got := volatileKeysIn(out); len(got) != 0 {
		t.Errorf("volatile keys survived nesting: %v", got)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("stripVolatile returned %T", out)
	}
	if m["nested"].(map[string]any)["count"] != float64(3) {
		t.Error("stripVolatile removed a sibling of a volatile key")
	}
	checks := m["checks"].([]any)
	if checks[0].(map[string]any)["name"] != "index.freshness" {
		t.Error("stripVolatile removed content inside an array")
	}
}
