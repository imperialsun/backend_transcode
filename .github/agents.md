# AI Agent Instructions — Demeter Backend

Quick facts

- Run locally: `go run ./cmd/server`
- Build: `go build ./cmd/server`
- Test: `go test ./...`
- SQLite is mandatory (`SQLITE_PATH`), WAL enabled at startup.

Architecture

- Entry: `cmd/server/main.go`
- API handlers: `internal/api`
- Auth/JWT/password: `internal/auth`
- RBAC helpers: `internal/rbac`
- SQLite store + migrations + seed: `internal/store`
- Mistral relay client: `internal/mistral`
- Env config: `internal/config`

Rules for changes

- Keep strict organization isolation (`organization_id`) on business data reads/writes.
- Keep provider permissions enforced server-side (`provider.*`).
- Never expose backend secrets (especially `MISTRAL_API_KEY`) to clients.
- Keep `standalone` mode behavior untouched on frontend integrations.

Tooling setup

```bash
make install-tools
export PATH="$HOME/.local/bin:$PATH"
golangci-lint version
```

Required pre-commit checks

```bash
go test ./...
go test ./... -race -coverprofile=coverage.out
golangci-lint run
go vet ./...
test -z "$(gofmt -l .)"
go build ./cmd/server
```

Commit policy

- Before commit/push, share:
  - diff
  - outputs of all required checks
- Wait for explicit user approval before `git commit` / `git push`.
