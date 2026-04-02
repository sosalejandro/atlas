# testreg scan

Discover test files across all platforms and map them to features using `@testreg` annotations. Unmapped tests are saved to `_unmapped.yaml` for manual review. The registry YAML files are updated with new file references and status changes.

Scanners: Go (`*_test.go`), Vitest (`*.test.ts`), Playwright (`*.spec.ts`), Jest (`__tests__/`), Maestro (`*.yaml`), Python (`test_*.py`, `*_test.py`).

## Usage

```
testreg scan [flags]
```

No additional flags — scan discovers and maps everything automatically.

## Examples

### Standard scan

```
$ testreg scan

Scan complete.
  Total test files: 1103
  Mapped:           995
  Unmapped:         108
```

*Output from nutrition-project-v2 — 184 features across 16 domains.*

### First scan on a new project (with --discover)

```
$ testreg init --discover && testreg scan

Discovered 43 routes → 43 features across 15 domains
  auth: 5 features
  careers: 5 features
  enroll: 5 features
  subjects: 4 features
  ...

Scan complete.
  Total test files: 6
  Mapped:           3 (auto-mapped by directory proximity)
  Unmapped:         3
```

*Output from Metro-Grama — an Echo/SurrealDB application with 43 routes.*

## How It Works

1. All 8 scanners run concurrently (Go goroutines)
2. Each scanner walks the project tree looking for its file pattern
3. Tests with `@testreg` annotations are mapped to their declared feature
4. Tests without annotations are saved to `_unmapped.yaml`
5. Registry YAML files are updated with discovered test file paths and status

## Tips

- Run `testreg scan` before `testreg audit` or `testreg status` to ensure fresh data
- Or use `testreg audit --rescan` to combine both in one command
- Check `_unmapped.yaml` after scanning to find tests that need `@testreg` annotations
- Scan is fast (~0.5s for 771 test files) — run it freely
