// @testreg trace.sqlc-mapper
package adapters

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSQLCMapper_Map_BasicConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Create a minimal sqlc.yaml.
	sqlcYAML := `version: "2"
sql:
  - engine: "postgresql"
    queries: "./queries"
    schema: "./schema.sql"
    gen:
      go:
        package: "db"
        out: "./generated"
`
	if err := os.WriteFile(filepath.Join(dir, "sqlc.yaml"), []byte(sqlcYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create queries directory with a SQL file.
	queriesDir := filepath.Join(dir, "queries")
	if err := os.MkdirAll(queriesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	userSQL := `-- User management queries

-- name: GetUserByID :one
SELECT id, email, name FROM users WHERE id = $1 AND is_active = true;

-- name: GetUserByEmail :one
SELECT id, email, name FROM users WHERE email = $1 AND is_active = true;

-- name: ListUsers :many
SELECT id, email, name FROM users WHERE is_active = true ORDER BY created_at DESC LIMIT $1 OFFSET $2;

-- name: CreateUser :one
INSERT INTO users (email, name) VALUES ($1, $2) RETURNING id, email, name;

-- name: UpdateUser :exec
UPDATE users SET name = $2 WHERE id = $1;

-- name: DeleteUser :execrows
DELETE FROM users WHERE id = $1;
`
	if err := os.WriteFile(filepath.Join(queriesDir, "user.sql"), []byte(userSQL), 0o644); err != nil {
		t.Fatal(err)
	}

	mapper := NewSQLCMapper()
	result, err := mapper.Map(dir, "sqlc.yaml")
	if err != nil {
		t.Fatalf("Map() returned unexpected error: %v", err)
	}

	expected := map[string]struct {
		queryType string
		line      int
	}{
		"GetUserByID":    {"one", 3},
		"GetUserByEmail": {"one", 6},
		"ListUsers":      {"many", 9},
		"CreateUser":     {"one", 12},
		"UpdateUser":     {"exec", 15},
		"DeleteUser":     {"execrows", 18},
	}

	if len(result) != len(expected) {
		t.Fatalf("expected %d mappings, got %d", len(expected), len(result))
	}

	for name, want := range expected {
		got, ok := result[name]
		if !ok {
			t.Errorf("missing mapping for %s", name)
			continue
		}

		if got.GoMethod != name {
			t.Errorf("GoMethod: got %q, want %q", got.GoMethod, name)
		}
		if got.QueryName != name {
			t.Errorf("QueryName: got %q, want %q", got.QueryName, name)
		}
		if got.QueryType != want.queryType {
			t.Errorf("%s QueryType: got %q, want %q", name, got.QueryType, want.queryType)
		}
		if got.SQLLine != want.line {
			t.Errorf("%s SQLLine: got %d, want %d", name, got.SQLLine, want.line)
		}
		if got.SQLFile != "queries/user.sql" {
			t.Errorf("%s SQLFile: got %q, want %q", name, got.SQLFile, "queries/user.sql")
		}
	}
}

func TestSQLCMapper_Map_MultipleQueryFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	sqlcYAML := `version: "2"
sql:
  - engine: "postgresql"
    queries: "./queries"
    schema: "./schema.sql"
    gen:
      go:
        package: "db"
        out: "./generated"
`
	if err := os.WriteFile(filepath.Join(dir, "sqlc.yaml"), []byte(sqlcYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	queriesDir := filepath.Join(dir, "queries")
	if err := os.MkdirAll(queriesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	userSQL := `-- name: GetUser :one
SELECT * FROM users WHERE id = $1;
`
	recipeSQL := `-- name: GetRecipe :one
SELECT * FROM recipes WHERE id = $1;

-- name: ListRecipes :many
SELECT * FROM recipes;
`
	if err := os.WriteFile(filepath.Join(queriesDir, "user.sql"), []byte(userSQL), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queriesDir, "recipe.sql"), []byte(recipeSQL), 0o644); err != nil {
		t.Fatal(err)
	}

	mapper := NewSQLCMapper()
	result, err := mapper.Map(dir, "sqlc.yaml")
	if err != nil {
		t.Fatalf("Map() returned unexpected error: %v", err)
	}

	if len(result) != 3 {
		t.Fatalf("expected 3 mappings, got %d", len(result))
	}

	if _, ok := result["GetUser"]; !ok {
		t.Error("missing mapping for GetUser")
	}
	if _, ok := result["GetRecipe"]; !ok {
		t.Error("missing mapping for GetRecipe")
	}
	if _, ok := result["ListRecipes"]; !ok {
		t.Error("missing mapping for ListRecipes")
	}

	// Verify files are correctly attributed.
	if result["GetUser"].SQLFile != "queries/user.sql" {
		t.Errorf("GetUser.SQLFile: got %q, want queries/user.sql", result["GetUser"].SQLFile)
	}
	if result["GetRecipe"].SQLFile != "queries/recipe.sql" {
		t.Errorf("GetRecipe.SQLFile: got %q, want queries/recipe.sql", result["GetRecipe"].SQLFile)
	}
}

func TestSQLCMapper_Map_MultipleSQLEntries(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// SQLC config with two sql entries (different query dirs).
	sqlcYAML := `version: "2"
sql:
  - engine: "postgresql"
    queries: "./queries/core"
    schema: "./schema.sql"
    gen:
      go:
        package: "core"
        out: "./generated/core"
  - engine: "postgresql"
    queries: "./queries/analytics"
    schema: "./schema.sql"
    gen:
      go:
        package: "analytics"
        out: "./generated/analytics"
`
	if err := os.WriteFile(filepath.Join(dir, "sqlc.yaml"), []byte(sqlcYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	coreDir := filepath.Join(dir, "queries", "core")
	analyticsDir := filepath.Join(dir, "queries", "analytics")
	if err := os.MkdirAll(coreDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(analyticsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(coreDir, "user.sql"), []byte("-- name: GetUser :one\nSELECT 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(analyticsDir, "report.sql"), []byte("-- name: GetReport :one\nSELECT 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mapper := NewSQLCMapper()
	result, err := mapper.Map(dir, "sqlc.yaml")
	if err != nil {
		t.Fatalf("Map() returned unexpected error: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 mappings, got %d", len(result))
	}
	if _, ok := result["GetUser"]; !ok {
		t.Error("missing mapping for GetUser")
	}
	if _, ok := result["GetReport"]; !ok {
		t.Error("missing mapping for GetReport")
	}
}

func TestSQLCMapper_Map_SingleFileQueries(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// When the queries field points to a single .sql file rather than a directory.
	sqlcYAML := `version: "2"
sql:
  - engine: "postgresql"
    queries: "./queries.sql"
    schema: "./schema.sql"
    gen:
      go:
        package: "db"
        out: "./generated"
`
	if err := os.WriteFile(filepath.Join(dir, "sqlc.yaml"), []byte(sqlcYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	singleSQL := `-- name: GetItem :one
SELECT * FROM items WHERE id = $1;

-- name: CreateItem :exec
INSERT INTO items (name) VALUES ($1);
`
	if err := os.WriteFile(filepath.Join(dir, "queries.sql"), []byte(singleSQL), 0o644); err != nil {
		t.Fatal(err)
	}

	mapper := NewSQLCMapper()
	result, err := mapper.Map(dir, "sqlc.yaml")
	if err != nil {
		t.Fatalf("Map() returned unexpected error: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 mappings, got %d", len(result))
	}
}

func TestSQLCMapper_Map_MissingConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mapper := NewSQLCMapper()
	_, err := mapper.Map(dir, "nonexistent.yaml")
	if err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestSQLCMapper_Map_InvalidYAML(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// YAML parser accepts many surprising inputs; use a genuine structural error.
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("version: [\ninvalid:\n  - {\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mapper := NewSQLCMapper()
	_, err := mapper.Map(dir, "bad.yaml")
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestSQLCMapper_Map_EmptySQLFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	sqlcYAML := `version: "2"
sql:
  - engine: "postgresql"
    queries: "./queries"
    schema: "./schema.sql"
    gen:
      go:
        package: "db"
        out: "./generated"
`
	if err := os.WriteFile(filepath.Join(dir, "sqlc.yaml"), []byte(sqlcYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	queriesDir := filepath.Join(dir, "queries")
	if err := os.MkdirAll(queriesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// SQL file with no annotations.
	if err := os.WriteFile(filepath.Join(queriesDir, "empty.sql"), []byte("SELECT 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mapper := NewSQLCMapper()
	result, err := mapper.Map(dir, "sqlc.yaml")
	if err != nil {
		t.Fatalf("Map() returned unexpected error: %v", err)
	}

	if len(result) != 0 {
		t.Errorf("expected 0 mappings for empty SQL file, got %d", len(result))
	}
}

func TestSQLCMapper_Map_AllQueryTypes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	sqlcYAML := `version: "2"
sql:
  - engine: "postgresql"
    queries: "./queries"
    schema: "./schema.sql"
    gen:
      go:
        package: "db"
        out: "./generated"
`
	if err := os.WriteFile(filepath.Join(dir, "sqlc.yaml"), []byte(sqlcYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	queriesDir := filepath.Join(dir, "queries")
	if err := os.MkdirAll(queriesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	allTypesSQL := `-- name: FetchOne :one
SELECT 1;

-- name: FetchMany :many
SELECT 1;

-- name: DoExec :exec
SELECT 1;

-- name: DoExecRows :execrows
SELECT 1;

-- name: DoExecResult :execresult
SELECT 1;

-- name: DoBatchOne :batchone
SELECT 1;

-- name: DoBatchMany :batchmany
SELECT 1;

-- name: DoBatchExec :batchexec
SELECT 1;

-- name: DoCopyFrom :copyfrom
SELECT 1;
`
	if err := os.WriteFile(filepath.Join(queriesDir, "types.sql"), []byte(allTypesSQL), 0o644); err != nil {
		t.Fatal(err)
	}

	mapper := NewSQLCMapper()
	result, err := mapper.Map(dir, "sqlc.yaml")
	if err != nil {
		t.Fatalf("Map() returned unexpected error: %v", err)
	}

	expectedTypes := map[string]string{
		"FetchOne":     "one",
		"FetchMany":    "many",
		"DoExec":       "exec",
		"DoExecRows":   "execrows",
		"DoExecResult": "execresult",
		"DoBatchOne":   "batchone",
		"DoBatchMany":  "batchmany",
		"DoBatchExec":  "batchexec",
		"DoCopyFrom":   "copyfrom",
	}

	if len(result) != len(expectedTypes) {
		t.Fatalf("expected %d mappings, got %d", len(expectedTypes), len(result))
	}

	for name, wantType := range expectedTypes {
		got, ok := result[name]
		if !ok {
			t.Errorf("missing mapping for %s", name)
			continue
		}
		if got.QueryType != wantType {
			t.Errorf("%s QueryType: got %q, want %q", name, got.QueryType, wantType)
		}
	}
}
