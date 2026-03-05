# Self-Healing Operator Makefile

PROJECT_NAME := ariadna-self-healing
MODULE       := github.com/ariadna-ops/ariadna-self-healing
BINARY       := selfhealing-operator

VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

GO      := go
GOFLAGS := -trimpath
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.gitCommit=$(GIT_COMMIT) -X main.buildDate=$(BUILD_DATE)

REGISTRY ?= ghcr.io/ariadna-ops
IMAGE    := $(REGISTRY)/$(BINARY)
TAG      ?= $(VERSION)

NAMESPACE ?= selfhealing-system

GOBIN := $(shell $(GO) env GOBIN)
ifeq ($(GOBIN),)
GOBIN := $(shell $(GO) env GOPATH)/bin
endif
CONTROLLER_GEN ?= $(shell which controller-gen 2>/dev/null || echo "$(GOBIN)/controller-gen")

BIN_DIR            := bin
CONFIG_DIR         := config
KUSTOMIZE_CRD      := $(CONFIG_DIR)/crd
TEST_MANIFESTS_DIR := test/e2e/testdata/manifests

OVERLAY ?= default

# =============================================================================
# Targets
# =============================================================================

.PHONY: help
help:
	@echo ""
	@echo "Usage: make <target>"
	@echo ""
	@echo "Development:"
	@echo "  fmt, vet, lint, test, test-short, coverage, verify  - Code quality and tests"
	@echo "  build, run                    - Build and run operator"
	@echo "  generate, manifests            - Code generation"
	@echo ""
	@echo "Docker:"
	@echo "  docker-build, docker-push      - Operator image"
	@echo "  docker-build-sender            - otel-sender image (e2e)"
	@echo ""
	@echo "Kubernetes:"
	@echo "  deploy, undeploy               - Operator (CRDs + deployment)"
	@echo "  e2e-deploy, e2e-undeploy       - E2E manifests (scenarios + test workloads)"
	@echo ""
	@echo "Other:"
	@echo "  clean, install-tools"
	@echo ""

.PHONY: all
all: build

# --- Development ---

.PHONY: fmt vet lint test test-short coverage verify tidy
fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

lint:
	golangci-lint run

test:
	$(GO) test -v -race -coverprofile=coverage.out ./...

test-short:
	$(GO) test -v -short ./...

coverage: test
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

verify: fmt vet lint test

tidy:
	$(GO) mod tidy

# --- Build ---

.PHONY: build build-linux build-sender run run-dry
build:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) ./cmd/operator

build-linux:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY)-linux-amd64 ./cmd/operator

build-sender:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -o $(BIN_DIR)/otel-sender ./cmd/otel-sender

run:
	$(GO) run ./cmd/operator --log-level=debug

run-dry:
	$(GO) run ./cmd/operator --log-level=debug --dry-run

# --- Code Generation ---

.PHONY: generate manifests
generate: controller-gen
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./api/..."

manifests: controller-gen
	@mkdir -p $(CONFIG_DIR)/crd/bases
	$(CONTROLLER_GEN) crd paths="./api/..." output:crd:artifacts:config=$(CONFIG_DIR)/crd/bases

# --- Docker ---

.PHONY: docker-build docker-push docker-buildx docker-build-sender
docker-build:
	docker build -t $(IMAGE):$(TAG) \
		--build-arg VERSION=$(VERSION) --build-arg GIT_COMMIT=$(GIT_COMMIT) --build-arg BUILD_DATE=$(BUILD_DATE) .

docker-push:
	docker push $(IMAGE):$(TAG)

docker-buildx:
	docker buildx build --platform linux/amd64,linux/arm64 -t $(IMAGE):$(TAG) \
		--build-arg VERSION=$(VERSION) --build-arg GIT_COMMIT=$(GIT_COMMIT) --build-arg BUILD_DATE=$(BUILD_DATE) --push .

docker-build-sender:
	docker build -f Dockerfile.otelsender -t $(REGISTRY)/otel-sender:$(TAG) .

# --- Kubernetes ---

.PHONY: deploy undeploy e2e-deploy e2e-undeploy
deploy: manifests
	@OVERLAY_PATH=$(CONFIG_DIR)/default; \
	if [ "$(OVERLAY)" = "dev" ]; then OVERLAY_PATH=$(CONFIG_DIR)/overlays/dev; fi; \
	if [ "$(OVERLAY)" = "production" ]; then OVERLAY_PATH=$(CONFIG_DIR)/overlays/production; fi; \
	kubectl apply -k $(KUSTOMIZE_CRD) && kubectl apply -k $$OVERLAY_PATH

undeploy:
	@OVERLAY_PATH=$(CONFIG_DIR)/default; \
	if [ "$(OVERLAY)" = "dev" ]; then OVERLAY_PATH=$(CONFIG_DIR)/overlays/dev; fi; \
	if [ "$(OVERLAY)" = "production" ]; then OVERLAY_PATH=$(CONFIG_DIR)/overlays/production; fi; \
	kubectl delete -k $$OVERLAY_PATH --ignore-not-found; \
	kubectl delete -k $(KUSTOMIZE_CRD) --ignore-not-found; \
	kubectl delete namespace $(NAMESPACE) --ignore-not-found

e2e-deploy:
	@for f in $(TEST_MANIFESTS_DIR)/*.yaml; do \
		echo "Deploying $$(basename $$f .yaml)..."; \
		kubectl apply -f $$f; \
	done

e2e-undeploy:
	@for f in $(TEST_MANIFESTS_DIR)/*.yaml; do \
		echo "Removing $$(basename $$f .yaml)..."; \
		kubectl delete -f $$f 2>/dev/null || true; \
	done

# --- Cleanup ---

.PHONY: clean clean-all
clean:
	rm -rf $(BIN_DIR)
	rm -f coverage.out coverage.html

clean-all: clean
	rm -rf $(CONFIG_DIR)/crd/bases

# --- Tools ---

.PHONY: install-tools
install-tools:
	$(GO) install sigs.k8s.io/controller-tools/cmd/controller-gen@latest

controller-gen:
	@which controller-gen > /dev/null 2>&1 || ($(MAKE) install-tools)
