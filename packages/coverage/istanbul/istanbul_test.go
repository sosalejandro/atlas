package istanbul

import (
	"reflect"
	"strings"
	"testing"
)

const sampleReport = `{
  "/repo/apps/web-patient/src/svc.tsx": {
    "path": "/repo/apps/web-patient/src/svc.tsx",
    "statementMap": {
      "0": {"start": {"line": 12, "column": 4}, "end": {"line": 14, "column": 6}},
      "1": {"start": {"line": 18, "column": 2}, "end": {"line": 18, "column": 30}},
      "2": {"start": {"line": 31, "column": 0}, "end": {"line": 33, "column": 1}}
    },
    "s": {"0": 3, "1": 0, "2": 5},
    "fnMap": {}, "f": {}, "branchMap": {}, "b": {}
  },
  "/repo/apps/web-patient/src/other.tsx": {
    "path": "/repo/apps/web-patient/src/other.tsx",
    "statementMap": {
      "0": {"start": {"line": 6, "column": 0}, "end": {"line": 6, "column": 10}}
    },
    "s": {"0": 0}
  }
}`

func TestParse_Sample(t *testing.T) {
	got, err := Parse(strings.NewReader(sampleReport))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	wantSvc := []Statement{
		{StartLine: 12, EndLine: 14, Count: 3},
		{StartLine: 18, EndLine: 18, Count: 0},
		{StartLine: 31, EndLine: 33, Count: 5},
	}
	if g := got["/repo/apps/web-patient/src/svc.tsx"]; !reflect.DeepEqual(g, wantSvc) {
		t.Fatalf("svc.tsx = %+v\nwant %+v", g, wantSvc)
	}
	wantOther := []Statement{{StartLine: 6, EndLine: 6, Count: 0}}
	if g := got["/repo/apps/web-patient/src/other.tsx"]; !reflect.DeepEqual(g, wantOther) {
		t.Fatalf("other.tsx = %+v\nwant %+v", g, wantOther)
	}
}

// TestParse_StatementIDOrderStable verifies statement ids are emitted in
// numeric (not lexical) order — "10" must follow "2", not precede it.
func TestParse_StatementIDOrderStable(t *testing.T) {
	const r = `{"/f.ts": {"path":"/f.ts",
      "statementMap": {
        "0": {"start":{"line":1,"column":0},"end":{"line":1,"column":1}},
        "2": {"start":{"line":3,"column":0},"end":{"line":3,"column":1}},
        "10": {"start":{"line":11,"column":0},"end":{"line":11,"column":1}}
      },
      "s": {"0":1,"2":1,"10":1}}}`
	got, err := Parse(strings.NewReader(r))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	lines := []int{}
	for _, s := range got["/f.ts"] {
		lines = append(lines, s.StartLine)
	}
	if !reflect.DeepEqual(lines, []int{1, 3, 11}) {
		t.Fatalf("start lines = %v, want [1 3 11] (numeric id order)", lines)
	}
}

// TestParse_MissingExecCountTreatedZero verifies a statement present in
// statementMap but absent from `s` counts toward total as not-executed
// (rather than being dropped).
func TestParse_MissingExecCountTreatedZero(t *testing.T) {
	const r = `{"/f.ts": {"path":"/f.ts",
      "statementMap": {"0": {"start":{"line":1,"column":0},"end":{"line":1,"column":1}}},
      "s": {}}}`
	got, err := Parse(strings.NewReader(r))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	stmts := got["/f.ts"]
	if len(stmts) != 1 || stmts[0].Count != 0 || stmts[0].Executed() {
		t.Fatalf("got %+v, want one not-executed statement", stmts)
	}
}

// TestParse_FallsBackToMapKeyWhenPathEmpty verifies the file is keyed by the
// outer map key when the inner "path" is absent.
func TestParse_FallsBackToMapKeyWhenPathEmpty(t *testing.T) {
	const r = `{"apps/web/src/x.ts": {
      "statementMap": {"0": {"start":{"line":1,"column":0},"end":{"line":1,"column":1}}},
      "s": {"0":1}}}`
	got, err := Parse(strings.NewReader(r))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, ok := got["apps/web/src/x.ts"]; !ok {
		t.Fatalf("expected key from map fallback, got keys %v", keys(got))
	}
}

func TestParse_MalformedFailsLoudly(t *testing.T) {
	for _, bad := range []string{
		`not json`,
		`{"/f.ts": {"statementMap": {"x": {"start":{"line":1},"end":{"line":1}}}, "s": {}}}`, // non-numeric id
		`{`,
	} {
		if _, err := Parse(strings.NewReader(bad)); err == nil {
			t.Errorf("expected error for %q, got nil", bad)
		}
	}
}

func keys(m map[string][]Statement) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
