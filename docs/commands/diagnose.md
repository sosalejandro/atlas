# atlas diagnose

`atlas diagnose` matches a free-form symptom string (typically an error
message or log line snippet) against the persisted symbol bodies in the
SQLite store and returns the symbols most likely to have produced it. It's
the triage tool for "we got this error in prod, where in the code would it
come from".

Confidence is a 0–1 score combining symptom-substring frequency, per-token
matches, and symbol body length. `--max-results` caps how many candidates
come back; `--min-confidence` drops weaker matches.

## Usage

```
atlas diagnose <symptom> [flags]
```

## Flags

| Flag                          | Default               | Description                                                                       |
| ----------------------------- | --------------------- | --------------------------------------------------------------------------------- |
| `--max-results`               | `10`                  | Cap the number of matches returned. `0` means "no cap".                           |
| `--min-confidence`            | `0`                   | Drop matches below this confidence floor (0..1).                                  |
| `--config` *(global)*         | `.atlas.yaml` lookup  | Explicit config path.                                                             |
| `--db-path` *(global)*        | `.atlas/atlas.db`     | Override the SQLite state path.                                                   |
| `--json` *(global)*           | off                   | Emit the stable JSON envelope instead of human-friendly text.                     |
| `-v`, `--verbose` *(global)*  | off                   | Verbose human-readable output.                                                    |

## Examples

### Triage an error string

```
# Run from: /tmp/atlas-fixture
$ atlas diagnose "Authenticate"
  0.450  AuthHandler.Login                                   go/auth.go:14  [feature=auth.login]
    matched whole symptom 2x in body; matched 2 symptom tokens
  0.425  AuthService.Authenticate                            go/auth.go:26  [feature=-]
    matched whole symptom 1x in body; matched 1 symptom tokens
```

Two candidates ranked by confidence. The body of `AuthHandler.Login`
contains the literal substring "Authenticate" twice (the method body calls
`h.svc.Authenticate(...)` and the comment mentions it), so it edges out
the actual `Authenticate` method itself. The `[feature=...]` tag is
populated when the symbol is linked to a feature via `@atlas:feature` or
`@atlas:contract` — `-` means no feature linkage in the store.

### Lower the confidence floor

By default `--min-confidence` is 0; passing a higher floor drops weaker
matches:

```
# Run from: /tmp/atlas-fixture
$ atlas diagnose "user-123" --min-confidence 0.1
  0.467  AuthService.Authenticate                            go/auth.go:26  [feature=-]
    matched whole symptom 1x in body; matched 2 symptom tokens
  0.350  AuthHandler.Login                                   go/auth.go:14  [feature=auth.login]
    matched whole symptom 1x in body; matched 4 symptom tokens
  0.242  AuthService.IssueToken                              go/auth.go:30  [feature=-]
    matched 1 symptom tokens
  0.167  py.billing                                          py/billing.py:1  [feature=-]
    matched 4 symptom tokens
  0.167  py.billing.BillingHandler                           py/billing.py:4  [feature=-]
    matched 4 symptom tokens
```

The Python candidates score 0.167 because they don't contain the literal
string — only tokens overlapped. Raising `--min-confidence 0.3` drops
them.

### No matches

```
# Run from: /tmp/atlas-fixture
$ atlas diagnose "invalid credentials"
diagnose: no candidates matched the symptom
```

A clean "no match" return is intentionally informative — it tells the
operator the index doesn't carry a body with that substring, so the
symptom is likely coming from external code or a runtime/library frame.

## How it works

1. Tokenize the symptom on whitespace + punctuation; lowercase the tokens.
2. Stream rows from the `symbols` table, joining `symbol_bodies` for the
   parsed source text.
3. For each symbol:
   - Count exact substring hits of the full symptom (`matched whole symptom Nx`).
   - Count distinct symptom-token hits in the body.
   - Combine into a confidence score (weighted toward exact-substring hits;
     normalised by body length so short symbols can compete with long ones).
4. Sort candidates by descending confidence, cap to `--max-results`, drop
   below `--min-confidence`.

The scorer is purely lexical — no semantic embedding, no fuzzy spelling
matcher. It's optimised for the case where the symptom is verbatim text
that came from a `fmt.Errorf` / `throw new Error(...)` / `raise Exception(...)`
site somewhere in the indexed code.
