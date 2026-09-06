package sqlops

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Every advisory has a legitimate exception, and a check with no way to say
// "yes, I know" gets turned off wholesale -- which is why the suppression
// spellings are documented in docs/commands/sql.md. Each documented form gets
// a case here, asserting BOTH the codes atlas read off the site and that the
// advisory is actually silenced, because reading the directive and honouring
// it are two different things and only the second one is the feature.
const suppressionFixture = `package repo

import (
	"context"
	"database/sql"
)

type R struct{ db *sql.DB }

// Unsuppressed is the control: without a directive this shape is a finding.
func (r *R) Unsuppressed(ctx context.Context) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT id FROM users")
	if err != nil {
		return nil, err
	}
	var out []int64
	for rows.Next() {
		var n int64
		_ = rows.Scan(&n)
		out = append(out, n)
	}
	return out, nil
}

// OnTheCallLine puts the directive after the call itself.
func (r *R) OnTheCallLine(ctx context.Context) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT id FROM users") // atlas:sql-ignore sql.unbounded-list
	if err != nil {
		return nil, err
	}
	var out []int64
	for rows.Next() {
		var n int64
		_ = rows.Scan(&n)
		out = append(out, n)
	}
	return out, nil
}

// InTheBlockAbove puts it in the comment block immediately above the call.
func (r *R) InTheBlockAbove(ctx context.Context) ([]int64, error) {
	// The tenants table holds nine rows and always will.
	// atlas:sql-ignore sql.unbounded-list
	rows, err := r.db.QueryContext(ctx, "SELECT id FROM tenants")
	if err != nil {
		return nil, err
	}
	var out []int64
	for rows.Next() {
		var n int64
		_ = rows.Scan(&n)
		out = append(out, n)
	}
	return out, nil
}

// InTheDocComment is knowingly unbounded; there are nine rows.
// atlas:sql-ignore sql.unbounded-list
func (r *R) InTheDocComment(ctx context.Context) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT id FROM tenants")
	if err != nil {
		return nil, err
	}
	var out []int64
	for rows.Next() {
		var n int64
		_ = rows.Scan(&n)
		out = append(out, n)
	}
	return out, nil
}

// SeveralCodes lists more than one, and says why.
// atlas:sql-ignore sql.unbounded-list,sql.select-star -- payload is versioned
func (r *R) SeveralCodes(ctx context.Context) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT * FROM users")
	if err != nil {
		return nil, err
	}
	var out []int64
	for rows.Next() {
		var n int64
		_ = rows.Scan(&n)
		out = append(out, n)
	}
	return out, nil
}

// Everything silences the lot.
// atlas:sql-ignore all
func (r *R) Everything(ctx context.Context) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT * FROM users")
	if err != nil {
		return nil, err
	}
	var out []int64
	for rows.Next() {
		var n int64
		_ = rows.Scan(&n)
		out = append(out, n)
	}
	return out, nil
}
`

const suppressionQueries = `-- name: Unsuppressed :many
SELECT id FROM users;

-- name: BelowTheHeader :many
-- The set is small and bounded by the tenant.
-- atlas:sql-ignore sql.unbounded-list
SELECT id FROM users;

-- atlas:sql-ignore sql.unbounded-list
-- name: AboveTheHeader :many
SELECT id FROM users;

-- name: SeveralCodes :many
-- atlas:sql-ignore sql.unbounded-list,sql.select-star -- knowingly wide
SELECT * FROM users;
`

// codesFor runs the per-operation checks over one operation.
func codesFor(op Operation) string {
	return strings.Join(codesOf(Advise([]Operation{op}, Schema{}, perOpOnly())), ",")
}

func suppressionsOf(op Operation) string {
	got := append([]string(nil), op.Suppressions...)
	sort.Strings(got)
	return strings.Join(got, ",")
}

func TestSuppression_EveryDocumentedGoForm(t *testing.T) {
	ops := extractSource(t, suppressionFixture)

	cases := []struct {
		symbol       string
		suppressions string
		codes        string
	}{
		{
			symbol: "R.Unsuppressed", suppressions: "",
			codes: CodeUnboundedList,
		},
		{
			symbol: "R.OnTheCallLine", suppressions: CodeUnboundedList,
			codes: "",
		},
		{
			symbol: "R.InTheBlockAbove", suppressions: CodeUnboundedList,
			codes: "",
		},
		{
			symbol: "R.InTheDocComment", suppressions: CodeUnboundedList,
			codes: "",
		},
		{
			// Comma-separated codes, and trailing prose that must be ignored
			// rather than read as another code.
			symbol:       "R.SeveralCodes",
			suppressions: CodeSelectStar + "," + CodeUnboundedList,
			codes:        "",
		},
		{
			symbol: "R.Everything", suppressions: DirectiveAll,
			codes: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.symbol, func(t *testing.T) {
			op, ok := ops[tc.symbol]
			if !ok {
				t.Fatalf("%s was not extracted", tc.symbol)
			}
			if got := suppressionsOf(op); got != tc.suppressions {
				t.Errorf("suppressions = %q, want %q", got, tc.suppressions)
			}
			if got := codesFor(op); got != tc.codes {
				t.Errorf("advisories = %q, want %q", got, tc.codes)
			}
		})
	}
}

func TestSuppression_InASQLQueryHeader(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "users.sql"), []byte(suppressionQueries), 0o600); err != nil {
		t.Fatal(err)
	}
	ops, err := ExtractSQLFiles([]string{dir}, dir)
	if err != nil {
		t.Fatalf("ExtractSQLFiles: %v", err)
	}
	byName := map[string]Operation{}
	for _, op := range ops {
		byName[op.Name] = op
	}

	cases := []struct {
		name         string
		suppressions string
		codes        string
	}{
		{name: "Unsuppressed", suppressions: "", codes: CodeUnboundedList},
		{name: "BelowTheHeader", suppressions: CodeUnboundedList, codes: ""},
		{name: "AboveTheHeader", suppressions: CodeUnboundedList, codes: ""},
		{
			name:         "SeveralCodes",
			suppressions: CodeSelectStar + "," + CodeUnboundedList,
			codes:        "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op, ok := byName[tc.name]
			if !ok {
				t.Fatalf("query %s was not extracted", tc.name)
			}
			if got := suppressionsOf(op); got != tc.suppressions {
				t.Errorf("suppressions = %q, want %q", got, tc.suppressions)
			}
			if got := codesFor(op); got != tc.codes {
				t.Errorf("advisories = %q, want %q", got, tc.codes)
			}
		})
	}
}

// `all` must silence a code nobody listed, and only at the site that asks for
// it.
func TestSuppression_AllIsNotPerCode(t *testing.T) {
	ops := extractSource(t, suppressionFixture)
	every := ops["R.Everything"]
	if !every.Suppressed(CodeMissingIndex) || !every.Suppressed(CodePossibleInjection) {
		t.Errorf("`all` did not silence an unlisted code: %v", every.Suppressions)
	}
	if ops["R.Unsuppressed"].Suppressed(CodeUnboundedList) {
		t.Error("a directive leaked from one call site to another")
	}
}
