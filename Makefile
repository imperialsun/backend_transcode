.PHONY: install-tools docs-check fmt-check test test-race lint vet build ci

install-tools:
	GOBIN=$$HOME/.local/bin go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

docs-check:
	go run ./cmd/docscheck

fmt-check:
	test -z "$$(gofmt -l .)"

test:
	go test ./...

test-race:
	go test ./... -race -coverprofile=coverage.out

lint:
	golangci-lint run

vet:
	go vet ./...

build:
	go build ./cmd/server

ci: docs-check fmt-check test test-race lint vet build
