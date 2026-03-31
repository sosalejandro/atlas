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

  Feature: User Login (auth.login)
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
```

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
- **Framework-agnostic annotations** -- `@api` and `@testreg` annotations work with any Go HTTP router
- **Wire and SQLC integration** -- resolves dependency injection bindings and generated query mappings automatically
- **TypeScript AST scanning** -- parses React Router, TanStack Query hooks, and API service files via `ts.createSourceFile`

---

## When testreg Works Best (and When It Doesn't)

testreg is not a generic tool. It makes specific assumptions about how your codebase is structured. Understanding these assumptions upfront determines how much value you'll get.

### What testreg assumes about your Go backend

The Go AST scanner resolves call graphs by parsing source code structure -- it does not use the Go type checker (`go/types`). This means it relies on **naming conventions** and **struct field injection** to classify nodes and follow call chains.

| Assumption | What testreg expects | What happens otherwise |
|------------|---------------------|----------------------|
| **Package naming** | Directories named `handler*/`, `service*/`, `repository*/`, `persistence*/` | Functions are classified as `service` (default). Health score weights may be wrong, but the graph still builds. |
| **Struct field injection** | Dependencies stored as struct fields: `type Handler struct { service AuthService }` | Calls through constructor parameters, closures, or globals are **invisible** to the scanner. Edges are lost. |
| **Layered architecture** | Handler -> Service -> Repository -> Query call flow | The health score weights (handler 30%, service 30%, repository 25%, query 15%) assume this layering. Flat architectures get skewed scores. |

### Dependency injection support

| DI Approach | Support Level | Details |
|-------------|--------------|---------|
| **Google Wire** | Full | Parses `wire.Bind()` and provider functions to resolve interface-to-concrete mappings |
| **Manual wiring (struct fields)** | Full | As long as dependencies are struct fields, the AST scanner resolves them |
| **Manual wiring (constructor params)** | Partial | The constructor call is visible, but calls inside the handler through params are not traced |
| **Uber Dig / fx** | None | Dig uses runtime reflection. No static analysis possible. Graph still builds from struct fields, but DI bindings are invisible |
| **No DI (closures/globals)** | None | Captured variables and global singletons cannot be traced via AST |

### Router support

| Router | Support Level | Details |
|--------|--------------|---------|
| **Chi** | Auto-detected | `r.Get()`, `r.Post()`, `r.Route()`, `r.Group()`, `r.With()` -- all parsed from router file |
| **stdlib `net/http`** | Via annotations | Add `@api GET /path` above handler functions |
| **Gin, Echo, Fiber, gorilla/mux** | Via annotations | Add `@api` annotations. No auto-detection for these frameworks |

If you don't use Chi, omit `router_file` from `.testreg.yaml` and use `@api` annotations on all handlers. The graph works identically.

### Data access support

| Tool | Support Level | Details |
|------|--------------|---------|
| **SQLC** | Full | Maps generated Go methods to SQL source files via `sqlc.yaml` |
| **Raw SQL / sqlx** | None | No query nodes in graph. Omit `sqlc_config` from config |
| **GORM, ent, Bun** | None | ORM calls are too dynamic for AST analysis |

### Frontend support

| Framework | Support Level | Details |
|-----------|--------------|---------|
| **React Router + hooks** | Full | Routes, components, hooks, and API service calls traced |
| **TanStack Query** | Full | `useQuery`/`useMutation` hooks recognized |
| **Next.js, Remix** | None | App directory routing not supported |
| **Vue, Svelte, Angular** | None | Only TypeScript/React patterns are parsed |

Frontend scanning is entirely optional. If omitted, testreg produces backend-only graphs.

### The `@api` escape hatch

For any pattern testreg can't auto-detect, the `@api` annotation provides a manual override:

```go
// @api POST /api/v1/auth/login
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) { ... }
```

This works with **any** Go HTTP framework. The annotation is framework-agnostic and takes precedence over auto-detected routes.

### Minimum viable setup

To get value from testreg with the least effort:

1. **Required:** Business features defined in registry YAML files with API surfaces
2. **Required:** `@testreg` annotations on test files
3. **Recommended:** Package naming that matches `handler/service/repository` conventions
4. **Recommended:** Dependencies as struct fields (not constructor params or closures)
5. **Optional:** Wire DI file, SQLC config, Chi router file, TypeScript scanner

Without items 1-2, testreg has nothing to work with. Without 3-4, the graph builds but classifications and call chains degrade.

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

# 2. Add @testreg annotations to your test files (see Annotation Format below)

# 3. Scan the project to map tests to features
testreg scan

# 4. View the coverage dashboard
testreg status

# 5. Trace a feature's full dependency chain
testreg trace auth.login

# 6. Run a health audit across all features
testreg audit
```

---

## Workflow

testreg has a natural flow depending on what you're doing. Each phase uses specific commands in a specific order.

### First Time (Project Onboarding)

```bash
# 1. Scaffold the registry with template domain files
testreg init

# 2. Discover existing test files and map them via @testreg annotations
testreg scan

# 3. See where you stand -- coverage dashboard across all features
testreg audit

# 4. Add @testreg annotations to existing test files that were unmapped
#    (check _unmapped.yaml for the list)
#    Then re-scan to pick them up
testreg scan

# 5. Establish your baseline health scores
testreg audit
```

### Before Coding (Understanding Phase)

Run these BEFORE writing any code for a feature. They tell you what code already exists, what patterns to follow, and what tests are missing.

```bash
# See the full call graph for the feature you're about to work on
# This shows every function from the frontend route to the SQL query
testreg trace <feature-id>

# See which parts of that call graph have test coverage and which don't
# This gives you a health score and a list of gaps with severity ratings
testreg audit <feature-id>

# If you want a visual diagram for a design doc or PR description
testreg graph <feature-id> --format mermaid
```

### During Implementation

While writing code, add annotations to new files:

```go
// On new Go handlers -- tells testreg which API endpoint this handles
// @api POST /api/v1/meals
func (h *MealHandler) CreateMeal(w http.ResponseWriter, r *http.Request) { ... }
```

```go
// On new test files -- tells testreg which feature this test covers
// @testreg meals.create #real
func TestCreateMealHandler(t *testing.T) { ... }
```

```typescript
// On new Playwright specs
// @testreg meals.create #real
test('should create a meal log', async ({ page }) => { ... })
```

### After Coding (Verification Phase)

```bash
# Update the registry with your new annotations
testreg scan

# Run all tests for the feature you changed (across all platforms)
testreg run <feature-id>

# Check that your changes improved the health score
testreg audit <feature-id>

# If health is still low, the audit output tells you exactly what's missing:
#   Gaps:
#     ✘ [CRITICAL] MealService.CreateMeal -- no unit test
#   Recommended Actions:
#     1. Write unit test for MealService.CreateMeal
#        File: src/application/services/meal_service_test.go
```

### When Tests Fail

```bash
# Step 1: Diagnose -- testreg matches the error against known patterns
#         and tells you which layer likely broke
testreg diagnose <feature-id> --symptom "401 Unauthorized"
# Output: Layer: backend-auth → Check: auth_service.go, then user_repo.go

# Step 2: Trace -- see the full dependency chain for context
testreg trace <feature-id>

# Step 3: Fix at the layer diagnose pointed to

# Step 4: Verify the fix
testreg run <feature-id>
```

### Before a Pull Request

```bash
# Check all features for regressions -- sorted by worst health first
testreg audit

# Focus on critical gaps only
testreg audit --min-health 0.5

# Generate a coverage report for the PR description
testreg report --format md --output COVERAGE.md
```

---

## Concepts & Variable Reference

### Feature ID

The primary identifier for a business feature. Format: `<domain>.<feature>[.<sub-feature>]`

```
auth.login              -- User login flow
recipes.create          -- Create a new recipe
plans.nutritionist.list -- List plans (nutritionist view)
meals.log.create        -- Create a meal log entry
```

Feature IDs are defined in the registry YAML files (`docs/testing/registry/<domain>.yaml`) and referenced in `@testreg` annotations across all test files.

### Node ID

Identifies a single function or entity in the dependency graph. Format depends on the node kind:

| Kind | Format | Example |
|------|--------|---------|
| Go method | `ReceiverType.MethodName` | `AuthHandler.Login` |
| Go function | `packageName.FuncName` | `repositories.HashToken` |
| SQL query | `sql:QueryName` | `sql:GetUserByEmail` |
| API endpoint | `METHOD /path` | `POST /api/v1/auth/login` |
| React route | `route:/path` | `route:/login` |
| React component | `ComponentName` | `LoginPage` |
| React hook | `hookName` | `useAuth` |
| API service method | `serviceName.method` | `authApi.login` |

### Node Kind

Classifies a node's role in the architecture. Used for color-coding in output and coverage calculations.

| Kind | Description | In trace output | Audit weight |
|------|-------------|-----------------|-------------|
| `handler` | HTTP/gRPC handler function | Cyan | 30% |
| `service` | Application/business logic | Green | 30% |
| `repository` | Data access layer | Yellow | 25% |
| `query` | SQL query (SQLC) | Magenta | 15% |
| `component` | React page/component | Cyan | -- |
| `hook` | React hook | Green | -- |
| `endpoint` | API boundary (URL) | White | -- |
| `external` | External service (Redis, SMTP) | Red | -- |

### Test Status

Each node in an `audit` output has a test status indicator:

| Symbol | Status | Meaning |
|--------|--------|---------|
| `✓` | tested | A test file directly covers this function's file |
| `◐` | partial | A test file exists in a related directory but may not cover this specific function |
| `✘` | untested | No test file found for this function's file |

### Gap Severity

Untested nodes are prioritized by severity in the `audit` output:

| Severity | Criteria | What it means |
|----------|----------|---------------|
| `CRITICAL` | Handler or service method with no test | Core business logic is untested |
| `HIGH` | Repository method with no integration test | Data access is untested |
| `MEDIUM` | SQL query with no test coverage | Database queries are unverified |
| `LOW` | Component, hook, or other node without a test | Supporting code is untested |

### Health Score

A weighted average of per-layer test coverage for a feature. Calculated by `testreg audit`.

```
Health = (handler_coverage × 0.30)
       + (service_coverage × 0.30)
       + (repository_coverage × 0.25)
       + (query_coverage × 0.15)
```

Score interpretation:

| Range | Color | Meaning |
|-------|-------|---------|
| 80-100% | Green | Well tested, safe to ship |
| 50-79% | Yellow | Gaps exist, review before shipping |
| 0-49% | Red | Significant gaps, prioritize testing |

### Confidence Score

Reported by `trace` and `audit`. Indicates how much of the source code was successfully parsed by the AST scanner.

```
Confidence: 100%  -- all files parsed successfully
Confidence: 94%   -- 1 file failed to parse (check warnings)
Confidence: 0%    -- root node not found (wrong feature ID or missing code)
```

Low confidence means the trace may be incomplete. Check the warnings output for which files failed.

### Annotations Quick Reference

| Annotation | Where | Purpose | Example |
|------------|-------|---------|---------|
| `@testreg <feature-id>` | Test files (Go, TS, YAML) | Map test to feature | `// @testreg auth.login` |
| `@testreg <id> #mocked` | Test files | Mark as mocked test | `// @testreg auth.login #mocked` |
| `@testreg <id> #real` | Test files | Mark as real integration test | `// @testreg auth.login #real` |
| `@testreg <id> #wip` | Test files | Mark as work in progress | `// @testreg auth.login #wip` |
| `@api METHOD /path` | Go handler methods | Map handler to API route | `// @api POST /api/v1/auth/login` |
| `@calls <node-id>` | Go functions | Declare hidden call edge | `// @calls notification_service.SendEmail` |

### Configuration Fields

The `.testreg.yaml` file at the project root controls the graph scanner:

| Field | Type | Default | When it's used |
|-------|------|---------|----------------|
| `graph.backend_root` | string | `"src"` | Every `trace`/`graph`/`audit` — where to find Go source files |
| `graph.router_file` | string | `""` | `trace`/`graph` — file or directory with HTTP route registrations |
| `graph.wire_file` | string | `""` | `trace`/`graph` — Wire DI file for interface→concrete resolution |
| `graph.sqlc_config` | string | `""` | `trace`/`graph` — SQLC config for query→SQL file mapping |
| `graph.frontend_roots` | []string | `[]` | `trace`/`graph`/`audit` — directories to scan for React/TS code |
| `graph.ignore_packages` | []string | `[]` | `trace`/`graph` — Go packages to skip (e.g., logging, middleware) |
| `graph.ignore_functions` | []string | `[]` | `trace`/`graph` — function glob patterns to skip (e.g., `*.String`) |
| `graph.cache_dir` | string | `".testreg-cache"` | All graph commands — where to cache parsed AST results |
| `graph.max_depth` | int | `10` | `trace`/`graph`/`audit` — how deep to traverse the call graph |

### Environment Variables

| Variable | Purpose | Example |
|----------|---------|---------|
| `TESTREG_TS_SCANNER` | Path to the TypeScript scanner script | `export TESTREG_TS_SCANNER=/path/to/ts-scanner.ts` |

Set `TESTREG_TS_SCANNER` to enable frontend graph scanning. Without it, only the Go backend is traced.

---

## Commands Reference

### `testreg init`

Bootstrap the registry directory with template YAML domain files. Idempotent: running it again merges new features without overwriting existing entries.

```bash
testreg init
testreg init --registry-dir path/to/registry
```

### `testreg scan`

Discover test files across all platforms and map them to features using `@testreg` annotations. Unmapped tests are saved to `_unmapped.yaml` for manual review.

Scanners included: Go (`*_test.go`), Vitest (`*.test.ts`), Playwright (`*.spec.ts`), Jest (`__tests__/`), Maestro (`*.yaml`).

```bash
testreg scan

# Output:
# Scan complete.
#   Total test files: 247
#   Mapped:           198
#   Unmapped:         49
```

### `testreg status`

Display a terminal table with coverage metrics per domain and platform.

```bash
testreg status                       # All domains
testreg status --domain auth         # Filter by domain
testreg status --priority critical   # Filter by priority
testreg status --format json         # JSON output
```

### `testreg check <feature>`

Show detailed coverage for a single feature, including all test entries with status, gap analysis, and actionable suggestions.

```bash
testreg check auth.login
testreg check meals.log --format json
```

### `testreg report`

Generate a comprehensive coverage report.

```bash
testreg report                              # Markdown (default) to docs/testing/COVERAGE.md
testreg report --format json                # JSON to stdout
testreg report --format terminal            # Terminal table
testreg report --output ./COVERAGE.md       # Custom output path
```

### `testreg update`

Ingest test results from CI output and update the registry with pass/fail status, pass rates, and last-run timestamps.

```bash
testreg update --playwright ./test-results/       # Playwright JSON results
testreg update --gotest ./go-test-output.json     # go test -json output
testreg update --maestro ./maestro-output/        # Maestro output directory
```

### `testreg run <feature>`

Execute tests associated with a feature. Collects run commands from the registry and executes them sequentially.

```bash
testreg run auth.login                      # Run all tests for a feature
testreg run auth.login --platform backend   # Backend tests only
testreg run auth.login --type unit          # Unit tests only
testreg run auth.login --dry-run            # Preview commands without executing
testreg run --failing                       # Run only failing features
testreg run --priority critical             # Run all critical-priority tests
```

| Flag | Description |
|------|-------------|
| `--platform` | Filter by platform: `backend`, `web`, `mobile` |
| `--type` | Filter by test type: `unit`, `integration`, `e2e` |
| `--dry-run` | Print commands without executing |
| `--failing` | Run tests for features with failures only |
| `--priority` | Run tests at a given priority level: `critical`, `high`, `medium`, `low` |

### `testreg trace <feature>`

Trace a feature's full-stack dependency graph. Starts from the feature's API entry points and follows the call chain through handlers, services, repositories, and SQL queries. Includes frontend routes and hooks when TypeScript scanning is configured.

```bash
testreg trace auth.login                        # Tree output (default)
testreg trace auth.login --format json          # JSON output
testreg trace auth.login --depth 5              # Limit traversal depth
testreg trace auth.login --verbose              # Include utility functions
testreg trace auth.login --list-nodes           # Flat list of all node IDs
testreg trace auth.login --list-nodes --kind service  # Filter by node kind
testreg trace auth.login --validate             # Check for duplicates, cycles, missing refs
```

### `testreg graph <feature>`

Export a feature's dependency graph in a format suitable for visualization tools.

```bash
testreg graph auth.login --format dot                    # Graphviz DOT
testreg graph auth.login --format mermaid                # Mermaid flowchart
testreg graph auth.login --format json                   # JSON
testreg graph auth.login --format dot --output auth.dot  # Write to file
```

Pipe DOT output to Graphviz:

```bash
testreg graph auth.login --format dot | dot -Tsvg -o auth.svg
```

### `testreg diagnose <feature> --symptom "..."`

Match an error symptom against built-in failure patterns, then trace the feature's dependency graph to identify which files to check first. Useful for rapid triage when a test or production error occurs.

```bash
testreg diagnose auth.login --symptom "401 unauthorized"
testreg diagnose auth.login --symptom "timeout exceeded" --json
```

Example output:

```
  Matched rule: Authentication failure
  Layer: backend-auth
  Description: Request lacks valid credentials or session has expired

  Files to check (ordered by likelihood):
    1. src/infrastructure/http/handlers/auth_handler.go
    2. src/application/services/auth_service.go
    3. src/infrastructure/auth/jwt_generator.go
```

Built-in symptom patterns include: `401/unauthorized`, `403/forbidden`, `404/not found`, `500/internal server error`, `timeout/deadline exceeded`, `connection refused`, `selector not found`, `empty response`, and more.

### `testreg audit <feature>`

Generate a unified health report for a single feature by combining dependency traces, test coverage data, and gap analysis.

```bash
testreg audit auth.login
testreg audit auth.login --format json
testreg audit auth.login --format markdown --output auth-health.md
```

Example output:

```
Feature: auth.login (critical)  Health: 74%
═══════════════════════════════════════════════════════

  Dependency Chain:
    route:/login                                            ✓ tested
    └─ LoginPage                                            ✓ tested
       └─ useAuth                                           ✓ tested
          └─ authApi.login                                  ✓ tested
             └─ POST /api/v1/auth/login                     ✓ tested
                └─ AuthHandler.Login                        ✓ tested
                   └─ authService.Login                     ✓ tested
                      ├─ JWTGenerator.GenerateTokenPair     ✓ tested
                      ├─ authRepository.StoreRefreshToken   ✘ NO TEST
                      ├─ repositories.HashToken             ✘ NO TEST
                      └─ sql:GetUserByEmail                 ✘ NO TEST

  Coverage by Layer:
    Handler:     1/1  (100%) ████████████████████
    Service:     6/8  ( 75%) ███████████████░░░░░
    Query:       0/1  (  0%) ░░░░░░░░░░░░░░░░░░░░

  Gaps (13):
    ✘ [CRITICAL] authRepository.StoreRefreshToken — no unit test
    ✘ [HIGH]     repositories.HashToken — no unit test
    ✘ [MEDIUM]   sql:GetUserByEmail — no query-level test

  Recommended Actions:
    1. Write unit test for authRepository.StoreRefreshToken
    2. Write unit test for repositories.HashToken
    3. Add integration test covering sql:GetUserByEmail
```

The health score is a weighted average of layer coverage: Handler 30%, Service 30%, Repository 25%, Query 15%.

Gap severity levels:
- **CRITICAL** -- handlers or services with no unit test
- **HIGH** -- repositories with no integration test
- **MEDIUM** -- SQL queries with no test
- **LOW** -- other untested nodes

### `testreg audit` (no arguments)

Generate a summary health table for all features, sorted worst-first. Supports filtering, sorting, and summary views.

```bash
testreg audit                                          # All features, worst-first
testreg audit --all                                    # Explicit all-features mode
testreg audit --min-health 0.8                         # Only features below 80% health
testreg audit --priority critical                      # Critical features only
testreg audit --priority critical,high                 # Critical + high features
testreg audit --sort priority-score                    # Sort by weighted gap score
testreg audit --sort priority-score -n 20              # Top 20 by priority score
testreg audit --sort name                              # Sort alphabetically
testreg audit --summary                                # Aggregate counts by priority tier
testreg audit --unconfigured                           # Features with no API surfaces
testreg audit --rescan                                 # Auto-scan before auditing
testreg audit --format json                            # JSON output
testreg audit --format markdown                        # Markdown output

# Flags compose freely:
testreg audit --priority critical --sort priority-score --min-health 0.5
testreg audit --priority critical --summary
testreg audit --rescan --priority critical -n 10
```

**`--summary` output:**

```
  Priority Summary:
    CRITICAL    8/23 at target  (15 gaps)  ████░░░░░░  35%
    HIGH        9/75 at target  (66 gaps)  █░░░░░░░░░  12%
    MEDIUM      3/52 at target  (49 gaps)  █░░░░░░░░░   6%
    LOW         2/34 at target  (32 gaps)  █░░░░░░░░░   6%

    Overall: 22/184 features at target (12%)
```

Example output:

```
┌──────────────────────────┬──────────┬────────┬──────┬──────┬──────┐
│ Feature                  │ Priority │ Health │ Gaps │ E2E  │ Unit │
├──────────────────────────┼──────────┼────────┼──────┼──────┼──────┤
│ auth.login               │ critical │   74%  │  13  │  ✓   │  ✓   │
│ billing.pricing-page     │ critical │   45%  │   8  │  ✘   │  ✓   │
│ meals.log                │ high     │   62%  │   5  │  ✓   │  ✓   │
│ recipes.search           │ medium   │   88%  │   2  │  ✓   │  ✓   │
└──────────────────────────┴──────────┴────────┴──────┴──────┴──────┘
```

### `testreg sprint`

Rank features by priority-weighted gap score for sprint planning. Replaces ad-hoc python scripts for prioritizing which features to test next.

The score is computed as `weight * max(0, target - health)`:
- Weights: critical=4, high=3, medium=2, low=1
- Targets: critical=100%, high=80%, medium=60%, low=40%

Features already at or above their target are excluded.

```bash
testreg sprint                                  # Top 20 priorities (default)
testreg sprint -n 10                            # Top 10 only
testreg sprint --priority critical,high         # Critical + high only
testreg sprint --group-by type                  # Group by gap type (unit, integration, e2e)
testreg sprint --group-by domain                # Group by domain (auth, meals, recipes)
testreg sprint --format json                    # JSON output
```

Example output:

```
Sprint Priorities (20 features, sorted by priority score):

  Score  Priority   Health  Target  Feature
  ──────────────────────────────────────────────────────────────────────
   4.00  critical      0%    100%  auth.login
   4.00  critical      0%    100%  auth.register
   3.00  critical     25%    100%  auth.token-refresh
   2.40  high         20%     80%  recipes.create

  By Fix Type:
    unit tests:          34 features
    integration tests:   18 features
    e2e tests:            6 features
    benchmarks:          12 features
    race tests:           8 features
```

### `testreg gaps`

Extract actionable test gap information. Designed for feeding into automated test-writing workflows and AI subagents.

```bash
testreg gaps                                    # All features with gaps
testreg gaps --feature auth.login               # Gaps for one feature
testreg gaps --priority critical                # Critical features only
testreg gaps --min-health 0.5                   # Features below 50% health
testreg gaps --format actionable                # Structured output with fix instructions
testreg gaps --format prompt                    # Optimized for AI consumption
testreg gaps --format json                      # JSON output
testreg gaps --priority critical -n 5           # Top 5 critical features
```

**`--format actionable` output:**

```
Feature: auth.login (critical, health: 74%)
  [CRITICAL] authService.Login -- no unit test
    File: src/application/services/auth_service.go:172
    Action: Write unit test for authService.Login
    Pattern: table-driven test with mock repository

  [HIGH] authRepository.StoreRefreshToken -- no integration test
    File: src/domain/repositories/auth_repository.go:329
    Action: Write integration test for authRepository.StoreRefreshToken
    Pattern: TestMain setup with test database
```

**`--format prompt` output** (for AI subagents):

```
## Feature: auth.login
Priority: critical | Health: 74% | Target: 100%

### Gaps (3):
1. CRITICAL: authService.Login has no unit test for service method
   - Source: src/application/services/auth_service.go:172
   - Write: Write unit test for authService.Login
   - Annotation: // @testreg auth.login #real
```

### `testreg diff`

Compare feature health snapshots across sprints. Track progress, identify regressions, and measure improvement.

```bash
# Save a baseline snapshot before a testing sprint
testreg diff --save-snapshot sprint-1

# After working on tests, compare against the baseline
testreg diff                                    # Compare vs latest saved snapshot
testreg diff --baseline path/to/snapshot.json   # Compare vs specific file

# Compare two named snapshots
testreg diff --from sprint-1 --to sprint-2

# JSON output for CI integration
testreg diff --format json
```

Example output:

```
Health Changes (since sprint-1):

  Improved (12 features):
    +100%  auth.login                      0% -> 100%
    + 74%  client-management.detail        0% ->  74%
    + 50%  recipes.create                 30% ->  80%

  Regressed (2 features):
    - 10%  meals.log.create              80% ->  70%

  Unchanged: 170 features

  Summary: +15.4% average health change
```

Snapshots are stored in `.testreg-cache/snapshots/` as JSON files. Each `--save-snapshot` also writes a `latest.json` for convenient `testreg diff` with no arguments.

---

## Global Flags

These flags are available on all commands:

```
--registry-dir <path>   Path to registry YAML files (default: docs/testing/registry)
--project-root <path>   Project root directory (auto-detected from git root)
```

---

## Annotation Format

testreg uses source code annotations to map test files to features and to connect handler functions to their HTTP endpoints.

### `@testreg` -- Map tests to features

Place `@testreg` annotations in test file comments to associate tests with features. Annotations can be file-level (apply to all tests in the file) or function-level (apply to the immediately following test).

**File-level annotation (Go):**

```go
// @testreg auth.login
package auth_test

func TestLoginSuccess(t *testing.T) { ... }
func TestLoginInvalidPassword(t *testing.T) { ... }
```

**Function-level annotation (Go):**

```go
// @testreg auth.login
func TestLoginSuccess(t *testing.T) { ... }

// @testreg auth.register
func TestRegisterNewUser(t *testing.T) { ... }
```

**TypeScript / Playwright / Jest:**

```typescript
// @testreg auth.login
test('should login with valid credentials', async () => { ... });

// @testreg auth.login,auth.session
test('should maintain session after login', async () => { ... });
```

**Maestro YAML:**

```yaml
# @testreg auth.login
appId: com.example.app
---
- launchApp
- tapOn: "Login"
```

**Multiple features:** Comma-separate IDs: `@testreg auth.login,auth.session`

**Flags:** Add hash-prefixed flags: `@testreg auth.login #flaky #slow`

### `@api` -- Map handlers to HTTP endpoints

Place `@api` annotations above Go handler functions to declare which HTTP endpoints they serve. This is the primary mechanism for connecting API routes to handler code without requiring a specific router framework.

```go
// @api POST /api/v1/auth/login
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
    // ...
}

// @api GET /api/v1/users/{id}
// @api PUT /api/v1/users/{id}
func (h *UserHandler) GetOrUpdateUser(w http.ResponseWriter, r *http.Request) {
    // ...
}
```

testreg also auto-discovers routes from Chi router files (specified via `router_file` in `.testreg.yaml`). The `@api` annotation is framework-agnostic and takes precedence when both are available.

---

## Configuration

testreg reads its configuration from `.testreg.yaml` in the project root. All fields are optional -- sensible defaults are applied when the file is absent or fields are omitted.

```yaml
graph:
  # Root directory for Go backend source code.
  # Default: "src"
  backend_root: src

  # Path to the Go router file for automatic route discovery (Chi/stdlib).
  # Optional. If omitted, only @api annotations are used.
  router_file: src/infrastructure/http/router.go

  # Path to the Wire dependency injection file.
  # Used to resolve interface-to-concrete bindings automatically.
  wire_file: src/infrastructure/config/wire.go

  # Path to sqlc.yaml config. Used to map generated Go methods to SQL source files.
  sqlc_config: sqlc.yaml

  # Frontend root directories for TypeScript AST scanning.
  # Each directory is scanned for React routes, hooks, and API service files.
  frontend_roots:
    - apps/web/src

  # Go packages to exclude from graph traversal.
  ignore_packages:
    - vendor
    - testutil

  # Function name patterns to exclude (glob syntax).
  ignore_functions:
    - "String"
    - "Error"
    - "MarshalJSON"

  # Directory for caching parsed AST data between runs.
  # Default: ".testreg-cache"
  cache_dir: .testreg-cache

  # Maximum call graph traversal depth.
  # Default: 10
  max_depth: 10
```

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

testreg combines three types of analysis: manually maintained YAML definitions, static analysis of source code (Go AST, TypeScript AST), and heuristic file-existence matching. Understanding which outputs are deterministic and which are best-effort helps you interpret results correctly.

```
Registry YAML ──┐
Go AST scanner ──┼──▶ Unified Graph ──▶ Trace ──▶ Audit
TS AST scanner ──┘       │                          │
                   Nodes + Edges          Test status (heuristic)
                   (deterministic)        Health score (weighted avg)
                                          Gap severity (rule-based)
```

### Data Sources

| Source | What It Provides | Method |
|--------|-----------------|--------|
| Registry YAML files | Feature definitions, priority, API surfaces, test file paths | Manual -- you maintain these |
| Go source code | Call graph nodes and edges (handler to SQL) | Deterministic -- `go/ast` + `go/parser` |
| TypeScript source code | Frontend route-to-API-call chains | Deterministic -- `ts.createSourceFile` via Node subprocess |
| `@testreg` annotations | Test-to-feature mappings | Deterministic -- regex extraction from comments |
| `@api` annotations | Handler-to-endpoint mappings | Deterministic -- regex extraction from comments |
| Test result output | Pass/fail, duration, retries | Deterministic -- parsed from `go test -json`, Playwright JSON, Maestro XML |

The graph scanner is real static analysis (`go/ast` + `go/parser`), not text search. But it does not use type checking (`go/types`), so it resolves method calls via struct field type maps and Wire bindings rather than full type inference.

### Test Coverage Detection

When testreg annotates a graph node as `tested`, `partial`, or `untested`, it uses a 5-strategy file matching heuristic against the registry's known test files:

1. **Exact convention match** -- For `auth_handler.go`, check if `auth_handler_test.go` exists. Result: `tested`.
2. **Same directory** -- A test file exists in the same directory with a different name. Result: `partial`.
3. **Base name match** -- A test file anywhere shares the base name (e.g., `auth_handler`). Result: `partial`.
4. **Package match** -- A test file shares a significant directory segment (e.g., both in `auth/`). Result: `partial`.
5. **No match** -- None of the above. Result: `untested`.

> **Important:** Test status reflects whether a test file exists near the source file, not whether tests pass. A node marked `tested` means a conventionally-named test file was found -- it says nothing about test quality or pass rate. Use `testreg update` to ingest actual pass/fail results from test runners.

### Gap Severity

Gaps are nodes with `untested` or `partial` status. Severity is assigned by the node's architectural layer:

| Node Kind | Severity | Rationale |
|-----------|----------|-----------|
| `handler`, `service` | CRITICAL | Core request-handling and business logic |
| `repository` | HIGH | Data access layer |
| `query` | MEDIUM | SQL queries (often tested indirectly via repository tests) |
| Everything else | LOW | Supporting code (components, hooks, utilities) |

This classification is hard-coded. testreg does not analyze code complexity, change frequency, or runtime risk.

### Health Score

The health score is a weighted average of per-layer coverage (see [Health Score](#health-score) in Concepts). Both `tested` and `partial` count as covered for scoring purposes.

> **Nuance:** `partial` counts the same as `tested` in the health score. This means the score can be optimistic -- a node marked `partial` has a test file nearby but may not actually test that specific function. Layers not present in the graph (e.g., no query nodes) are excluded from the weight denominator, not counted as zero.

### Performance Gap Detection

testreg checks test files for benchmark and race-test evidence via line-by-line text scanning:

- **Benchmark detection** -- looks for `func Benchmark*` at the start of a line
- **Race test detection** -- looks for `t.Parallel()` or `.Parallel()` anywhere in the line

This is pattern matching, not AST analysis. A commented-out `func BenchmarkFoo` would still count as present.

### Limitations and Trade-offs

- **Test status is file existence, not pass/fail.** A broken test still shows as `tested`.
- **`partial` counts as `tested`** in the health score, which can inflate it.
- **Gap severity is fixed by architectural layer.** There is no risk-based or complexity-based weighting.
- **Performance gap detection is text pattern matching** (`func Benchmark*`, `t.Parallel()`), not semantic analysis.
- **Confidence measures graph completeness, not coverage.** It starts at 1.0 and degrades by 0.9x per unresolved edge. A fully-traced graph with 0% test coverage still has confidence 1.0.
- **The Go AST scanner does not use `go/types`**, so it cannot resolve interface implementations beyond Wire bindings. Calls through non-Wire interfaces appear as missing edges (lowering confidence).
- **Feature ID inference** from `go test -json` output is a best-guess heuristic based on package paths and test names.

---

## Architecture

testreg follows hexagonal architecture with clean separation between domain logic and external adapters.

For details on how output is generated and what is heuristic vs deterministic, see [How testreg Works](#how-testreg-works) above.

```
cmd/                 Cobra command definitions (CLI entry points)
internal/
  domain/            Core types: Feature, Registry, Graph, Node, Edge, Audit
  ports/             Interface definitions (GraphBuilder, RegistryStore, TestScanner)
  app/               Use cases orchestrating domain logic
  adapters/          Implementations:
    go_ast_scanner   Go AST parsing (go/ast, go/parser) for call graph construction
    frontend_scanner Invokes ts-scanner.ts for TypeScript AST parsing
    route_parser     Chi/stdlib router file parsing for route discovery
    wire_resolver    Wire dependency injection binding resolution
    sqlc_mapper      SQLC config parsing to map generated methods to SQL sources
    annotation_parser  @testreg and @api annotation extraction
    yaml_store       Registry persistence (YAML read/write)
    *_scanner        Test file discovery (Go, Vitest, Playwright, Jest, Maestro)
    *_reporter       Output rendering (terminal, markdown, JSON)
ts-scanner.ts        TypeScript AST scanner (ts.createSourceFile)
```

### How the Graph Scanner Works

The Go AST scanner (`go_ast_scanner.go`) builds call graphs in four phases using only the standard library `go/ast` and `go/parser` packages -- no `go/types` or `golang.org/x/tools` required:

1. **Pre-resolution** -- Parse `sqlc.yaml` to build a map of generated Go methods to their SQL source files and line numbers.

2. **Route discovery** -- Parse the router file to extract HTTP method + path to handler function mappings. Also scans all Go files for `@api` annotations, which take precedence.

3. **Function discovery** -- Walk all `.go` files under the backend root. For each file: parse the AST, create `Node` entries for every function and method, build struct field type maps (used for resolving `h.service.Method()` chains), and resolve Wire bindings to map interfaces to their concrete implementations.

4. **Call graph extraction** -- Walk function bodies in the AST. For each call expression, resolve the target through field lookups, interface bindings, and receiver types. Add `Edge` entries to the graph. SQLC-generated method calls are replaced with `sql:QueryName` nodes pointing to the original `.sql` file.

The TypeScript scanner (`ts-scanner.ts`) runs as a Node.js subprocess and uses the TypeScript compiler API (`ts.createSourceFile`) to extract:
- React Router route definitions (path to component mappings)
- Component to hook dependencies (via import analysis)
- Hook to API service call chains (via call expression analysis)
- API service to HTTP endpoint mappings (URL string extraction)

Both scanners produce nodes and edges that are merged into a single unified `Graph`, enabling traces that span from a React route through to a SQL query.

---

## Supported Frameworks

### Backend

| Framework | Discovery Method |
|-----------|-----------------|
| Chi | Router file parsing (auto) |
| stdlib `net/http` | Router file parsing (auto) |
| Gin | `@api` annotations |
| Any Go HTTP framework | `@api` annotations |

### Frontend

| Framework | Discovery Method |
|-----------|-----------------|
| React Router | TypeScript AST scanning (auto) |
| TanStack Query | Hook call analysis (auto) |
| Custom hooks | Import chain resolution (auto) |

### Dependency Injection

| Tool | Integration |
|------|------------|
| Wire | Automatic binding resolution from `wire.go` |
| SQLC | Automatic method-to-SQL mapping from `sqlc.yaml` |

### Test Runners

| Runner | File Pattern | Platform |
|--------|-------------|----------|
| Go test | `*_test.go`, `*_e2e_test.go` | Backend |
| Vitest | `*.test.ts`, `*.test.tsx` | Web |
| Playwright | `*.spec.ts` | Web E2E |
| Jest | `__tests__/**` | Mobile |
| Maestro | `*.yaml` (in e2e dirs) | Mobile E2E |

---

## Advanced Workflows

### Sprint Planning

Run this at the start of each sprint to decide what to fix:

```bash
# 1. Save a baseline before starting work
testreg diff --save-snapshot sprint-3

# 2. See the priority-ranked list of gaps
testreg sprint --priority critical,high -n 20

# 3. Extract actionable gaps for parallel work
testreg gaps --priority critical --format actionable

# 4. Or feed gaps directly into AI subagents
testreg gaps --priority critical --format prompt > /tmp/gaps.md

# 5. After the sprint, measure improvement
testreg diff
```

### Composed Audit Queries

Flags compose freely. These replace the python scripts documented in the findings:

```bash
# "What are our worst critical features?"
testreg audit --priority critical --sort priority-score -n 10

# "How many features per tier are at target?"
testreg audit --summary

# "Which features are registered but unconfigured?"
testreg audit --unconfigured

# "Full refresh: rescan, then show me critical gaps sorted by impact"
testreg audit --rescan --priority critical --sort priority-score

# "Save the current state, work on tests, then diff"
testreg diff --save-snapshot before-fix
# ... write tests ...
testreg scan && testreg diff
```

### Dependency Graph Scripting

Use `--list-nodes` for scripting and automation:

```bash
# Get all service functions in a feature
testreg trace auth.login --list-nodes --kind service

# Cross-reference with test coverage
testreg trace auth.login --list-nodes | while read node; do
  echo "$node: $(grep -rl "$node" *_test.go 2>/dev/null | wc -l) test files"
done

# Validate trace integrity across all features
for feat in $(testreg audit --format json | jq -r '.[].FeatureID'); do
  testreg trace "$feat" --validate
done
```

### Automated Test Gap Fixing

Feed gap data into AI subagents for parallel test writing:

```bash
# Extract gaps for the 5 worst critical features
testreg gaps --priority critical -n 5 --format prompt > gaps.md

# Each gap includes:
#   - Source file and line number
#   - What test to write and where
#   - The @testreg annotation to add
#   - The test pattern to follow (table-driven, TestMain, etc.)
```

### Before/After Sprint Tracking

```bash
# Sprint start: save baseline
testreg diff --save-snapshot sprint-3-start

# Sprint end: compare
testreg diff --from sprint-3-start

# Compare any two points in time
testreg diff --from sprint-2 --to sprint-3-start

# JSON output for dashboards
testreg diff --format json | jq '.avg_delta'
```

---

## Common Mistakes

These are anti-patterns observed in real usage. Use the native commands instead.

| Mistake | Why it's wrong | Correct approach |
|---------|---------------|-----------------|
| `grep -r "@testreg auth" tests/` | Misses context, hits raw text | `testreg scan && testreg status --domain auth` |
| `find tests/ -name "*_test.go" \| wc -l` | Doesn't know which features are covered | `testreg status` |
| `diff <(find...) <(grep...)` for unannotated files | Fragile, breaks on structure changes | `testreg report` (check gaps section) |
| 80-line python coverage report script | Duplicates built-in logic | `testreg scan && testreg report` |
| `jq` pipeline to filter audit by priority | Fragile, version-dependent | `testreg audit --priority critical` |
| Python script to sort by priority score | Rebuilt every sprint planning | `testreg sprint` |
| Manual JSON comparison for before/after | Error-prone, no standard format | `testreg diff --save-snapshot` / `testreg diff` |
| Python script to extract gaps for subagents | Rebuilt every time | `testreg gaps --format actionable` |
| `testreg audit --format json \| python3 -c "..."` | Unnecessary indirection | Use native flags: `--priority`, `--sort`, `--summary` |
| Running `scan && audit` every time | Easy to forget `scan` | `testreg audit --rescan` |

---

## CI Integration

### Annotation enforcement

Fail the build if any test file is missing `@testreg` annotations:

```yaml
# .github/workflows/ci.yml
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

Post a coverage summary as a PR comment:

```yaml
- name: Post coverage summary
  run: |
    testreg scan
    testreg status > /tmp/coverage.txt
    gh pr comment ${{ github.event.pull_request.number }} \
      --body "$(cat /tmp/coverage.txt)"
```

### Registry freshness gate

Fail if `registry.json` is stale:

```yaml
- name: Verify registry is up to date
  run: |
    testreg scan
    git diff --exit-code docs/testing/registry/ || {
      echo "::error::Registry is stale. Run 'testreg scan' and commit."
      exit 1
    }
```

### Sprint progress tracking

Save snapshots in CI for automated progress tracking:

```yaml
- name: Save health snapshot
  if: github.ref == 'refs/heads/main'
  run: |
    testreg diff --save-snapshot "ci-$(date +%Y%m%d)"
    git add .testreg-cache/snapshots/
    git diff --cached --quiet || git commit -m "chore: update health snapshot"
```

---

## Contributing

Contributions are welcome. Please open an issue to discuss significant changes before submitting a pull request.

```bash
# Run tests
go test ./...

# Build
go build -o testreg .

# Install locally
go install .
```

The project uses only the Go standard library plus `cobra` and `yaml.v3` -- no heavy dependencies. The TypeScript scanner requires only `typescript` as a dev dependency.

---

## License

MIT
