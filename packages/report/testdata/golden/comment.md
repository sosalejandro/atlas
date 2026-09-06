<!-- atlas-report:sticky -->
## Atlas report

| Metric | Value |
| --- | --- |
| Features scored | 18 |
| Worst score | 31.0 (billing.invoice) |
| Statements attributed | 4812 / 4930 (97.6%) |

### Change since `main`

| Feature | Before | After | Delta |
| --- | ---: | ---: | ---: |
| `billing.invoice` | 58 | 31 | -27 |
| `auth.login` | 71 | 80 | +9 |

New features: `billing.pdf`

Removed features: `billing.legacy-export`

### Findings

**4 findings** — 1 error, 1 warning, 2 notices.

#### atlas/contract-drift — A declared contract no longer matches its implementation (1)

- `internal/api/routes.go:15` — contract GET /v1/invoices has drifted from its handler

#### atlas/feature-uncovered — A feature's health score is below the configured floor (1)

- `internal/billing/invoice.go:42` — feature billing.invoice scores 31.0/100 (coverage 12.5)

#### atlas/coverage-unattributed — Executed statements atlas could not charge to any indexed symbol (1)

- `internal/worker/queue.go:1` — 118 statements executed but charged to no indexed symbol (reason: no-indexed-symbol)

#### atlas/dead-code — A symbol with no qualifying incoming edges (1)

- `internal/legacy/shim.go:7` — Shim has 0 incoming import edges (dead-code candidate)
