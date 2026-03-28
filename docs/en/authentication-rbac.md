# Authentication and RBAC

## Session types

| Session | Access cookie | Refresh cookie | Access path | Refresh path | JWT audience | Runtime mode |
| --- | --- | --- | --- | --- | --- | --- |
| App | `tc_app_access` | `tc_app_refresh` | `/api/v1` | `/api/v1/auth` | `app` | `backend` |
| Admin | `tc_admin_access` | `tc_admin_refresh` | `/api/v1/admin` | `/api/v1/admin/auth` | `admin` | `admin` |

All session cookies are:

- `HttpOnly`,
- `SameSite=Strict`,
- `Secure` depending on `COOKIE_SECURE`.

## Login, refresh, logout

### Login

- `POST /api/v1/auth/login` and `POST /api/v1/admin/auth/login` are limited to 10 attempts per minute and per IP.
- The backend loads the user by email, verifies the Argon2id hash, rejects `inactive` users, then generates:
  - an `HS256` signed access JWT,
  - a random refresh token,
  - a `refresh_sessions` row in the database.
- Admin login adds a `csrfToken` to the response and writes an `admin.login` audit log.

### Refresh

- Refresh only reads the dedicated refresh cookie.
- The session is rejected if it is revoked, expired, of the wrong type, or if the stored hash no longer matches.
- Refresh rotates the token and updates the stored hash.

### Logout

- Logout revokes the current refresh session if it exists.
- Access and refresh cookies are immediately expired on the client side.

### Password change

- `PUT /api/v1/auth/me/password` accepts `{ currentPassword, password }` on an app session.
- The backend verifies the current password, hashes the new password with Argon2id, updates the user record, revokes every refresh session for that user, and invalidates every outstanding password reset token for that user.

### Email password reset

- `POST /api/v1/auth/forgot-password` and `POST /api/v1/admin/auth/forgot-password` accept `{ email }` and return `204` for any syntactically valid request.
- Public reset requests do not reveal whether the email exists, whether the user is inactive, or whether SMTP delivery failed.
- `POST /api/v1/auth/reset-password` and `POST /api/v1/admin/auth/reset-password` accept `{ token, password }`.
- Reset tokens are:
  - one-shot,
  - stored hashed in `password_reset_tokens`,
  - time-limited through `PASSWORD_RESET_TTL_MINUTES`,
  - scoped to the `app` or `admin` namespace.
- A successful reset:
  - updates the user's Argon2id password hash,
  - revokes every refresh session for that user,
  - invalidates every other outstanding reset token for that user.
- Admins also get `POST /api/v1/admin/users/:id/password-reset-email` to send an app reset link to a user inside their scope.
- Admins also get `POST /api/v1/admin/organizations/:id/users/bulk` to create multiple users in one request, assign the default roles, generate a temporary password server-side, and email each login plus password to the new account.
- Admins also get `DELETE /api/v1/admin/users/:id` to permanently delete a user inside their scope, with protections against self-deletion and deleting the last required admin.

## Email and public URL configuration

Backend variables used by the email reset flow:

- `SMTP_HOST`
- `SMTP_PORT`
- `SMTP_USERNAME`
- `SMTP_PASSWORD`
- `SMTP_FROM_EMAIL`
- `SMTP_FROM_NAME`
- `APP_PUBLIC_URL`
- `ADMIN_PUBLIC_URL`
- `PASSWORD_RESET_TTL_MINUTES`

## Access token loading

The middleware accepts the access token from:

- the dedicated cookie,
- or an `Authorization: Bearer <token>` header.

The refresh token is never read from `Authorization`.

## Extra admin protections

### Origin enforcement

For every `/api/v1/admin*` route, if an `Origin` header is present, it must belong to `ADMIN_CORS_ORIGINS`.

### CSRF

All mutating admin routes (`POST`, `PUT`, `PATCH`, `DELETE`) require:

- a `csrfToken` present in the admin claims,
- an `X-Admin-CSRF` header strictly equal to that token.

`GET`, `HEAD`, and `OPTIONS` do not require the header.

### Admin scope

Full admin access requires:

- the `feature.admin` permission,
- and either the `super_admin` or `org_admin` role.

## RBAC catalog seeded at startup

### Global roles

- `super_admin`
- `user`

### Organization roles

- `org_admin`
- `org_member`

### Feature permissions

- `feature.localupload`
- `feature.cloudupload`
- `feature.llmlocal`
- `feature.llmapi`
- `feature.settings`
- `feature.telemetry`
- `feature.admin`

### Cloud provider permissions

- `provider.cloud.whisper`
- `provider.cloud.mistral`
- `provider.cloud.demeter_sante`

### LLM provider permissions

- `provider.llm.huggingface`
- `provider.llm.mistral`
- `provider.llm.demeter_sante`

## Effective permission resolution

The backend computes a user's permissions as:

1. union of global role permissions,
2. union of organization role permissions,
3. application of user overrides:
   - `deny` removes a permission,
   - `allow` adds a permission.

This resolution is reloaded on every protected request.

## Organization scope

- `super_admin` can view and mutate every organization.
- `org_admin` stays limited to `claims.OrgID`.
- App routes also use `claims.OrgID` for activity ingestion and settings.

## Links

- API reference: [`api-reference.md`](api-reference.md)
- Security: [`security-privacy.md`](security-privacy.md)
- Database: [`database.md`](database.md)
