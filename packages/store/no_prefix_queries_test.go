package store

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

// Issue #112's actual defect: "is this real code" was re-derived at every
// call site by string-matching a reserved prefix on the id or the file
// path. `symbols.node_class` replaced that, and this test is what stops it
// coming back.
//
// The failure it guards against is not a wrong answer today -- it is two
// copies of one rule drifting. The moment a scanner mints a new kind of
// anchor, a query that asks the column keeps working and a query that
// pattern-matches 'external:py%' silently starts counting synthetic
// vertices as authored code, with every count still green.
//
// Scope is the query layer: the SQL this package sends to SQLite, whether
// it lives in queries/*.sql or in a Go string constant. Test files are
// exempt -- a fixture has to be able to name `external:py` to prove the
// stub still never shows up as dead code -- and so is migration 0019
// itself, which is where the rule is allowed to appear exactly once.
func TestNoQueryRederivesNodeClassFromAPrefix(t *testing.T) {
	// A prefix check looks like `LIKE 'external:py%'`, `LIKE "sql:%"`, or
	// a bare literal comparison against a reserved prefix. Match the
	// prefixes wherever they appear inside a SQL string rather than trying
	// to parse SQL: any mention of a reserved prefix in a query is either
	// a prefix check or something even less defensible.
	var patterns []*regexp.Regexp
	for _, p := range shared.AnchorPrefixes {
		patterns = append(patterns, regexp.MustCompile(regexp.QuoteMeta(p)))
	}

	offenders := map[string][]string{}

	// --- queries/*.sql -------------------------------------------------
	sqlFiles, err := filepath.Glob(filepath.Join("queries", "*.sql"))
	if err != nil {
		t.Fatalf("glob queries: %v", err)
	}
	for _, f := range sqlFiles {
		body, err := os.ReadFile(f) //nolint:gosec // fixed test-local path.
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "--") {
				continue // a comment may name the old rule; only SQL counts.
			}
			for _, re := range patterns {
				if re.MatchString(line) {
					offenders[f] = append(offenders[f], strings.TrimSpace(line))
				}
			}
		}
	}

	// --- raw SQL in this package's Go source ---------------------------
	goFiles, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob go: %v", err)
	}
	for _, f := range goFiles {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		body, err := os.ReadFile(f) //nolint:gosec // fixed test-local path.
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			for _, re := range patterns {
				if re.MatchString(trimmed) {
					offenders[f] = append(offenders[f], trimmed)
				}
			}
		}
	}

	if len(offenders) == 0 {
		return
	}
	files := make([]string, 0, len(offenders))
	for f := range offenders {
		files = append(files, f)
	}
	sort.Strings(files)
	var b strings.Builder
	b.WriteString("a query re-derives the declaration/anchor split from a " +
		"reserved id prefix. Filter on symbols.node_class instead " +
		"(shared.NodeClassDeclaration / shared.NodeClassAnchor):\n")
	for _, f := range files {
		for _, line := range offenders[f] {
			b.WriteString("  " + f + ": " + line + "\n")
		}
	}
	t.Error(b.String())
}

// The companion assertion, one level up: the store's own read path must
// actually distinguish the two classes. A store that never filters on
// node_class would pass the test above trivially, by having deleted the
// prefix checks and replaced them with nothing.
func TestFindDeadFiltersOnNodeClass(t *testing.T) {
	if !strings.Contains(deadCodeBaseSelect, "node_class") {
		t.Error("the dead-code SELECT no longer projects node_class")
	}

	s := openTestStore(t)
	ctx := t.Context()

	// An anchor with no inbound edges. Under the old prefix rule it was
	// excluded because its PATH matched 'external:py%'; under the column it
	// is excluded because ingest classified it. Same row, same verdict,
	// different mechanism -- which is the whole acceptance criterion.
	if _, err := s.Symbols().Insert(ctx, SymbolRow{
		QualifiedName: "os.path.join",
		Kind:          shared.KindFunc,
		FilePath:      "external:py",
		Line:          1,
	}); err != nil {
		t.Fatalf("seed anchor: %v", err)
	}
	// And a declaration with no inbound edges, which MUST still be found --
	// otherwise "excludes anchors" could be satisfied by excluding
	// everything.
	if _, err := s.Symbols().Insert(ctx, SymbolRow{
		QualifiedName: "pkg.orphan",
		Kind:          shared.KindFunc,
		FilePath:      "pkg/orphan.py",
		Line:          1,
	}); err != nil {
		t.Fatalf("seed declaration: %v", err)
	}

	rows, err := s.Symbols().FindDead(ctx, DeadCodeFilter{})
	if err != nil {
		t.Fatalf("FindDead: %v", err)
	}
	var names []string
	for _, r := range rows {
		names = append(names, string(r.Symbol.QualifiedName))
		if r.Symbol.NodeClass != shared.NodeClassDeclaration {
			t.Errorf("FindDead returned a %s: %s", r.Symbol.NodeClass, r.Symbol.QualifiedName)
		}
	}
	sort.Strings(names)
	if len(names) != 1 || names[0] != "pkg.orphan" {
		t.Errorf("FindDead = %v, want exactly [pkg.orphan]", names)
	}
}
