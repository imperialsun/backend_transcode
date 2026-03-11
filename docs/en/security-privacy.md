# Security and Privacy

## Security model

This repo implements full server-side authentication:

- Argon2id password hashing,
- HMAC-signed access JWTs,
- random refresh tokens hashed in the database,
- live user / org / role / permission verification on every protected request.

## Sessions and cookies

- `HttpOnly` cookies,
- `SameSite=Strict`,
- `Secure` controlled by `COOKIE_SECURE`,
- strict separation between app and admin cookies,
- separate JWT audiences for `app` and `admin` sessions.

## Extra admin protections

- origin filtering on `/api/v1/admin*` through `ADMIN_CORS_ORIGINS`,
- mandatory CSRF on mutating admin routes through `X-Admin-CSRF`,
- admin scope limited to `super_admin` or `org_admin` plus `feature.admin`.

## Secret handling

- `JWT_SECRET` must never keep its default value outside development.
- `MISTRAL_API_KEY` must remain server-side.
- `.env` files and local SQLite databases must never be committed.
- Bootstrap credentials are only meant for the first startup of an empty database.

## Data confidentiality

### Local backend routes

- settings, sessions, RBAC, and activity are stored locally in SQLite,
- no external provider receives data through those routes alone.

### Demeter relay

- audio content and prompts sent to `Demeter Sante` leave the backend toward Mistral,
- the provider key stays hidden on the server.

## Image and runtime

- final `distroless` non-root image,
- `/data` volume for SQLite,
- exposed port `8080`,
- no shell inside the runtime image.

## Continuous verification

Current workflows:

- `ci.yml`
- `codeql.yml`
- `prod-smoke.yml`
- `trivy.yml`

They cover tests, lint, build, static analysis, vulnerability scanning, and HTTP smoke checks.

## Operational recommendations

- enable `COOKIE_SECURE=true` in production,
- limit `ADMIN_CORS_ORIGINS` to the real admin origins,
- protect the SQLite volume and backups,
- remove or rotate bootstrap credentials after initialization.

## Links

- Deployment: [`deployment-operations.md`](deployment-operations.md)
- Auth and RBAC: [`authentication-rbac.md`](authentication-rbac.md)
- Root policy: [`SECURITY.md`](../../SECURITY.md)
