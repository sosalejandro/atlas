package sqlops

import (
	"sort"
	"strings"
	"testing"
)

// opIndex keys extracted operations by the symbol they live in so a case can
// name the one it is about without depending on file order.
func opIndex(t *testing.T, ops []Operation) map[string]Operation {
	t.Helper()
	out := make(map[string]Operation, len(ops))
	for _, op := range ops {
		if _, dup := out[op.SymbolName]; dup {
			t.Fatalf("fixture has two operations in %s; the test keys on one per symbol", op.SymbolName)
		}
		out[op.SymbolName] = op
	}
	return out
}

func extractFixture(t *testing.T) map[string]Operation {
	t.Helper()
	ops, warnings, err := ExtractGo("testdata/goqueries", ExtractOptions{})
	if err != nil {
		t.Fatalf("ExtractGo: %v", err)
	}
	if len(warnings) != 0 {
		t.Logf("warnings: %v", warnings)
	}
	return opIndex(t, ops)
}

func TestExtractGo_ResolvesLiteralsConstsAndLocals(t *testing.T) {
	ops := extractFixture(t)

	// A const declared elsewhere in the package must resolve.
	list, ok := ops["UserRepo.ListUsers"]
	if !ok {
		t.Fatal("ListUsers not extracted")
	}
	if !list.Resolved {
		t.Fatalf("ListUsers unresolved: %s", list.UnresolvedReason)
	}
	if list.Statement.Kind != KindSelect || list.RowScan != ScanSlice {
		t.Errorf("ListUsers = %s/%s, want select/slice", list.Statement.Kind, list.RowScan)
	}
	if list.Position.Path != "repo.go" || list.Position.Line == 0 {
		t.Errorf("ListUsers position = %+v, want repo.go with a line", list.Position)
	}

	get := ops["UserRepo.GetUser"]
	if get.RowScan != ScanSingle {
		t.Errorf("GetUser row scan = %q, want %q", get.RowScan, ScanSingle)
	}
	touch := ops["UserRepo.Touch"]
	if touch.RowScan != ScanExec || touch.Statement.Kind != KindUpdate {
		t.Errorf("Touch = %s/%s, want update/exec", touch.Statement.Kind, touch.RowScan)
	}
}

func TestExtractGo_UnresolvedIsRecordedWithAReason(t *testing.T) {
	ops := extractFixture(t)

	for _, name := range []string{"UserRepo.SearchUsers", "UserRepo.CountByTable", "UserRepo.Dynamic"} {
		op, ok := ops[name]
		if !ok {
			t.Fatalf("%s was dropped entirely; unresolved queries must still be recorded", name)
		}
		if op.Resolved {
			t.Errorf("%s reported resolved", name)
		}
		if op.UnresolvedReason == "" {
			t.Errorf("%s has no unresolved reason", name)
		}
	}

	if r := ops["UserRepo.Dynamic"].UnresolvedReason; !strings.Contains(r, "resolve") {
		t.Errorf("Dynamic reason = %q, want it to say the expression could not be resolved", r)
	}
}

func TestExtractGo_InterpolationTracksCallerData(t *testing.T) {
	ops := extractFixture(t)

	search := ops["UserRepo.SearchUsers"]
	if !search.Interpolated || search.Interpolation != InterpolationConcat {
		t.Errorf("SearchUsers interpolation = %v/%q, want true/concat", search.Interpolated, search.Interpolation)
	}
	if !search.CallerData {
		t.Error("SearchUsers splices a function parameter; CallerData must be true")
	}

	count := ops["UserRepo.CountByTable"]
	if !count.Interpolated || count.Interpolation != InterpolationSprintf {
		t.Errorf("CountByTable interpolation = %v/%q, want true/sprintf", count.Interpolated, count.Interpolation)
	}
	if !count.CallerData {
		t.Error("CountByTable formats a function parameter into the SQL; CallerData must be true")
	}

	// The placeholder-widening idiom formats no caller value into the text.
	// It is still interpolation, but flagging it as caller data would make
	// the injection advisory cry wolf on the safest common pattern.
	expand := ops["UserRepo.ExpandIn"]
	if !expand.Interpolated {
		t.Error("ExpandIn is built with Sprintf; Interpolated must be true")
	}
	if expand.CallerData {
		t.Error("ExpandIn splices only a generated placeholder list; CallerData must be false")
	}

	if ops["UserRepo.ListUsers"].Interpolated {
		t.Error("a plain const query must not be reported as interpolated")
	}
}

func TestExtractGo_IgnoresNonDatabaseCalls(t *testing.T) {
	ops := extractFixture(t)
	if _, found := ops["repo.notADatabase"]; found {
		t.Error("a Query() call on a non-database receiver with non-SQL text was recorded")
	}

	// The whole inventory, pinned. An extraction that silently grows is as
	// much a bug as one that silently shrinks: both mean the resolved
	// fraction the command reports is measured against a moving denominator.
	var names []string
	for n := range ops {
		names = append(names, n)
	}
	sort.Strings(names)
	want := "UserRepo.AllTenants,UserRepo.CountByTable,UserRepo.Dynamic," +
		"UserRepo.ExpandIn,UserRepo.GetUser,UserRepo.ListUsers,UserRepo.Page," +
		"UserRepo.SearchUsers,UserRepo.Touch"
	if got := strings.Join(names, ","); got != want {
		t.Errorf("extracted symbols =\n  %s\nwant\n  %s", got, want)
	}
}

func TestExtractGo_ReadsSuppressionDirectives(t *testing.T) {
	ops := extractFixture(t)
	got := ops["UserRepo.AllTenants"].Suppressions
	sort.Strings(got)
	if strings.Join(got, ",") != "sql.unbounded-list" {
		t.Errorf("AllTenants suppressions = %v, want [sql.unbounded-list]", got)
	}
	if len(ops["UserRepo.ListUsers"].Suppressions) != 0 {
		t.Error("ListUsers picked up a suppression it does not declare")
	}
}
