# testreg Tool Findings -- CLI Gaps Identified from Real Usage

Every pattern below was discovered during real testreg usage on the nutrition-project-v2 monorepo (184 features, 16 domains, full-stack Go + React + React Native). Each time we piped testreg output through python or bash to extract, filter, or aggregate data, that represents a UX gap in the CLI. These should become native flags or commands.

**Date:** 2026-03-31
**Project:** nutrition-project-v2 (testreg v0.x)

---

## Section 1: Output Filtering Gaps

### Gap 1: Filter audit by priority

**Situation:** Needed to see only critical-priority features to focus a testing sprint. Had to pipe JSON through python to filter by the `Priority` field.

**Current workaround:**
```bash
testreg audit --format json | python3 -c "
import json, sys
data = json.load(sys.stdin)
for f in data:
    if f['Priority'] == 'critical':
        print(f'{f[\"FeatureID\"]:40s} health={f[\"HealthScore\"]:.0%}')
"
```

**Proposed native feature:**
```bash
testreg audit --priority critical
testreg audit --priority critical,high   # multiple values
```

**Priority:** HIGH -- this is the most common filter operation during sprint planning.

---

### Gap 2: Filter features below health threshold

**Situation:** Needed to find all features below 50% health.

**Current workaround:**
```bash
testreg audit --format json | python3 -c "
import json, sys
data = json.load(sys.stdin)
for f in data:
    if f['HealthScore'] < 0.5:
        print(f'{f[\"FeatureID\"]:40s} {f[\"HealthScore\"]:.0%}')
"
```

**Status:** Already addressed. `testreg audit --min-health 0.5` exists and works correctly.

---

### Gap 3: Sort features by priority score (weight x delta)

**Situation:** During sprint planning, needed to rank features by "bang for the buck" -- how much a critical feature is below its target vs. how much a low-priority feature is below its target. Critical features with large gaps should sort first.

**Current workaround:**
```bash
testreg audit --format json | python3 -c "
import json, sys

WEIGHTS = {'critical': 4, 'high': 3, 'medium': 2, 'low': 1}
TARGETS = {'critical': 1.0, 'high': 0.8, 'medium': 0.6, 'low': 0.4}

data = json.load(sys.stdin)
scored = []
for f in data:
    w = WEIGHTS.get(f['Priority'], 1)
    target = TARGETS.get(f['Priority'], 0.5)
    delta = max(0, target - f['HealthScore'])
    score = w * delta
    if score > 0:
        scored.append((score, f))

scored.sort(key=lambda x: -x[0])
for score, f in scored[:20]:
    print(f'{score:5.2f}  {f[\"Priority\"]:8s}  {f[\"HealthScore\"]:5.0%}  {f[\"FeatureID\"]}')
"
```

**Proposed native feature:**
```bash
testreg audit --sort priority-score       # sort by weighted gap
testreg audit --sort priority-score -n 20 # top 20 only
testreg audit --sort health               # sort by health ascending
testreg audit --sort name                 # sort alphabetically
```

**Priority:** HIGH -- this is the primary input for sprint planning decisions.

---

### Gap 4: Count features by priority x health bucket

**Situation:** Needed a summary view showing how many features per priority tier are "done" (at target) vs. "gap" (below target). This was the first thing asked for in every planning session.

**Current workaround:**
```bash
testreg audit --format json | python3 -c "
import json, sys
from collections import defaultdict

TARGETS = {'critical': 1.0, 'high': 0.8, 'medium': 0.6, 'low': 0.4}
data = json.load(sys.stdin)

buckets = defaultdict(lambda: {'done': 0, 'gap': 0, 'total': 0})
for f in data:
    p = f['Priority']
    target = TARGETS.get(p, 0.5)
    bucket = 'done' if f['HealthScore'] >= target else 'gap'
    buckets[p][bucket] += 1
    buckets[p]['total'] += 1

for p in ['critical', 'high', 'medium', 'low']:
    b = buckets[p]
    print(f'{p.upper():10s}  {b[\"done\"]}/{b[\"total\"]} at target  ({b[\"gap\"]} gaps)')
"
```

**Example output from workaround:**
```
CRITICAL    8/23 at target  (15 gaps)
HIGH        9/75 at target  (66 gaps)
MEDIUM      3/52 at target  (49 gaps)
LOW         2/34 at target  (32 gaps)
```

**Proposed native feature:**
```bash
testreg audit --summary
```

**Expected output:**
```
Priority Summary:
  CRITICAL   8/23 at target  (15 gaps)  ████░░░░░░ 35%
  HIGH       9/75 at target  (66 gaps)  █░░░░░░░░░ 12%
  MEDIUM     3/52 at target  (49 gaps)  █░░░░░░░░░  6%
  LOW        2/34 at target  (32 gaps)  █░░░░░░░░░  6%

  Overall: 22/184 features at target (12%)
```

**Priority:** MEDIUM -- useful for dashboards and progress tracking.

---

### Gap 5: Filter features with 0 gaps but 0% health (missing API surfaces)

**Situation:** Some features showed 0% health but also 0 gaps. This happens when the registry YAML defines the feature but no `api_surfaces` are configured, so testreg has nothing to trace. These features are effectively invisible to the graph scanner.

**Current workaround:**
```bash
testreg audit --format json | python3 -c "
import json, sys

data = json.load(sys.stdin)
for f in data:
    if f['HealthScore'] == 0 and f.get('GapCount', 0) == 0:
        print(f'{f[\"FeatureID\"]:40s} priority={f[\"Priority\"]}  (no API surfaces configured)')
"
```

**Proposed native feature:**
```bash
testreg audit --unconfigured       # features missing API surfaces
testreg audit --missing-surfaces   # alias
testreg check --unconfigured       # alternative: separate check command
```

**Priority:** MEDIUM -- important during initial onboarding and registry maintenance.

---

## Section 2: Trace Output Processing Gaps

### Gap 6: Extract all node IDs from a trace (flat list)

**Situation:** Needed a flat list of all function/method/query nodes in a feature's dependency tree for cross-referencing with test coverage data. The tree format is great for humans but hard to process programmatically.

**Current workaround:**
```bash
testreg trace auth.login --format json | python3 -c "
import json, sys

def collect_ids(node, ids=None):
    if ids is None:
        ids = []
    ids.append(node['id'])
    for child in node.get('children', []):
        collect_ids(child, ids)
    return ids

tree = json.load(sys.stdin)
for node_id in collect_ids(tree):
    print(node_id)
"
```

**Proposed native feature:**
```bash
testreg trace auth.login --list-nodes    # flat list of all node IDs
testreg trace auth.login --list-nodes --kind service  # filter by kind
```

**Expected output:**
```
route:/login
LoginPage
useAuth
authApi.login
POST /api/v1/auth/login
AuthHandler.Login
authService.Login
JWTGenerator.GenerateTokenPair
JWTGenerator.GenerateAccessToken
JWTGenerator.GenerateRefreshToken
authRepository.StoreRefreshToken
repositories.HashToken
sql:GetUserByEmail
```

**Priority:** LOW -- mainly needed for scripting and automation.

---

### Gap 7: Check for duplicate nodes in trace

**Situation:** During registry debugging, discovered that some features had duplicate nodes in their trace (same function appearing multiple times at different depths). Needed to validate trace integrity.

**Current workaround:**
```bash
testreg trace auth.login --format json | python3 -c "
import json, sys
from collections import Counter

def collect_ids(node, ids=None):
    if ids is None:
        ids = []
    ids.append(node['id'])
    for child in node.get('children', []):
        collect_ids(child, ids)
    return ids

tree = json.load(sys.stdin)
counts = Counter(collect_ids(tree))
dupes = {k: v for k, v in counts.items() if v > 1}
if dupes:
    print('DUPLICATES FOUND:')
    for node_id, count in sorted(dupes.items(), key=lambda x: -x[1]):
        print(f'  {count}x  {node_id}')
else:
    print('No duplicates.')
"
```

**Proposed native feature:**
```bash
testreg trace auth.login --validate   # warns about duplicates, cycles, missing refs
testreg trace --validate-all          # validate all feature traces
```

**Priority:** LOW -- useful for registry maintenance and debugging.

---

### Gap 8: Count total nodes/depth across all features

**Situation:** Wanted aggregate statistics about the dependency graph -- total nodes, average depth, deepest feature -- to understand codebase complexity and estimate audit scope.

**Current workaround:**
```bash
testreg audit --format json | python3 -c "
import json, sys, subprocess

data = json.load(sys.stdin)
total_nodes = 0
max_depth = 0
for f in data:
    result = subprocess.run(
        ['testreg', 'trace', f['FeatureID'], '--format', 'json'],
        capture_output=True, text=True
    )
    if result.returncode == 0:
        tree = json.loads(result.stdout)
        # count nodes and max depth
        ...
"
```

**Proposed native feature:**
```bash
testreg trace --all --summary
```

**Expected output:**
```
Trace Summary (184 features):
  Total unique nodes:    1,247
  Average depth:         6.2
  Deepest feature:       plans.nutritionist.detail (depth: 12)
  Most connected node:   authService.Login (referenced by 8 features)
  Unreachable features:  3 (missing root nodes)
```

**Priority:** LOW -- useful for project-level metrics and documentation.

---

## Section 3: Metrics and Performance Gaps

### Gap 9: Profile testreg itself

**Situation:** Audit of 184 features was taking longer than expected. Needed to understand where time was being spent.

**Current workaround:**
```bash
/usr/bin/time -v testreg audit --all 2>&1 | grep -E "wall clock|Maximum resident"
```

**Status:** Partially addressed. The `--metrics` flag was added for timing output. Could be extended with `--profile` for CPU/memory profiling and `--trace-timing` to show per-phase breakdown.

---

### Gap 10: Batch audit with timing per feature

**Situation:** Needed to identify which specific features were slow to audit (possibly due to deep dependency graphs or large codebases).

**Current workaround:**
```bash
testreg audit --format json | python3 -c "
import json, sys
data = json.load(sys.stdin)
features = [f['FeatureID'] for f in data]
" | while read feat; do
    start=$(date +%s%N)
    testreg audit "$feat" > /dev/null 2>&1
    end=$(date +%s%N)
    ms=$(( (end - start) / 1000000 ))
    echo "${ms}ms  $feat"
done | sort -rn
```

**Proposed native feature:**
```bash
testreg audit --all --per-feature-timing
```

**Expected output:**
```
Audit Timing (184 features):
  2,340ms  plans.nutritionist.detail    (12 nodes, depth 11)
    890ms  auth.login                   (13 nodes, depth 9)
    450ms  recipes.create               (8 nodes, depth 7)
    ...
  Total: 14.2s  Avg: 77ms/feature
```

**Priority:** LOW -- useful for performance optimization of testreg itself.

---

## Section 4: Sprint Workflow Gaps

### Gap 11: Priority-scored feature ranking for sprint planning

**Situation:** This is the most complex and most frequently needed workflow. Every sprint planning session required ranking features by priority-weighted gap to decide what to fix first. This was rebuilt in python every single time.

**Current workaround:**
```python
#!/usr/bin/env python3
"""Sprint planning helper -- run after testreg audit --format json"""
import json, sys

WEIGHTS = {'critical': 4, 'high': 3, 'medium': 2, 'low': 1}
TARGETS = {'critical': 1.0, 'high': 0.8, 'medium': 0.6, 'low': 0.4}

data = json.load(sys.stdin)
ranked = []
for f in data:
    w = WEIGHTS.get(f['Priority'], 1)
    target = TARGETS.get(f['Priority'], 0.5)
    delta = max(0, target - f['HealthScore'])
    score = w * delta
    if score > 0:
        ranked.append({
            'score': score,
            'feature': f['FeatureID'],
            'priority': f['Priority'],
            'health': f['HealthScore'],
            'target': target,
            'gaps': f.get('Gaps', []),
        })

ranked.sort(key=lambda x: -x['score'])

# Group by gap type for efficient batch fixing
by_type = {'unit': [], 'benchmark': [], 'race': [], 'integration': [], 'e2e': []}
for r in ranked:
    for gap in r['gaps']:
        gap_type = gap.get('Type', 'unit')
        by_type.setdefault(gap_type, []).append(r['feature'])

print("=== SPRINT PRIORITIES (top 20) ===")
for r in ranked[:20]:
    print(f"  {r['score']:5.2f}  {r['priority']:8s}  {r['health']:5.0%} -> {r['target']:.0%}  {r['feature']}")

print("\n=== BY FIX TYPE ===")
for gap_type, features in by_type.items():
    if features:
        print(f"  {gap_type}: {len(features)} features")
```

**Proposed native feature:**
```bash
testreg sprint                            # full sprint planning output
testreg sprint -n 20                      # top 20 priorities
testreg sprint --group-by type            # group by gap type
testreg sprint --group-by domain          # group by domain
testreg sprint --priority critical,high   # filter to critical+high only
```

**Expected output:**
```
Sprint Priorities (20 features, sorted by priority score):

  Score  Priority  Health  Target  Feature
  4.00   critical    0%    100%   auth.login
  4.00   critical    0%    100%   auth.register
  3.00   critical   25%    100%   auth.token-refresh
  2.40   high       20%     80%   recipes.create
  ...

By Fix Type:
  unit tests:        34 features (estimated 2-3 hours)
  integration tests: 18 features (estimated 4-6 hours)
  benchmarks:        12 features (estimated 1-2 hours)
  race tests:         8 features (estimated 1-2 hours)
  e2e tests:          6 features (estimated 3-4 hours)
```

**Priority:** HIGH -- this is the highest-value missing command. It would replace the most frequently written ad-hoc script.

---

### Gap 12: Before/after comparison (health deltas)

**Situation:** After a testing sprint, needed to see what improved. Manually compared two audit outputs by copying numbers into a spreadsheet.

**Current workaround:**
```bash
# Save baseline
testreg audit --format json > /tmp/audit-before.json

# ... do work ...

# Compare
testreg audit --format json > /tmp/audit-after.json

python3 -c "
import json

before = {f['FeatureID']: f['HealthScore'] for f in json.load(open('/tmp/audit-before.json'))}
after = {f['FeatureID']: f['HealthScore'] for f in json.load(open('/tmp/audit-after.json'))}

deltas = []
for fid in after:
    b = before.get(fid, 0)
    a = after[fid]
    if a != b:
        deltas.append((a - b, fid, b, a))

deltas.sort(key=lambda x: -x[0])
for delta, fid, b, a in deltas:
    arrow = '+' if delta > 0 else ''
    print(f'  {arrow}{delta:5.0%}  {fid:40s}  {b:.0%} -> {a:.0%}')
"
```

**Proposed native feature:**
```bash
testreg diff                              # compare current vs last saved snapshot
testreg diff --baseline audit-before.json # compare against specific baseline
testreg audit --save-snapshot sprint-1    # save named snapshot
testreg diff --from sprint-1 --to sprint-2
```

**Expected output:**
```
Health Changes (since sprint-1):

  Improved (12 features):
    +100%  auth.login                      0% -> 100%
    + 74%  client-management.detail        0% ->  74%
    + 50%  recipes.create                 30% ->  80%
    ...

  Regressed (2 features):
    - 10%  meals.log.create              80% ->  70%
    ...

  Unchanged: 170 features

  Summary: +15.4% average health improvement
```

**Priority:** MEDIUM -- important for measuring progress but can be worked around.

---

### Gap 13: Batch gap extraction for AI/subagent prompts

**Situation:** When dispatching parallel subagents to fix test gaps, needed to extract actionable gap information in a structured format that could be fed directly into prompts.

**Current workaround:**
```bash
for feat in auth.login auth.register recipes.create; do
    testreg audit "$feat" --format json | python3 -c "
import json, sys
data = json.load(sys.stdin)
for f in data:
    print(f'Feature: {f[\"FeatureID\"]}')
    print(f'Priority: {f[\"Priority\"]}')
    print(f'Health: {f[\"HealthScore\"]:.0%}')
    for gap in f.get('Gaps', []):
        print(f'  GAP [{gap[\"Severity\"]}]: {gap[\"NodeID\"]} -- {gap[\"Description\"]}')
        print(f'       File: {gap[\"File\"]}')
        print(f'       Fix:  Write {gap[\"FixType\"]} test')
    print()
"
done
```

**Proposed native feature:**
```bash
testreg gaps --priority critical             # all gaps for critical features
testreg gaps --feature auth.login            # gaps for one feature
testreg gaps --format actionable             # structured output for automation
testreg gaps --format prompt                 # output formatted for AI consumption
```

**Expected `--format actionable` output:**
```
Feature: auth.login (critical, health: 74%)
  [CRITICAL] authService.Login -- no unit test
    File: src/application/services/auth_service.go:172
    Action: Write unit test in src/application/services/auth_service_test.go
    Pattern: table-driven test with mock repository

  [HIGH] authRepository.StoreRefreshToken -- no integration test
    File: src/domain/repositories/auth_repository.go:329
    Action: Write integration test with real database
    Pattern: TestMain setup with test database
```

**Priority:** HIGH -- directly enables automated test gap fixing workflows.

---

## Section 5: Registry Management Gaps

### Gap 14: Find features with no API surface

**Situation:** Features defined in registry YAML but with no `api_surfaces` field are invisible to the graph scanner. The audit shows them as 0% health with 0 gaps, which is misleading -- they are not "untested," they are "unconfigured."

**Current workaround:**
```bash
testreg audit --format json | python3 -c "
import json, sys

data = json.load(sys.stdin)
unconfigured = [f for f in data if f['HealthScore'] == 0 and f.get('GapCount', 0) == 0]
print(f'Unconfigured features ({len(unconfigured)}):')
for f in sorted(unconfigured, key=lambda x: x['Priority']):
    print(f'  {f[\"Priority\"]:8s}  {f[\"FeatureID\"]}')
"
```

**Proposed native feature:**
```bash
testreg check --unconfigured
```

**Expected output:**
```
Unconfigured Features (32 features with no API surfaces):

  critical (3):
    auth.2fa-setup
    auth.account-recovery
    auth.session-management

  high (12):
    plans.nutritionist.approve
    plans.nutritionist.archive
    ...

  Action: Add api_surfaces to these features in docs/testing/registry/*.yaml
```

**Priority:** MEDIUM -- prevents silent misses during onboarding.

---

### Gap 15: Bulk scan + audit in one command

**Situation:** Every workflow required running `testreg scan` followed by `testreg audit`. Forgetting to scan first means the audit uses stale data. This was a constant friction point.

**Current workaround:**
```bash
testreg scan && testreg audit
testreg scan && testreg audit --priority critical --min-health 0.5
```

**Proposed native feature:**
```bash
testreg audit --rescan             # auto-scan before auditing
testreg audit --rescan --priority critical
```

**Priority:** LOW -- trivial to chain with `&&`, but would reduce a common friction point.

---

## Section 6: Proposed New Commands/Flags Summary

| # | Gap | Current Workaround | Proposed Feature | Priority |
|---|-----|--------------------|-----------------|----------|
| 1 | Filter by priority | `python3 -c "filter Priority"` | `testreg audit --priority critical` | HIGH |
| 2 | Health threshold | `python3 -c "filter HealthScore"` | `testreg audit --min-health 0.5` (exists) | -- |
| 3 | Sort by priority score | `python3 -c "weight * delta, sort"` | `testreg audit --sort priority-score` | HIGH |
| 4 | Summary counts | `python3 Counter` | `testreg audit --summary` | MEDIUM |
| 5 | Missing surfaces | `python3 -c "health==0 and gaps==0"` | `testreg audit --unconfigured` | MEDIUM |
| 6 | Flat node list | `python3 tree walk` | `testreg trace --list-nodes` | LOW |
| 7 | Duplicate node check | `python3 Counter on IDs` | `testreg trace --validate` | LOW |
| 8 | Aggregate trace stats | `subprocess per feature` | `testreg trace --all --summary` | LOW |
| 9 | Profile testreg | `/usr/bin/time -v` | `testreg audit --profile` | LOW |
| 10 | Per-feature timing | `bash loop with date` | `testreg audit --per-feature-timing` | LOW |
| 11 | Sprint planning | 30-line python script | `testreg sprint` command | HIGH |
| 12 | Before/after diff | manual json comparison | `testreg diff` command | MEDIUM |
| 13 | Actionable gaps export | `python3 gap extraction` | `testreg gaps --format actionable` | HIGH |
| 14 | Unconfigured features | `python3 filter` | `testreg check --unconfigured` | MEDIUM |
| 15 | Auto-rescan before audit | `scan && audit` | `testreg audit --rescan` | LOW |

---

## Implementation Priority

### Phase 1 -- High Impact (4 items)

These address the most painful daily-use gaps:

1. **`--priority` flag on audit** -- simple filter, high usage frequency
2. **`--sort priority-score`** -- enables data-driven sprint decisions
3. **`testreg sprint` command** -- replaces the most complex ad-hoc script
4. **`testreg gaps --format actionable`** -- enables automated gap-fixing workflows

### Phase 2 -- Medium Impact (4 items)

These improve dashboard/reporting workflows:

5. **`testreg audit --summary`** -- aggregate counts per priority tier
6. **`testreg diff` command** -- measure progress across sprints
7. **`testreg audit --unconfigured`** -- surface registry configuration gaps
8. **`testreg check --unconfigured`** -- registry health validation

### Phase 3 -- Low Impact (7 items)

Nice-to-have for power users and automation:

9. **`testreg trace --list-nodes`** -- flat output for scripting
10. **`testreg trace --validate`** -- graph integrity checks
11. **`testreg trace --all --summary`** -- aggregate graph statistics
12. **`testreg audit --rescan`** -- convenience flag
13. **`testreg audit --per-feature-timing`** -- performance profiling
14. **`testreg audit --profile`** -- extended profiling
15. **`testreg trace --validate-all`** -- bulk validation

---

## Design Notes

### JSON output as escape hatch

The `--format json` flag on all commands is critical infrastructure. Even with all these native features, users will always have novel queries. The JSON output path should remain the primary extensibility mechanism.

### Flag composition

New flags should compose with existing ones:

```bash
# These should all work together:
testreg audit --priority critical --sort priority-score --min-health 0.5
testreg audit --priority critical --summary
testreg sprint --priority critical,high -n 10 --group-by type
```

### Snapshot storage for diff

The `testreg diff` command needs a snapshot storage mechanism. Suggested approach:

```
.testreg-cache/
  snapshots/
    2026-03-30T14:00:00.json
    sprint-1.json              # named snapshots
    latest.json                # auto-saved on each audit
```

Each `testreg audit` could auto-save to `latest.json`, and `testreg diff` compares current against `latest.json` by default. Named snapshots via `testreg audit --save-snapshot <name>`.
