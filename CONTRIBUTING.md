# Contributing

## Workflow

- Work on a dedicated branch.
- Keep commits atomic and focused.
- In PR description, include impact, risks, and validation evidence.

## Tooling setup

```bash
make install-tools
export PATH="$HOME/.local/bin:$PATH"
golangci-lint version
```

## Required local gate before PR

```bash
go test ./...
go test ./... -race -coverprofile=coverage.out
golangci-lint run
go vet ./...
test -z "$(gofmt -l .)"
go build ./cmd/server
```

## Commit / push policy

- Before `git commit` or `git push`, share:
  - current diff
  - outputs of all required checks above
- Wait for explicit approval before committing/pushing.

## Security reminders

- Never commit secrets.
- Keep `JWT_SECRET` and `MISTRAL_API_KEY` in environment variables only.
