package sqlops

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/sosalejandro/atlas/packages/shared"
)

// nameAnnotationRe matches the sqlc query header:
//
//	-- name: GetUserByEmail :one
//
// It duplicates the pattern in internal/adapters/sqlc_mapper.go on purpose.
// That mapper answers "which .sql file does this generated method come from";
// this package answers "what does the query do", and coupling packages/ to
// internal/ to share a nine-token regex would be the worse trade.
var nameAnnotationRe = regexp.MustCompile(
	`^--\s*name:\s*(\w+)\s+:(one|many|exec|execrows|execlastid|execresult|batchone|batchmany|batchexec|copyfrom)\b`)

// sqlcScan maps a sqlc query type onto what the generated code does with the
// rows. This is better evidence than the Go call-site heuristic: `:many` is a
// declaration that the result becomes a slice, not an inference from a nearby
// append().
var sqlcScan = map[string]RowScan{
	"one": ScanSingle, "many": ScanSlice,
	"exec": ScanExec, "execrows": ScanExec, "execlastid": ScanExec,
	"execresult": ScanExec, "copyfrom": ScanExec,
	"batchone": ScanSingle, "batchmany": ScanSlice, "batchexec": ScanExec,
}

// ExtractSQLFiles reads the named queries out of every .sql file under the
// given directories. Paths in the result are relative to root.
func ExtractSQLFiles(dirs []string, root string) ([]Operation, error) {
	var ops []Operation
	for _, dir := range dirs {
		files, err := collectSQLFiles(dir)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			fileOps, err := extractQueryFile(f, root)
			if err != nil {
				return nil, err
			}
			ops = append(ops, fileOps...)
		}
	}
	sortOperations(ops)
	return ops, nil
}

// extractQueryFile splits one .sql file at its `-- name:` headers and analyses
// each query.
//
// A file with no headers yields nothing rather than being analysed as one
// giant statement: an unannotated .sql file is a migration or a seed, and
// those are the schema's business, not the query inventory's.
func extractQueryFile(path, root string) ([]Operation, error) {
	b, err := os.ReadFile(path) //nolint:gosec // paths come from a directory walk the caller chose.
	if err != nil {
		return nil, fmt.Errorf("read query file %s: %w", path, err)
	}
	lines := strings.Split(string(b), "\n")
	rel := relativeTo(root, path)

	heads := queryHeads(lines)
	ops := make([]Operation, 0, len(heads))
	for i, h := range heads {
		end := len(lines)
		if i+1 < len(heads) {
			end = heads[i+1].line - 1
		}
		ops = append(ops, buildQueryOperation(h, lines, end, rel))
	}
	return ops, nil
}

// queryHead is one `-- name: X :kind` annotation and the 1-based line it sits
// on.
type queryHead struct {
	name string
	kind string
	line int
}

func queryHeads(lines []string) []queryHead {
	var out []queryHead
	for i, l := range lines {
		m := nameAnnotationRe.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil {
			continue
		}
		out = append(out, queryHead{name: m[1], kind: m[2], line: i + 1})
	}
	return out
}

func buildQueryOperation(h queryHead, lines []string, end int, rel string) Operation {
	body := lines[h.line:min(end, len(lines))]
	text := strings.Join(body, "\n")

	scan, ok := sqlcScan[h.kind]
	if !ok {
		scan = ScanUnknown
	}
	op := Operation{
		Source: SourceSQLFile,
		Name:   h.name,
		// The Go scanner anchors sqlc queries in the graph as
		// `sql:<QueryName>` (packages/codeindex/go/scanner.go); matching that
		// id is what lets an operation join a trace instead of floating
		// beside it.
		SymbolName:   "sql:" + h.name,
		Position:     shared.FilePosition{Path: rel, Line: h.line},
		SQL:          strings.TrimSpace(text),
		RowScan:      scan,
		Suppressions: mergeSuppressions(queryDirectives(lines, h.line, end)),
	}
	if st, ok := AnalyzeStatement(text); ok {
		op.Resolved = true
		op.Statement = st
	} else {
		op.UnresolvedReason = ReasonNotAStatement
	}
	return op
}

// queryDirectives collects `atlas:sql-ignore` codes from the comment block
// that belongs to this query: the header line itself, the comment lines
// immediately above it, and the comment lines between the header and the SQL.
//
// The block below the header is where the natural spelling lives -- sqlc
// convention already puts a query's prose there -- and the bound at `end`
// stops a directive leaking into the next query.
func queryDirectives(lines []string, headLine, end int) []string {
	var out []string
	out = append(out, parseDirective(lines[headLine-1])...)
	for i := headLine - 2; i >= 0 && strings.HasPrefix(strings.TrimSpace(lines[i]), "--"); i-- {
		out = append(out, parseDirective(lines[i])...)
	}
	for i := headLine; i < min(end, len(lines)); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(trimmed, "--") {
			break
		}
		out = append(out, parseDirective(trimmed)...)
	}
	return out
}
