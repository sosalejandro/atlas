package redact

import (
	"strings"
	"testing"
)

// leakedDSNPassword is the credential the fixtures below hide inside a
// query. It is asserted on in both directions: found where it should be,
// and absent from anything that gets stored.
const leakedDSNPassword = "Kq9Xm2Vz7Pw4Rt6Y"

func leakyQuery() string {
	return "SELECT * FROM dblink('postgres://reporting:" + leakedDSNPassword +
		"@warehouse.internal:5432/dw', 'SELECT 1')"
}

// TestField_RedactsARedactableColumnOnTheWayIn is the ingest-time half of
// the promise `atlas security` could previously only make about a store it
// had already let the credential into.
func TestField_RedactsARedactableColumnOnTheWayIn(t *testing.T) {
	res := Field("sql_operations", "sql_text", leakyQuery())
	if !res.Redacted() {
		t.Fatalf("Field found no secret in %q", leakyQuery())
	}
	if strings.Contains(res.Text, leakedDSNPassword) {
		t.Errorf("the credential survived Field: %q", res.Text)
	}
	// The row has to stay analysable: `atlas sql` reports on this text.
	for _, keep := range []string{"dblink", "postgres://", "reporting", "warehouse.internal"} {
		if !strings.Contains(res.Text, keep) {
			t.Errorf("Field destroyed %q: %q", keep, res.Text)
		}
	}
	if len(res.Findings) != 1 {
		t.Fatalf("findings = %+v, want exactly one", res.Findings)
	}
	if res.Findings[0].Kind != KindConnectionString {
		t.Errorf("rule = %q, want %q", res.Findings[0].Kind, KindConnectionString)
	}
}

// TestField_LeavesANonRedactableColumnAlone: an identity column is reported
// by Sweep and never rewritten, because rewriting it would change what the
// index MEANS. Field applies the same rule, from the same registry.
func TestField_LeavesANonRedactableColumnAlone(t *testing.T) {
	for _, tc := range []struct{ table, column string }{
		{"sql_operations", "symbol_name"}, // identifier
		{"sql_operations", "file_path"},   // path
		{"symbols", "qualified_name"},     // identifier
	} {
		if Redactable(tc.table, tc.column) {
			t.Fatalf("%s.%s is marked redactable; this test asserts the opposite",
				tc.table, tc.column)
		}
		res := Field(tc.table, tc.column, leakyQuery())
		if res.Text != leakyQuery() {
			t.Errorf("Field rewrote %s.%s: %q", tc.table, tc.column, res.Text)
		}
		if res.Redacted() {
			t.Errorf("Field reported findings for the non-redactable %s.%s", tc.table, tc.column)
		}
	}
}

// TestField_UnregisteredColumnIsNotRewritten: this package does not rewrite
// a column it cannot describe. A mistyped column name must not silently
// become a redaction of something nobody classified.
func TestField_UnregisteredColumnIsNotRewritten(t *testing.T) {
	if Redactable("sql_operations", "sql_txt") {
		t.Fatal("an unregistered column reported itself redactable")
	}
	if got := Field("no_such_table", "no_such_column", leakyQuery()); got.Text != leakyQuery() {
		t.Errorf("Field rewrote an unregistered column: %q", got.Text)
	}
}

// TestField_AgreesWithTheRegistry: Field and `atlas security redact` have to
// rewrite exactly the same set of columns, because they are documented as
// one rule. Driving both off Columns() is what makes that true rather than a
// coincidence, and this is the check.
func TestField_AgreesWithTheRegistry(t *testing.T) {
	for _, c := range Columns() {
		if got := Redactable(c.Table, c.Name); got != c.Redactable {
			t.Errorf("Redactable(%s, %s) = %v, registry says %v",
				c.Table, c.Name, got, c.Redactable)
		}
	}
}

// TestField_IsIdempotent: a re-ingest of unchanged source must not redact a
// redaction, or the digest that correlates one credential across nine rows
// would churn on every scan.
func TestField_IsIdempotent(t *testing.T) {
	once := Field("sql_operations", "sql_text", leakyQuery())
	twice := Field("sql_operations", "sql_text", once.Text)
	if twice.Redacted() {
		t.Errorf("a second pass redacted the placeholder: %q", twice.Text)
	}
	if twice.Text != once.Text {
		t.Errorf("a second pass changed the text:\n got %q\nwant %q", twice.Text, once.Text)
	}
}
