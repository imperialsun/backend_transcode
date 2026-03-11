# Contributing

This file is the short entrypoint. The detailed guides live in:

- French: [`docs/fr/contributing.md`](docs/fr/contributing.md)
- English: [`docs/en/contributing.md`](docs/en/contributing.md)

## Minimum local gate before PR

```bash
make docs-check
go test ./...
go test ./... -race -coverprofile=coverage.out
golangci-lint run
go vet ./...
test -z "$(gofmt -l .)"
go build ./cmd/server
```

## Scope reminders

- Keep changes focused and test-backed.
- Update both FR and EN docs when backend behavior changes.
- Do not commit secrets or private credentials.
