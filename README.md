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
│   ├── ts/               # TS scanner (embedded ts-scanner.ts)
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

- [`docs/architecture.md`](./docs/architecture.md) — package boundaries + dependency direction
- [`docs/annotations.md`](./docs/annotations.md) — `@atlas:<kind> <id>` grammar
- [`docs/schema-v1.md`](./docs/schema-v1.md) — SQLite schema reference
- [`docs/migration-from-testreg.md`](./docs/migration-from-testreg.md) — cutover guide for testreg users

## Install (once Phase 7 ships)

```
go install github.com/sosalejandro/atlas/cmd/atlas@latest
```

## License

Same as the testreg repo this was forked from.
