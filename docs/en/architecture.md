# Architecture

## Overview

The backend is a `Go + Fiber + SQLite` service organized in layers:

- `cmd/server/main.go`: global bootstrap, middleware, routes, shutdown.
- `internal/api/*`: HTTP handlers, auth middleware, simple validation, request logging.
- `internal/store/*`: SQLite access, migrations, RBAC seeding, settings, activity, audit persistence.
- `internal/auth/*`: password hashing, JWT, refresh tokens, CSRF.
- `internal/mistral/*`: HTTP client used by the `Demeter Sante` routes.
- `internal/rbac/*`: small helpers for role and permission checks.

## Startup sequence

1. `config.Load()` reads the environment and applies defaults.
2. `store.Open()` creates the SQLite directory if needed, enables `foreign_keys`, `WAL`, `synchronous = NORMAL`, then runs migration and seed.
3. Admin bootstrap is applied only if the database is empty and bootstrap variables are present.
4. `api.App` assembles `Config`, `Store`, and `MistralClient`.
5. Fiber registers:
   - request logger,
   - recover middleware,
   - admin origin filter,
   - CORS,
   - health, auth, settings, activity, provider, and admin routes.
6. The process listens on `PORT` and waits for `SIGINT` or `SIGTERM` to perform a 10 second graceful shutdown.

## Middleware request path

Processing order:

1. request / response logging,
2. panic recovery,
3. unexpected origin blocking on `/api/v1/admin*`,
4. app + admin CORS,
5. auth middleware for the route group,
6. permission / scope / CSRF middleware,
7. business handler.

## Route groups

| Group | Prefix | Role |
| --- | --- | --- |
| Health | `/healthz`, `/readyz` | liveness and readiness |
| App auth | `/api/v1/auth/*` | app login, refresh, logout, `me` |
| Admin auth | `/api/v1/admin/auth/*` | admin login, refresh, logout, `me` |
| Settings | `/api/v1/settings*` | user settings read and write |
| Activity | `/api/v1/activity/*`, `/api/v1/admin/activity/*` | usage ingestion + admin aggregates |
| Demeter | `/api/v1/providers/demeter-sante/*` | server-side Mistral relay |
| Admin | `/api/v1/admin/*` | organizations, users, roles, permissions |

## Technical boundaries

| Area | Main dependencies | Contract |
| --- | --- | --- |
| API | Fiber, `internal/store`, `internal/auth` | parse JSON, enforce auth, return JSON or HTTP codes |
| Store | `database/sql`, `modernc.org/sqlite` | source of truth for users, roles, settings, sessions, activity |
| Auth | JWT HMAC, Argon2id, random tokens | no server-side session state except refresh sessions in DB |
| Mistral relay | `net/http` | passes requests upstream and returns upstream status/body |

## Multi-tenancy and live claims

- Every user belongs to exactly one organization.
- JWT claims are not the only control. On each protected request, the middleware reloads the user, organization, roles, and permissions from the database.
- An `inactive` user or organization is denied even if the access token has not expired yet.

## Persistence

- Single SQLite database with `SetMaxOpenConns(4)`, `SetMaxIdleConns(4)`, and `busy_timeout=5000 ms`.
- `refresh_sessions` only stores refresh token hashes.
- `user_settings` stores an opaque JSON blob owned by the frontend.
- `activity_usage_events` stores idempotent usage events keyed by `event_id`.
- `audit_logs` captures admin logins and admin mutations.

## Links

- Auth and RBAC: [`authentication-rbac.md`](authentication-rbac.md)
- Database: [`database.md`](database.md)
- Deployment: [`deployment-operations.md`](deployment-operations.md)
