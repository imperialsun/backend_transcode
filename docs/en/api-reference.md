# API Reference

## General conventions

- Default local base URL: `http://localhost:8080`
- Main prefix: `/api/v1`
- Standard JSON error:

```json
{
  "error": "message"
}
```

- Protected routes accept the access token from a cookie or `Authorization: Bearer`.
- Mutating admin routes also require `X-Admin-CSRF`.

## App login example

```json
POST /api/v1/auth/login
{
  "email": "admin@demeter.local",
  "password": "ChangeMe123!"
}
```

Response:

```json
{
  "user": {
    "id": "uuid",
    "email": "admin@demeter.local",
    "status": "active"
  },
  "organization": {
    "id": "uuid",
    "name": "Demeter Sante",
    "code": "demeter-sante",
    "status": "active"
  },
  "globalRoles": ["super_admin", "user"],
  "orgRoles": ["org_admin", "org_member"],
  "permissions": ["feature.settings", "feature.admin"],
  "runtimeMode": "backend"
}
```

## Health

| Method | Route | Auth | Notes |
| --- | --- | --- | --- |
| `GET` | `/healthz` | none | returns `{"status":"ok"}` |
| `GET` | `/readyz` | none | returns `{"status":"ready"}` when DB + Mistral are ready |

## App auth

| Method | Route | Auth | Permissions | Notes |
| --- | --- | --- | --- | --- |
| `POST` | `/api/v1/auth/login` | none | none | app login, rate limited to 10/min/IP |
| `POST` | `/api/v1/auth/refresh` | app refresh cookie | none | rotates the refresh token |
| `POST` | `/api/v1/auth/logout` | optional app refresh cookie | none | revokes the refresh session if found |
| `PUT` | `/api/v1/auth/me/password` | app session | none | changes the current password, revokes refresh sessions and outstanding reset tokens |
| `GET` | `/api/v1/auth/me` | app session | none | returns the current user context |

## Admin auth

| Method | Route | Auth | Permissions | Notes |
| --- | --- | --- | --- | --- |
| `POST` | `/api/v1/admin/auth/login` | none | none | admin login, rate limited to 10/min/IP, returns `csrfToken` |
| `POST` | `/api/v1/admin/auth/refresh` | admin refresh cookie | none | rotates the admin refresh token and renews `csrfToken` |
| `POST` | `/api/v1/admin/auth/logout` | optional admin refresh cookie | none | revokes the admin refresh session |
| `GET` | `/api/v1/admin/auth/me` | admin session | effective admin scope | returns the current admin context |

## Settings

| Method | Route | Auth | Permissions | Notes |
| --- | --- | --- | --- | --- |
| `GET` | `/api/v1/settings` | app session | `feature.settings` | returns `settings: {}` when no record exists |
| `PUT` | `/api/v1/settings` | app session | `feature.settings` | accepts any valid JSON |
| `POST` | `/api/v1/settings/reset` | app session | `feature.settings` | replaces the document with `{}` |

Example `PUT /api/v1/settings`:

```json
{
  "schemaVersion": 1,
  "settings": {
    "cloudProvider": "demeter_sante"
  }
}
```

## Activity

| Method | Route | Auth | Permissions | Notes |
| --- | --- | --- | --- | --- |
| `POST` | `/api/v1/activity/events` | app session | none | ingests a batch of events with `eventId` idempotency |
| `GET` | `/api/v1/admin/activity/summary` | admin session | `feature.admin` + admin scope | global summary for `super_admin`, current org summary otherwise |
| `GET` | `/api/v1/admin/activity/organizations/:id/summary` | admin session | `feature.admin` + admin scope | forced summary for a specific organization |
| `GET` | `/api/v1/admin/users/:id/activity/summary` | admin session | `feature.admin` + admin scope | user summary for the selected account |

Example ingest payload:

```json
{
  "events": [
    {
      "eventId": "evt-001",
      "eventKind": "transcription",
      "sourceMode": "cloud_backend",
      "provider": "demeter_sante",
      "status": "success",
      "occurredAt": "2026-03-09T10:00:00Z",
      "meta": {
        "durationSeconds": 42
      }
    }
  ]
}
```

## `Demeter Sante` provider

| Method | Route | Auth | Permissions | Notes |
| --- | --- | --- | --- | --- |
| `GET` | `/api/v1/providers/demeter-sante/models` | app session | `provider.cloud.demeter_sante` or `provider.llm.demeter_sante` | proxies `GET /v1/models` |
| `POST` | `/api/v1/providers/demeter-sante/audio/transcriptions` | app session | `feature.cloudupload` + `provider.cloud.demeter_sante` | requires `multipart/form-data` |
| `POST` | `/api/v1/providers/demeter-sante/audio/transcriptions/backend` | app session | `feature.cloudupload` + `provider.cloud.demeter_sante` | slice-only `slice-v1` transport in 5 MiB chunks; the backend reconstructs the source audio server-side and exposes the result through polling |
| `POST` | `/api/v1/providers/demeter-sante/chat/completions` | app session | `feature.llmapi` + `provider.llm.demeter_sante` | proxies JSON to Mistral |

## Admin

All routes below require:

- a valid admin session,
- the `feature.admin` permission,
- `super_admin` or `org_admin` scope,
- an `X-Admin-CSRF` header on mutating routes.

| Method | Route | Notes |
| --- | --- | --- |
| `GET` | `/api/v1/admin/organizations` | all orgs for `super_admin`, current org otherwise |
| `POST` | `/api/v1/admin/organizations` | creates an org, reserved to `super_admin` |
| `PATCH` | `/api/v1/admin/organizations/:id` | updates an org, reserved to `super_admin` |
| `GET` | `/api/v1/admin/organizations/:id/users` | lists users in an org |
| `POST` | `/api/v1/admin/organizations/:id/users` | creates a user and grants `user` + `org_member` |
| `POST` | `/api/v1/admin/organizations/:id/users/bulk` | creates multiple users from an email list, grants `user` + `org_member`, and emails each account a temporary password; partial success is returned per email |
| `PATCH` | `/api/v1/admin/users/:id` | updates email, status, org (org only for `super_admin`) |
| `DELETE` | `/api/v1/admin/users/:id` | permanently deletes the user; rejects self-deletion and deleting the last required admin |
| `DELETE` | `/api/v1/admin/users/:id/activity` | purges all activity events for the user while keeping the account |
| `PUT` | `/api/v1/admin/users/:id/password` | changes the password and revokes refresh sessions |
| `PUT` | `/api/v1/admin/users/:id/global-roles` | reserved to `super_admin` |
| `PUT` | `/api/v1/admin/users/:id/org-roles` | updates organization roles |
| `PUT` | `/api/v1/admin/users/:id/entitlements` | updates `allow` / `deny` overrides |
| `GET` | `/api/v1/admin/users/:id/access` | returns the full access context for a user |
| `GET` | `/api/v1/admin/catalog/roles` | returns global and organization role catalogs |
| `GET` | `/api/v1/admin/catalog/permissions` | returns the seeded permission catalog |

Example overrides payload:

```json
{
  "overrides": [
    {
      "permissionCode": "feature.telemetry",
      "effect": "deny"
    }
  ]
}
```

## Notable status codes

- `400`: invalid payload, invalid JSON, invalid activity date, wrong content type.
- `401`: missing or invalid access token, missing or invalid refresh token.
- `403`: inactive user or org, missing permission, insufficient admin scope, forbidden admin origin, invalid CSRF token.
- `404`: missing organization or user.
- `429`: too many login attempts.
- `502`: network failure while contacting Mistral.
- `503`: Mistral not configured or database not ready for `readyz`.

## Links

- Auth and RBAC: [`authentication-rbac.md`](authentication-rbac.md)
- Settings: [`settings-reference.md`](settings-reference.md)
- Demeter provider: [`provider-demeter-sante.md`](provider-demeter-sante.md)
