# testreg

**Full-stack dependency tracing and test coverage registry for polyglot codebases.**

testreg maintains a YAML registry of business features mapped to their tests across Go, TypeScript, Playwright, Jest, Vitest, and Maestro. It parses Go and TypeScript ASTs to build full-stack dependency graphs -- from React route to SQL query -- and cross-references them with test coverage to surface exactly where gaps exist.

<!-- badges -->
<!-- [![Go Reference](https://pkg.go.dev/badge/github.com/sosalejandro/testreg.svg)](https://pkg.go.dev/github.com/sosalejandro/testreg) -->
<!-- [![CI](https://github.com/sosalejandro/testreg/actions/workflows/ci.yml/badge.svg)](https://github.com/sosalejandro/testreg/actions) -->

---

## Quick Example

Trace the entire dependency chain for a login feature in one command:

```
$ testreg trace auth.login

  Feature: Login (auth.login)
  Priority: critical
  API Surfaces:
    POST /api/v1/auth/login

route:/login                                              apps/web/src/router.tsx:142
└─ LoginPage                                              apps/web/src/pages/auth/LoginPage.tsx:13
   └─ useAuth                                             apps/web/src/hooks/useAuth.ts:19
      └─ authApi.login                                    apps/web/src/services/api/auth.ts:46
         └─ POST /api/v1/auth/login                       src/infrastructure/http/handlers/auth_handler.go:576
            └─ AuthHandler.Login                          src/infrastructure/http/handlers/auth_handler.go:249
               └─ authService.Login                       src/application/services/auth_service.go:172
                  ├─ JWTGenerator.GenerateTokenPair        src/infrastructure/auth/jwt_generator.go:70
                  │  ├─ JWTGenerator.GenerateAccessToken   src/infrastructure/auth/jwt_generator.go:97
                  │  └─ JWTGenerator.GenerateRefreshToken  src/infrastructure/auth/jwt_generator.go:123
                  ├─ authRepository.StoreRefreshToken      src/domain/repositories/auth_repository.go:329
                  ├─ repositories.HashToken                src/domain/repositories/auth_repository.go:90
                  └─ sql:GetUserByEmail                    src/domain/repositories/queries/user.sql:21

  Confidence: 100%  |  Nodes: 11  |  Depth: 7
```

*Output from [nutrition-project-v2](https://github.com/sosalejandro/nutrition-project-v2) — a full-stack Go + React monorepo with 184 features, 1103 test files, and 2,122 source files.*

No manual wiring. testreg parsed the Go AST, resolved Wire dependency injection bindings, mapped SQLC-generated methods to their SQL source files, and ran the TypeScript scanner against the frontend -- automatically.

---

## Features

- **Full-stack dependency tracing** -- React route to Go handler to SQL query, resolved from source code
- **Zero-config for Go backends** -- AST-based auto-discovery handles ~95% of call edges without annotations
- **Feature-centric coverage** -- maps business features (not just files) to their tests across all platforms
- **Unified health audits** -- per-feature health score combining dependency traces, test coverage, and gap severity
- **Failure triage** -- match error symptoms against dependency graphs to pinpoint which files to check first
- **Graph export** -- Graphviz DOT, Mermaid, and JSON output for visualization
- **Multi-framework support** -- Go, Vitest, Playwright, Jest, and Maestro test runners
- **Wire, Fx, and SQLC integration** -- resolves DI bindings and generated query mappings automatically
- **TypeScript AST scanning** -- parses React Router, TanStack Query hooks, and API service files
- **API contract extraction** -- `testreg contract` shows full input/output types from GraphQL schema to SQL query
- **Auto-scaffolding** -- `testreg init --discover` creates features from actual routes (Chi, Echo, stdlib)
- **Sprint planning** -- priority-weighted gap scoring to decide what to test next
- **AI-ready output** -- `testreg gaps --format prompt` generates structured output for AI coding agents

---

## Installation

```bash
go install github.com/sosalejandro/testreg@latest
```

Or build from source:

```bash
git clone https://github.com/sosalejandro/testreg.git
cd testreg
go build -o testreg .
```

For frontend graph support, install the TypeScript dependency:

```bash
cd testreg && npm install
```

**Requirements:** Go 1.25+, Node.js 22+ (for TypeScript scanner, optional)

---

## Quick Start

```bash
# 1. Initialize the registry with template YAML files
testreg init

# 2. Or auto-discover features from your router (Chi, Echo, stdlib)
testreg init --discover

# 3. Add @testreg annotations to your test files (see Annotations below)

# 4. Scan the project to map tests to features
testreg scan

# 5. View the coverage dashboard
testreg status

# 6. Trace a feature's full dependency chain
testreg trace auth.login

# 7. Run a health audit across all features
testreg audit
```

---

## Commands

testreg has 16 commands organized by workflow phase. Each command has a detailed reference doc in [`docs/commands/`](docs/commands/).

### Understanding Your Codebase

| Command | Description | Reference |
|---------|-------------|-----------|
| [`trace`](docs/commands/trace.md) | Trace a feature's full-stack dependency graph | [trace.md](docs/commands/trace.md) |
| [`graph`](docs/commands/graph.md) | Export dependency graph as DOT, Mermaid, or JSON | [graph.md](docs/commands/graph.md) |
| [`contract`](docs/commands/contract.md) | Show API contracts and function signatures per layer | [contract.md](docs/commands/contract.md) |

### Measuring Coverage

| Command | Description | Reference |
|---------|-------------|-----------|
| [`scan`](docs/commands/scan.md) | Discover test files and map them to features | [scan.md](docs/commands/scan.md) |
| [`status`](docs/commands/status.md) | Coverage dashboard by domain and platform | [status.md](docs/commands/status.md) |
| [`check`](docs/commands/check.md) | Detailed coverage for a single feature | [check.md](docs/commands/check.md) |
| [`report`](docs/commands/report.md) | Generate markdown or JSON coverage report | [report.md](docs/commands/report.md) |
| [`audit`](docs/commands/audit.md) | Unified health report with gap analysis | [audit.md](docs/commands/audit.md) |

### Planning and Fixing

| Command | Description | Reference |
|---------|-------------|-----------|
| [`sprint`](docs/commands/sprint.md) | Priority-ranked gap list for sprint planning | [sprint.md](docs/commands/sprint.md) |
| [`gaps`](docs/commands/gaps.md) | Actionable gap extraction for humans or AI agents | [gaps.md](docs/commands/gaps.md) |
| [`diagnose`](docs/commands/diagnose.md) | Match error symptoms to dependency graph layers | [diagnose.md](docs/commands/diagnose.md) |

### Running and Tracking

| Command | Description | Reference |
|---------|-------------|-----------|
| [`run`](docs/commands/run.md) | Execute tests for a feature or priority level | [run.md](docs/commands/run.md) |
| [`update`](docs/commands/update.md) | Ingest test results (Playwright, go test, Maestro) | [update.md](docs/commands/update.md) |
| [`diff`](docs/commands/diff.md) | Compare health snapshots across sprints | [diff.md](docs/commands/diff.md) |
| [`metrics`](docs/commands/metrics.md) | Quality signals: slow, flaky, race conditions | [metrics.md](docs/commands/metrics.md) |

### Setup

| Command | Description | Reference |
|---------|-------------|-----------|
| [`init`](docs/commands/init.md) | Bootstrap registry with templates or auto-discovery | [init.md](docs/commands/init.md) |

### Global Flags

```
--registry-dir <path>   Path to registry YAML files (default: docs/testing/registry)
--project-root <path>   Project root directory (auto-detected from git root)
--metrics               Show performance metrics after command execution
```

---

## Workflow

### First Time (Project Onboarding)

```bash
# 1. Scaffold features from your actual routes (no annotations needed)
testreg init --discover

# 2. Scan to map existing test files
testreg scan

# 3. See where you stand
testreg audit --summary

# 4. Check _unmapped.yaml for tests that need @testreg annotations
# 5. Add annotations, re-scan, establish baseline
testreg scan && testreg audit
```

### Before Coding (Understanding Phase)

```bash
# See the full call graph for the feature you're about to work on
testreg trace <feature-id>

# See which parts have test coverage and which don't
testreg audit <feature-id>

# Generate a visual diagram for a design doc or PR
testreg graph <feature-id> --format mermaid
```

### After Coding (Verification Phase)

```bash
# Update the registry with your new annotations
testreg scan

# Run all tests for the feature you changed
testreg run <feature-id>

# Check that your changes improved the health score
testreg audit <feature-id>
```

### When Tests Fail

```bash
# Diagnose -- match the error against known patterns
testreg diagnose <feature-id> --symptom "401 Unauthorized"
# Output: Layer: backend-auth → Check: auth_service.go, then user_repo.go

# Trace -- see the full dependency chain for context
testreg trace <feature-id>
```

### Sprint Planning

```bash
# 1. Save a baseline
testreg diff --save-snapshot sprint-3

# 2. See the priority-ranked gap list
testreg sprint --priority critical,high -n 20

# 3. Extract actionable gaps for AI subagents
testreg gaps --priority critical --format prompt > /tmp/gaps.md

# 4. After the sprint, measure improvement
testreg diff
```

---

## When testreg Works Best (and When It Doesn't)

testreg is not a generic tool. It makes specific assumptions about how your codebase is structured.

### What testreg assumes about your Go backend

| Assumption | What testreg expects | What happens otherwise |
|------------|---------------------|----------------------|
| **Package naming** | Directories named `handler*/`, `service*/`, `repository*/`, `persistence*/` | Functions are classified as `service` (default). Health score weights may be wrong, but the graph still builds. |
| **Struct field injection** | Dependencies stored as struct fields: `type Handler struct { service AuthService }` | Calls through constructor parameters, closures, or globals are **invisible** to the scanner. Edges are lost. |
| **Layered architecture** | Handler → Service → Repository → Query call flow | The health score weights (handler 30%, service 30%, repository 25%, query 15%) assume this layering. Flat architectures get skewed scores. |

### Dependency injection support

| DI Approach | Support Level | Details |
|-------------|--------------|---------|
| **Google Wire** | Full | Parses `wire.Bind()` and provider functions to resolve interface-to-concrete mappings |
| **Uber Fx / Dig** | Full | Parses `fx.Provide()`, `fx.Options()`, `fx.Invoke()`, and `dig.Provide()` to resolve provider return types |
| **Manual wiring (struct fields)** | Full | As long as dependencies are struct fields, the AST scanner resolves them |
| **Manual wiring (constructor params)** | Partial | Constructor call visible, but calls through params are not traced |
| **No DI (closures/globals)** | None | Captured variables and global singletons cannot be traced via AST |

### Router support

| Router | Support Level | Details |
|--------|--------------|---------|
| **Chi** | Auto-detected | `r.Get()`, `r.Post()`, `r.Route()`, `r.Group()`, `r.With()` |
| **Echo** | Auto-detected | `e.GET()`, `e.POST()`, `e.Group()`, `e.Any()` |
| **stdlib `net/http`** | Auto-detected | `mux.HandleFunc()`, `mux.Handle()`, Go 1.22+ pattern routing |
| **Gin, Fiber, gorilla/mux** | Via `@api` annotations | No auto-detection for these frameworks |

### Data access and frontend support

| Tool/Framework | Support Level |
|----------------|--------------|
| **SQLC** | Full -- maps generated Go methods to SQL source files |
| **React Router + TanStack Query** | Full auto-discovery via TypeScript AST |
| **Raw SQL / ORM** | Not supported for graph tracing |
| **Vue, Svelte, Angular** | Coverage tracking via annotations only (no call graph) |

### The `@api` escape hatch

For any pattern testreg can't auto-detect:

```go
// @api POST /api/v1/auth/login
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) { ... }
```

Works with **any** Go HTTP framework. Takes precedence over auto-detected routes.

---

## Concepts

### Feature ID

The primary identifier for a business feature: `<domain>.<feature>[.<sub-feature>]`

```
auth.login              -- User login flow
recipes.create          -- Create a new recipe
plans.nutritionist.list -- List plans (nutritionist view)
```

### Node Kind

Classifies a node's role in the architecture:

| Kind | Description | Color | Audit Weight |
|------|-------------|-------|-------------|
| `handler` | HTTP/gRPC handler | Cyan | 30% |
| `service` | Business logic | Green | 30% |
| `repository` | Data access | Yellow | 25% |
| `query` | SQL query (SQLC) | Magenta | 15% |
| `component` | React page/component | Cyan | -- |
| `hook` | React hook | Green | -- |
| `endpoint` | API boundary (URL) | White | -- |
| `external` | External service | Red | -- |

### Health Score

Weighted average of per-layer test coverage:

```
Health = (handler_coverage × 0.30) + (service_coverage × 0.30)
       + (repository_coverage × 0.25) + (query_coverage × 0.15)
```

| Range | Meaning |
|-------|---------|
| 80-100% | Well tested, safe to ship |
| 50-79% | Gaps exist, review before shipping |
| 0-49% | Significant gaps, prioritize testing |

### Gap Severity

| Severity | Criteria |
|----------|----------|
| CRITICAL | Handler or service with no unit test |
| HIGH | Repository with no integration test |
| MEDIUM | SQL query with no test |
| LOW | Other untested nodes |

### Test Status

| Symbol | Meaning |
|--------|---------|
| `✓` | A test file directly covers this function's file |
| `◐` | A test exists in a related directory |
| `✘` | No test file found |

---

## Annotations

### `@testreg` -- Map tests to features

```go
// @testreg auth.login
func TestLoginSuccess(t *testing.T) { ... }
```

```typescript
// @testreg auth.login
test('should login with valid credentials', async () => { ... });
```

```python
# @testreg auth.login #real
def test_login_success(client):
    ...
```

```yaml
# @testreg auth.login
appId: com.example.app
```

**Multiple features:** `@testreg auth.login,auth.session`

**Flags:** `@testreg auth.login #mocked #slow`

| Flag | Meaning |
|------|---------|
| `#real` | Real integration test (not mocked) |
| `#mocked` | Test uses mocks |
| `#wip` | Work in progress |
| `#flaky` | Known flaky test |

### `@api` -- Map handlers to HTTP endpoints

```go
// @api POST /api/v1/auth/login
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) { ... }
```

### `@calls` -- Declare hidden call edges

```go
// @calls notification_service.SendEmail
func (s *OrderService) PlaceOrder(...) { ... }
```

---

## Configuration

testreg reads its configuration from `.testreg.yaml` at the project root. All fields are optional — sensible defaults are applied when the file is absent or fields are omitted.

### Full example

```yaml
graph:
  backend_root: src
  router_file: src/infrastructure/http/router.go
  wire_file: src/infrastructure/config/wire.go
  fx_dir: src/pkg/application/common
  sqlc_config: sqlc.yaml
  frontend_roots:
    - apps/web/src
  ignore_packages: [vendor, testutil]
  ignore_functions: ["String", "Error", "MarshalJSON"]
  cache_dir: .testreg-cache
  max_depth: 10
  type_checking: false
  concurrency: 4
  graphql:
    schema_dirs:
      - src/training/pkg/schema
  layer_rules:
    handler: ["controladores", "controllers", "api"]
    repository: ["dao", "store", "repositorios"]
    service: ["usecase", "interactor"]
    query: ["queries"]
```

### Field reference

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `backend_root` | string | `"src"` | Root directory where Go source files live. testreg walks this directory recursively to discover all `.go` files for AST parsing. Set this to wherever your Go packages start (e.g., `"."` if packages are at the repo root, `"src"` for monorepos). |
| `router_file` | string | `""` | Path to the Go file (or directory) containing HTTP route registrations. Used for auto-discovery of routes with Chi, Echo, and stdlib routers. If omitted, only `@api` annotations are used to map endpoints. Can be a single file (`router.go`) or a directory (scans all `.go` files in it). |
| `wire_file` | string | `""` | Path to a Google Wire DI file. testreg parses `wire.Bind()` calls and provider functions to resolve interface-to-concrete type mappings. This allows the call graph to follow calls through interfaces (e.g., `h.service.Login()` where `service` is an interface bound to a concrete type via Wire). |
| `fx_dir` | string | `""` | Directory containing Uber Fx or Dig provider modules. testreg scans for `fx.Provide()`, `fx.Options()`, `fx.Invoke()`, and `dig.Provide()` calls to resolve DI bindings. Mutually exclusive with `wire_file` — use whichever DI framework your project uses. |
| `sqlc_config` | string | `""` | Path to `sqlc.yaml`. testreg parses this to map SQLC-generated Go methods to their original `.sql` source files and line numbers. Without this, SQL query nodes won't appear in the graph. |
| `frontend_roots` | []string | `[]` | Directories to scan for TypeScript/React source code. Each directory is scanned for React Router route definitions, component imports, hook usage, and API service calls. Requires `TESTREG_TS_SCANNER` environment variable and `npm install` in the testreg directory. |
| `ignore_packages` | []string | `[]` | Go package directory names to skip during AST scanning. Useful for excluding `vendor/`, `testutil/`, `mocks/`, or other directories that add noise to the call graph. |
| `ignore_functions` | []string | `[]` | Function name glob patterns to exclude from the graph. Common exclusions: `"String"`, `"Error"`, `"MarshalJSON"` — utility methods that clutter the trace without adding insight. |
| `cache_dir` | string | `".testreg-cache"` | Directory for caching parsed AST data, snapshots, and metrics history. |
| `max_depth` | int | `10` | Maximum call graph traversal depth. Prevents infinite recursion in deeply nested call chains. Can be overridden per-command with `--depth`. |
| `type_checking` | bool | `false` | **Experimental — not recommended.** Enables `go/types` for exact cross-package type resolution. See the warning in the Performance section. |
| `concurrency` | int | `4` | Maximum parallel goroutines for AST scanning. Increase on machines with many cores for faster graph building. |
| `graphql.schema_dirs` | []string | `[]` | Directories containing `.graphqls` schema files. Used by `testreg contract` to show GraphQL mutation/query definitions as the first layer in the contract chain. |
| `layer_rules.handler` | []string | `[]` | Additional directory name patterns classified as `handler`. Extends the built-in defaults (`handler`, `resolver`). Example: `["controladores", "controllers", "api"]` |
| `layer_rules.service` | []string | `[]` | Additional directory name patterns classified as `service`. Extends the built-in defaults (`service`). Example: `["usecase", "interactor"]` |
| `layer_rules.repository` | []string | `[]` | Additional directory name patterns classified as `repository`. Extends the built-in defaults (`repository`, `persistence`). Example: `["dao", "store", "repositorios"]` |
| `layer_rules.query` | []string | `[]` | Additional directory name patterns classified as `query`. Extends the built-in defaults (`generated`). Example: `["queries"]` |

### Minimal configs by project type

**Go backend only (Chi router, Wire DI, SQLC):**
```yaml
graph:
  backend_root: src
  router_file: src/infrastructure/http/router.go
  wire_file: src/infrastructure/config/wire.go
  sqlc_config: sqlc.yaml
```

**Go backend only (Echo router, Uber Fx):**
```yaml
graph:
  backend_root: .
  router_file: internal/server/routes.go
  fx_dir: internal/providers
```

**Go backend only (no DI framework, no SQLC):**
```yaml
graph:
  backend_root: .
```
In this case, use `@api` annotations on handlers and `@calls` annotations for any calls through interfaces.

**Full-stack Go + React:**
```yaml
graph:
  backend_root: src
  router_file: src/infrastructure/http/router.go
  wire_file: src/infrastructure/config/wire.go
  sqlc_config: sqlc.yaml
  frontend_roots:
    - apps/web/src
```

**No config file at all:**
testreg still works — it scans from `src/` by default, uses only `@api` annotations for routing, and relies on directory naming for layer classification.

### Environment variables

| Variable | Purpose |
|----------|---------|
| `TESTREG_TS_SCANNER` | Path to the TypeScript scanner script. Set this to enable frontend graph scanning. Without it, only the Go backend is traced. |

---

## Layer Recognition

testreg classifies every function in the call graph into an architectural layer (`handler`, `service`, `repository`, `query`, `external`). This classification drives the health score weights and gap severity. Understanding how it works helps you structure your project so testreg produces accurate results.

### How layers are assigned

**Backend (Go)** — classification is based on the **directory name** containing the Go file:

| Directory contains | Assigned layer | Examples |
|-------------------|---------------|---------|
| `handler` or `resolver` | `handler` | `handlers/`, `http/handler/`, `resolvers/` |
| `repository` or `persistence` | `repository` | `repositories/`, `persistence/`, `repo/` |
| `service` | `service` | `services/`, `application/services/` |
| `generated` | `query` | `generated/` (SQLC output) |
| *(anything else)* | `service` (default) | `utils/`, `pkg/`, `internal/` |

The match is case-insensitive and uses `strings.Contains`, so `my_handlers/` and `UserHandler/` both match `handler`. Nested paths work too — `src/infrastructure/http/handlers/auth_handler.go` matches because the path contains `handler`.

**SQLC queries** — methods generated by SQLC are classified as `query` and linked to their `.sql` source file. This happens through `sqlc_config` parsing, not directory naming.

**External calls** — calls to Go standard library packages (`http.`, `fmt.`, `json.`, `context.`, `strings.`, etc.) are classified as `external` and excluded from health scoring.

**Frontend (TypeScript)** — the TypeScript scanner emits explicit kind labels:

| Kind | What it matches |
|------|----------------|
| `component` | React page/component files imported from route definitions |
| `hook` | React hooks (`use*` functions) |
| `endpoint` | API route paths (URL strings in API service files) |

### What this means for your project

**If your directories follow the convention**, everything works automatically:

```
src/
├── infrastructure/
│   └── http/
│       └── handlers/          ← classified as "handler"
├── application/
│   └── services/              ← classified as "service"
├── domain/
│   └── repositories/          ← classified as "repository"
└── generated/                 ← classified as "query" (SQLC)
```

**If your directories don't match**, testreg still builds the graph — but everything defaults to `service`. This means:
- Health scores are skewed (all weight goes to the service layer)
- Gap severity may be wrong (a handler gap reported as service-level)
- The trace tree still works correctly (edges are based on AST, not naming)

**To fix misclassified layers**, you have three options:
1. **Add `layer_rules`** to `.testreg.yaml` to teach testreg your naming conventions (recommended)
2. **Rename directories** to include `handler`, `service`, `repository`, or `persistence`
3. **Accept the default** — the graph and traces are still accurate, only health scores and gap severity are affected

**Example — non-English codebase:**
```yaml
graph:
  layer_rules:
    handler: ["controladores", "controllers"]
    repository: ["repositorios", "dao", "store"]
    service: ["servicios", "usecase"]
```

Custom rules are checked **before** the built-in defaults. They extend the defaults, so the built-in patterns (`handler`, `service`, `repository`, `persistence`, `generated`) still work alongside your custom ones.

### Common directory structures that work

```
# Clean architecture
src/handlers/         → handler
src/services/         → service
src/repositories/     → repository

# Hexagonal architecture
src/infrastructure/http/handlers/   → handler
src/application/services/           → service
src/domain/repositories/            → repository

# Domain-driven (with convention)
src/auth/handler/     → handler
src/auth/service/     → service
src/auth/persistence/ → repository

# Flat structure (everything defaults to service)
src/auth/             → service (default)
src/meals/            → service (default)
```

### Structures that DON'T work without `layer_rules`

```
# No naming convention — everything becomes "service"
src/auth/auth.go          → service (should be handler? service? unknown)
src/auth/auth_db.go       → service (should be repository)

# Non-English naming — defaults don't recognize these
src/controladores/        → service (should be handler)
src/repositorios/         → service (should be repository)
```

**Fix with `layer_rules`:**
```yaml
graph:
  layer_rules:
    handler: ["controladores"]
    repository: ["repositorios"]
```

For flat structures without any naming convention (`src/auth/auth.go`), use `@api` annotations to identify handlers and accept that health scores will be approximate.

---

## Registry YAML Format

Each domain is stored as a separate YAML file in the registry directory:

```yaml
domain: auth
description: Authentication and authorization features
features:
  - id: auth.login
    name: User Login
    description: Email/password authentication with JWT tokens
    roles: [patient, nutritionist, admin]
    priority: critical
    surfaces:
      web:
        route: /login
        component: LoginPage
      mobile:
        screen: LoginScreen
      api:
        - method: POST
          path: /api/v1/auth/login
    coverage:
      unit:
        backend:
          status: covered
          files: [src/services/auth_service_test.go]
          mocked: true
        web:
          status: missing
      integration:
        backend:
          status: covered
          files: [src/handlers/auth_e2e_test.go]
          mocked: false
      e2e:
        web:
          status: covered
          files: [e2e/auth.spec.ts]
          last_run: "2026-03-30"
          pass_rate: 1.0
```

**Coverage status values:** `covered`, `partial`, `missing`, `failing`

**Priority levels:** `critical`, `high`, `medium`, `low`

---

## How testreg Works

testreg combines three types of analysis: manually maintained YAML definitions, static analysis of source code (Go AST, TypeScript AST), and heuristic file-existence matching.

```
Registry YAML ──┐
Go AST scanner ──┼──▶ Unified Graph ──▶ Trace ──▶ Audit
TS AST scanner ──┘       │                          │
                   Nodes + Edges          Test status (heuristic)
                   (deterministic)        Health score (weighted avg)
                                          Gap severity (rule-based)
```

### Graph Scanner Phases

1. **Pre-resolution** -- Parse `sqlc.yaml` to map generated Go methods to SQL source files
2. **Route discovery** -- Parse router file + `@api` annotations to extract HTTP routes
3. **Function discovery** -- Walk all `.go` files, create nodes, build struct field type maps, resolve Wire/Fx bindings
4. **Call graph extraction** -- Walk function bodies, resolve calls through field lookups and DI bindings, add edges

The TypeScript scanner runs as a Node.js subprocess using `ts.createSourceFile` to extract React Router routes, component→hook→API call chains.

### Test Coverage Detection

5-strategy file matching heuristic:

1. **Exact convention** -- `auth_handler.go` → `auth_handler_test.go` → `tested`
2. **Same directory** -- Test file in same directory → `partial`
3. **Base name match** -- Shared base name anywhere → `partial`
4. **Package match** -- Shared directory segment → `partial`
5. **No match** → `untested`

> **Important:** Test status reflects file existence, not test quality or pass rate. Use `testreg update` to ingest actual results.

### Limitations

- Test status is file existence, not pass/fail
- `partial` counts as `tested` in health score (can inflate it)
- Gap severity is fixed by architectural layer, not risk-based
- Performance gap detection (`func Benchmark*`, `t.Parallel()`) is text pattern matching
- The default scanner uses `go/ast` without `go/types` -- cannot resolve interface implementations beyond Wire/Fx bindings
- Feature ID inference from `go test -json` is best-guess heuristic

---

## CI Integration

### Annotation enforcement

```yaml
- name: Check test annotations
  run: |
    testreg scan
    testreg report
    GAPS=$(sed -n '/## Unannotated Test Files/,/^##/p' docs/testing/COVERAGE.md | grep -c "^- " || true)
    if [ "$GAPS" -gt 0 ]; then
      echo "::error::$GAPS test files missing @testreg annotations"
      exit 1
    fi
```

### PR coverage comment

```yaml
- name: Post coverage summary
  run: |
    testreg scan
    testreg status > /tmp/coverage.txt
    gh pr comment ${{ github.event.pull_request.number }} --body "$(cat /tmp/coverage.txt)"
```

### Sprint progress tracking

```yaml
- name: Save health snapshot
  if: github.ref == 'refs/heads/main'
  run: |
    testreg diff --save-snapshot "ci-$(date +%Y%m%d)"
    git add .testreg-cache/snapshots/
    git diff --cached --quiet || git commit -m "chore: update health snapshot"
```

---

## Common Mistakes

| Mistake | Correct Approach |
|---------|-----------------|
| `grep -r "@testreg auth" tests/` | `testreg scan && testreg status --domain auth` |
| `find tests/ -name "*_test.go" \| wc -l` | `testreg status` |
| 80-line python coverage report script | `testreg scan && testreg report` |
| `jq` pipeline to filter audit by priority | `testreg audit --priority critical` |
| Python script to sort by priority score | `testreg sprint` |
| Manual JSON comparison for before/after | `testreg diff --save-snapshot` / `testreg diff` |
| Python script to extract gaps for subagents | `testreg gaps --format actionable` |
| Running `scan && audit` every time | `testreg audit --rescan` |

---

## Performance

Measured on a real production monorepo (nutrition-project-v2: 184 features, 1103 test files, full-stack Go + React + TypeScript).

| Command | Wall Time | What it does |
|---------|-----------|-------------|
| `testreg scan` | **0.5s** | Discover 1103 test files across 8 parallel scanners |
| `testreg trace auth.login` | **0.7s** | Build full-stack call graph, trace 16 nodes |
| `testreg audit auth.login` | **0.7s** | Trace + annotate + health score for one feature |
| `testreg audit --all` | **13s** | Audit all 184 features (graph built once) |
| `testreg sprint -n 10` | **10s** | Rank all 184 features by priority-weighted gap score |

| Scenario | Peak RSS |
|----------|----------|
| Small project (41 features, 42 test files) | **17 MB** |
| Large monorepo (184 features, 1103 test files) | **153 MB** |

```
Binary:       ~12 MB (static, single file)
Source:       15,207 lines (production code)
Tests:        12,694 lines (371 tests)
Dependencies: 3 direct (cobra, yaml.v3, x/tools)
```

Key optimization: `ExecuteAll` builds the graph once, then traces each feature against the shared graph. Before: 1m52s for 184 features. After: **14s** (7.9x speedup).

> **Warning: `type_checking: true` is experimental and buggy.** It does not yet integrate the route parser, Wire/Fx resolver, or SQLC mapper — producing fewer traced nodes than the default scanner. Uses ~4 GB memory vs ~150 MB. **We do not recommend enabling this feature yet.**

---

## Architecture

```
cmd/                 Cobra command definitions (CLI entry points)
internal/
  domain/            Core types: Feature, Registry, Graph, Node, Edge, Audit
  ports/             Interface definitions (GraphBuilder, RegistryStore, TestScanner)
  app/               Use cases orchestrating domain logic
  adapters/          Implementations:
    go_ast_scanner   Go AST parsing (go/ast, go/parser) for call graph construction
    frontend_scanner Invokes ts-scanner.ts for TypeScript AST parsing
    route_parser     Chi/Echo/stdlib router file parsing for route discovery
    wire_resolver    Wire dependency injection binding resolution
    sqlc_mapper      SQLC config parsing to map generated methods to SQL sources
    annotation_parser  @testreg and @api annotation extraction
    yaml_store       Registry persistence (YAML read/write)
    *_scanner        Test file discovery (Go, Vitest, Playwright, Jest, Maestro)
    *_reporter       Output rendering (terminal, markdown, JSON)
ts-scanner.ts        TypeScript AST scanner (ts.createSourceFile)
```

---

## Supported Frameworks

### Backend

| Framework | Discovery Method |
|-----------|-----------------|
| Chi | Router file parsing (auto) |
| Echo | Route file parsing (auto) |
| stdlib `net/http` | Router file parsing (auto) |
| Any Go HTTP framework | `@api` annotations |

### Frontend

| Framework | Discovery Method |
|-----------|-----------------|
| React Router | TypeScript AST scanning (auto) |
| TanStack Query | Hook call analysis (auto) |
| Any framework | `@testreg` annotations (coverage tracking only) |

### Dependency Injection

| Tool | Integration |
|------|------------|
| Wire | Automatic binding resolution from `wire.go` |
| Uber Fx / Dig | Automatic provider resolution |
| SQLC | Automatic method-to-SQL mapping |

### Test Runners

| Runner | File Pattern | Platform |
|--------|-------------|----------|
| Go test | `*_test.go` | Backend |
| Vitest | `*.test.ts` | Web |
| Playwright | `*.spec.ts` | Web E2E |
| Jest | `__tests__/**` | Mobile |
| Maestro | `*.yaml` (in e2e dirs) | Mobile E2E |
| pytest | `test_*.py`, `*_test.py` | Backend |

---

## Contributing

Contributions are welcome. Please open an issue to discuss significant changes before submitting a pull request.

```bash
go test ./...    # Run tests
go build -o testreg .  # Build
go install .     # Install locally
```

---

## License

[Apache 2.0](LICENSE)

Copyright 2026 Alejandro Sosa
