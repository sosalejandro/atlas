# Google Stitch Prompts — testreg Dashboard

Each prompt is self-contained. Use them in order. Start a **new chat** where marked.

---

## Prompt 1: Design System Foundation

**Context:** New chat. This establishes the base that all subsequent prompts build on.

```
Design a design system for a developer productivity dashboard called "testreg."

PURPOSE: Test coverage analysis and dependency graph visualization for Go + TypeScript monorepos. Used by backend and frontend engineers to find test gaps, plan sprints, and trace full-stack call chains.

AUDIENCE: Software developers on macOS, Linux, Windows. Used daily. Must feel like a tool they already know (VS Code sidebar, GitHub data tables, Linear's polish).

THEME: Dark mode primary, light mode supported. Toggle in header.

DARK PALETTE:
  Background: #0d1117 (main), #161b22 (cards/panels), #21262d (hover/selected), #30363d (elevated/modals)
  Text: #e6edf3 (primary), #8b949e (secondary/labels), #484f58 (muted/disabled)
  Borders: #30363d (default), #21262d (subtle)
  Accent: #58a6ff (links, primary buttons)

LIGHT PALETTE:
  Background: #ffffff, #f6f8fa, #eaeef2, #ffffff
  Text: #1f2328, #656d76, #afb8c1
  Borders: #d0d7de, #eaeef2

SEMANTIC COLORS (same both modes):
  Green: #3fb950 (healthy, ≥80%, tested, improved)
  Yellow: #d29922 (warning, 50-79%, partial)
  Red: #f85149 (critical, <50%, untested, regressed)
  Blue: #58a6ff (informational, links, handler nodes)
  Purple: #bc8cff (query nodes)

TYPOGRAPHY:
  UI: Inter or system font stack (-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto)
  Code/numbers: JetBrains Mono or ui-monospace, "Cascadia Code", monospace
  Sizes: 11px labels, 13px body, 14px table, 16px section headers, 20px page titles, 24px hero numbers
  Numbers always use tabular-nums for alignment

SPACING: 4px base unit. Padding: 8px compact, 12px default, 16px cards, 24px sections.

RADIUS: 2px inputs, 4px badges/chips, 8px cards, 12px pills/health badges

COMPONENTS TO DESIGN:
  1. Health badge (pill shape, colored by tier: green/yellow/red, shows percentage)
  2. Severity badge (uppercase pill: CRITICAL red, HIGH yellow, MEDIUM blue, LOW gray)
  3. Progress bar (8px height, colored fill, percentage label)
  4. Priority dot (8px circle: red=critical, yellow=high, blue=medium, gray=low)
  5. Data table row (40px height, hover highlight, selected state with left accent border)
  6. Card (bg-secondary, 1px border, 8px radius, 16px padding, uppercase header)
  7. Tree node (indented with connector lines, kind-colored dot, status icon, file reference)
  8. Donut chart (64px, health percentage in center, colored arc)

Please generate a component library sheet showing all 8 components in both dark and light mode.
```

---

## Prompt 2: App Shell Layout

**Context:** Same chat as Prompt 1. References the design system.

```
Using the design system from above, design the application shell layout.

STRUCTURE:
  - Fixed header (48px height): Logo (circle icon ◉) + "testreg" text left, project name center-left, [Scan] primary button + [☀/🌙] theme toggle + [⚙] settings right
  - Fixed sidebar (200px, left): 8 navigation items with icons + labels. Active item has left 3px accent border + bg-tertiary. Collapsible to 48px (icons only) via toggle at bottom.
  - Main content area: fills remaining space, independently scrollable
  - Fixed status bar (32px, bottom): shows "184 features • 771 tests • 18% at target • Last scan: 10.2s" in monospace, subtle top border

SIDEBAR ITEMS (with icons):
  1. Overview (dashboard grid icon)
  2. Features (list icon)
  3. Graph (node-graph icon)
  4. Sprint (flag/target icon)
  5. Contract (layers/stack icon)
  6. Metrics (chart icon)
  7. Diff (git-diff icon)
  8. Diagnose (stethoscope/debug icon)

RESPONSIVE:
  Desktop (>1200px): Full sidebar visible
  Tablet (768-1200px): Sidebar collapsed to 48px icons-only by default
  Mobile (<768px): No sidebar, bottom tab bar with 4 main items (Overview, Features, Sprint, Graph), overflow menu for rest

Show the shell in desktop, tablet, and mobile breakpoints. Dark mode. Empty content area with a subtle "Select a page" placeholder.
```

---

## Prompt 3: Overview Page

**Context:** Same chat. This is the landing page.

```
Design the Overview page that fills the main content area of the shell.

This is the executive dashboard — everything a developer needs at a glance.

LAYOUT: 2-column grid on desktop, single column on mobile. 5 card sections.

CARD 1 — Health by Priority (full width):
  4 donut charts side by side, each showing:
    - Priority label (CRITICAL / HIGH / MEDIUM / LOW)
    - 64px donut chart with percentage in center
    - "17/23 at target" below
  Below the 4 donuts: full-width progress bar showing overall: "33 of 184 features at target (18%)"

CARD 2 — Coverage Matrix (left column):
  3 horizontal bars:
    - "Unit    ████░░░░░░  49%" (yellow fill — between 50-79 would be yellow, below 50 is red)
    - "Integ   █░░░░░░░░░  10%" (red fill)
    - "E2E     ██░░░░░░░░  16%" (red fill)

CARD 3 — Performance Score (right column):
  3 horizontal bars:
    - "Benchmarks  █░░░░░  12%" (red)
    - "Race tests  ██░░░░  28%" (red)
    - "Overall     █░░░░░  18%" (red)

CARD 4 — Top Sprint Priorities (full width):
  5-row table showing:
    Score | Priority dot + label | Feature name | Health bar (current → target)
  Example rows:
    3.00  ● critical  training.end-session       ██░░░░░ 25% → 100%
    2.40  ● high      plans-nutri.meal-option    ░░░░░░░  0% →  80%
  Footer link: "→ View all sprint priorities"

CARD 5 — Domains (full width):
  2-column grid of domain rows, each showing:
    Domain name + progress bar + "9/10" fraction
  Examples:
    auth         ████████░░ 9/10
    training     █████████░ 11/12
    meals        ██████░░░░ 8/12
    billing      █████░░░░░ 9/14
  Clickable — each domain links to Features page filtered by that domain.

Use real data from the examples above. Dark mode.
```

---

## Prompt 4: Features Page — Table

**Context:** New chat. Start fresh — this is a complex page.

```
Design a features data table page for a developer dashboard called "testreg."

Dark mode. Background #0d1117. Cards #161b22. Text #e6edf3 primary, #8b949e secondary.
Font: Inter for UI, JetBrains Mono for numbers. Accent: #58a6ff.

FILTER BAR (top, inside a subtle card):
  - Search input with magnifying glass icon: "Search features..."
  - 3 dropdown filters: Priority [All ▼], Domain [All ▼], Health [Below target ▼]
  - Sort dropdown: [Health ↑ ▼]
  - Result count: "184 results" right-aligned, muted

TABLE:
  Columns: Feature | Priority | Health | Perf | Gaps | E2E | Unit
  
  Feature: left-aligned text, 13px
  Priority: colored dot (8px circle) + text (critical=red, high=yellow, medium=blue, low=gray)
  Health: percentage in colored pill badge (green ≥80, yellow 50-79, red <50)
  Perf: percentage in colored pill badge (same thresholds)
  Gaps: number, right-aligned, monospace
  E2E: icon ✓ green or ✘ red
  Unit: icon ✓ green or ✘ red

  Row height: 40px
  Hover: bg #21262d
  Selected: bg #21262d + left border 3px #58a6ff

  Example data (15 rows):
    auth.login          ● critical   74%   32%   13   ✓   ✓
    auth.register       ● critical   68%   15%    8   ✓   ✓
    auth.token-refresh  ● critical  100%    0%    0   ✓   ✓
    meals.log           ● high       62%    5%    5   ✓   ✓
    meals.history       ● high      100%   27%    8   ✘   ✓
    recipes.create      ● high       45%   16%   12   ✓   ✓
    recipes.search      ● medium     88%    0%    2   ✓   ✓
    billing.subscription● critical  100%    2%   21   ✘   ✓
    billing.update-pay  ● high        0%    0%    8   ✘   ✘
    training.session    ● high       92%   40%    1   ✓   ✓
    training.end-session● critical   25%    0%   15   ✘   ✘
    settings.account    ● high        0%    0%    6   ✘   ✘
    shopping.list       ● high       78%    5%    3   ✓   ✓
    recovery.heatmap    ● medium     33%    0%    7   ✓   ✘
    plans-nutri.create  ● critical  100%   16%    2   ✘   ✓

  Pagination: "◀ 1 2 3 ... 8 ▶" bottom-right

Show the table with auth.login selected (highlighted row).
```

---

## Prompt 5: Features Page — Detail Panel

**Context:** Same chat as Prompt 4. This slides in from the right when a row is clicked.

```
Design a detail panel that slides in from the right side of the features table (from the previous design).

Panel width: 420px. Background: #161b22. Left border: 1px #30363d. 
Slide animation: 200ms ease-out from right edge.

HEADER:
  Feature name: "auth.login" (16px bold, #e6edf3)
  Priority badge: "CRITICAL" (red pill)
  Health: "74%" (yellow pill, large)
  Close button: X icon top-right

TABS (below header):
  4 tabs: [Graph] [Gaps] [Tests] [Performance ⚡]
  Active tab: bottom border 2px #58a6ff, text #e6edf3
  Inactive: text #8b949e

TAB 1 — Graph (default, selected):
  Mini dependency tree showing the call chain:
  
  🔵 route:/login                         ✓
  └─ 🔵 LoginPage                         ✓
     └─ 🟢 useAuth                        ✓
        └─ ⚪ authApi.login                ✓
           └─ 🔵 POST /api/v1/auth/login   ✓
              └─ 🔵 AuthHandler.Login       ✓
                 └─ 🟢 authService.Login    ✓
                    ├─ 🟢 Argon2Hasher      ✓
                    ├─ 🟢 JWTGenerator      ✓
                    │  ├─ GenerateAccess     ✓
                    │  └─ GenerateRefresh    ✓
                    ├─ 🟡 authRepo.Store     ✘
                    ├─ 🟡 hashToken          ✘
                    └─ 🟣 sql:GetUserByEmail  ✘

  ✓ = green text, ✘ = red text
  Kind dots: colored circles (blue=handler/component, green=service/hook, yellow=repo, purple=query)
  Lines: thin connector lines in #30363d

  Footer: "Confidence: 100% | Nodes: 16 | Depth: 8"
  Link: "[Open full graph →]" in accent blue

TAB 2 — Gaps:
  List of gap items:
    ✘ [CRITICAL] authRepo.StoreRefreshToken
      no unit test
      → auth_repository.go:329
    
    ✘ [HIGH] repositories.HashToken
      no unit test
      → auth_repository.go:90

TAB 3 — Tests:
  List of test files covering this feature:
    ✓ auth_service_test.go (unit, backend)
    ✓ auth_test.go (integration, backend)
    ✓ auth.spec.ts (e2e, web)

TAB 4 — Performance ⚡:
  Benchmark coverage: 1/1 (100%) ████████████
  Race test coverage: 0/1 (0%)   ░░░░░░░░░░░░
  Performance Score: 60%
  
  Gaps:
    ✘ [MEDIUM] No t.Parallel() in auth_service_test.go

LAYER COVERAGE (always visible at bottom of panel, below tabs):
  Handler:    ████████████ 100%
  Service:    █████████░░░  75%
  Repository: ░░░░░░░░░░░░   0%
  Query:      ░░░░░░░░░░░░   0%

Show the panel open alongside the table, with the Graph tab selected. 
The table should be visible but slightly dimmed behind the panel on mobile.
```

---

## Prompt 6: Graph Page

**Context:** New chat. This is the most visual page.

```
Design a full-page interactive dependency graph visualization for a developer tool.

Dark mode. Background: #0d1117. Node cards: #161b22 with colored borders.

CONTROLS BAR (top):
  Feature selector: dropdown [auth.login ▼]
  Layout toggle: radio pills [Tree | Force | Horizontal]
  Buttons: [Fit to view] [Reset zoom] [Export ▼ (PNG, SVG, Mermaid)]

GRAPH AREA (full remaining height):
  Tree layout, top-to-bottom. Show this exact dependency chain:

  Level 0: "route:/login" — cyan border (component)
  Level 1: "LoginPage" — cyan border
  Level 2: "useAuth" — green border (hook)
  Level 3: "authApi.login" — white border (endpoint)
  Level 4: "POST /api/v1/auth/login" — blue border (handler)
  Level 5: "AuthHandler.Login" — blue border
  Level 6: "authService.Login" — green border (service)
  Level 7 (branches):
    - "Argon2Hasher.Verify" — green border
    - "JWTGenerator.GenerateTokenPair" — green border (has 2 children below)
    - "authRepo.StoreRefreshToken" — yellow border, DASHED (untested)
    - "repositories.HashToken" — yellow border, DASHED
    - "sql:GetUserByEmail" — purple border, DOTTED (untested)

  NODE DESIGN:
    Rounded rectangle, ~160px × 36px
    Background: #161b22
    Border: 2px, colored by kind
    Border-style: solid = tested, dashed = partial, dotted = untested
    Text: 12px monospace, #e6edf3
    Selected node: border 3px, subtle glow shadow

  EDGES:
    Straight lines with small arrowheads
    Color: #30363d default, #58a6ff when highlighted
    Width: 1px default, 2px highlighted

  HOVER STATE (on a node):
    Tooltip showing:
      "authService.Login
       File: src/application/services/auth_service.go:172
       Kind: service
       Status: tested (3 test files)
       Coverage: 73% statements"

LEGEND BAR (bottom):
  Kind: 🔵 handler  🟢 service  🟡 repository  🟣 query  🔴 external  ⬜ component  ◻ hook
  Border: ── tested  - - partial  ··· untested

FOOTER INFO:
  "Confidence: 100% | Nodes: 16 | Depth: 8 | Feature: auth.login (critical)"

The graph should feel like a polished code visualization tool (think VS Code call hierarchy 
or Chrome DevTools performance waterfall). Clean, precise, no decorative elements.
```

---

## Prompt 7: Sprint & Diff Pages

**Context:** New chat. Two simpler data pages.

```
Design two pages for a developer dashboard. Dark mode (#0d1117 bg, #161b22 cards).

PAGE 1 — SPRINT PLANNING:

Controls: Show count [20 ▼], Priority filter [All ▼]
Group-by toggle: radio pills (None | Fix Type | Domain)
Export buttons: [JSON] [Prompt for AI] [Copy]

Main table:
  Score | Priority (colored dot + label) | Feature name | Health bar (current → target)
  
  3.00  ● critical  training.end-session      ██░░░░░░░ 25% → 100%
  2.40  ● high      plans-nutri.meal-option   ░░░░░░░░░  0% →  80%
  2.40  ● high      shopping.generate         ░░░░░░░░░  0% →  80%
  2.40  ● high      billing.update-payment    ░░░░░░░░░  0% →  80%

  Health bars show TWO segments: filled (current) and ghost (target).
  Gap between current and target is the visual representation of work needed.

Below table — "By Fix Type" section:
  Horizontal stacked bar chart:
    unit tests:          ████████████████████░░░░░  67 features
    integration tests:   ████████░░░░░░░░░░░░░░░░░  34 features
    e2e tests:           ████░░░░░░░░░░░░░░░░░░░░░  18 features
    benchmarks:          ██████████░░░░░░░░░░░░░░░  42 features
    race tests:          █████████░░░░░░░░░░░░░░░░  38 features


PAGE 2 — DIFF / PROGRESS TRACKING:

Top section — Snapshots:
  Table: Name | Date | Features | Health
    sprint-3-start  Mar 28  184  12%
    sprint-3-end    Mar 31  184  18%
    latest          Mar 31  184  18%
  
  New snapshot input: [name field] [💾 Save] button

Compare controls: [sprint-3-start ▼] → [latest ▼] [Compare]

Results:
  "Improved (12 features):" header in green
  Each row: "+100%  auth.login  ░░░░░░░░→████████ 0% → 100%"
  Mini before→after bar per feature: left portion gray (before), right portion green (after)

  "Regressed (2 features):" header in red
  Each row: "- 10%  meals.log.create  ████████→██████░░ 80% → 70%"

  "Unchanged: 170 features" in muted text

  Summary card: "Average change: +15.4% ↑" with large green number

Show both pages side by side if possible, otherwise stacked.
```

---

## Prompt 8: Metrics & Diagnose Pages

**Context:** Same chat as Prompt 7. Two more pages.

```
Design two more pages for the same dashboard.

PAGE 1 — METRICS / QUALITY SIGNALS:

Prerequisites banner (top, dismissible):
  Info icon ⓘ + "Import test results to see quality signals:"
  Code block: "$ go test -json ./... > test-output.json"
  [Copy] [Dismiss] buttons

4-card grid (2×2):
  Card 1 — Slowest Tests:
    Ranked list, each row: test name (truncated) + duration right-aligned
    TestRecipeSync         4.2s
    TestAuthE2E            3.8s
    TestBillingFlow        2.1s
    TestMealLog            1.9s

  Card 2 — Flaky Tests:
    TestWSReconnect        3 retries ⚠
    TestUploadPDF          2 retries ⚠
    (or "No flaky tests ✓" in green if empty)

  Card 3 — Race Conditions:
    ⚠ TestConcurrentAuth
    ⚠ TestParallelSync
    (warning triangle icon + test name)

  Card 4 — Memory Hogs:
    BenchRecipeList        4.2 MB/op
    BenchUserSearch        2.8 MB/op
    BenchBillingCalc       1.1 MB/op

Health Trend chart (full width below cards):
  SVG line chart, dark background
  X axis: dates (Mar 1, 5, 10, 15, 20, 25, 28, 30, 31)
  Y axis: 0% to 100%
  3 lines: 
    Solid white line: overall health
    Dashed green line: critical tier
    Dotted yellow line: high tier
  Dots on data points, hover shows tooltip with exact values
  Legend below chart


PAGE 2 — DIAGNOSE:

Input section (card):
  Feature dropdown: [auth.login ▼]
  Symptom text input: [401 Unauthorized_____] with [🔍 Diagnose] button
  Quick symptom chips below input (3 rows of clickable pills):
    Row 1 (HTTP): [401] [403] [404] [500] [409] [422] [429] [502] [503]
    Row 2 (Data/Infra): [timeout] [connection refused] [unique constraint]
                        [deadlock] [json unmarshal] [CORS] [EOF] [TLS]
    Row 3 (Frontend): [TypeError] [selector not found] [hydration mismatch]
  Clicking a chip fills the input field

Results (appears after submitting):

  Best Match card (primary, prominent):
    Layer: backend-auth
    Confidence: 70% with a colored progress bar (70% fill, accent blue)
    "Authentication failure — request lacks valid credentials or session has expired"
    Check order: handler → service → external

  Also Matched section (secondary, smaller, below best match):
    List of additional matching rules, each showing:
      "55% backend-routing — Not found: endpoint or resource missing"
    (Only visible when multiple rules match the same symptom)

  Files to Check (ordered list, each in a subtle card row):
    1. src/infrastructure/http/handlers/auth_handler.go
       └─ AuthHandler.Login (line 249)
    2. src/application/services/auth_service.go
       └─ authService.Login (line 172)
    3. src/infrastructure/auth/jwt_generator.go
       └─ JWTGenerator.GenerateTokenPair (line 70)

  Graph with highlights (below files):
    Same mini tree as the feature detail panel, but nodes mentioned in 
    "Files to Check" have a star ★ icon and a highlighted border (#58a6ff).
    Other nodes are dimmed (#484f58 text).
```

---

## Prompt 9: Scan Modal

**Context:** New chat. Overlay component.

```
Design a modal/overlay for triggering a project scan in a dark developer dashboard.

Dark mode. Modal background: #161b22. Overlay: rgba(0,0,0,0.6).
Modal width: 520px, centered, border-radius 12px, subtle shadow.

HEADER: "Scan & Import" with X close button

SECTION 1 — "Always Run" (3 checkboxes, all checked, slightly muted to show they're mandatory):
  ☑ Discover test files (Go, TypeScript, Python, Playwright, Maestro)
  ☑ Build dependency graph (Go AST + TypeScript)
  ☑ Audit all features (health scores, gaps, performance)

SECTION 1b — "Advanced" (collapsed by default, toggleable):
  ☐ Enable type checking (go/types) — ⚠ Experimental, buggy, ~4GB RAM
  Layer rules: link to settings page for custom layer_rules configuration
    (e.g., "controladores" → handler, "dao" → repository)

SECTION 2 — "Import Test Results" (optional, unchecked by default):
  Each option is a checkbox + file path input + helper command:

  ☐ Go test results
    [test-output.json___________] [Browse]
    $ go test -json ./... > test-output.json

  ☐ Playwright results
    [test-results/_______________] [Browse]
    $ npx playwright test --reporter=json

  ☐ Vitest results
    [vitest-output.json__________] [Browse]
    $ npx vitest --reporter=json

  ☐ Coverage profile
    [cover.out___________________] [Browse]
    $ go test -coverprofile=cover.out ./...

  Helper commands in monospace, #8b949e color, can be clicked to copy

SECTION 3 — Progress (visible during/after scan):
  Step indicators:
    ✓ Scanning test files...                    0.5s    (green check)
    ✓ Building Go AST graph...                  8.2s    (green check)
    ● Auditing 184 features...  ████████░░ 67%  5.1s    (blue spinner)
    ○ Importing test results...                 pending (gray circle)

  Total elapsed: 13.8s

FOOTER: [Cancel] secondary button + [▶ Run Selected] primary button (accent blue)

Show the modal in "running" state — first 2 steps complete, third in progress.
The progress bar inside step 3 is the same accent blue (#58a6ff).
```

---

## Prompt 10: Contract Page

**Context:** New chat. This is the layered API contract view.

```
Design a full-page "Contract View" for a developer dashboard called "testreg."

This page shows the full API contract for a feature, rendered as stacked layer cards from entry point down to SQL. It answers: "What does this feature touch, end to end?"

Dark mode. Background: #0d1117. Cards: #161b22. Text: #e6edf3 primary, #8b949e secondary.
Font: Inter for UI, JetBrains Mono for code and field tables. Accent: #58a6ff.

CONTROLS BAR (top, inside a subtle card):
  Feature selector dropdown: [training.record-exercise ▼]
    Populated from registry. Selecting a feature loads the contract.
  Format toggle: radio pills [Terminal] [JSON] [Markdown]
  Action button: [Copy as markdown] — copies contract to clipboard for PR descriptions

ENTRY POINT BANNER (below controls):
  "Entry: GRAPHQL Mutation.trainingLogSet" in monospace
  Small info chip: "4 layers | type_checking: on ⚠ experimental"

LAYER CARDS (stacked vertically, the core of the page):
  Each layer is a card (#161b22 background, 1px #30363d border, 8px radius, 16px padding).
  Each card has a 4px left border colored by layer kind:
    Blue (#58a6ff) — handler/resolver
    Green (#3fb950) — service
    Yellow (#d29922) — repository
    Purple (#bc8cff) — query/SQL

  CARD HEADER: uppercase label + file:line reference right-aligned in muted text
    "LAYER 1: GRAPHQL API" with "schema.graphqls:142" right

  Inside each card: Input/Output tabs. Active tab has bottom border 2px accent.

  LAYER 1 — GraphQL API (blue left border):
    Header: "LAYER 1: GRAPHQL API" | "schema.graphqls:142"
    Content:
      Code block (monospace, 12px):
        mutation { trainingLogSet(input: TrainingLogSetInput!): TrainingExerciseSet! }
      
      Struct field table (when type_checking enabled):
        "Input: TrainingLogSetInput"
        Table with columns: Field | Type | Required
          sessionId    UUID     yes
          exerciseId   UUID     yes
          reps         Int      no
          weight       Float    no
        
        Table styling: 40px row height, alternating bg (#161b22 / #1c2128), 
        monospace text, header row in #8b949e uppercase 11px

  LAYER 2 — Gateway Resolver (blue left border):
    Header: "LAYER 2: GATEWAY RESOLVER" | "training.resolvers.go:60"
    Content:
      Function: "mutationResolver.TrainingLogSet()" in monospace
      Delegates to: "r.Training.LogSet()" — accent blue link
      
      Input struct table: generated.TrainingLogSetInput
        Same table format as Layer 1, showing Go struct fields
      Output: "*generated.TrainingExerciseSet" in monospace

  LAYER 3 — Service (green left border):
    Header: "LAYER 3: SERVICE" | "session_lifecycle_service.go:141"
    Content:
      Function: "SessionLifecycleService.LogSet()"
      
      Calls (indented tree):
        ├─ aggregates.NewExerciseSet()
        ├─ setRepo.Create()
        └─ eventPublisher.PublishSetLogged()
      
      Each call is a monospace line. Calls that link to other layers 
      are rendered as accent blue links.

  LAYER 4 — Repository / SQL (yellow left border, then purple):
    Header: "LAYER 4: REPOSITORY" | "exercise_set_repository.go:28"
    Content:
      Function: "setRepo.Create()"
      SQL: "InsertExerciseSet" — purple text (#bc8cff)
      SQL file: "queries/exercise_sets.sql:12" in muted text

TEST COVERAGE SECTION (bottom, full-width card):
  Header: "TEST COVERAGE" uppercase
  Per-layer status list:
    Layer 1 (GraphQL):     ✘ NO TEST          — red text (#f85149)
    Layer 2 (Resolver):    ✘ NO TEST          — red text
    Layer 3 (Service):     ✓ event_publisher_test.go    — green text (#3fb950)
    Layer 4 (Repository):  ✓ exercise_set_test.go       — green text
  
  Summary bar: "2/4 layers tested" with a 4-segment progress bar 
  (2 green, 2 red segments)
  
  Missing coverage note in muted text: "Missing: resolver layer, GraphQL schema tests"

RESPONSIVE:
  Desktop: single column of layer cards, max-width 900px centered
  Tablet: same layout, slightly reduced padding
  Mobile: full width, struct field tables scroll horizontally

Show the page with all 4 layers expanded, the training.record-exercise feature selected,
type_checking enabled (so struct field tables are visible), Terminal format active.
Note: type_checking is experimental and buggy. Show a small warning banner when enabled:
"⚠ type_checking is experimental — struct field tables may be incomplete"
The page should feel like reading an API specification — clean, precise, layered.
```

---

## Prompt Order Summary

| # | Prompt | Context | What it produces |
|---|--------|---------|-----------------|
| 1 | Design System | New chat | Component library (badges, bars, cards, nodes) |
| 2 | App Shell | Same chat | Layout (header, sidebar, status bar, responsive) |
| 3 | Overview Page | Same chat | Landing dashboard with health rings and domain bars |
| 4 | Features Table | New chat | Searchable data table with filters |
| 5 | Features Detail | Same chat | Slide-in panel with tabs (graph, gaps, tests, perf) |
| 6 | Graph Page | New chat | Interactive tree visualization with hover/zoom |
| 7 | Sprint + Diff | New chat | Two data pages (priority ranking + snapshot comparison) |
| 8 | Metrics + Diagnose | Same chat | Two pages (quality signals + symptom diagnosis) |
| 9 | Scan Modal | New chat | Modal overlay with progress indicators |
| 10 | Contract Page | New chat | Layered API contract view with struct field tables |
