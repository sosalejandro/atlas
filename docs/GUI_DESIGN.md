# testreg GUI — Web Dashboard Design

**Date:** 2026-03-31
**Status:** Design only
**Concept:** `testreg serve` spins up a local HTTP server (htmx + Go templates) that aggregates all testreg data into an interactive dashboard. One command, full visibility.

---

## The Problem

testreg produces 12+ types of output across different commands. A developer has to run each command separately, mentally stitch the results together, and remember what each number means. The data exists — it's just scattered across terminal outputs.

## The Solution

```bash
testreg serve
# → Scanning project...
# → Building graph...
# → Server running at http://localhost:6789
```

One command. Opens a browser. Everything visible at once.

---

## Architecture

### Why htmx + Go templates (not React/Vue)

1. **Zero JavaScript build step** — htmx is a single JS file, served from the Go binary
2. **Server-side rendering** — Go templates render HTML, htmx handles partial updates
3. **No frontend build** — no node_modules, no webpack, no npm. Pure Go binary.
4. **Embedded assets** — templates + htmx.js + CSS embedded via `go:embed`
5. **Same binary** — `testreg serve` is just another cobra command in the existing binary

### How data flows

```
Browser (htmx)                    Go Server (net/http)
┌─────────────────┐              ┌─────────────────────────┐
│ Dashboard page   │── GET / ───→│ Run all scans            │
│                  │←── HTML ────│ Render full dashboard    │
│                  │              │                         │
│ [Scan] button    │─ POST /scan→│ Run testreg scan         │
│ partial update   │←── <div> ──│ Return updated scan card │
│                  │              │                         │
│ Feature detail   │─ GET /feat/→│ Run audit + trace        │
│ slide panel      │←── <div> ──│ Return feature detail    │
│                  │              │                         │
│ Graph viz        │─ GET /graph→│ Run graph --format json  │
│ D3/Mermaid       │←── JSON ───│ Return graph data        │
└─────────────────┘              └─────────────────────────┘
```

testreg's existing use cases are the backend. The server is a thin HTTP layer over them — it calls the same `AuditFeatureUseCase`, `TraceFeatureUseCase`, etc. that the CLI uses.

---

## Pages and Layout

### Main Layout

```
┌─────────────────────────────────────────────────────────────────┐
│  testreg ● project-name                    [Scan] [Refresh]     │
├──────────┬──────────────────────────────────────────────────────┤
│          │                                                      │
│ sidebar  │  main content area                                   │
│          │                                                      │
│ Overview │  (changes based on sidebar selection)                │
│ Features │                                                      │
│ Sprint   │                                                      │
│ Graph    │                                                      │
│ Contract │                                                      │
│ Metrics  │                                                      │
│ Diff     │                                                      │
│ Report   │                                                      │
│          │                                                      │
├──────────┴──────────────────────────────────────────────────────┤
│  status bar: 184 features │ 771 tests │ 18% at target │ 10.2s  │
└─────────────────────────────────────────────────────────────────┘
```

### Page 1: Overview (default landing)

**What it shows:** The executive summary — everything you need in one glance.

```
┌─────────────────────────────────────────────────────────────┐
│  Health Summary                                              │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐       │
│  │ CRITICAL │ │   HIGH   │ │  MEDIUM  │ │   LOW    │       │
│  │  74%     │ │   19%    │ │    3%    │ │    0%    │       │
│  │ 17/23    │ │  14/75   │ │   2/65   │ │   0/21   │       │
│  └──────────┘ └──────────┘ └──────────┘ └──────────┘       │
│                                                              │
│  Coverage by Type              │  Performance Score          │
│  ┌────────────────────────┐    │  ┌────────────────────┐    │
│  │ Unit:    49% ████░░░░░ │    │  │ Benchmarks:  12%   │    │
│  │ Integ:   10% █░░░░░░░░ │    │  │ Race tests:  28%   │    │
│  │ E2E:     16% ██░░░░░░░ │    │  │ Overall:     18%   │    │
│  └────────────────────────┘    │  └────────────────────┘    │
│                                                              │
│  Top Gaps (sprint priorities)                                │
│  ┌──────────────────────────────────────────────────────┐   │
│  │ 3.00 critical  training.end-session         25%→100% │   │
│  │ 2.40 high      plans-nutritionist.meal...     0%→80% │   │
│  │ 2.40 high      shopping.generate-from-plan    0%→80% │   │
│  │ 2.40 high      billing.update-payment         0%→80% │   │
│  │ 2.40 high      client-analytics.meal-comp...  0%→80% │   │
│  └──────────────────────────────────────────────────────┘   │
│                                                              │
│  Domain Breakdown (clickable → goes to Features page)        │
│  ┌────────────────────────────────────────────┐              │
│  │ auth         ████████░░ 9/10  90%          │              │
│  │ meals        ██████░░░░ 8/12  67%          │              │
│  │ recipes      ██████░░░░ 6/10  60%          │              │
│  │ training     █████████░11/12  92%          │              │
│  │ billing      █████░░░░░ 9/14  64%          │              │
│  │ ...                                        │              │
│  └────────────────────────────────────────────┘              │
│                                                              │
│  Quick Contract Preview                                      │
│  ┌──────────────────────────────────────────────────────┐   │
│  │ Feature: [___________________] [Preview]              │   │
│  │                                                       │   │
│  │ training.record-exercise                              │   │
│  │   Entry: GRAPHQL Mutation.trainingLogSet              │   │
│  │   Layers: GraphQL → Resolver → Service → Repository   │   │
│  │   Coverage: 2/4 layers tested                         │   │
│  │   [Open full contract →]                              │   │
│  └──────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────┘
```

**Data source:** `audit --summary` + `status` + `sprint -n 5`

### Page 2: Features

**What it shows:** Searchable/filterable table of all features with drill-down.

```
┌─────────────────────────────────────────────────────────────┐
│  Features                                                    │
│  ┌─────────────────────────────────────────────────────┐    │
│  │ Search: [____________]  Priority: [All ▼]           │    │
│  │ Domain: [All ▼]   Health: [Below target ▼]          │    │
│  └─────────────────────────────────────────────────────┘    │
│                                                              │
│  ┌─────────────────────────────────────────────────────┐    │
│  │ Feature          │ Priority │ Health │ Perf │ Gaps  │    │
│  ├──────────────────┼──────────┼────────┼──────┼───────┤    │
│  │ ▶ auth.login     │ critical │  74%   │  32% │  13   │    │
│  │   auth.register  │ critical │  68%   │  15% │   8   │    │
│  │   meals.log      │ high     │  62%   │   5% │   5   │    │
│  │   ...            │          │        │      │       │    │
│  └─────────────────────────────────────────────────────┘    │
│                                                              │
│  ┌─── Feature Detail (slide-in panel on click) ──────────┐  │
│  │                                                        │  │
│  │  auth.login (critical) — Health: 74%                   │  │
│  │                                                        │  │
│  │  [Dependency Graph]  [Gaps]  [Tests]  [Perf]           │  │
│  │                                                        │  │
│  │  ┌─ Dependency Chain ──────────────────────────────┐  │  │
│  │  │ route:/login                         ✓ tested   │  │  │
│  │  │ └─ LoginPage                         ✓ tested   │  │  │
│  │  │    └─ useAuth                        ✓ tested   │  │  │
│  │  │       └─ authApi.login               ✓ tested   │  │  │
│  │  │          └─ POST /api/v1/auth/login  ✓ tested   │  │  │
│  │  │             └─ AuthHandler.Login     ✓ tested   │  │  │
│  │  │                └─ authService.Login  ✓ tested   │  │  │
│  │  │                   ├─ JWTGenerator... ✓ tested   │  │  │
│  │  │                   ├─ authRepo.Store  ✘ NO TEST  │  │  │
│  │  │                   └─ sql:GetUser...  ✘ NO TEST  │  │  │
│  │  └────────────────────────────────────────────────┘  │  │
│  │                                                        │  │
│  │  Gaps (13):                                            │  │
│  │   ✘ [CRITICAL] authRepo.StoreRefreshToken             │  │
│  │   ✘ [HIGH]     repositories.HashToken                  │  │
│  │   ✘ [MEDIUM]   sql:GetUserByEmail                      │  │
│  │                                                        │  │
│  └────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

**Data source:** `audit --all --format json` for the table, `audit <feature> --format json` for detail, `trace <feature> --format json` for the dependency chain.

**htmx behavior:** Clicking a row does `hx-get="/feature/auth.login"` which returns the detail panel HTML as a partial update. No page reload.

### Page 3: Graph

**What it shows:** Interactive dependency graph visualization for a selected feature.

```
┌─────────────────────────────────────────────────────────────┐
│  Dependency Graph                                            │
│  Feature: [auth.login ▼]                                    │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐   │
│  │                                                      │   │
│  │         ┌──────────┐                                 │   │
│  │         │route:/login│                               │   │
│  │         └─────┬─────┘                                │   │
│  │               │                                      │   │
│  │         ┌─────▼─────┐                                │   │
│  │         │ LoginPage  │                                │   │
│  │         └─────┬─────┘                                │   │
│  │               │                                      │   │
│  │         ┌─────▼──────┐                               │   │
│  │         │  useAuth    │                               │   │
│  │         └─────┬──────┘                               │   │
│  │               │                                      │   │
│  │         (... interactive D3 or Mermaid diagram ...)   │   │
│  │                                                      │   │
│  └──────────────────────────────────────────────────────┘   │
│                                                              │
│  Legend: 🔵 handler  🟢 service  🟡 repository  🟣 query   │
│          🔴 external  ⬜ component  ◻ hook                   │
│                                                              │
│  Node colors: green=tested  yellow=partial  red=untested    │
└─────────────────────────────────────────────────────────────┘
```

**Data source:** `graph <feature> --format json` for nodes/edges, rendered client-side with a lightweight JS graph library (D3-force, Cytoscape.js, or Mermaid).

This is the ONE page that needs client-side JavaScript beyond htmx — graph layout algorithms run in the browser. The graph data comes from the server as JSON.

### Page 4: Sprint Planning

**What it shows:** Priority-ranked gap list with grouping controls.

```
┌─────────────────────────────────────────────────────────────┐
│  Sprint Planning                                             │
│  ┌─────────────────────────────────────────────────────┐    │
│  │ Show: [Top 20 ▼]  Group by: [None ▼] [Type] [Domain]│    │
│  │ Priority: [All ▼]  Export: [JSON] [Prompt] [CSV]     │    │
│  └─────────────────────────────────────────────────────┘    │
│                                                              │
│  ┌─────────────────────────────────────────────────────┐    │
│  │ Score  Priority  Health  Target  Feature             │    │
│  │ ───────────────────────────────────────────────────  │    │
│  │  3.00  critical    25%    100%  training.end-session │    │
│  │  2.40  high         0%     80%  plans-nutri.meal... │    │
│  │  2.40  high         0%     80%  shopping.generate.. │    │
│  │  ...                                                 │    │
│  └─────────────────────────────────────────────────────┘    │
│                                                              │
│  By Fix Type:                                                │
│  ┌────────────────────────────────────────┐                  │
│  │ unit tests:        67 features ██████  │                  │
│  │ integration tests: 34 features ███     │                  │
│  │ e2e tests:         18 features ██      │                  │
│  │ benchmarks:        42 features ████    │                  │
│  │ race tests:        38 features ████    │                  │
│  └────────────────────────────────────────┘                  │
│                                                              │
│  [Export for AI Agent]  ← downloads --format prompt output   │
└─────────────────────────────────────────────────────────────┘
```

**Data source:** `sprint --format json`

### Page 5: Metrics & Quality Signals

**What it shows:** Test execution data — slow tests, flaky tests, race conditions, memory hogs, trends.

```
┌─────────────────────────────────────────────────────────────┐
│  Quality Signals                                             │
│                                                              │
│  ⚠ Prerequisites: Run tests with metrics collection first:  │
│    go test -json ./... > test-output.json                    │
│    testreg update --gotest test-output.json --with-metrics   │
│    npx vitest --reporter=json > vitest-output.json           │
│    testreg update --vitest vitest-output.json --with-metrics │
│                                                              │
│  [Import test results]  [Refresh]                            │
│                                                              │
│  Slowest Tests               │  Flaky Tests                  │
│  ┌────────────────────────┐  │  ┌────────────────────────┐  │
│  │ TestRecipeSync  4.2s   │  │  │ TestWSReconnect (3 ret)│  │
│  │ TestAuthE2E     3.8s   │  │  │ TestUploadPDF (2 ret)  │  │
│  │ TestBillingFlow 2.1s   │  │  │                        │  │
│  └────────────────────────┘  │  └────────────────────────┘  │
│                               │                              │
│  Race Conditions             │  Memory Hogs                  │
│  ┌────────────────────────┐  │  ┌────────────────────────┐  │
│  │ TestConcurrentAuth     │  │  │ BenchRecipeList 4.2MB  │  │
│  │ TestParallelSync       │  │  │ BenchUserSearch 2.8MB  │  │
│  └────────────────────────┘  │  └────────────────────────┘  │
│                                                              │
│  Health Trends (last 10 runs)                                │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  100% ┤                                         ●    │   │
│  │   80% ┤                              ●─────●───●     │   │
│  │   60% ┤                    ●────●───●                │   │
│  │   40% ┤          ●────●──●                           │   │
│  │   20% ┤  ●──●──●                                    │   │
│  │    0% ┼───┼───┼───┼───┼───┼───┼───┼───┼───┼        │   │
│  │       run1  2   3   4   5   6   7   8   9  10       │   │
│  └──────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────┘
```

**Data source:** `metrics --format json` for quality signals. Trend chart from snapshot history in `.testreg-cache/snapshots/`.

**Prerequisites notice:** This page shows a banner if no metrics history exists, with copy-paste commands to generate the data. The [Import test results] button triggers `testreg update` via htmx POST.

### Page 6: Diff / Progress Tracking

**What it shows:** Before/after comparison with snapshot management.

```
┌─────────────────────────────────────────────────────────────┐
│  Progress Tracking                                           │
│                                                              │
│  Snapshots:                                                  │
│  ┌────────────────────────────────────────────────────┐     │
│  │ sprint-3-start  2026-03-28  184 features  12%     │     │
│  │ sprint-3-end    2026-03-31  184 features  18%     │     │
│  │ latest          2026-03-31  184 features  18%     │     │
│  │                                                    │     │
│  │ [Save new snapshot: [___________] [Save]]          │     │
│  └────────────────────────────────────────────────────┘     │
│                                                              │
│  Compare: [sprint-3-start ▼] → [latest ▼]  [Compare]       │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  Improved (12 features):                              │   │
│  │   +100%  auth.login                      0% → 100%   │   │
│  │   + 74%  client-management.detail        0% →  74%   │   │
│  │   + 50%  recipes.create                 30% →  80%   │   │
│  │                                                       │   │
│  │  Regressed (2 features):                              │   │
│  │   - 10%  meals.log.create              80% →  70%    │   │
│  │                                                       │   │
│  │  Unchanged: 170 features                              │   │
│  │  Summary: +15.4% average health change                │   │
│  └──────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────┘
```

**Data source:** `diff --format json` + snapshot files from `.testreg-cache/snapshots/`

### Page 7: Diagnose (interactive)

**What it shows:** Enter a symptom, get a diagnosis with confidence scoring, multi-match results, and files to check.

```
┌─────────────────────────────────────────────────────────────┐
│  Diagnose                                                    │
│                                                              │
│  Feature: [auth.login ▼]                                    │
│  Symptom: [401 Unauthorized________]  [Diagnose]            │
│                                                              │
│  Quick symptoms:                                             │
│  [401] [403] [404] [500] [timeout] [connection refused]     │
│  [unique constraint] [json unmarshal] [CORS] [deadlock]     │
│  [sql: no rows] [TypeError] [hydration mismatch] [EOF]     │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  Best Match                                           │   │
│  │  Layer: backend-auth                                  │   │
│  │  Confidence: 70%  ████████░░░░                        │   │
│  │  Description: Authentication failure — request lacks  │   │
│  │               valid credentials or session expired    │   │
│  │  Check order: handler → service → external            │   │
│  │                                                       │   │
│  │  Also Matched:                                        │   │
│  │  55% backend-routing — endpoint or resource not found │   │
│  │                                                       │   │
│  │  Files to check (ordered by likelihood):              │   │
│  │   1. src/infrastructure/http/handlers/auth_handler.go │   │
│  │   2. src/application/services/auth_service.go         │   │
│  │   3. src/infrastructure/auth/jwt_generator.go         │   │
│  └──────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────┘
```

**Data source:** `diagnose <feature> --symptom "..." --json`

**Key elements:**
- **26 quick symptom chips** — clickable, organized in rows: HTTP codes, DB/serialization, frontend errors
- **Confidence bar** — visual indicator next to percentage. High (85-95%): almost certainly correct. Low (50-70%): directional hint.
- **Multi-match** — "Also Matched" shows secondary rules when a symptom hits multiple patterns (e.g., `"500: deadline exceeded"` matches both 500 and timeout)
- **Graph highlights** — dependency tree with ★ on diagnosed nodes

### Page 8: Contract View

**What it shows:** Full API contract for a feature, rendered as layered cards from entry point down to SQL. Each layer shows the function signature, file location, inputs/outputs, and (when `type_checking: true`) exact struct field tables. Test coverage is shown per layer at the bottom.

> **Note:** `type_checking: true` is experimental and buggy. The GUI should show a warning banner when this option is enabled, and struct field tables should be labeled as "(experimental)" to set expectations.

```
┌─────────────────────────────────────────────────────────────────────┐
│  Contract: [training.record-exercise ▼]    [Terminal] [JSON] [MD]  │
│                                                                     │
│  Entry: GRAPHQL Mutation.trainingLogSet                            │
│                                                                     │
│  ┌─── Layer 1: GraphQL API ─────────────────────────────────────┐  │
│  │  mutation { trainingLogSet(input: TrainingLogSetInput!): ... } │  │
│  │                                                               │  │
│  │  Input: TrainingLogSetInput                                   │  │
│  │  ┌──────────────┬──────────┬──────────┐                      │  │
│  │  │ Field        │ Type     │ Required │                      │  │
│  │  ├──────────────┼──────────┼──────────┤                      │  │
│  │  │ sessionId    │ UUID     │ yes      │                      │  │
│  │  │ exerciseId   │ UUID     │ yes      │                      │  │
│  │  │ reps         │ Int      │ no       │                      │  │
│  │  │ weight       │ Float    │ no       │                      │  │
│  │  └──────────────┴──────────┴──────────┘                      │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                                                     │
│  ┌─── Layer 2: Gateway Resolver ────────────────────────────────┐  │
│  │  mutationResolver.TrainingLogSet()                            │  │
│  │  File: src/cmd/graphql/resolvers/training.resolvers.go:60     │  │
│  │  Delegates to: r.Training.LogSet()                            │  │
│  │                                                               │  │
│  │  Input: generated.TrainingLogSetInput  (struct field table)   │  │
│  │  Output: *generated.TrainingExerciseSet                       │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                                                     │
│  ┌─── Layer 3: Service ─────────────────────────────────────────┐  │
│  │  SessionLifecycleService.LogSet()                             │  │
│  │  File: session_lifecycle_service.go:141                        │  │
│  │                                                               │  │
│  │  Calls:                                                       │  │
│  │  ├─ aggregates.NewExerciseSet()                               │  │
│  │  ├─ setRepo.Create()                                          │  │
│  │  └─ eventPublisher.PublishSetLogged()                         │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                                                     │
│  ┌─── Layer 4: Repository / SQL ────────────────────────────────┐  │
│  │  setRepo.Create()                                             │  │
│  │  File: exercise_set_repository.go:28                          │  │
│  │                                                               │  │
│  │  SQL: InsertExerciseSet                                       │  │
│  │  File: queries/exercise_sets.sql:12                           │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                                                     │
│  ┌─── Test Coverage ────────────────────────────────────────────┐  │
│  │  Layer 1 (GraphQL):     ✘ NO TEST                             │  │
│  │  Layer 2 (Resolver):    ✘ NO TEST                             │  │
│  │  Layer 3 (Service):     ✓ event_publisher_test.go             │  │
│  │  Layer 4 (Repository):  ✓ exercise_set_test.go                │  │
│  │                                                               │  │
│  │  Coverage: 2/4 layers │ Missing: resolver, GraphQL schema     │  │
│  └───────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────┘
```

**Visual design details:**
- Each layer card has a colored left border gradient indicating its kind:
  - Blue (#58a6ff) for handler/resolver layers
  - Green (#3fb950) for service layers
  - Yellow (#d29922) for repository layers
  - Purple (#bc8cff) for SQL/query layers
- Struct field tables appear inside layer cards when `type_checking: true` is enabled in the project config. The `TypeExtractor` (backed by go/types) resolves exact field names, types, and tags across packages.
- Format toggle buttons (Terminal / JSON / MD) switch between a styled terminal view, raw JSON output, and rendered markdown. The markdown view includes a "Copy as markdown" button for pasting contract summaries into PR descriptions.
- The feature selector dropdown is populated from the registry. Selecting a feature triggers `hx-get="/contract/{id}"` to fetch the layered card layout.

**Data source:** `testreg contract <feature> --format json`

**htmx behavior:** Feature selection does `hx-get="/contract/{featureID}"` which returns the full set of layer cards. Format toggle buttons swap the content area between pre-rendered views via `hx-get="/contract/{featureID}?format=terminal|json|md"`. The "Copy as markdown" button uses a small inline script to copy the markdown content to clipboard.

---

## Scan Controls (the "Play Button")

The top bar has a **[Scan]** button that opens a checklist of what to scan:

```
┌─ Scan Options ──────────────────────────┐
│                                          │
│  ☑ Discover test files (scan)            │
│  ☑ Build dependency graph (Go AST)       │
│  ☑ Build frontend graph (TypeScript)     │
│  ☑ Audit all features (health scores)    │
│  ☐ Enable type checking (go/types)       │
│    ⚠ Experimental — not recommended.     │
│    Buggy, uses ~4GB RAM, incomplete.     │
│  ☐ Import Go test results                │
│     Path: [test-output.json______]       │
│  ☐ Import Playwright results             │
│     Path: [test-results/__________]      │
│  ☐ Import Vitest results                 │
│     Path: [vitest-output.json_____]      │
│  ☐ Import coverage profile               │
│     Path: [cover.out_____________]       │
│                                          │
│  [▶ Run Selected]                        │
│                                          │
│  Progress:                               │
│  ████████░░░░░░░░ 52% — Building graph   │
│                                          │
└──────────────────────────────────────────┘
```

Each checkbox adds a step to the pipeline. The first 4 are default (always run). The import steps are optional — enabled when the user has test output files to enrich the report.

**Settings (accessible via ⚙ in header):** The settings page should expose `.testreg.yaml` configuration, including:
- `layer_rules` — custom directory name patterns for layer classification (e.g., `controladores` → handler, `dao` → repository). This allows non-English codebases and alternative naming conventions to get accurate health scores without renaming directories.
- All `graph.*` fields — backend_root, router_file, wire_file, fx_dir, etc.

**htmx behavior:** POST to `/scan/run` with the selected options. Server runs each step sequentially, streaming progress via SSE (Server-Sent Events). Each completed step pushes a partial HTML update to the progress bar and enables the corresponding data pages.

---

## Implementation Architecture

### File structure

```
cmd/
  serve.go                    # cobra command: testreg serve
internal/
  server/
    server.go                 # HTTP server setup, routes, middleware
    handlers.go               # Request handlers calling existing use cases
    templates/
      layout.html             # Base layout (sidebar, header, status bar)
      overview.html            # Overview page template
      features.html            # Features table + detail panel
      feature_detail.html      # Feature detail partial (htmx target)
      sprint.html              # Sprint planning page
      graph.html               # Graph visualization page
      metrics.html             # Quality signals page
      diff.html                # Diff/progress page
      diagnose.html            # Diagnose page
      contract.html            # Contract view page
      contract_layer.html      # Contract layer card partial (htmx target)
      scan_modal.html          # Scan options modal
    static/
      htmx.min.js             # htmx library (~14KB)
      style.css               # Minimal CSS (no framework, or Pico CSS ~10KB)
      graph.js                 # D3-force or Cytoscape.js for graph viz
```

All templates and static files are embedded via `//go:embed`:

```go
//go:embed templates/* static/*
var embeddedFS embed.FS
```

### Server implementation

```go
// cmd/serve.go
var serveCmd = &cobra.Command{
    Use:   "serve",
    Short: "Start the web dashboard",
    RunE: func(cmd *cobra.Command, args []string) error {
        srv := server.New(server.Config{
            ProjectRoot: resolvedProjectRoot(),
            RegistryDir: resolvedRegistryDir(),
            Port:        servePort,
        })
        return srv.ListenAndServe()
    },
}

// internal/server/server.go
type Server struct {
    config    Config
    store     ports.RegistryReader
    builder   ports.GraphBuilder
    auditUC   *app.AuditFeatureUseCase
    traceUC   *app.TraceFeatureUseCase
    scanUC    *app.ScanTestsUseCase
    // ... all existing use cases
}

func (s *Server) routes() http.Handler {
    mux := http.NewServeMux()
    
    // Pages (full HTML)
    mux.HandleFunc("GET /", s.handleOverview)
    mux.HandleFunc("GET /features", s.handleFeatures)
    mux.HandleFunc("GET /sprint", s.handleSprint)
    mux.HandleFunc("GET /graph", s.handleGraph)
    mux.HandleFunc("GET /metrics", s.handleMetrics)
    mux.HandleFunc("GET /diff", s.handleDiff)
    mux.HandleFunc("GET /diagnose", s.handleDiagnose)
    mux.HandleFunc("GET /contract", s.handleContract)
    
    // htmx partials (return HTML fragments)
    mux.HandleFunc("GET /feature/{id}", s.handleFeatureDetail)
    mux.HandleFunc("GET /contract/{id}", s.handleContractDetail)
    mux.HandleFunc("GET /graph/data/{id}", s.handleGraphData)
    mux.HandleFunc("POST /scan/run", s.handleScanRun)
    mux.HandleFunc("POST /diagnose/run", s.handleDiagnoseRun)
    mux.HandleFunc("POST /diff/compare", s.handleDiffCompare)
    mux.HandleFunc("POST /snapshot/save", s.handleSnapshotSave)
    
    // Static files
    mux.Handle("GET /static/", http.FileServer(http.FS(embeddedFS)))
    
    return mux
}
```

### Handler pattern

Each handler calls the existing use case and renders a template:

```go
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
    // Reuse existing use cases — same code as CLI
    config := s.graphConfig()
    
    results, _ := s.auditUC.ExecuteAll(s.config.RegistryDir, config)
    summary := buildAuditSummary(results)  // reuse from cmd/audit.go
    
    // Sprint top 5
    scored := computeSprintScores(results)
    
    // Render template
    s.render(w, "overview.html", map[string]any{
        "Summary":  summary,
        "Sprint":   scored[:min(5, len(scored))],
        "Domains":  groupByDomain(results),
    })
}
```

### htmx interactions

Feature detail panel (no page reload):
```html
<!-- features.html -->
<tr hx-get="/feature/{{.FeatureID}}" 
    hx-target="#detail-panel" 
    hx-swap="innerHTML"
    class="cursor-pointer hover:bg-gray-50">
  <td>{{.FeatureID}}</td>
  <td>{{.Priority}}</td>
  <td>{{.HealthScore | percent}}</td>
</tr>

<div id="detail-panel">
  <!-- Feature detail loads here via htmx -->
</div>
```

Scan with progress (SSE):
```html
<!-- scan_modal.html -->
<form hx-post="/scan/run" hx-target="#scan-progress" hx-swap="innerHTML">
  <input type="checkbox" name="scan" checked> Discover test files
  <input type="checkbox" name="graph" checked> Build dependency graph
  <input type="checkbox" name="audit" checked> Audit all features
  <button type="submit">▶ Run Selected</button>
</form>

<div id="scan-progress"></div>
```

---

## Data Enrichment: What Feeds the Dashboard

### Data the tool generates (always available)

| Source | What it provides | Used on page |
|--------|-----------------|-------------|
| `scan` | Test file inventory, annotation mappings | Overview, Features |
| `status` | Domain × platform coverage matrix | Overview |
| `audit --all` | Health scores, gaps, actions, perf scores | Overview, Features, Sprint |
| `trace <feature>` | Dependency chain tree | Feature detail, Graph |
| `graph <feature>` | Nodes + edges for visualization | Graph |
| `sprint` | Priority-scored ranking | Sprint |
| `diagnose` | Symptom → layer → files | Diagnose |
| `contract <feature>` | Layered API contract (schema → resolver → service → repo → SQL) | Contract, Overview (quick preview) |
| `diff` | Snapshot comparison | Diff |

### Data from external test runners (optional enrichment)

| Source | How to get it | What it adds |
|--------|--------------|-------------|
| `go test -json` | Run tests, save output | Pass/fail status, duration per test |
| `go test -coverprofile` | Run with coverage | Statement-level coverage (Phase 2) |
| `go test -bench` | Run benchmarks | Benchmark metrics (ns/op, B/op, allocs/op) |
| `go test -race` | Run with race detector | Race condition detection |
| Playwright JSON reporter | `npx playwright test --reporter=json` | E2E pass/fail, duration, retries, screenshots |
| Vitest JSON reporter | `npx vitest --reporter=json` | Unit test pass/fail, duration |
| Jest JSON reporter | `npx jest --json` | Unit test pass/fail, coverage |
| Maestro output | `maestro test` | E2E flow pass/fail |
| `coverage.py` JSON | `pytest --cov --cov-report=json` | Python statement coverage |

The Metrics page shows a banner when these haven't been imported yet, with the exact commands to run.

### Error/stacktrace enrichment (future, may need LLM)

When test results include failure output (stacktraces, assertion errors), the dashboard could:
1. **Display the stacktrace** alongside the dependency graph — highlighting which node in the graph corresponds to the failing file
2. **Cross-reference with `diagnose`** — auto-match the error message to a symptom rule
3. **Suggest root cause** — based on the symptom rule's layer ordering, highlight the most likely file

This is doable without LLM for structured errors (HTTP status codes, timeout messages, assertion failures). LLM integration would add natural language error interpretation — that's a separate feature, out of scope for v1 but the data pipeline supports it.

---

## Prerequisites and Workflow

### First-time assessment (no tests run yet)

```
1. testreg serve
2. Dashboard shows: overview with gaps, 0% coverage
3. User sees which features have no tests
4. User runs tests externally
5. User clicks [Import test results] with the output files
6. Dashboard refreshes with enriched data
```

### Ongoing development

```
1. testreg serve (keeps running)
2. Developer writes tests, adds @testreg annotations
3. Clicks [Scan] → dashboard updates with new test files
4. Runs tests → imports results → metrics page updates
5. Before sprint: checks Sprint page for priorities
6. After sprint: checks Diff page for progress
```

---

## Tech Choices

| Component | Choice | Why |
|-----------|--------|-----|
| Server | Go `net/http` (stdlib) | Already a dependency, no new imports |
| Templates | `html/template` (stdlib) | Already available, type-safe |
| Interactivity | htmx (~14KB) | Server-rendered partials, no build step |
| CSS | Pico CSS (~10KB) or hand-rolled | Minimal, classless, looks good by default |
| Graph viz | D3-force (~80KB) or Mermaid | Only page that needs client-side JS |
| Embedding | `go:embed` | Templates + static in the binary, single file deploy |
| Progress | SSE (`text/event-stream`) | Native browser support, no WebSocket needed |

**Total JS payload:** ~100KB (htmx + graph library). No npm, no build, no node_modules.

---

## What's NOT in v1

- Real-time file watching (auto-rescan on file save)
- LLM-powered error interpretation
- Multi-project dashboard (one project per server instance)
- User authentication (local tool, not shared server)
- Persistent database (reads from registry YAML + cache files each request)
- CI/CD integration dashboard (could POST results to the running server)

**Previously listed, now implemented:**
- ~~Auto-scaffolding from routes~~ — done via `testreg init --discover` (Chi, Echo, stdlib routers)
- ~~GraphQL support~~ — done via GraphQL resolver tracing (`Mutation.trainingLogSet` entry point resolution)
- ~~Python support~~ — done via `# @testreg` annotations and `test_*.py` file discovery

These are all natural extensions but not needed for v1. The core value is: **one command, full visibility, interactive exploration.**
