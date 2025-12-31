# Makefile for zstack-ovn-kubernetes
#
# Build targets:
#   make build          - Build all binaries
#   make build-controller - Build controller binary
#   make build-node     - Build node agent binary
#   make build-cni      - Build CNI binary
#   make test           - Run unit tests
#   make test-integration - Run integration tests
#   make test-e2e       - Run end-to-end tests
#   make lint           - Run linters
#   make docker-build   - Build Docker images
#   make docker-push    - Push Docker images
#   make clean          - Clean build artifacts

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod
GOFMT=gofmt
GOLINT=golangci-lint

# Binary names
CONTROLLER_BINARY=zstack-ovnkube-controller
NODE_BINARY=zstack-ovnkube-node
CNI_BINARY=zstack-ovn-cni

# Build directories
BUILD_DIR=bin
CMD_DIR=cmd

# Version information
VERSION ?= 0.1.0
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

# Docker parameters
# IMAGE_REPO: Docker registry/repository prefix (e.g., ghcr.io/jiayi-1994)
# IMAGE_TAG: Image tag (defaults to VERSION)
# PLATFORMS: Target platforms for multi-arch builds
#
# Usage examples:
#   make docker-build                                    # Build with defaults
#   make docker-build IMAGE_REPO=myregistry IMAGE_TAG=v1.0.0
#   make docker-push IMAGE_REPO=ghcr.io/jiayi-1994 IMAGE_TAG=latest
#   make docker-buildx IMAGE_REPO=myregistry PLATFORMS=linux/amd64
#
IMAGE_REPO ?= ghcr.io/jiayi-1994
REGISTRY ?= $(IMAGE_REPO)
IMAGE_TAG ?= $(VERSION)
PLATFORMS ?= linux/amd64,linux/arm64

# LDFLAGS for version injection
LDFLAGS=-ldflags "-X main.Version=$(VERSION) -X main.GitCommit=$(GIT_COMMIT) -X main.BuildDate=$(BUILD_DATE)"

.PHONY: all build build-controller build-node build-cni test lint clean docker-build docker-push \
	docker-build-controller docker-build-node docker-build-cni \
	docker-buildx docker-buildx-controller docker-buildx-node docker-buildx-cni \
	docker-buildx-setup docker-buildx-load

# Default target
all: build

# Build all binaries
build: build-controller build-node build-cni

# Build controller binary
build-controller:
	@echo "Building $(CONTROLLER_BINARY)..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(CONTROLLER_BINARY) ./$(CMD_DIR)/$(CONTROLLER_BINARY)

# Build node agent binary
build-node:
	@echo "Building $(NODE_BINARY)..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(NODE_BINARY) ./$(CMD_DIR)/$(NODE_BINARY)

# Build CNI binary
build-cni:
	@echo "Building $(CNI_BINARY)..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(CNI_BINARY) ./$(CMD_DIR)/$(CNI_BINARY)

# Run unit tests
test:
	@echo "Running unit tests..."
	$(GOTEST) -v -race -coverprofile=coverage.out ./pkg/...

# Run integration tests
test-integration:
	@echo "Running integration tests..."
	$(GOTEST) -v -tags=integration ./test/integration/...

# Run end-to-end tests
test-e2e:
	@echo "Running e2e tests..."
	$(GOTEST) -v -tags=e2e ./test/e2e/...

# Run linters
lint:
	@echo "Running linters..."
	$(GOLINT) run ./...

# Format code
fmt:
	@echo "Formatting code..."
	$(GOFMT) -s -w .

# Verify code formatting
verify-fmt:
	@echo "Verifying code formatting..."
	@test -z "$$($(GOFMT) -s -l . | tee /dev/stderr)"

# Download dependencies
deps:
	@echo "Downloading dependencies..."
	$(GOMOD) download

# Tidy dependencies
tidy:
	@echo "Tidying dependencies..."
	$(GOMOD) tidy

# Generate code (CRDs, deepcopy, etc.)
generate:
	@echo "Generating code..."
	$(GOCMD) generate ./...

# Build Docker images
docker-build: docker-build-controller docker-build-node docker-build-cni

# Build controller Docker image
docker-build-controller:
	@echo "Building $(CONTROLLER_BINARY) Docker image..."
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(REGISTRY)/$(CONTROLLER_BINARY):$(IMAGE_TAG) \
		-f Dockerfile.controller .

# Build node Docker image
docker-build-node:
	@echo "Building $(NODE_BINARY) Docker image..."
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(REGISTRY)/$(NODE_BINARY):$(IMAGE_TAG) \
		-f Dockerfile.node .

# Build CNI Docker image
docker-build-cni:
	@echo "Building $(CNI_BINARY) Docker image..."
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(REGISTRY)/$(CNI_BINARY):$(IMAGE_TAG) \
		-f Dockerfile.cni .

# Build multi-architecture Docker images using buildx
docker-buildx: docker-buildx-controller docker-buildx-node docker-buildx-cni

# Build multi-arch controller image
docker-buildx-controller:
	@echo "Building multi-arch $(CONTROLLER_BINARY) Docker image..."
	docker buildx build \
		--platform $(PLATFORMS) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(REGISTRY)/$(CONTROLLER_BINARY):$(IMAGE_TAG) \
		-f Dockerfile.controller \
		--push .

# Build multi-arch node image
docker-buildx-node:
	@echo "Building multi-arch $(NODE_BINARY) Docker image..."
	docker buildx build \
		--platform $(PLATFORMS) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(REGISTRY)/$(NODE_BINARY):$(IMAGE_TAG) \
		-f Dockerfile.node \
		--push .

# Build multi-arch CNI image
docker-buildx-cni:
	@echo "Building multi-arch $(CNI_BINARY) Docker image..."
	docker buildx build \
		--platform $(PLATFORMS) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(REGISTRY)/$(CNI_BINARY):$(IMAGE_TAG) \
		-f Dockerfile.cni \
		--push .

# Setup docker buildx builder for multi-arch builds
# Run this once before using docker-buildx targets
docker-buildx-setup:
	@echo "Setting up docker buildx builder..."
	@docker buildx inspect zstack-ovn-builder > /dev/null 2>&1 || \
		docker buildx create --name zstack-ovn-builder --driver docker-container --bootstrap
	docker buildx use zstack-ovn-builder
	@echo "Buildx builder 'zstack-ovn-builder' is ready"

# Build multi-arch images and load to local docker (for testing)
# Note: --load only works with single platform
docker-buildx-load:
	@echo "Building and loading images to local docker..."
	docker buildx build \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(REGISTRY)/$(CONTROLLER_BINARY):$(IMAGE_TAG) \
		-f Dockerfile.controller \
		--load .
	docker buildx build \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(REGISTRY)/$(NODE_BINARY):$(IMAGE_TAG) \
		-f Dockerfile.node \
		--load .
	docker buildx build \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(REGISTRY)/$(CNI_BINARY):$(IMAGE_TAG) \
		-f Dockerfile.cni \
		--load .

# Push Docker images
docker-push:
	@echo "Pushing Docker images..."
	docker push $(REGISTRY)/$(CONTROLLER_BINARY):$(IMAGE_TAG)
	docker push $(REGISTRY)/$(NODE_BINARY):$(IMAGE_TAG)
	docker push $(REGISTRY)/$(CNI_BINARY):$(IMAGE_TAG)

# Tag images as latest
docker-tag-latest:
	@echo "Tagging images as latest..."
	docker tag $(REGISTRY)/$(CONTROLLER_BINARY):$(IMAGE_TAG) $(REGISTRY)/$(CONTROLLER_BINARY):latest
	docker tag $(REGISTRY)/$(NODE_BINARY):$(IMAGE_TAG) $(REGISTRY)/$(NODE_BINARY):latest
	docker tag $(REGISTRY)/$(CNI_BINARY):$(IMAGE_TAG) $(REGISTRY)/$(CNI_BINARY):latest

# Clean build artifacts
clean:
	@echo "Cleaning..."
	rm -rf $(BUILD_DIR)
	rm -f coverage.out

# Help
help:
	@echo "Available targets:"
	@echo "  build                  - Build all binaries"
	@echo "  build-controller       - Build controller binary"
	@echo "  build-node             - Build node agent binary"
	@echo "  build-cni              - Build CNI binary"
	@echo "  test                   - Run unit tests"
	@echo "  test-integration       - Run integration tests"
	@echo "  test-e2e               - Run end-to-end tests"
	@echo "  lint                   - Run linters"
	@echo "  fmt                    - Format code"
	@echo "  deps                   - Download dependencies"
	@echo "  tidy                   - Tidy dependencies"
	@echo "  generate               - Generate code"
	@echo ""
	@echo "Docker targets:"
	@echo "  docker-build           - Build Docker images (single arch)"
	@echo "  docker-build-controller - Build controller Docker image"
	@echo "  docker-build-node      - Build node Docker image"
	@echo "  docker-build-cni       - Build CNI Docker image"
	@echo "  docker-push            - Push Docker images to registry"
	@echo "  docker-tag-latest      - Tag images as latest"
	@echo ""
	@echo "Multi-arch Docker targets (buildx):"
	@echo "  docker-buildx-setup    - Setup buildx builder (run once)"
	@echo "  docker-buildx          - Build and push multi-arch images"
	@echo "  docker-buildx-controller - Build multi-arch controller image"
	@echo "  docker-buildx-node     - Build multi-arch node image"
	@echo "  docker-buildx-cni      - Build multi-arch CNI image"
	@echo "  docker-buildx-load     - Build and load to local docker (testing)"
	@echo ""
	@echo "Other targets:"
	@echo "  clean                  - Clean build artifacts"
	@echo "  help                   - Show this help message"
	@echo ""
	@echo "Variables:"
	@echo "  IMAGE_REPO=$(IMAGE_REPO)   - Docker registry/repository (alias: REGISTRY)"
	@echo "  IMAGE_TAG=$(IMAGE_TAG)     - Image tag (default: VERSION)"
	@echo "  PLATFORMS=$(PLATFORMS)     - Target platforms for buildx"
	@echo "  VERSION=$(VERSION)         - Version string"
	@echo ""
	@echo "Examples:"
	@echo "  make docker-build IMAGE_REPO=myregistry IMAGE_TAG=v1.0.0"
	@echo "  make docker-push IMAGE_REPO=ghcr.io/jiayi-1994"
	@echo "  make docker-buildx PLATFORMS=linux/amd64"
	@echo "  make docker-buildx-setup  # Run once to setup buildx"
	@echo "  make docker-buildx-load   # Build and load locally for testing"
