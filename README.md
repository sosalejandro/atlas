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

- [Quickstart](./docs/quickstart.md) — first 5 minutes with atlas
- [Languages](./docs/languages/) — per-language usage guides
  ([Go](./docs/languages/go.md) /
  [TypeScript](./docs/languages/ts.md) /
  [Python](./docs/languages/py.md))

### Reference

- [Commands](./docs/commands/) — per-subcommand reference
  (`atlas init`, `scan`, `trace`, `audit`, `codebase`, `cov`, `diff`,
  `snapshot`, `sprint`, `diagnose`, `contract`, `migrate-annotations`)
- [Architecture](./docs/architecture.md) — package boundaries + dependency direction
- [Annotations](./docs/annotations.md) — `@atlas:<kind> <id>` grammar
- [Schema v1](./docs/schema-v1.md) — SQLite schema reference
- [Migration from testreg](./docs/migration-from-testreg.md) — cutover guide for testreg users

## Install

```
go install github.com/sosalejandro/atlas/cmd/atlas@latest
```

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

For a specific tagged release, swap `@latest` for the version you want
(e.g. `@v0.1.2`). See [Releases](https://github.com/sosalejandro/atlas/releases)
for the full version history and changelog — releases are cut automatically
by [release-please](https://github.com/googleapis/release-please) from
conventional-commit messages on `main`.

After install, verify with `atlas --version`. If the version reports `dev`
instead of a semver, you installed from a non-tag ref (commit hash or
branch) — for reproducible pinning use a tagged release.

## License

Same as the testreg repo this was forked from.
