package store

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

// symbolColumns returns the column names of the symbols table, in
// declaration order.
func symbolColumns(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.sqlDB().QueryContext(context.Background(), `PRAGMA table_info(symbols)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(symbols): %v", err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var (
			cid        int
			name, typ  string
			notNull    int
			dflt       *string
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &primaryKey); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows: %v", err)
	}
	return out
}

// Migration 0019 renames bc_path to domain. The old name must be gone, not
// merely shadowed -- a lingering bc_path column is how half a rename ships.
func TestMigration0019_BCPathRenamedToDomain(t *testing.T) {
	s := openTestStore(t)
	cols := symbolColumns(t, s)

	var hasDomain, hasBCPath bool
	for _, c := range cols {
		switch c {
		case "domain":
			hasDomain = true
		case "bc_path":
			hasBCPath = true
		}
	}
	if !hasDomain {
		t.Errorf("symbols has no `domain` column; got %v", cols)
	}
	if hasBCPath {
		t.Errorf("symbols still has `bc_path`; got %v", cols)
	}
}

func TestMigration0019_NodeClassColumnExists(t *testing.T) {
	s := openTestStore(t)
	cols := symbolColumns(t, s)
	for _, c := range cols {
		if c == "node_class" {
			return
		}
	}
	t.Fatalf("symbols has no `node_class` column; got %v", cols)
}

// The triggers are the CHECK constraint SQLite would not let migration 0019
// add to a table with inbound foreign keys. They are the guarantee for a
// writer that bypasses the Go layer, so they get a test of their own.
func TestMigration0019_NodeClassGuardRejectsUnsetAndUnknown(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	insert := func(qn string, class any) error {
		_, err := s.sqlDB().ExecContext(ctx,
			`INSERT INTO symbols (qualified_name, kind, file_path, line, node_class)
			 VALUES (?, 'func', 'a.go', 1, ?)`, qn, class)
		return err
	}

	if err := insert("pkg.Unset", nil); err == nil {
		t.Error("INSERT with node_class NULL succeeded; the guard trigger did not fire")
	}
	if err := insert("pkg.Bogus", "symbol"); err == nil {
		t.Error(`INSERT with node_class "symbol" succeeded; the guard trigger did not fire`)
	}
	if err := insert("pkg.Real", string(shared.NodeClassDeclaration)); err != nil {
		t.Fatalf("INSERT with a legal node_class failed: %v", err)
	}
	// An UPDATE must not be able to walk a legal row back out of the set.
	_, err := s.sqlDB().ExecContext(ctx,
		`UPDATE symbols SET node_class = 'whatever' WHERE qualified_name = 'pkg.Real'`)
	if err == nil {
		t.Error("UPDATE to an illegal node_class succeeded; the update guard did not fire")
	}
}

// The backfill in 0019 is the prefix rule spelled in SQL. It must agree with
// shared.ClassifyNode -- the Go copy that classifies every row written after
// the migration -- for every prefix in the closed set. If the two disagree,
// a store carried across the migration and a store rebuilt from scratch
// answer "is this real code" differently, which is the exact class of
// silent drift the column exists to end.
func TestMigration0019_BackfillMatchesClassifyNode(t *testing.T) {
	// The backfill runs over rows that pre-date the column, so the rows
	// have to be inserted into a v18 schema and the migration applied on
	// top. We cannot reach back through golang-migrate from here, so we
	// reproduce the pre-migration state exactly: drop the guards, null the
	// column out, then re-run the backfill statement from 0019 verbatim.
	s := openTestStore(t)
	ctx := context.Background()

	type row struct {
		qn   string
		path string
	}
	rows := []row{
		{"auth.Login", "internal/auth/login.go"},
		{"route:/login", "internal/http/router.go"},
		{"sql:GetUserByEmail", "db/queries/users.sql"},
		{"endpoint:POST /v1/login", "api/openapi.yaml"},
		{"typing.List", "external:py"},
		{"pkg.Handler", "internal/routes/handler.go"},
	}

	if _, err := s.sqlDB().ExecContext(ctx, `DROP TRIGGER symbols_node_class_insert_guard`); err != nil {
		t.Fatalf("drop insert guard: %v", err)
	}
	if _, err := s.sqlDB().ExecContext(ctx, `DROP TRIGGER symbols_node_class_update_guard`); err != nil {
		t.Fatalf("drop update guard: %v", err)
	}
	for i, r := range rows {
		if _, err := s.sqlDB().ExecContext(ctx,
			`INSERT INTO symbols (qualified_name, kind, file_path, line, node_class)
			 VALUES (?, 'func', ?, ?, NULL)`, r.qn, r.path, i+1); err != nil {
			t.Fatalf("seed %q: %v", r.qn, err)
		}
	}

	if _, err := s.sqlDB().ExecContext(ctx, backfillStatementFromMigration0019(t)); err != nil {
		t.Fatalf("re-run 0019 backfill: %v", err)
	}
	if _, err := s.sqlDB().ExecContext(ctx,
		`UPDATE symbols SET node_class = 'declaration' WHERE node_class IS NULL`); err != nil {
		t.Fatalf("re-run 0019 declaration fill: %v", err)
	}

	for _, r := range rows {
		var got string
		err := s.sqlDB().QueryRowContext(ctx,
			`SELECT node_class FROM symbols WHERE qualified_name = ?`, r.qn).Scan(&got)
		if err != nil {
			t.Fatalf("read back %q: %v", r.qn, err)
		}
		want := string(shared.ClassifyNode(shared.SymbolID(r.qn), r.path))
		if got != want {
			t.Errorf("backfill classified (%q, %q) as %q; ClassifyNode says %q",
				r.qn, r.path, got, want)
		}
	}
}

// backfillStatementFromMigration0019 extracts the anchor UPDATE out of the
// migration file itself, so the test exercises the shipped SQL rather than a
// paraphrase of it that can rot independently.
func backfillStatementFromMigration0019(t *testing.T) string {
	t.Helper()
	body, err := schemaFS.ReadFile("schema/0019_taxonomy_declaration_anchor.up.sql")
	if err != nil {
		t.Fatalf("read migration 0019: %v", err)
	}
	const marker = "UPDATE symbols SET node_class = 'anchor'"
	i := strings.Index(string(body), marker)
	if i < 0 {
		t.Fatal("migration 0019 no longer contains the anchor backfill UPDATE")
	}
	rest := string(body)[i:]
	end := strings.Index(rest, ";")
	if end < 0 {
		t.Fatal("anchor backfill UPDATE is unterminated")
	}
	return rest[:end+1]
}

// The backfill's LIKE patterns and shared.AnchorPrefixes are two spellings
// of one closed set. Nothing but this test stops a scanner author from
// adding a prefix to the Go set and leaving every pre-existing row in an
// upgraded store misclassified.
func TestMigration0019_BackfillCoversEveryAnchorPrefix(t *testing.T) {
	body, err := schemaFS.ReadFile("schema/0019_taxonomy_declaration_anchor.up.sql")
	if err != nil {
		t.Fatalf("read migration 0019: %v", err)
	}
	backfill := backfillStatementFromMigration0019(t)

	// Every prefix in the Go set must appear as a LIKE pattern against
	// BOTH columns -- pyscan marks anchors by path, the sqlc mapper marks
	// them by id, and a set that covers only one of those columns silently
	// misses half the anchors.
	for _, p := range shared.AnchorPrefixes {
		for _, col := range []string{"qualified_name", "file_path"} {
			want := col + " LIKE '" + p + "%'"
			if !strings.Contains(backfill, want) {
				t.Errorf("migration 0019 backfill is missing %q", want)
			}
		}
	}

	// And the reverse: no LIKE pattern in the backfill may name a prefix
	// the Go set does not know about, or a store rebuilt by a scan would
	// classify that row differently from one carried across the migration.
	re := regexp.MustCompile(`LIKE '([a-z]+:)%'`)
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(backfill, -1) {
		seen[m[1]] = true
	}
	known := map[string]bool{}
	for _, p := range shared.AnchorPrefixes {
		known[p] = true
	}
	var extra []string
	for p := range seen {
		if !known[p] {
			extra = append(extra, p)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("migration 0019 backfills prefixes absent from shared.AnchorPrefixes: %v", extra)
	}

	// The trigger's closed set has to be the same closed set.
	for _, class := range []shared.NodeClass{shared.NodeClassDeclaration, shared.NodeClassAnchor} {
		if !strings.Contains(string(body), "'"+string(class)+"'") {
			t.Errorf("migration 0019 never mentions node class %q", class)
		}
	}
}
