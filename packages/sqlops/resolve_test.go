package sqlops

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// renderOperands makes a formatOperands result comparable in one field:
// `arg:s` for a string-shaped verb, `arg:-` for anything else.
func renderOperands(ops []formatOperand) string {
	out := make([]string, 0, len(ops))
	for _, op := range ops {
		shape := "-"
		if op.str {
			shape = "s"
		}
		out = append(out, fmt.Sprintf("%d:%s", op.arg, shape))
	}
	return strings.Join(out, ",")
}

// A verb does not always consume the argument sitting at its own ordinal: `*`
// takes a width from an argument of its own, and `[n]` names the argument
// outright and moves the cursor for everything after it. Zipping positionally
// checks the wrong expression, which on an injection check is both a miss and
// a false alarm waiting to happen.
func TestFormatOperands_StarsAndExplicitIndexes(t *testing.T) {
	cases := []struct {
		format string
		want   string
		ok     bool
	}{
		{format: "SELECT id FROM t", want: "", ok: true},
		{format: "SELECT %s FROM %s", want: "0:s,1:s", ok: true},
		{format: "SELECT %d, %s", want: "0:-,1:s", ok: true},
		{format: "SELECT %-10s FROM %s", want: "0:s,1:s", ok: true},
		// The width argument shifts every later verb along by one.
		{format: "SELECT %*s FROM %s", want: "0:-,1:s,2:s", ok: true},
		{format: "SELECT %.*f FROM %s", want: "0:-,1:-,2:s", ok: true},
		{format: "SELECT %*.*f FROM %s", want: "0:-,1:-,2:-,3:s", ok: true},
		// An explicit index repoints the cursor, Go's own rule.
		{format: "SELECT %[2]s WHERE k = %d", want: "1:s,2:-", ok: true},
		{format: "SELECT %[1]s", want: "0:s", ok: true},
		// Escapes consume nothing.
		{format: "SELECT '100%%' , %s", want: "0:s", ok: true},
		// Directives atlas cannot account for are refused rather than
		// guessed at.
		{format: "SELECT %s FROM t WHERE x LIKE '%'", ok: false},
		{format: "SELECT %[x]s", ok: false},
		{format: "SELECT %[0]s", ok: false},
		{format: "SELECT %[2]*[1]d", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.format, func(t *testing.T) {
			ops, ok := formatOperands(tc.format)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (ops %v)", ok, tc.ok, renderOperands(ops))
			}
			if ok && renderOperands(ops) != tc.want {
				t.Errorf("operands = %q, want %q", renderOperands(ops), tc.want)
			}
		})
	}
}

// extractSourceOps writes one Go file into a package directory and extracts
// it, returning the operations in order.
func extractSourceOps(t *testing.T, body string) []Operation {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "repo.go"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ops, warnings, err := ExtractGo(dir, ExtractOptions{})
	if err != nil {
		t.Fatalf("ExtractGo: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	return ops
}

// extractSource keys those operations by enclosing symbol.
func extractSource(t *testing.T, body string) map[string]Operation {
	t.Helper()
	return opIndex(t, extractSourceOps(t, body))
}

const formatFixture = `package repo

import (
	"context"
	"database/sql"
	"fmt"
)

type R struct{ db *sql.DB }

// Width takes its column width from a local, so the caller-supplied table
// name lands one argument further along than its verb's ordinal.
func (r *R) Width(ctx context.Context, table string) error {
	width := 10
	_, err := r.db.ExecContext(ctx, fmt.Sprintf("SELECT %*s FROM %s", width, "id", table))
	return err
}

// Indexed names its arguments out of order.
func (r *R) Indexed(ctx context.Context, id int, table string) error {
	_, err := r.db.ExecContext(ctx, fmt.Sprintf("SELECT id FROM %[2]s WHERE k = %[1]d", id, table))
	return err
}

// Safe formats only values that never became text a caller controls.
func (r *R) Safe(ctx context.Context, id int) error {
	width := 4
	_, err := r.db.ExecContext(ctx, fmt.Sprintf("SELECT %*s FROM audit WHERE k = %d", width, "id", id))
	return err
}
`

func TestExtractGo_FormatVerbsBindToTheArgumentTheyConsume(t *testing.T) {
	ops := extractSource(t, formatFixture)

	for _, name := range []string{"R.Width", "R.Indexed"} {
		op, ok := ops[name]
		if !ok {
			t.Fatalf("%s was not extracted", name)
		}
		if !op.CallerData {
			t.Errorf("%s formats a caller-supplied table name into the SQL; CallerData must be true", name)
		}
		if op.InterpolatedExpr != "table" {
			t.Errorf("%s interpolated expr = %q, want the table parameter", name, op.InterpolatedExpr)
		}
	}

	// The counterweight: only an integer verb consumes the caller's value, so
	// the injection check must stay quiet.
	if safe := ops["R.Safe"]; safe.CallerData {
		t.Errorf("R.Safe formats an int with %%d; CallerData must be false, got expr %q", safe.InterpolatedExpr)
	}
}
