# How testreg Developed Itself: A Dogfooding Case Study

**Date:** 2026-03-31
**Project:** testreg -- full-stack dependency tracing and test coverage registry
**Scope:** One extended AI-assisted development session, from findings docs to 100% self-coverage

---

## Where It Started

testreg began life as a CLI tool that solved a specific problem: tracking test coverage for a Go + React monorepo called nutrition-project-v2. The monorepo had 184 business features across 16 domains. testreg could scan test files, match them to features via `@testreg` annotations, and show a coverage dashboard. It worked.

But the tool had a habit its users kept exposing. Every time someone needed to actually *decide* something -- which features to test next in a sprint, how much health improved after a push, which gaps to hand off to a parallel AI agent -- they piped testreg's output through Python scripts and bash one-liners. The tool was producing raw material, not answers.

Two findings documents captured this pattern. `floreo-nutrire-tool-findings.md` catalogued 15 specific gaps where developers wrote 10-to-30-line Python scripts to extract the data testreg should have given them natively. A second document identified 6 recurring anti-patterns where grep and Python were standing in for missing CLI capabilities.

Every one of those scripts was a feature request written in code.

---

## What Happened in One Session

In a single extended session with AI-assisted development, every gap in those findings documents was addressed. But what made the session remarkable was not the volume of features shipped -- it was the feedback loop that emerged when testreg started tracking itself.

### The First Move: Turning Findings into Features

The 15 gaps fell into clear clusters. Sprint planning required a priority-weighted ranking algorithm (Gap #11). Before/after health comparison required snapshot diffing (Gap #12). AI agent dispatch required structured gap extraction (Gap #13). Audit filtering required priority and health threshold flags (Gaps #1, #3, #4, #5).

Five new commands and nine new flags replaced every Python workaround:

- `testreg sprint` replaced the 30-line priority-scoring script that was rebuilt from scratch every sprint planning session because nobody ever committed it.
- `testreg gaps --format prompt` replaced custom Python extractors that structured gap data for AI agents. The format is designed to minimize context tokens: instead of "read the codebase, find the gap, figure out what to test," an agent gets "write a unit test for authService.Login at auth_service.go:172, annotate with `// @testreg auth.login #real`."
- `testreg diff` replaced manual JSON comparisons where developers copied numbers into spreadsheets.
- `testreg audit --priority critical --sort priority-score --summary` replaced three separate Python scripts with composable flags.

At this point the session could have stopped. Every documented gap was closed. But then we did the thing that changed the trajectory.

### The Decision That Found Three Critical Bugs

We set up testreg to track itself. 41 features across 7 domains. The tool that analyzes test coverage was now analyzing its own test coverage.

The first run of `testreg status` reported 69% health. Reasonable for a tool still in development. Then we ran `testreg audit --summary`.

Every single feature showed 0% health.

Not low health. Zero. Across all 41 features. The status command said 69%. The audit command said 0%. Both were looking at the same codebase.

#### Bug #1: The 0% Health Score

The audit command calculates health by tracing the dependency graph from API surfaces down through handlers, services, repositories, and queries. It checks whether each node in that graph has a corresponding test. The problem: testreg is a CLI tool. It has no API surfaces. No HTTP handlers. No REST endpoints. No GraphQL resolvers. The graph tracer found nothing to trace, so it scored every feature at zero.

The status command used a different path entirely -- registry-based matching via `@testreg` annotations. It found tests. It reported health. The two commands were using fundamentally different scoring mechanisms, and they disagreed catastrophically.

The fix was a registry-based health fallback. When graph tracing produces no nodes (because the project has no API surfaces), the audit falls back to annotation-based coverage data. After the fix, `testreg diff` confirmed the numbers aligned.

**Why this matters to every potential user:** Any developer trying testreg on a CLI tool, a library, an event-driven microservice, or anything without HTTP endpoints would have seen 0% health on all features. They would have concluded the tool was broken. This bug was invisible to the project testreg was originally built for, because nutrition-project-v2 has hundreds of API surfaces. Dogfooding against a different *kind* of project revealed the assumption.

#### Bug #2: The Performance Analysis Gap

Same pattern, different symptom. Performance scores showed 0/0 for every feature -- zero benchmarks detected, zero race tests detected -- despite test files containing `BenchmarkLogin` functions and `t.Parallel()` calls. The performance analyzer only ran on graph-traced nodes, and when there were no graph nodes, it produced nothing. The fallback pattern that fixed health scoring had not been applied to performance scoring.

Running `testreg audit scan.annotation-parser` on testreg itself showed "Performance Score: 0%, Benchmark: 0/0, Race: 0/0" for a feature whose test file demonstrably contained benchmarks. The tool was lying about its own test quality.

#### Bug #3: Contradictory Dashboard

The 0% audit score versus 69% status score was not just a data inconsistency -- it was a UX contradiction that would erode trust in the tool. A developer running both commands in sequence would reasonably conclude that at least one of them is wrong, and they would be right to distrust both.

### Testing Against Someone Else's Code

After fixing the self-tracking bugs, we tested testreg against a completely external project: Metro-Grama, an Echo/SurrealDB application with a `client/src/` frontend structure. This was the second critical decision of the session.

#### Bug #4: The Scanner Path Bug

Metro-Grama's frontend tests lived in `client/src/features/*.test.tsx`. testreg found none of them.

The Vitest scanner had a hardcoded `isWebPath()` function that recognized exactly three path prefixes: `web/`, `apps/web/`, and `src/`. The nutrition-project-v2 monorepo used `apps/web/`. Metro-Grama used `client/src/`. Every project in the world that stored frontend code in `client/`, `frontend/`, or `app/` was invisible to testreg's Vitest scanner.

This is the kind of bug that one project can never find. nutrition-project-v2 used the exact path prefix that the scanner was hardcoded to match. Only by testing against a project with different conventions did the assumption surface.

#### Bug #5: The Test Type Misclassification

Metro-Grama's report showed `health_test.go [go/backend/integration]` for a test file that creates an in-memory Echo context with `httptest.NewRecorder()`. That is a unit test by any reasonable definition -- no network, no database, no external dependencies. Meanwhile, module tests that connected to a real SurrealDB instance were classified as "unit."

The heuristic was inverted. Handler tests (which use in-memory HTTP) were tagged as integration. Database tests (which hit real databases) were tagged as unit. The classification logic assumed that handler-level tests are inherently integration tests, when in fact Go's `httptest` package exists precisely to make handler testing a unit-level activity.

Both scanner bugs were fixed and validated against Metro-Grama before moving on.

### Features That Emerged From the Friction

The most interesting outcomes of the session were not the bugs found but the features that emerged organically from using the tool.

#### `testreg contract`

A conversation about how `go/types` could resolve cross-package function calls led to an unexpected realization: if testreg has full type information, it does not just trace calls -- it can extract the exact data shapes flowing through each layer. A frontend developer could run one command and see the complete implementation chain: what fields the GraphQL mutation accepts, what struct the handler deserializes into, what service method gets called, what repository query runs, what SQL executes.

`testreg contract training.record-exercise` shows all of this in one output. The contract is always current because it is derived from source code, not documentation. No reading five files across three packages. No Slack messages asking "what fields does this endpoint accept?"

This feature was not in any findings document. It was not a gap anyone had identified. It emerged from the intersection of a type-resolution capability and the realization that dependency tracing is not just about tests -- it is about understanding.

#### `testreg init --discover`

Running testreg against Metro-Grama revealed that `testreg init` generated useless feature templates. It created `auth.login` with an API surface of `/api/v1/auth/login`. Metro-Grama's actual route was `POST /auth/admin/login`. The init command was guessing routes instead of reading them.

`--discover` parses the actual router file, finds all real routes, groups them by module, and generates features with correct API surfaces. It supports Chi, Echo, and stdlib `net/http` routers. The feature went from "user spends 20 minutes fixing generated YAML" to "user reviews generated YAML and hits enter."

#### Auto-Mapping by Directory Proximity

After `init --discover` created 43 features from Metro-Grama's routes, the scan showed all 6 test files as "unmapped." They had no `@testreg` annotations. But `server/modules/auth/auth_test.go` obviously tests auth features -- the directory name is the domain name.

Auto-mapping matches test files to features when a directory segment matches a domain name. It is an O(tests x domains x path_depth) string comparison that eliminates the need for annotations in projects with conventional directory structures. Annotations still win when present; directory proximity is the fallback.

### From 51% to 100% Self-Coverage

With the bugs fixed and new features in place, we turned to testreg's own test coverage. The starting point was 51% unit coverage -- 24 test files across 41 features. The session ended at 100% coverage with 42+ test files.

This was not rote test generation. Each test file was informed by the gaps testreg itself reported. `testreg gaps --feature scan.annotation-parser --format prompt` told the AI agent exactly what to test, where the source file was, and what annotation to use. The tool accelerated its own test coverage.

### The Performance Discovery

The final major outcome was a 7.9x performance improvement. The original architecture built the dependency graph once per feature during audit. For 184 features in nutrition-project-v2, this meant 184 full AST parses. The audit took 1 minute 52 seconds.

Building the graph once and querying it per-feature reduced the audit to 14 seconds. The optimization was identified by running `testreg audit --metrics` against the nutrition monorepo and observing that graph construction dominated the time profile.

A 2-minute command does not get integrated into CI. A 14-second command does. The performance improvement is what makes testreg viable as a CI gate, not just a developer tool.

---

## The Feedback Loop

The session followed a pattern that repeated throughout:

```
1. Human identifies pain point (findings docs, observed friction)
2. AI implements fix (new command, flag, or scanner)
3. testreg runs against itself (dogfooding)
4. testreg finds new issues (0% health bug, scanner gaps)
5. AI fixes issues
6. testreg verifies fixes (diff shows improvement)
7. Test against external project (Metro-Grama)
8. Find more gaps (scanner path bug, classification bug)
9. AI fixes, testreg verifies
10. New feature ideas emerge from usage (contract, discover)
```

This loop ran continuously. The AI was not just writing code -- it was using the tool it was building to validate the code it wrote, discovering bugs in the process, fixing them, and generating new feature ideas from the friction it encountered. The tool and its test coverage co-evolved in real time.

### Token Efficiency as a Design Principle

Without testreg, an AI agent working on a codebase needs to:

1. Read the directory structure to understand the project (~2,000 tokens)
2. Read test files to understand coverage (~5,000 tokens)
3. Figure out what is tested and what is not (~3,000 tokens)
4. Decide what to work on next (~1,000 tokens)

Total: ~11,000 tokens before any productive work begins.

With testreg:

1. `testreg sprint -n 5 --format json` produces ~200 tokens of structured priorities
2. `testreg gaps --feature auth.login --format prompt` produces ~100 tokens of exact instructions

Token reduction for coverage assessment: roughly 90%. The tool front-loads the analysis. The AI consumes structured output and immediately begins productive work.

The `--format prompt` flag exists because unstructured data wastes tokens. It was designed for machines, not humans. Every field is actionable: source file path, line number, what test to write, where to put it, exact annotation to use. The AI agent does not need to discover anything -- it receives a work order.

---

## What the Numbers Show

| Metric | Start of Session | End of Session |
|--------|-----------------|----------------|
| CLI commands | 10 | 17 |
| Audit flags | 3 | 9 |
| Test files | 24 | 42+ |
| Features tracked (self) | 0 | 41 |
| Unit coverage (self) | 51% | 100% |
| Audit time (184 features) | 1m 52s | 14s |
| Router support | Chi only | Chi, Echo, stdlib |
| DI support | Wire only | Wire, Uber Fx/Dig |
| Language support | Go, TypeScript | Go, TypeScript, Python |
| Framework-specific scanners | 5 | 8 |
| Design documents created | 0 | 7 |
| External projects tested against | 0 | 2 |

Every row in this table represents a capability that did not exist at the start of the session. Not planned for a future sprint. Shipped, tested, and validated against multiple projects.

---

## What This Means for Other Teams

### 1. Static Analysis Tools Must Eat Their Own Dogfood

Running testreg on itself found three critical bugs that unit tests could not catch. The 0% health bug was not a logic error in a function -- it was a design assumption (graph-based scoring) that does not hold for an entire category of software. You cannot write a unit test for "this tool produces misleading results for CLI tools" unless you first run the tool against a CLI tool.

If your static analysis tool only runs against the project it was built for, you are testing the happy path. The tool needs to be used on diverse projects with different conventions, different architectures, and different assumptions.

### 2. Findings Documents Are Feature Specifications

The 15 gaps in `floreo-nutrire-tool-findings.md` became 15 implemented features. Every Python workaround was a missing CLI capability. The bash one-liners were not laziness -- they were specifications written in the most precise language available: working code that shows exactly what the user needed and exactly what the tool did not provide.

Listen to what users do with your output. If they pipe it through Python, your CLI is incomplete.

### 3. AI Agents Work Best with Structured Input

`--format prompt` and `--format json` exist because unstructured data wastes tokens. When an AI agent receives "here is a big text dump of test coverage," it spends thousands of tokens parsing, filtering, and reasoning about what to do. When it receives "write a unit test for this function at this file and line number, annotate with this string," it spends dozens of tokens and starts writing code.

Design your tool's output for both humans and machines. The human needs a readable dashboard. The machine needs a work order. They are different outputs with different design constraints.

### 4. The Graph Is the Product

Everything testreg does -- `trace`, `audit`, `sprint`, `gaps`, `contract`, `diagnose` -- is a different view of the same dependency graph. The graph is built once from Go AST parsing, Wire resolution, SQLC mapping, and TypeScript scanning. Each command queries it differently.

This is not an accident of implementation. It is a design principle. Build the graph once, query it many ways. The graph captures the structure of the system. The commands are lenses for different questions about that structure.

### 5. Cross-Project Testing Reveals Assumptions

Testing against Metro-Grama found bugs that nutrition-project-v2 could never find. Different directory structure (`client/src/` vs `apps/web/`), different framework (Echo vs Chi), different database (SurrealDB vs PostgreSQL), different conventions. One project is a single data point. Two projects reveal which behaviors are general and which are accidental.

If your tool works on one project, you have a proof of concept. If it works on two unrelated projects, you might have a product.

### 6. Performance Optimization Pays for Itself in Adoption

The 7.9x speedup (graph built once vs per-feature) moved testreg from "too slow for CI" to "runs in CI on every push." The technical change was straightforward -- cache the graph instead of rebuilding it. The impact on adoption was not.

A 2-minute command gets run manually before sprint planning. A 14-second command gets run automatically on every pull request. The difference between "developer tool" and "CI gate" is often just performance.

### 7. Optional Features Beat Required Ones

`go/types` is opt-in for exact cross-package type resolution. Python support is annotation-only. The tool works with minimal configuration and gets better with more configuration. No one is forced to set up a full type-checking pass to get basic coverage tracking.

The fastest path to adoption is a tool that provides value with zero configuration and more value with investment. Required configuration is a barrier. Optional configuration is a feature.

---

## Closing: The Shape of the Session

This session was not a linear march from requirements to implementation. It was a conversation between a developer, an AI, and a tool that kept interrupting to say "actually, you missed something."

The findings documents provided the starting direction. Dogfooding provided the validation. Cross-project testing provided the generalization. And the AI provided the velocity to make the entire loop -- find gap, implement fix, validate fix, discover new gap -- run fast enough to complete in a single session.

The result is a tool that tracks 41 features about itself, validates its own health, runs against projects it was not designed for, and produces structured output that AI agents can consume without context preamble. It is, in a concrete and measurable sense, a tool that helps build itself.

That feedback loop -- static analysis informing development, development improving static analysis -- is what makes this approach different from writing a tool and then writing tests for it. The tool and its quality co-evolve. The bugs it finds in itself are the bugs its users would have found. The features it needs for itself are the features its users need.

testreg is its own best user, and that is exactly the point.
