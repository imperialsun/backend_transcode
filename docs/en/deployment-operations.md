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
| `APP_CORS_ORIGINS` | `http://localhost:3000,http://localhost:4173` | app origins |
| `ADMIN_CORS_ORIGINS` | `http://localhost:4173` | admin origins and origin filter |
| `CORS_ORIGINS` | same value as app origins | legacy fallback for `APP_CORS_ORIGINS` |
| `MISTRAL_API_BASE_URL` | `https://api.mistral.ai` | upstream base URL |
| `MISTRAL_API_KEY` | empty | key required for Demeter and `readyz` |
| `MISTRAL_REQUEST_TIMEOUT_SECONDS` | `480` | timeout for non-audio Mistral calls |
| `MISTRAL_AUDIO_TRANSCRIPTION_TIMEOUT_SECONDS` | `1200` | dedicated timeout for `POST /v1/audio/transcriptions` |
| `BOOTSTRAP_ADMIN_EMAIL` | empty | admin email created on first boot |
| `BOOTSTRAP_ADMIN_PASSWORD` | empty | admin password created on first boot |
| `BOOTSTRAP_ORG_NAME` | `Default Organization` | bootstrap org name |

## Local launch

```bash
cp .env.example .env
./scripts/run-local-backend.sh
```

The script:

- loads `.env` when it exists without overriding already-set environment variables,
- forces defaults for `APP_ENV`, `PORT`, and `SQLITE_PATH`,
- runs `go run ./cmd/server`.

## Docker image

### Build

```bash
docker build -t transcode-backend:local .
```

### Run

```bash
docker run --rm \
  -p 8080:8080 \
  -v "$(pwd)/data:/data" \
  -e APP_ENV=production \
  -e JWT_SECRET=change-me \
  -e MISTRAL_API_KEY=change-me \
  transcode-backend:local
```

Image characteristics:

- built with `CGO_ENABLED=0`,
- binary located at `/server`,
- runtime `gcr.io/distroless/base-debian12:nonroot`,
- `VOLUME ["/data"]`,
- `EXPOSE 8080`.

## Docker Compose

The repo also provides:

- `compose.yml` for a prod-like local deployment using the final image,
- `compose.dev.yml` for development with `go run` inside `golang:1.25.7`.

Prod-like launch:

```bash
docker compose up --build
```

Development launch:

```bash
docker compose -f compose.dev.yml up
```

Compose specifics:

- all runtime variables are declared inline under `environment:`,
- local Compose defaults `APP_PUBLIC_URL` and `ADMIN_PUBLIC_URL` to the front and admin localhost ports so password reset links keep working in dev,
- `compose.yml` persists SQLite in the named volume `backend-data`,
- `compose.dev.yml` uses separate named volumes for SQLite and Go build caches,
- real secrets should still come from environment substitution, not committed literal values,
- there is no Docker `healthcheck` because the distroless runtime image does not ship an HTTP probe tool.

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
