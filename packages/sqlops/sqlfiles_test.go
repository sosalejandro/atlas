package sqlops

import (
	"strings"
	"testing"
)

func TestExtractSQLFiles_NamedQueries(t *testing.T) {
	ops, err := ExtractSQLFiles([]string{"testdata/queries"}, "testdata/queries")
	if err != nil {
		t.Fatalf("ExtractSQLFiles: %v", err)
	}
	byName := map[string]Operation{}
	for _, op := range ops {
		byName[op.Name] = op
	}
	if len(byName) != 4 {
		t.Fatalf("extracted %d queries, want 4: %v", len(byName), byName)
	}

	list := byName["ListUsers"]
	if list.Source != SourceSQLFile || list.RowScan != ScanSlice {
		t.Errorf("ListUsers = %s/%s, want sql/slice", list.Source, list.RowScan)
	}
	if !list.Resolved || list.Statement.Kind != KindSelect {
		t.Errorf("ListUsers unresolved or misclassified: %+v", list)
	}
	// The symbol name matches the `sql:<QueryName>` anchor the Go scanner
	// already puts in the graph, so the operation joins the rest of it.
	if list.SymbolName != "sql:ListUsers" {
		t.Errorf("ListUsers symbol = %q, want %q", list.SymbolName, "sql:ListUsers")
	}
	if list.Position.Path != "users.sql" || list.Position.Line != 1 {
		t.Errorf("ListUsers position = %+v, want users.sql:1", list.Position)
	}
	if strings.Join(list.Suppressions, ",") != "sql.unbounded-list" {
		t.Errorf("ListUsers suppressions = %v", list.Suppressions)
	}

	if got := byName["GetUser"].RowScan; got != ScanSingle {
		t.Errorf("GetUser row scan = %q, want single", got)
	}
	if !byName["GetUser"].Statement.SelectStar {
		t.Error("GetUser is a SELECT *; the flag must be set")
	}
	if got := byName["DeleteSession"].RowScan; got != ScanExec {
		t.Errorf("DeleteSession row scan = %q, want exec", got)
	}
	page := byName["PageEvents"]
	if !page.Statement.HasOffset || page.Statement.OffsetBound != OffsetParameter {
		t.Errorf("PageEvents offset = %v/%q", page.Statement.HasOffset, page.Statement.OffsetBound)
	}
	if len(byName["PageEvents"].Suppressions) != 0 {
		t.Error("a directive in one query's header leaked into the next query")
	}
}
