# Demeter Backend

Backend `Go + Fiber + SQLite` for Transcode frontend backend mode.

## Features

- Authentication (`login`, `refresh`, `logout`, `me`) with secure cookies.
- Multi-tenant model: one user belongs to one organization.
- RBAC with global roles, organization roles, and per-user permission overrides.
- User settings source-of-truth on backend, synchronized by frontend on login.
- `Demeter Santé` provider relay endpoints that call Mistral server-side.
- SQLite with WAL enabled and persistent storage support.

## Quick start

```bash
cp .env.example .env
go run ./cmd/server
```

By default, API listens on `http://localhost:8080` and exposes routes under `/api/v1`.

## Environment variables

- `PORT` (default `8080`)
- `SQLITE_PATH` (default `./backend.sqlite`)
- `JWT_SECRET` (required in production)
- `ACCESS_TTL_MINUTES` (default `15`)
- `REFRESH_TTL_HOURS` (default `720`)
- `COOKIE_SECURE` (`true`/`false`)
- `CORS_ORIGINS` (CSV)
- `MISTRAL_API_KEY` (required for Demeter relay routes and `readyz`)
- `MISTRAL_API_BASE_URL` (default `https://api.mistral.ai`)
- `BOOTSTRAP_ADMIN_EMAIL`
- `BOOTSTRAP_ADMIN_PASSWORD`
- `BOOTSTRAP_ORG_NAME` (default `Default Organization`)

## Health endpoints

- `GET /healthz`
- `GET /readyz`

## Main API surface

- `/api/v1/auth/*`
- `/api/v1/settings*`
- `/api/v1/providers/demeter-sante/*`
- `/api/v1/admin/*`
