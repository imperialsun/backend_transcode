# Demeter Backend

Backend `Go + Fiber + SQLite` for the Transcode frontend backend mode and admin panel.

## Main capabilities

- App authentication and dedicated admin authentication with isolated cookies.
- Multi-tenant organization model with global roles, organization roles, and per-user permission overrides.
- Backend-owned user settings storage exposed under `/api/v1/settings`.
- Activity ingestion and admin summaries for transcription and report usage.
- `Demeter Sante` relay routes that call Mistral server-side.
- SQLite persistence with WAL enabled and optional bootstrap admin creation.

## Quickstart

### Local development

```bash
cp .env.example .env
go run ./cmd/server
```

Alternative launcher:

```bash
./scripts/run-local-backend.sh
```

Default base URL: `http://localhost:8080`

### Docker Compose

Prod-like launch with the final image:

```bash
docker compose up --build
```

Development launch with `go run` and the source tree mounted:

```bash
docker compose -f compose.dev.yml up
```

Both Compose files declare their environment variables inline. Real secret values can still be injected through shell variables or Compose substitution without being hardcoded in the YAML.

## Health checks

- `GET /healthz`
- `GET /readyz`

`/readyz` requires both SQLite and the Mistral client to be configured.

## Environment summary

- Core runtime: `APP_ENV`, `PORT`, `SQLITE_PATH`, `JWT_SECRET`
- App sessions: `ACCESS_TTL_MINUTES`, `REFRESH_TTL_HOURS`, `COOKIE_SECURE`
- Admin sessions: `ADMIN_ACCESS_TTL_MINUTES`, `ADMIN_REFRESH_TTL_HOURS`
- CORS and origin control: `APP_CORS_ORIGINS`, `ADMIN_CORS_ORIGINS`, legacy `CORS_ORIGINS`
- Provider relay: `MISTRAL_API_BASE_URL`, `MISTRAL_API_KEY`
- First-start bootstrap: `BOOTSTRAP_ADMIN_EMAIL`, `BOOTSTRAP_ADMIN_PASSWORD`, `BOOTSTRAP_ORG_NAME`

The full environment reference lives in [`docs/en/deployment-operations.md`](docs/en/deployment-operations.md) and [`docs/fr/deployment-operations.md`](docs/fr/deployment-operations.md).

## Documentation

- Documentation portal: [`docs/README.md`](docs/README.md)
- French docs: [`docs/fr/index.md`](docs/fr/index.md)
- English docs: [`docs/en/index.md`](docs/en/index.md)

## Project guides

- Contributing entrypoint: [`CONTRIBUTING.md`](CONTRIBUTING.md)
- Security policy: [`SECURITY.md`](SECURITY.md)
