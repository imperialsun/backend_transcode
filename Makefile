.PHONY: install-tools fmt-check test test-race lint vet build ci

install-tools:
	GOBIN=$$HOME/.local/bin go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

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

ci: fmt-check test test-race lint vet build
