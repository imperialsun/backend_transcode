# Contributing to the Backend

## Workflow

- work on a dedicated branch,
- keep commits atomic,
- include impact, risks, validation, and documentation updates in the PR.

## Required local gate before PR

```bash
make docs-check
go test ./...
go test ./... -race -coverprofile=coverage.out
golangci-lint run
go vet ./...
test -z "$(gofmt -l .)"
go build ./cmd/server
```

## Documentation rules

- every backend behavior change must be documented in both FR and EN,
- portal links must remain valid,
- new routes, permissions, environment variables, or schema changes must be reflected in the relevant reference pages.

## Commit / push policy

Before `git commit` or `git push`, share:

- the current diff,
- the outputs of the checks above.

Then wait for explicit approval if the collaboration workflow requires it.

## Security reminders

- never commit secrets,
- keep `JWT_SECRET` and `MISTRAL_API_KEY` in the environment,
- do not disclose vulnerability details outside a private channel.

## Links

- Root guide: [`CONTRIBUTING.md`](../../CONTRIBUTING.md)
- Security policy: [`SECURITY.md`](../../SECURITY.md)
- CI and quality: [`ci-quality-observability.md`](ci-quality-observability.md)
