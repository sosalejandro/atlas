package sqlops

import (
	"fmt"
	"sort"

	"github.com/sosalejandro/atlas/packages/shared"
)

// Advisory codes. Each is stable, greppable, and suppressible by name -- both
// from a source directive (`// atlas:sql-ignore <code>`) and from the command
// line.
const (
	CodeUnboundedList      = "sql.unbounded-list"
	CodeUnstablePagination = "sql.unstable-pagination"
	CodeOffsetDepth        = "sql.offset-depth"
	CodePossibleInjection  = "sql.possible-injection"
	CodeMissingIndex       = "sql.missing-index"
	CodeSelectStar         = "sql.select-star"
	CodeOrphanTable        = "sql.orphan-table"
	CodeUnusedIndex        = "sql.unused-index"
)

// Confidence is how sure Atlas is. It is part of every advisory because these
// checks reason over a partial, static view: a reader who cannot tell a
// deterministic fact from a heuristic has no way to triage the list, and will
// eventually stop reading it.
type Confidence string

// The closed set of confidence levels.
const (
	// ConfidenceHigh: the finding follows from the SQL text or from a
	// syntactic fact at the call site. If the query runs, the finding holds.
	ConfidenceHigh Confidence = "high"
	// ConfidenceMedium: the finding depends on something Atlas inferred --
	// the row shape, the completeness of the schema scan.
	ConfidenceMedium Confidence = "medium"
	// ConfidenceLow: the finding depends on context Atlas cannot see, such as
	// whether a payload crosses a service boundary.
	ConfidenceLow Confidence = "low"
)

// Advisory is one finding, anchored at a file:line.
type Advisory struct {
	Code       string              `json:"code"`
	Confidence Confidence          `json:"confidence"`
	Position   shared.FilePosition `json:"position"`
	Symbol     string              `json:"symbol,omitempty"`
	Query      string              `json:"query,omitempty"`
	Message    string              `json:"message"`
	// Remedy is the concrete next action. An advisory that names a problem
	// and not its fix gets triaged as noise.
	Remedy string `json:"remedy,omitempty"`
}

// SkippedCheck records a check that did not run, and why.
//
// This type is the honesty requirement in structural form. A missing-index
// check over a schema Atlas never read has no verdict -- not "pass". Recording
// the skip lets the command tell the reader which questions were actually
// answered.
type SkippedCheck struct {
	Code string `json:"code"`
	// Scope names what was skipped: "table:events", or empty for a check
	// that did not run at all.
	Scope  string `json:"scope,omitempty"`
	Reason string `json:"reason"`
}

// AdviseOptions tunes advisory generation.
type AdviseOptions struct {
	// Suppress silences these codes everywhere, on top of the per-site
	// `atlas:sql-ignore` directives.
	Suppress []string
}

// AdviseResult is the advisory pass output.
type AdviseResult struct {
	Advisories []Advisory     `json:"advisories"`
	Skipped    []SkippedCheck `json:"skipped_checks,omitempty"`
}

// Advise runs every check over the operation inventory and the schema.
func Advise(ops []Operation, schema Schema, opts AdviseOptions) AdviseResult {
	a := &adviser{schema: schema, silenced: setOf(opts.Suppress)}
	for _, op := range ops {
		a.perOperation(op)
	}
	a.schemaWide(ops)
	sortAdvisories(a.res.Advisories)
	sort.Slice(a.res.Skipped, func(i, j int) bool {
		if a.res.Skipped[i].Code != a.res.Skipped[j].Code {
			return a.res.Skipped[i].Code < a.res.Skipped[j].Code
		}
		return a.res.Skipped[i].Scope < a.res.Skipped[j].Scope
	})
	return a.res
}

type adviser struct {
	schema   Schema
	silenced map[string]bool
	res      AdviseResult
	// skippedScope dedupes "did not check table X" across the many operations
	// that touch X.
	skippedScope map[string]bool
}

func (a *adviser) perOperation(op Operation) {
	a.injection(op)
	if !op.Resolved {
		// Every other check reads the statement shape, and an unresolved
		// operation has none. It is reported in the inventory as unanalysed;
		// inventing a verdict for it is the failure mode this whole feature
		// is built against.
		return
	}
	a.unbounded(op)
	a.pagination(op)
	a.selectStar(op)
	a.missingIndex(op)
}

// --- per-operation checks ------------------------------------------------

// unbounded flags a SELECT with no upper bound on its result set whose rows
// are collected. Keyset walks are excluded: their bound is the cursor, and the
// page size lives in the caller.
func (a *adviser) unbounded(op Operation) {
	st := op.Statement
	if st.Kind != KindSelect || st.HasLimit || st.Keyset {
		return
	}
	conf := ConfidenceHigh
	switch op.RowScan {
	case ScanSingle, ScanExec:
		return
	case ScanUnknown:
		// The call site returns rows but Atlas saw no slice being built, so
		// the result may well be consumed one row at a time.
		conf = ConfidenceMedium
	}
	a.add(op, Advisory{
		Code:       CodeUnboundedList,
		Confidence: conf,
		Message: fmt.Sprintf("SELECT over %s has no LIMIT and no keyset predicate; the result set grows with the table",
			tableList(st)),
		Remedy: "add a LIMIT, or paginate by keyset (WHERE ordered_column < $cursor ORDER BY ordered_column)",
	})
}

// pagination covers the two OFFSET failure modes: ordering that is not
// specified, and depth that is caller-controlled.
func (a *adviser) pagination(op Operation) {
	st := op.Statement
	if st.HasLimit && st.HasOffset && !st.HasOrderBy {
		a.add(op, Advisory{
			Code:       CodeUnstablePagination,
			Confidence: ConfidenceHigh,
			Message:    "LIMIT/OFFSET with no ORDER BY: row order is unspecified, so rows can repeat or vanish between pages",
			Remedy:     "add an ORDER BY on a unique or tie-broken column set",
		})
	}
	if st.HasOffset && st.OffsetBound == OffsetParameter {
		a.add(op, Advisory{
			Code:       CodeOffsetDepth,
			Confidence: ConfidenceHigh,
			Message:    "OFFSET takes a caller-supplied value; cost grows linearly with the offset, so deep pages get slower without any code change",
			Remedy:     "switch to keyset pagination, or cap the reachable offset",
		})
	}
}

func (a *adviser) selectStar(op Operation) {
	if !op.Statement.SelectStar {
		return
	}
	a.add(op, Advisory{
		Code: CodeSelectStar,
		// Low: whether the extra columns reach a payload is a fact about the
		// caller, which this pass cannot see.
		Confidence: ConfidenceLow,
		Message:    "SELECT * couples the result shape to the table; adding a column changes what this query returns",
		Remedy:     "project the columns the caller needs",
	})
}

// injection is deliberately the narrowest check here. It fires only when a
// value that traces to a parameter of the enclosing function is concatenated
// or formatted into query text.
//
// Everything else stays quiet -- the placeholder list built with
// strings.Repeat, the table name pulled from a package constant, the fragment
// assembled from a switch. Those show up in the unresolved inventory with
// their reason, which is where a reader can weigh them. A false positive here
// costs the credibility of every other advisory in the report, so the bar is
// set where a finding is nearly always worth acting on.
func (a *adviser) injection(op Operation) {
	if !op.Interpolated || !op.CallerData {
		return
	}
	detail := ""
	if op.InterpolatedExpr != "" {
		detail = fmt.Sprintf(" (%s)", op.InterpolatedExpr)
	}
	a.add(op, Advisory{
		Code:       CodePossibleInjection,
		Confidence: ConfidenceHigh,
		Message: fmt.Sprintf("query text is built by %s from a value the caller supplies%s",
			interpolationName(op.Interpolation), detail),
		Remedy: "bind the value as a parameter; if it must be an identifier, validate it against a fixed allowlist",
	})
}

// missingIndex reports a filtered table no index can serve from its leading
// column. It runs per (operation, table) and never guesses about a table whose
// DDL was not read.
func (a *adviser) missingIndex(op Operation) {
	for _, ta := range op.Statement.Tables {
		cols := filteredColumns(op.Statement, ta.Table)
		if len(cols) == 0 {
			continue // no filter to index.
		}
		if !a.indexCheckAvailable(ta.Table) {
			continue
		}
		if _, ok := a.schema.LeadingIndexFor(ta.Table, cols); ok {
			continue
		}
		a.add(op, Advisory{
			Code: CodeMissingIndex,
			// Medium: the schema scan may not have read every migration
			// directory, and a database can also have indexes created
			// outside version control.
			Confidence: ConfidenceMedium,
			Message: fmt.Sprintf("filters %s on %s, and no index on that table leads with any of those columns",
				ta.Table, joinSorted(cols)),
			Remedy: fmt.Sprintf("add an index on %s leading with the most selective filtered column", ta.Table),
		})
	}
}

// indexCheckAvailable reports whether the index check can run for a table,
// recording a skip when it cannot.
func (a *adviser) indexCheckAvailable(table string) bool {
	if a.schema.Empty() {
		a.skipOnce(SkippedCheck{
			Code:   CodeMissingIndex,
			Reason: "no DDL was read; point --schema-dir at the migrations to enable the index checks",
		})
		return false
	}
	if !a.schema.Known(table) {
		a.skipOnce(SkippedCheck{
			Code:   CodeMissingIndex,
			Scope:  "table:" + table,
			Reason: "no CREATE TABLE for this table was found in the schema files that were read",
		})
		return false
	}
	return true
}

// --- schema-wide checks --------------------------------------------------

// schemaWide runs the checks that compare the whole inventory against the
// whole schema. Both of them assert a negative -- "nothing queries this
// table", "nothing uses this index" -- and a negative asserted over a partial
// inventory is simply false. So they run only when every operation resolved.
func (a *adviser) schemaWide(ops []Operation) {
	if a.schema.Empty() {
		a.skipOnce(SkippedCheck{Code: CodeOrphanTable, Reason: "no DDL was read"})
		a.skipOnce(SkippedCheck{Code: CodeUnusedIndex, Reason: "no DDL was read"})
		return
	}
	if n := countUnresolved(ops); n > 0 {
		reason := fmt.Sprintf("%d of %d operations could not be resolved; a table or index cannot be called unused on a partial inventory", n, len(ops))
		a.skipOnce(SkippedCheck{Code: CodeOrphanTable, Reason: reason})
		a.skipOnce(SkippedCheck{Code: CodeUnusedIndex, Reason: reason})
		return
	}
	touched, filtered := usageSets(ops)
	a.orphanTables(touched)
	a.unusedIndexes(filtered)
}

func (a *adviser) orphanTables(touched map[string]bool) {
	for _, tbl := range a.schema.Tables {
		if touched[tbl.Name] {
			continue
		}
		a.addSchema(Advisory{
			Code: CodeOrphanTable,
			// Medium: a query in another language, or in a directory outside
			// the scanned root, would also touch it.
			Confidence: ConfidenceMedium,
			Position:   tbl.Position,
			Message:    fmt.Sprintf("no query in the scanned sources reads or writes %s", tbl.Name),
			Remedy:     "confirm the table is still needed, or widen the scan if it is used from code atlas did not read",
		})
	}
}

// unusedIndexes reports non-unique CREATE INDEX declarations whose leading
// column no query filters or orders on. Unique indexes and primary keys are
// never reported: they enforce a constraint, and their read cost is beside
// the point.
func (a *adviser) unusedIndexes(filtered map[string]map[string]bool) {
	for _, ix := range a.schema.Indexes {
		if ix.Origin != OriginCreateIndex || ix.Unique || len(ix.Columns) == 0 {
			continue
		}
		if filtered[ix.Table][ix.Columns[0]] {
			continue
		}
		a.addSchema(Advisory{
			Code: CodeUnusedIndex,
			// Low: an index also serves constraint checks, foreign-key
			// enforcement and queries issued by tooling rather than code.
			Confidence: ConfidenceLow,
			Position:   ix.Position,
			Message: fmt.Sprintf("no query filters or orders on %s.%s, the leading column of index %s; the index costs every write",
				ix.Table, ix.Columns[0], ix.Name),
			Remedy: "confirm the index is earning its write cost before dropping it",
		})
	}
}

// --- plumbing ------------------------------------------------------------

func (a *adviser) add(op Operation, adv Advisory) {
	if a.silenced[adv.Code] || op.Suppressed(adv.Code) {
		return
	}
	adv.Position = op.Position
	adv.Symbol = op.SymbolName
	adv.Query = op.Name
	a.res.Advisories = append(a.res.Advisories, adv)
}

func (a *adviser) addSchema(adv Advisory) {
	if a.silenced[adv.Code] {
		return
	}
	a.res.Advisories = append(a.res.Advisories, adv)
}

// skipOnce records a skipped check, deduped by (code, scope): a hundred
// queries against one unscanned table are one unanswered question, not a
// hundred.
func (a *adviser) skipOnce(s SkippedCheck) {
	if a.silenced[s.Code] {
		return
	}
	if a.skippedScope == nil {
		a.skippedScope = map[string]bool{}
	}
	key := s.Code + "|" + s.Scope
	if a.skippedScope[key] {
		return
	}
	a.skippedScope[key] = true
	a.res.Skipped = append(a.res.Skipped, s)
}

// filteredColumns returns the WHERE-clause columns of one table that an index
// could serve. Join predicates are excluded: the planner chooses the join
// order, so a missing index on one side is not the same claim.
func filteredColumns(st Statement, table string) map[string]bool {
	out := map[string]bool{}
	for _, p := range st.Predicates {
		if p.Table != table || p.Clause != ClauseWhere {
			continue
		}
		if p.Operator == "like" || p.Operator == "not like" || p.Operator == "is" {
			// A leading-wildcard LIKE cannot use an index and IS NULL rarely
			// justifies one; neither is evidence that an index is missing.
			continue
		}
		out[p.Column] = true
	}
	return out
}

// usageSets returns the tables any operation touches, and per table the
// columns any operation filters or orders on.
func usageSets(ops []Operation) (map[string]bool, map[string]map[string]bool) {
	touched := map[string]bool{}
	filtered := map[string]map[string]bool{}
	for _, op := range ops {
		for _, ta := range op.Statement.Tables {
			touched[ta.Table] = true
			if filtered[ta.Table] == nil {
				filtered[ta.Table] = map[string]bool{}
			}
		}
		for _, p := range op.Statement.Predicates {
			if p.Table == "" {
				continue
			}
			if filtered[p.Table] == nil {
				filtered[p.Table] = map[string]bool{}
			}
			filtered[p.Table][p.Column] = true
		}
		markOrderedColumns(op, filtered)
	}
	return touched, filtered
}

// markOrderedColumns credits ORDER BY columns to the single table a statement
// reads. With more than one table in play the column's owner is ambiguous, and
// crediting the wrong table would hide a genuinely unused index.
func markOrderedColumns(op Operation, filtered map[string]map[string]bool) {
	if len(op.Statement.Tables) != 1 {
		return
	}
	t := op.Statement.Tables[0].Table
	if filtered[t] == nil {
		filtered[t] = map[string]bool{}
	}
	for _, c := range op.Statement.OrderBy {
		filtered[t][c] = true
	}
}

func countUnresolved(ops []Operation) int {
	n := 0
	for _, op := range ops {
		if !op.Resolved {
			n++
		}
	}
	return n
}

func tableList(st Statement) string {
	names := make([]string, 0, len(st.Tables))
	for _, t := range st.Tables {
		names = append(names, t.Table)
	}
	if len(names) == 0 {
		return "an unnamed source"
	}
	return joinStrings(names)
}

func joinSorted(set map[string]bool) string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return joinStrings(out)
}

func joinStrings(in []string) string {
	switch len(in) {
	case 0:
		return ""
	case 1:
		return in[0]
	}
	s := in[0]
	for _, v := range in[1:] {
		s += ", " + v
	}
	return s
}

func interpolationName(i Interpolation) string {
	if i == InterpolationSprintf {
		return "string formatting"
	}
	return "string concatenation"
}

func sortAdvisories(in []Advisory) {
	sort.SliceStable(in, func(i, j int) bool {
		if in[i].Position.Path != in[j].Position.Path {
			return in[i].Position.Path < in[j].Position.Path
		}
		if in[i].Position.Line != in[j].Position.Line {
			return in[i].Position.Line < in[j].Position.Line
		}
		return in[i].Code < in[j].Code
	})
}
