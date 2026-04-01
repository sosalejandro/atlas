# Contract Trace — Live API Contracts from Source Code

**Date:** 2026-03-31
**Status:** Design
**Depends on:** `GO_TYPES_PLAN.md` (go/types integration)

---

## The Vision

A frontend developer runs one command and sees **everything they need** to implement a feature:

```bash
testreg contract training.record-exercise
```

Output:

```
Feature: Record Exercise (training.record-exercise)
Entry:   GRAPHQL Mutation.trainingLogSet

═══════════════════════════════════════════════════════════════════════

  Layer 1: GraphQL API (what the frontend calls)
  ─────────────────────────────────────────────────

  mutation {
    trainingLogSet(input: TrainingLogSetInput!): TrainingExerciseSet!
  }

  Input: TrainingLogSetInput
  ┌──────────────────────┬──────────┬──────────┐
  │ Field                │ Type     │ Required │
  ├──────────────────────┼──────────┼──────────┤
  │ sessionId            │ UUID     │ yes      │
  │ exerciseId           │ UUID     │ yes      │
  │ reps                 │ Int      │ no       │
  │ weight               │ Float    │ no       │
  │ durationSeconds      │ Int      │ no       │
  │ distanceMeters       │ Float    │ no       │
  │ rpe                  │ Int      │ no       │
  │ setType              │ String   │ no       │
  │ clientGeneratedId    │ UUID     │ no       │
  └──────────────────────┴──────────┴──────────┘

  Response: TrainingExerciseSet
  ┌──────────────────────┬──────────┐
  │ Field                │ Type     │
  ├──────────────────────┼──────────┤
  │ id                   │ UUID     │
  │ exerciseId           │ UUID     │
  │ setType              │ String   │
  │ reps                 │ Int      │
  │ weight               │ Float    │
  │ durationSeconds      │ Int      │
  │ distanceMeters       │ Float    │
  │ rpe                  │ Int      │
  │ createdAt            │ DateTime │
  └──────────────────────┴──────────┘

═══════════════════════════════════════════════════════════════════════

  Layer 2: Gateway Resolver
  ─────────────────────────
  File: src/cmd/graphql/resolvers/training.resolvers.go:60
  
  func (r *mutationResolver) TrainingLogSet(
      ctx context.Context,
      input generated.TrainingLogSetInput,
  ) (*generated.TrainingExerciseSet, error)

  Transforms: generated.TrainingLogSetInput → internal LogSetInput
  Delegates to: r.Training.LogSet()
  Auth: injectTrainingAuth(ctx) — requires authenticated user

═══════════════════════════════════════════════════════════════════════

  Layer 3: Internal Resolver
  ──────────────────────────
  File: src/training/internal/infrastructure/graphql/resolver.go:289

  func (r *TrainingResolver) LogSet(
      ctx context.Context,
      input LogSetInput,
  ) (*SetDTO, error)

  Input: LogSetInput
  ┌──────────────────────┬──────────────────────┐
  │ Field                │ Type                 │
  ├──────────────────────┼──────────────────────┤
  │ SessionID            │ uuid.UUID            │
  │ ExerciseID           │ uuid.UUID            │
  │ SetType              │ *string              │
  │ Reps                 │ *int                 │
  │ Weight               │ *float64             │
  │ DurationSeconds      │ *int                 │
  │ DistanceMeters       │ *float64             │
  │ RPE                  │ *int                 │
  │ ClientGeneratedID    │ *uuid.UUID           │
  └──────────────────────┴──────────────────────┘

  Delegates to: r.sessionService.LogSet()

═══════════════════════════════════════════════════════════════════════

  Layer 4: Service
  ────────────────
  File: src/training/internal/application/services/session_lifecycle_service.go:141

  func (s *SessionLifecycleService) LogSet(
      ctx context.Context,
      input LogSetInput,
  ) (*aggregates.ExerciseSet, error)

  Business Rules:
  - Validates session exists and is active
  - Creates ExerciseSet domain aggregate
  - Persists via setRepo.Create
  - Publishes SetLogged event
  - Detects personal records via prService

  Calls:
  ├─ aggregates.NewExerciseSet()        → domain/aggregates/exercise_set.go:25
  ├─ setRepo.Create()                   → persistence/set_repository.go:30
  ├─ eventPublisher.PublishSetLogged()   → event_publisher.go:85
  └─ prService.DetectAndStore()          → personal_record_service.go:45

═══════════════════════════════════════════════════════════════════════

  Layer 5: Domain
  ───────────────
  File: src/training/internal/domain/aggregates/exercise_set.go

  type ExerciseSet struct {
      ID              uuid.UUID
      SessionID       uuid.UUID
      ExerciseID      uuid.UUID
      SetType         value_objects.SetType    // "working", "warmup", "dropset", "failure"
      Reps            *int16
      Weight          *float64
      DurationSeconds *int32
      DistanceMeters  *float64
      RPE             *int16
      CreatedAt       time.Time
  }

  Validation:
  - SetType must be valid enum value
  - At least one metric required (reps, weight, duration, or distance)

═══════════════════════════════════════════════════════════════════════

  Layer 6: Persistence
  ────────────────────
  Repository: SetRepository (interface)
  Implementation: PostgresSetRepository

  func (r *PostgresSetRepository) Create(
      ctx context.Context,
      set *aggregates.ExerciseSet,
  ) error

  SQL: sql:CreateExerciseSet
  File: src/training/internal/infrastructure/persistence/queries/sets.sql:15

═══════════════════════════════════════════════════════════════════════

  Test Coverage for this chain:
  ├─ ✓ exercise_set_test.go          (domain aggregate)
  ├─ ✓ workout_session_test.go       (session lifecycle)
  ├─ ✓ personal_record_service_test.go
  ├─ ✓ event_publisher_test.go
  ├─ ✘ NO TEST: resolver layer
  ├─ ✘ NO TEST: repository layer
  └─ ✓ training-session.yaml         (Maestro E2E)

═══════════════════════════════════════════════════════════════════════

  Implementation Checklist (for frontend):
  1. Call mutation trainingLogSet with TrainingLogSetInput
  2. sessionId must reference an active session (start one first)
  3. exerciseId must reference a valid exercise from the catalog
  4. At least one metric field required (reps, weight, duration, distance)
  5. setType defaults to "working" if omitted
  6. clientGeneratedId is optional — for offline sync deduplication
  7. Response includes the created set with server-generated ID and timestamp
```

---

## What This Requires

### Data Sources (what feeds each layer)

| Layer | Source | Requires go/types? |
|-------|--------|-------------------|
| **GraphQL schema** | `.graphqls` files (regex parse) | No |
| **Gateway resolver** | `*.resolvers.go` AST + type info | Yes — for parameter/return types |
| **Internal resolver** | `resolver.go` AST + type info | Yes — for struct field types |
| **Service** | `*_service.go` AST + type info | Yes — for method signatures |
| **Domain** | `aggregates/*.go` struct declarations | Yes — for full struct fields |
| **Persistence** | Repository interface + SQL queries | Partial — interface from types, SQL from SQLC |
| **Test coverage** | Existing audit system | No — already built |

### New Command: `testreg contract <feature-id>`

```go
var contractCmd = &cobra.Command{
    Use:   "contract <feature-id>",
    Short: "Show the full API contract and implementation chain for a feature",
    Long:  `Traces the dependency chain and extracts type information at each layer.
Shows input/output contracts, data transformations, and business rules
from the GraphQL schema down to the SQL query.

Requires graph.type_checking: true in .testreg.yaml for full type extraction.
Without it, shows the call chain without struct field details.`,
}
```

**Flags:**
- `--format terminal` (default) — colored layered output as shown above
- `--format json` — structured JSON for programmatic consumption
- `--format markdown` — documentation-ready markdown
- `--layer N` — show only layers 1-N (e.g., `--layer 2` for just the API contract)
- `--input-only` — show only input types (what to send)
- `--response-only` — show only response types (what comes back)

### New Domain Type: `ContractOutput`

```go
// domain/contract.go

type ContractOutput struct {
    FeatureID   string
    FeatureName string
    EntryPoint  string            // "GRAPHQL Mutation.trainingLogSet"
    Layers      []ContractLayer
    TestCoverage []ContractTestEntry
}

type ContractLayer struct {
    Number      int               // 1-6
    Name        string            // "GraphQL API", "Gateway Resolver", etc.
    File        string
    Line        int
    Signature   string            // full function signature
    InputType   *ContractType     // parameter struct
    OutputType  *ContractType     // return struct
    DelegateTo  string            // what it calls next
    Notes       []string          // auth requirements, business rules
}

type ContractType struct {
    Name   string
    Fields []ContractField
}

type ContractField struct {
    Name     string
    Type     string             // "UUID", "Int", "*float64", etc.
    Required bool
    Doc      string             // from Go doc comments
}

type ContractTestEntry struct {
    File     string
    Status   string             // "tested", "untested"
    Layer    string             // which layer it covers
}
```

### New Use Case: `ContractFeatureUseCase`

```go
// app/contract_feature.go

type ContractFeatureUseCase struct {
    traceUC  *TraceFeatureUseCase
    registry ports.RegistryReader
    // go/types info (nil if type_checking is false)
    typeInfo *TypeInfoProvider
}

func (uc *ContractFeatureUseCase) Execute(
    registryDir, featureID string,
    config ports.GraphConfig,
) (*domain.ContractOutput, error) {
    // 1. Trace the call chain (existing)
    traceOutput, _ := uc.traceUC.Execute(registryDir, featureID, config)
    
    // 2. For each node in the chain, extract type information
    layers := uc.extractLayers(traceOutput)
    
    // 3. Parse GraphQL schema for API-level types (if GRAPHQL entry)
    if isGraphQL(traceOutput) {
        layers = prepend(layers, uc.parseGraphQLSchema(config))
    }
    
    // 4. Extract test coverage for the chain
    coverage := uc.extractTestCoverage(traceOutput)
    
    return &domain.ContractOutput{
        Layers:       layers,
        TestCoverage: coverage,
    }, nil
}
```

### Type Extraction (the go/types part)

```go
// adapters/type_extractor.go

type TypeExtractor struct {
    packages []*packages.Package
}

// ExtractFunctionSignature returns the full signature with resolved types.
func (e *TypeExtractor) ExtractFunctionSignature(nodeID string) *FunctionSignature {
    // Find the function in loaded packages
    // Use TypesInfo to resolve parameter and return types
    // Recursively extract struct fields for composite types
}

// ExtractStructFields returns all fields of a named struct type.
func (e *TypeExtractor) ExtractStructFields(typeName string) []ContractField {
    // Look up the type in loaded packages
    // Walk struct fields, resolving nested types
    // Include doc comments from AST
}
```

### GraphQL Schema Parser (lightweight, no external deps)

```go
// adapters/graphql_schema_parser.go

type GraphQLSchemaParser struct{}

// ParseSchema reads .graphqls files and extracts type definitions.
func (p *GraphQLSchemaParser) ParseSchema(schemaDir string) (*GraphQLSchema, error) {
    // Regex-based parsing of .graphqls files
    // Extract: type definitions, input types, field lists
    // Map: GraphQL types to their fields with nullability
}

type GraphQLSchema struct {
    Types  map[string]GraphQLType   // "TrainingLogSetInput" → fields
    Queries    []GraphQLField
    Mutations  []GraphQLField
}

type GraphQLType struct {
    Name   string
    Fields []GraphQLField
}

type GraphQLField struct {
    Name     string
    Type     string    // "UUID!", "[String]", "Int"
    Required bool      // true if trailing "!"
}
```

The schema parser is regex-based because gqlgen schemas follow predictable patterns:
```
type TrainingLogSetInput {
  sessionId: UUID!
  exerciseId: UUID!
  reps: Int
}
```

No need for a full GraphQL parser — 50 lines of regex covers this.

---

## Implementation Phases

### Phase 1: Basic contract (without go/types)

What it shows: call chain + function signatures from AST + GraphQL schema types.

```bash
testreg contract training.record-exercise
# Shows: layers 1 (schema), 2 (resolver signature), test coverage
# Missing: struct field details at layers 3-6
```

**Files:**
- `internal/domain/contract.go` — types
- `internal/app/contract_feature.go` — use case
- `internal/adapters/graphql_schema_parser.go` — .graphqls parser
- `internal/adapters/contract_renderer.go` — terminal/json/markdown output
- `cmd/contract.go` — cobra command

**No new dependencies.** Uses existing AST data + regex schema parsing.

### Phase 2: Full contract (with go/types)

What it adds: struct field extraction at every layer, exact type resolution, validation rules from domain logic.

```bash
testreg contract training.record-exercise
# Shows: ALL layers with full struct fields, types, docs
```

**Files:**
- `internal/adapters/type_extractor.go` — go/types struct field extraction
- Modify `contract_feature.go` to use TypeExtractor when available

**Requires:** `golang.org/x/tools/go/packages` dependency (from GO_TYPES_PLAN.md).

### Phase 3: Contract diff

```bash
testreg contract training.record-exercise --diff main
# Shows: what changed in the contract since the main branch
# Fields added, removed, type changes
```

Compares two versions of the contract output. Useful in PRs — "this PR changes the TrainingLogSetInput by adding a `notes` field."

---

## Output Formats

### Terminal (default)

The layered output shown in "The Vision" section above. Colored, structured, with tables for struct fields.

### JSON

```json
{
  "feature_id": "training.record-exercise",
  "entry_point": "GRAPHQL Mutation.trainingLogSet",
  "layers": [
    {
      "number": 1,
      "name": "GraphQL API",
      "input_type": {
        "name": "TrainingLogSetInput",
        "fields": [
          {"name": "sessionId", "type": "UUID", "required": true},
          {"name": "exerciseId", "type": "UUID", "required": true},
          {"name": "reps", "type": "Int", "required": false}
        ]
      },
      "output_type": {
        "name": "TrainingExerciseSet",
        "fields": [...]
      }
    },
    {
      "number": 2,
      "name": "Gateway Resolver",
      "file": "src/cmd/graphql/resolvers/training.resolvers.go",
      "line": 60,
      "signature": "func (r *mutationResolver) TrainingLogSet(ctx context.Context, input generated.TrainingLogSetInput) (*generated.TrainingExerciseSet, error)",
      "delegate_to": "r.Training.LogSet"
    }
  ],
  "test_coverage": [
    {"file": "exercise_set_test.go", "status": "tested", "layer": "domain"},
    {"file": null, "status": "untested", "layer": "resolver"}
  ]
}
```

### Markdown

```markdown
# API Contract: Record Exercise

## Entry Point
`GRAPHQL Mutation.trainingLogSet`

## Input
| Field | Type | Required |
|-------|------|----------|
| sessionId | UUID | yes |
| exerciseId | UUID | yes |
| reps | Int | no |
...

## Response
| Field | Type |
|-------|------|
| id | UUID |
...

## Call Chain
1. `mutationResolver.TrainingLogSet` → training.resolvers.go:60
2. `TrainingResolver.LogSet` → resolver.go:289
3. `SessionLifecycleService.LogSet` → session_lifecycle_service.go:141
...
```

This format is directly usable in PRs, Notion, or Confluence.

---

## Config

```yaml
# .testreg.yaml
graph:
  type_checking: true          # required for full struct field extraction
  graphql:
    schema_dirs:               # where to find .graphqls files
      - src/training/pkg/schema
      - src/supplement/pkg/schema
      - src/nutrition/pkg/schema
```

Without `graphql.schema_dirs`, the contract command skips Layer 1 (GraphQL schema) and starts from Layer 2 (resolver).

Without `type_checking`, the contract shows call chain and function signatures but not struct field tables.

---

## REST Contracts

This works for REST features too, not just GraphQL:

```bash
testreg contract auth.login
```

```
Feature: Login (auth.login)
Entry:   POST /api/v1/auth/login

  Layer 1: HTTP API
  ─────────────────
  POST /api/v1/auth/login
  Content-Type: application/json

  Request Body: LoginRequest
  ┌──────────────────────┬──────────┬──────────┐
  │ Field                │ Type     │ Required │
  ├──────────────────────┼──────────┼──────────┤
  │ email                │ string   │ yes      │
  │ password             │ string   │ yes      │
  └──────────────────────┴──────────┴──────────┘

  Response: LoginResponse
  ┌──────────────────────┬──────────┐
  │ Field                │ Type     │
  ├──────────────────────┼──────────┤
  │ token                │ string   │
  │ user                 │ User     │
  │ refreshToken         │ string   │
  └──────────────────────┴──────────┘

  Layer 2: Handler
  ────────────────
  File: src/infrastructure/http/handlers/auth_handler.go:249
  ...
```

For REST, the request/response types come from the handler's `json.Decode`/`c.Bind` call (input) and `json.Encode`/`c.JSON` call (output). go/types resolves which struct is being bound.

---

## Why This Matters

**Today:** A frontend developer asks "how do I call the training log set endpoint?" The answer is: read the GraphQL schema, then read the resolver, then read the service, then read the domain types, then read the SQL. 5 files across 3 packages.

**With contract:** `testreg contract training.record-exercise` — one command, complete answer. The contract is always current because it's derived from source code, not maintained documentation.

**For AI agents:** `testreg contract training.record-exercise --format json` produces structured data that an AI agent can consume to generate frontend code, write tests, or validate implementations. The `--format prompt` variant could generate implementation instructions directly.

**For PRs:** `testreg contract training.record-exercise --format markdown` generates documentation that goes directly into a PR description.

---

## Estimated Scope

### Phase 1 (without go/types): ~400 lines

| File | Lines | What |
|------|-------|------|
| `domain/contract.go` | ~50 | Types |
| `app/contract_feature.go` | ~100 | Use case |
| `adapters/graphql_schema_parser.go` | ~80 | .graphqls regex parser |
| `adapters/contract_renderer.go` | ~120 | Terminal/JSON/Markdown output |
| `cmd/contract.go` | ~50 | Cobra command + flags |
| Tests | ~100 | Schema parser + entry point |

### Phase 2 (with go/types): ~200 additional lines

| File | Lines | What |
|------|-------|------|
| `adapters/type_extractor.go` | ~150 | Struct field extraction via go/types |
| Modify `contract_feature.go` | ~50 | Wire type extractor into layers |

**Total: ~600 lines for full implementation.** No architectural changes — builds on existing trace infrastructure.
