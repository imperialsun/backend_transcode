# Settings Reference

## Backend responsibility

The backend is the source of truth for persistent user settings. It does not understand the frontend's detailed business schema: it stores one opaque JSON blob per user.

## Returned envelope

```json
{
  "version": 1,
  "schemaVersion": 1,
  "updatedAt": "2026-03-09T10:00:00Z",
  "settings": {}
}
```

## Fields

| Field | Type | Behavior |
| --- | --- | --- |
| `version` | integer | increments on every save or reset |
| `schemaVersion` | integer | forced to `1` if missing or `<= 0` |
| `updatedAt` | RFC3339 | generated from the server clock |
| `settings` | JSON | opaque user document |

## Route semantics

### `GET /api/v1/settings`

- requires `feature.settings`,
- reads `user_settings` by `claims.UserID`,
- if no row exists, returns a virtual envelope with `settings: {}`.

### `PUT /api/v1/settings`

- requires `feature.settings`,
- only validates that `settings` is valid JSON,
- turns an empty payload into `{}`,
- performs an upsert on `user_settings`,
- updates `organization_id`, `schema_version`, `updated_at`,
- increments `version` if the row already exists.

### `POST /api/v1/settings/reset`

- requires `feature.settings`,
- replaces the document with `{}`,
- resets `schemaVersion` to `1`.

## Frontend / backend split

The backend:

- persists the document,
- manages the version and timestamp,
- isolates data per user and organization.

The frontend:

- defines application defaults,
- owns fine-grained schema migration,
- decides which keys are written into `settings`.

## Current limits

- no server-side JSON schema validation,
- no optimistic concurrency control,
- no automatic `user_settings` row creation when a user is created.

## Links

- API reference: [`api-reference.md`](api-reference.md)
- Architecture: [`architecture.md`](architecture.md)
- Database: [`database.md`](database.md)
