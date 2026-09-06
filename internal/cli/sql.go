package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/sqlops"
	"github.com/sosalejandro/atlas/packages/store"
)

// newSQLCmd builds the `atlas sql` verb group: the data access layer as
// something you can query.
//
// The split is deliberate. `scan` reads the working tree and writes the
// inventory; `list` and `advise` read the inventory back out of the store.
// Advisories are therefore computed from persisted rows, which means CI and a
// developer's terminal are looking at the same evidence, and a stale answer
// is visibly stale (`atlas sql list` shows the file:line it came from) rather
// than quietly recomputed from a tree that has since moved on.
func newSQLCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sql",
		Short: "Inspect the SQL your code issues: shape, parameters, pagination, indexes",
		Long: `sql extracts the SQL statements your code issues and records what they do:
the statement kind, the tables read and written, the columns filtered on,
the parameter count, and whether the read is bounded.

It then turns that shape into advisories -- unbounded reads, unstable and
deep pagination, caller data reaching query text, filters no index can serve.

Atlas reads SQL only where it is statically visible: string literals passed
to database/sql methods, constants holding query text, and the .sql files
sqlc generates from. A query assembled by a builder or across functions is
recorded as UNRESOLVED with the reason, never dropped, and every command
reports the resolved fraction so you can weigh the advisories against how
much of the data layer they were computed over.`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(newSQLScanCmd(), newSQLListCmd(), newSQLAdviseCmd())
	return cmd
}

// --- scan ----------------------------------------------------------------

func newSQLScanCmd() *cobra.Command {
	var (
		schemaDirs   []string
		queryDirs    []string
		includeTests bool
	)
	cmd := &cobra.Command{
		Use:   "scan [path]",
		Short: "Extract SQL operations and schema into the Atlas store",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := loaded.repoRoot
			if len(args) == 1 {
				root = args[0]
			}
			return runSQLScan(cmd, sqlops.Options{
				Root:       root,
				SchemaDirs: schemaDirs,
				QueryDirs:  queryDirs,
				Extract:    sqlops.ExtractOptions{IncludeTests: includeTests},
			})
		},
	}
	cmd.Flags().StringSliceVar(&schemaDirs, "schema-dir", nil,
		"directory holding DDL (repeatable); default: the sqlc schema path plus conventional migration directories")
	cmd.Flags().StringSliceVar(&queryDirs, "query-dir", nil,
		"directory holding sqlc .sql query files (repeatable); default: the sqlc queries path")
	cmd.Flags().BoolVar(&includeTests, "include-tests", false,
		"index queries in _test.go files too")
	return cmd
}

// sqlScanResult is the --json payload for `atlas sql scan`.
type sqlScanResult struct {
	Root             string   `json:"root"`
	SchemaDirs       []string `json:"schema_dirs,omitempty"`
	QueryDirs        []string `json:"query_dirs,omitempty"`
	Operations       int      `json:"operations"`
	Resolved         int      `json:"resolved"`
	Unresolved       int      `json:"unresolved"`
	ResolvedFraction float64  `json:"resolved_fraction"`
	Tables           int      `json:"tables"`
	Indexes          int      `json:"indexes"`
	Warnings         []string `json:"warnings,omitempty"`
}

func runSQLScan(cmd *cobra.Command, opts sqlops.Options) error {
	ctx := sqlCmdContext(cmd)
	// Advisories are computed on read, from the stored rows.
	opts.SkipAdvisories = true

	rep, err := sqlops.Analyze(opts)
	if err != nil {
		return fmt.Errorf("sql scan: %w", err)
	}

	s, err := openStoreForRead(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	if err := s.SQLOps().Replace(ctx, toStoreRecords(rep.Operations)); err != nil {
		return fmt.Errorf("sql scan: persist operations: %w", err)
	}
	tables, indexes := toStoreSchema(rep.Schema)
	if err := s.SQLOps().ReplaceSchema(ctx, tables, indexes); err != nil {
		return fmt.Errorf("sql scan: persist schema: %w", err)
	}

	res := sqlScanResult{
		Root:             opts.Root,
		SchemaDirs:       rep.SchemaDirs,
		QueryDirs:        rep.QueryDirs,
		Operations:       len(rep.Operations),
		Resolved:         rep.Resolved,
		Unresolved:       rep.Unresolved,
		ResolvedFraction: rep.ResolvedFraction(),
		Tables:           len(tables),
		Indexes:          len(indexes),
		Warnings:         rep.Warnings,
	}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "sql.scan",
			map[string]any{"root": opts.Root}, res, rep.Warnings)
	}
	renderSQLScan(cmd.OutOrStdout(), res, rep)
	return nil
}

func renderSQLScan(w io.Writer, res sqlScanResult, rep sqlops.Report) {
	fmt.Fprintf(w, "\n  indexed %d operations from %s\n", res.Operations, res.Root)
	fmt.Fprintf(w, "  %s\n", coverageLine(res.Resolved, res.Unresolved))
	fmt.Fprintf(w, "  schema: %d tables, %d indexes", res.Tables, res.Indexes)
	if len(res.SchemaDirs) == 0 {
		fmt.Fprint(w, "  (no DDL found -- pass --schema-dir to enable the index checks)")
	} else {
		fmt.Fprintf(w, " from %s", strings.Join(res.SchemaDirs, ", "))
	}
	fmt.Fprintln(w)

	if rep.Unresolved > 0 {
		fmt.Fprintln(w, "\n  Unresolved (recorded, not analysed):")
		for _, reason := range unresolvedReasonCounts(rep.Operations) {
			fmt.Fprintf(w, "    %3d  %s\n", reason.count, reason.reason)
		}
	}
	for _, warn := range rep.Warnings {
		fmt.Fprintf(w, "  warn: %s\n", warn)
	}
	fmt.Fprintf(w, "\n  next: atlas sql advise\n\n")
}

// --- list ----------------------------------------------------------------

func newSQLListCmd() *cobra.Command {
	var (
		onlyUnresolved bool
		table          string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the SQL operations recorded by the last scan",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSQLList(cmd, onlyUnresolved, table)
		},
	}
	cmd.Flags().BoolVar(&onlyUnresolved, "unresolved", false,
		"list only the operations atlas could not statically resolve")
	cmd.Flags().StringVar(&table, "table", "",
		"list only operations touching this table")
	return cmd
}

// sqlListResult is the --json payload for `atlas sql list`.
type sqlListResult struct {
	Operations       []store.SQLOperationRecord `json:"operations"`
	Resolved         int                        `json:"resolved"`
	Unresolved       int                        `json:"unresolved"`
	ResolvedFraction float64                    `json:"resolved_fraction"`
}

func runSQLList(cmd *cobra.Command, onlyUnresolved bool, table string) error {
	ctx := sqlCmdContext(cmd)
	s, err := openStoreForRead(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	all, err := s.SQLOps().List(ctx)
	if err != nil {
		return fmt.Errorf("sql list: %w", err)
	}
	resolved, unresolved := countResolution(all)
	rows := filterOperations(all, onlyUnresolved, table)

	res := sqlListResult{
		Operations:       rows,
		Resolved:         resolved,
		Unresolved:       unresolved,
		ResolvedFraction: fraction(resolved, unresolved),
	}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "sql.list",
			map[string]any{"unresolved": onlyUnresolved, "table": table}, res, nil)
	}
	renderSQLList(cmd.OutOrStdout(), res, len(all))
	return nil
}

func renderSQLList(w io.Writer, res sqlListResult, total int) {
	fmt.Fprintln(w)
	if total == 0 {
		fmt.Fprintln(w, "  no SQL operations recorded -- run `atlas sql scan` first")
		fmt.Fprintln(w)
		return
	}
	fmt.Fprintf(w, "  %s\n\n", coverageLine(res.Resolved, res.Unresolved))
	for _, op := range res.Operations {
		fmt.Fprintf(w, "  %s:%d  %s\n", op.FilePath, op.Line, describeOperation(op))
		if op.SymbolName != "" {
			fmt.Fprintf(w, "    in %s\n", op.SymbolName)
		}
		if !op.Resolved {
			fmt.Fprintf(w, "    UNRESOLVED: %s\n", op.UnresolvedReason)
			continue
		}
		if line := predicateLine(op); line != "" {
			fmt.Fprintf(w, "    %s\n", line)
		}
	}
	fmt.Fprintln(w)
}

// describeOperation renders the one-line summary of a stored operation.
func describeOperation(op store.SQLOperationRecord) string {
	var b strings.Builder
	b.WriteString(strings.ToUpper(op.Kind))
	if names := tableNames(op); names != "" {
		b.WriteString(" " + names)
	}
	if op.Name != "" {
		b.WriteString(" [" + op.Name + "]")
	}
	b.WriteString(fmt.Sprintf("  params=%d", op.ParamCount))
	for _, flag := range []struct {
		on   bool
		text string
	}{
		{op.HasLimit, "limit"}, {op.HasOffset, "offset"},
		{op.HasOrderBy, "order-by"}, {op.Keyset, "keyset"},
		{op.SelectStar, "select-*"},
	} {
		if flag.on {
			b.WriteString(" " + flag.text)
		}
	}
	return b.String()
}

func tableNames(op store.SQLOperationRecord) string {
	parts := make([]string, 0, len(op.Tables))
	for _, t := range op.Tables {
		parts = append(parts, t.Access[:1]+":"+t.Table)
	}
	return strings.Join(parts, " ")
}

func predicateLine(op store.SQLOperationRecord) string {
	if len(op.Predicates) == 0 {
		return ""
	}
	parts := make([]string, 0, len(op.Predicates))
	for _, p := range op.Predicates {
		col := p.Column
		if p.Table != "" {
			col = p.Table + "." + p.Column
		}
		parts = append(parts, fmt.Sprintf("%s %s", col, p.Operator))
	}
	return "filters: " + strings.Join(parts, ", ")
}

// --- advise --------------------------------------------------------------

func newSQLAdviseCmd() *cobra.Command {
	var (
		suppress      []string
		minConfidence string
	)
	cmd := &cobra.Command{
		Use:   "advise",
		Short: "Report advisories over the recorded SQL operations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSQLAdvise(cmd, suppress, minConfidence)
		},
	}
	cmd.Flags().StringSliceVar(&suppress, "suppress", nil,
		"advisory codes to silence globally (repeatable); per-site suppression uses an `// atlas:sql-ignore <code>` comment")
	cmd.Flags().StringVar(&minConfidence, "min-confidence", "low",
		"drop advisories below this confidence (low|medium|high)")
	return cmd
}

// sqlAdviseResult is the --json payload for `atlas sql advise`.
//
// ResolvedFraction and SkippedChecks are part of the contract, not decoration:
// a consumer that gates a build on this output needs to know how much of the
// data layer produced it and which checks abstained.
type sqlAdviseResult struct {
	Advisories       []sqlops.Advisory     `json:"advisories"`
	Skipped          []sqlops.SkippedCheck `json:"skipped_checks,omitempty"`
	Operations       int                   `json:"operations"`
	Resolved         int                   `json:"resolved"`
	Unresolved       int                   `json:"unresolved"`
	ResolvedFraction float64               `json:"resolved_fraction"`
	MinConfidence    string                `json:"min_confidence"`
}

func runSQLAdvise(cmd *cobra.Command, suppress []string, minConfidence string) error {
	ctx := sqlCmdContext(cmd)
	rank, err := confidenceRank(minConfidence)
	if err != nil {
		return err
	}

	s, err := openStoreForRead(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	rows, err := s.SQLOps().List(ctx)
	if err != nil {
		return fmt.Errorf("sql advise: %w", err)
	}
	schema, err := loadStoredSchema(ctx, s)
	if err != nil {
		return err
	}

	out := sqlops.Advise(toSQLOpsOperations(rows), schema, sqlops.AdviseOptions{Suppress: suppress})
	resolved, unresolved := countResolution(rows)
	res := sqlAdviseResult{
		Advisories:       filterByConfidence(out.Advisories, rank),
		Skipped:          out.Skipped,
		Operations:       len(rows),
		Resolved:         resolved,
		Unresolved:       unresolved,
		ResolvedFraction: fraction(resolved, unresolved),
		MinConfidence:    minConfidence,
	}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "sql.advise",
			map[string]any{"suppress": suppress, "min_confidence": minConfidence}, res, nil)
	}
	renderSQLAdvise(cmd.OutOrStdout(), res)
	return nil
}

func renderSQLAdvise(w io.Writer, res sqlAdviseResult) {
	fmt.Fprintln(w)
	if res.Operations == 0 {
		fmt.Fprintln(w, "  no SQL operations recorded -- run `atlas sql scan` first")
		fmt.Fprintln(w)
		return
	}
	fmt.Fprintf(w, "  %s\n\n", coverageLine(res.Resolved, res.Unresolved))

	if len(res.Advisories) == 0 {
		fmt.Fprintln(w, "  no advisories at or above the requested confidence")
	}
	for _, a := range res.Advisories {
		fmt.Fprintf(w, "  %s  %s:%d  [%s]\n", a.Code, a.Position.Path, a.Position.Line, a.Confidence)
		if a.Symbol != "" {
			fmt.Fprintf(w, "    in %s\n", a.Symbol)
		}
		fmt.Fprintf(w, "    %s\n", a.Message)
		if a.Remedy != "" {
			fmt.Fprintf(w, "    fix: %s\n", a.Remedy)
		}
	}
	if len(res.Skipped) > 0 {
		fmt.Fprintln(w, "\n  Checks that did not run:")
		for _, sk := range res.Skipped {
			scope := sk.Scope
			if scope == "" {
				scope = "all"
			}
			fmt.Fprintf(w, "    %s (%s): %s\n", sk.Code, scope, sk.Reason)
		}
	}
	fmt.Fprintln(w)
}

// --- conversions ---------------------------------------------------------

// toStoreRecords maps analysis output onto store rows.
func toStoreRecords(ops []sqlops.Operation) []store.SQLOperationRecord {
	out := make([]store.SQLOperationRecord, 0, len(ops))
	for _, op := range ops {
		rec := store.SQLOperationRecord{
			Ref: op.Ref(), Source: string(op.Source), Name: op.Name,
			FilePath: op.Position.Path, Line: op.Position.Line,
			SymbolName: op.SymbolName,
			Kind:       storeKind(op),
			Resolved:   op.Resolved, UnresolvedReason: op.UnresolvedReason,
			SQLText: op.SQL, RowScan: string(op.RowScan),
			ParamCount:    op.Statement.ParamCount,
			Interpolation: string(op.Interpolation),
			CallerData:    op.CallerData,
			HasLimit:      op.Statement.HasLimit,
			HasOffset:     op.Statement.HasOffset,
			HasOrderBy:    op.Statement.HasOrderBy,
			Keyset:        op.Statement.Keyset,
			SelectStar:    op.Statement.SelectStar,
			OffsetBound:   storeOffsetBound(op),
			Suppressions:  op.Suppressions,
		}
		for _, t := range op.Statement.Tables {
			rec.Tables = append(rec.Tables, store.SQLTableAccess{
				Table: t.Table, Access: string(t.Access),
			})
		}
		for _, p := range op.Statement.Predicates {
			rec.Predicates = append(rec.Predicates, store.SQLPredicate{
				Clause: string(p.Clause), Table: p.Table, Column: p.Column,
				Operator: p.Operator, Bound: p.Bound,
			})
		}
		out = append(out, rec)
	}
	return out
}

// storeKind keeps the schema's CHECK constraint honest: an unresolved
// operation has no statement, and writing its zero-valued Kind would store an
// empty string where the column expects a known verb.
func storeKind(op sqlops.Operation) string {
	if !op.Resolved || op.Statement.Kind == "" {
		return "unknown"
	}
	return string(op.Statement.Kind)
}

func storeOffsetBound(op sqlops.Operation) string {
	if op.Statement.OffsetBound == "" {
		return string(sqlops.OffsetNone)
	}
	return string(op.Statement.OffsetBound)
}

func toStoreSchema(sc sqlops.Schema) ([]store.SQLTableRow, []store.SQLIndexRow) {
	tables := make([]store.SQLTableRow, 0, len(sc.Tables))
	for _, t := range sc.Tables {
		tables = append(tables, store.SQLTableRow{
			Name: t.Name, FilePath: t.Position.Path, Line: t.Position.Line,
		})
	}
	indexes := make([]store.SQLIndexRow, 0, len(sc.Indexes))
	for _, ix := range sc.Indexes {
		indexes = append(indexes, store.SQLIndexRow{
			Table: ix.Table, Name: ix.Name, Columns: ix.Columns,
			Unique: ix.Unique, Predicate: ix.Predicate, Origin: string(ix.Origin),
			FilePath: ix.Position.Path, Line: ix.Position.Line,
		})
	}
	return tables, indexes
}

// toSQLOpsOperations rebuilds analysis values from stored rows so the advisory
// pass runs over exactly what was persisted. Rebuilding rather than re-parsing
// is the point: `advise` must be able to explain a finding by pointing at a
// row someone else can read.
func toSQLOpsOperations(rows []store.SQLOperationRecord) []sqlops.Operation {
	out := make([]sqlops.Operation, 0, len(rows))
	for _, r := range rows {
		op := sqlops.Operation{
			Source:     sqlops.OperationSource(r.Source),
			Name:       r.Name,
			Position:   shared.FilePosition{Path: r.FilePath, Line: r.Line},
			SymbolName: r.SymbolName,
			Resolved:   r.Resolved, UnresolvedReason: r.UnresolvedReason,
			SQL: r.SQLText, RowScan: sqlops.RowScan(r.RowScan),
			Interpolated:  r.Interpolation != "",
			Interpolation: sqlops.Interpolation(r.Interpolation),
			CallerData:    r.CallerData,
			Suppressions:  r.Suppressions,
			Statement: sqlops.Statement{
				Kind:        sqlops.StatementKind(r.Kind),
				ParamCount:  r.ParamCount,
				HasLimit:    r.HasLimit,
				HasOffset:   r.HasOffset,
				HasOrderBy:  r.HasOrderBy,
				Keyset:      r.Keyset,
				SelectStar:  r.SelectStar,
				OffsetBound: sqlops.OffsetBound(r.OffsetBound),
			},
		}
		for _, t := range r.Tables {
			op.Statement.Tables = append(op.Statement.Tables, sqlops.TableAccess{
				Table: t.Table, Access: sqlops.Access(t.Access),
			})
		}
		for _, p := range r.Predicates {
			op.Statement.Predicates = append(op.Statement.Predicates, sqlops.Predicate{
				Clause: sqlops.PredicateClause(p.Clause), Table: p.Table,
				Column: p.Column, Operator: p.Operator, Bound: p.Bound,
			})
		}
		out = append(out, op)
	}
	return out
}

// loadStoredSchema rebuilds the Schema value the index checks consult. A store
// with no rows yields an empty Schema, which is what makes those checks report
// "did not run" instead of "no index exists".
func loadStoredSchema(ctx context.Context, s *store.Store) (sqlops.Schema, error) {
	tables, err := s.SQLOps().Tables(ctx)
	if err != nil {
		return sqlops.Schema{}, fmt.Errorf("sql advise: load tables: %w", err)
	}
	indexes, err := s.SQLOps().Indexes(ctx)
	if err != nil {
		return sqlops.Schema{}, fmt.Errorf("sql advise: load indexes: %w", err)
	}
	schemaTables := make([]sqlops.SchemaTable, 0, len(tables))
	for _, t := range tables {
		schemaTables = append(schemaTables, sqlops.SchemaTable{
			Name:     t.Name,
			Position: shared.FilePosition{Path: t.FilePath, Line: t.Line},
		})
	}
	schemaIndexes := make([]sqlops.Index, 0, len(indexes))
	for _, ix := range indexes {
		schemaIndexes = append(schemaIndexes, sqlops.Index{
			Table: ix.Table, Name: ix.Name, Columns: ix.Columns,
			Unique: ix.Unique, Predicate: ix.Predicate,
			Origin:   sqlops.IndexOrigin(ix.Origin),
			Position: shared.FilePosition{Path: ix.FilePath, Line: ix.Line},
		})
	}
	return sqlops.NewSchema(schemaTables, schemaIndexes), nil
}

// --- shared helpers ------------------------------------------------------

// sqlCmdContext is the local alias for the shared context helper, kept so this
// file reads without a jump to cov_shim.go.
func sqlCmdContext(cmd *cobra.Command) context.Context { return cmdContext(cmd) }

// confidenceRank maps the --min-confidence flag onto a threshold. An unknown
// value is an error rather than a silent default: a CI job filtering on a
// typo'd level would report a clean run forever.
func confidenceRank(v string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "low":
		return 0, nil
	case "medium":
		return 1, nil
	case "high":
		return 2, nil
	}
	return 0, fmt.Errorf("sql advise: unknown --min-confidence %q; allowed: low|medium|high", v)
}

func rankOf(c sqlops.Confidence) int {
	switch c {
	case sqlops.ConfidenceHigh:
		return 2
	case sqlops.ConfidenceMedium:
		return 1
	}
	return 0
}

func filterByConfidence(in []sqlops.Advisory, min int) []sqlops.Advisory {
	out := make([]sqlops.Advisory, 0, len(in))
	for _, a := range in {
		if rankOf(a.Confidence) >= min {
			out = append(out, a)
		}
	}
	return out
}

func filterOperations(in []store.SQLOperationRecord, onlyUnresolved bool, table string) []store.SQLOperationRecord {
	out := make([]store.SQLOperationRecord, 0, len(in))
	for _, op := range in {
		if onlyUnresolved && op.Resolved {
			continue
		}
		if table != "" && !touchesTable(op, table) {
			continue
		}
		out = append(out, op)
	}
	return out
}

func touchesTable(op store.SQLOperationRecord, table string) bool {
	for _, t := range op.Tables {
		if t.Table == table {
			return true
		}
	}
	return false
}

func countResolution(rows []store.SQLOperationRecord) (resolved, unresolved int) {
	for _, r := range rows {
		if r.Resolved {
			resolved++
		} else {
			unresolved++
		}
	}
	return resolved, unresolved
}

// fraction mirrors sqlops.Report.ResolvedFraction: an empty inventory is
// fully resolved, because nothing was missed.
func fraction(resolved, unresolved int) float64 {
	total := resolved + unresolved
	if total == 0 {
		return 1
	}
	return float64(resolved) / float64(total)
}

// coverageLine is the honesty banner every verb prints. It is deliberately
// impossible to read the advisory list without also reading how much of the
// data layer it covers.
func coverageLine(resolved, unresolved int) string {
	total := resolved + unresolved
	return fmt.Sprintf("%d operations: %d resolved, %d unresolved (%.0f%% of the data layer analysed)",
		total, resolved, unresolved, fraction(resolved, unresolved)*100)
}

// unresolvedReasonCount is one row of the unresolved breakdown.
type unresolvedReasonCount struct {
	reason string
	count  int
}

func unresolvedReasonCounts(ops []sqlops.Operation) []unresolvedReasonCount {
	tally := map[string]int{}
	for _, op := range ops {
		if !op.Resolved {
			tally[op.UnresolvedReason]++
		}
	}
	out := make([]unresolvedReasonCount, 0, len(tally))
	for reason, n := range tally {
		out = append(out, unresolvedReasonCount{reason: reason, count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].count != out[j].count {
			return out[i].count > out[j].count
		}
		return out[i].reason < out[j].reason
	})
	return out
}
