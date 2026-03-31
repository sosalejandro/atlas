// @testreg trace.fx-resolver
package adapters

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFxResolver_ResolveFromSource_FxProvide(t *testing.T) {
	t.Parallel()

	src := `package di

import "go.uber.org/fx"

var Module = fx.Options(
	fx.Provide(
		NewCassandraConfig,
		NewKafkaConfig,
		NewJsonSerializer,
	),
)

func NewCassandraConfig() config.CassandraConfiguration {
	return config.NewDefaultCassandraConfig()
}

func NewKafkaConfig() config.KafkaConfiguration {
	return config.NewDefaultKafkaConfig()
}

func NewJsonSerializer() *serializer.JsonSerializer {
	return serializer.NewJsonSerializer()
}
`
	resolver := NewFxResolver()
	result, err := resolver.ResolveFromSource(src, "module.go")
	if err != nil {
		t.Fatalf("ResolveFromSource failed: %v", err)
	}

	// NewCassandraConfig returns config.CassandraConfiguration (interface) → mapping
	// NewKafkaConfig returns config.KafkaConfiguration (interface) → mapping
	// NewJsonSerializer returns *serializer.JsonSerializer (pointer) → skipped
	if len(result) != 2 {
		t.Fatalf("expected 2 mappings, got %d: %v", len(result), mapKeys(result))
	}

	cassMapping, ok := result["CassandraConfiguration"]
	if !ok {
		t.Fatalf("expected CassandraConfiguration mapping, got keys: %v", mapKeys(result))
	}
	if cassMapping.Interface != "config.CassandraConfiguration" {
		t.Errorf("expected Interface=config.CassandraConfiguration, got %q", cassMapping.Interface)
	}
	if cassMapping.Concrete != "config.DefaultCassandraConfig" {
		t.Errorf("expected Concrete=config.DefaultCassandraConfig, got %q", cassMapping.Concrete)
	}

	kafkaMapping, ok := result["KafkaConfiguration"]
	if !ok {
		t.Fatalf("expected KafkaConfiguration mapping, got keys: %v", mapKeys(result))
	}
	if kafkaMapping.Interface != "config.KafkaConfiguration" {
		t.Errorf("expected Interface=config.KafkaConfiguration, got %q", kafkaMapping.Interface)
	}
}

func TestFxResolver_ResolveFromSource_FxOptionsNesting(t *testing.T) {
	t.Parallel()

	src := `package di

import "go.uber.org/fx"

var Module = fx.Options(
	fx.Provide(NewProductConsumersDI),
	fx.Invoke(RegisterProductsHandlers),
	fx.Invoke(StartProductConsumer),
)

func NewProductConsumersDI(logger *zap.Logger) consumers.ProductConsumer {
	return consumers.NewDefaultProductConsumer(logger)
}
`
	resolver := NewFxResolver()
	result, err := resolver.ResolveFromSource(src, "module.go")
	if err != nil {
		t.Fatalf("ResolveFromSource failed: %v", err)
	}

	// NewProductConsumersDI returns consumers.ProductConsumer (interface) → mapping
	if len(result) != 1 {
		t.Fatalf("expected 1 mapping, got %d: %v", len(result), mapKeys(result))
	}

	mapping, ok := result["ProductConsumer"]
	if !ok {
		t.Fatalf("expected ProductConsumer mapping, got keys: %v", mapKeys(result))
	}
	if mapping.Interface != "consumers.ProductConsumer" {
		t.Errorf("expected Interface=consumers.ProductConsumer, got %q", mapping.Interface)
	}
	if mapping.Concrete != "consumers.DefaultProductConsumer" {
		t.Errorf("expected Concrete=consumers.DefaultProductConsumer, got %q", mapping.Concrete)
	}
}

func TestFxResolver_ResolveFromSource_FxInvoke(t *testing.T) {
	t.Parallel()

	src := `package di

import "go.uber.org/fx"

var Module = fx.Options(
	fx.Invoke(RegisterProductsHandlers),
	fx.Invoke(StartProductConsumer),
)
`
	resolver := NewFxResolver()
	result, err := resolver.ResolveFromSource(src, "module.go")
	if err != nil {
		t.Fatalf("ResolveFromSource failed: %v", err)
	}

	// fx.Invoke functions are initialization, not providers — no mappings.
	if len(result) != 0 {
		t.Fatalf("expected 0 mappings for invoke-only, got %d", len(result))
	}
}

func TestFxResolver_ResolveFromSource_ProviderReturnsPointer(t *testing.T) {
	t.Parallel()

	src := `package di

import "go.uber.org/fx"

var Module = fx.Options(
	fx.Provide(
		NewProductRepositoryFactory,
		NewProducer,
	),
)

func NewProductRepositoryFactory(
	config *cassandra.CassandraConfiguration,
	logger *zap.Logger,
) *repositories.ProductRepositoryFactory {
	return repositories.NewProductRepositoryFactory(config, logger)
}

func NewProducer(cfg config.KafkaConfiguration) *kafka.Producer {
	return kafka.NewProducer(cfg)
}
`
	resolver := NewFxResolver()
	result, err := resolver.ResolveFromSource(src, "module.go")
	if err != nil {
		t.Fatalf("ResolveFromSource failed: %v", err)
	}

	// Both return pointer types → no interface mappings.
	if len(result) != 0 {
		t.Fatalf("expected 0 mappings for pointer returns, got %d: %v", len(result), mapKeys(result))
	}
}

func TestFxResolver_ResolveFromSource_DigProvide(t *testing.T) {
	t.Parallel()

	src := `package di

import "go.uber.org/dig"

func BuildContainer() *dig.Container {
	container := dig.New()
	container.Provide(NewUserService)
	container.Provide(NewOrderService)
	return container
}

func NewUserService(repo repositories.UserRepository) services.UserService {
	return services.NewDefaultUserService(repo)
}

func NewOrderService(repo repositories.OrderRepository) *services.OrderServiceImpl {
	return services.NewOrderServiceImpl(repo)
}
`
	resolver := NewFxResolver()
	result, err := resolver.ResolveFromSource(src, "container.go")
	if err != nil {
		t.Fatalf("ResolveFromSource failed: %v", err)
	}

	// NewUserService returns services.UserService (interface) → mapping
	// NewOrderService returns *services.OrderServiceImpl (pointer) → skipped
	if len(result) != 1 {
		t.Fatalf("expected 1 mapping, got %d: %v", len(result), mapKeys(result))
	}

	mapping, ok := result["UserService"]
	if !ok {
		t.Fatalf("expected UserService mapping, got keys: %v", mapKeys(result))
	}
	if mapping.Interface != "services.UserService" {
		t.Errorf("expected Interface=services.UserService, got %q", mapping.Interface)
	}
	if mapping.Concrete != "services.DefaultUserService" {
		t.Errorf("expected Concrete=services.DefaultUserService, got %q", mapping.Concrete)
	}
}

func TestFxResolver_ResolveFromSource_EmptyNoFxCalls(t *testing.T) {
	t.Parallel()

	src := `package di

import "fmt"

func main() {
	fmt.Println("no fx here")
}
`
	resolver := NewFxResolver()
	result, err := resolver.ResolveFromSource(src, "main.go")
	if err != nil {
		t.Fatalf("ResolveFromSource failed: %v", err)
	}

	if len(result) != 0 {
		t.Fatalf("expected 0 mappings, got %d", len(result))
	}
}

func TestFxResolver_ResolveFromSource_InvalidSource(t *testing.T) {
	t.Parallel()

	resolver := NewFxResolver()
	_, err := resolver.ResolveFromSource("not valid go source{{{", "bad.go")
	if err == nil {
		t.Fatal("expected error for invalid Go source")
	}
}

func TestFxResolver_Resolve_MultiFileDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// File 1: Module definition with fx.Provide and fx.Invoke.
	moduleFile := `package di

import "go.uber.org/fx"

var Module = fx.Options(
	fx.Provide(
		NewEventManager,
		NewHandlerRegistry,
		NewRetryStrategy,
	),
	fx.Invoke(StartConsumer),
)
`
	if err := os.WriteFile(filepath.Join(dir, "module.go"), []byte(moduleFile), 0o644); err != nil {
		t.Fatal(err)
	}

	// File 2: Provider implementations.
	providersFile := `package di

import (
	"github.com/example/app/events"
	"github.com/example/app/handlers"
	"github.com/example/app/retry"
)

func NewEventManager(cfg *Config) events.EventManager {
	return events.NewKafkaEventManager(cfg)
}

func NewHandlerRegistry() handlers.HandlerRegistry {
	return handlers.NewDefaultHandlerRegistry()
}

// Returns a pointer — should be skipped.
func NewRetryStrategy() *retry.ExponentialRetry {
	return retry.NewExponentialRetry()
}

func StartConsumer(em events.EventManager) {
	em.Start()
}
`
	if err := os.WriteFile(filepath.Join(dir, "providers.go"), []byte(providersFile), 0o644); err != nil {
		t.Fatal(err)
	}

	resolver := NewFxResolver()
	result, err := resolver.Resolve(dir)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// NewEventManager returns events.EventManager (interface) → mapping
	// NewHandlerRegistry returns handlers.HandlerRegistry (interface) → mapping
	// NewRetryStrategy returns *retry.ExponentialRetry (pointer) → skipped
	if len(result) != 2 {
		t.Fatalf("expected 2 mappings, got %d: %v", len(result), mapKeys(result))
	}

	eventMapping, ok := result["EventManager"]
	if !ok {
		t.Fatalf("expected EventManager mapping, got keys: %v", mapKeys(result))
	}
	if eventMapping.Interface != "events.EventManager" {
		t.Errorf("expected Interface=events.EventManager, got %q", eventMapping.Interface)
	}
	if eventMapping.Concrete != "events.KafkaEventManager" {
		t.Errorf("expected Concrete=events.KafkaEventManager, got %q", eventMapping.Concrete)
	}
	if eventMapping.ProviderFunc != "events.NewKafkaEventManager" {
		t.Errorf("expected ProviderFunc=events.NewKafkaEventManager, got %q", eventMapping.ProviderFunc)
	}

	handlerMapping, ok := result["HandlerRegistry"]
	if !ok {
		t.Fatalf("expected HandlerRegistry mapping, got keys: %v", mapKeys(result))
	}
	if handlerMapping.Interface != "handlers.HandlerRegistry" {
		t.Errorf("expected Interface=handlers.HandlerRegistry, got %q", handlerMapping.Interface)
	}
}

func TestFxResolver_Resolve_EmptyDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	resolver := NewFxResolver()
	result, err := resolver.Resolve(dir)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if len(result) != 0 {
		t.Fatalf("expected 0 mappings, got %d", len(result))
	}
}

func TestFxResolver_Resolve_FxNewComposition(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Simulate fx.New() with module composition — the providers are in
	// the referenced modules, not inline. This test verifies that
	// fx.Provide inside fx.New is still detected.
	mainFile := `package main

import "go.uber.org/fx"

func main() {
	app := fx.New(
		fx.Provide(NewLogger),
		fx.Invoke(func(lc fx.Lifecycle) {}),
	)
	app.Run()
}

func NewLogger() logging.Logger {
	return logging.NewZapLogger()
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(mainFile), 0o644); err != nil {
		t.Fatal(err)
	}

	resolver := NewFxResolver()
	result, err := resolver.Resolve(dir)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if len(result) != 1 {
		t.Fatalf("expected 1 mapping, got %d: %v", len(result), mapKeys(result))
	}

	mapping, ok := result["Logger"]
	if !ok {
		t.Fatalf("expected Logger mapping, got keys: %v", mapKeys(result))
	}
	if mapping.Interface != "logging.Logger" {
		t.Errorf("expected Interface=logging.Logger, got %q", mapping.Interface)
	}
	if mapping.Concrete != "logging.ZapLogger" {
		t.Errorf("expected Concrete=logging.ZapLogger, got %q", mapping.Concrete)
	}
}
