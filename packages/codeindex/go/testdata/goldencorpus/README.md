# goldencorpus

A deliberately small but real-shaped Go tree used as the fixture for the
determinism suite and the golden snapshot (see `docs/testing/determinism.md`).

It is not a toy: the layout mirrors a layered HTTP service (cmd -> handlers ->
services -> persistence, plus a platform package and an outward-facing
client package), and it carries every scanner hazard we have been bitten by:

- two packages that both declare a type named `Config` with a `Validate`
  method, so their SymbolIDs collide — and `internal/platform/config.Load`
  calls the one it owns, so the snapshot records which of the two a call
  binds to, not merely that both exist;
- unexported plain functions (dropped by the scanner) next to unexported
  methods (kept);
- an interface with two implementations, so an interface-typed call has no
  single concrete callee;
- a generic type (`collections.Cache[K, V]`) whose methods are called
  through an instantiated field, so a resolver that keys on the
  instantiated type instead of the declaration fragments the symbol table;
- an external test package (`orders_test`), because the in-package form
  would close an import cycle;
- two handler types with the same method name, so route handler resolution
  has more than one candidate;
- generated code in both shapes tools emit it: alongside hand-written code,
  and in its own `generated/` subtree.

## It compiles, and that is load-bearing

This directory used to say "nothing here compiles or is meant to". That
changed with issue #87. The Go scanner now resolves calls with
`go/packages` + `go/types`, and the type checker needs a module boundary
and a build that succeeds — so the fixture that pins the scanner's output
has to be one, or the golden snapshot would only ever exercise the AST
fallback.

Consequences worth knowing before editing:

- `go.mod` declares `module example.com/orderd`. The directory is a
  separate module from atlas; `testdata/` is skipped by the go tool, so
  `go build ./...` at the repo root never sees it.
- **`go build ./...` and `go test ./...` must pass inside this directory.**
  A type error here does not fail loudly — it degrades the scan of this
  package to name resolution, and the only symptom is tiers moving in the
  snapshot.
- The `OrderRepository` port lives in `internal/services/orders`, beside
  its consumer, not beside its implementations. That is what breaks the
  import cycle the fixture used to have (persistence needs `orders.Order`
  in its signatures, so orders cannot import persistence).
- `testdata/brokencorpus` is the deliberate opposite: a module with one
  package that does not type-check, pinning that the scanner degrades per
  package instead of failing.

Changing a file under this directory changes the golden snapshot. That is the
point: regenerate it with the documented command and explain the diff.
