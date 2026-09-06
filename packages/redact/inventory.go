package redact

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"sort"
)

// Inventory is the answer to "if I send you this database, what am I
// sending?" -- taken from the database in front of you rather than from a
// document describing one.
type Inventory struct {
	// Path is the state database this inventory describes.
	Path string `json:"path"`

	// SizeBytes is the main database file. SidecarBytes is the write-ahead
	// log and shared-memory files beside it, which hold committed data that
	// has not been checkpointed and travel with the database in practice:
	// anyone copying atlas.db without atlas.db-wal may be copying a stale
	// database, which is worth knowing before quoting a size.
	SizeBytes    int64 `json:"size_bytes"`
	SidecarBytes int64 `json:"sidecar_bytes"`

	SchemaVersion int `json:"schema_version"`

	Tables []TableStat `json:"tables"`

	// Unclassified names tables and "table.column" TEXT columns present in
	// the database that this package has no description for. It should
	// always be empty (schema_test.go fails the build otherwise), and it is
	// reported anyway: an inventory that quietly omits what it does not
	// recognise is worse than no inventory, because it reads as complete.
	Unclassified []string `json:"unclassified,omitempty"`
}

// TableStat is one table's contribution to the inventory.
type TableStat struct {
	Name    string  `json:"name"`
	Purpose string  `json:"purpose"`
	Rows    int64   `json:"rows"`
	Classes []Class `json:"classes,omitempty"`
}

// SourceTextColumns returns the columns holding verbatim repository text,
// which is the part of the answer a security review actually reacts to.
func (inv Inventory) SourceTextColumns() []Column {
	var out []Column
	for _, c := range columns {
		if c.Class == ClassSourceText {
			out = append(out, c)
		}
	}
	return out
}

// NonEmptyTables returns the tables that actually hold rows, largest first.
func (inv Inventory) NonEmptyTables() []TableStat {
	out := make([]TableStat, 0, len(inv.Tables))
	for _, t := range inv.Tables {
		if t.Rows > 0 {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Rows > out[j].Rows })
	return out
}

// Take reads the inventory of the state database open on db. path is the
// file db was opened from; it is used for the size figures and echoed back,
// because a report about a database that does not say WHICH database is not
// evidence of anything.
func Take(ctx context.Context, db *sql.DB, path string) (Inventory, error) {
	inv := Inventory{Path: path}
	inv.SizeBytes, inv.SidecarBytes = fileSizes(path)

	version, err := schemaVersion(ctx, db)
	if err != nil {
		return Inventory{}, err
	}
	inv.SchemaVersion = version

	live, err := liveSchema(ctx, db)
	if err != nil {
		return Inventory{}, err
	}

	known := map[string]Table{}
	for _, t := range tables {
		known[t.Name] = t
	}
	classesByTable := map[string][]Class{}
	knownColumn := map[string]bool{}
	for _, c := range columns {
		knownColumn[c.Table+"."+c.Name] = true
		classesByTable[c.Table] = appendClass(classesByTable[c.Table], c.Class)
	}

	for _, tbl := range live {
		desc, ok := known[tbl.name]
		if !ok {
			inv.Unclassified = append(inv.Unclassified, tbl.name)
		}
		rows, err := countRows(ctx, db, tbl.name)
		if err != nil {
			return Inventory{}, err
		}
		inv.Tables = append(inv.Tables, TableStat{
			Name:    tbl.name,
			Purpose: desc.Purpose,
			Rows:    rows,
			Classes: classesByTable[tbl.name],
		})
		for _, col := range tbl.textColumns {
			if !knownColumn[tbl.name+"."+col] {
				inv.Unclassified = append(inv.Unclassified, tbl.name+"."+col)
			}
		}
	}
	sort.Strings(inv.Unclassified)
	return inv, nil
}

// liveTable is what the database says about one table, as opposed to what
// the registry says.
type liveTable struct {
	name        string
	textColumns []string
	// keyColumns are the primary-key columns, in key order. Empty for a
	// rowid table with an implicit INTEGER PRIMARY KEY that pragma reports
	// as pk=1 -- that case is covered, the empty case is a table with no
	// declared key at all, where the sweep falls back to rowid.
	keyColumns []string
}

// liveSchema enumerates the user tables and their TEXT columns.
//
// sqlite_% tables are excluded: sqlite_sequence and the autoindexes are the
// engine's bookkeeping, not atlas's data, and reporting them as content
// atlas stores would be false.
func liveSchema(ctx context.Context, db *sql.DB) ([]liveTable, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("redact: list tables: %w", err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("redact: scan table name: %w", err)
		}
		names = append(names, n)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}

	out := make([]liveTable, 0, len(names))
	for _, n := range names {
		t, err := describeTable(ctx, db, n)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func describeTable(ctx context.Context, db *sql.DB, name string) (liveTable, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT name, type, pk FROM pragma_table_info(?) ORDER BY cid`, name)
	if err != nil {
		return liveTable{}, fmt.Errorf("redact: table_info %s: %w", name, err)
	}
	out := liveTable{name: name}
	type keyed struct {
		col string
		ord int
	}
	var keys []keyed
	for rows.Next() {
		var col, typ string
		var pk int
		if err := rows.Scan(&col, &typ, &pk); err != nil {
			_ = rows.Close()
			return liveTable{}, fmt.Errorf("redact: scan table_info %s: %w", name, err)
		}
		if isTextType(typ) {
			out.textColumns = append(out.textColumns, col)
		}
		if pk > 0 {
			keys = append(keys, keyed{col, pk})
		}
	}
	if err := closeRows(rows); err != nil {
		return liveTable{}, err
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].ord < keys[j].ord })
	for _, k := range keys {
		out.keyColumns = append(out.keyColumns, k.col)
	}
	return out, nil
}

// isTextType reports whether a declared column type is TEXT.
//
// Exact match rather than SQLite's affinity rules (which would make VARCHAR,
// CLOB and even BLOB-with-a-TEXT-substring count). Every column in the atlas
// schema is declared with one of a handful of literal type names, so an
// affinity emulation here would add ways to be subtly wrong without adding
// a single column to the result.
func isTextType(declared string) bool {
	return declared == "TEXT" || declared == "text"
}

func countRows(ctx context.Context, db *sql.DB, table string) (int64, error) {
	if !safeIdent(table) {
		return 0, fmt.Errorf("redact: refusing to query table with unexpected name %q", table)
	}
	var n int64
	// The table name is interpolated because SQLite has no parameter form
	// for an identifier. safeIdent above is the guard: it rejects anything
	// that is not [A-Za-z_][A-Za-z0-9_]*, so nothing that reaches here can
	// close the quote. The names themselves come from sqlite_master, not
	// from user input.
	q := fmt.Sprintf(`SELECT COUNT(*) FROM "%s"`, table)
	if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return 0, fmt.Errorf("redact: count %s: %w", table, err)
	}
	return n, nil
}

func schemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var v int
	err := db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("redact: read schema_migrations: %w", err)
	}
	return v, nil
}

// fileSizes returns the database size and the combined size of its WAL and
// shared-memory sidecars. A missing file contributes zero rather than an
// error: a freshly checkpointed database has no -wal, and that is not a
// condition worth failing a security report over.
func fileSizes(path string) (main int64, sidecar int64) {
	if st, err := os.Stat(path); err == nil {
		main = st.Size()
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if st, err := os.Stat(path + suffix); err == nil {
			sidecar += st.Size()
		}
	}
	return main, sidecar
}

// identRe is the shape every atlas table and column name has. Used as a
// guard before an identifier is interpolated into SQL.
var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func safeIdent(s string) bool { return identRe.MatchString(s) }

func appendClass(in []Class, c Class) []Class {
	for _, existing := range in {
		if existing == c {
			return in
		}
	}
	return append(in, c)
}

// closeRows reports both the iteration error and the close error, in that
// order of interest: a scan loop that ended early because of a driver error
// otherwise looks exactly like one that ran to completion.
func closeRows(rows *sql.Rows) error {
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("redact: iterate rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("redact: close rows: %w", err)
	}
	return nil
}
