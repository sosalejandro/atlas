# testreg Dashboard — Wireframes & Design System

**Purpose:** Comprehensive design spec for generating the dashboard UI in Google Stitch. Each section is self-contained with enough detail to produce a high-fidelity mockup.

---

## Design Philosophy

**For developers, by developers.** The dashboard is a productivity tool, not a marketing site. Every pixel serves a purpose. Think VS Code's sidebar + GitHub's data density + Linear's polish.

### Design Principles

1. **Density over whitespace** — Developers want data, not padding. Show more, scroll less.
2. **Scannable over readable** — Color-coded health bars, severity badges, status icons. Eyes should find problems in < 2 seconds.
3. **Context over navigation** — Feature detail opens in a slide panel, not a new page. The table stays visible.
4. **Dark-first, light-supported** — Most developers use dark mode. Design dark first, derive light.
5. **Diagrams are first-class** — The dependency graph is not a hidden feature. It's visible on every feature card, embedded in reports, and interactive on the Graph page.

### Visual Language

- **Type:** Inter or system font stack (`-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, ...`)
- **Mono:** JetBrains Mono or `ui-monospace, "Cascadia Code", "Fira Code", Consolas, monospace`
- **Corners:** 8px radius on cards, 4px on badges/chips, 2px on inputs
- **Shadows:** Subtle (0 1px 3px rgba(0,0,0,0.12)) for cards, stronger for modals
- **Motion:** 150ms ease-out for panel slides, 200ms for hovers, no gratuitous animation

---

## Color System

### Dark Mode (primary)

```
Background:
  --bg-primary:     #0d1117    (GitHub dark — main background)
  --bg-secondary:   #161b22    (cards, panels)
  --bg-tertiary:    #21262d    (hover states, selected rows)
  --bg-elevated:    #30363d    (modals, dropdowns)

Text:
  --text-primary:   #e6edf3    (headings, important text)
  --text-secondary: #8b949e    (labels, descriptions)
  --text-muted:     #484f58    (disabled, placeholders)

Borders:
  --border-default: #30363d
  --border-muted:   #21262d

Health colors (semantic — same in both modes):
  --health-green:   #3fb950    (80-100% — healthy)
  --health-yellow:  #d29922    (50-79% — needs attention)
  --health-red:     #f85149    (0-49% — critical)
  --health-blue:    #58a6ff    (informational, links)

Severity badges:
  --sev-critical:   #f85149 bg, #ffd7d5 text
  --sev-high:       #d29922 bg, #fff8c5 text
  --sev-medium:     #58a6ff bg, #ddf4ff text
  --sev-low:        #8b949e bg, #e6edf3 text

Node kinds (for graph coloring):
  --node-handler:   #58a6ff    (blue)
  --node-service:   #3fb950    (green)
  --node-repo:      #d29922    (yellow)
  --node-query:     #bc8cff    (purple)
  --node-component: #79c0ff    (light blue)
  --node-hook:      #56d364    (bright green)
  --node-external:  #f85149    (red)
  --node-endpoint:  #e6edf3    (white)

Accent:
  --accent:         #58a6ff    (primary action buttons, links)
  --accent-hover:   #79c0ff
```

### Light Mode (derived)

```
Background:
  --bg-primary:     #ffffff
  --bg-secondary:   #f6f8fa
  --bg-tertiary:    #eaeef2
  --bg-elevated:    #ffffff

Text:
  --text-primary:   #1f2328
  --text-secondary: #656d76
  --text-muted:     #afb8c1

Borders:
  --border-default: #d0d7de
  --border-muted:   #eaeef2
```

Health and severity colors stay the same (they're designed to work on both backgrounds).

---

## Layout Structure

### Shell (persistent across all pages)

```
┏━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓
┃ ◉ testreg    nutrition-project-v2         [⟳ Scan]  [☀/🌙]  [⚙]  ┃
┣━━━━━━━━━━┳━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┫
┃          ┃                                                         ┃
┃  nav     ┃  page content                                           ┃
┃          ┃                                                         ┃
┃  ○ Over  ┃                                                         ┃
┃  ○ Feat  ┃                                                         ┃
┃  ○ Graph ┃                                                         ┃
┃  ○ Sprint┃                                                         ┃
┃  ○ Metr  ┃                                                         ┃
┃  ○ Diff  ┃                                                         ┃
┃  ○ Diag  ┃                                                         ┃
┃          ┃                                                         ┃
┃          ┃                                                         ┃
┃          ┃                                                         ┃
┣━━━━━━━━━━┻━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┫
┃ 184 features • 771 tests • 18% at target • Last scan: 10.2s ago   ┃
┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛
```

**Header (48px):**
- Left: testreg logo (◉ circle icon) + project name from registry
- Right: Scan button (primary action), theme toggle (☀/🌙), settings gear
- Background: `--bg-secondary`

**Sidebar (200px, collapsible to 48px icons-only):**
- Icon + label for each page
- Active page highlighted with left accent border + `--bg-tertiary`
- Collapse toggle at bottom
- Background: `--bg-secondary`

**Status bar (32px):**
- Feature count, test count, % at target, last scan time
- Background: `--bg-primary` with top border
- Monospace font for numbers

**Page content:** Full remaining width, scrollable independently from sidebar.

### Responsive Breakpoints

```
Desktop (>1200px):  Full layout, sidebar visible, detail panels side-by-side
Tablet (768-1200px): Sidebar collapsed to icons, detail panels overlay
Mobile (<768px):    No sidebar (bottom tab bar), single column, simplified graphs
```

---

## Page Wireframes

### 1. Overview Page

**Layout:** 2-column grid of cards on desktop, single column on mobile.

```
┌─────────────────────────────────────────────────────────────────────┐
│                                                                     │
│  ┌─── Health by Priority ───────────────────────────────────────┐  │
│  │                                                               │  │
│  │  ┌────────────┐ ┌────────────┐ ┌────────────┐ ┌────────────┐│  │
│  │  │  CRITICAL  │ │    HIGH    │ │   MEDIUM   │ │    LOW     ││  │
│  │  │            │ │            │ │            │ │            ││  │
│  │  │   ╭───╮    │ │   ╭───╮    │ │   ╭───╮    │ │   ╭───╮    ││  │
│  │  │   │74%│    │ │   │19%│    │ │   │ 3%│    │ │   │ 0%│    ││  │
│  │  │   ╰───╯    │ │   ╰───╯    │ │   ╰───╯    │ │   ╰───╯    ││  │
│  │  │  17/23     │ │  14/75     │ │   2/65     │ │   0/21     ││  │
│  │  │  at target │ │  at target │ │  at target │ │  at target ││  │
│  │  └────────────┘ └────────────┘ └────────────┘ └────────────┘│  │
│  │                                                               │  │
│  │  ████████████████████████████████████████░░░░░░░░░░░░  18%   │  │
│  │  Overall: 33 of 184 features at target                       │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                                                     │
│  ┌─── Coverage Matrix ──────────┐  ┌─── Performance ────────────┐  │
│  │                               │  │                             │  │
│  │  Unit    ████░░░░░░  49%     │  │  Benchmarks  █░░░░░  12%   │  │
│  │  Integ   █░░░░░░░░░  10%     │  │  Race tests  ██░░░░  28%   │  │
│  │  E2E     ██░░░░░░░░  16%     │  │  Overall     █░░░░░  18%   │  │
│  │                               │  │                             │  │
│  └───────────────────────────────┘  └─────────────────────────────┘  │
│                                                                     │
│  ┌─── Top Sprint Priorities ────────────────────────────────────┐  │
│  │                                                               │  │
│  │  3.00  ● critical  training.end-session        ██░░░░░ 25%  │  │
│  │  2.40  ● high      plans-nutri.meal-option     ░░░░░░░  0%  │  │
│  │  2.40  ● high      shopping.generate-from      ░░░░░░░  0%  │  │
│  │  2.40  ● high      billing.update-payment      ░░░░░░░  0%  │  │
│  │  2.40  ● high      client-analytics.meal       ░░░░░░░  0%  │  │
│  │                                                               │  │
│  │  → View all sprint priorities                                 │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                                                     │
│  ┌─── Domains ──────────────────────────────────────────────────┐  │
│  │                                                               │  │
│  │  auth           ████████░░  9/10   training    █████████░ 11/12│  │
│  │  meals          ██████░░░░  8/12   recipes     ██████░░░░  6/10│  │
│  │  billing        █████░░░░░  9/14   settings    █████░░░░░  8/15│  │
│  │  scan           ██████████  7/7    recovery    ███░░░░░░░  3/14│  │
│  │  ...                                                          │  │
│  │                                                               │  │
│  │  Click any domain → Features page filtered to that domain    │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

**Card design:**
- Background: `--bg-secondary`
- Border: 1px `--border-default`
- Border-radius: 8px
- Padding: 16px
- Header: 13px uppercase `--text-secondary`, 600 weight
- Numbers: 24px `--text-primary`, tabular-nums

**Health ring (the ╭───╮ circles):**
- SVG donut chart, 64px diameter
- Colored arc based on health level (green/yellow/red)
- Percentage in center, 20px bold
- Count below: "17/23" in `--text-secondary`

**Progress bars:**
- Height: 8px, border-radius: 4px
- Background: `--bg-tertiary`
- Fill: health color gradient
- Percentage label right-aligned

### 2. Features Page

**Layout:** Full-width data table with right-side detail panel.

```
┌─────────────────────────────────────────────────────────────────────┐
│                                                                     │
│  ┌─── Filters ──────────────────────────────────────────────────┐  │
│  │  🔍 Search features...    Priority [All ▼]  Domain [All ▼]  │  │
│  │  Health [Below target ▼]  Sort [Health ↑ ▼]   184 results    │  │
│  └──────────────────────────────────────────────────────────────┘  │
│                                                                     │
│  ┌─── Feature Table ───────────────┐  ┌─── Detail Panel ────────┐  │
│  │                                  │  │                          │  │
│  │  Feature        Pri  Health Perf│  │  auth.login              │  │
│  │  ─────────────────────────────  │  │  critical • Health: 74%  │  │
│  │  ▸ auth.login   ● C   74%  32% │  │                          │  │
│  │    auth.regist  ● C   68%  15% │  │  ┌─ Tabs ─────────────┐ │  │
│  │    auth.logout  ● C  100%   0% │  │  │ Graph │Gaps│Tests│⚡│ │  │
│  │    meals.log    ● H   62%   5% │  │  └──────────────────────┘ │  │
│  │    meals.hist   ● H  100%  27% │  │                          │  │
│  │    recipes.cre  ● H   45%  16% │  │  ┌─ Mini Graph ────────┐ │  │
│  │    recipes.edi  ● M   88%   0% │  │  │                      │ │  │
│  │    billing.sub  ● C  100%   2% │  │  │  route:/login        │ │  │
│  │    billing.upd  ● H    0%   0% │  │  │  └─ LoginPage        │ │  │
│  │    training.se  ● H   92%  40% │  │  │     └─ useAuth       │ │  │
│  │    training.en  ● C   25%   0% │  │  │        └─ authApi    │ │  │
│  │    ...                          │  │  │           └─ POST..  │ │  │
│  │                                  │  │  │              └─ Auth│ │  │
│  │  ◀ 1 2 3 ... 8 ▶               │  │  │                 └─..│ │  │
│  │                                  │  │  └──────────────────────┘ │  │
│  └──────────────────────────────────┘  │                          │  │
│                                         │  Gaps (13):             │  │
│                                         │  ● CRITICAL authRepo   │  │
│                                         │  ● HIGH     hashToken  │  │
│                                         │  ● MEDIUM   sql:GetU.. │  │
│                                         │                          │  │
│                                         │  Layer Coverage:        │  │
│                                         │  Handler  ████████ 100% │  │
│                                         │  Service  ██████░░  75% │  │
│                                         │  Repo     ░░░░░░░░   0% │  │
│                                         │  Query    ░░░░░░░░   0% │  │
│                                         │                          │  │
│                                         │  [Open full graph →]    │  │
│                                         └──────────────────────────┘  │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

**Table design:**
- Row height: 40px
- Hover: `--bg-tertiary`
- Selected row: `--bg-tertiary` + left border 3px `--accent`
- Priority dot: colored circle inline (●) — red/yellow/blue/gray
- Health: colored text (green/yellow/red) based on value
- Perf: same coloring
- Monospace for numbers

**Detail panel:**
- Width: 400px on desktop, full overlay on tablet/mobile
- Slide-in from right: 200ms ease-out
- Tabs for different views: Graph, Gaps, Tests, Performance (⚡)
- Mini graph: simplified tree view (not full D3), just indented nodes with colored dots
- Each node shows test status icon: ✓ green, ◐ yellow, ✘ red

**Mini graph in detail panel:**
- ASCII-style tree but rendered as styled HTML divs
- Each node: colored dot (by kind) + name + status icon
- Hovering a node shows tooltip: file:line, test status, coverage %
- Click "Open full graph →" navigates to the Graph page for this feature

### 3. Graph Page

**Layout:** Full-width interactive graph with controls.

```
┌─────────────────────────────────────────────────────────────────────┐
│                                                                     │
│  Feature: [auth.login ▼]   Layout: [Tree ▼]   [Fit] [↻] [Export]  │
│                                                                     │
│  ┌─── Interactive Graph ────────────────────────────────────────┐  │
│  │                                                               │  │
│  │                    ┌──────────────┐                           │  │
│  │                    │ route:/login │  ← cyan (component)      │  │
│  │                    └──────┬───────┘                           │  │
│  │                           │                                   │  │
│  │                    ┌──────▼───────┐                           │  │
│  │                    │  LoginPage   │  ← cyan                  │  │
│  │                    └──────┬───────┘                           │  │
│  │                           │                                   │  │
│  │                    ┌──────▼───────┐                           │  │
│  │                    │   useAuth    │  ← green (hook)          │  │
│  │                    └──────┬───────┘                           │  │
│  │                           │                                   │  │
│  │                    ┌──────▼───────┐                           │  │
│  │                    │ authApi.login│  ← white (endpoint)      │  │
│  │                    └──────┬───────┘                           │  │
│  │                           │                                   │  │
│  │              ┌────────────▼────────────┐                     │  │
│  │              │ POST /api/v1/auth/login │  ← blue (handler)  │  │
│  │              └────────────┬────────────┘                     │  │
│  │                           │                                   │  │
│  │              ┌────────────▼────────────┐                     │  │
│  │              │   authService.Login     │  ← green (service) │  │
│  │              └────┬────┬────┬─────┬───┘                     │  │
│  │                   │    │    │     │                           │  │
│  │             ┌─────▼┐ ┌▼───┐│ ┌───▼──────────┐              │  │
│  │             │Argon2│ │JWT ││ │authRepo.Store │ ← yellow    │  │
│  │             │Verify│ │Gen ││ │RefreshToken   │   (repo)    │  │
│  │             └──────┘ └─┬──┘│ └───────────────┘              │  │
│  │                  ┌─────┼───┘                                 │  │
│  │                  │ ┌───▼────────────┐                       │  │
│  │                  │ │sql:GetUserByEm │  ← purple (query)    │  │
│  │                  │ └────────────────┘                       │  │
│  │             ┌────▼──────┐ ┌─────────────┐                  │  │
│  │             │GenAccess  │ │GenRefresh   │                   │  │
│  │             │Token      │ │Token        │                   │  │
│  │             └───────────┘ └─────────────┘                   │  │
│  │                                                               │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                                                     │
│  Legend:                                                            │
│  🔵 handler  🟢 service  🟡 repository  🟣 query                   │
│  🔴 external  ⬜ component  ◻ hook  ⚪ endpoint                     │
│                                                                     │
│  Node border: solid = tested, dashed = partial, dotted = untested  │
│                                                                     │
│  Confidence: 100%  │  Nodes: 16  │  Depth: 8                      │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

**Graph rendering:**
- D3-force layout with top-to-bottom tree orientation (not force-directed — tree layout is more readable for call graphs)
- Each node: rounded rectangle, 160px × 36px
- Node fill: `--bg-secondary`, border color by node kind
- Node border-style: solid (tested), dashed (partial), dotted (untested)
- Edge: straight line with arrowhead, 1px `--border-default`
- Hover node: tooltip with file:line, test status, coverage %
- Click node: highlight all edges from/to this node
- Zoom: mouse wheel, pinch on mobile
- Pan: drag background
- Fit button: auto-fit zoom to show entire graph

**Export options:**
- PNG (canvas screenshot)
- SVG (vector, for docs)
- Mermaid (copy to clipboard — for markdown/PRs)

**Layout options:**
- Tree (default — top-to-bottom hierarchy)
- Force (force-directed — shows clusters)
- Horizontal (left-to-right — for wide screens)

### 4. Sprint Planning Page

```
┌─────────────────────────────────────────────────────────────────────┐
│                                                                     │
│  Sprint Planning                                                    │
│                                                                     │
│  ┌─── Controls ─────────────────────────────────────────────────┐  │
│  │  Show [20 ▼] results   Priority [All ▼]                      │  │
│  │  Group by: (○) None  (●) Fix Type  (○) Domain                │  │
│  │  [Export JSON]  [Export Prompt]  [Copy to clipboard]           │  │
│  └──────────────────────────────────────────────────────────────┘  │
│                                                                     │
│  ┌─── Ranked Features ─────────────────────────────────────────┐  │
│  │                                                               │  │
│  │  Score   Priority    Health → Target   Feature                │  │
│  │  ─────────────────────────────────────────────────────────   │  │
│  │  3.00    ● critical   ██░░░░░ 25→100%  training.end-session  │  │
│  │  2.40    ● high       ░░░░░░░  0→ 80%  plans-nutri.meal...  │  │
│  │  2.40    ● high       ░░░░░░░  0→ 80%  shopping.generate    │  │
│  │  2.40    ● high       ░░░░░░░  0→ 80%  billing.update-pay   │  │
│  │  2.40    ● high       ░░░░░░░  0→ 80%  client-analytics..   │  │
│  │  ...                                                          │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                                                     │
│  ┌─── By Fix Type ─────────────────────────────────────────────┐  │
│  │                                                               │  │
│  │  unit tests         ████████████████████░░░░░  67 features   │  │
│  │  integration tests  ████████░░░░░░░░░░░░░░░░░  34 features   │  │
│  │  e2e tests          ████░░░░░░░░░░░░░░░░░░░░░  18 features   │  │
│  │  benchmarks         ██████████░░░░░░░░░░░░░░░  42 features   │  │
│  │  race tests         █████████░░░░░░░░░░░░░░░░  38 features   │  │
│  │                                                               │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

**Export Prompt button:** Downloads a `.md` file with `testreg gaps --format prompt` output — ready to paste into an AI agent for automated test writing.

### 5. Metrics Page

```
┌─────────────────────────────────────────────────────────────────────┐
│                                                                     │
│  Quality Signals                                                    │
│                                                                     │
│  ┌─── Prerequisites ────────────────────────────────────────────┐  │
│  │  ⓘ Import test results to see quality signals:               │  │
│  │                                                               │  │
│  │  $ go test -json ./... > test-output.json                     │  │
│  │  $ testreg update --gotest test-output.json --with-metrics    │  │
│  │                                           [Copy] [Dismiss]   │  │
│  └──────────────────────────────────────────────────────────────┘  │
│                                                                     │
│  ┌─── Slowest ──────────┐  ┌─── Flaky ──────────────────────┐    │
│  │                       │  │                                 │    │
│  │  TestRecipeSync 4.2s  │  │  TestWSReconnect   3 retries   │    │
│  │  TestAuthE2E    3.8s  │  │  TestUploadPDF     2 retries   │    │
│  │  TestBilling    2.1s  │  │                                 │    │
│  │  TestMealLog    1.9s  │  │  No flaky tests ✓              │    │
│  │  TestRecipeSr   1.4s  │  │  (if empty)                    │    │
│  └───────────────────────┘  └─────────────────────────────────┘    │
│                                                                     │
│  ┌─── Race Conditions ──┐  ┌─── Memory Hogs ────────────────┐    │
│  │                       │  │                                 │    │
│  │  ⚠ TestConcurrent..  │  │  BenchRecipeList    4.2 MB/op  │    │
│  │  ⚠ TestParallelSync  │  │  BenchUserSearch    2.8 MB/op  │    │
│  │                       │  │  BenchBillingCalc   1.1 MB/op  │    │
│  └───────────────────────┘  └─────────────────────────────────┘    │
│                                                                     │
│  ┌─── Health Trend ────────────────────────────────────────────┐  │
│  │                                                               │  │
│  │  100% ┤                                              ●      │  │
│  │   80% ┤                                  ●───●───●──●       │  │
│  │   60% ┤                      ●───●───●──●                   │  │
│  │   40% ┤          ●───●───●──●                               │  │
│  │   20% ┤  ●───●──●                                          │  │
│  │    0% ┼───┼───┼───┼───┼───┼───┼───┼───┼───┼                │  │
│  │      Mar 1   5  10  15  20  25  28  30  31                  │  │
│  │                                                               │  │
│  │  ── overall health   ─ ─ critical tier   · · · high tier     │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

**Trend chart:** SVG line chart with:
- X axis: snapshot dates
- Y axis: health percentage
- Lines: overall, critical tier, high tier
- Dots: clickable, shows snapshot details on hover
- Data source: `.testreg-cache/snapshots/*.json` files

### 6. Diff Page

```
┌─────────────────────────────────────────────────────────────────────┐
│                                                                     │
│  Progress Tracking                                                  │
│                                                                     │
│  ┌─── Snapshots ────────────────────────────────────────────────┐  │
│  │                                                               │  │
│  │  Name              Date        Features   Health              │  │
│  │  sprint-3-start    Mar 28      184        12%                │  │
│  │  sprint-3-end      Mar 31      184        18%                │  │
│  │  latest            Mar 31      184        18%                │  │
│  │                                                               │  │
│  │  New snapshot: [_______________]  [💾 Save]                   │  │
│  └──────────────────────────────────────────────────────────────┘  │
│                                                                     │
│  Compare: [sprint-3-start ▼]  →  [latest ▼]   [Compare]           │
│                                                                     │
│  ┌─── Results ──────────────────────────────────────────────────┐  │
│  │                                                               │  │
│  │  ┌── Improved (12) ───────────────────────────────────────┐  │  │
│  │  │  +100%  auth.login                     0% ░░░→ 100% ██│  │  │
│  │  │  + 74%  client-management.detail       0% ░░░→  74% ██│  │  │
│  │  │  + 50%  recipes.create                30% ██░→  80% ██│  │  │
│  │  │  ...                                                    │  │  │
│  │  └─────────────────────────────────────────────────────────┘  │  │
│  │                                                               │  │
│  │  ┌── Regressed (2) ───────────────────────────────────────┐  │  │
│  │  │  - 10%  meals.log.create              80% ██░→  70% █░│  │  │
│  │  │  -  5%  settings.notifications        60% ██░→  55% █░│  │  │
│  │  └─────────────────────────────────────────────────────────┘  │  │
│  │                                                               │  │
│  │  Unchanged: 170 features                                      │  │
│  │                                                               │  │
│  │  ┌── Summary ─────────────────────────────────────────────┐  │  │
│  │  │  Average change: +15.4%  ████████████████░░░░ ↑        │  │  │
│  │  └─────────────────────────────────────────────────────────┘  │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

**Diff bars:** Each feature row shows a before→after mini progress bar. Green background for improved, red for regressed.

### 7. Diagnose Page

```
┌─────────────────────────────────────────────────────────────────────┐
│                                                                     │
│  Diagnose                                                           │
│                                                                     │
│  ┌─── Input ────────────────────────────────────────────────────┐  │
│  │                                                               │  │
│  │  Feature: [auth.login                    ▼]                  │  │
│  │  Symptom: [401 Unauthorized                 ]  [🔍 Diagnose] │  │
│  │                                                               │  │
│  │  Quick symptoms (clickable chips):                            │  │
│  │  [401] [403] [404] [500] [timeout] [connection refused]      │  │
│  │  [unique constraint] [json unmarshal] [CORS] [EOF]           │  │
│  │  [deadlock] [sql: no rows] [TypeError] [hydration mismatch] │  │
│  └──────────────────────────────────────────────────────────────┘  │
│                                                                     │
│  ┌─── Diagnosis ────────────────────────────────────────────────┐  │
│  │                                                               │  │
│  │  Best Match                                                   │  │
│  │  ┌──────────────────────────────────────────────────────────┐│  │
│  │  │  Layer:       backend-auth                               ││  │
│  │  │  Confidence:  70%  ████████░░░░                          ││  │
│  │  │  Description: Authentication failure — request lacks     ││  │
│  │  │               valid credentials or session has expired   ││  │
│  │  │  Check order: handler → service → external               ││  │
│  │  └──────────────────────────────────────────────────────────┘│  │
│  │                                                               │  │
│  │  Also Matched (secondary rules, lower confidence):            │  │
│  │  ┌──────────────────────────────────────────────────────────┐│  │
│  │  │  55% backend-routing — Not found: endpoint does not      ││  │
│  │  │                        exist or resource is missing      ││  │
│  │  └──────────────────────────────────────────────────────────┘│  │
│  │                                                               │  │
│  │  Files to check (ordered by likelihood):                     │  │
│  │  ┌──────────────────────────────────────────────────────────┐│  │
│  │  │  1. src/infrastructure/http/handlers/auth_handler.go     ││  │
│  │  │     └─ AuthHandler.Login (line 249)                      ││  │
│  │  │                                                          ││  │
│  │  │  2. src/application/services/auth_service.go             ││  │
│  │  │     └─ authService.Login (line 172)                      ││  │
│  │  │                                                          ││  │
│  │  │  3. src/infrastructure/auth/jwt_generator.go             ││  │
│  │  │     └─ JWTGenerator.GenerateTokenPair (line 70)          ││  │
│  │  └──────────────────────────────────────────────────────────┘│  │
│  │                                                               │  │
│  │  ┌─ Graph (diagnosed nodes highlighted) ────────────────────┐│  │
│  │  │  route:/login                                             ││  │
│  │  │  └─ LoginPage                                             ││  │
│  │  │     └─ useAuth                                            ││  │
│  │  │        └─ authApi.login                                   ││  │
│  │  │           └─ POST /api/v1/auth/login                      ││  │
│  │  │              └─ ★ AuthHandler.Login ← check first         ││  │
│  │  │                 └─ ★ authService.Login ← check second     ││  │
│  │  │                    ├─ ★ JWTGenerator... ← check third     ││  │
│  │  │                    ├─ authRepo.StoreRefreshToken           ││  │
│  │  │                    └─ sql:GetUserByEmail                   ││  │
│  │  └──────────────────────────────────────────────────────────┘│  │
│  │                                                               │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

**Quick symptom chips:** Clickable chips that populate the input field. Organized in 3 rows: HTTP status codes, database/serialization errors, and frontend errors. Covers all 26 built-in symptom rules.

**Confidence scoring:** Each matched rule shows a confidence percentage (0-100%) indicating how diagnostic the pattern is. High confidence (85-95%) means the pattern almost certainly identifies the correct layer (e.g., `unique constraint` → data layer). Lower confidence (50-70%) means the error could originate in multiple layers (e.g., generic `500`).

**Multi-match:** When a symptom matches multiple rules, the best match is shown prominently and secondary matches are listed below with their confidence and layer. For example, `"500 internal server error: context deadline exceeded"` matches both the 500 rule and the timeout rule.

**Graph with highlights:** The dependency tree is shown with diagnosed nodes highlighted (★ star + different border/background). The visual immediately shows WHERE in the call chain to look.

---

## Scan Modal

```
┌─── Scan & Import ───────────────────────────────────────────────────┐
│                                                                      │
│  ┌─── Always Run ────────────────────────────────────────────────┐  │
│  │  ☑ Discover test files (Go, TS, Python, Playwright, Maestro)  │  │
│  │  ☑ Build dependency graph (Go AST + TypeScript)               │  │
│  │  ☑ Audit all features (health scores, gaps, perf)             │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                                                      │
│  ┌─── Import Test Results (optional) ────────────────────────────┐  │
│  │  ☐ Go test results                                            │  │
│  │    [test-output.json_______________] [Browse]                  │  │
│  │    $ go test -json ./... > test-output.json                    │  │
│  │                                                                │  │
│  │  ☐ Playwright results                                         │  │
│  │    [test-results/_________________] [Browse]                   │  │
│  │    $ npx playwright test --reporter=json                       │  │
│  │                                                                │  │
│  │  ☐ Vitest results                                             │  │
│  │    [vitest-output.json_____________] [Browse]                  │  │
│  │    $ npx vitest --reporter=json                                │  │
│  │                                                                │  │
│  │  ☐ Coverage profile                                           │  │
│  │    [cover.out_____________________] [Browse]                   │  │
│  │    $ go test -coverprofile=cover.out ./...                     │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                                                      │
│  ┌─── Progress ──────────────────────────────────────────────────┐  │
│  │                                                                │  │
│  │  ✓ Scanning test files...                          0.5s       │  │
│  │  ✓ Building Go AST graph...                        8.2s       │  │
│  │  ● Auditing 184 features...  ████████░░░░ 67%      5.1s       │  │
│  │  ○ Importing test results...                       pending    │  │
│  │                                                                │  │
│  │  Total: 13.8s                                                  │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                                                      │
│                                          [Cancel]  [▶ Run Selected]  │
│                                                                      │
└──────────────────────────────────────────────────────────────────────┘
```

**Progress indicators:**
- ✓ Completed (green check)
- ● In progress (blue spinner)
- ○ Pending (gray circle)
- ✘ Failed (red X)

Each step shows wall time. SSE streams updates from the server.

---

## Component Library

### Cards
```
┌─────────────────────────┐
│  Card Title              │  ← 13px uppercase, --text-secondary, 600
│                          │
│  Card content            │  ← 14px, --text-primary
│                          │
└─────────────────────────┘
Background: --bg-secondary
Border: 1px --border-default
Radius: 8px
Padding: 16px
```

### Health Badge
```
  ┌───────┐
  │  74%  │  ← pill shape, colored by health tier
  └───────┘
green: ≥80%  bg: --health-green/10, text: --health-green
yellow: 50-79%  bg: --health-yellow/10, text: --health-yellow
red: <50%  bg: --health-red/10, text: --health-red
Padding: 4px 8px, radius: 12px, font: mono 12px bold
```

### Severity Badge
```
  ┌──────────┐
  │ CRITICAL │  ← uppercase, pill
  └──────────┘
critical: bg: --sev-critical, text: white
high:     bg: --sev-high, text: dark
medium:   bg: --sev-medium, text: white
low:      bg: --sev-low, text: white
Padding: 2px 8px, radius: 4px, font: 11px 600
```

### Progress Bar
```
  ████████░░░░░░░░░░░  42%
  
Height: 8px (inline) or 12px (standalone)
Radius: 4px
Background: --bg-tertiary
Fill: health color based on percentage
Label: right-aligned, mono, --text-secondary
```

### Tree Node (for dependency chains)
```
  🔵 AuthHandler.Login            ✓ tested  73%
  │   file: auth_handler.go:249
  │
  ├─ 🟢 authService.Login         ◐ partial 12%
  │     file: auth_service.go:172
  │
  └─ 🟡 authRepo.StoreRefresh     ✘ no test
        file: auth_repo.go:329

Node: 16px line-height, indented with connector lines
Kind dot: 8px circle, colored by node kind
Status: icon + text, colored (green/yellow/red)
Coverage %: mono, only shown when coverage data available
File: --text-muted, 12px, hidden until hover
```

### Data Table
```
  Feature          Priority   Health   Perf   Gaps
  ────────────────────────────────────────────────
  auth.login       ● critical  74%     32%    13
  auth.register    ● critical  68%     15%     8
  meals.log        ● high      62%      5%     5

Header: 12px uppercase, --text-secondary, sticky
Row: 40px height, hover --bg-tertiary
Selected: --bg-tertiary + 3px left accent border
Alternating: subtle (--bg-primary / --bg-secondary)
Font: 13px, mono for numbers
```

---

## Google Stitch Prompt

Use this prompt to generate the base design in Google Stitch:

```
Design a developer productivity dashboard web application called "testreg" 
for test coverage and dependency graph analysis.

Theme: Dark mode (GitHub dark palette), with light mode toggle. 
Developer-focused, data-dense, no unnecessary whitespace.
Font: Inter for UI, JetBrains Mono for code/numbers.

Layout: Fixed sidebar (200px) with icon + label navigation, 
collapsible to 48px. Fixed header (48px) with project name, 
scan button, theme toggle. Fixed status bar (32px) at bottom.

Pages to design:

1. OVERVIEW: 4 health ring cards (Critical/High/Medium/Low showing 
   percentage donut charts), coverage matrix bars (Unit/Integ/E2E), 
   performance score card, top 5 sprint priorities table, 
   domain breakdown with horizontal progress bars.

2. FEATURES: Full-width data table with searchable filters 
   (priority, domain, health threshold). Right-side detail panel 
   (400px, slides in) showing: mini dependency tree, gaps list with 
   severity badges, layer coverage bars, performance score.

3. GRAPH: Full-width interactive tree diagram showing dependency 
   chain from React route to SQL query. Nodes are colored rounded 
   rectangles (blue=handler, green=service, yellow=repo, purple=query). 
   Node borders: solid=tested, dashed=partial, dotted=untested.
   Feature selector dropdown, layout toggle, export buttons.

4. SPRINT: Priority-ranked table with score, priority badge, 
   health progress bar showing current→target. Group-by toggles 
   (none/type/domain). Export buttons for JSON and AI prompt format.

5. METRICS: 4-card grid showing Slowest Tests, Flaky Tests, 
   Race Conditions, Memory Hogs. Below: line chart showing 
   health trend over time with multiple series.

6. DIFF: Snapshot list table with save button, compare dropdowns. 
   Results showing improved (green), regressed (red), unchanged 
   sections with before→after progress bars per feature.

7. DIAGNOSE: Input form with feature dropdown and symptom text field. 
   Quick-select chips for common errors (401, 500, timeout). 
   Results showing matched rule card, ordered file list, and 
   dependency tree with diagnosed nodes highlighted.

8. SCAN MODAL: Overlay with checkboxes for scan steps, file path 
   inputs for test result imports, progress section with step 
   indicators (✓ done, ● running, ○ pending).

Color system:
- Health: green (#3fb950) ≥80%, yellow (#d29922) 50-79%, red (#f85149) <50%
- Severity badges: critical=red, high=yellow, medium=blue, low=gray
- Node kinds: handler=blue, service=green, repo=yellow, query=purple
- Background: #0d1117 primary, #161b22 cards, #21262d hover
- Text: #e6edf3 primary, #8b949e secondary
- Accent: #58a6ff

Responsive: Desktop (>1200px) full layout, tablet sidebar collapses, 
mobile single column with bottom tab bar.

Style: Clean, minimal, professional. Similar to GitHub's dark mode 
analytics pages crossed with Linear's task management UI.
No decorative elements, no illustrations. Every pixel serves a purpose.
```
