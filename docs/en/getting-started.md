# Getting Started

## Prerequisites

- Go `1.25.7` or compatible with `go.mod`.
- `make` for the standard repo commands.
- Docker is optional if you want to run the containerized stacks locally.
- A `MISTRAL_API_KEY` if you want to use the Demeter routes or get a green `readyz`.

## Local setup

```bash
cp .env.example .env
go run ./cmd/server
```

Alternative launcher with automatic `.env` loading:

```bash
./scripts/run-local-backend.sh
```

By default, the API listens on `http://localhost:8080` and business routes live under `/api/v1`.

## Minimal verification

```bash
curl -fsS http://localhost:8080/healthz
curl -fsS http://localhost:8080/readyz
```

`/healthz` only checks that the process answers.
`/readyz` requires both SQLite and the Mistral client to be configured.

## Useful commands

- `make docs-check`: validates the documentation structure and Markdown links.
- `make test`: runs `go test ./...`.
- `make ci`: chains docs, format, tests, lint, vet, and build.
- `./scripts/run-local-backend.sh`: loads `.env` and runs `go run ./cmd/server`.

## Bootstrap admin

On the first startup only, the backend can create an organization and an admin if:

- the `users` table is empty,
- `BOOTSTRAP_ADMIN_EMAIL` is set,
- `BOOTSTRAP_ADMIN_PASSWORD` is set.

The bootstrap password is hashed with Argon2id before insertion.

## Docker execution

```bash
docker build -t transcode-backend:local .
docker run --rm \
  -p 8080:8080 \
  -v "$(pwd)/data:/data" \
  -e APP_ENV=production \
  -e JWT_SECRET=change-me \
  -e MISTRAL_API_KEY=change-me \
  transcode-backend:local
```

The image writes SQLite to `/data/backend.sqlite` by default.

## Docker Compose

Prod-like launch with the existing distroless image:

```bash
docker compose up --build
```

Development launch with `go run` inside `golang:1.25.7` and the repo mounted into the container:

```bash
docker compose -f compose.dev.yml up
```

The Compose files keep the full `environment:` blocks inline. Secrets are still expected through variable substitution such as `JWT_SECRET` and `MISTRAL_API_KEY`, rather than hardcoded values committed to the repo.

There is intentionally no Docker `healthcheck`. Use `GET /healthz` for a generic manual check. `GET /readyz` also requires SQLite access and a configured Mistral client, so it stays red when `MISTRAL_API_KEY` is empty.

## Frequent setup issues

### `readyz` returns 503

This happens when:

- `MISTRAL_API_KEY` is empty,
- `MISTRAL_API_BASE_URL` is invalid,
- SQLite cannot be opened.

### No bootstrap admin is created

This happens if the database already contains at least one user. `EnsureBootstrap` only runs on an empty database.

### Cookies are not being set locally

Check that `COOKIE_SECURE=false` when testing over plain HTTP.

## Recommended next reading

- Architecture: [`architecture.md`](architecture.md)
- Auth and RBAC: [`authentication-rbac.md`](authentication-rbac.md)
- Deployment: [`deployment-operations.md`](deployment-operations.md)
