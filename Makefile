# atlas build entry points.
#
# Every CI step is a target here, and every target's logic lives in
# .github/scripts/*.sh rather than in this file or in workflow YAML. The
# reason is testability: .github/scripts/scripts_test.sh exercises those
# scripts on a laptop, whereas a step written inline in a workflow can only
# be tested by pushing and waiting.
#
# Run `make` for the list.

SHELL := /usr/bin/env bash
SCRIPTS := .github/scripts

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_.-]+:.*?## / {printf "  \033[1m%-22s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# --- build ------------------------------------------------------------------

.PHONY: print-go-version
print-go-version: ## Print the pinned Go toolchain version (used by CI)
	@tr -d '[:space:]' < $(SCRIPTS)/toolchain.txt

.PHONY: build
build: ## Reproducible build for the host platform into dist/
	@$(SCRIPTS)/build.sh

.PHONY: build-dev
build-dev: ## Like build, but tolerate a Go toolchain other than the pinned one
	@ATLAS_SKIP_TOOLCHAIN_CHECK=1 $(SCRIPTS)/build.sh

.PHONY: build-matrix
build-matrix: ## Cross-compile every shipped target and assert they are cgo-free
	@$(SCRIPTS)/build-matrix.sh

.PHONY: checksums
checksums: ## Write dist/SHA256SUMS over the built artifacts
	@$(SCRIPTS)/checksums.sh dist

.PHONY: repro
repro: ## Build this commit twice and prove the bytes are identical
	@$(SCRIPTS)/verify-repro.sh

.PHONY: repro-matrix
repro-matrix: ## As repro, but across every shipped target (slow)
	@TARGETS="$$(sed -n 's/^ATLAS_TARGETS="\(.*\)"$$/\1/p' $(SCRIPTS)/lib.sh)" $(SCRIPTS)/verify-repro.sh

.PHONY: clean
clean: ## Remove build output
	@rm -rf dist

# --- checks -----------------------------------------------------------------

.PHONY: vet
vet: ## go vet
	@go vet ./...

.PHONY: test
test: ## go test with the race detector
	@go test ./... -race -timeout 5m

.PHONY: lint
lint: ## golangci-lint over the Go source
	@golangci-lint run ./packages/... ./internal/...

.PHONY: test-scripts
test-scripts: ## Test the release/CI scripts (fast subset)
	@bash $(SCRIPTS)/scripts_test.sh

.PHONY: test-scripts-full
test-scripts-full: ## Test the release/CI scripts including the six-target cross-compile
	@ATLAS_SCRIPT_TESTS_SLOW=1 bash $(SCRIPTS)/scripts_test.sh

.PHONY: version-check
version-check: ## Check the binary version, release manifest and changelog agree
	@$(SCRIPTS)/check-version-consistency.sh

.PHONY: ci
ci: vet test lint test-scripts-full ## Everything CI runs, minus the OS matrix
