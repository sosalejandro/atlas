# goldencorpus

A deliberately small but real-shaped Go tree used as the fixture for the
determinism suite and the golden snapshot (see `docs/testing/determinism.md`).

It is not a toy: the layout mirrors a layered HTTP service (cmd -> handlers ->
services -> persistence, plus a platform package and an outward-facing
client package), and it carries every scanner hazard we have been bitten by:

- two packages that both declare a type named `Config` with a `Validate`
  method, so their SymbolIDs collide;
- unexported plain functions (dropped by the scanner) next to unexported
  methods (kept);
- an interface with two implementations, so interface-typed calls have no
  single concrete callee and get dropped;
- two handler types with the same method name, so route handler resolution
  has more than one candidate;
- generated code in both shapes tools emit it: alongside hand-written code,
  and in its own `generated/` subtree.

Nothing here compiles or is meant to. The scanner is AST-only (no go/types,
no module resolution), so the import paths are fictional on purpose.

Changing a file under this directory changes the golden snapshot. That is the
point: regenerate it with the documented command and explain the diff.
