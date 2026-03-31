# Unified Scan Architecture — One Command, Full Stack

**Date:** 2026-03-31
**Status:** Design only
**Problem:** Today `testreg audit auth.login` silently spawns a Node.js subprocess for the TS scanner, scans the entire frontend in one shot, and blocks until it finishes. For large monorepos this is slow. The user also has to ensure Node.js is installed, `ts-scanner.ts` is findable, and the whole thing feels like two separate tools duct-taped together.

**Goal:** `testreg audit auth.login` handles everything — backend Go AST, frontend TypeScript, coverage enrichment, test scanning — managed by the CLI, parallelized where possible, no manual orchestration.

---

## Current Architecture (Problems)

```
testreg audit auth.login
  │
  ├─ Go AST Scanner (in-process, Go)
  │    Phase 0: SQLC pre-resolve
  │    Phase 1: Route discovery
  │    Phase 2: Function discovery    ← walks ALL .go files sequentially
  │    Phase 3: Call extraction       ← walks ALL function bodies sequentially
  │
  ├─ Frontend Scanner (subprocess, Node.js)
  │    cmd := exec.Command("node", "ts-scanner.ts", projectRoot)
  │    output, _ := cmd.Output()     ← BLOCKS until entire TS scan completes
  │    json.Unmarshal(output)         ← parses entire result at once
  │
  └─ Merge → Trace → Annotate → Score
```

**Problems:**
1. **Sequential**: Go AST scan finishes, THEN TS scan starts (or vice versa). They could run in parallel.
2. **Monolithic TS scan**: Scans ALL frontend_roots in one subprocess. A large monorepo with 5 frontend apps waits for all 5.
3. **Single Node.js process**: No parallelism within the TS scanner itself.
4. **External dependency**: Requires `node` on PATH and `ts-scanner.ts` to be findable.
5. **No progress feedback**: User sees nothing during a long scan.

---

## Proposed Architecture

### Principle: Go manages parallelism, TypeScript does the parsing

Go's goroutines are the coordination layer. TypeScript is invoked per-directory (not per-project) as lightweight subprocesses. Go AST scanning and TS scanning run concurrently.

```
testreg audit auth.login
  │
  ├─ goroutine 1: Go AST Scanner
  │    ├─ goroutine 1a: Phase 0+1 (SQLC + routes)
  │    └─ goroutine 1b: Phase 2+3 per directory (parallelized)
  │
  ├─ goroutine 2: Frontend Scanner (per frontend_root)
  │    ├─ goroutine 2a: ts-scanner apps/web/src
  │    ├─ goroutine 2b: ts-scanner apps/mobile/src
  │    └─ goroutine 2c: ts-scanner apps/admin/src
  │
  ├─ goroutine 3: Coverage Profile Reader (if configured)
  │    Parse cover.out + coverage-final.json concurrently
  │
  ├─ goroutine 4: Test Scanner (scan for @testreg annotations)
  │    All 7 scanners run concurrently
  │
  └─ sync.WaitGroup → Merge all results → Trace → Annotate → Score
```

### Key Design Decisions

**1. Per-directory TS subprocess, not per-project**

Today: `node ts-scanner.ts /project-root` → scans everything.
Proposed: `node ts-scanner.ts /project-root/apps/web/src` → one subprocess per `frontend_root`.

Benefits:
- Multiple frontend apps scan in parallel (goroutines)
- Smaller memory footprint per subprocess
- If one app fails, others still complete
- Progress reporting per app

The ts-scanner.ts already accepts a project root — changing it to accept a specific directory is a 5-line change.

**2. Go manages goroutines, not TypeScript workers**

Why not Node.js worker threads inside ts-scanner.ts?
- Adds complexity to the TS code (worker pool, message passing)
- Go already has goroutines + channels (zero-cost concurrency)
- The TypeScript compiler API (`ts.createSourceFile`) is synchronous anyway — parallelism happens at the directory level, not the file level
- Fewer moving parts: Go spawns N subprocesses, collects JSON, merges

**3. Concurrency budget from config**

```yaml
# .testreg.yaml
graph:
  concurrency: 4          # max parallel goroutines for scanning
  frontend_roots:
    - apps/web/src
    - apps/mobile/src
    - apps/admin/src
```

Default: `runtime.NumCPU()` or 4, whichever is smaller. Each frontend_root gets its own goroutine. Go AST scanning gets the remaining budget.

**4. Parallel Go AST scanning (within the Go scanner)**

Today: `filepath.WalkDir` processes files sequentially.
Proposed: Walk directories, group .go files by package directory, process packages in parallel.

```go
// Collect packages
packages := groupFilesByPackage(goFiles)

// Process packages in parallel with bounded concurrency
sem := make(chan struct{}, concurrency)
var wg sync.WaitGroup

for _, pkg := range packages {
    wg.Add(1)
    sem <- struct{}{} // acquire semaphore
    go func(files []string) {
        defer wg.Done()
        defer func() { <-sem }() // release semaphore
        
        for _, file := range files {
            parseFile(ctx, file)
        }
    }(pkg.files)
}
wg.Wait()
```

**Challenge:** `scanContext` has shared mutable state (`funcLookup`, `structFields`, `graph`). Need either:
- Option A: Per-package local results, merge after all goroutines finish (cleanest)
- Option B: Mutex-protected shared state (simpler but contention risk)

Recommended: **Option A** — each goroutine produces a local `[]Node` + `[]Edge` + field maps. A single merge step combines them. This matches the existing merge pattern for frontend nodes.

**5. The `--rescan` flag triggers everything**

```bash
testreg audit --rescan auth.login
```

This becomes: scan (all platforms in parallel) → build graph (Go + TS in parallel) → trace → annotate → score. One command, full stack.

---

## Implementation Plan

### Phase 1: Parallel Go + TS scanning (highest impact)

**File changes:**

| File | Change |
|------|--------|
| `internal/adapters/go_ast_scanner.go` | Run Go AST scan and frontend scan concurrently via goroutines |
| `internal/adapters/frontend_scanner.go` | Accept per-directory scanning, spawn one subprocess per `frontend_root` |
| `internal/adapters/graph_config.go` | Add `Concurrency int` to config |
| `internal/ports/graph.go` | Add `Concurrency int` to GraphConfig |

**Go AST scanner changes:**

```go
func (s *GoASTScanner) Build(projectRoot string, config ports.GraphConfig) (*domain.Graph, error) {
    ctx := s.newScanContext(projectRoot, config)
    
    var wg sync.WaitGroup
    var frontendResult *FrontendScanResult
    var frontendErr error
    
    // Start frontend scan in parallel (if configured)
    if len(config.FrontendRoots) > 0 {
        wg.Add(1)
        go func() {
            defer wg.Done()
            frontendResult, frontendErr = s.scanFrontendParallel(projectRoot, config)
        }()
    }
    
    // Run Go phases (can also be parallelized internally in Phase 2)
    s.preResolve(ctx, projectRoot, config)
    s.discoverRoutes(ctx, projectRoot, config)
    s.discoverFunctions(ctx, backendAbs)
    s.resolveHandlerRefs(ctx)
    s.extractCalls(ctx)
    
    // Wait for frontend
    wg.Wait()
    
    // Merge frontend results
    if frontendErr != nil {
        fmt.Fprintf(os.Stderr, "warning: frontend scan failed: %v\n", frontendErr)
    } else if frontendResult != nil {
        s.frontendScanner.MergeIntoGraph(ctx.graph, frontendResult)
    }
    
    return ctx.graph, nil
}
```

**Frontend scanner changes:**

```go
// ScanParallel scans multiple frontend roots concurrently.
func (s *FrontendScanner) ScanParallel(projectRoot string, roots []string, concurrency int) (*FrontendScanResult, error) {
    results := make(chan *FrontendScanResult, len(roots))
    errors := make(chan error, len(roots))
    sem := make(chan struct{}, concurrency)
    
    for _, root := range roots {
        sem <- struct{}{}
        go func(dir string) {
            defer func() { <-sem }()
            result, err := s.scanDirectory(projectRoot, dir)
            if err != nil {
                errors <- err
                return
            }
            results <- result
        }(root)
    }
    
    // Collect and merge
    merged := &FrontendScanResult{}
    for range roots {
        select {
        case r := <-results:
            merged.Nodes = append(merged.Nodes, r.Nodes...)
            merged.Edges = append(merged.Edges, r.Edges...)
            // ...
        case err := <-errors:
            fmt.Fprintf(os.Stderr, "warning: %v\n", err)
        }
    }
    return merged, nil
}
```

### Phase 2: Parallel Go package scanning (medium impact)

Parallelize Phase 2 (function discovery) across package directories. Each package produces local results that merge into the shared `scanContext`.

This is more complex because `structFields` and `funcLookup` are shared maps. The per-package approach:

```go
type packageScanResult struct {
    nodes        []*domain.Node
    edges        []domain.Edge
    structFields map[string]map[string]string
    funcInfos    map[string]*funcInfo
}

// Each goroutine produces a packageScanResult
// Main goroutine merges all results into scanContext
```

### Phase 3: Eliminate Node.js dependency (ambitious, deferred)

Rewrite ts-scanner.ts in Go using a TypeScript parser library. Options:

| Approach | Pros | Cons |
|----------|------|------|
| **Go port of ts-scanner logic** using `github.com/nicolo-ribaudo/tc39-proposal-type-annotations` or similar | No Node.js dependency, pure Go binary | No mature Go TypeScript parser exists |
| **Embed Node.js** via `rogchap/v8go` | Real V8 engine, TypeScript API works | Heavy dependency, complex build |
| **Use `esbuild` Go API** for parsing | Excellent Go parser, fast | esbuild strips types but doesn't expose full AST for analysis |
| **Tree-sitter with TypeScript grammar** | Language-agnostic, Go bindings exist | Query-based, not full type resolution |
| **Keep Node.js subprocess** but improve protocol | Simplest, already works | External dependency remains |

**Recommended: Keep Node.js subprocess** (Phase 1-2 make it fast enough). Only consider Go-native parsing if Node.js availability becomes a real deployment blocker.

### Phase 4: Unified `--full` flag

```bash
testreg audit --full auth.login
# Equivalent to: scan + build graph + trace + annotate + coverage enrich + score
# All in one command with maximum parallelism
```

This combines `--rescan` with `--coverprofile` auto-detection:

```go
if auditFull {
    // 1. Run scan (all platforms, parallel)
    // 2. If cover.out exists, load it
    // 3. Run audit with graph + coverage enrichment
}
```

---

## Concurrency Model

```
                    ┌─────────────────┐
                    │  testreg audit   │
                    └────────┬────────┘
                             │
              ┌──────────────┼──────────────┐
              ▼              ▼              ▼
        ┌──────────┐  ┌──────────┐  ┌──────────┐
        │ Go AST   │  │ Frontend │  │ Coverage │
        │ Scanner  │  │ Scanner  │  │ Reader   │
        └────┬─────┘  └────┬─────┘  └────┬─────┘
             │              │              │
       ┌─────┼─────┐   ┌───┼───┐          │
       ▼     ▼     ▼   ▼   ▼   ▼          ▼
     pkg1  pkg2  pkg3  web mob admin    cover.out
       │     │     │    │   │   │          │
       └─────┴─────┘    └───┴───┘          │
             │              │              │
             ▼              ▼              ▼
        ┌──────────────────────────────────────┐
        │         sync.WaitGroup.Wait()        │
        │       Merge all results into Graph   │
        └──────────────┬───────────────────────┘
                       │
                       ▼
                  Trace → Annotate → Score → Render
```

All goroutines are managed by Go. TypeScript subprocesses are fire-and-collect (spawn, read stdout JSON, done). No inter-process communication beyond stdin/stdout.

---

## Config

```yaml
# .testreg.yaml
graph:
  concurrency: 4              # max parallel goroutines (default: min(NumCPU, 4))
  frontend_roots:
    - apps/web/src            # each gets its own goroutine + subprocess
    - apps/mobile/src
    - apps/admin/src
  coverprofiles:
    - cover.out
    - coverage/coverage-final.json
```

---

## Expected Performance Impact

| Scenario | Today | After Phase 1 | After Phase 2 |
|----------|-------|---------------|---------------|
| Go-only project (200 files) | 2.5s | 2.5s (no change) | ~1.5s (parallel packages) |
| Go + 1 frontend app | 4.0s (serial) | ~2.8s (parallel) | ~2.0s |
| Go + 3 frontend apps | 8.0s (serial) | ~3.5s (parallel) | ~2.5s |
| Monorepo (500 Go files + 3 frontends) | 15s+ | ~6s | ~4s |

Phase 1 (parallel Go + TS) gives ~2x speedup for full-stack projects. Phase 2 (parallel packages) adds another ~30% for large Go codebases.

---

## Verification

```bash
# Correctness: output must be identical to sequential mode
testreg trace auth.login > /tmp/sequential.json
TESTREG_CONCURRENCY=4 testreg trace auth.login > /tmp/parallel.json
diff /tmp/sequential.json /tmp/parallel.json   # must be empty

# Performance: measure wall time
time testreg audit --all                        # before
time testreg audit --all                        # after (with parallel)

# Race detector: goroutine safety
go test -race ./internal/adapters/ -run TestGoAST
```
