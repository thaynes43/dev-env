# dev-env v2: the operator, the keeper, agentd and agent-run.
#
# Every target is safe to run in the shared dev-env pod (CLAUDE.md, "No CPU
# burners"): Go uses at most GO_PARALLELISM CPUs and packages at a time
# (GOMAXPROCS and -p), golangci-lint runs with --concurrency GO_PARALLELISM, and
# each command runs under `nice -n 19`. CI runs these same targets. Raise
# GO_PARALLELISM or clear NICE only on a machine of your own.

SHELL := /usr/bin/env bash
.SHELLFLAGS := -euo pipefail -c
.DEFAULT_GOAL := help

GO ?= go
GO_PARALLELISM ?= 2
NICE ?= nice -n 19

# Child processes inherit these too: `go list` under controller-gen and
# golangci-lint, and the test binaries.
export GOMAXPROCS := $(GO_PARALLELISM)
export GOFLAGS := $(strip $(GOFLAGS) -p=$(GO_PARALLELISM))
# Static binaries everywhere. agent-run must be static (D-06); the operator
# image and the agent image then need no libc of a matching version.
export CGO_ENABLED := 0

BIN_DIR := $(CURDIR)/bin
TOOLS_DIR := $(BIN_DIR)/tools
BINARIES := agent-run agentd dev-env-keeper dev-env-operator

# Stamped into every binary (internal/version). VERSION is the release tag; there
# is none before release-please's first release (B4). COMMIT stays empty here
# because the Go toolchain stamps the revision and a "-dirty" mark itself from
# the checkout. An empty value falls back to the toolchain's stamp. Where there is
# no .git, as in an image build, neither source exists: pass COMMIT=<sha>, and
# VERSION=<release tag> on a release build, or the binaries report "dev".
VERSION ?= $(shell git describe --tags --match 'v[0-9]*' --dirty 2>/dev/null)
COMMIT ?=
VERSION_PKG := github.com/thaynes43/dev-env/internal/version
LDFLAGS := -X $(VERSION_PKG).version=$(VERSION) -X $(VERSION_PKG).commit=$(COMMIT)

# Pinned tools, installed into bin/tools/ on first use. The file name carries
# the version, so a bump installs the new one.
# renovate: datasource=go depName=sigs.k8s.io/controller-tools
CONTROLLER_TOOLS_VERSION ?= v0.22.0
# renovate: datasource=go depName=github.com/golangci/golangci-lint/v2
GOLANGCI_LINT_VERSION ?= v2.14.0
# setup-envtest downloads kube-apiserver, etcd and kubectl for the envtest suites
# (from the controller-tools GitHub releases) into bin/tools/envtest/.
# renovate: datasource=go depName=sigs.k8s.io/controller-runtime/tools/setup-envtest
SETUP_ENVTEST_VERSION ?= v0.25.2
# The API server the envtest suites run against. It follows the main cluster's
# Kubernetes minor (v1.35.5 on 2026-10-06), not the client libraries in go.mod,
# so the suites prove the CRDs on the server haynes-ops applies them to. Bump it
# by hand when the cluster moves to a new minor; Renovate does not track it.
ENVTEST_K8S_VERSION ?= 1.35.0
CONTROLLER_GEN := $(TOOLS_DIR)/controller-gen-$(CONTROLLER_TOOLS_VERSION)
GOLANGCI_LINT := $(TOOLS_DIR)/golangci-lint-$(GOLANGCI_LINT_VERSION)
SETUP_ENVTEST := $(TOOLS_DIR)/setup-envtest-$(SETUP_ENVTEST_VERSION)
ENVTEST_DIR := $(TOOLS_DIR)/envtest

.PHONY: help
help: ## List the targets.
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: generate
generate: $(CONTROLLER_GEN) ## Regenerate the deep-copy code and the CRDs in config/crd/.
	$(NICE) $(CONTROLLER_GEN) object paths=./api/...
	$(NICE) $(CONTROLLER_GEN) crd paths=./api/... output:crd:artifacts:config=config/crd

.PHONY: check-generated
check-generated: generate ## Fail if the generated files differ from what is committed.
	@if [ -n "$$(git status --porcelain -- api config/crd)" ]; then \
		echo "Generated files are out of date. Run 'make generate' and commit the result:"; \
		git status --porcelain -- api config/crd; \
		git --no-pager diff -- api config/crd; \
		exit 1; \
	fi

.PHONY: lint
lint: $(GOLANGCI_LINT) ## Run golangci-lint.
	$(NICE) $(GOLANGCI_LINT) run --concurrency $(GO_PARALLELISM) ./...

# The envtest suites (internal/testenv) start a real kube-apiserver and etcd per
# package. Both inherit GOMAXPROCS and the nice level from go test, and -p caps
# how many packages, and so how many API servers, run at once.
.PHONY: test
test: $(SETUP_ENVTEST) ## Run the unit tests and the envtest suites, GO_PARALLELISM packages at a time.
	assets="$$($(ENVTEST_USE))"; $(ENVTEST_WRITABLE); \
		KUBEBUILDER_ASSETS="$$assets" $(NICE) $(GO) test -p $(GO_PARALLELISM) ./...

.PHONY: envtest
envtest: $(SETUP_ENVTEST) ## Download the envtest binaries into bin/tools/envtest/, for a plain `go test`.
	@assets="$$($(ENVTEST_USE))"; $(ENVTEST_WRITABLE); echo "KUBEBUILDER_ASSETS=$$assets"

# setup-envtest prints the binaries' directory, downloading them on first use.
ENVTEST_USE = $(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(ENVTEST_DIR) -p path
# setup-envtest leaves its version directory read-only, which makes
# `git worktree remove --force` (and v1's worktree sweeper) fail. Undo that.
ENVTEST_WRITABLE = chmod -R u+w $(ENVTEST_DIR)

.PHONY: build
build: $(addprefix $(BIN_DIR)/,$(BINARIES)) ## Build every binary into bin/ and check that agent-run is static and small.
	@$(GO) version -m $(BIN_DIR)/agent-run | grep -q 'CGO_ENABLED=0' \
		|| { echo "bin/agent-run was not built with CGO_ENABLED=0 (D-06)"; exit 1; }
	@if $(GO) version -m $(BIN_DIR)/agent-run | grep -E '^\s+dep\s+(k8s\.io|sigs\.k8s\.io)/'; then \
		echo "bin/agent-run links the Kubernetes libraries above; it imports only the standard library, apiv1 and agentd's protocol (D-06, D-50)"; exit 1; fi

# The CLI also ships as a macOS binary (laptops). Static like the Linux one.
.PHONY: build-agent-run-darwin
build-agent-run-darwin: ## Cross-build agent-run for darwin/arm64 into bin/.
	GOOS=darwin GOARCH=arm64 $(NICE) $(GO) build -p $(GO_PARALLELISM) -trimpath -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/agent-run-darwin-arm64 ./cmd/agent-run

# One rule per binary. FORCE leaves the up-to-date check to Go's build cache.
$(BIN_DIR)/%: FORCE
	$(NICE) $(GO) build -p $(GO_PARALLELISM) -trimpath -ldflags '$(LDFLAGS)' -o $@ ./cmd/$*

.PHONY: FORCE
FORCE:

.PHONY: tools
tools: $(CONTROLLER_GEN) $(GOLANGCI_LINT) $(SETUP_ENVTEST) ## Install the pinned tools into bin/tools/.

$(CONTROLLER_GEN):
	@mkdir -p $(TOOLS_DIR)
	GOBIN=$(TOOLS_DIR) $(NICE) $(GO) install -p $(GO_PARALLELISM) sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)
	mv $(TOOLS_DIR)/controller-gen $@

$(GOLANGCI_LINT):
	@mkdir -p $(TOOLS_DIR)
	GOBIN=$(TOOLS_DIR) $(NICE) $(GO) install -p $(GO_PARALLELISM) github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	mv $(TOOLS_DIR)/golangci-lint $@

$(SETUP_ENVTEST):
	@mkdir -p $(TOOLS_DIR)
	GOBIN=$(TOOLS_DIR) $(NICE) $(GO) install -p $(GO_PARALLELISM) sigs.k8s.io/controller-runtime/tools/setup-envtest@$(SETUP_ENVTEST_VERSION)
	mv $(TOOLS_DIR)/setup-envtest $@

.PHONY: clean
clean: ## Remove the built binaries. The tools in bin/tools/ stay.
	rm -f $(addprefix $(BIN_DIR)/,$(BINARIES)) $(BIN_DIR)/agent-run-darwin-arm64
