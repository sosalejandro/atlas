// Package sqlops turns a repository's data access layer from a symbol name
// into something you can ask questions of.
//
// Atlas already knows that a repository method is covered by a test. What it
// could not say -- and what actually breaks in production -- is that the query
// inside that method has no LIMIT, takes a caller-supplied offset, and filters
// on a column no index leads with. This package extracts SQL where it is
// statically visible, classifies its shape, and turns that shape into
// advisories with a confidence attached.
//
// The design commitment that matters most is the one about what it CANNOT
// read. A query assembled by a builder, or across functions, is recorded as
// unresolved with the reason -- never dropped. Analysing the easy half of a
// codebase and reporting a clean bill of health is the failure mode this
// package is built to avoid, so every report carries the resolved fraction and
// every check that did not run says so.
//
// It is deliberately not a SQL engine: no grammar, no planner, no cost model.
// See lex.go for the boundary and statement.go for what is extracted.
package sqlops

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Options configures a full analysis pass.
type Options struct {
	// Root is the directory to analyse. Every path in the Report is relative
	// to it.
	Root string
	// SchemaDirs holds DDL. Empty means: read the schema paths any sqlc
	// config names, plus the conventional migration directories that exist.
	SchemaDirs []string
	// QueryDirs holds sqlc .sql query files. Empty means: read the query
	// paths any sqlc config names.
	QueryDirs []string
	// Extract tunes the Go sweep.
	Extract ExtractOptions
	// Advise tunes advisory generation.
	Advise AdviseOptions
	// SkipAdvisories analyses and stores without running the checks.
	SkipAdvisories bool
}

// Report is one analysis pass.
type Report struct {
	Operations []Operation    `json:"operations"`
	Schema     Schema         `json:"schema"`
	Advisories []Advisory     `json:"advisories"`
	Skipped    []SkippedCheck `json:"skipped_checks,omitempty"`

	// Resolved and Unresolved are the honesty counters. Their ratio is
	// printed with every run: an advisory list is only as trustworthy as the
	// fraction of the data layer it was computed over.
	Resolved   int `json:"resolved"`
	Unresolved int `json:"unresolved"`

	// Merged is how many generated call sites were reconciled onto the .sql
	// query that defines them. It is reported rather than quietly applied
	// because it changes the denominator of the resolved fraction, and a
	// number that moves for reasons the report does not explain is a number
	// nobody trusts.
	Merged int `json:"merged"`

	// SchemaDirs and QueryDirs record what was actually read, so a reader can
	// see why a check reports "not run".
	SchemaDirs []string `json:"schema_dirs,omitempty"`
	QueryDirs  []string `json:"query_dirs,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
}

// ResolvedFraction is the share of operations whose shape Atlas could read.
// An empty inventory returns 1: nothing was missed because nothing was found,
// and reporting 0% resolved for a repository with no SQL in it would be a lie
// in the other direction.
func (r Report) ResolvedFraction() float64 {
	total := r.Resolved + r.Unresolved
	if total == 0 {
		return 1
	}
	return float64(r.Resolved) / float64(total)
}

// Analyze extracts, classifies and (unless suppressed) advises over a
// repository.
func Analyze(opts Options) (Report, error) {
	if opts.Root == "" {
		return Report{}, fmt.Errorf("sqlops: root is required")
	}
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return Report{}, fmt.Errorf("sqlops: abs root %s: %w", opts.Root, err)
	}

	schemaDirs, queryDirs, err := resolveInputDirs(root, opts)
	if err != nil {
		return Report{}, err
	}

	rep := Report{
		SchemaDirs: relativeAll(root, schemaDirs),
		QueryDirs:  relativeAll(root, queryDirs),
	}
	if rep.Schema, err = ParseSchemaDirs(schemaDirs, root); err != nil {
		return Report{}, err
	}
	goOps, warnings, err := ExtractGo(root, opts.Extract)
	if err != nil {
		return Report{}, err
	}
	rep.Warnings = warnings
	sqlOps, err := ExtractSQLFiles(queryDirs, root)
	if err != nil {
		return Report{}, err
	}

	rep.Operations, rep.Merged = reconcile(goOps, sqlOps)
	sortOperations(rep.Operations)
	assignOrdinals(rep.Operations)
	for _, op := range rep.Operations {
		if op.Resolved {
			rep.Resolved++
		} else {
			rep.Unresolved++
		}
	}
	if !opts.SkipAdvisories {
		res := Advise(rep.Operations, rep.Schema, opts.Advise)
		rep.Advisories, rep.Skipped = res.Advisories, res.Skipped
	}
	return rep, nil
}

// reconcile folds the two views of a sqlc project into one.
//
// A sqlc query exists twice in the sources: as the `-- name: X :many` block in
// the .sql file, and as the generated Go call that passes that same text --
// header comment and all -- to database/sql. Counting both counts every query
// in the data layer twice. That is not a cosmetic inflation: it doubles the
// operation count, and it moves the resolved fraction, which is the number the
// honesty contract rests on and the one a CI gate reads.
//
// The .sql operation is the one kept. It is the definition rather than a
// generated echo of it, its `:one`/`:many` annotation is better evidence of
// the row shape than any inference from the generated body, and its file:line
// is where a developer would go to change the query. The generated call's
// suppression directives are merged onto it so a directive written in either
// place still silences the advisory.
//
// Only cross-source pairs merge. Two Go call sites issuing identical SQL are
// two places that can be slow, and collapsing them would hide one.
func reconcile(goOps, sqlOps []Operation) ([]Operation, int) {
	byName := make(map[string]int, len(sqlOps))
	for i, op := range sqlOps {
		if op.Name != "" {
			byName[op.Name] = i
		}
	}
	out := make([]Operation, 0, len(goOps)+len(sqlOps))
	merged := 0
	for _, op := range goOps {
		name := sqlcQueryName(op.SQL)
		i, ok := byName[name]
		if name == "" || !ok {
			out = append(out, op)
			continue
		}
		sqlOps[i].Suppressions = mergeSuppressions(sqlOps[i].Suppressions, op.Suppressions)
		merged++
	}
	return append(out, sqlOps...), merged
}

// sqlcQueryName reads the `-- name: X :many` header sqlc copies verbatim into
// the query text it generates. The header must be the first thing in the text:
// a `-- name:` line further down belongs to some other query that happens to
// have been pasted along, and matching on it would merge two queries that are
// not the same one.
func sqlcQueryName(sqlText string) string {
	for _, line := range strings.Split(sqlText, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if m := nameAnnotationRe.FindStringSubmatch(line); m != nil {
			return m[1]
		}
		return ""
	}
	return ""
}

// resolveInputDirs turns the caller's (possibly empty) directory lists into
// absolute paths, discovering them from sqlc configs and conventional
// locations when they were not given.
func resolveInputDirs(root string, opts Options) (schemaDirs, queryDirs []string, err error) {
	skip := setOf(opts.Extract.withDefaults().SkipDirs)
	discoveredSchema, discoveredQueries, err := discoverSQLCPaths(root, skip)
	if err != nil {
		return nil, nil, err
	}

	schemaDirs = absAll(root, opts.SchemaDirs)
	if len(schemaDirs) == 0 {
		schemaDirs = append(discoveredSchema, probeConventionalSchemaDirs(root)...)
	}
	queryDirs = absAll(root, opts.QueryDirs)
	if len(queryDirs) == 0 {
		queryDirs = discoveredQueries
	}
	return dedupeStrings(schemaDirs), dedupeStrings(queryDirs), nil
}

func absAll(root string, dirs []string) []string {
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if !filepath.IsAbs(d) {
			d = filepath.Join(root, d)
		}
		out = append(out, filepath.Clean(d))
	}
	return out
}

func relativeAll(root string, dirs []string) []string {
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		out = append(out, relativeTo(root, d))
	}
	return out
}
