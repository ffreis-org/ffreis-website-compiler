SHELL := /bin/bash

CONTAINER_COMMAND ?= podman
GOFMT ?= gofmt
GOLANGCI_LINT ?= golangci-lint
GITLEAKS ?= gitleaks
GOVULNCHECK ?= govulncheck
COVERAGE_MIN ?= 90

LEFTHOOK_VERSION ?= 1.7.10
LEFTHOOK_DIR ?= $(CURDIR)/.bin
LEFTHOOK_BIN ?= $(LEFTHOOK_DIR)/lefthook
PREFIX ?= ffreis

MUTATION_PACKAGES ?= ./internal/...
MUTATION_THRESHOLD ?= 60
IMAGE_PROVIDER ?=
IMAGE_TAG ?= local
COMPILER_IMAGE_NAME ?= website-compiler-cli
IMAGE_PREFIX := $(if $(IMAGE_PROVIDER),$(IMAGE_PROVIDER)/,)$(PREFIX)
IMAGE_ROOT := $(IMAGE_PREFIX)
WEBSITE_COMPILER_IMAGE ?= $(IMAGE_ROOT)/$(COMPILER_IMAGE_NAME):$(IMAGE_TAG)
WEBSITE_COMPILER_BUILDER_IMAGE ?= golang:1.25.8-bookworm
WEBSITE_COMPILER_RUNTIME_IMAGE ?= debian:bookworm-slim

export CONTAINER_COMMAND PREFIX IMAGE_PROVIDER IMAGE_TAG COMPILER_IMAGE_NAME IMAGE_PREFIX IMAGE_ROOT WEBSITE_COMPILER_IMAGE WEBSITE_COMPILER_BUILDER_IMAGE WEBSITE_COMPILER_RUNTIME_IMAGE

WC := ./website-compiler
WEBSITE_ROOT ?=
DIST_DIR ?= dist
EXAMPLE_ROOT := examples/hello-world
EXAMPLE_DIST := $(EXAMPLE_ROOT)/dist

.PHONY: mutation-test help info install build build-inline build-no-assets serve \
	example-build clean clean-example container-build docker-build ci-list \
	fmt fmt-check lint test test-race coverage-gate smoke-check quality-gates \
	validate plan \
	secrets-scan-staged hook-generated-drift \
	lefthook-bootstrap lefthook-install lefthook-run lefthook

## mutation-test: run mutation testing with gremlins (slow — intended for CI/weekly)
mutation-test: ## Run mutation testing with gremlins (slow — CI only)
	@which gremlins >/dev/null 2>&1 || go install github.com/go-gremlins/gremlins/cmd/gremlins@latest
	gremlins unleash --threshold-efficacy $(MUTATION_THRESHOLD) $(MUTATION_PACKAGES)

help: ## Show available compiler commands
	@awk 'BEGIN {FS = ":.*## "; printf "Usage: make <target> WEBSITE_ROOT=path [DIST_DIR=path]\n\nTargets:\n"} /^[a-zA-Z0-9_.-]+:.*## / {printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

info: ## Print effective variables
	@echo "WEBSITE_ROOT=$(WEBSITE_ROOT)"
	@echo "DIST_DIR=$(DIST_DIR)"
	@echo "CONTAINER_COMMAND=$(CONTAINER_COMMAND)"
	@echo "PREFIX=$(PREFIX)"
	@echo "IMAGE_PROVIDER=$(IMAGE_PROVIDER)"
	@echo "IMAGE_TAG=$(IMAGE_TAG)"
	@echo "COMPILER_IMAGE_NAME=$(COMPILER_IMAGE_NAME)"
	@echo "IMAGE_ROOT=$(IMAGE_ROOT)"
	@echo "WEBSITE_COMPILER_IMAGE=$(WEBSITE_COMPILER_IMAGE)"
	@echo "WEBSITE_COMPILER_BUILDER_IMAGE=$(WEBSITE_COMPILER_BUILDER_IMAGE)"
	@echo "WEBSITE_COMPILER_RUNTIME_IMAGE=$(WEBSITE_COMPILER_RUNTIME_IMAGE)"

install: ## Install website-compiler in GOPATH/bin
	go install ./cmd/website-compiler

build: ## Build static site
	@test -n "$(WEBSITE_ROOT)" || (echo "WEBSITE_ROOT is required, e.g. WEBSITE_ROOT=../my-website" && exit 1)
	$(WC) build -website-root $(WEBSITE_ROOT) -out $(DIST_DIR)

build-inline: ## Build static site with inlined assets
	@test -n "$(WEBSITE_ROOT)" || (echo "WEBSITE_ROOT is required, e.g. WEBSITE_ROOT=../my-website" && exit 1)
	$(WC) build -website-root $(WEBSITE_ROOT) -out $(DIST_DIR) -inline-assets

build-no-assets: ## Build static site without copying assets
	@test -n "$(WEBSITE_ROOT)" || (echo "WEBSITE_ROOT is required, e.g. WEBSITE_ROOT=../my-website" && exit 1)
	$(WC) build -website-root $(WEBSITE_ROOT) -out $(DIST_DIR) -copy-assets=false

serve: ## Serve website locally at :8080
	@test -n "$(WEBSITE_ROOT)" || (echo "WEBSITE_ROOT is required, e.g. WEBSITE_ROOT=../my-website" && exit 1)
	$(WC) serve -website-root $(WEBSITE_ROOT) -addr :8080

example-build: ## Build bundled hello-world example
	$(WC) build -website-root $(EXAMPLE_ROOT) -out $(EXAMPLE_DIST)

clean: ## Remove default dist output
	rm -rf $(DIST_DIR)

clean-example: ## Remove example dist output
	rm -rf $(EXAMPLE_DIST)

fmt: ## Format all Go files in place
	$(GOFMT) -w .

fmt-check: ## Fail if Go files are not gofmt-formatted
	@./scripts/hooks/check_required_tools.sh $(GOFMT)
	@out="$$(find . -type f -name '*.go' -not -path './vendor/*' -not -path './.git/*' -print0 | xargs -0 -r $(GOFMT) -l)"; \
	if [ -n "$$out" ]; then \
		echo "Unformatted Go files:"; \
		echo "$$out"; \
		echo "Run: $(GOFMT) -w <files>"; \
		exit 1; \
	fi

lint: ## Run golangci-lint
	@command -v $(GOLANGCI_LINT) >/dev/null 2>&1 || (echo "Missing tool: $(GOLANGCI_LINT). Install with: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest" && exit 1)
	$(GOLANGCI_LINT) run

validate: ## Static analysis and compilation check (go vet + build)
	go vet ./...
	go build -o /dev/null ./...

plan: ## Not applicable — use 'make validate' or 'make quality-gates' for Go repos
	@echo "INFO: 'plan' is Terraform-specific and does not apply to Go repos."
	@echo "      To verify compilation: make validate"
	@echo "      For full quality gates: make quality-gates"

test: ## Run unit tests
	go test ./...

test-race: ## Run tests with race detector
	go test -race ./...

coverage-gate: ## Run tests with coverage and fail if below COVERAGE_MIN
	@COVERAGE_MIN="$(COVERAGE_MIN)" ./scripts/hooks/check_coverage_gate.sh

smoke-check: ## Build hello-world example and validate output
	@set -euo pipefail; \
	tmp_dir="$$(mktemp -d)"; \
	trap 'rm -rf "$$tmp_dir"' EXIT; \
	go run ./cmd/build-static -website-root ./examples/hello-world -out "$$tmp_dir"; \
	test -f "$$tmp_dir/index.html"

secrets-scan-staged: ## Scan staged diff for secrets
	@command -v $(GITLEAKS) >/dev/null 2>&1 || (echo "Missing tool: $(GITLEAKS). Install: https://github.com/gitleaks/gitleaks#installing" && exit 1)
	$(GITLEAKS) protect --staged --redact

quality-gates: ## Run strict pre-push quality gates
	@command -v $(GOVULNCHECK) >/dev/null 2>&1 || (echo "Missing tool: $(GOVULNCHECK). Install with: go install golang.org/x/vuln/cmd/govulncheck@latest" && exit 1)
	$(MAKE) test
	$(MAKE) test-race
	$(MAKE) coverage-gate
	$(GOVULNCHECK) ./...
	$(MAKE) smoke-check

hook-generated-drift: ## Run generate target if present and fail on drift
	@set -euo pipefail; \
	if $(MAKE) -n generate >/dev/null 2>&1; then \
		$(MAKE) generate; \
		if ! git diff --quiet -- .; then \
			echo "Generated files are out of date. Run 'make generate' and commit updates."; \
			git status --short; \
			exit 1; \
		fi; \
	else \
		echo "No 'generate' target found; skipping generated drift check."; \
	fi

container-build: ## Build CLI container image
	@if [ -f containers/Dockerfile.cli ]; then \
		$(CONTAINER_COMMAND) build \
			--build-arg BUILDER_IMAGE="$(WEBSITE_COMPILER_BUILDER_IMAGE)" \
			--build-arg RUNTIME_IMAGE="$(WEBSITE_COMPILER_RUNTIME_IMAGE)" \
			-t "$(WEBSITE_COMPILER_IMAGE)" \
			-f containers/Dockerfile.cli .; \
	else \
		echo "containers/Dockerfile.cli not found"; \
	fi

docker-build: container-build ## Backward-compatible alias


PLATFORM_STANDARDS_SHA := b6a9ef92199954e3da5b80814321cb92f649fb81
PLATFORM_STANDARDS_RAW := https://raw.githubusercontent.com/FelipeFuhr/ffreis-platform-standards

HOOK_SCRIPTS := \
	check_merge_markers.sh \
	check_large_files.sh \
	check_binary_files.sh \
	check_commit_msg.sh \
	check_required_tools.sh

hook-scripts: ## Download bootstrap + hook scripts from ffreis-platform-standards
	@mkdir -p scripts/hooks
	@curl -fsSL "$(PLATFORM_STANDARDS_RAW)/$(PLATFORM_STANDARDS_SHA)/lefthook/bootstrap_lefthook.sh" \
		-o scripts/bootstrap_lefthook.sh && chmod +x scripts/bootstrap_lefthook.sh
	@for script in $(HOOK_SCRIPTS); do \
		curl -fsSL "$(PLATFORM_STANDARDS_RAW)/$(PLATFORM_STANDARDS_SHA)/lefthook/scripts/$$script" \
			-o "scripts/hooks/$$script" && chmod +x "scripts/hooks/$$script"; \
	done
	@echo "Hook scripts downloaded."

lefthook-bootstrap: hook-scripts ## Download lefthook binary into ./.bin
	LEFTHOOK_VERSION="$(LEFTHOOK_VERSION)" BIN_DIR="$(LEFTHOOK_DIR)" bash ./scripts/bootstrap_lefthook.sh

lefthook-install: lefthook-bootstrap ## Install git hooks if missing
	@if [ -x "$(LEFTHOOK_BIN)" ] && [ -x ".git/hooks/pre-commit" ] && [ -x ".git/hooks/pre-push" ] && [ -x ".git/hooks/commit-msg" ]; then \
		echo "lefthook hooks already installed"; \
		exit 0; \
	fi
	LEFTHOOK="$(LEFTHOOK_BIN)" "$(LEFTHOOK_BIN)" install

lefthook-run: lefthook-bootstrap ## Run hooks (pre-commit + commit-msg + pre-push)
	LEFTHOOK="$(LEFTHOOK_BIN)" "$(LEFTHOOK_BIN)" run pre-commit
	@tmp_msg="$$(mktemp)"; \
	echo "chore(hooks): validate commit-msg hook" > "$$tmp_msg"; \
	LEFTHOOK="$(LEFTHOOK_BIN)" "$(LEFTHOOK_BIN)" run commit-msg -- "$$tmp_msg"; \
	rm -f "$$tmp_msg"
	LEFTHOOK="$(LEFTHOOK_BIN)" "$(LEFTHOOK_BIN)" run pre-push

lefthook: lefthook-bootstrap lefthook-install lefthook-run ## Install hooks and run them

ci-list: ## List local CI workflows
	@ls -1 .github/workflows | sort
