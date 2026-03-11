# Base de donnees

## Vue d ensemble

Le backend utilise SQLite via `modernc.org/sqlite` avec:

- `PRAGMA foreign_keys = ON`
- `PRAGMA journal_mode = WAL`
- `PRAGMA synchronous = NORMAL`
- `SetMaxOpenConns(1)` et `SetMaxIdleConns(1)`

La source du schema est `internal/store/store.go`.

## Schema logique

### Tables coeur

| Table | Role |
| --- | --- |
| `organizations` | organisations et statut |
| `users` | utilisateurs rattaches a une organisation |
| `user_settings` | reglages applicatifs par utilisateur |
| `refresh_sessions` | sessions refresh app et admin |
| `audit_logs` | journal d audit admin |
| `activity_usage_events` | evenements d usage transcription / report |

### Tables RBAC

| Table | Role |
| --- | --- |
| `global_roles` | roles globaux |
| `organization_roles` | roles organisation |
| `permissions` | catalogue de permissions |
| `user_global_roles` | affectation user -> roles globaux |
| `user_organization_roles` | affectation user -> roles org |
| `global_role_permissions` | mapping role global -> permissions |
| `organization_role_permissions` | mapping role org -> permissions |
| `user_permission_overrides` | overrides `allow` / `deny` |

## Relations principales

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

## Colonnes importantes

### `organizations`

- `id` UUID texte
- `name`
- `code` unique, normalise en lowercase avec `-`
- `status` (`active` ou `inactive`)
- `created_at`, `updated_at`

### `users`

- `id`
- `organization_id`
- `email` unique et lowercased
- `password_hash`
- `status`
- `created_at`, `updated_at`

### `refresh_sessions`

- `id`
- `user_id`, `organization_id`
- `session_type` (`app` ou `admin`)
- `refresh_hash`
- `expires_at`, `revoked_at`, `created_at`

### `user_settings`

- `user_id` cle primaire
- `organization_id`
- `settings_json`
- `version`
- `schema_version`
- `updated_at`

### `activity_usage_events`

- `event_id` cle primaire pour l idempotence
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

## Index explicites

- `idx_users_org`
- `idx_refresh_user`
- `idx_activity_org_day`
- `idx_activity_user_day`
- `idx_activity_kind_org_day`
- `idx_activity_provider_org_day`

## Initialisation au boot

1. ouverture SQLite,
2. migration schema,
3. ajout de la colonne `session_type` si besoin,
4. seed du catalogue permissions / roles / mappings,
5. bootstrap admin optionnel sur base vide.

## Comportement metier stocke en base

### Bootstrap

- cree une organisation `active`,
- cree un user `active`,
- lui assigne `super_admin`, `user`, `org_admin`, `org_member`.

### Settings

- `SaveUserSettings` fait un upsert,
- `version` s incremente a chaque ecriture,
- `ResetUserSettings` ecrit `{}` avec `schema_version = 1`.

### Sessions refresh

- le backend ne stocke jamais le refresh token brut,
- seul le hash SHA-256 du token combine est persiste,
- les mutations admin et certains changements user revoquent les refresh sessions.

### Activity

- insertion en transaction,
- comptage des doublons si `event_id` existe deja,
- agregats journaliers, par user, par mode, et par provider.

## Points d attention

- les changements de roles / overrides sont visibles immediatement car les permissions sont rechargees a chaque requete,
- un changement de mot de passe revoque les refresh sessions mais ne supprime pas l access token courant avant expiration,
- les fichiers `*.sqlite`, `*.sqlite-wal`, `*.sqlite-shm` ne doivent pas etre commits.

## Liens

- Architecture: [`architecture.md`](architecture.md)
- Auth et RBAC: [`authentication-rbac.md`](authentication-rbac.md)
- Activite: [`activity-observability.md`](activity-observability.md)
