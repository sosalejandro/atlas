# Atlas

**Code-graph + coverage + audit toolkit for polyglot codebases.**

Atlas is the spiritual successor to [testreg](https://github.com/sosalejandro/testreg)
— rebuilt as a monorepo of SRP-focused library packages with a single
`atlas` CLI on top. Library consumers (like
[bmad-story-runner-cli](https://github.com/sosalejandro/bmad-story-runner-cli))
import individual packages directly; end users install one binary.

> **Status: Phase 0 — restructure in progress.** This repo carries
> testreg's full git history (commits document the lessons being
> applied). The legacy `cmd/`, `internal/`, `e2e/` directories are
> being migrated into `packages/` + `cmd/atlas/` + `internal/cli/`
> phase by phase. See `docs/architecture.md` for the target layout
> and `docs/migration-from-testreg.md` for the cutover plan.

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

Measured cold-start wall time on one laptop: **5.9 s** on this repository
(581 indexed files, 4,385 symbols, Go + TypeScript + Python) and **2.2 s** on
a synthetic 3,000-file Go tree.

**Inferred is not declared.** Everything `onboard` proposes is namespaced
under `provisional:`, written to `.atlas/provisional/capabilities.json`, and
absent from the features table — atlas's registry is worth something only
because a human wrote every row in it. The single path in is
`atlas onboard promote`, which writes an `@atlas:feature` annotation into
your source (dry run by default) and lets the ordinary scan pick it up.
Annotations you already have are adopted as-is and never re-proposed.

- [Quickstart](./docs/quickstart.md) — the whole first run, with real output
- [Languages](./docs/languages/) — per-language usage guides
  ([Go](./docs/languages/go.md) /
  [TypeScript](./docs/languages/ts.md) /
  [Python](./docs/languages/py.md))

### Reference

- [Commands](./docs/commands/) — per-subcommand reference
  (`atlas onboard`, `init`, `scan`, `trace`, `audit`, `codebase`, `cov`,
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

Releases are reproducible: the same commit and the pinned Go toolchain
produce byte-identical binaries, so you can rebuild a release yourself and
compare digests rather than taking anyone's word for it. CI proves it on
every push by building twice and comparing.

```bash
git checkout v0.14.0 && make build VERSION=v0.14.0
sha256sum dist/atlas_v0.14.0_linux_amd64   # compare against the release's SHA256SUMS
```

Verify with `atlas version`, which reports the stamps and the build flags. A
version of `dev` means you installed from a non-tag ref.

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
