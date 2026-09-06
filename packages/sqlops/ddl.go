package sqlops

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sosalejandro/atlas/packages/shared"
)

// IndexOrigin says which declaration produced an index. It matters for the
// advisories: an index Atlas inferred from a PRIMARY KEY is as real as one
// spelled CREATE INDEX, but a reader who sees "no index on this column" is
// entitled to know which declarations Atlas actually read.
type IndexOrigin string

// The closed set of index origins.
const (
	OriginCreateIndex      IndexOrigin = "create-index"
	OriginPrimaryKey       IndexOrigin = "primary-key"
	OriginUniqueConstraint IndexOrigin = "unique-constraint"
)

// SchemaTable is one table Atlas found a CREATE TABLE for.
type SchemaTable struct {
	Name     string              `json:"name"`
	Position shared.FilePosition `json:"position"`
}

// Index is one index Atlas can see. Columns are in declaration order, which is
// the only order that matters: an index serves a lookup on its leading column.
type Index struct {
	Table     string              `json:"table"`
	Name      string              `json:"name"`
	Columns   []string            `json:"columns"`
	Unique    bool                `json:"unique"`
	Predicate string              `json:"predicate,omitempty"`
	Origin    IndexOrigin         `json:"origin"`
	Position  shared.FilePosition `json:"position"`
}

// Schema is the DDL Atlas managed to read. It is explicitly a partial view:
// Known reports whether a given table was in the files scanned, and every
// index check consults it first so an unscanned schema produces "did not
// check", never "no index exists".
type Schema struct {
	Tables  []SchemaTable `json:"tables"`
	Indexes []Index       `json:"indexes"`
	// Files lists the DDL files that were read, so a report can say what the
	// index checks were based on.
	Files []string `json:"files,omitempty"`

	byTable map[string]bool
}

// NewSchema builds a Schema from rows loaded elsewhere -- the persisted
// inventory, typically. It exists so a consumer reading `sql_tables` and
// `sql_indexes` back out of the store gets a Schema whose Known() is
// populated; a Schema assembled by struct literal would report every table as
// unknown and turn every index check into a false "did not run".
func NewSchema(tables []SchemaTable, indexes []Index) Schema {
	s := Schema{Tables: tables, Indexes: indexes, byTable: make(map[string]bool, len(tables))}
	for _, t := range tables {
		s.byTable[t.Name] = true
	}
	return s
}

// Known reports whether the schema scan saw a CREATE TABLE for name.
func (s Schema) Known(table string) bool { return s.byTable[table] }

// Empty reports that no DDL was read at all -- the signal that every index
// check should be reported as not-run rather than as passing.
func (s Schema) Empty() bool { return len(s.Tables) == 0 }

// LeadingIndexFor returns an index on table whose FIRST column is one of the
// filtered columns.
//
// Leading-column-only is the conservative rule and it is the correct one. An
// index on (tenant_id, created_at) cannot serve a lookup that filters only
// created_at; treating any listed column as covered would let the single most
// common real-world index mistake pass silently, which is the whole reason
// this check exists.
func (s Schema) LeadingIndexFor(table string, filtered map[string]bool) (Index, bool) {
	for _, ix := range s.Indexes {
		if ix.Table != table || len(ix.Columns) == 0 {
			continue
		}
		if filtered[ix.Columns[0]] {
			return ix, true
		}
	}
	return Index{}, false
}

// ParseSchemaDirs reads every .sql file under each dir (recursively) and
// extracts the tables and indexes it declares. Paths in the result are
// relative to root so stored rows stay portable across worktrees.
//
// A directory that does not exist is skipped rather than failing: the caller
// probes conventional locations (db/migrations, the sqlc schema path) and most
// repositories have only one of them.
//
// Rollback migrations are NOT read. A `*.down.sql` file undoes its `up`
// sibling; reading both leaves the inventory holding a table that was created
// once and dropped once, so the "26 tables, 65 indexes" line overstates the
// schema by exactly the migrations that have a rollback. Within the files that
// ARE read, statements apply in order: a later DROP or RENAME rewrites what an
// earlier CREATE recorded, so the inventory is the schema as of the last
// migration rather than the union of everything ever declared.
func ParseSchemaDirs(dirs []string, root string) (Schema, error) {
	sc := Schema{byTable: map[string]bool{}}
	for _, dir := range dirs {
		files, err := collectSQLFiles(dir)
		if err != nil {
			return Schema{}, err
		}
		for _, f := range files {
			if isRollbackMigration(f) {
				continue
			}
			if err := sc.addFile(f, root); err != nil {
				return Schema{}, err
			}
		}
	}
	sort.Slice(sc.Tables, func(i, j int) bool { return sc.Tables[i].Name < sc.Tables[j].Name })
	sort.Slice(sc.Indexes, func(i, j int) bool {
		if sc.Indexes[i].Table != sc.Indexes[j].Table {
			return sc.Indexes[i].Table < sc.Indexes[j].Table
		}
		return sc.Indexes[i].Name < sc.Indexes[j].Name
	})
	return sc, nil
}

func (s *Schema) addFile(path, root string) error {
	b, err := os.ReadFile(path) //nolint:gosec // paths come from a directory walk the caller chose.
	if err != nil {
		return fmt.Errorf("read schema file %s: %w", path, err)
	}
	rel := relativeTo(root, path)
	s.Files = append(s.Files, rel)
	for _, raw := range splitStatements(string(b)) {
		pos := shared.FilePosition{Path: rel, Line: raw.Line}
		s.absorb(lex(raw.Text), pos)
	}
	return nil
}

// absorb routes one DDL statement to the right extractor.
//
// CREATE adds; DROP and ALTER ... RENAME TO take away. A migration set that
// drops a table must not leave it in the inventory, or every count built on
// the inventory overstates the schema and `sql.orphan-table` fires on a table
// that no longer exists. Everything else is ignored -- ALTER TABLE ADD COLUMN,
// views, triggers and data seeds all live in migration files and none of them
// change the index picture Atlas reasons about. The inventory is therefore
// exact in its tables and indexes and additive in its columns: an ALTER that
// drops or renames a column is not applied.
func (s *Schema) absorb(toks []sqlToken, pos shared.FilePosition) {
	if len(toks) == 0 || toks[0].kind != tokKeyword {
		return
	}
	switch toks[0].val {
	case "CREATE":
		s.absorbCreate(toks, pos)
	case "DROP":
		s.absorbDrop(toks)
	case "ALTER":
		s.absorbAlter(toks)
	}
}

func (s *Schema) absorbCreate(toks []sqlToken, pos shared.FilePosition) {
	i := skipKeywords(toks, 0, "CREATE", "TEMP", "TEMPORARY")
	if i >= len(toks) || toks[i].kind != tokKeyword {
		return
	}
	switch toks[i].val {
	case "TABLE":
		s.absorbTable(toks, i+1, pos)
	case "UNIQUE":
		if j := skipKeywords(toks, i+1, "INDEX"); j != i+1 {
			s.absorbIndex(toks, j, pos, true)
		}
	case "INDEX":
		s.absorbIndex(toks, i+1, pos, false)
	}
}

// absorbDrop applies `DROP TABLE [IF EXISTS] a, b` and
// `DROP INDEX [CONCURRENTLY] [IF EXISTS] name`.
func (s *Schema) absorbDrop(toks []sqlToken) {
	i := skipKeywords(toks, 0, "DROP")
	if i >= len(toks) || toks[i].kind != tokKeyword {
		return
	}
	kind := toks[i].val
	i = skipKeywords(toks, skipConcurrently(toks, i+1), "IF", "EXISTS")
	for _, name := range droppedNames(toks, i) {
		switch kind {
		case "TABLE":
			s.dropTable(name)
		case "INDEX":
			s.dropIndex(name)
		}
	}
}

// droppedNames reads the comma-separated object list of a DROP, stopping at
// the first token that is not part of it -- CASCADE, RESTRICT, or the
// `ON table` of MySQL's DROP INDEX.
func droppedNames(toks []sqlToken, i int) []string {
	var out []string
	for i < len(toks) {
		name, next := readQualifiedName(toks, i)
		if name == "" {
			return out
		}
		out = append(out, name)
		if next >= len(toks) || toks[next].kind != tokPunct || toks[next].val != "," {
			return out
		}
		i = next + 1
	}
	return out
}

// absorbAlter applies `ALTER TABLE [IF EXISTS] [ONLY] old RENAME TO new`.
//
// Only the table rename is applied. `RENAME COLUMN` is deliberately skipped:
// rewriting an index definition Atlas never re-read from a column rename would
// be a guess dressed as a fact, and the index columns are what the
// missing-index check reasons over.
func (s *Schema) absorbAlter(toks []sqlToken) {
	i := skipKeywords(toks, 0, "ALTER")
	if i >= len(toks) || toks[i].kind != tokKeyword || toks[i].val != "TABLE" {
		return
	}
	i = skipKeywords(toks, i+1, "IF", "EXISTS", "ONLY")
	from, i := readQualifiedName(toks, i)
	if from == "" || !isWord(toks, i, "RENAME") || !isWord(toks, i+1, "TO") {
		return
	}
	if to, _ := readQualifiedName(toks, i+2); to != "" {
		s.renameTable(from, to)
	}
}

// dropTable forgets a table and every index that served it.
func (s *Schema) dropTable(name string) {
	if !s.byTable[name] {
		return
	}
	delete(s.byTable, name)
	kept := s.Tables[:0]
	for _, t := range s.Tables {
		if t.Name != name {
			kept = append(kept, t)
		}
	}
	s.Tables = kept
	keptIx := s.Indexes[:0]
	for _, ix := range s.Indexes {
		if ix.Table != name {
			keptIx = append(keptIx, ix)
		}
	}
	s.Indexes = keptIx
}

// dropIndex forgets one index by name. A synthetic constraint index is never
// matched: `DROP INDEX users_pk` names a real declaration, and the ones Atlas
// inferred from PRIMARY KEY carry names it made up.
func (s *Schema) dropIndex(name string) {
	kept := s.Indexes[:0]
	for _, ix := range s.Indexes {
		if ix.Name != name || ix.Origin != OriginCreateIndex {
			kept = append(kept, ix)
		}
	}
	s.Indexes = kept
}

func (s *Schema) renameTable(from, to string) {
	if !s.byTable[from] {
		return
	}
	delete(s.byTable, from)
	s.byTable[to] = true
	for i := range s.Tables {
		if s.Tables[i].Name == from {
			s.Tables[i].Name = to
		}
	}
	for i := range s.Indexes {
		if s.Indexes[i].Table == from {
			s.Indexes[i].Table = to
		}
	}
}

func (s *Schema) absorbTable(toks []sqlToken, i int, pos shared.FilePosition) {
	i = skipKeywords(toks, i, "IF", "NOT", "EXISTS")
	name, i := readQualifiedName(toks, i)
	if name == "" {
		return
	}
	s.Tables = append(s.Tables, SchemaTable{Name: name, Position: pos})
	s.byTable[name] = true
	s.Indexes = append(s.Indexes, tableConstraintIndexes(toks, i, name, pos)...)
}

// absorbIndex reads `CREATE [UNIQUE] INDEX [CONCURRENTLY] [IF NOT EXISTS]
// name ON table (cols) [WHERE ...]`.
//
// The `ON` is required rather than skipped-if-present, and CONCURRENTLY is
// stepped over explicitly. Postgres' CONCURRENTLY is not a reserved word here,
// so it lexes as an identifier; taking whatever follows INDEX as the name and
// whatever follows that as the table recorded
// `CREATE INDEX CONCURRENTLY i ON t` as an index named `i` against a table
// called `i`, which does not exist -- while t went on looking unindexed and
// the missing-index advisory fired on it. Anchoring the table on the keyword
// means a spelling atlas has not met records nothing rather than something
// false.
func (s *Schema) absorbIndex(toks []sqlToken, i int, pos shared.FilePosition, unique bool) {
	i = skipKeywords(toks, skipConcurrently(toks, i), "IF", "NOT", "EXISTS")
	name, i := readQualifiedName(toks, i)
	if name == "" || !isKeyword(toks, i, "ON") {
		return
	}
	table, i := readQualifiedName(toks, i+1)
	if table == "" {
		return
	}
	cols, next := identsInGroup(toks, i)
	s.Indexes = append(s.Indexes, Index{
		Table:     table,
		Name:      name,
		Columns:   cols,
		Unique:    unique,
		Predicate: partialPredicate(toks, next),
		Origin:    OriginCreateIndex,
		Position:  pos,
	})
}

// tableConstraintIndexes mines a CREATE TABLE body for the declarations that
// implicitly create an index: PRIMARY KEY and UNIQUE, in both their inline
// (`id INTEGER PRIMARY KEY`) and table-constraint (`PRIMARY KEY (a, b)`)
// spellings. Missing these would make every lookup by primary key report as
// unindexed, which is the fastest possible way to make the advisory worthless.
func tableConstraintIndexes(toks []sqlToken, i int, table string, pos shared.FilePosition) []Index {
	body, ok := parenBody(toks, i)
	if !ok {
		return nil
	}
	var out []Index
	for _, def := range splitTopLevel(body) {
		if ix, ok := constraintIndex(def, table, pos); ok {
			out = append(out, ix)
		}
	}
	return out
}

// constraintIndex turns one comma-separated element of a CREATE TABLE body
// into an index, when it declares one.
func constraintIndex(def []sqlToken, table string, pos shared.FilePosition) (Index, bool) {
	if len(def) == 0 {
		return Index{}, false
	}
	pk := hasKeywordPair(def, "PRIMARY", "KEY")
	uk := hasKeyword(def, "UNIQUE")
	if !pk && !uk {
		return Index{}, false
	}
	origin, suffix := OriginUniqueConstraint, "_uk"
	if pk {
		origin, suffix = OriginPrimaryKey, "_pk"
	}
	cols, name := constraintColumns(def, table, suffix)
	if len(cols) == 0 {
		return Index{}, false
	}
	return Index{
		Table: table, Name: name, Columns: cols,
		Unique: true, Origin: origin, Position: pos,
	}, true
}

// constraintColumns yields the columns a constraint covers plus a synthetic
// index name. A table-level constraint lists its columns in parentheses; an
// inline one applies to the column the definition opens with.
func constraintColumns(def []sqlToken, table, suffix string) ([]string, string) {
	if def[0].kind == tokKeyword {
		cols, _ := identsInGroup(def, 0)
		return cols, table + suffix
	}
	if def[0].kind != tokIdent {
		return nil, ""
	}
	col := def[0].val
	if suffix == "_uk" {
		return []string{col}, table + "_" + col + suffix
	}
	return []string{col}, table + suffix
}

// partialPredicate renders the WHERE clause of a partial index back to text.
// It is stored verbatim and never interpreted: a partial index that does not
// cover a query's rows is a judgement Atlas is not equipped to make, and
// showing the predicate lets the reader make it.
func partialPredicate(toks []sqlToken, i int) string {
	i = skipKeywords(toks, i, "WHERE")
	var parts []string
	for ; i < len(toks); i++ {
		switch toks[i].kind {
		case tokString:
			parts = append(parts, "'...'")
		case tokKeyword:
			parts = append(parts, toks[i].val)
		default:
			parts = append(parts, toks[i].val)
		}
	}
	return strings.Join(parts, " ")
}

// --- token helpers -------------------------------------------------------

// skipKeywords advances past the given keywords in order, tolerating any that
// are absent (`IF NOT EXISTS` is optional, `TEMP` is optional).
func skipKeywords(toks []sqlToken, i int, words ...string) int {
	for _, w := range words {
		if i < len(toks) && toks[i].kind == tokKeyword && toks[i].val == w {
			i++
		}
	}
	return i
}

// skipConcurrently steps over Postgres' CONCURRENTLY. It is not in the keyword
// set on purpose -- putting it there would make a column of that name vanish
// from every predicate -- so it arrives as a bare identifier.
func skipConcurrently(toks []sqlToken, i int) int {
	if isWord(toks, i, "CONCURRENTLY") {
		return i + 1
	}
	return i
}

// isKeyword reports that the token at i is exactly the given reserved word.
func isKeyword(toks []sqlToken, i int, word string) bool {
	return i >= 0 && i < len(toks) && toks[i].kind == tokKeyword && toks[i].val == word
}

// isWord is isKeyword's tolerant sibling: it also matches an identifier,
// because RENAME, TO and CONCURRENTLY are not in the keyword set (the clause
// walker has no use for them) and therefore lex as identifiers.
func isWord(toks []sqlToken, i int, word string) bool {
	if i < 0 || i >= len(toks) {
		return false
	}
	switch toks[i].kind {
	case tokKeyword, tokIdent:
		return strings.EqualFold(toks[i].val, word)
	}
	return false
}

// parenBody returns the tokens inside the parenthesised group starting at or
// after i.
func parenBody(toks []sqlToken, i int) ([]sqlToken, bool) {
	for ; i < len(toks); i++ {
		if toks[i].kind == tokPunct && toks[i].val == "(" {
			end := skipParenGroup(toks, i)
			if end > i+1 {
				return toks[i+1 : end-1], true
			}
			return nil, false
		}
	}
	return nil, false
}

// identsInGroup returns the bare identifiers of the parenthesised list at or
// after i, plus the index just past the group. Expression indexes
// (`lower(email)`) contribute their inner identifier, which is the closest
// honest answer available without an expression grammar.
func identsInGroup(toks []sqlToken, i int) ([]string, int) {
	for ; i < len(toks); i++ {
		if toks[i].kind == tokPunct && toks[i].val == "(" {
			break
		}
	}
	if i >= len(toks) {
		return nil, i
	}
	end := skipParenGroup(toks, i)
	var out []string
	for _, def := range splitTopLevel(toks[i+1 : max(end-1, i+1)]) {
		for _, t := range def {
			if t.kind == tokIdent {
				out = append(out, t.val)
				break
			}
		}
	}
	return out, end
}

// splitTopLevel splits a token slice on commas at nesting depth zero.
func splitTopLevel(toks []sqlToken) [][]sqlToken {
	var out [][]sqlToken
	depth, start := 0, 0
	for i, t := range toks {
		if t.kind != tokPunct {
			continue
		}
		switch t.val {
		case "(":
			depth++
		case ")":
			depth--
		case ",":
			if depth == 0 {
				out = append(out, toks[start:i])
				start = i + 1
			}
		}
	}
	if start < len(toks) {
		out = append(out, toks[start:])
	}
	return out
}

func hasKeyword(toks []sqlToken, w string) bool {
	for _, t := range toks {
		if t.kind == tokKeyword && t.val == w {
			return true
		}
	}
	return false
}

func hasKeywordPair(toks []sqlToken, a, b string) bool {
	for i := 0; i+1 < len(toks); i++ {
		if toks[i].kind == tokKeyword && toks[i].val == a &&
			toks[i+1].kind == tokKeyword && toks[i+1].val == b {
			return true
		}
	}
	return false
}

// --- statement splitting -------------------------------------------------

// rawStatement is one `;`-terminated statement plus the 1-based line it starts
// on. The line is what every advisory's file:line anchor is built from.
type rawStatement struct {
	Text string
	Line int
}

// splitStatements cuts src on semicolons that are not inside a string,
// identifier quote or comment. Splitting on a naive strings.Split would break
// on the first `;` in a comment -- which real migration files contain -- and
// silently truncate the schema.
func splitStatements(src string) []rawStatement {
	var out []rawStatement
	line, start, startLine := 1, 0, 1
	for i := 0; i < len(src); {
		if next, ok := skipTrivia(src, i); ok {
			line += strings.Count(src[i:next], "\n")
			if start == i {
				start, startLine = next, line
			}
			i = next
			continue
		}
		switch c := src[i]; c {
		case '\'', '"', '`':
			next := skipQuoted(src, i, c)
			line += strings.Count(src[i:next], "\n")
			i = next
		case ';':
			if text := strings.TrimSpace(src[start:i]); text != "" {
				out = append(out, rawStatement{Text: text, Line: startLine})
			}
			i++
			start, startLine = i, line
		default:
			i++
		}
	}
	if text := strings.TrimSpace(src[start:]); text != "" {
		out = append(out, rawStatement{Text: text, Line: startLine})
	}
	return out
}

// collectSQLFiles walks dir for .sql files. A missing directory yields no
// files and no error.
func collectSQLFiles(dir string) ([]string, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, nil //nolint:nilerr // a missing probe location is not an error.
	}
	var files []string
	walkErr := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // an unreadable subtree is skipped, not fatal.
		}
		if strings.HasSuffix(p, ".sql") {
			files = append(files, p)
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walk schema dir %s: %w", dir, walkErr)
	}
	sort.Strings(files)
	return files, nil
}

// isRollbackMigration recognises the `*.down.sql` naming every migration
// runner (golang-migrate, dbmate, node-pg-migrate) gives the statements that
// undo a migration rather than apply it.
func isRollbackMigration(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return base == "down.sql" || strings.HasSuffix(base, ".down.sql")
}

// relativeTo renders path relative to root with forward slashes, falling back
// to the absolute path when the two share no prefix.
func relativeTo(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}
