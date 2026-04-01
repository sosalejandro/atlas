# Case Study: testreg in Career Lenses

**Project:** Career Lenses -- AI-powered career management SaaS  
**Stack:** Go (API) + React/TypeScript (SPA) + Python (AI service)  
**Date:** 2026-03-31  
**Author:** Alejandro Sosa  
**AI Assistant:** Claude Code (Opus 4.6)

---

## 1. Executive Summary

`testreg` is a 1,200-line Go CLI tool that scans test files for annotations, detects source files without corresponding tests, and audits test quality through assertion density and mock-to-real ratio analysis. Built in a single development session alongside Epic 3 (AI Coaching) of Career Lenses, it transformed "784 tests pass" from a false confidence signal into an actionable quality dashboard. It caught 22 untested source files, a false-positive load test hiding an unwired rate limiter, and 4 files with near-empty test functions -- none of which would have been found by running the test suite alone.

---

## 2. The Problem: "All Tests Pass" Is Not Enough

Midway through Epic 3 implementation, the project had an impressive-looking test suite:

- 55 annotated features showing "covered" status
- Tests across Go, Python, and TypeScript platforms
- Green CI on every check

But a simple question exposed the illusion: **"Are these tests actually valid? Can we guarantee the quality?"**

The answer was no. The test suite told us that individual functions worked in isolation. It said nothing about:

- Whether every source file had a corresponding test file
- Whether tests contained meaningful assertions or were empty shells
- Whether the system worked when the pieces were connected
- What percentage of tests used mocks versus real services

We needed a tool that asked harder questions than "did the tests pass?"

---

## 3. How testreg Evolved (Session Timeline)

### Phase 1: Basic Annotation Scanner

Started as a simple scanner that found `// @testreg feature.name` comments in test files and wrote them to `tests/registry.json`.

```bash
go run ./cmd/testreg scan   # find annotations
go run ./cmd/testreg status # show coverage table
```

Result: "55 features, 0 gaps." This was misleading -- it only checked whether test FILES had annotations, not whether source files had tests.

### Phase 2: Python Support

The AI service (Python) had 19 tests that were invisible to the scanner because it only matched `//` comments, not Python's `#` comments.

**Discovery:** The regex `//\s*@testreg` doesn't match `# @testreg`. Updated to `(?://|#)\s*@testreg`.

Added scan rules for `ai-service/tests/test_*.py` and an AI platform column to the status table. Python tests appeared in the registry for the first time.

### Phase 3: Source Gap Detection (`gaps` command)

**The breakthrough.** Instead of asking "do test files have annotations?", we asked "do source files have tests?"

```bash
go run ./cmd/testreg gaps
```

This scanned source files across all platforms and checked if each had a corresponding test using language-specific conventions:
- **Go:** Any `*_test.go` in the same directory
- **Python:** `tests/test_<filename>.py`
- **TypeScript:** Co-located `<filename>.test.ts` or `.test.tsx`

**Result: 22 source files with zero tests.** The "55 features covered" report had hidden them completely.

The `--format prompt` flag generates a structured brief per gap -- file path, category, suggested test path, feature ID, source preview, and category-specific test suggestions. This became the most impactful feature: we dispatched 3 AI agents in parallel with gap prompts and closed all 22 gaps in one round.

### Phase 4: Quality Audit (`audit` command)

Having tests isn't enough if they're empty. The audit command counts:

- **Test functions** per file (language-aware: `func Test*`, `def test_*`, `it(`)
- **Assertions** per file (`assert`, `expect(`, `t.Error`, etc.)
- **Assertion ratio** (assertions / test functions -- target >= 1.5)
- **Mock vs real ratio** from `#mocked` / `#real` flags

```bash
go run ./cmd/testreg audit
```

**Findings:**
- `sentry_test.go`: 4 test functions, 3 assertions (0.8 ratio) -- nearly empty
- `upload_e2e_test.go`: 5 tests, 5 assertions (1.0) -- one assertion per test
- 78% of tests flagged `#mocked`, only 22% `#real`

The 5-point quality score became the gate:
1. Tests exist
2. Assertion density >= 1.5
3. Has integration tests (#real)
4. No empty test files
5. All source files have tests

### Phase 5: k6 JavaScript Support

The audit showed 4 load test files as "unknown language." Adding JavaScript assertion patterns (`check(`, `group(`) made them auditable. This led to actually RUNNING the load tests, which led to discovering:

- All GraphQL load tests returned 401 (no auth headers)
- The rate limiter load test always passed because **the rate limiter was never wired into the middleware chain**

A "passing" test that can never fail is worse than no test at all. We added threshold enforcement (`status_429: count>0`) so the test correctly FAILS when the rate limiter isn't active.

### Phase 6: Annotation Safety (First 10 Lines)

A subtle bug: test fixture strings inside Go test files like `"// @testreg auth.login\npackage auth\n"` matched the annotation regex, creating junk entries in the registry. Fix: only scan the first 10 lines of each file (annotations belong at the top, before imports).

---

## 4. Gaps Found That Would Have Been Missed

| Gap | How Found | Consequence If Missed |
|---|---|---|
| 22 source files with no tests | `testreg gaps` | Shipped untested streaming, enrichment, chat composer code |
| `sentry_test.go` at 0.8 assertions/test | `testreg audit` | 4 test functions that proved almost nothing |
| Rate limiter not wired | k6 audit led to running load tests | Rate limiting never enforced in production |
| Python tests invisible | Missing scan rule | AI service coverage at 0% in reports despite 19 tests existing |
| `ChatPanel.test.tsx` only tested empty state | Code review triggered by audit | Primary user flow (send message, see response) completely untested |
| Async retry path untested | Code review triggered by audit | Production async code path had zero coverage |
| Thread repo no integration tests | `gaps` + system health | CRUD never verified against real SurrealDB |
| UTF-8 truncation bug | Code review triggered by gap closure | Multi-byte characters corrupted in thread previews |
| Unbounded conversation history | Code review triggered by gap closure | Memory growth in long coaching sessions |
| Global keyboard listener | Code review triggered by gap closure | Slash command menu intercepting keystrokes across entire page |

**Chain reaction example:** `testreg audit` flagged k6 as "unknown language" -> added JS support -> ran k6 tests -> found 401 errors -> added auth to load tests -> found rate limiter passing with 0 rejections -> added threshold enforcement -> discovered rate limiter not wired in `main.go` -> wired it -> verified with k6 -> rate limiter now enforcing.

Without testreg, the rate limiter would have shipped unwired to production.

---

## 5. Features Discovered During the Session

### `--format prompt` (Agent Dispatch)

**Origin:** 22 gaps needed tests. Writing them manually would take hours.

**Idea:** testreg already knows the file path, category, language, and what kind of tests the file needs. Package that as a structured prompt.

**Output:** A 492-line prompt with per-file briefs:
```markdown
### `web/src/hooks/useAIStream.ts`
- **Category**: react-hook
- **Create**: `web/src/hooks/useAIStream.test.ts`
- **@testreg**: `hook.useAIStream`
- **Source preview**: (first 8 lines)
- **What to test**:
  - Test hook returns expected initial state
  - Test state transitions on actions/events
  - Test cleanup on unmount
```

**Impact:** 3 AI agents dispatched in parallel, all 22 gaps closed in ~10 minutes. The agents didn't need to explore the codebase -- testreg pre-computed the analysis.

### `audit` Command

**Origin:** "The tests exist, but are they actually valid?"

**Idea:** Count assertions per test function. A test with 0 assertions proves nothing.

**Result:** Found 4 files below 1.5 ratio. All strengthened. Led to code review that found 3 critical and 7 important issues.

### k6 Support

**Origin:** "unknown language" in audit for load test files.

**Chain:** Added JS patterns -> ran load tests -> found unwired rate limiter.

This feature was added in response to an audit gap, and its biggest value wasn't the audit itself -- it was triggering the execution of tests that had been silently skipped.

### Flag Enforcement (`#real` / `#mocked`)

**Origin:** "22% real test rate" revealed by audit.

**Impact:** 63 unflagged files classified in one agent dispatch. Clear visibility into what the test suite validates versus what it assumes.

---

## 6. The AI-Assisted Development Workflow

A repeatable pattern emerged:

```
1. Implement feature (via AI agent)
2. testreg scan            -> update registry
3. testreg gaps            -> find untested source files
4. testreg gaps --format prompt -> generate agent briefs
5. Dispatch AI agents to write tests
6. testreg audit           -> verify test quality
7. Code review agent       -> verify test correctness
8. Fix issues found
9. testreg status          -> final dashboard
```

**Token efficiency comparison:**

Without testreg:
- Developer: "Write tests for the missing files"
- AI agent: Explores codebase, guesses what needs tests, reads files to understand patterns
- Burns context window on exploration before writing a single test

With testreg:
- Developer: (pastes `testreg gaps --format prompt` output)
- AI agent: Gets exact file paths, categories, suggested feature IDs, source previews
- Writes targeted tests immediately

The exploration phase is eliminated. testreg does the analysis once (< 1 second), the AI agent does the implementation.

---

## 7. Stack-Specific Benefits

### Go

- **Convention alignment:** Go's `*_test.go` in same package maps directly to testreg's gap detection (any test file in directory = covered)
- **Build tags:** `//go:build integration` and `//go:build e2e` naturally separate test tiers; testreg's scan rules respect this
- **Assertion patterns:** `t.Error`, `t.Fatal`, `assert.`, `require.` are distinct and countable -- no ambiguity
- **Compile-time verification:** Go's type system catches wiring issues early; testreg catches coverage issues that the compiler can't

### React/TypeScript

- **Co-location convention:** `Component.tsx` -> `Component.test.tsx` is a clean 1:1 mapping for gap detection
- **vitest + @testing-library:** `expect(`, `toBeInTheDocument`, `toHaveBeenCalled` all countable as assertions
- **Smart exclusions:** `.stories.tsx` (Storybook) and `components/ui/` (shadcn wrappers) excluded automatically -- they're tested by their maintainers
- **Hook testing:** `renderHook` pattern from @testing-library maps cleanly to testreg's hook category with specific test suggestions

### Python

- **Comment style:** `# @testreg` annotation required extending the regex from `//` to `(?://|#)`
- **Naming convention:** `tests/test_*.py` maps to source files by stripping the `test_` prefix
- **`#mocked` importance:** Python AI tests heavily mock httpx/OpenRouter; the flag makes this explicit rather than hidden
- **pytest patterns:** `assert`, `pytest.raises` as assertion patterns; `def test_*` as function patterns

---

## 8. Measurable Impact

| Metric | Before testreg | After testreg | Delta |
|---|---|---|---|
| Features tracked | 0 | 93 | +93 |
| Source file gaps | Unknown | 0 | 22 found and fixed |
| Test functions | ~300 | 784 | +484 |
| Assertions | ~800 | 2,158 | +1,358 |
| Low-density files | Unknown | 0 | 4 found and fixed |
| Quality score | N/A | 5/5 | -- |
| False positives caught | 0 | 1 | Rate limiter load test |
| Bugs found via gap chain | 0 | 3 | UTF-8, async retry, unbounded history |
| Code review issues | 0 | 10 | 3 critical, 7 important |
| Agent dispatch time | Manual exploration | ~5 min | Via `--format prompt` |
| k6 load tests | Never run | 3/3 pass | Health, GraphQL, ratelimit |

---

## 9. What Static Analysis + AI Achieves

The key insight: **static analysis tools don't replace AI -- they make AI dramatically more effective.**

Without testreg, the AI assistant:
- Reports "all tests pass" and moves on
- Doesn't know which source files lack tests
- Can't quantify assertion quality
- Misses false positives in load tests
- Spends tokens exploring the codebase before acting

With testreg, the AI assistant:
- Gets precise gap data to act on immediately
- Generates targeted test briefs (saving tokens and context window)
- Verifies its own work quality after writing (audit loop)
- Catches systemic issues invisible to test runners (mock ratio, assertion density)
- Discovers chain-reaction bugs through structured analysis

**The pattern is generalizable:** any static analysis tool that produces structured output (`--format json` or `--format prompt`) becomes an AI force multiplier. The tool does the analysis cheaply and deterministically. The AI does the implementation creatively and at scale.

This is not about replacing developers or replacing AI. It's about the **compound effect** of cheap deterministic analysis (static tool, < 1 second) feeding expensive creative execution (AI agent, minutes). The tool finds 22 gaps in milliseconds. The AI writes 22 test files in 10 minutes. Neither alone achieves what both together produce.

---

## 10. Recommendations for Other Teams

1. **Build annotation-based coverage tracking early.** It's a one-day investment that compounds over the entire project lifecycle.

2. **Source gap detection is more valuable than annotation coverage.** "Do test files have annotations?" is a weaker question than "do source files have tests?"

3. **Assertion density is a better quality signal than test count.** 784 tests with 0 assertions proves nothing. 100 tests with 5 assertions each proves a lot.

4. **`--format prompt` for AI agent dispatch saves significant tokens.** Pre-computed analysis eliminates the exploration phase that burns context window.

5. **Flag tests as `#real` / `#mocked` from day one.** The ratio matters -- 78% mocked means the system has never been tested as a whole.

6. **Tests that always pass are worse than no tests.** The rate limiter load test passed for weeks because it tested a feature that wasn't wired. Add threshold enforcement so tests fail when the feature they test isn't active.

7. **Static analysis before code review catches structural issues.** The reviewer can then focus on behavioral correctness instead of "does this file have tests?"

8. **Chain reactions are where the real value hides.** testreg audit -> k6 "unknown" -> run k6 -> find 401 -> find unwired rate limiter. Each link in the chain was invisible without the previous one.

---

*This case study documents a single development session. The testreg tool, the gaps it found, and the workflow it enabled are specific to Career Lenses but the patterns -- annotation tracking, gap detection, assertion auditing, AI agent dispatch -- are applicable to any Go/React/Python project using AI-assisted development.*
