.PHONY: install-tools docs-check fmt-check test test-race lint vet build ci local-check

GO_BUILD_CACHE ?= $(CURDIR)/tmp/go-build-cache
GOLANGCI_LINT_CACHE_DIR ?= $(CURDIR)/tmp/golangci-lint-cache
GOPATH := $(shell go env GOPATH)
GO_VERSION := $(shell awk '/^go[[:space:]]/ { print $$2; exit }' go.mod)
GOLANGCI_LINT_VERSION ?= v2.11.4
GOLANGCI_LINT_BIN ?= $(CURDIR)/bin/golangci-lint
GOLANGCI_LINT_TIMEOUT ?= 15m
GOLANGCI_LINT_CACHE_ENTRY := $(GOPATH)/pkg/mod/cache/download/github.com/golangci/golangci-lint/v2/@v/$(GOLANGCI_LINT_VERSION).info
GOLANGCI_LINT_PROXY ?= $(if $(wildcard $(GOLANGCI_LINT_CACHE_ENTRY)),file://$(GOPATH)/pkg/mod/cache/download,https://proxy.golang.org)
GOLANGCI_LINT_GOTOOLCHAIN ?= go$(GO_VERSION)

export GOCACHE := $(GO_BUILD_CACHE)
export GOLANGCI_LINT_CACHE := $(GOLANGCI_LINT_CACHE_DIR)

install-tools: $(GOLANGCI_LINT_BIN)

$(GOLANGCI_LINT_BIN): Makefile go.mod
	mkdir -p $(dir $(GOLANGCI_LINT_BIN)) $(GO_BUILD_CACHE) $(GOLANGCI_LINT_CACHE_DIR)
	GOTOOLCHAIN=$(GOLANGCI_LINT_GOTOOLCHAIN) GOCACHE=$(GO_BUILD_CACHE) GOBIN=$(dir $(GOLANGCI_LINT_BIN)) GOPROXY=$(GOLANGCI_LINT_PROXY) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

docs-check:
	go run ./cmd/docscheck

fmt-check:
	test -z "$$(gofmt -l .)"

test:
	go test ./...

test-race:
	go test ./... -race -coverprofile=coverage.out

lint: $(GOLANGCI_LINT_BIN)
	GOCACHE=$(GO_BUILD_CACHE) GOLANGCI_LINT_CACHE=$(GOLANGCI_LINT_CACHE_DIR) $(GOLANGCI_LINT_BIN) run --timeout=$(GOLANGCI_LINT_TIMEOUT)

vet:
	go vet ./...

build:
	go build ./cmd/server

ci: docs-check fmt-check test test-race lint vet build

local-check: fmt-check lint test vet build
