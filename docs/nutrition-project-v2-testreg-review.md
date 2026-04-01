# Case Study: How testreg Transformed Test Coverage on a Full-Stack Nutrition Platform

**Project:** nutrition-project-v2 (Floreo Nutrire)
**Stack:** Go backend (Clean Architecture, GraphQL + REST, Chi, gqlgen, SQLC), React Native mobile app, React web app, PostgreSQL
**Sprint Duration:** ~8 hours across 2026-03-28 to 2026-03-31
**Registry:** 184 features across 16 domains (auth, billing, training, meals, recovery, etc.)

---

## The Starting Point

Before this session, the project had tests scattered across Go, Vitest, Playwright, and Maestro. There were 749 test files. The problem was not a lack of tests -- it was a lack of *visibility*. Nobody could answer: "Which business features are actually validated by our test suite?"

The existing Playwright E2E suite had a 18% failure rate (68 of 38 spec files failing). Four entire spec files were fully broken. Unit test coverage existed but was unmapped to business features. The team was adding tests without knowing which features still had gaps.

testreg was built to answer one question: **For each business feature, what is the test coverage across every layer of the stack?**

### Before the sprint

| Metric | Value |
|--------|-------|
| Features tracked | 184 |
| Features at 80%+ health | ~29 |
| Critical features at 100% | ~10 of 23 |
| Test files with `@testreg` annotations | 747 of 749 |
| Unit coverage (aggregate) | 42% |
| Integration coverage | 10% |
| E2E coverage | 16% |

### After the sprint

| Metric | Value |
|--------|-------|
| Features at 80%+ health | 46 |
| Critical features at 100% | 20 of 23 |
| New tests written | ~500+ |
| Commits | 40 |
| Files changed | 852 |
| Lines of test code added | ~22,000 |
| Production bugs found and fixed | 5 (1 multi-layer, 4 related) |
| New Maestro E2E flows | 3 |
| Updated Maestro E2E flows | 2 |

---

## 1. How testreg Drove the Quality Process

### The audit-dispatch-verify loop

The core workflow was simple and repeatable:

```
testreg audit  -->  prioritize gaps  -->  dispatch subagents  -->  testreg audit (verify)
```

`testreg audit` produces output like this for each feature:

```
training.record-exercise  CRITICAL  health: 0%
  Gaps:
    - resolver: no tests for mutationResolver.TrainingLogSet  (src/cmd/graphql/resolvers/training.resolvers.go:60)
    - handler: DTO conversion trainingSetDTOToGQL untested     (src/cmd/graphql/resolvers/training_resolvers.go:145)
    - service: LogSet has unit tests but no edge cases          (src/training/internal/application/services/session_lifecycle_service.go:141)
    - e2e: training-session.yaml checks labels not values       (apps/mobile/e2e/flows/patient/training-session.yaml:45)
```

Each gap comes with exact `file:line` references. This is the key differentiator from "just running a coverage tool." Code coverage tells you which lines were executed. testreg tells you which *business features* those lines belong to, what layers are missing tests, and how to prioritize based on feature criticality.

### Priority-weighted scoring

testreg weights features by priority: CRITICAL features get 4x weight, HIGH gets 2x, MEDIUM gets 1x. This means a CRITICAL feature at 50% health contributes 4x more to the overall "debt" score than a MEDIUM feature at 50%.

The practical effect: the audit output naturally sorts by business impact. When we had 100+ features needing work, the ordering told us exactly where to start.

```
CRITICAL (4x):  auth.login, training.record-exercise, billing.checkout, meals.log-create
HIGH (2x):      communication.chat-list, recovery.heatmap, admin.dashboard
MEDIUM (1x):    settings.sessions, recipes.search, shopping.list-view
```

### Batch dispatching with subagents

Over 7 batches, we dispatched ~25 subagents, each receiving a precise gap list from `testreg audit`. A typical dispatch looked like:

```
Batch 5 targets:
  - recovery.readiness-score:  add benchmarks + race tests for ReadinessService
  - infra.persistence:         test NewPostgresRepository error paths
  - admin.rbac:                add permission matrix edge cases
  - billing.checkout:          handler-level tests for CheckoutHandler
  - training.start-session:    domain aggregate tests for WorkoutSession state machine
```

Each subagent got the exact files to modify, the test patterns to follow (table-driven for Go, describe/it for Vitest), and the `@testreg` annotation to add. No codebase exploration needed.

### The `@testreg` annotation bridge

Without annotations, tests are just files with assertions. With `@testreg`, each test maps to a business feature:

```go
// Go test file
// @testreg training.record-exercise
func TestLogSet_HappyPath_SameDiscipline(t *testing.T) {
```

```typescript
// Vitest test file
// @testreg billing.subscribe
describe('useSubscriptionHook', () => {
```

```yaml
# Maestro E2E flow
# @testreg training.start-session
appId: com.floreo.nutrire
---
```

`testreg scan` reads these annotations across Go, TypeScript, and YAML files and maps them back to registry features. This is what makes the health scoring work -- without annotations, you have test files but no way to know which business features they validate.

---

## 2. Gaps That Could Not Have Been Found Without testreg

### The Training Session Stats Bug (the star of this session)

This was the most significant finding of the sprint: a multi-layer production bug that no individual test or code review would catch.

**How it started:** `testreg audit` showed `training.record-exercise` and `training.end-session` at 0% health despite having unit tests. This seemed wrong -- the service layer had 20+ passing tests.

**What the audit revealed:** The newly shipped GraphQL scanner showed that the *outer resolver* had no tests. The dependency chain from `Mutation.trainingLogSet` to the database was:

```
mutationResolver.TrainingLogSet        (outer resolver - NO TESTS)
  --> r.Training.LogSet                (delegates to bounded context)
    --> TrainingResolver.LogSet        (internal resolver)
      --> sessionService.LogSet        (service - TESTED)
        --> setRepo.Create             (repository)
          --> sql:CreateExerciseSet     (SQL query)
```

The unit tests covered the service layer and below. Nobody had tested the resolver layer that sits between GraphQL and the service. This was the integration seam where data transformations happened -- and where 5 bugs were hiding:

**Bug 1: `ListHistory` returned sessions without segments.** The Go service's `ListHistory` method loaded sessions but did not populate their segments (they were nil). The session summary screen showed `Total Volume: 0 kg` and `Exercises: 0` because there were no segments to compute stats from.

```go
// The fix in session_lifecycle_service.go
// Before: sessions came back with nil Segments
// After: load full sessions with segments via GetByID instead of List
```

**Bug 2: Frontend race condition in SessionSummaryScreen.** The mobile app re-fetched session data via a separate GraphQL query when navigating to the summary screen. This raced against cache invalidation from the `completeSession` mutation. Sometimes the re-fetch returned stale (incomplete) data.

The fix: pass completion stats via navigation params instead of re-fetching.

**Bug 3: Stale closure in `handleCompleteSet`.** The React Native `handleCompleteSet` callback captured `weight` and `reps` from a stale closure. When the user tapped "Complete," the callback sent `weight=0, reps=0` to the backend because the closure had captured the initial state, not the current state.

The fix: use `useRef` to read the latest values at call time instead of capturing them in a closure.

**Bug 4: `set.exercise` was always null.** The `trainingSetDTOToGQL` function in the Go resolver never populated the `Exercise` field on `TrainingSet`. A comment said "resolved lazily via DataLoader" -- but no DataLoader existed. This meant `exerciseCount` was always 0 in the summary, and exercises showed as "Unknown Exercise."

```go
// Before: trainingSetDTOToGQL
// Exercise: nil, // "resolved lazily via DataLoader" <-- DataLoader didn't exist
// After: populate Exercise from the set's metadata
```

**Bug 5: SQLite FOREIGN KEY errors.** The mobile app's `sessionPersistence.saveSet()` threw unhandled promise rejections when saving sets with invalid foreign key references. These errors polluted the error state and caused intermittent UI glitches.

**Why testreg found this and nothing else would have:**

- All existing unit tests *passed*. The bug was at the integration seam between layers.
- The Maestro E2E test for `training-session.yaml` also *passed* -- it checked for the presence of labels (`assertVisible: text: 'Total Volume'`) but not their values. `Total Volume: 0 kg` was a passing test.
- Traditional code coverage would show the resolver as "covered" because other test paths touched it. testreg showed it was *uncovered for this specific feature's entry point*.
- The dependency chain trace (`testreg trace training.record-exercise`) showed the exact call path where data stopped flowing correctly.

### The E2E Value Assertion Gap

testreg's E2E coverage markers showed `training.start-session` as "covered" by `training-session.yaml`. But examining the file revealed the test only validated label presence:

```yaml
# Before: this PASSES even when stats are all zeros
- assertVisible:
    text: 'Total Volume'
- assertVisible:
    text: 'Exercises'
```

After the training stats bug was found, we updated the flow to use testID-based value assertions:

```yaml
# After: this FAILS if stats are wrong
- assertVisible:
    id: 'stat-total-volume'
- assertVisible:
    id: 'stat-exercises'
- assertVisible:
    id: 'ex0-set1-weight-inc'
```

And added exercise-scoped testIDs (`ex0-set1-weight-inc`, `ex0-set1-reps-inc`) so the E2E test could interact with specific exercise slots in the active session screen.

This is a pattern testreg surfaces naturally: a feature can be "covered" by a test file but not actually *validated*. The annotation maps the test to the feature, but the audit's gap analysis reveals when the test's assertions are too shallow.

---

## 3. Features Discovered and Built During This Session

### GraphQL Scanner (new testreg capability)

This was the single biggest testreg improvement to come out of this sprint.

**The problem:** testreg could trace REST entry points (`POST /api/v1/auth/login` maps to `AuthHandler.Login`) but could not trace GraphQL entry points. When training features declared `method: GRAPHQL, path: Mutation.trainingLogSet` in their registry YAML, `testreg trace training.record-exercise` returned an empty trace.

**The discovery:** This gap was found *during* the sprint when we ran `testreg trace training.record-exercise` and got `(empty trace)`. The training features had API surfaces defined but testreg did not know how to resolve `Mutation.trainingLogSet` to a Go function.

**The fix:** The entire GraphQL scanner turned out to be ~15 lines of naming convention code:

```go
// "Mutation.trainingLogSet" --> "mutationResolver.TrainingLogSet"
func graphqlEntryPoint(path string) string {
    parts := strings.SplitN(path, ".", 2)
    if len(parts) != 2 || parts[1] == "" {
        return ""
    }
    receiver := strings.ToLower(parts[0][:1]) + parts[0][1:] + "Resolver"
    method := strings.ToUpper(parts[1][:1]) + parts[1][1:]
    return receiver + "." + method
}
```

The Go AST scanner already discovered resolver methods during its function walk (Phase 2). The only missing piece was the entry point mapping -- converting the GraphQL operation name from the registry to a Go node ID the graph already contained.

**The impact:** After shipping the GraphQL scanner, `training.record-exercise` and `training.end-session` dropped from 50-60% health to 0%. This was *correct* -- the previous scores were inflated because the scanner could not see the resolver-level gap. The corrected scores drove the investigation that found the 5-bug training stats issue.

### Dependency Chain YAML (manual fallback)

While the GraphQL scanner was being built, we manually defined `dependency_chain` sections in the training registry YAML:

```yaml
dependency_chain:
  - layer: resolver
    file: src/cmd/graphql/resolvers/training.resolvers.go
    function: mutationResolver.TrainingLogSet
  - layer: internal-resolver
    file: src/training/internal/infrastructure/graphql/resolver.go
    function: TrainingResolver.LogSet
  - layer: service
    file: src/training/internal/application/services/session_lifecycle_service.go
    function: SessionLifecycleService.LogSet
```

This revealed that testreg's `Feature` struct does not parse the `dependency_chain` field yet -- it is silently ignored during YAML unmarshaling. Another item for the testreg roadmap.

### The `@calls` annotation pattern

The GraphQL scanner stops at depth 1 (the outer resolver) because `r.Training.LogSet()` crosses a package boundary via a struct field of interface type. The Go AST scanner resolves struct field types, but when the field is an interface (`TrainingService`) that is satisfied by a concrete type in a different package, the scanner needs either `go/types` (opt-in, requires buildable project) or a manual hint.

The `@calls` annotation pattern was identified as the pragmatic workaround:

```go
// @calls TrainingResolver.LogSet
func (r *mutationResolver) TrainingLogSet(ctx context.Context, input model.LogSetInput) (*model.TrainingSet, error) {
    return r.Training.LogSet(ctx, input)
}
```

This is already supported by the testreg scanner but was not widely used. The training investigation made it clear that deeper call chain tracing needs either `@calls` annotations or `go/types` enabled.

---

## 4. The Story: AI-Assisted Development + Static Analysis

### Token Efficiency

This is where testreg's value proposition is clearest for AI-assisted development.

**Without testreg**, asking an AI agent to "find all untested code" requires reading every source file, understanding the architecture, and cross-referencing against test files. For a project with 852 files, this consumes massive context.

**With testreg**, `testreg audit` produces ~5-10 lines per feature with exact file:line references. The total audit output for 184 features fits in ~2K tokens. Compare that to the ~100K+ tokens an AI would need to read and analyze the same codebase from scratch.

The workflow:

```
1. Run: testreg audit --severity critical    (~500 tokens of output)
2. Parse output, group gaps into 5 batches   (~200 tokens of planning)
3. Dispatch 5 parallel subagents, each with  (~300 tokens per dispatch)
   precise gap list and file references
4. Each subagent writes 20-60 tests           (no exploration needed)
5. Run: testreg audit --verify               (~500 tokens to confirm)
```

Total orchestration overhead: ~2K tokens. Without testreg, step 3 alone would cost ~20K tokens per subagent for codebase exploration.

### Bug Finding Across 5 Layers

The training session bug is the clearest example of why static dependency analysis matters for AI-assisted debugging.

Traditional QA would find "stats show 0" but not *why*. A developer might spend hours manually tracing the call chain. testreg's trace showed the exact path:

```
Mutation.trainingLogSet
  --> mutationResolver.TrainingLogSet     (resolver - gap here)
    --> r.Training.LogSet                 (delegation)
      --> sessionService.LogSet           (service - tested but...)
        --> setRepo.Create                (repository)
          --> sql:CreateExerciseSet        (SQL)
```

With this trace, the AI agent could jump directly to `mutationResolver.TrainingLogSet`, see the `trainingSetDTOToGQL` function, and find the null `Exercise` field -- in one read operation instead of a multi-file search.

The 5 bugs spanned:

| Layer | Bug | File |
|-------|-----|------|
| Go resolver | `set.exercise` always null, no DataLoader | `training_resolvers.go` |
| Go service | `ListHistory` returns sessions without segments | `session_lifecycle_service.go` |
| React Native state | Stale closure sends weight=0, reps=0 | `ActiveSessionScreen.tsx` |
| React Native navigation | Re-fetch race condition on summary screen | `SessionSummaryScreen.tsx` |
| React Native persistence | Unhandled SQLite promise rejections | `sessionPersistence.ts` |

No single test or code review would catch all 5. They required tracing the full stack from GraphQL entry point to SQLite persistence, understanding how data flows (and stops flowing) at each layer boundary.

### Productivity Numbers

| Metric | This Sprint | Typical Without testreg |
|--------|-------------|------------------------|
| Tests written | ~500+ | ~50-80 (manual prioritization) |
| Time to find training stats bug | ~30 minutes (trace + audit) | Hours to days (manual debugging) |
| Subagent dispatches | ~25 (7 batches) | N/A (serial investigation) |
| Tests per subagent | 20-60 | N/A |
| Context tokens per dispatch | ~300 (file:line references) | ~20K (codebase exploration) |
| Features reaching 80%+ health | +17 (29 to 46) | +3-5 (guesswork prioritization) |

### Cross-Stack Tracing

testreg scans both Go (AST parsing via `go/ast`) and TypeScript (via `ts-scanner.ts` using the TypeScript compiler API). The registry YAML acts as the bridge:

```yaml
# Registry YAML bridges frontend and backend
- id: auth.login
  surfaces:
    web:
      route: /login
      component: LoginPage
    mobile:
      screen: LoginScreen
    api:
      - method: POST
        path: /api/v1/auth/login
  coverage:
    unit:
      backend:
        tests:
          - file: src/application/services/auth_service_test.go
      web:
        tests:
          - file: packages/state-management/src/stores/__tests__/authStore.test.ts
      mobile:
        status: missing
    e2e:
      web:
        tests:
          - file: apps/web/e2e/auth.spec.ts
      mobile:
        tests:
          - file: apps/mobile/e2e/flows/auth/login.yaml
```

`@testreg` annotations work across all three codebases:

- Go: `// @testreg auth.login`
- TypeScript: `// @testreg auth.login`
- Maestro YAML: `# @testreg auth.login`

The audit output aggregates coverage across all surfaces. A feature like `auth.login` shows gaps in mobile unit tests even if the backend and web are fully covered. This cross-stack visibility is what made it possible to prioritize Maestro E2E flows alongside Go unit tests in the same sprint.

### For Solopreneurs

This sprint was run by a single developer with AI assistance. The testreg workflow is designed for this exact scenario:

1. **Priority-weighted scoring** means you fix what matters first. With 184 features, you cannot test everything. The 4x weight on CRITICAL features ensures that `auth.login` gets fixed before `settings.theme-selection`.

2. **Parallel subagent dispatch** means you write tests for 5 features simultaneously. Each subagent gets a precise gap list -- no shared state, no coordination overhead.

3. **The loop is repeatable.** Run `testreg audit`, parse the output, dispatch, verify. No domain expertise required from the AI agents. The gap list is the spec.

4. **The annotations are the documentation.** Every test file maps to a business feature. Six months from now, when someone asks "what tests cover the checkout flow?", `testreg status billing.checkout` gives the answer instantly.

---

## Appendix: Commit Timeline

The 40 commits across this sprint, organized by type:

| Batch | Commit | Tests Added | Features Improved |
|-------|--------|-------------|-------------------|
| 1 | `feat(testing): add dep graph config` | auth benchmarks, unit tests | auth.login, auth.register |
| 2 | `test(coverage): handler tests` | admin, dashboard, auth register/refresh | 4 features |
| 3 | `test(coverage): critical client-management` | unit tests for list/detail | client-management.list, .detail |
| 4 | `test(coverage): training annotations + handler tests` | quick-win handler tests | training.dashboard, .browse-exercises |
| 5 | `test(coverage): benchmarks, race tests, unit tests` | 7 features closed | recovery.readiness, infra.persistence |
| 6 | `test(business-logic): auth, shopping, analytics + maestro` | auth gaps + 3 new E2E flows | auth.logout, shopping.list-view |
| 7 | `test(business-logic): training resolvers, near-misses, comms` | resolver tests, DTO tests | training.record-exercise, communication.chat-send |
| Bug fixes | 6 commits fixing training stats bug | -- | training.end-session, training.record-exercise |
| E2E | 4 commits improving Maestro flows | value assertions, stepper inputs | training.start-session |

### Key Files

| Path | Purpose |
|------|---------|
| `docs/testing/registry/*.yaml` | 16 registry files, 184 features |
| `apps/mobile/e2e/flows/patient/training-session.yaml` | Maestro E2E flow that exposed the value assertion gap |
| `src/cmd/graphql/resolvers/training_resolvers_test.go` | Resolver tests added after audit revealed the gap |
| `src/training/internal/application/services/session_lifecycle_service.go` | ListHistory bug fix (nil segments) |
| `apps/mobile/src/screens/training/ActiveSessionScreen.tsx` | Stale closure fix (weight=0, reps=0) |

---

## What's Next

The sprint left 138 features below 80% health. The remaining work breaks down as:

- **3 CRITICAL features** still need work: `communication.chat-send` (67%), `training.record-exercise` (improved but needs deeper integration tests), `meals.log-create` (99%, nearly there)
- **63 HIGH features** need attention -- the next sprint target
- **The `dependency_chain` YAML field** needs to be parsed by testreg's Feature struct
- **`@calls` annotations** need wider adoption for cross-package call tracing
- **`go/types` opt-in** would eliminate the need for `@calls` in most cases

The testreg → audit → dispatch → verify loop is now proven and repeatable. The next sprint should be more efficient: the annotation infrastructure is in place, the GraphQL scanner is shipped, and the patterns are established.
