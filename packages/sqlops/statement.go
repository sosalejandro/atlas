package sqlops

import "sort"

// StatementKind is the verb a statement leads with. "other" covers DDL,
// transaction control and anything else Atlas records but does not reason
// about; it is deliberately not an error, because a query it cannot classify
// must still appear in the inventory.
type StatementKind string

// The closed set of statement kinds.
const (
	KindSelect StatementKind = "select"
	KindInsert StatementKind = "insert"
	KindUpdate StatementKind = "update"
	KindDelete StatementKind = "delete"
	KindOther  StatementKind = "other"
)

// Access says whether a statement reads a table or writes it.
type Access string

// The closed set of table accesses.
const (
	AccessRead  Access = "read"
	AccessWrite Access = "write"
)

// TableAccess is one table the statement touches, and how.
type TableAccess struct {
	Table  string `json:"table"`
	Access Access `json:"access"`
}

// PredicateClause distinguishes a filter from a join condition. The two are
// kept apart because a missing index on a join key and a missing index on a
// filter have different costs and different fixes.
type PredicateClause string

// The closed set of predicate clauses.
const (
	ClauseWhere PredicateClause = "where"
	ClauseJoin  PredicateClause = "join"
)

// Predicate is one column comparison Atlas could read out of a WHERE or JOIN
// clause. Table is empty when the alias could not be resolved to a table --
// an empty Table means "unknown", never "no table", and the index checks
// treat it as a reason to skip rather than a reason to complain.
type Predicate struct {
	Table    string          `json:"table,omitempty"`
	Column   string          `json:"column"`
	Operator string          `json:"operator"`
	Clause   PredicateClause `json:"clause"`
	// Bound reports that the right-hand side is a bind parameter, i.e. the
	// value comes from the caller. That is what separates keyset pagination
	// from a constant range filter.
	Bound bool `json:"bound"`
}

// OffsetBound records where an OFFSET's value comes from. A caller-supplied
// offset is the one that degrades with depth; a constant one does not.
type OffsetBound string

// The closed set of offset bindings.
const (
	OffsetNone       OffsetBound = "none"
	OffsetParameter  OffsetBound = "parameter"
	OffsetLiteral    OffsetBound = "literal"
	OffsetExpression OffsetBound = "expression"
)

// Statement is everything Atlas extracts from one SQL statement.
type Statement struct {
	Kind       StatementKind `json:"kind"`
	Tables     []TableAccess `json:"tables,omitempty"`
	Predicates []Predicate   `json:"predicates,omitempty"`
	OrderBy    []string      `json:"order_by,omitempty"`
	ParamCount int           `json:"param_count"`
	HasLimit   bool          `json:"has_limit"`
	HasOffset  bool          `json:"has_offset"`
	HasOrderBy bool          `json:"has_order_by"`
	SelectStar bool          `json:"select_star"`
	// Keyset reports cursor pagination: a LIMIT, plus a caller-bound range
	// predicate on the column the statement orders by FIRST. All three parts
	// are required -- see detectKeyset for why anything weaker is a filter
	// wearing a cursor's clothes.
	Keyset      bool        `json:"keyset"`
	OffsetBound OffsetBound `json:"offset_bound"`
}

// clause is the position in the statement the walker currently occupies.
type clause int

const (
	clNone clause = iota
	clSelect
	clFrom
	clJoin
	clOn
	clWhere
	clHaving
	clOrder
	clLimit
	clOffset
	clInto
	clUpdateTarget
	clSet
	clOther
)

// clauseFor maps a clause-opening keyword onto the state it enters. Keywords
// absent from the map leave the state alone, which is what makes the walker
// tolerant of dialect syntax it has never seen.
var clauseFor = map[string]clause{
	"SELECT": clSelect, "FROM": clFrom, "JOIN": clJoin, "ON": clOn,
	"USING": clOn, "WHERE": clWhere, "HAVING": clHaving, "ORDER": clOrder,
	"LIMIT": clLimit, "OFFSET": clOffset, "INTO": clInto,
	"UPDATE": clUpdateTarget, "SET": clSet, "GROUP": clOther,
	"VALUES": clOther, "RETURNING": clOther, "CONFLICT": clOther,
}

// verbFor maps the leading keyword onto the statement kind.
var verbFor = map[string]StatementKind{
	"SELECT": KindSelect, "INSERT": KindInsert,
	"UPDATE": KindUpdate, "DELETE": KindDelete,
}

// tableClauses are the states in which a bare identifier names a table.
var tableClauses = map[clause]Access{
	clFrom: AccessRead, clJoin: AccessRead,
	clInto: AccessWrite, clUpdateTarget: AccessWrite,
}

// AnalyzeStatement extracts the shape of one SQL statement. The second return
// is false when the text has no recognisable leading SQL keyword at all.
//
// The false case is load-bearing. If a fragment Atlas cannot read came back as
// a zero-valued Statement, its Kind would be the empty string and its
// HasLimit false -- and a later pass would happily report "unbounded read" on
// something that is not even a query. Refusing to classify is the only honest
// answer, and the caller records it as unresolved instead.
func AnalyzeStatement(src string) (Statement, bool) {
	toks := lex(src)
	if len(toks) == 0 || toks[0].kind != tokKeyword {
		return Statement{}, false
	}
	a := &analyzer{
		toks:    toks,
		ctes:    collectCTEs(toks),
		aliases: map[string]string{},
		params:  map[string]bool{},
	}
	a.run()
	if a.st.Kind == "" {
		a.st.Kind = KindOther
	}
	a.finish()
	return a.st, true
}

// LooksLikeSQL reports whether a string fragment opens with a SQL verb. The Go
// extractor uses it to decide whether an expression it could not fully resolve
// is worth recording at all -- a `fmt.Sprintf` whose format string starts with
// SELECT is unambiguously a query; one that does not is somebody else's
// business.
func LooksLikeSQL(s string) bool {
	toks := lex(s)
	if len(toks) == 0 || toks[0].kind != tokKeyword {
		return false
	}
	switch toks[0].val {
	case "SELECT", "INSERT", "UPDATE", "DELETE", "WITH", "REPLACE", "MERGE",
		"CREATE", "DROP", "ALTER", "TRUNCATE":
		return true
	}
	return false
}

// analyzer carries the walk state. It is single-use: one statement, one
// analyzer.
type analyzer struct {
	toks    []sqlToken
	st      Statement
	cl      clause
	depth   int
	ctes    map[string]bool
	aliases map[string]string
	params  map[string]bool
	// positional counts bare `?` placeholders, which unlike $1 and :name
	// cannot be deduplicated -- two `?` are two parameters.
	positional int
	// expectTable is set when a clause has just opened and the next
	// identifier names a table rather than a column.
	expectTable bool
	// deleteTarget marks the FROM that follows DELETE, whose table is
	// written rather than read.
	deleteTarget bool
	// limitSaw records the first token after LIMIT, so MySQL's
	// `LIMIT <offset>, <count>` can be recognised retroactively when the
	// comma arrives.
	limitSaw OffsetBound
}

func (a *analyzer) run() {
	for i := 0; i < len(a.toks); {
		i = a.step(i)
	}
}

// step consumes at least one token and returns the next index.
func (a *analyzer) step(i int) int {
	t := a.toks[i]
	switch t.kind {
	case tokPunct:
		a.handlePunct(i)
	case tokParam:
		a.tallyParam(t.val)
	case tokKeyword:
		return a.handleKeyword(i)
	case tokIdent:
		return a.handleIdent(i)
	case tokStar:
		a.noteStar(i)
	case tokNumber:
		a.noteLimitOperand(OffsetLiteral)
	case tokString:
		a.noteLimitOperand(OffsetExpression)
	}
	return i + 1
}

func (a *analyzer) handlePunct(i int) {
	v := a.toks[i].val
	if comparisonOps[v] && a.inPredicateClause() {
		a.recordPredicate(i, v)
		return
	}
	switch v {
	case "(":
		a.depth++
	case ")":
		a.depth--
	case ",":
		a.onComma()
	}
}

// onComma has two jobs: re-arm table collection for `FROM a, b`, and turn
// MySQL's `LIMIT 100, 20` into the offset it actually is.
func (a *analyzer) onComma() {
	if !a.atStatementLevel() {
		return
	}
	if a.cl == clLimit && a.limitSaw != "" && !a.st.HasOffset {
		a.st.HasOffset = true
		a.st.OffsetBound = a.limitSaw
		return
	}
	if _, ok := tableClauses[a.cl]; ok {
		a.expectTable = true
	}
}

func (a *analyzer) handleKeyword(i int) int {
	k := a.toks[i].val
	if v, ok := verbFor[k]; ok && a.st.Kind == "" {
		a.st.Kind = v
	}
	if keywordOps[k] && a.inPredicateClause() {
		a.recordPredicate(i, k)
	}
	switch k {
	case "DELETE":
		a.deleteTarget = true
	case "ORDER":
		a.st.HasOrderBy = true
	case "LIMIT", "FETCH":
		// Only a LIMIT on THIS statement bounds it. One inside a subquery, a
		// CTE body or an `IN (...)` list bounds that inner result set and
		// says nothing about how many rows the outer statement returns --
		// crediting it to the outer statement silently suppressed the
		// unbounded-read advisory on the query that most needed it.
		if a.atStatementLevel() {
			a.st.HasLimit = true
		}
	case "OFFSET":
		if a.atStatementLevel() {
			a.st.HasOffset = true
			a.st.OffsetBound = a.operandBound(i + 1)
		}
	case "UNION", "EXCEPT", "INTERSECT":
		// A compound query restarts the clause machine; without this a
		// `LIMIT` before the UNION would leave the second arm looking bounded.
		a.cl = clNone
	}
	// Clause transitions apply at any nesting depth: a subquery's WHERE is
	// still a WHERE, and its predicates are exactly as interesting as the
	// outer ones. The depth is consulted only where nesting changes the
	// meaning -- a comma inside parentheses, a star inside a function call.
	if c, ok := clauseFor[k]; ok {
		a.cl = c
		_, a.expectTable = tableClauses[c]
	}
	return i + 1
}

func (a *analyzer) handleIdent(i int) int {
	if _, ok := tableClauses[a.cl]; ok && a.expectTable {
		return a.readTableRef(i)
	}
	if a.cl == clOrder && a.atStatementLevel() && !followedByDot(a.toks, i) {
		a.st.OrderBy = append(a.st.OrderBy, a.toks[i].val)
	}
	return i + 1
}

// followedByDot reports that the identifier at i is a qualifier rather than
// the name being referred to, so `ORDER BY t.created_at` records the column
// and not the table alias.
func followedByDot(toks []sqlToken, i int) bool {
	return i+1 < len(toks) && toks[i+1].kind == tokPunct && toks[i+1].val == "."
}

// noteStar sets SelectStar only for a star in projection position -- directly
// after SELECT/DISTINCT/ALL, after a comma, or after the `.` of `t.*`. The
// guard keeps `SELECT price * qty` and `count(*)` from being reported as
// `SELECT *`, which would be a false advisory on a very common shape.
func (a *analyzer) noteStar(i int) {
	if a.cl != clSelect || !a.atStatementLevel() || i == 0 {
		return
	}
	switch prev := a.toks[i-1]; {
	case prev.kind == tokKeyword && (prev.val == "SELECT" || prev.val == "DISTINCT" || prev.val == "ALL"):
		a.st.SelectStar = true
	case prev.kind == tokPunct && (prev.val == "," || prev.val == "."):
		a.st.SelectStar = true
	}
}

func (a *analyzer) tallyParam(v string) {
	if v == "?" {
		a.positional++
	} else {
		a.params[v] = true
	}
	a.noteLimitOperand(OffsetParameter)
}

// noteLimitOperand remembers the first operand after LIMIT so a trailing
// comma can reclassify it as MySQL's offset.
func (a *analyzer) noteLimitOperand(b OffsetBound) {
	if a.cl == clLimit && a.limitSaw == "" {
		a.limitSaw = b
	}
}

// atStatementLevel reports that the walker is in the statement itself rather
// than inside one of its subqueries. It is what keeps a bound belonging to an
// inner result set from being read as a bound on the outer one.
//
// Depth zero IS the statement's own level: AnalyzeStatement refuses text whose
// first token is not a keyword, so a wholly parenthesised statement never
// reaches the walker at all.
func (a *analyzer) atStatementLevel() bool { return a.depth == 0 }

func (a *analyzer) inPredicateClause() bool {
	return a.cl == clWhere || a.cl == clOn || a.cl == clHaving
}

// readTableRef consumes `[schema.]table [AS alias | alias]` and records the
// access. CTE names are registered as aliases but never as tables: a CTE is a
// name for a subquery, and reporting it as a table would put a table that does
// not exist into the data footprint of a capability.
func (a *analyzer) readTableRef(i int) int {
	name, next := readQualifiedName(a.toks, i)
	if name == "" {
		return i + 1
	}
	access := tableClauses[a.cl]
	if a.deleteTarget && a.cl == clFrom {
		access = AccessWrite
		a.deleteTarget = false
	}
	alias, next := readAlias(a.toks, next)
	if alias == "" {
		alias = name
	}
	a.aliases[alias] = name
	if !a.ctes[name] {
		a.st.Tables = append(a.st.Tables, TableAccess{Table: name, Access: access})
	}
	a.expectTable = false
	return next
}

// recordPredicate captures the column on the left of an operator at index i.
// Everything is driven from the operator rather than from the column because
// the operator is the only sqlToken whose position is unambiguous -- scanning
// leftward from it finds the column through casts, functions and NOT without
// needing an expression grammar.
func (a *analyzer) recordPredicate(i int, op string) {
	operator, colEnd := normalizeOperator(a.toks, i, op)
	bound := a.operandBound(i+1) == OffsetParameter
	cl := ClauseWhere
	if a.cl == clOn {
		cl = ClauseJoin
	}
	for _, c := range columnsLeftOf(a.toks, colEnd) {
		a.st.Predicates = append(a.st.Predicates, Predicate{
			Table:    c.qualifier,
			Column:   c.name,
			Operator: operator,
			Clause:   cl,
			Bound:    bound,
		})
	}
}

// operandBound classifies the first sqlToken of an operand, looking through
// opening parens so `IN ($1, $2)` and `< ($1, $2)` read as bound.
func (a *analyzer) operandBound(i int) OffsetBound {
	for i < len(a.toks) && a.toks[i].kind == tokPunct && a.toks[i].val == "(" {
		i++
	}
	if i >= len(a.toks) {
		return OffsetExpression
	}
	switch a.toks[i].kind {
	case tokParam:
		return OffsetParameter
	case tokNumber:
		return OffsetLiteral
	}
	return OffsetExpression
}

// finish resolves aliases, attributes unqualified columns, decides keyset, and
// puts everything in a deterministic order so two runs over an unchanged
// repository produce byte-identical rows.
func (a *analyzer) finish() {
	a.st.ParamCount = len(a.params) + a.positional
	if a.st.OffsetBound == "" {
		a.st.OffsetBound = OffsetNone
	}
	a.st.Tables = dedupeTables(a.st.Tables)
	a.resolvePredicateTables()
	a.st.Predicates = dedupePredicates(a.st.Predicates)
	a.st.OrderBy = dedupeStrings(a.st.OrderBy)
	a.st.Keyset = a.detectKeyset()
}

// resolvePredicateTables maps each predicate's qualifier through the alias
// table. An unqualified column is attributed only when the statement touches
// exactly one table -- with two tables in play the column could belong to
// either, and guessing would aim an index advisory at the wrong table.
func (a *analyzer) resolvePredicateTables() {
	sole := ""
	if len(a.st.Tables) == 1 {
		sole = a.st.Tables[0].Table
	}
	for i := range a.st.Predicates {
		q := a.st.Predicates[i].Table
		if q == "" {
			a.st.Predicates[i].Table = sole
			continue
		}
		if t, ok := a.aliases[q]; ok {
			a.st.Predicates[i].Table = t
		}
	}
}

// detectKeyset reports cursor pagination, and requires everything that
// actually makes a cursor walk bounded:
//
//   - a LIMIT on this statement. The cursor bounds where the page STARTS, not
//     how big it is: `WHERE created_at < $1 ORDER BY created_at DESC` returns
//     every row before the cursor, which is the whole table on the first page.
//   - a caller-bound range predicate (the cursor value comes from the previous
//     page, so it is a parameter, not a constant),
//   - on the column the statement orders by FIRST. An index walk advances
//     along the leading sort column; a range predicate on any other ordered
//     column does not move the cursor.
//
// Requiring all three is the difference between recognising pagination and
// silencing the unbounded-read check. `WHERE created_at < $1 ORDER BY
// created_at DESC` is a LIMIT-less read of everything before the cursor;
// `WHERE created_at > $1 ORDER BY id LIMIT 100` is a time window sorted by
// something else; `WHERE depth < $1 ORDER BY tenant_id, depth LIMIT 100` is a
// recursion guard. All three used to read as keyset pagination and take their
// query out of the advisory entirely. Anything that does not meet the bar
// falls through to the advisory, which is suppressible per site when the
// caller really does apply the page size itself.
func (a *analyzer) detectKeyset() bool {
	if !a.st.HasLimit || !a.st.HasOrderBy || len(a.st.OrderBy) == 0 {
		return false
	}
	lead := a.st.OrderBy[0]
	for _, p := range a.st.Predicates {
		if p.Clause == ClauseWhere && p.Bound && rangeOps[p.Operator] && p.Column == lead {
			return true
		}
	}
	return false
}

// --- token-level helpers -------------------------------------------------

// qualifiedColumn is a column reference with the alias or table that
// qualified it, if any.
type qualifiedColumn struct {
	qualifier string
	name      string
}

// columnsLeftOf finds the column(s) an operator applies to, scanning left
// from index end (exclusive). The common case is a single `[alias.]column`.
// The other case is a row-value comparison -- `(created_at, id) < ($1, $2)` --
// which is the standard keyset spelling and yields one predicate per column;
// treating it as unreadable would report every correctly-paginated query as
// unbounded.
func columnsLeftOf(toks []sqlToken, end int) []qualifiedColumn {
	j := end - 1
	if j < 0 {
		return nil
	}
	if toks[j].kind == tokPunct && toks[j].val == ")" {
		return columnsInGroup(toks, j)
	}
	if toks[j].kind != tokIdent {
		return nil
	}
	col := qualifiedColumn{name: toks[j].val}
	if j >= 2 && toks[j-1].kind == tokPunct && toks[j-1].val == "." && toks[j-2].kind == tokIdent {
		col.qualifier = toks[j-2].val
	}
	return []qualifiedColumn{col}
}

// columnsInGroup walks back from the `)` at closeIdx to its matching `(` and
// returns the identifiers inside, ignoring the separators.
func columnsInGroup(toks []sqlToken, closeIdx int) []qualifiedColumn {
	open := matchingOpen(toks, closeIdx)
	if open < 0 {
		return nil
	}
	var out []qualifiedColumn
	for j := open + 1; j < closeIdx; j++ {
		if col, ok := groupColumnAt(toks, j, closeIdx); ok {
			out = append(out, col)
		}
	}
	return out
}

// groupColumnAt reads the column reference at j, if there is one. An
// identifier followed by `.` is a qualifier, not the column.
func groupColumnAt(toks []sqlToken, j, closeIdx int) (qualifiedColumn, bool) {
	if toks[j].kind != tokIdent {
		return qualifiedColumn{}, false
	}
	if j+1 < closeIdx && toks[j+1].kind == tokPunct && toks[j+1].val == "." {
		return qualifiedColumn{}, false
	}
	col := qualifiedColumn{name: toks[j].val}
	if j >= 2 && toks[j-1].kind == tokPunct && toks[j-1].val == "." {
		col.qualifier = toks[j-2].val
	}
	return col, true
}

// matchingOpen scans left from a closing paren to its partner, or -1 when the
// group is unbalanced.
func matchingOpen(toks []sqlToken, closeIdx int) int {
	depth := 0
	for j := closeIdx; j >= 0; j-- {
		if toks[j].kind != tokPunct {
			continue
		}
		switch toks[j].val {
		case ")":
			depth++
		case "(":
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

// normalizeOperator lowercases the operator and folds a preceding NOT into it,
// returning the index at which the left-hand operand ends.
func normalizeOperator(toks []sqlToken, i int, op string) (string, int) {
	if toks[i].kind == tokKeyword {
		if i > 0 && toks[i-1].kind == tokKeyword && toks[i-1].val == "NOT" {
			return "not " + lower(op), i - 1
		}
		return lower(op), i
	}
	return op, i
}

// readQualifiedName consumes `a.b.c` and returns the final segment -- the
// table name proper, with any schema or database qualifier dropped, because
// the schema parser records tables under their bare names too.
func readQualifiedName(toks []sqlToken, i int) (string, int) {
	if i >= len(toks) || toks[i].kind != tokIdent {
		return "", i
	}
	name := toks[i].val
	i++
	for i+1 < len(toks) && toks[i].kind == tokPunct && toks[i].val == "." && toks[i+1].kind == tokIdent {
		name = toks[i+1].val
		i += 2
	}
	return name, i
}

// readAlias consumes `AS alias` or a bare alias identifier. A keyword never
// counts as an alias, so `FROM users WHERE ...` does not name the table
// "WHERE".
func readAlias(toks []sqlToken, i int) (string, int) {
	if i >= len(toks) {
		return "", i
	}
	if toks[i].kind == tokKeyword && toks[i].val == "AS" && i+1 < len(toks) && toks[i+1].kind == tokIdent {
		return toks[i+1].val, i + 2
	}
	if toks[i].kind == tokIdent {
		return toks[i].val, i + 1
	}
	return "", i
}

// collectCTEs returns the names bound by a leading WITH clause. They are
// gathered up front because the clause walker's state at the closing paren of
// a CTE body is whatever the body left behind, which is not a reliable place
// to recognise the next `name AS (`.
func collectCTEs(toks []sqlToken) map[string]bool {
	out := map[string]bool{}
	if len(toks) == 0 || toks[0].kind != tokKeyword || toks[0].val != "WITH" {
		return out
	}
	for i := 1; i+2 < len(toks); {
		if toks[i].kind == tokKeyword && toks[i].val == "RECURSIVE" {
			i++
			continue
		}
		name, bodyStart, ok := cteBinding(toks, i)
		if !ok {
			return out
		}
		out[name] = true
		next := skipParenGroup(toks, bodyStart)
		if next >= len(toks) || toks[next].kind != tokPunct || toks[next].val != "," {
			return out
		}
		i = next + 1
	}
	return out
}

// cteBinding reads one `name [(col, ...)] AS (` binding at i, returning the
// name and the index of its body's opening paren.
//
// The optional column list is the part that is easy to miss --
// `WITH RECURSIVE chain(id, depth) AS (...)` -- and missing it makes the CTE
// look like a table, which then reports forever as a table whose DDL atlas
// could not find.
func cteBinding(toks []sqlToken, i int) (name string, bodyStart int, ok bool) {
	if toks[i].kind != tokIdent {
		return "", 0, false
	}
	at := i + 1
	if at < len(toks) && toks[at].kind == tokPunct && toks[at].val == "(" {
		at = skipParenGroup(toks, at)
	}
	if at >= len(toks) || toks[at].kind != tokKeyword || toks[at].val != "AS" {
		return "", 0, false
	}
	return toks[i].val, at + 1, true
}

// skipParenGroup returns the index just past the parenthesised group starting
// at i, or len(toks) when it is unbalanced.
func skipParenGroup(toks []sqlToken, i int) int {
	depth := 0
	for ; i < len(toks); i++ {
		if toks[i].kind != tokPunct {
			continue
		}
		switch toks[i].val {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(toks)
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// --- deterministic dedupe ------------------------------------------------

func dedupeTables(in []TableAccess) []TableAccess {
	seen := map[TableAccess]bool{}
	out := make([]TableAccess, 0, len(in))
	for _, t := range in {
		if t.Table == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Table != out[j].Table {
			return out[i].Table < out[j].Table
		}
		return out[i].Access < out[j].Access
	})
	return out
}

func dedupePredicates(in []Predicate) []Predicate {
	type key struct {
		t, c, o string
		cl      PredicateClause
	}
	seen := map[key]int{}
	out := make([]Predicate, 0, len(in))
	for _, p := range in {
		if p.Column == "" {
			continue
		}
		k := key{p.Table, p.Column, p.Operator, p.Clause}
		if idx, ok := seen[k]; ok {
			// Keep the bound variant: a column compared once to a constant
			// and once to a parameter is a caller-supplied filter, and that
			// is the fact the pagination checks need.
			if p.Bound {
				out[idx].Bound = true
			}
			continue
		}
		seen[k] = len(out)
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Clause != out[j].Clause {
			return out[i].Clause < out[j].Clause
		}
		if out[i].Table != out[j].Table {
			return out[i].Table < out[j].Table
		}
		if out[i].Column != out[j].Column {
			return out[i].Column < out[j].Column
		}
		return out[i].Operator < out[j].Operator
	})
	return out
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
