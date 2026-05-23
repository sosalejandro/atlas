# atlas contract

`atlas contract` groups the API-contract-extraction verbs. Today there's a
single subcommand — `list` — that runs the contract extractor against a
fresh codeindex scan and prints every discovered contract.

A "contract" is one of:

- **`huma-op`** — Huma framework operation (Go).
- **`route`** — HTTP route discovered through the router-parse pipeline
  (Chi, Echo, stdlib, Huma).
- **`func`** — Plain Go/TS function or method carrying an `@atlas:contract`
  annotation.
- **`graphql`** — GraphQL query / mutation / subscription operation.

## Usage

```
atlas contract list [flags]
```

## Flags

| Flag                          | Default               | Description                                                                                       |
| ----------------------------- | --------------------- | ------------------------------------------------------------------------------------------------- |
| `--kind`                      | (all)                 | Filter by `ContractKind` — one of `huma-op`, `route`, `func`, `graphql`.                          |
| `--root`                      | repo root / cwd       | Project root for the scan.                                                                        |
| `--config` *(global)*         | `.atlas.yaml` lookup  | Explicit config path.                                                                             |
| `--db-path` *(global)*        | `.atlas/atlas.db`     | Override the SQLite state path.                                                                   |
| `--json` *(global)*           | off                   | Emit the stable JSON envelope instead of human-friendly text.                                     |
| `-v`, `--verbose` *(global)*  | off                   | Verbose human-readable output.                                                                    |

## Examples

### List every contract

```
# Run from: /tmp/atlas-fixture
$ atlas contract list
contracts: 3
  [func/go] Login  go/auth.go:14
    sig: func (*AuthHandler) Login(ctx context.Context, email string, password string) (string, error)
  [func/go] Authenticate  go/auth.go:26
    sig: func (*AuthService) Authenticate(ctx context.Context, email string, password string) (string, error)
  [func/go] IssueToken  go/auth.go:30
    sig: func (*AuthService) IssueToken(ctx context.Context, userID string) (string, error)
```

Three Go functions surface as contracts in this fixture. The
`[func/go]` tag is `<kind>/<language>`. Each entry shows the symbol name,
its source position, and the extracted signature. On a real-world repo
this view also includes `[huma-op]`, `[route]`, and `[graphql]` entries.

### Filter by kind

```
# Run from: /tmp/atlas-fixture
$ atlas contract list --kind func
contracts: 3
  [func/go] Login  go/auth.go:14
    sig: func (*AuthHandler) Login(ctx context.Context, email string, password string) (string, error)
  [func/go] Authenticate  go/auth.go:26
    sig: func (*AuthService) Authenticate(ctx context.Context, email string, password string) (string, error)
  [func/go] IssueToken  go/auth.go:30
    sig: func (*AuthService) IssueToken(ctx context.Context, userID string) (string, error)
```

In the fixture every contract is `func`, so `--kind func` returns the same
three rows. On a backend repo with router-discovered HTTP endpoints,
`--kind route` is the typical scoping flag — useful for "show me every
REST surface my backend exposes".

### JSON envelope

```
# Run from: /tmp/atlas-fixture
$ atlas contract list --json | jq '.result.contracts[0]'
{
  "kind": "func",
  "language": "go",
  "symbol_name": "Login",
  "file": "go/auth.go",
  "line": 14,
  "signature": "func (*AuthHandler) Login(ctx context.Context, email string, password string) (string, error)"
}
```

The `result.contracts` array carries one object per row.

## How it works

1. Run a fresh codeindex scan (always live — `contract list` does not read
   from the cached store; it re-extracts on every invocation).
2. Walk the discovered symbols and apply each extractor:
   - **Huma extractor** — looks for `huma.Register` call sites.
   - **Route extractor** — feeds the router-parse pipeline.
   - **Func extractor** — picks up `@atlas:contract` annotations.
   - **GraphQL extractor** — parses `gqlgen` / `graphql-tools` resolvers.
3. Emit the union, sorted by file:line.

A future verb (`atlas contract diff`) will compare two extracted contract
sets across snapshots — useful for "did this PR remove an HTTP route".
Today the diff is available only through [`atlas diff`](./diff.md)'s
`contracts:` slice.
