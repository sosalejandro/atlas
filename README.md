# Atlas

**Produces evidence. Everything else produces either an opinion or a link
somebody has to maintain.**

Atlas derives feature-level traceability from your code — which capability a
symbol implements, which tests execute it, what a change puts at risk — and
**refuses to answer when it cannot establish the answer.**

```bash
atlas onboard      # a provisional capability map, from zero annotations
atlas cov diff --base origin/main --fail-under 80
```

## Why another one of these

Every tool in this space either guesses or asks you to maintain the links
yourself.

| | What it gives you | What it cannot do |
| --- | --- | --- |
| **LSP** (`gopls`, `tsserver`) | Definitions, references, call hierarchy — live and type-accurate | Every query needs `file + line + character`. No persistence, no aggregate, no coverage, no exit code to gate on. |
| **Coverage tools** | Coverage per file, on every PR | No concept of a capability, so they cannot tell you a *feature* is untested |
| **Traceability suites** | Audit-ready matrices | The links are written by humans and maintained by humans. Manual links rot. |
| **Diagram tools** | Beautiful architecture diagrams | Nothing connects the drawing to the code, so it is stale within a month |

Atlas works **code-up** instead of requirements-down: it derives the map from
the source, and a derived link cannot rot because there is nothing to
maintain.

## What "refuses to answer" means in practice

This is the part that is unusual, so it is worth being concrete.

- **Every edge states how it was resolved** — `typed`, `name_resolved`,
  `syntactic` or `imported`. An answer built on guesses says so.
- **A stale index is not answered from.** If a file changed since the scan,
  the spans no longer describe it, and commands that join a diff against them
  refuse rather than attribute a change to the wrong function.
- **Exit codes distinguish *"I checked and it failed"* from *"I could not
  check"*** — `0` clean, `1` a real finding, `2` bad usage, `3` undetermined.
  Retrying on `3` is safe in a way retrying on `1` is not.
- **The denominators are published.** Unattributed statements, unresolved
  queries, references that lead outside the index — all counted, so an empty
  result never looks like a clean one.
- **Nothing leaves your machine**, enforced by a test that fails the build if
  any first-party package can open an outbound connection.

## Where it is strongest

Applications — code with routes, queries and capabilities. On a pure library
the feature concept has less to hold onto, and atlas will tell you so rather
than invent structure that is not there.

Go is type-checked end to end. TypeScript and Python are scanned natively, and
any language with a [SCIP](https://github.com/scip-code/scip) indexer — C#, C,
C++, Rust, Java, Scala, Kotlin, PHP, Ruby — can be read in.

## Status

Pre-release. The [roadmap](ROADMAP.md) says what is next and what would tell
us to stop; [docs/strategy/positioning.md](docs/strategy/positioning.md) says
why.

## Target structure

```
packages/                 # SRP libraries — each importable from external Go projects
├── shared/               # FilePosition, FeatureID, SymbolID, errors
├── codeindex/            # AST → symbol graph (foundation)
│   ├── go/               # Go AST scanner
│   ├── ts/               # TS scanner (embedded scanner.ts; requires `node` on PATH)
│   ├── py/               # Python scanner (embedded scanner.py; requires `python3` on PATH)
│   └── annotations/      # @atlas / @testreg parser
├── graph/                # Node / Edge model + adjacency
├── resolver/             # Wire + Fx DI introspection
├── sqlcmap/              # SQLC method ↔ SQL file mapper
├── routeparse/           # HTTP route discovery (Chi, Echo, stdlib, Huma)
├── store/                # SQLite-backed registry + cache
├── coverage/             # Test result ingestion (Go test JSON, Playwright, Vitest, Jest, Maestro)
├── audit/                # Health scoring
├── sprintplan/           # Gap-weighted prioritization
├── diff/                 # Snapshot diff
├── contract/             # API contract extraction
└── diagnose/             # Error → code matching

cmd/atlas/                # single CLI binary
internal/cli/             # cobra subcommand implementations
docs/                     # architecture / annotations / schema-v1 / migration / api
```

## Documentation

### Getting started

One command, on a repository with no annotations in it:

```bash
go install github.com/sosalejandro/atlas/cmd/atlas@latest
cd your-project
atlas onboard
```

`atlas onboard` scans the project, builds the SQL inventory, reads the HTTP
route registrations and mines git history, then derives a **provisional
capability map** from all of it — no `@atlas:feature` annotations required.
It reports what that map made visible (endpoints nothing tests, tables
written from more than one capability, code under active change with no test
reaching it, SQL advisories, dead-code candidates), states plainly what it
could **not** see, and prints the CI snippet that turns the whole thing into
a gate.

Cold-start wall time, measured with `time` on one developer laptop against
an empty state DB: **6.5 s** on this repository (619 indexed files, 4,674
symbols, Go + TypeScript + Python; three runs at 7.1 / 6.6 / 6.5 s) and
**1.2 s** on a synthetic 3,000-file pure-Go tree. Those are one machine's
numbers rather than a benchmark — `onboard` prints its own per-phase timings
so you can take the measurement on yours. See
[How long it takes](./docs/quickstart.md#how-long-it-takes).

**Inferred is not declared.** Everything `onboard` proposes is namespaced
under `provisional:`, written to `.atlas/provisional/capabilities.json`, and
absent from the features table — atlas's registry is worth something only
because a human wrote every row in it. The single path in is
`atlas onboard promote`, which writes an `@atlas:feature` annotation into
your source (dry run by default) and lets the ordinary scan pick it up.
Annotations you already have are adopted as-is and never re-proposed.

- [Quickstart](./docs/quickstart.md) — the whole first run, with recorded output
- [`atlas onboard`](./docs/commands/onboard.md) — the command reference,
  including `onboard promote` and what each test-evidence grade claims
- [Languages](./docs/languages/) — per-language usage guides
  ([Go](./docs/languages/go.md) /
  [TypeScript](./docs/languages/ts.md) /
  [Python](./docs/languages/py.md))

### Reference

- [Commands](./docs/commands/) — per-subcommand reference
  (`atlas onboard`, `init`, `scan`, `chain`, `audit`, `codebase`, `cov`,
  `diff`, `snapshot`, `sprint`, `diagnose`, `contract`, `sql`, `hotspots`,
  `migrate-annotations`)
- [Architecture](./docs/architecture.md) — package boundaries + dependency direction
- [Annotations](./docs/annotations.md) — `@atlas:<kind> <id>` grammar
- [Schema v1](./docs/schema-v1.md) — SQLite schema reference
- [Migration from testreg](./docs/migration-from-testreg.md) — cutover guide for testreg users

### Build and release

- [Install](./docs/install.md) — channels, verifying a download, reproducing a release build
- [Releasing](./docs/releasing.md) — maintainer guide: how a release is cut, what CI blocks on

## Install

Full instructions, including how to verify a download, live in
[docs/install.md](./docs/install.md).

**In GitHub Actions** — installs a verified release, puts it on `PATH`, runs it:

```yaml
- uses: sosalejandro/atlas/.github/actions/atlas@v0.14.0
  with:
    args: audit --json
```

**As a Go developer:**

```
go install github.com/sosalejandro/atlas/cmd/atlas@latest
```

Swap `@latest` for a specific tag (e.g. `@v0.1.2`) to pin. See
[Releases](https://github.com/sosalejandro/atlas/releases) for the version
history — releases are cut by
[release-please](https://github.com/googleapis/release-please) from
conventional-commit messages on `main`.

**As a downloaded binary,** from a
[release](https://github.com/sosalejandro/atlas/releases): a single static
binary per platform, plus `SHA256SUMS`, a keyless Sigstore signature over
it, an SPDX SBOM and SLSA provenance.

Releases are reproducible: the same commit built with the pinned Go
toolchain produces byte-identical binaries, so you can rebuild a release
yourself and compare digests rather than taking anyone's word for it.

```bash
git checkout v0.14.0 && make build VERSION=v0.14.0
sha256sum dist/atlas_v0.14.0_linux_amd64   # compare against the release's SHA256SUMS
```

That rebuild, run by you, is the check that settles it. CI's `make repro`
builds twice and compares, but both builds are on one machine with one
toolchain, so what it establishes is narrower: that the bytes do not depend
on the output directory, `TMPDIR`, `GOMAXPROCS` or the absolute path of the
checkout. Scope and method are in
[docs/install.md](./docs/install.md#reproducing-a-release-build).

`atlas version` reports the stamps and the build flags. Read the version
string with care on a build you did not download from a release: the release
commit carries its stamps in the source, so a `go install` from any ref —
tag or not — reports those baked values rather than `dev`. See
[docs/install.md](./docs/install.md#go-install) and
[docs/commands/version.md](./docs/commands/version.md).

### Optional runtime dependencies

The language sub-scanners shell out to native runtimes when a project
contains TypeScript or Python sources. Each is **optional** — if the
runtime isn't on PATH, atlas surfaces a single warning and continues
scanning the languages it can:

| Language   | Runtime  | Min version | Skip with                         |
| ---------- | -------- | ----------- | --------------------------------- |
| Go         | (none)   | —           | (always on)                       |
| TypeScript | `node`   | 18+         | `.atlas.yaml` `scan.skip_ts: true`|
| Python     | `python3`| 3.8+        | `codeindex.Options.SkipPY = true` |

## License

Same as the testreg repo this was forked from.
