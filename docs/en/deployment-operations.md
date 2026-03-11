# Deployment and Operations

## Overview

The repo provides:

- a local binary through `go run ./cmd/server`,
- a helper script `scripts/run-local-backend.sh`,
- a multi-stage `golang -> distroless` Docker image.

## Environment variables

| Variable | Default | Usage |
| --- | --- | --- |
| `APP_ENV` | `development` | affects some defaults, including `COOKIE_SECURE` |
| `PORT` | `8080` | HTTP listening port |
| `SQLITE_PATH` | `./backend.sqlite` or `/data/backend.sqlite` in the image | SQLite path |
| `JWT_SECRET` | insecure dev value | JWT signing secret |
| `ACCESS_TTL_MINUTES` | `15` | app access TTL |
| `REFRESH_TTL_HOURS` | `720` | app refresh TTL |
| `ADMIN_ACCESS_TTL_MINUTES` | `10` | admin access TTL |
| `ADMIN_REFRESH_TTL_HOURS` | `12` | admin refresh TTL |
| `COOKIE_SECURE` | `false` in dev, `true` in prod | cookie `Secure` flag |
| `APP_CORS_ORIGINS` | `http://localhost:3000,http://localhost:5173` | app origins |
| `ADMIN_CORS_ORIGINS` | `http://localhost:4173` | admin origins and origin filter |
| `CORS_ORIGINS` | same value as app origins | legacy fallback for `APP_CORS_ORIGINS` |
| `MISTRAL_API_BASE_URL` | `https://api.mistral.ai` | upstream base URL |
| `MISTRAL_API_KEY` | empty | key required for Demeter and `readyz` |
| `BOOTSTRAP_ADMIN_EMAIL` | empty | admin email created on first boot |
| `BOOTSTRAP_ADMIN_PASSWORD` | empty | admin password created on first boot |
| `BOOTSTRAP_ORG_NAME` | `Default Organization` | bootstrap org name |

## Local launch

```bash
cp .env.example .env
./scripts/run-local-backend.sh
```

The script:

- loads `.env` when it exists,
- forces defaults for `APP_ENV`, `PORT`, and `SQLITE_PATH`,
- runs `go run ./cmd/server`.

## Docker image

### Build

```bash
docker build -t demeter-backend .
```

### Run

```bash
docker run --rm \
  -p 8080:8080 \
  -v "$(pwd)/data:/data" \
  -e APP_ENV=production \
  -e JWT_SECRET=change-me \
  -e MISTRAL_API_KEY=change-me \
  demeter-backend
```

Image characteristics:

- built with `CGO_ENABLED=0`,
- binary located at `/server`,
- runtime `gcr.io/distroless/base-debian12:nonroot`,
- `VOLUME ["/data"]`,
- `EXPOSE 8080`.

## Minimal runbook

### Startup

1. define the environment variables,
2. mount a persistent volume for SQLite,
3. start the process,
4. verify:

```bash
curl -fsS http://localhost:8080/healthz
curl -fsS http://localhost:8080/readyz
```

### Shutdown

The server listens for `SIGINT` and `SIGTERM`, then attempts a 10 second graceful shutdown.

### Data

- SQLite runs in WAL mode,
- `*.sqlite-wal` and `*.sqlite-shm` files are expected,
- back up the full data directory, not only `backend.sqlite`.

## Readiness caveat

`/readyz` is not a generic app health endpoint:

- it pings the database,
- it also requires `MISTRAL_API_KEY` and a non-empty base URL.

A deployment without Mistral therefore has a green `healthz` but a red `readyz`.

## Links

- Security: [`security-privacy.md`](security-privacy.md)
- CI and quality: [`ci-quality-observability.md`](ci-quality-observability.md)
- Troubleshooting: [`troubleshooting.md`](troubleshooting.md)
