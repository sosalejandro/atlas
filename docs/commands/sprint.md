# testreg sprint

Rank features by priority-weighted gap score for sprint planning.

## Score formula

```
score = weight * max(0, target - health)
```

| Priority | Weight | Target |
|----------|--------|--------|
| critical | 4 | 100% |
| high | 3 | 80% |
| medium | 2 | 60% |
| low | 1 | 40% |

Features at or above target are excluded.

## Usage

```
testreg sprint [flags]
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--format` | `terminal` | Output format: `terminal`, `json` |
| `-n, --limit` | `20` | Top N results |
| `--priority` | (all) | Filter by priority (comma-separated) |
| `--group-by` | (none) | Group output by: `type`, `domain` |

## Examples

### Default sprint output

```
$ testreg sprint

Sprint Priorities (10 features, sorted by priority score):

   Score  Priority   Health  Target  Feature
  ──────────────────────────────────────────────────────────────────────
    2.40  high          0%     80%  shopping.create
    2.40  high          0%     80%  plans-patient.list
    1.65  high         25%     80%  auth.biometric-login
    1.65  high         25%     80%  training.session-indicator
    1.20  high         40%     80%  settings.account
    1.20  high         40%     80%  settings.privacy
    1.20  high         40%     80%  recovery.score
    1.20  high         40%     80%  recovery.readiness
    1.20  medium        0%     60%  shopping.recipe-from-inventory
    1.20  medium        0%     60%  admin.api-docs
```

### Group by domain

```
$ testreg sprint --group-by domain -n 10

  auth (3 features, score: 11.00):
    4.00  critical   0%  auth.login
    4.00  critical   0%  auth.register
    3.00  critical  25%  auth.token-refresh

  recipes (2 features, score: 4.80):
    2.40  high      20%  recipes.create
    2.40  high      20%  recipes.search
```

### Critical and high only

```bash
testreg sprint --priority critical,high -n 10
```

## Tips

- Run at the start of each sprint to decide what to fix.
- Use `--group-by domain` to assign test-writing work by team or domain owner.
- Combine with `testreg gaps --format prompt` to hand off specific fixes to AI agents.
