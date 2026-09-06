package sqlops

import (
	"strings"
	"testing"
)

const twoCallsOnOneLine = `package repo

import (
	"context"
	"database/sql"
)

type R struct{ db *sql.DB }

func (r *R) Both(ctx context.Context, id int64) error {
	if _, err := r.db.ExecContext(ctx, "UPDATE users SET seen_at = now() WHERE id = $1", id); err != nil {
		return err
	}
	_, e1 := r.db.ExecContext(ctx, "DELETE FROM sessions WHERE id = $1", id); _, e2 := r.db.ExecContext(ctx, "DELETE FROM tokens WHERE id = $1", id)
	if e1 != nil {
		return e1
	}
	return e2
}
`

// The ref is UNIQUE in the store, so two operations that share one must lose
// one of themselves on write. Two database/sql calls on a single source line
// share everything the fingerprint was made of -- source, file, line and
// enclosing symbol -- and the second silently replaced the first, shrinking
// the inventory and the denominator the resolved fraction is computed over.
func TestOperationRef_DisambiguatesCallsOnOneLine(t *testing.T) {
	ops := extractSourceOps(t, twoCallsOnOneLine)
	if len(ops) != 3 {
		t.Fatalf("extracted %d operations, want 3", len(ops))
	}

	refs := map[string]Operation{}
	for _, op := range ops {
		if prev, dup := refs[op.Ref()]; dup {
			t.Fatalf("ref %q is shared by %q and %q; one of them cannot be stored",
				op.Ref(), prev.SQL, op.SQL)
		}
		refs[op.Ref()] = op
	}

	// The common case keeps its readable shape: no suffix on a line with one
	// operation on it.
	var suffixed int
	for ref := range refs {
		if strings.Contains(ref, "#") {
			suffixed++
		}
	}
	if suffixed != 1 {
		t.Errorf("%d refs carry an ordinal suffix, want exactly 1: %v", suffixed, refs)
	}

	// Re-extracting the same tree must produce the same refs, or every scan
	// churns the stored rows.
	for _, op := range extractSourceOps(t, twoCallsOnOneLine) {
		if _, ok := refs[op.Ref()]; !ok {
			t.Errorf("ref %q is not stable across extractions", op.Ref())
		}
	}
}
