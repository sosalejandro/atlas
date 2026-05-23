# TypeScript language guide

The TypeScript scanner is a Node.js subprocess that walks each `.ts` /
`.tsx` file through the TypeScript Compiler API, then returns the
discovered route / component / hook / api-service symbols and edges to
the atlas Go orchestrator. Source lives in
[`packages/codeindex/ts/`](../../packages/codeindex/ts/) — the embedded
`scanner.ts` is what `node` actually runs.

Unlike the Go scanner, the TS scanner is **router-aware**, not a general
symbol indexer. It only emits symbols reachable from a discovered router
(React Router, TanStack Router, or Expo Router). If atlas can't find a
router signal under `--root`, every TS file under that root is silently
skipped.

## Prerequisites

- `node` on `PATH`, version **18 or newer**.
- The `typescript` package resolvable somewhere — either in the scanned
  project's own `node_modules/`, or via `--node-modules-path
  <some-other-node_modules>`.

The TS scanner is **optional**: if `node` isn't on PATH, atlas emits a
single warning and continues indexing Go + Python.

## What gets indexed

The scanner walks the project root + every direct child of `apps/` and
`packages/`. From each discovered TS root it surfaces:

| What                                                    | Symbol kind   | Notes                                                                                                                              |
| ------------------------------------------------------- | ------------- | ---------------------------------------------------------------------------------------------------------------------------------- |
| Router declarations (React, TanStack, Expo)             | `route`       | `createBrowserRouter` / `createRouter` calls, or file-based routes under `app/`.                                                  |
| React components reachable from a route                 | `component`   | Indexed only when the route → component chain is statically resolvable.                                                            |
| Custom hooks (`useFoo`, `useBar`)                       | `hook`        | Looked up in `src/hooks/`, `apps/web/src/hooks/`, similar conventions.                                                              |
| API service modules (`services/api/*.ts`)               | `service`     | Picked up when the service is imported from a component or hook.                                                                   |
| `@atlas:*` annotations                                  | (annotation)  | Parsed identically to the Go side — same `@atlas:<kind> <id>` grammar applies.                                                     |

Other TS files — pure utility modules, generated types, test files —
are **not** indexed unless they're transitively reachable from a router.

The scanner skips: `node_modules/`, `dist/`, `build/`, `.next/`, `.expo/`,
`coverage/`, plus any directory starting with `.`. Generated routing
files (`routeTree.gen.ts`, `routeTree.gen.tsx`) are dropped to avoid
double-counting auto-generated routes.

## Sample project layout

The scanner's defaults match the typical Vite / Next-ish layout:

```
my-web-app/
├── package.json
├── tsconfig.json
├── node_modules/                              ← needs `typescript` here
├── apps/
│   └── web/
│       ├── src/
│       │   ├── router.tsx                     ← discovered: createBrowserRouter
│       │   ├── pages/
│       │   │   ├── LoginPage.tsx              ← discovered via route
│       │   │   └── DashboardPage.tsx
│       │   ├── hooks/
│       │   │   └── useAuth.ts                 ← discovered via component import
│       │   └── services/
│       │       └── api/
│       │           └── auth.ts                ← discovered via hook import
└── packages/
    └── shared-ui/
        └── src/
            └── Button.tsx                     ← indexed if exported from a discovered route
```

After `atlas init` this produces something like:

```
symbols:        12  (1 route, 2 pages, 1 hook, 2 services, 6 components)
edges:          15  (route -> page -> hook -> service)
annotations:     8  (component-level @atlas:feature markers)
```

## Worked queries

### Find a page component

```
# Run from: my-web-app/, after `atlas init`
$ atlas codebase find LoginPage
LoginPage  apps/web/src/pages/LoginPage.tsx:13  [component]
```

### Trace from a route to the API

```
# Run from: my-web-app/
$ atlas trace web.auth.login
trace feature web.auth.login (5 nodes)
route:/login                                              [route]      apps/web/src/router.tsx:142
LoginPage                                                 [component]  apps/web/src/pages/LoginPage.tsx:13
  useAuth                                                 [hook]       apps/web/src/hooks/useAuth.ts:19
    authApi.login                                         [service]    apps/web/src/services/api/auth.ts:46
      POST /api/v1/auth/login                             [endpoint]   (cross-language hop)
```

The `cross-language hop` row marks where the TS trace passes the baton
to the Go scanner — the next hop, `AuthHandler.Login`, lives in
`.go` files indexed by the Go scanner.

### List every API service site

```
# Run from: my-web-app/
$ atlas codebase pattern api-call
pattern api-call: 7 symbols
  apps/web/src/services/api/auth.ts:14         api-call
  apps/web/src/services/api/auth.ts:46         api-call
  apps/web/src/services/api/billing.ts:22      api-call
  ...
```

## Common gotchas

### 1. No router signal → no TS symbols at all

The most common surprise: atlas reports `warning: no router signal
detected (react-router, tanstack, or expo)` and `files_scanned` looks
correct, but `atlas codebase find LoginPage` returns "symbol not found".
This happens when the scanner walks an apps/* directory but can't find
*any* of:

- A file containing `createBrowserRouter(...)` or `createHashRouter(...)`.
- A file containing `createRouter(...)` AND importing
  `@tanstack/react-router`.
- An `app/` directory with `expo-router` in `package.json` dependencies.

**Workaround**: if you have a router that atlas doesn't recognise (a
custom wrapper, a server-only Next.js app, a Storybook config), the TS
scanner won't emit anything for that root. Either:

1. Annotate the symbols you care about with `@atlas:feature` directly —
   the annotation parser fires independently of the router walker.
2. Open an issue at <https://github.com/sosalejandro/atlas/issues> with
   the router shape; new framework signals are additive.

### 2. `tsconfig.json` is advisory — the scanner uses `ts.createSourceFile` directly

The TS scanner discovers files via filesystem walk, not via `tsconfig.json`
project references. Setting `"include"` or `"exclude"` in your tsconfig
does NOT change what atlas indexes. The scanner does forward
`--tsconfig <path>` to the embedded `scanner.ts` (`Options.TsconfigPath`),
but it's reserved for future type-aware passes — today it has no effect
on file discovery.

Practical impact: if your monorepo has `tsconfig.json` files at multiple
levels with non-default `include` patterns, atlas may emit symbols for
files your build excludes, or fail to skip files you'd expect it to. The
ground truth is the directory layout (`apps/*` + `packages/*` + scanner
skip rules), not the tsconfig.

### 3. The TS scanner needs `typescript` resolvable somewhere

`scanner.ts` imports `typescript`. If the scanned project has no
`node_modules/` (a slim repo, a backend-mostly monorepo, a fresh clone
before `pnpm install`), the scanner subprocess crashes with a module
resolution error and atlas reports zero TS symbols. Three fixes:

1. `npm install` / `pnpm install` in the scanned project before
   `atlas init`.
2. Pass `--node-modules-path /path/to/some/other/node_modules` —
   atlas appends it to `NODE_PATH`. A standalone `npm install
   typescript` in any directory works.
3. Configure `.atlas.yaml` to skip TS entirely:
   ```yaml
   scan:
     skip_ts: true
   ```

### 4. JSX / TSX `.tsx` files are indexed; `.jsx` / `.js` aren't (mostly)

The scanner's default extensions list is `['.ts', '.tsx']`. The Expo
file-based-router walker also picks up `.jsx` / `.js` under `app/`, but
non-Expo projects with `.jsx` route trees won't be discovered. If you're
on plain JavaScript (no TypeScript) atlas's TS scanner is not the right
tool — open an issue if this matters; today the recommendation is to
migrate to `.ts`.

## Related

- Annotation grammar (works identically across Go / TS / Python):
  [`docs/annotations.md`](../annotations.md).
- Go scanner: [`docs/languages/go.md`](./go.md).
- Python scanner: [`docs/languages/py.md`](./py.md).
- Per-command reference: [`docs/commands/`](../commands/).
- The TS scanner internals: [`packages/codeindex/ts/scanner.ts`](../../packages/codeindex/ts/scanner.ts)
  is the embedded TypeScript walker, [`packages/codeindex/ts/scanner.go`](../../packages/codeindex/ts/scanner.go)
  is the Go orchestrator that drives it.
