package sqlops

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// sqlcConfigNames are the file names sqlc itself looks for. JSON parses
// through the YAML decoder because YAML is a superset of it.
var sqlcConfigNames = map[string]bool{
	"sqlc.yaml": true, "sqlc.yml": true, "sqlc.json": true,
}

// conventionalSchemaDirs are probed when no sqlc config names a schema. They
// are the standard migration locations; a directory that does not exist is
// skipped, so probing costs nothing.
var conventionalSchemaDirs = []string{
	"db/migrations", "migrations", "sql/migrations", "db/schema", "schema",
}

// sqlcConfig is the subset of sqlc's config Atlas reads: where the schema
// lives and where the queries live. Both fields accept a string or a list in
// sqlc's own schema, so both are decoded permissively.
type sqlcConfig struct {
	SQL []struct {
		Engine  string    `yaml:"engine"`
		Schema  yaml.Node `yaml:"schema"`
		Queries yaml.Node `yaml:"queries"`
	} `yaml:"sql"`
}

// discoverSQLCPaths walks root for sqlc configs and returns the schema and
// query directories they name, resolved relative to each config's own
// directory (which is how sqlc resolves them).
func discoverSQLCPaths(root string, skip map[string]bool) (schemaDirs, queryDirs []string, err error) {
	walkErr := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable subtree is skipped, not fatal.
		}
		if d.IsDir() {
			if p != root && skip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !sqlcConfigNames[d.Name()] {
			return nil
		}
		s, q, readErr := readSQLCConfig(p)
		if readErr != nil {
			// A malformed sqlc config is the project's business, not a
			// reason to abandon the whole analysis.
			return nil
		}
		schemaDirs = append(schemaDirs, s...)
		queryDirs = append(queryDirs, q...)
		return nil
	})
	if walkErr != nil {
		return nil, nil, fmt.Errorf("walk for sqlc config under %s: %w", root, walkErr)
	}
	return dedupeStrings(schemaDirs), dedupeStrings(queryDirs), nil
}

func readSQLCConfig(path string) (schemaDirs, queryDirs []string, err error) {
	b, err := os.ReadFile(path) //nolint:gosec // path comes from a directory walk the caller chose.
	if err != nil {
		return nil, nil, fmt.Errorf("read sqlc config %s: %w", path, err)
	}
	var cfg sqlcConfig
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, nil, fmt.Errorf("parse sqlc config %s: %w", path, err)
	}
	base := filepath.Dir(path)
	for _, entry := range cfg.SQL {
		schemaDirs = append(schemaDirs, resolveAgainst(base, nodeStrings(entry.Schema))...)
		queryDirs = append(queryDirs, resolveAgainst(base, nodeStrings(entry.Queries))...)
	}
	return schemaDirs, queryDirs, nil
}

// nodeStrings flattens a YAML node that is either a scalar path or a sequence
// of them.
func nodeStrings(n yaml.Node) []string {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Value == "" {
			return nil
		}
		return []string{n.Value}
	case yaml.SequenceNode:
		out := make([]string, 0, len(n.Content))
		for _, c := range n.Content {
			if c.Kind == yaml.ScalarNode && c.Value != "" {
				out = append(out, c.Value)
			}
		}
		return out
	}
	return nil
}

// resolveAgainst turns config-relative paths into filesystem paths. A path
// naming a single .sql file resolves to its directory: both the schema and
// query readers walk directories, and reading the file's siblings costs
// nothing while missing a schema split across files costs the index checks.
func resolveAgainst(base string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		full := p
		if !filepath.IsAbs(full) {
			full = filepath.Join(base, p)
		}
		if strings.HasSuffix(full, ".sql") {
			full = filepath.Dir(full)
		}
		out = append(out, filepath.Clean(full))
	}
	return out
}

// probeConventionalSchemaDirs returns the conventional migration directories
// that actually exist under root.
func probeConventionalSchemaDirs(root string) []string {
	var out []string
	for _, rel := range conventionalSchemaDirs {
		p := filepath.Join(root, rel)
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
