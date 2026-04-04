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

- `RequestTrace` reuses `X-Request-ID` or generates a shared `trace_id` and propagates it through the backend.
- `RequestLogger` logs every HTTP request as a trace-shaped step line with the route path, `step=request_completed` or `step=request_failed`, `trace_id`, user/org context when available, and final status/duration fields.
- `RequestTimeout` emits `step=request_timeout`, and auth denials emit `step=access_denied`, so one request can be followed end to end.
- Non-trivial handlers emit short step logs for auth, settings, activity, admin, demeter, meetings, mistral, mailer, store lifecycle, and reports generation flows.
- Lifecycle steps ending in `_success` are routine logs and are not persisted in `backend_error_events`; only error, failed, timeout, and HTTP 5xx steps are captured.
- Outgoing services never log mail bodies, transcripts, passwords, or tokens; they only log counters, statuses, and compact error summaries.
- Logs stay boundary-only and use stable route paths instead of raw URLs, so query strings never leak into runtime logs.
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
