.PHONY: docs-check fmt-check test test-race lint vet build ci local-check

GO_BUILD_CACHE ?= $(CURDIR)/tmp/go-build-cache
GOLANGCI_LINT_CACHE_DIR ?= $(CURDIR)/tmp/golangci-lint-cache
GOLANGCI_LINT_TIMEOUT ?= 15m

export GOCACHE := $(GO_BUILD_CACHE)
export GOLANGCI_LINT_CACHE := $(GOLANGCI_LINT_CACHE_DIR)

docs-check:
	go run ./cmd/docscheck

fmt-check:
	test -z "$$(gofmt -l .)"

test:
	go test -v ./...

test-race:
	go test ./... -race -coverprofile=coverage.out

lint:
	GOCACHE=$(GO_BUILD_CACHE) GOLANGCI_LINT_CACHE=$(GOLANGCI_LINT_CACHE_DIR) golangci-lint run --timeout=$(GOLANGCI_LINT_TIMEOUT)

vet:
	go vet ./...

build:
	go build ./cmd/server

ci: docs-check fmt-check test test-race lint vet build

local-check: fmt-check lint test vet build
