package sqlops

import "github.com/sosalejandro/atlas/packages/shared"

// OperationSource says where Atlas found a query.
type OperationSource string

// The closed set of operation sources.
const (
	// SourceGo is a query passed to a database/sql method from Go source.
	SourceGo OperationSource = "go"
	// SourceSQLFile is a named query in a .sql file sqlc generates from.
	SourceSQLFile OperationSource = "sql"
)

// RowScan is what the call site does with the rows. It is the difference
// between a LIMIT-less SELECT that loads a table into memory and one that
// reads a single row by primary key, which look identical in the SQL.
type RowScan string

// The closed set of row-scan shapes.
const (
	ScanSlice   RowScan = "slice"
	ScanSingle  RowScan = "single"
	ScanExec    RowScan = "exec"
	ScanUnknown RowScan = "unknown"
)

// Interpolation names the mechanism by which non-literal text reached the
// query, empty when the query text is entirely static.
type Interpolation string

// The closed set of interpolation mechanisms.
const (
	InterpolationNone    Interpolation = ""
	InterpolationConcat  Interpolation = "concat"
	InterpolationSprintf Interpolation = "sprintf"
)

// Unresolved reasons. These are user-visible strings: they appear in the
// `unresolved` list of `atlas sql` output, and a reader must be able to tell
// from the reason alone whether the query is worth making visible to Atlas.
const (
	ReasonConcat = "query text is concatenated with an expression atlas cannot evaluate"
	// ReasonSprintf covers fmt.Sprintf and friends. The format string is
	// usually visible, so the fragment is recorded even though the whole is
	// not.
	ReasonSprintf = "query text is assembled with a formatting call"
	// ReasonDynamic is the query builder, the cross-function assembly, the
	// value read from config. Atlas cannot resolve it and says so.
	ReasonDynamic = "query text is an expression atlas cannot statically resolve"
	// ReasonReassigned is a local whose value is written more than once; the
	// last writer is not knowable without flow analysis.
	ReasonReassigned = "query variable is assigned more than once; atlas cannot resolve which value reaches the call"
	// ReasonNotAStatement means the text resolved but the lexer found no SQL
	// verb in it. Recording it as an operation with an unknown shape is
	// honest; classifying it as a SELECT would not be.
	ReasonNotAStatement = "resolved text contains no recognisable SQL statement"
)

// Operation is one SQL operation Atlas found, resolved or not.
//
// The unresolved case carries as much as could be seen -- the fragment, the
// symbol, the position, the reason -- because the alternative, dropping it,
// is what turns a partial analysis into a false clean bill of health.
type Operation struct {
	Source     OperationSource     `json:"source"`
	Name       string              `json:"name,omitempty"`
	Position   shared.FilePosition `json:"position"`
	SymbolName string              `json:"symbol,omitempty"`

	Resolved         bool   `json:"resolved"`
	UnresolvedReason string `json:"unresolved_reason,omitempty"`

	// SQL is the resolved query text, or the fragment of it Atlas could see
	// when unresolved.
	SQL       string    `json:"sql,omitempty"`
	Statement Statement `json:"statement"`
	RowScan   RowScan   `json:"row_scan"`

	// Interpolated reports that query text was built rather than written.
	Interpolated  bool          `json:"interpolated"`
	Interpolation Interpolation `json:"interpolation,omitempty"`
	// CallerData reports that the interpolated value traces to a parameter of
	// the enclosing function. This is the line between "a value was formatted
	// into SQL" (common, often a table name from a constant) and "caller data
	// reaches SQL text" (the thing worth waking someone for), and it is what
	// the injection advisory's confidence is graded on.
	CallerData bool `json:"caller_data"`
	// Interpolated fragments, for the advisory message.
	InterpolatedExpr string `json:"interpolated_expr,omitempty"`

	// Suppressions are the advisory codes an `atlas:sql-ignore` directive at
	// the call site turns off.
	Suppressions []string `json:"suppressions,omitempty"`
}

// Suppressed reports whether an advisory code is silenced for this operation.
func (o Operation) Suppressed(code string) bool {
	for _, s := range o.Suppressions {
		if s == code || s == "all" {
			return true
		}
	}
	return false
}

// Ref is the stable identity of an operation: source, position and name. It is
// the fingerprint the store keys on, so re-scanning an unchanged repository
// rewrites the same rows instead of accumulating duplicates.
func (o Operation) Ref() string {
	name := o.Name
	if name == "" {
		name = o.SymbolName
	}
	return string(o.Source) + ":" + o.Position.Path + ":" + itoa(o.Position.Line) + ":" + name
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
