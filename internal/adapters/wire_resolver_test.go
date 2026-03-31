// @testreg trace.wire-resolver
package adapters

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWireResolver_ResolveFromSource_ExplicitBind(t *testing.T) {
	t.Parallel()

	src := `package config

import "github.com/google/wire"

var AppSet = wire.NewSet(
	NewFooService,
	wire.Bind(new(FooRepository), new(*PostgresFooRepository)),
	wire.Bind(new(BarService), new(*BarServiceImpl)),
)
`
	resolver := NewWireResolver()
	result, err := resolver.ResolveFromSource(src, "wire.go")
	if err != nil {
		t.Fatalf("ResolveFromSource failed: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 mappings, got %d", len(result))
	}

	// Check FooRepository binding.
	fooMapping, ok := result["FooRepository"]
	if !ok {
		t.Fatal("expected FooRepository mapping")
	}
	if fooMapping.Interface != "FooRepository" {
		t.Errorf("expected Interface=FooRepository, got %q", fooMapping.Interface)
	}
	if fooMapping.Concrete != "*PostgresFooRepository" {
		t.Errorf("expected Concrete=*PostgresFooRepository, got %q", fooMapping.Concrete)
	}

	// Check BarService binding — keyed by interface short name.
	barMapping, ok := result["BarService"]
	if !ok {
		t.Fatalf("expected BarService mapping, got keys: %v", mapKeys(result))
	}
	if barMapping.Interface != "BarService" {
		t.Errorf("expected Interface=BarService, got %q", barMapping.Interface)
	}
	if barMapping.Concrete != "*BarServiceImpl" {
		t.Errorf("expected Concrete=*BarServiceImpl, got %q", barMapping.Concrete)
	}
}

func TestWireResolver_ResolveFromSource_QualifiedBind(t *testing.T) {
	t.Parallel()

	src := `package config

import "github.com/google/wire"

var WireSet = wire.NewSet(
	NewPostgresUserRepo,
	wire.Bind(new(repositories.UserRepository), new(*persistence.PostgresUserRepository)),
)
`
	resolver := NewWireResolver()
	result, err := resolver.ResolveFromSource(src, "wire.go")
	if err != nil {
		t.Fatalf("ResolveFromSource failed: %v", err)
	}

	if len(result) != 1 {
		t.Fatalf("expected 1 mapping, got %d", len(result))
	}

	mapping, ok := result["UserRepository"]
	if !ok {
		t.Fatal("expected UserRepository mapping")
	}
	if mapping.Interface != "repositories.UserRepository" {
		t.Errorf("expected Interface=repositories.UserRepository, got %q", mapping.Interface)
	}
	if mapping.Concrete != "*persistence.PostgresUserRepository" {
		t.Errorf("expected Concrete=*persistence.PostgresUserRepository, got %q", mapping.Concrete)
	}
}

func TestWireResolver_ResolveFromSource_WireBuild(t *testing.T) {
	t.Parallel()

	src := `package config

import "github.com/google/wire"

func InitializeApp() *App {
	wire.Build(
		NewApp,
		NewDB,
		wire.Bind(new(Cache), new(*RedisCache)),
	)
	return nil
}
`
	resolver := NewWireResolver()
	result, err := resolver.ResolveFromSource(src, "wire.go")
	if err != nil {
		t.Fatalf("ResolveFromSource failed: %v", err)
	}

	if len(result) != 1 {
		t.Fatalf("expected 1 mapping, got %d", len(result))
	}

	mapping, ok := result["Cache"]
	if !ok {
		t.Fatalf("expected Cache mapping, got keys: %v", mapKeys(result))
	}
	if mapping.Interface != "Cache" {
		t.Errorf("expected Interface=Cache, got %q", mapping.Interface)
	}
	if mapping.Concrete != "*RedisCache" {
		t.Errorf("expected Concrete=*RedisCache, got %q", mapping.Concrete)
	}
}

func TestWireResolver_ResolveFromSource_EmptySet(t *testing.T) {
	t.Parallel()

	src := `package config

import "github.com/google/wire"

var EmptySet = wire.NewSet()
`
	resolver := NewWireResolver()
	result, err := resolver.ResolveFromSource(src, "wire.go")
	if err != nil {
		t.Fatalf("ResolveFromSource failed: %v", err)
	}

	if len(result) != 0 {
		t.Fatalf("expected 0 mappings, got %d", len(result))
	}
}

func TestWireResolver_ResolveFromSource_NoBind(t *testing.T) {
	t.Parallel()

	src := `package config

import "github.com/google/wire"

var AppSet = wire.NewSet(
	NewFooService,
	NewBarService,
	NewBazRepo,
)
`
	resolver := NewWireResolver()
	result, err := resolver.ResolveFromSource(src, "wire.go")
	if err != nil {
		t.Fatalf("ResolveFromSource failed: %v", err)
	}

	// No explicit wire.Bind calls, so ResolveFromSource returns nothing.
	if len(result) != 0 {
		t.Fatalf("expected 0 mappings from source-only parse, got %d", len(result))
	}
}

func TestWireResolver_ResolveFromSource_InvalidSource(t *testing.T) {
	t.Parallel()

	resolver := NewWireResolver()
	_, err := resolver.ResolveFromSource("not valid go source{{{", "bad.go")
	if err == nil {
		t.Fatal("expected error for invalid Go source")
	}
}

func TestWireResolver_ResolveFromSource_MultipleSets(t *testing.T) {
	t.Parallel()

	src := `package config

import "github.com/google/wire"

var InfraSet = wire.NewSet(
	NewDB,
	wire.Bind(new(ports.Database), new(*pgxpool.Pool)),
)

var ServiceSet = wire.NewSet(
	NewAuthService,
	wire.Bind(new(services.AuthService), new(*auth.AuthServiceImpl)),
)
`
	resolver := NewWireResolver()
	result, err := resolver.ResolveFromSource(src, "wire.go")
	if err != nil {
		t.Fatalf("ResolveFromSource failed: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 mappings, got %d", len(result))
	}

	if _, ok := result["Database"]; !ok {
		t.Error("expected Database mapping from InfraSet")
	}
	if _, ok := result["AuthService"]; !ok {
		t.Errorf("expected AuthService mapping from ServiceSet, got keys: %v", mapKeys(result))
	}
}

func TestWireResolver_Resolve_ReturnTypeInference(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Create a wire.go file with provider references (no explicit Bind).
	wireFile := `package config

import "github.com/google/wire"

var WireSet = wire.NewSet(
	ProvideRecipeRepository,
	ProvideAuthService,
	ProvideHTTPServer,
)
`
	if err := os.WriteFile(filepath.Join(dir, "wire.go"), []byte(wireFile), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a sibling file with provider function definitions that return
	// interface types — the pattern used in the nutrition project.
	providers := `package config

import (
	"github.com/example/app/domain/repositories"
	"github.com/example/app/infrastructure/persistence"
	"github.com/example/app/application/services"
	"github.com/example/app/infrastructure/auth"
	"github.com/example/app/infrastructure/http"
)

// ProvideRecipeRepository returns an interface type.
func ProvideRecipeRepository(db *DB) repositories.RecipeRepository {
	return persistence.NewPostgresRecipeRepository(db)
}

// ProvideAuthService returns an interface type.
func ProvideAuthService(repo repositories.AuthRepository) services.AuthService {
	return auth.NewAuthServiceImpl(repo)
}

// ProvideHTTPServer returns a concrete pointer — should NOT appear as mapping.
func ProvideHTTPServer(logger Logger) *http.HTTPServer {
	return http.NewHTTPServer(logger)
}
`
	if err := os.WriteFile(filepath.Join(dir, "wire_providers.go"), []byte(providers), 0o644); err != nil {
		t.Fatal(err)
	}

	resolver := NewWireResolver()
	result, err := resolver.Resolve(filepath.Join(dir, "wire.go"))
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// Should have 2 mappings: RecipeRepository and AuthService.
	// HTTPServer returns a pointer type so it should be excluded.
	if len(result) != 2 {
		t.Fatalf("expected 2 mappings, got %d: %+v", len(result), result)
	}

	recipeMapping, ok := result["RecipeRepository"]
	if !ok {
		t.Fatal("expected RecipeRepository mapping")
	}
	if recipeMapping.Interface != "repositories.RecipeRepository" {
		t.Errorf("expected Interface=repositories.RecipeRepository, got %q", recipeMapping.Interface)
	}
	if recipeMapping.Concrete != "persistence.PostgresRecipeRepository" {
		t.Errorf("expected Concrete=persistence.PostgresRecipeRepository, got %q", recipeMapping.Concrete)
	}
	if recipeMapping.ProviderFunc != "persistence.NewPostgresRecipeRepository" {
		t.Errorf("expected ProviderFunc=persistence.NewPostgresRecipeRepository, got %q", recipeMapping.ProviderFunc)
	}

	authMapping, ok := result["AuthService"]
	if !ok {
		t.Fatal("expected AuthService mapping")
	}
	if authMapping.Interface != "services.AuthService" {
		t.Errorf("expected Interface=services.AuthService, got %q", authMapping.Interface)
	}
	if authMapping.Concrete != "auth.AuthServiceImpl" {
		t.Errorf("expected Concrete=auth.AuthServiceImpl, got %q", authMapping.Concrete)
	}
}

func TestWireResolver_Resolve_MixedBindAndInference(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	wireFile := `package config

import "github.com/google/wire"

var WireSet = wire.NewSet(
	ProvideCache,
	ProvideUserRepo,
	wire.Bind(new(ports.EventBus), new(*kafka.EventBusImpl)),
)
`
	if err := os.WriteFile(filepath.Join(dir, "wire.go"), []byte(wireFile), 0o644); err != nil {
		t.Fatal(err)
	}

	providers := `package config

import (
	"github.com/example/app/domain/repositories"
	"github.com/example/app/infrastructure/persistence"
	"github.com/example/app/infrastructure/cache"
)

func ProvideCache(cfg Config) cache.CacheService {
	return cache.NewRedisCache(cfg)
}

func ProvideUserRepo(db DB) repositories.UserRepository {
	return persistence.NewPostgresUserRepository(db)
}
`
	if err := os.WriteFile(filepath.Join(dir, "wire_repos.go"), []byte(providers), 0o644); err != nil {
		t.Fatal(err)
	}

	resolver := NewWireResolver()
	result, err := resolver.Resolve(filepath.Join(dir, "wire.go"))
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// 3 mappings: EventBus from Bind, CacheService and UserRepository from inference.
	if len(result) != 3 {
		t.Fatalf("expected 3 mappings, got %d: %+v", len(result), result)
	}

	if _, ok := result["EventBus"]; !ok {
		t.Errorf("expected EventBus mapping from wire.Bind, got keys: %v", mapKeys(result))
	}
	if _, ok := result["CacheService"]; !ok {
		t.Error("expected CacheService mapping from return type inference")
	}
	if _, ok := result["UserRepository"]; !ok {
		t.Error("expected UserRepository mapping from return type inference")
	}
}

func TestWireResolver_Resolve_FileNotFound(t *testing.T) {
	t.Parallel()

	resolver := NewWireResolver()
	_, err := resolver.Resolve("/nonexistent/wire.go")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestWireResolver_Resolve_NoProviderFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	wireFile := `package config

import "github.com/google/wire"

var WireSet = wire.NewSet(
	ProvideSomething,
)
`
	if err := os.WriteFile(filepath.Join(dir, "wire.go"), []byte(wireFile), 0o644); err != nil {
		t.Fatal(err)
	}

	// No provider definition files — ProvideSomething is referenced but not
	// defined in any file in the directory.
	resolver := NewWireResolver()
	result, err := resolver.Resolve(filepath.Join(dir, "wire.go"))
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// No mappings since provider function cannot be found.
	if len(result) != 0 {
		t.Fatalf("expected 0 mappings, got %d", len(result))
	}
}

func TestWireResolver_Resolve_MultipleReturnValues(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	wireFile := `package config

import "github.com/google/wire"

var WireSet = wire.NewSet(
	ProvideDBWithCleanup,
)
`
	if err := os.WriteFile(filepath.Join(dir, "wire.go"), []byte(wireFile), 0o644); err != nil {
		t.Fatal(err)
	}

	// Provider that returns (interface, cleanup) — common Wire pattern.
	providers := `package config

import (
	"github.com/example/app/ports"
	"github.com/example/app/infra"
)

func ProvideDBWithCleanup() (ports.Database, func()) {
	db := infra.NewPostgresDB()
	return db, func() { db.Close() }
}
`
	if err := os.WriteFile(filepath.Join(dir, "wire_infra.go"), []byte(providers), 0o644); err != nil {
		t.Fatal(err)
	}

	resolver := NewWireResolver()
	result, err := resolver.Resolve(filepath.Join(dir, "wire.go"))
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// First return type is ports.Database — should be picked up.
	if len(result) != 1 {
		t.Fatalf("expected 1 mapping, got %d: %+v", len(result), result)
	}

	mapping, ok := result["Database"]
	if !ok {
		t.Fatal("expected Database mapping")
	}
	if mapping.Interface != "ports.Database" {
		t.Errorf("expected Interface=ports.Database, got %q", mapping.Interface)
	}
}

// --- Helper function tests ---

func TestShortTypeName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"repositories.UserRepository", "UserRepository"},
		{"*persistence.PostgresUserRepository", "PostgresUserRepository"},
		{"UserRepository", "UserRepository"},
		{"*FooImpl", "FooImpl"},
		{"services.AuthService", "AuthService"},
	}

	for _, tt := range tests {
		got := shortTypeName(tt.input)
		if got != tt.want {
			t.Errorf("shortTypeName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestConstructorToConcrete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"persistence.NewPostgresUserRepository", "persistence.PostgresUserRepository"},
		{"services.NewMealLogService", "services.MealLogService"},
		{"auth.NewJWTValidator", "auth.JWTValidator"},
		{"NewFoo", "Foo"},
		{"cache.NewRedisCache", "cache.RedisCache"},
	}

	for _, tt := range tests {
		got := constructorToConcrete(tt.input)
		if got != tt.want {
			t.Errorf("constructorToConcrete(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// mapKeys returns all keys from a map for diagnostic output.
func mapKeys(m map[string]InterfaceMapping) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
