package redact

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// SweepOptions configures a sweep of the state database.
type SweepOptions struct {
	// Apply rewrites the redactable columns in place. Off by default: the
	// answer to "what am I about to send" must be obtainable without
	// modifying the thing being described.
	Apply bool
}

// Hit is one secret found in one stored value.
type Hit struct {
	Table  string `json:"table"`
	Column string `json:"column"`

	// Row is the primary key of the row the value sits in, key parts joined
	// by "/", or the rowid for a table with no declared key. It is what
	// turns "there is a credential in your database" into something an
	// operator can act on.
	Row string `json:"row"`

	// Redactable mirrors the column registry: false means atlas found the
	// secret but will not rewrite the column, because doing so would change
	// what the index means rather than what it discloses.
	Redactable bool `json:"redactable"`

	// Applied is true when this sweep actually rewrote the value.
	Applied bool `json:"applied"`

	Finding
}

// SweepReport is the outcome of a sweep, including how much was looked at.
//
// The denominators are not decoration. "No secrets found" means nothing
// without them: a sweep that read zero columns because the schema drifted
// reports exactly the same empty hit list as a clean database.
type SweepReport struct {
	Hits []Hit `json:"hits"`

	ColumnsRead int   `json:"columns_read"`
	RowsRead    int64 `json:"rows_read"`

	// ValuesRewritten counts DISTINCT values replaced, not rows: one
	// credential hardcoded in nine queries is one value and nine rows.
	//
	// It is computed in BOTH modes. Without SweepOptions.Apply it is what
	// the sweep WOULD replace -- which is the only number a dry run is for.
	// Leaving it at zero there made `--dry-run` report "would change
	// nothing" over a store full of secrets, which is the one answer a dry
	// run must never give. Use Hit.Applied, not this figure, to tell
	// whether the database was actually written.
	ValuesRewritten int `json:"values_rewritten"`

	// Unredactable counts hits in columns atlas will not rewrite. These are
	// the ones that need a source change, and they are the ones a report
	// that only counted successful redactions would hide.
	Unredactable int `json:"unredactable"`

	// ColumnsNotSwept names the TEXT columns the live schema has and the
	// compiled-in registry does not. Nothing read them, so no claim about
	// them was made or can be made.
	//
	// It exists because "no secrets found" and "nothing was looked at" print
	// identically otherwise. In a released build this list is empty --
	// schema_test.go compares the registry against a freshly migrated store
	// and fails the build on drift -- so a non-empty list means the binary
	// is older than the database in front of it, which is precisely when the
	// report needs to say so out loud.
	ColumnsNotSwept []string `json:"columns_not_swept,omitempty"`
}

// Clean reports whether the sweep found nothing.
func (r SweepReport) Clean() bool { return len(r.Hits) == 0 }

// Sweep reads every REGISTERED TEXT column the live schema also has,
// looking for secrets, and -- with SweepOptions.Apply -- replaces the ones
// it is allowed to rewrite.
//
// Registered, not "every TEXT column in the file": the loop below iterates
// the compiled-in registry, so a column the schema has and the registry
// does not is never read. In a released build that set is empty, because
// schema_test.go compares the registry against a freshly migrated store and
// fails the build on drift -- but the guarantee is a build-time one, and
// this function is also run by binaries older than the database in front of
// them. Whatever it did not read is named in SweepReport.ColumnsNotSwept
// rather than left to be inferred from a clean hit list.
//
// Within that set every registered column is read, not only the ones
// expected to carry source text. A credential in a file path or a symbol
// name is a real disclosure even though it is not one atlas can fix.
func Sweep(ctx context.Context, db *sql.DB, opts SweepOptions) (SweepReport, error) {
	live, err := liveSchema(ctx, db)
	if err != nil {
		return SweepReport{}, err
	}
	registered := map[string]bool{}
	for _, c := range columns {
		registered[c.Table+"."+c.Name] = true
	}
	keysByTable := map[string][]string{}
	present := map[string]bool{}

	var rep SweepReport
	for _, t := range live {
		keysByTable[t.name] = t.keyColumns
		for _, c := range t.textColumns {
			present[t.name+"."+c] = true
			if !registered[t.name+"."+c] {
				rep.ColumnsNotSwept = append(rep.ColumnsNotSwept, t.name+"."+c)
			}
		}
	}
	sort.Strings(rep.ColumnsNotSwept)
	pending := map[rewrite]string{}
	for _, col := range columns {
		// A registered column the live schema does not have is not an
		// error here: Take reports the mismatch, and refusing to sweep the
		// rest of the database over it would be the wrong trade.
		if !present[col.Table+"."+col.Name] {
			continue
		}
		hits, rows, err := sweepColumn(ctx, db, col, keysByTable[col.Table], pending)
		if err != nil {
			return SweepReport{}, err
		}
		rep.ColumnsRead++
		rep.RowsRead += rows
		rep.Hits = append(rep.Hits, hits...)
	}
	for i := range rep.Hits {
		if !rep.Hits[i].Redactable {
			rep.Unredactable++
		}
	}
	// Counted before the apply branch, not inside it: the dry run's whole
	// job is to report this number, and a number only the writing path
	// computes is a number the dry run gets wrong.
	rep.ValuesRewritten = len(pending)
	if !opts.Apply || len(pending) == 0 {
		return rep, nil
	}
	if err := applyRewrites(ctx, db, pending); err != nil {
		return SweepReport{}, err
	}
	for i := range rep.Hits {
		rep.Hits[i].Applied = rep.Hits[i].Redactable
	}
	return rep, nil
}

// rewrite identifies one value to replace. Keyed by the OLD value rather
// than by row, so a credential repeated across rows is one rewrite: that is
// what makes ValuesRewritten a count of leaks rather than of occurrences,
// and it is why the UPDATE can match on the value instead of threading a
// composite primary key through every table shape.
type rewrite struct {
	table  string
	column string
	old    string
}

// sweepColumn scans one column, returning its hits and the number of rows
// read. Values needing a rewrite are recorded in pending.
func sweepColumn(
	ctx context.Context, db *sql.DB, col Column, keys []string, pending map[rewrite]string,
) ([]Hit, int64, error) {
	if !safeIdent(col.Table) || !safeIdent(col.Name) {
		return nil, 0, fmt.Errorf("redact: unexpected identifier %q.%q", col.Table, col.Name)
	}
	query, keyCount, err := selectColumnSQL(col, keys)
	if err != nil {
		return nil, 0, err
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, 0, fmt.Errorf("redact: read %s.%s: %w", col.Table, col.Name, err)
	}

	var (
		hits  []Hit
		count int64
	)
	scan := make([]any, keyCount+1)
	holders := make([]sql.NullString, keyCount+1)
	for i := range holders {
		scan[i] = &holders[i]
	}
	for rows.Next() {
		if err := rows.Scan(scan...); err != nil {
			_ = rows.Close()
			return nil, 0, fmt.Errorf("redact: scan %s.%s: %w", col.Table, col.Name, err)
		}
		count++
		value := holders[keyCount].String
		result := Text(value)
		if !result.Redacted() {
			continue
		}
		row := rowKey(holders[:keyCount])
		for _, f := range result.Findings {
			hits = append(hits, Hit{
				Table: col.Table, Column: col.Name, Row: row,
				Redactable: col.Redactable, Finding: f,
			})
		}
		if col.Redactable {
			pending[rewrite{col.Table, col.Name, value}] = result.Text
		}
	}
	if err := closeRows(rows); err != nil {
		return nil, 0, err
	}
	return hits, count, nil
}

// selectColumnSQL builds the read query for one column, returning the number
// of key columns it selects ahead of the value.
//
// Identifiers are interpolated because SQLite has no bind form for them;
// every one of them has passed safeIdent and originates in this package's
// compiled-in registry or in sqlite_master, never in user input.
func selectColumnSQL(col Column, keys []string) (string, int, error) {
	selected := make([]string, 0, len(keys)+1)
	for _, k := range keys {
		if !safeIdent(k) {
			return "", 0, fmt.Errorf("redact: unexpected key column %q on %s", k, col.Table)
		}
		selected = append(selected, `"`+k+`"`)
	}
	if len(selected) == 0 {
		// A table with no declared primary key still has a rowid, and a hit
		// with no row identity is not actionable.
		selected = append(selected, "rowid")
	}
	keyCount := len(selected)
	selected = append(selected, `"`+col.Name+`"`)
	q := fmt.Sprintf(`SELECT %s FROM "%s" WHERE "%s" IS NOT NULL AND "%s" <> ''`,
		strings.Join(selected, ", "), col.Table, col.Name, col.Name)
	return q, keyCount, nil
}

func rowKey(parts []sql.NullString) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, p.String)
	}
	return strings.Join(out, "/")
}

// applyRewrites replaces every pending value in one transaction.
//
// All-or-nothing on purpose: a partial redaction leaves the operator with a
// database that is neither the one they inspected nor a clean one, and no
// way to tell which rows are which.
func applyRewrites(ctx context.Context, db *sql.DB, pending map[rewrite]string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("redact: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for key, replacement := range pending {
		// Matching on the old value rather than on a primary key is what
		// lets one statement clean every row holding the same credential,
		// and it is safe precisely because no redactable column
		// participates in a key or a unique index (enforced by
		// TestRedactableColumns_AreNeverPartOfAKey).
		q := fmt.Sprintf(`UPDATE "%s" SET "%s" = ? WHERE "%s" = ?`, key.table, key.column, key.column)
		if _, err := tx.ExecContext(ctx, q, replacement, key.old); err != nil {
			return fmt.Errorf("redact: rewrite %s.%s: %w", key.table, key.column, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("redact: commit redaction: %w", err)
	}
	return nil
}
