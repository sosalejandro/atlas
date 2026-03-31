package adapters

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// SQLCMapping maps a generated Go method to its SQL source.
type SQLCMapping struct {
	GoMethod  string // e.g. "GetUserByEmail"
	SQLFile   string // e.g. "src/domain/repositories/queries/users.sql"
	SQLLine   int
	QueryName string // e.g. "GetUserByEmail" (from -- name: annotation)
	QueryType string // "one", "many", "exec", "execrows"
}

// SQLCMapper reads SQLC config and query files to build method-to-SQL mappings.
type SQLCMapper struct{}

// NewSQLCMapper creates a new mapper.
func NewSQLCMapper() *SQLCMapper {
	return &SQLCMapper{}
}

// sqlcConfig represents the subset of sqlc.yaml we need to parse.
type sqlcConfig struct {
	Version string          `yaml:"version"`
	SQL     []sqlcSQLEntry  `yaml:"sql"`
}

// sqlcSQLEntry represents one entry in the sql array.
type sqlcSQLEntry struct {
	Engine  string          `yaml:"engine"`
	Queries string          `yaml:"queries"`
	Schema  string          `yaml:"schema"`
	Gen     sqlcGenSection  `yaml:"gen"`
}

// sqlcGenSection represents the gen section.
type sqlcGenSection struct {
	Go sqlcGoGen `yaml:"go"`
}

// sqlcGoGen represents the go generation config.
type sqlcGoGen struct {
	Package string `yaml:"package"`
	Out     string `yaml:"out"`
}

// nameAnnotationRe matches SQLC query annotations like:
//
//	-- name: GetUserByEmail :one
//	-- name: ListRecipes :many
var nameAnnotationRe = regexp.MustCompile(`^--\s*name:\s*(\w+)\s+:(one|many|exec|execrows|execresult|batchone|batchmany|batchexec|copyfrom)\s*$`)

// Map reads the SQLC config file and all referenced query directories.
// Returns a map of GoMethodName to SQLCMapping.
func (m *SQLCMapper) Map(projectRoot, sqlcConfigPath string) (map[string]SQLCMapping, error) {
	cfgPath := sqlcConfigPath
	if !filepath.IsAbs(cfgPath) {
		cfgPath = filepath.Join(projectRoot, cfgPath)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("reading sqlc config %s: %w", cfgPath, err)
	}

	var cfg sqlcConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing sqlc config %s: %w", cfgPath, err)
	}

	result := make(map[string]SQLCMapping)

	for _, entry := range cfg.SQL {
		queriesDir := entry.Queries
		if !filepath.IsAbs(queriesDir) {
			queriesDir = filepath.Join(projectRoot, queriesDir)
		}

		sqlFiles, err := findSQLFiles(queriesDir)
		if err != nil {
			return nil, fmt.Errorf("scanning query directory %s: %w", queriesDir, err)
		}

		for _, sqlFile := range sqlFiles {
			mappings, err := parseSQLFile(sqlFile)
			if err != nil {
				return nil, fmt.Errorf("parsing SQL file %s: %w", sqlFile, err)
			}

			// Store relative paths from project root for portability.
			relPath, relErr := filepath.Rel(projectRoot, sqlFile)
			if relErr != nil {
				relPath = sqlFile
			}
			relPath = filepath.ToSlash(relPath)

			for _, mapping := range mappings {
				mapping.SQLFile = relPath
				result[mapping.GoMethod] = mapping
			}
		}
	}

	return result, nil
}

// findSQLFiles returns all .sql files under dir. If dir is a file rather than
// a directory, it returns that single file (SQLC supports both file and
// directory values for the queries field).
func findSQLFiles(dir string) ([]string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}

	if !info.IsDir() {
		if strings.HasSuffix(dir, ".sql") {
			return []string{dir}, nil
		}
		return nil, nil
	}

	var files []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".sql") {
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}

	return files, nil
}

// parseSQLFile scans a single .sql file for SQLC name annotations and returns
// the mappings found.
func parseSQLFile(path string) ([]SQLCMapping, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var mappings []SQLCMapping
	scanner := bufio.NewScanner(f)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		matches := nameAnnotationRe.FindStringSubmatch(line)
		if matches == nil {
			continue
		}

		queryName := matches[1]
		queryType := matches[2]

		mappings = append(mappings, SQLCMapping{
			GoMethod:  queryName, // SQLC generates Go methods with the same name as the query.
			SQLLine:   lineNum,
			QueryName: queryName,
			QueryType: queryType,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return mappings, nil
}
