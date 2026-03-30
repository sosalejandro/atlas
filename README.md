# testreg

A CLI tool for tracking test coverage across Go, TypeScript, Playwright, Jest, and Maestro. Maintains a YAML registry mapping features to test files and generates coverage dashboards.

## Installation

```bash
go install github.com/sosalejandro/testreg@latest
```

Or build from source:

```bash
git clone https://github.com/sosalejandro/testreg.git
cd testreg
go build -o testreg .
```

## Quick Start

```bash
# Initialize the registry with template domain files
testreg init

# Scan your project for test files and map them to features
testreg scan

# View the coverage dashboard
testreg status

# Check a specific feature
testreg check auth.login

# Generate a markdown coverage report
testreg report
```

## Commands

### `testreg init`

Bootstraps the registry directory with template YAML files. Idempotent: running it again merges new features without overwriting existing entries.

### `testreg scan`

Discovers test files across all platforms and maps them to features in the registry. Scanners included:

- **Go** (`*_test.go`, `*_e2e_test.go`)
- **Vitest** (`*.test.ts`, `*.test.tsx`)
- **Playwright** (`*.spec.ts`)
- **Maestro** (`*.yaml` in mobile e2e directories)
- **Jest** (files in `__tests__/` directories)

Unmapped tests are saved to `_unmapped.yaml` for manual review.

### `testreg status`

Shows a terminal table with coverage metrics per domain.

```
testreg status                    # All domains
testreg status --domain auth      # Filter by domain
testreg status --priority critical # Filter by priority
testreg status --format json      # JSON output
```

### `testreg check <feature-id>`

Displays detailed coverage for a single feature, including:

- All coverage entries with status and files
- Gap analysis
- Actionable suggestions

```
testreg check auth.login
testreg check meals.log --format json
```

### `testreg update`

Ingests test results and updates the registry YAML files.

```
testreg update --playwright ./test-results/
testreg update --gotest ./go-test-output.json
testreg update --maestro ./maestro-output/
```

### `testreg report`

Generates a comprehensive coverage report.

```
testreg report                           # Markdown (default)
testreg report --format json             # JSON
testreg report --format terminal         # Terminal table
testreg report --output ./COVERAGE.md    # Custom output path
```

## Global Flags

```
--registry-dir <path>   Path to registry YAML files (default: docs/testing/registry)
--project-root <path>   Project root directory (auto-detected from git root)
```

## Registry YAML Format

Each domain is stored as a separate YAML file:

```yaml
domain: auth
description: Authentication and authorization features
features:
  - id: auth.login
    name: User Login
    description: Email/password authentication with JWT tokens
    roles: [patient, nutritionist, admin]
    priority: critical
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
          status: covered
          files: [src/services/auth_service_test.go]
          mocked: true
        web:
          status: missing
      integration:
        backend:
          status: covered
          files: [src/handlers/auth_e2e_test.go]
          mocked: false
      e2e:
        web:
          status: covered
          files: [e2e/auth.spec.ts]
          last_run: "2026-03-30"
          pass_rate: 1.0
```

## Architecture

The project follows hexagonal architecture:

- `internal/domain/` -- Core types with zero dependencies
- `internal/ports/` -- Interface definitions
- `internal/app/` -- Use cases orchestrating domain logic
- `internal/adapters/` -- Implementations (YAML store, scanners, parsers, reporters)
- `cmd/` -- Cobra command definitions

## License

MIT
