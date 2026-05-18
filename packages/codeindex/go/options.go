package goscan

import (
	"github.com/sosalejandro/atlas/packages/shared"
)

// Options configures Scan.
//
// Zero values are safe — Scan will walk every .go file under rootDir using
// the built-in layer rules and no DI / SQLC pre-resolution. Callers that
// need richer resolution wire the optional hooks below; Phase 1 ships the
// hooks but leaves the per-hook implementations (routeparse/, sqlcmap/,
// resolver/) for later phases.
type Options struct {
	// BackendRoot is the path relative to rootDir under which the scanner
	// walks. Defaults to "." (the whole rootDir).
	BackendRoot string

	// IgnorePackages lists directory names or rootDir-relative paths to
	// skip entirely. e.g. ["docs", "examples/legacy"].
	IgnorePackages []string

	// IgnoreFunctions lists glob patterns (with single `*` wildcard) that
	// match SymbolIDs to skip when adding call edges. e.g. ["fmt.*",
	// "*.String"].
	IgnoreFunctions []string

	// LayerRules extends the built-in directory→SymbolKind mapping.
	// Patterns are case-insensitive substring matches against the
	// rootDir-relative package directory path.
	LayerRules LayerRules

	// EntryPoints, when non-empty, switches the scanner into BuildFrom
	// mode: only the subgraph reachable from these entry points is
	// retained; the rest is pruned. Entry points may be exact SymbolIDs
	// or partial (suffix) matches per the legacy fuzzy-find behaviour.
	EntryPoints []shared.SymbolID

	// SQLCMethods is a pre-resolved map of "GoMethodName" → SQLC mapping.
	// Optional. When supplied, the scanner adds query: nodes + edges for
	// any call whose method name appears as a key.
	//
	// In Phase 1 the map is supplied by the caller; the actual parser
	// lives in packages/sqlcmap (Phase 2+).
	SQLCMethods map[string]SQLCMapping

	// InterfaceBindings is a pre-resolved DI map (short interface name →
	// concrete implementation). Used by the call resolver to follow
	// interface-typed method calls into the concrete impl. Optional.
	//
	// Supplied by packages/resolver in later phases.
	InterfaceBindings map[string]InterfaceBinding

	// Routes is the pre-discovered list of HTTP routes — each adds an
	// endpoint Node and an endpoint→handler Edge. Optional.
	//
	// Supplied by packages/routeparse in later phases.
	Routes []Route

	// Logger receives scan-time warnings (parse skips, unresolved
	// handlers). Defaults to shared.NopLogger.
	Logger shared.Logger

	// SkipTests, when true, excludes `_test.go` files from the scan. The
	// default (zero value: false) is to INCLUDE test files because Atlas's
	// primary use case — annotation-driven feature attribution — relies
	// on test files being indexed: the canonical place for
	// `// @atlas:feature` / `// @testreg` is on the test that verifies the
	// feature, not on the production handler it exercises.
	//
	// Set SkipTests=true only for pure production graph-only audits where
	// test funcs would just be noise. Named with inverse polarity
	// deliberately so the Go zero value (false) matches Atlas's intended
	// default of "include tests" without requiring a constructor.
	SkipTests bool
}

// LayerRules extends the built-in directory→SymbolKind classification.
// Each slice is a list of case-insensitive substring patterns matched
// against the rootDir-relative package directory. First match wins; if
// none match, the built-in heuristics in classifyNodeKind apply.
type LayerRules struct {
	Handler    []string
	Service    []string
	Repository []string
	Query      []string
}

// SQLCMapping is the shape Phase-1 callers pass in for SQLC pre-resolution.
// Mirrors the future packages/sqlcmap output verbatim so swapping the
// in-package hook for the real parser later is a no-op for Scan callers.
type SQLCMapping struct {
	GoMethod  string // e.g. "GetUserByEmail"
	SQLFile   string // e.g. "src/domain/repositories/queries/users.sql"
	SQLLine   int
	QueryName string // e.g. "GetUserByEmail" (from -- name: annotation)
	QueryType string // "one", "many", "exec", "execrows"
}

// InterfaceBinding mirrors the shape of packages/resolver's future output.
// Pre-Phase-1 callers will leave this empty; the scanner falls back to
// fuzzy match on interface method calls.
type InterfaceBinding struct {
	Interface    string // e.g. "repositories.UserRepository"
	Concrete     string // e.g. "persistence.PostgresUserRepository"
	ProviderFunc string // e.g. "NewPostgresUserRepository"
	File         string // where the provider is defined
}

// Route is one (method, path, handlerRef) tuple from a future
// packages/routeparse run.
type Route struct {
	Method  string // GET, POST, PUT, DELETE, PATCH, ...
	Path    string // /api/v1/auth/login
	Handler string // handler reference (e.g. "h.authHandler.Login")
	File    string // absolute or rootDir-relative; scanner normalises to rel
	Line    int
}
