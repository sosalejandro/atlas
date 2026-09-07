# Comparable output — `--stable`

Two runs of the same atlas command over the same code produce **identical
bytes** under `--stable`. Without it, they do not.

```bash
atlas doctor --stable > before.json
# ... change nothing ...
atlas doctor --stable > after.json
diff before.json after.json      # empty
```

## Why this needed a flag

Every JSON envelope carries `generated_at`. That single field makes every
output differ from itself between runs — and it is why atlas had no
comparable output at all until this shipped.

It also explains why nobody noticed. `generated_at` is RFC3339 with **second**
granularity, so a quick loop of three runs finishes inside one second and
looks perfectly stable. The first measurement taken for
[#162](https://github.com/sosalejandro/atlas/issues/162) concluded the output
*was* stable for exactly that reason. Put a real second between the runs and
four of five commands differ.

Every test comparing two runs therefore puts a full second between them.

## What `--stable` removes

Fields whose value depends on **when** or **where** the command ran, rather
than on what the code says. Two classes, both disqualifying for comparison:

| class | examples |
| --- | --- |
| temporal | `generated_at`, `sampled_at`, `parsed_at`, `last_scanned`, anything ending `_ms` / `_ns` / `_duration` |
| environmental | `db_path`, `root`, `project_root`, `input` — absolute paths differ between a laptop and a CI runner for the same commit |

It removes nothing else. `--stable` is not a summary or a quiet mode: it is
the same answer with the parts that are not about your code taken out, which
is the only form in which two answers can be compared.

`--stable` implies `--json`, because only the envelope is machine-comparable.

## What it is for

- **Checked-in artifacts.** A generated diagram
  ([#111](https://github.com/sosalejandro/atlas/issues/111)) that a drift gate
  compares against the tree has to serialise identically, or every regenerate
  is a diff and the gate reports change on every run — which is the same as
  reporting nothing.
- **Digests and receipts.** A digest computed over a payload containing a
  wall-clock stamp certifies nothing
  ([#161](https://github.com/sosalejandro/atlas/issues/161)).
- **CI comparison.** `atlas health --stable` before and after a change, diffed
  directly, with no jq incantation to strip timestamps first.

## Adding a field

If you add a field whose value depends on the run rather than the code, add it
to `volatileKeys` in `internal/cli/stable.go`. The list is explicit rather
than inferred from naming, so that this is a decision somebody makes rather
than a suffix that happens to match — with the exception of the duration
suffixes, which are added often enough that enumerating them would go stale.

## What this does not yet cover

Ordering. Atlas's current `--json` surface happens to emit stable ordering,
but **nothing pins it**: `docs/testing/determinism.md` states that container
order is deliberately not part of the contract, and every determinism test
canonicalises (sorts) before comparing. An ordering regression in a formatter
would pass the whole suite today.

That matters most for the formatters
[#107](https://github.com/sosalejandro/atlas/issues/107) will add, since Go
randomises map iteration and any formatter walking a map emits a different
file every run. Each new format must declare and test a total order; `--stable`
does not supply one.
