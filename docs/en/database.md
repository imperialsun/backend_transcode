# Database

## Overview

The backend uses SQLite through `modernc.org/sqlite` with:

- `PRAGMA foreign_keys = ON`
- `PRAGMA journal_mode = WAL`
- `PRAGMA synchronous = NORMAL`
- `SetMaxOpenConns(4)` and `SetMaxIdleConns(4)`
- `PRAGMA busy_timeout = 5000`

The schema source of truth is `internal/store/store.go`.

## Logical schema

### Core tables

| Table | Role |
| --- | --- |
| `organizations` | organizations and their status |
| `users` | users attached to one organization |
| `user_settings` | per-user application settings |
| `refresh_sessions` | app and admin refresh sessions |
| `audit_logs` | admin audit trail |
| `activity_usage_events` | transcription / report usage events |

### RBAC tables

| Table | Role |
| --- | --- |
| `global_roles` | global roles |
| `organization_roles` | organization roles |
| `permissions` | permission catalog |
| `user_global_roles` | user -> global role assignments |
| `user_organization_roles` | user -> org role assignments |
| `global_role_permissions` | global role -> permission mapping |
| `organization_role_permissions` | org role -> permission mapping |
| `user_permission_overrides` | `allow` / `deny` overrides |

## Main relations

```text
organizations (1) ---- (N) users
users (1) ----------- (1) user_settings
users (1) ----------- (N) refresh_sessions
organizations (1) --- (N) activity_usage_events
users (1) ----------- (N) activity_usage_events

users (N) ---- (N) global_roles
users (N) ---- (N) organization_roles
roles (N) ---- (N) permissions
users (N) ---- (N) permissions via overrides
```

## Important columns

### `organizations`

- `id` text UUID
- `name`
- `code` unique, normalized to lowercase with `-`
- `status` (`active` or `inactive`)
- `created_at`, `updated_at`

### `users`

- `id`
- `organization_id`
- `email` unique and lowercased
- `password_hash`
- `status`
- `created_at`, `updated_at`

### `refresh_sessions`

- `id`
- `user_id`, `organization_id`
- `session_type` (`app` or `admin`)
- `refresh_hash`
- `expires_at`, `revoked_at`, `created_at`

### `user_settings`

- `user_id` primary key
- `organization_id`
- `settings_json`
- `version`
- `schema_version`
- `updated_at`

### `activity_usage_events`

- `event_id` primary key for idempotency
- `organization_id`, `user_id`
- `event_kind`, `source_mode`, `provider`, `status`
- `occurred_at`, `day`, `meta_json`, `created_at`

### `audit_logs`

- `actor_user_id`
- `organization_id`
- `action`
- `target_type`, `target_id`
- `payload_json`
- `created_at`

## Explicit indexes

- `idx_users_org`
- `idx_refresh_user`
- `idx_activity_org_day`
- `idx_activity_user_day`
- `idx_activity_kind_org_day`
- `idx_activity_provider_org_day`

## Boot initialization

1. open SQLite,
2. migrate the schema,
3. add the `session_type` column if needed,
4. seed permission / role / mapping catalogs,
5. optionally bootstrap the first admin on an empty database.

## Business behavior backed by the database

### Bootstrap

- creates one `active` organization,
- creates one `active` user,
- assigns `super_admin`, `user`, `org_admin`, and `org_member`.

### Settings

- `SaveUserSettings` performs an upsert,
- `version` increments on every write,
- `ResetUserSettings` writes `{}` with `schema_version = 1`.

### Refresh sessions

- the backend never stores raw refresh tokens,
- only the SHA-256 hash of the combined token is persisted,
- admin mutations and some user changes revoke refresh sessions.

### Activity

- insertion runs in a transaction,
- duplicates are counted when `event_id` already exists,
- aggregates are exposed by day, by user, by mode, and by provider.

## Points to watch

- role and override changes are visible immediately because permissions are reloaded on every request,
- a password change revokes refresh sessions but does not delete the current access token before expiry,
- `*.sqlite`, `*.sqlite-wal`, and `*.sqlite-shm` files must never be committed.

## Links

- Architecture: [`architecture.md`](architecture.md)
- Auth and RBAC: [`authentication-rbac.md`](authentication-rbac.md)
- Activity: [`activity-observability.md`](activity-observability.md)
