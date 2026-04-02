# testreg audit

Generate a unified feature health report combining dependency graph traces, test coverage, and gap analysis.

Two modes are available:

- **Single feature**: `testreg audit <feature-id>` produces a detailed report with dependency chain, coverage by layer, gaps, and recommended actions.
- **All features**: `testreg audit` produces a summary table sorted worst-first.

## Usage

```
testreg audit [feature-id] [flags]
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--all` | `false` | Show all features in summary mode |
| `--format` | `terminal` | Output format: `terminal`, `json`, `markdown` |
| `--min-health` | `0.0` | Only show features below this health score (0.0-1.0) |
| `--priority` | (all) | Filter by priority: `critical,high,medium,low` (comma-separated) |
| `--sort` | `health` | Sort order: `health`, `priority-score`, `name` |
| `-n, --limit` | (all) | Limit output to top N results |
| `--summary` | `false` | Show aggregate counts per priority tier |
| `--unconfigured` | `false` | Show features with no API surfaces |
| `--rescan` | `false` | Run scan before auditing |
| `--output` | stdout | Write to file instead of stdout |

## Examples

### Single feature audit

```
$ testreg audit auth.login

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

*Output from nutrition-project-v2.*

### All features summary table

```
$ testreg audit

┌──────────────────────────────────────────────┬──────────┬────────┬──────┬──────┬──────┬──────┐
│ Feature                                      │ Priority │ Health │ Perf │ Gaps │ E2E  │ Unit │
├──────────────────────────────────────────────┼──────────┼────────┼──────┼──────┼──────┼──────┤
│ communication.chat-send                     │ critical│  80%  │  73%│ 0   │ ✓   │ ✘   │
│ billing.subscribe                           │ critical│  90%  │   4%│ 12  │ ✓   │ ✓   │
│ plans-nutritionist.auto-generate            │ critical│  99%  │  21%│ 37  │ ✘   │ ✓   │
│ meals.log-create                            │ critical│  99%  │  40%│ 36  │ ✓   │ ✓   │
│ client-management.measurements-list         │ critical│ 100%  │   0%│ 0   │ ✓   │ ✓   │
└──────────────────────────────────────────────┴──────────┴────────┴──────┴──────┴──────┴──────┘
```

### Priority summary

```
$ testreg audit --summary

  Priority Summary:
    CRITICAL   31/36 at target  ( 5 gaps)  █████████░  86%
    HIGH       26/62 at target  (36 gaps)  ████░░░░░░  42%
    MEDIUM     14/52 at target  (38 gaps)  ███░░░░░░░  27%
    LOW         6/34 at target  (28 gaps)  ██░░░░░░░░  18%

    Overall: 77/184 features at target (42%)
```

*Output from nutrition-project-v2 -- 184 features across 16 domains.*

### Composed queries

```bash
# Worst critical features
testreg audit --priority critical --sort priority-score -n 10

# Features below 50% health
testreg audit --min-health 0.5

# Full refresh
testreg audit --rescan --priority critical --sort priority-score

# Unconfigured features
testreg audit --unconfigured
```

## Health score formula

```
Health = (handler_coverage * 0.30) + (service_coverage * 0.30) + (repository_coverage * 0.25) + (query_coverage * 0.15)
```

## Gap severity

| Severity | Condition |
|----------|-----------|
| CRITICAL | Handler or service untested |
| HIGH | Repository untested |
| MEDIUM | Query untested |
| LOW | Other |

## Tips

- Use `--rescan` to avoid forgetting to run `testreg scan` first.
- `--summary` replaces the Python scripts previously used for priority tier dashboards.
- Flags compose freely: `--priority critical --sort priority-score --min-health 0.5`.
