# CI, Quality, and Observability

## Recommended local gate

```bash
make docs-check
go test ./...
go test ./... -race -coverprofile=coverage.out
golangci-lint run
go vet ./...
test -z "$(gofmt -l .)"
go build ./cmd/server
```

## Makefile targets

| Target | Role |
| --- | --- |
| `docs-check` | validates FR/EN structure and Markdown links |
| `fmt-check` | ensures `gofmt -l .` is empty |
| `test` | `go test ./...` |
| `test-race` | `go test ./... -race -coverprofile=coverage.out` |
| `lint` | `golangci-lint run` |
| `vet` | `go vet ./...` |
| `build` | `go build ./cmd/server` |
| `ci` | chains all checks above |

## GitHub workflows

### `ci.yml`

- checkout,
- setup Go,
- download dependencies,
- `make docs-check`,
- unit tests,
- race test + coverage,
- lint,
- vet,
- format check,
- build,
- upload of `coverage.out`.

### `codeql.yml`

- Go static analysis,
- runs on PRs, `main` pushes, scheduled runs, and manual triggers.

### `prod-smoke.yml`

- builds the Docker image,
- starts the backend container,
- verifies `healthz` and `readyz`,
- also covers login, `auth/me`, `settings`, and `settings/reset`.

### `trivy.yml`

- filesystem scan,
- Docker image scan,
- SARIF publication into GitHub code scanning.

## Runtime observability

- `RequestLogger` logs every HTTP request with user/org context when available.
- `GET /healthz` and `GET /readyz` serve as probes.
- `audit_logs` capture sensitive admin activity.
- Admin activity endpoints provide lightweight business observability.

## Expectations for contributions

- every behavior change must update both FR and EN docs,
- docs links must stay valid,
- any new route, permission, environment variable, or schema change must appear in the relevant reference pages.

## Links

- Detailed contribution guide: [`contributing.md`](contributing.md)
- Activity: [`activity-observability.md`](activity-observability.md)
- Security: [`security-privacy.md`](security-privacy.md)
