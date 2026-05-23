# atlas sprint

`atlas sprint` composes the audit signal with the gap-weighted prioritisation
in [`packages/sprintplan/`](../../packages/sprintplan/) and emits a ranked
backlog: which features to invest engineering time in next.

By default the full backlog is returned. Pass `--top N` to cap. The
`.atlas.yaml > sprint.default_top_n` config key applies when `--top` is
unset.

## Usage

```
atlas sprint [flags]
```

## Flags

| Flag                          | Default               | Description                                                                                               |
| ----------------------------- | --------------------- | --------------------------------------------------------------------------------------------------------- |
| `--top`                       | `0` (full backlog)    | Cap output to the top-N items. `0` means "no cap" or "use config default", whichever is more restrictive. |
| `--config` *(global)*         | `.atlas.yaml` lookup  | Explicit config path.                                                                                     |
| `--db-path` *(global)*        | `.atlas/atlas.db`     | Override the SQLite state path.                                                                           |
| `--json` *(global)*           | off                   | Emit the stable JSON envelope instead of human-friendly text.                                             |
| `-v`, `--verbose` *(global)*  | off                   | Verbose human-readable output.                                                                            |

## Examples

### Full sprint backlog

```
# Run from: /tmp/atlas-fixture
$ atlas sprint
 1. billing.subscribe                                   priority= 60.00 cost=S
    - Score 0 (critical), 0 linked symbols, cost=S
    - no audit signals available (no coverage, no aggregate, no contract, no annotation source)
 2. auth.login                                          priority= 20.00 cost=S
    - Score 100 (healthy), 1 linked symbols, cost=S
    - recency decay: 100 (recent activity boost)
```

Two features ranked by priority score. `billing.subscribe` wins because
it has no audit signals (score 0, classified `critical`) — atlas treats
the absence of signal as a higher-priority gap than a healthy feature.
`auth.login` is in the same backlog but at priority 20 because its score
is 100 ("healthy") and the recency-decay component picked up a recent
annotation touch.

The `cost=S` field is the planning-poker cost estimate threaded through
from the planner (S / M / L / XL); on this fixture every feature is S
because the codebase is tiny.

### Top-N cap

```
# Run from: /tmp/atlas-fixture
$ atlas sprint --top 1
 1. billing.subscribe                                   priority= 60.00 cost=S
    - Score 0 (critical), 0 linked symbols, cost=S
    - no audit signals available (no coverage, no aggregate, no contract, no annotation source)
```

`--top 1` collapses the output to the highest-priority feature only —
useful in CI to surface the single most-pressing gap.

### Per-config default

In `.atlas.yaml`:

```yaml
sprint:
  default_top_n: 10
```

With that config, `atlas sprint` (no `--top` flag) returns 10 rows. An
explicit `--top` on the CLI always wins over the config default.

## How it works

1. Read every feature from the store.
2. Read the latest audit score per feature.
3. For each feature, compute the priority components:
   - **Score gap** — `100 - audit_score`; weighted highest.
   - **Symbol-fan-out** — features with many linked symbols carry more
     blast radius.
   - **Recency decay** — features touched recently get a small boost so
     "the area we were just working in" stays surfaced.
   - **Cost** — planning-poker estimate (S/M/L/XL) folded into the rank.
4. Sort descending by priority.
5. Cap to `--top N` (or config default), render.

The priority formula is intentionally simple and inspectable — the
per-feature explanation lines under each row name which components fired
and what they contributed. When a feature surprises you in the rank, the
explanation tells you why.
