# testreg status

Display a terminal table with coverage metrics per domain and platform.

## Usage

```
testreg status [flags]
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--domain` | (all) | Filter by domain name |
| `--priority` | (all) | Filter by priority: `critical`, `high`, `medium`, `low` |
| `--format` | `table` | Output format: `table`, `json` |

## Examples

### Full dashboard

```
$ testreg status

  Test Coverage Registry
  Generated: now  |  Project: nutrition-project-v2

  ┌──────────────────────┬───────┬──────────┬──────────┬──────────┐
  │ Domain               │ Total │ Unit     │ Integ.   │ E2E      │
  ├──────────────────────┼───────┼──────────┼──────────┼──────────┤
  │ admin               │ 12   │ 6/12 ✓  │ 7/12 ✓  │ 3/12 ✓  │
  │ auth                │ 10   │ 9/10 ✓  │ 6/10 ✓  │ 5/10 ✓  │
  │ billing             │ 14   │ 9/14 ✓  │ 8/14 ✓  │ 3/14 ✓  │
  │ client-analytics    │ 10   │ 6/10 ✓  │ 4/10 ✓  │ 4/10 ✓  │
  │ client-management   │ 12   │ 8/12 ✓  │ 3/12 ✓  │ 7/12 ✓  │
  │ communication       │ 11   │ 5/11 ✓  │ 8/11 ✓  │ 2/11 ✓  │
  │ infra               │ 15   │ 10/15 ✓ │ 10/15 ✓ │ 2/15 ✓  │
  │ meals               │ 12   │ 9/12 ✓  │ 6/12 ✓  │ 4/12 ✓  │
  │ patient-dashboard   │ 5    │ 2/5 ✓   │ 3/5 ✓   │ 2/5 ✓   │
  │ plans-nutritionist  │ 14   │ 11/14 ✓ │ 11/14 ✓ │ 9/14 ✓  │
  │ plans-patient       │ 8    │ 2/8 ✓   │ 3/8 ✓   │ 3/8 ✓   │
  │ recipes             │ 10   │ 10/10 OK│ 7/10 ✓  │ 6/10 ✓  │
  │ recovery            │ 14   │ 4/14 ✓  │ 4/14 ✓  │ 3/14 ✓  │
  │ settings            │ 15   │ 9/15 ✓  │ 6/15 ✓  │ 5/15 ✓  │
  │ shopping            │ 10   │ 10/10 OK│ 4/10 ✓  │ 4/10 ✓  │
  │ training            │ 12   │ 12/12 OK│ 4/12 ✓  │ 6/12 ✓  │
  ├──────────────────────┼───────┼──────────┼──────────┼──────────┤
  │ TOTAL               │ 184  │ 66% ~~  │ 51% ~~  │ 37% !!  │
  └──────────────────────┴───────┴──────────┴──────────┴──────────┘

  Critical gaps: 7 critical features missing E2E coverage
```

*Output from nutrition-project-v2 — 184 features across 16 domains.*

### Filter by domain

```bash
testreg status --domain auth
```

### Filter by priority

```bash
testreg status --priority critical
```

## Tips

- `status` shows registry-level coverage (annotation-based), not graph-level health scores
- For graph-aware health scores, use `testreg audit` instead
- Use `--format json` for CI integration
