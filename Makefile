# RiskKernel — common dev tasks. `make help` lists them.

BINARY      := riskkernel
PKG         := github.com/prashar32/riskkernel
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE        ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w \
	-X $(PKG)/internal/version.Version=$(VERSION) \
	-X $(PKG)/internal/version.Commit=$(COMMIT) \
	-X $(PKG)/internal/version.Date=$(DATE)
IMAGE       ?= ghcr.io/prashar32/riskkernel

.DEFAULT_GOAL := help

.PHONY: help
help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  %-14s %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the static binary
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/riskkernel

.PHONY: test
test: ## Run tests with the race detector
	go test -race ./...

.PHONY: vet
vet: ## go vet
	go vet ./...

.PHONY: fmt
fmt: ## Check formatting (fails if anything is unformatted)
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then echo "unformatted:"; echo "$$unformatted"; exit 1; fi

.PHONY: vuln
vuln: ## Run govulncheck
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

.PHONY: check
check: fmt vet test ## fmt + vet + test

.PHONY: sdk-test
sdk-test: ## Run the Python SDK tests (stdlib only)
	cd sdks/python && python3 -m unittest discover -s tests -t . -v

.PHONY: docker
docker: ## Build the Docker image locally
	docker build \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) \
		-t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

.PHONY: run
run: build ## Build and run the daemon
	./$(BINARY) serve

.PHONY: clean
clean: ## Remove build artifacts
	rm -f $(BINARY) coverage.out
	rm -rf dist
