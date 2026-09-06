package redact

// This file is the machine-readable half of docs/security.md.
//
// The data-handling statement makes a specific promise -- "here is every
// table atlas writes and here is what goes in it" -- and a promise like that
// rots the moment a migration lands. So the enumeration lives here, next to
// the code that acts on it, and schema_test.go compares it against the
// schema a migrated store actually has. A new table or a new TEXT column
// fails the build until someone says what it holds; the document is then
// written from this list rather than from memory.
//
// Two facts per column matter to a security review, and they are separate:
//
//   Class      -- what the column holds, which is what the reviewer is
//                 asking about. The distinction that carries the weight is
//                 ClassSourceText: those columns hold text copied verbatim
//                 out of the repository, so "atlas stores only structure"
//                 is false and this list is the proof.
//
//   Redactable -- whether `atlas security redact` may rewrite the column in
//                 place. Only free text qualifies. Rewriting an identifier
//                 or a path would not remove a disclosure, it would change
//                 what the index means, and a secret that ended up in a
//                 SYMBOL NAME has to be fixed in the source anyway.

// Class is what kind of content a stored column holds.
type Class string

const (
	// ClassPath is a filesystem path. Repo-relative everywhere except
	// coverage_runs.raw_path, which records where a coverage report was
	// read from and can therefore be absolute and machine-specific.
	ClassPath Class = "path"

	// ClassIdentifier is a name declared in the source: a symbol, package,
	// table, column, feature or owner. Not a secret by itself, but it is
	// the shape of the system and it is proprietary.
	ClassIdentifier Class = "identifier"

	// ClassSourceText is text copied VERBATIM out of the repository: SQL
	// query text, branch conditions, annotation values, doc comments and
	// signatures inside a snapshot blob. This is the class that can contain
	// a credential, and the class that makes the state database proprietary
	// content rather than metadata about it.
	ClassSourceText Class = "source-text"

	// ClassUserText is free text a person supplied on the command line or
	// in config -- a snapshot note, a trend annotation. Small, but written
	// by a human and therefore capable of holding anything.
	ClassUserText Class = "user-text"

	// ClassEnum is a value from a closed vocabulary atlas assigns itself.
	// It cannot carry content out of the repository.
	ClassEnum Class = "enum"

	// ClassDigest is a hash of file content. One-way; it discloses only
	// whether two files are identical.
	ClassDigest Class = "digest"
)

// Table describes one table of the atlas state database.
type Table struct {
	Name    string `json:"name"`
	Purpose string `json:"purpose"`
}

// Column describes one TEXT column. Non-TEXT columns are deliberately
// absent: they hold counts, scores, line numbers and timestamps, none of
// which can carry text out of the repository, and enumerating them would
// bury the ones that can.
type Column struct {
	Table string `json:"table"`
	Name  string `json:"name"`
	Class Class  `json:"class"`

	// Holds is the one-line description printed by `atlas security` and
	// reproduced in docs/security.md. Required.
	Holds string `json:"holds"`

	// Redactable allows `atlas security redact` to rewrite this column.
	Redactable bool `json:"redactable"`
}

// Tables returns every table of the state database, in schema order.
func Tables() []Table { return tables }

// Columns returns every TEXT column of the state database.
func Columns() []Column { return columns }

var tables = []Table{
	{"schema_migrations", "golang-migrate's bookkeeping: applied version and dirty flag"},
	{"config", "runtime knobs written by atlas itself"},
	{"features", "one row per @atlas:feature / @atlas:contract id found in the source"},
	{"symbols", "one row per indexed declaration: name, kind, file and line span"},
	{"edges", "call, implement, embed, import and inheritance relations between symbols"},
	{"feature_symbols", "which symbols implement or test which feature"},
	{"file_hashes", "content hash and mtime per scanned file; drives incremental re-scan"},
	{"skipped_files", "the exclusion ledger: which files the walk declined to index, and why"},
	{"annotations", "the raw @atlas annotations parsed out of comments, before resolution"},
	{"coverage_runs", "one row per ingested test/coverage report, with attribution counters"},
	{"coverage_symbol_spans", "the [line, end_line] span each measured symbol occupied at the time of a run, so a later carry can tell whether the symbol it would carry is still the symbol that was measured (#136)"},
	{"coverage_results", "per-symbol coverage outcome and statement counts for a run"},
	{"coverage_run_gaps", "files in a coverage report atlas could not attribute to a symbol"},
	{"test_coverage", "which production symbols each individual test executed"},
	{"coverage_history", "the per-commit score series behind `atlas trend`"},
	{"coverage_history_features", "the per-feature breakdown of one history point"},
	{"snapshots", "a whole serialised index at a git ref, for `atlas diff`"},
	{"audit_snapshot_runs", "a whole-project audit score blob, per computation"},
	{"sql_operations", "every SQL operation found in the source, including its query text"},
	{"sql_operation_tables", "which tables each operation reads and writes"},
	{"sql_operation_predicates", "the columns each operation filters and joins on"},
	{"sql_tables", "tables atlas read out of the DDL in the repository"},
	{"sql_indexes", "indexes atlas read out of the DDL in the repository"},
	{"cfg_blocks", "control-flow graph nodes per symbol"},
	{"cfg_edges", "control-flow graph edges, carrying the source text of each condition"},
	{"cfg_symbols", "per-symbol structural metrics: complexity, decisions, conditions"},
	{"cfg_decision_coverage", "per-symbol decision-coverage counters"},
	{"cfg_findings", "control-flow diagnostics: query-in-loop, unreachable, untested branch"},
}

//nolint:lll // one column per line reads as a table; wrapping it would not.
var columns = []Column{
	{"annotations", "file_path", ClassPath, "repo-relative path the annotation was found in", false},
	{"annotations", "kind", ClassEnum, "annotation kind (feature, contract, owner, bc, saga, ...)", false},
	{"annotations", "value", ClassSourceText, "the annotation's argument text, verbatim from the comment", true},
	{"annotations", "source", ClassEnum, "which grammar produced it (atlas, testreg)", false},

	{"audit_snapshot_runs", "score_json", ClassSourceText, "serialised audit scores; carries feature ids and titles", true},

	{"cfg_blocks", "kind", ClassEnum, "block kind (entry, body, branch, loop, exit)", false},

	{"cfg_decision_coverage", "source", ClassEnum, "which coverage run the decision counters came from", false},

	{"cfg_edges", "kind", ClassEnum, "edge kind (seq, true, false, loop-back, fallthrough)", false},
	{"cfg_edges", "condition", ClassSourceText, "the SOURCE TEXT of the branch condition, verbatim", true},

	{"cfg_findings", "kind", ClassEnum, "finding kind (flow.query-in-loop, flow.unreachable, ...)", false},
	{"cfg_findings", "confidence", ClassEnum, "high, medium or low", false},
	{"cfg_findings", "detail", ClassSourceText, "the finding's explanation, which quotes source constructs", true},

	{"config", "key", ClassIdentifier, "config key atlas set", false},
	{"config", "value", ClassUserText, "config value atlas set", true},

	{"coverage_history", "commit_sha", ClassIdentifier, "the commit (or tag) a measurement was taken at", false},
	{"coverage_history", "note", ClassUserText, "free-form note passed to `atlas trend record`", true},

	{"coverage_history_features", "feature_id", ClassIdentifier, "the feature this breakdown row scores", false},

	{"coverage_results", "feature_id", ClassIdentifier, "the feature the result was attributed to", false},
	{"coverage_results", "status", ClassEnum, "pass, fail or skip", false},
	{"coverage_results", "message", ClassSourceText, "the test framework's message; failure output can quote data", true},

	{"coverage_run_gaps", "path", ClassPath, "a file in the coverage report atlas could not attribute", false},
	{"coverage_run_gaps", "reason", ClassEnum, "why the file could not be attributed", false},

	{"coverage_runs", "framework", ClassEnum, "go-test, playwright, vitest, jest or maestro", false},
	{"coverage_symbol_spans", "file_path", ClassPath, "repo-relative path the measured symbol occupied when the run was ingested", false},
	{"coverage_runs", "raw_path", ClassPath, "where the report was read from; may be an absolute local path", false},
	{"coverage_runs", "summary_json", ClassSourceText, "the ingest's own summary of the report", true},
	{"coverage_runs", "run_group", ClassIdentifier, "caller-chosen key joining several runs into one measurement", false},

	{"edges", "kind", ClassEnum, "call, implement, embed, construct, import, inheritance, decorator", false},
	{"edges", "file_path", ClassPath, "repo-relative path the relation was observed in", false},
	{"edges", "edge_meta", ClassEnum, "kind-specific qualifier, e.g. the scope of a Python import", false},

	{"feature_symbols", "feature_id", ClassIdentifier, "the feature side of the link", false},
	{"feature_symbols", "role", ClassEnum, "test, impl or contract", false},
	{"feature_symbols", "source", ClassEnum, "annotation or inferred", false},

	{"features", "id", ClassIdentifier, "the feature id as written in the annotation", false},
	{"features", "title", ClassSourceText, "the feature's human title, taken from the source", true},
	{"features", "owner", ClassIdentifier, "the owner handle from an @atlas:owner annotation", false},
	{"features", "kind", ClassEnum, "feature or contract", false},
	{"features", "deprecated_since", ClassIdentifier, "version string from an @atlas:deprecated annotation", false},
	{"features", "introduced_in", ClassIdentifier, "version string from an @atlas:since annotation", false},

	{"file_hashes", "file_path", ClassPath, "repo-relative path of a scanned file", false},
	{"file_hashes", "content_hash", ClassDigest, "SHA-256 of the file content at scan time", false},

	{"skipped_files", "file_path", ClassPath, "repo-relative path of a file the walk skipped", false},
	{"skipped_files", "rule", ClassEnum, "which exclusion rule claimed the file", false},
	{"skipped_files", "detail", ClassSourceText, "the evidence for the rule, e.g. the matched glob or header", true},

	{"snapshots", "git_ref", ClassIdentifier, "the git ref the snapshot was captured at", false},
	{"snapshots", "index_json", ClassSourceText, "THE WHOLE SERIALISED INDEX: symbol DOC COMMENTS and SIGNATURES included", true},
	{"snapshots", "audit_json", ClassSourceText, "the audit slice at that ref", true},
	{"snapshots", "notes", ClassUserText, "free-form note passed to `atlas snapshot --note`", true},

	{"sql_indexes", "table_name", ClassIdentifier, "the table the index is declared on", false},
	{"sql_indexes", "name", ClassIdentifier, "the index name", false},
	{"sql_indexes", "columns", ClassIdentifier, "the indexed column list, verbatim from the DDL", false},
	{"sql_indexes", "predicate", ClassSourceText, "the partial-index WHERE clause, verbatim from the DDL", true},
	{"sql_indexes", "origin", ClassEnum, "create-index, primary-key or unique-constraint", false},
	{"sql_indexes", "file_path", ClassPath, "repo-relative path of the DDL file", false},

	{"sql_operation_predicates", "clause", ClassEnum, "where or join", false},
	{"sql_operation_predicates", "table_name", ClassIdentifier, "the table the predicate filters", false},
	{"sql_operation_predicates", "column_name", ClassIdentifier, "the column the predicate filters", false},
	{"sql_operation_predicates", "operator", ClassEnum, "the comparison operator", false},

	{"sql_operation_tables", "table_name", ClassIdentifier, "a table the operation touches", false},
	{"sql_operation_tables", "access", ClassEnum, "read or write", false},

	{"sql_operations", "ref", ClassIdentifier, "fingerprint of source, file, line and name", false},
	{"sql_operations", "source", ClassEnum, "go or sql", false},
	{"sql_operations", "name", ClassIdentifier, "the query's name, for named queries", false},
	{"sql_operations", "file_path", ClassPath, "repo-relative path the query was found in", false},
	{"sql_operations", "symbol_name", ClassIdentifier, "the enclosing symbol's qualified name", false},
	{"sql_operations", "kind", ClassEnum, "select, insert, update, delete, other or unknown", false},
	{"sql_operations", "unresolved_reason", ClassSourceText, "why the query could not be read; quotes the construct", true},
	{"sql_operations", "sql_text", ClassSourceText, "THE QUERY TEXT, verbatim, including any inline literals", true},
	{"sql_operations", "row_scan", ClassEnum, "slice, single, exec or unknown", false},
	{"sql_operations", "interpolation", ClassSourceText, "the interpolated fragment, verbatim, when the query is built by concatenation", true},
	{"sql_operations", "offset_bound", ClassEnum, "none, parameter, literal or expression", false},
	{"sql_operations", "suppressions", ClassSourceText, "the atlas directives found on the enclosing declaration", true},

	{"sql_tables", "name", ClassIdentifier, "a table name read out of the DDL", false},
	{"sql_tables", "file_path", ClassPath, "repo-relative path of the DDL file", false},

	{"symbols", "qualified_name", ClassIdentifier, "the symbol's fully qualified name", false},
	{"symbols", "kind", ClassEnum, "type, func, method, interface, var or const", false},
	{"symbols", "file_path", ClassPath, "repo-relative path the symbol is declared in", false},
	{"symbols", "package", ClassIdentifier, "the declaring package", false},
	{"symbols", "bc_path", ClassPath, "the bounded-context prefix of the file path", false},
	{"symbols", "pattern_matches", ClassSourceText, "serialised EDA recogniser hits, which quote source constructs", true},
}
