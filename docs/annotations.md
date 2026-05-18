# Atlas Annotations — Grammar Reference

Annotations are how code declares its membership in a **feature**. Atlas derives
the entire feature registry from annotations alone — there is no parallel YAML
file to maintain, no out-of-band index to sync, and no separate "registry
owner" role. The code is the registry.

This shift removes the single largest source of drift in the old `testreg`
workflow: the hand-edited YAML files in `docs/testing/registry/`. With Atlas,
when you delete an annotated test, the feature instance is automatically
dropped from the next scan. When you rename a feature ID, a single grep
captures every site. When you onboard a new contributor, the rule is
"annotate the code you write" — nothing to teach beyond the grammar on this
page.

The parser lives at `packages/codeindex/annotations/` (porting the legacy
`internal/adapters/annotation_parser.go` from testreg with extended grammar
support — see the [Legacy reader](#legacy-reader-testreg) section).

---

## TL;DR

```go
// @atlas:feature auth.login #real            ← new (recommended)
// @testreg auth.login #real                  ← legacy (still works)
```

Both are parsed. Both produce the same internal record. The new form reserves
namespace for future kinds (`contract`, `owner`, `deprecated`, `since`, ...)
without grammar ambiguity. The `atlas migrate-annotations` command bulk-renames
legacy → new when you're ready.

---

## The new grammar

```
@atlas:<kind> <id> [<id>...] [<tag>...]
```

Tokens, separated by whitespace:

| Token  | Required | Description                                                                                       |
| ------ | -------- | ------------------------------------------------------------------------------------------------- |
| `kind` | yes      | One of the [known kinds](#known-kinds). Closed enum at parse time, advisory-warned if unrecognised |
| `id`   | yes      | Dot-namespaced feature identifier (`admin.dashboard`, `auth.login.oauth`). 1+ allowed             |
| `tag`  | no       | `#`-prefixed conventional tag (`#real`, `#mocked`). Multiple allowed. Must come **after** all IDs |

### Parser rules

1. **One annotation per comment line.** The parser extracts the first
   `@atlas:` match per line. Two annotations on the same line are
   undefined behavior — split them across two comments.
2. **Multi-line comments are unwrapped to one logical line.** A `/* ... */`
   block (TS/Go) or `<!-- ... -->` block (Markdown/HTML) is collapsed before
   matching, so an annotation can wrap across lines if you prefer block style.
3. **Whitespace is the only separator.** Commas in IDs are tolerated for
   testreg backward compat but the new form is strictly space-separated.
4. **Tags must follow IDs.** `@atlas:feature checkout.cart #real` is fine.
   `@atlas:feature #real checkout.cart` parses incorrectly — the first
   `#tag` token terminates the ID list.
5. **IDs are validated by kind:**
   - `feature` / `contract` — strict regex `[a-z0-9_]+(\.[a-z0-9_]+)*`. Case-sensitive lowercase + dot-namespacing. `Auth.Login` is rejected; `auth.login` is canonical.
   - `owner` / `deprecated` / `since` — relaxed, accepts any non-whitespace token. This is necessary because owners (`platform-team`, `@alice`) and versions (`v2.0`, `1.4.0-beta`) don't fit the dot-namespace shape.
   The implementation in `packages/codeindex/annotations/parser.go` enforces these two grammars per kind. New kinds added via the registration mechanism (see "Forward compatibility") choose their own validator at registration time.

---

## Known kinds

The initial closed set of `<kind>` values:

| Kind          | Purpose                                                          | Example                          |
| ------------- | ---------------------------------------------------------------- | -------------------------------- |
| `feature`     | Code (typically a test) belongs to a named feature               | `@atlas:feature auth.login`      |
| `contract`    | Function/method is the feature's public API surface              | `@atlas:contract auth.login`     |
| `owner`       | Team or person responsible for the code                          | `@atlas:owner platform-team`     |
| `deprecated`  | Code scheduled for removal in the named version                  | `@atlas:deprecated v2.0`         |
| `since`       | Version the feature was introduced                               | `@atlas:since v1.4.0`            |

`feature` is by far the most common — most test files only ever use that one.
The other four exist so a single grammar covers ownership, lifecycle, and API
surface metadata without inventing new comment dialects later.

### Future kinds

Adding a new kind is non-breaking. The parser accepts any `@atlas:<word>`
shape; unknown kinds are recorded as advisory entries (a one-time warning per
unique unknown kind) until a handler is registered. See
[Forward compatibility](#forward-compatibility).

---

## Per-language comment syntax

The annotation lives inside any line the language treats as a comment. The
`@atlas:` token must follow a `@`, so prose mentions of "atlas:feature" outside
a `@` prefix are safely ignored.

### Go

```go
// @atlas:feature auth.login #real
func TestLogin_Success(t *testing.T) {
    // ...
}
```

Method receivers / block comments work the same:

```go
/*
@atlas:contract auth.login
@atlas:owner platform-team
*/
func (h *AuthHandler) Login(ctx context.Context, req LoginReq) (LoginResp, error) {
    // ...
}
```

### TypeScript / TSX

Both line and block forms parse identically:

```ts
// @atlas:feature checkout.cart
test('adds item to cart', async () => { /* ... */ });

/* @atlas:feature checkout.cart #real */
test.describe('cart e2e', () => { /* ... */ });
```

JSDoc blocks count as block comments:

```ts
/**
 * @atlas:feature web.dashboard
 * @atlas:owner frontend-team
 */
export function Dashboard() { /* ... */ }
```

### Python (future)

Python scanner is **dropped from v0** of Atlas, but the grammar is reserved:

```py
# @atlas:feature analytics.daily_rollup
def test_daily_rollup():
    ...
```

### Markdown

For docs that themselves participate in a feature (runbooks, ADRs, etc.):

```md
<!-- @atlas:feature ops.incident_response #docs -->

# Incident Response Runbook
```

Markdown front-matter is also accepted; the parser scans the file as-is and
matches against `<!-- ... -->` blocks anywhere.

---

## Multi-feature annotations

A single test or function can belong to multiple features. List the IDs
space-separated; tags follow:

```go
// @atlas:feature checkout.cart checkout.shipping checkout.tax #real
func TestCheckoutE2E_FullFlow(t *testing.T) {
    // exercises 3 features in one test
}
```

```ts
// @atlas:feature web.dashboard web.notifications #mocked
test('dashboard renders notifications panel', () => { /* ... */ });
```

The parser produces one feature-instance record per ID, all sharing the same
tag set and source position. This matches the legacy `@testreg` semantics
exactly — see the [Legacy reader](#legacy-reader-testreg) section.

---

## Tags reference

Tags are `#`-prefixed conventional markers. The parser collects them verbatim;
project-specific tags are allowed, and lint warns on unknown ones but does
not reject them.

### Conventional tags

| Tag       | Meaning                                                      |
| --------- | ------------------------------------------------------------ |
| `#real`   | Runs against real infrastructure (DB, Redis, network, etc.)  |
| `#mocked` | Uses test doubles for external dependencies                  |
| `#flaky`  | Known intermittent failure — surfaced in audit reports       |
| `#slow`   | Long-running test (>30s typical) — excluded from PR-fast tier |

### Project-specific tags

Anything starting with `#` is captured. Examples a team might add:

```go
// @atlas:feature reports.export #real #pii #nightly
func TestExportPII_RealDB(t *testing.T) { /* ... */ }
```

`atlas lint --tags` lists tag usage across the codebase and flags any tag
that appears fewer than 3 times (a common typo signal — `#mockd` vs `#mocked`).

---

## Legacy reader — `@testreg`

The old grammar continues to parse correctly. Atlas maps every legacy
annotation to the `feature` kind internally — no behavior difference at scan
time.

```go
// @testreg auth.login                          ← parses as @atlas:feature auth.login
// @testreg auth.login #real                    ← parses as @atlas:feature auth.login #real
// @testreg checkout.cart checkout.shipping #real
//                                              ← parses as @atlas:feature checkout.cart checkout.shipping #real
```

Multi-id support is preserved. Comma-separated IDs (a quirk the old testreg
parser tolerated) are also still accepted:

```go
// @testreg auth.login,auth.logout #real        ← tolerated; canonicalised to space-separated
```

There is **no plan to remove legacy support**. The 1,110 existing annotations
in nutrition-v2-go are expected to live alongside new `@atlas:` annotations
indefinitely, until the team chooses to run the migration. The legacy reader
is part of Atlas v1 and v2 — removal would be a v3-or-later breaking change
preceded by deprecation cycles.

---

## `atlas migrate-annotations`

Bulk-renames legacy `@testreg` annotations to the new `@atlas:feature` form.
Sed-based and language-aware: respects per-language comment delimiters, never
crosses file boundaries, idempotent on already-migrated code.

### Usage

```bash
atlas migrate-annotations [--apply] [--scope <path>] [--keep-tags]
```

| Flag           | Default       | Description                                                                  |
| -------------- | ------------- | ---------------------------------------------------------------------------- |
| (no flag)      | `--dry-run`   | Scan only. Report counts + samples of what would change. No file writes      |
| `--apply`      | off           | Perform the rewrite. Files are edited in place                              |
| `--scope`      | repo root     | Limit to a subdirectory (`--scope src/contexts/auth`)                       |
| `--keep-tags`  | `true`        | Preserve `#tag` items verbatim in the rewritten annotation                  |

### Example: dry-run

```
$ atlas migrate-annotations
Scanning 2,320 Go + 2,139 TS files for @testreg annotations...

Found 1,110 legacy annotations across 890 files:
  864 in Go (.go)
  246 in TS (.ts, .tsx)

Sample rewrites:
  src/contexts/auth/login_test.go:42
    - // @testreg auth.login #real
    + // @atlas:feature auth.login #real

  apps/web-patient/src/features/dashboard/__tests__/dashboard.test.tsx:18
    - // @testreg web.dashboard
    + // @atlas:feature web.dashboard

  apps/web-patient/e2e/checkout.spec.ts:7
    - // @testreg checkout.cart checkout.shipping #real
    + // @atlas:feature checkout.cart checkout.shipping #real

Run with --apply to perform the rewrite.
```

### Example: apply

```
$ atlas migrate-annotations --apply --scope src/contexts/auth
Migrating @testreg → @atlas:feature in src/contexts/auth/...

Rewrote 87 annotations in 41 files.
Validation pass: 41/41 files parse cleanly with the new grammar.
Done.
```

### Idempotency

Re-running `atlas migrate-annotations --apply` on already-migrated code is a
no-op. The regex matches only `@testreg ` (with the trailing space), never
`@atlas:` — so subsequent runs find zero matches and report:

```
$ atlas migrate-annotations
Scanning... no legacy annotations found. Nothing to do.
```

### Safety

- Files are rewritten atomically (write-to-temp + rename) — no partial writes
  on crash
- Annotation tags are preserved verbatim under `--keep-tags=true` (default)
- The rewrite operates strictly on the annotation token; surrounding comment
  text is untouched
- Atlas validates each rewritten file with the new parser before moving to the
  next file; any parse failure aborts the run with the offending file:line

---

## Parser ambiguity test cases

The grammar is designed to reject ambiguity at parse time with line-precise
errors. Examples:

### Unambiguous (accepted)

```go
// @atlas:feature foo.bar #real            ← clear
// @atlas:feature foo.bar baz.qux #real    ← multi-id, clear
/* @atlas:owner alice */                   ← block comment, clear
// some prose then @atlas:feature x.y      ← annotation extracted from tail
```

### Ambiguous (rejected with error)

```go
// @atlas feature foo.bar                  ← missing colon between kind and id
//   error: parser expected '@atlas:<kind>', got '@atlas feature' at line N

// @atlas:feature                          ← no id
//   error: '@atlas:feature' requires at least one feature id at line N

// @atlas:feature foo.bar @atlas:owner alice
//   error: two annotations on one line; split into separate comments at line N

// @atlas:Feature foo.bar                  ← uppercase kind
//   error: unknown kind 'Feature' — did you mean 'feature'? at line N
```

### Not an annotation (silently ignored)

```go
// Things like atlas:feature in prose      ← no leading @, ignored
// see @atlasfeature for details           ← no colon, no space, ignored
// TODO: write @atlas annotations later    ← no kind after colon, ignored
```

The leading `@` is the anchor. Without it, the parser does not engage —
prose mentions of "atlas" or "atlas:feature" are safe.

---

## Forward compatibility

Adding a new `<kind>` later is **non-breaking** for code already on Atlas:

1. Older Atlas versions encounter the unknown kind, emit a one-time advisory
   warning, and skip the annotation
2. Newer Atlas versions handle the kind natively
3. No grammar change is required — the parser already accepts any
   `@atlas:<word>` shape

### Kind registration

Kinds are registered in `packages/codeindex/annotations/kinds.go` as a
versioned map:

```go
// Kinds are the closed enum of recognised @atlas:<kind> values.
// Adding a new kind: append to this map, document in docs/annotations.md,
// bump the parser version, and ship.
var Kinds = map[string]KindHandler{
    "feature":    featureHandler,
    "contract":   contractHandler,
    "owner":      ownerHandler,
    "deprecated": deprecatedHandler,
    "since":      sinceHandler,
    // future: "experimental", "internal-only", ...
}
```

A KindHandler receives the parsed payload (ids + tags + position) and decides
what to do with it. Most kinds produce a database row in the per-project
`atlas-state.db` SQLite store; some (like `owner`, `since`) are pure metadata
attached to the enclosing feature.

---

## What annotations are NOT

- **Don't annotate every function.** Annotate _tests_ and _contract-bearing
  handlers_. Internal helpers and utility functions are discovered by the AST
  scanner via call-graph — they inherit feature membership transitively.
- **Don't annotate third-party imports.** Vendor directories and `node_modules`
  are excluded from the scan regardless.
- **Don't put annotations in CSS, HTML, or JSON files.** Markdown is the only
  non-code exception (runbooks, ADRs, etc.). CSS/HTML carry no testable
  behavior — nothing meaningful for Atlas to track.
- **Don't use annotations as comments.** Treat `@atlas:feature` the way you
  treat a `//go:build` tag — machine-readable contract, not narrative.
- **Don't invent new kinds without registering them.** Unknown kinds parse but
  warn; the data is unrecoverable without code changes. Register first.

---

## Reference implementation

The annotation parser lives at `packages/codeindex/annotations/parser.go`,
ported and extended from testreg's `internal/adapters/annotation_parser.go`.

The extensions on top of the legacy parser:

- New regex matches `@atlas:<kind>\s+(.+)` in addition to `@testreg\s+(.+)`
- Kind dispatch via the `Kinds` map (see [Kind registration](#kind-registration))
- Stricter ID validation (`[a-z0-9_]+(\.[a-z0-9_]+)*`) under the new grammar;
  legacy `@testreg` IDs are not re-validated to preserve the 1,110 existing
  annotations untouched
- Block-comment unwrapping for `/* ... */` and `<!-- ... -->` so block-style
  annotations parse the same as line comments

The legacy fields (`AnnotatedTest`, `ExtractedFunction`, `APIAnnotation`) are
preserved verbatim — Atlas's storage layer reads the same in-memory shape the
testreg scanner produced, so downstream code (`packages/audit`, `packages/sprintplan`,
etc.) compiles unchanged against the new parser.
