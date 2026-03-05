# Base de donnees: schema et fonctionnement

## 1) Vue d'ensemble

Le backend utilise SQLite (driver `modernc.org/sqlite`) avec:

- `PRAGMA foreign_keys = ON`
- `PRAGMA journal_mode = WAL`
- `PRAGMA synchronous = NORMAL`
- 1 seule connexion ouverte (`SetMaxOpenConns(1)`)

La source du schema est dans:

- [`internal/store/store.go`](/home/imperialsun/Transcode/Backend/internal/store/store.go)

## 2) Schema logique

### Tables coeur

| Table | But |
| --- | --- |
| `organizations` | Organisations et statut |
| `users` | Utilisateurs (1 user appartient a 1 organisation) |
| `user_settings` | Reglages applicatifs synchronises par utilisateur |
| `refresh_sessions` | Sessions refresh token pour auth |
| `audit_logs` | Journal d'audit (table prete, usage metier a completer) |
| `activity_usage_events` | Evenements d'usage (transcriptions + rapports) |

### Tables RBAC

| Table | But |
| --- | --- |
| `global_roles` | Roles globaux (`super_admin`, `user`) |
| `organization_roles` | Roles organisation (`org_admin`, `org_member`) |
| `permissions` | Catalogue des permissions |
| `user_global_roles` | Affectation user -> roles globaux |
| `user_organization_roles` | Affectation user -> roles org |
| `global_role_permissions` | Mapping role global -> permissions |
| `organization_role_permissions` | Mapping role org -> permissions |
| `user_permission_overrides` | Overrides user (`allow` / `deny`) |

## 3) Relations principales

```text
organizations (1) ---- (N) users
users (1) ----------- (1) user_settings
users (1) ----------- (N) refresh_sessions
organizations (1) --- (N) activity_usage_events
users (1) ----------- (N) activity_usage_events

users (N) ---- (N) global_roles           via user_global_roles
users (N) ---- (N) organization_roles     via user_organization_roles
global_roles (N) ---- (N) permissions     via global_role_permissions
organization_roles (N) ---- (N) permissions via organization_role_permissions
users (N) ---- (N) permissions            via user_permission_overrides
```

## 4) Colonnes importantes par table

### `organizations`

- `id` (PK, TEXT UUID)
- `name` (TEXT)
- `code` (TEXT UNIQUE)
- `status` (`active` / `inactive`)
- `created_at`, `updated_at`

### `users`

- `id` (PK, TEXT UUID)
- `organization_id` (FK -> `organizations.id`)
- `email` (UNIQUE)
- `password_hash` (argon2id)
- `status` (`active` / `inactive`)
- `created_at`, `updated_at`

### `permissions`

- `code` est la cle metier stable (ex: `feature.settings`, `provider.cloud.demeter_sante`)
- `scope` categorise la permission (`feature`, `provider_cloud`, `provider_llm`)

### `user_settings`

- `user_id` (PK)
- `organization_id` (FK)
- `settings_json` (blob JSON)
- `version` (incremente a chaque sauvegarde)
- `schema_version`
- `updated_at`

### `refresh_sessions`

- `id` (session id)
- `user_id`, `organization_id`
- `refresh_hash` (hash du refresh token)
- `expires_at`, `revoked_at`, `created_at`

### `activity_usage_events`

- `event_id` (PK, idempotence cote client)
- `organization_id` (FK)
- `user_id` (FK)
- `event_kind` (`transcription` | `report`)
- `source_mode` (`local` | `cloud_direct` | `cloud_backend`)
- `provider` (ex: `local_upload`, `mic`, `whisper`, `mistral`, `demeter_sante`, `local`, `huggingface`)
- `status` (`success` | `error`)
- `occurred_at`, `day` (YYYY-MM-DD), `meta_json`, `created_at`

## 5) Index

Indexes explicites:

- `idx_users_org` sur `users(organization_id)`
- `idx_refresh_user` sur `refresh_sessions(user_id)`
- `idx_activity_org_day` sur `activity_usage_events(organization_id, day)`
- `idx_activity_user_day` sur `activity_usage_events(user_id, day)`
- `idx_activity_kind_org_day` sur `activity_usage_events(event_kind, organization_id, day)`
- `idx_activity_provider_org_day` sur `activity_usage_events(provider, organization_id, day)`

## 6) Initialisation au demarrage

Sequence au boot:

1. Ouverture SQLite + PRAGMA
2. Migration schema (`Migrate`)
3. Seed catalogue (`SeedBaseCatalog`):
   - permissions feature/provider
   - roles globaux et org
   - mapping roles -> permissions
4. Bootstrap admin optionnel (`EnsureBootstrap`) si:
   - la table `users` est vide
   - `BOOTSTRAP_ADMIN_EMAIL` et `BOOTSTRAP_ADMIN_PASSWORD` sont fournis

## 7) Fonctionnement metier

### 7.1 Auth et sessions

- `POST /api/v1/auth/login`
  - charge user par email
  - verifie hash password
  - refuse user `inactive`
  - emet access token JWT + refresh token
  - persiste session refresh en base (`refresh_sessions`)
- `POST /api/v1/auth/refresh`
  - verifie session refresh + expiration + hash
  - rotate le token refresh
- `POST /api/v1/auth/logout`
  - revoque la session refresh active

### 7.2 RBAC (droits effectifs)

Les permissions effectives d'un user sont calculees par:

1. Union des permissions venant des roles globaux
2. Union des permissions venant des roles organisation
3. Application des overrides utilisateur:
   - `deny` retire une permission
   - `allow` ajoute une permission

Implementation:

- `ResolveEffectivePermissions` dans `store.go`

### 7.3 Reglages utilisateur

- Lecture (`GET /settings`):
  - si aucune ligne `user_settings`, le backend renvoie `settings: {}`
  - le front applique ensuite ses defaults applicatifs
- Ecriture (`PUT /settings`):
  - upsert ligne `user_settings`
  - incremente `version`
- Reset (`POST /settings/reset`):
  - force `settings_json = {}`

Note: a la creation d'un user, la ligne `user_settings` n'est pas creee automatiquement.

### 7.4 Creation user par admin

Lors de `POST /api/v1/admin/organizations/:id/users`:

- insertion dans `users`
- affectation role global `user`
- affectation role org `org_member`

### 7.5 Isolation organisation

Regle de base:

- un user appartient a une seule organisation (`users.organization_id`)
- les routes admin appliquent un scope:
  - `super_admin`: toutes orgs
  - `org_admin`: uniquement son `organization_id`

### 7.6 Suivi d'activite (transcriptions/rapports)

- Ingestion: `POST /api/v1/activity/events` (route authentifiee)
- Consultation admin: `GET /api/v1/admin/activity/organizations/:id/summary`
- Les evenements sont traites en batch avec idempotence sur `event_id`.
- Les aggregats exposent:
  - totaux (`transcriptions`, `reports`)
  - repartition par jour
  - repartition par utilisateur
  - breakdown par `source_mode` et `provider`
- Les evenements locaux/directs sont remontes par la webapp via une file locale, puis flush vers le backend a la connexion/reseau.

## 8) Catalogue de permissions seedees

Permissions `feature.*`:

- `feature.localupload`
- `feature.cloudupload`
- `feature.llmlocal`
- `feature.llmapi`
- `feature.settings`
- `feature.telemetry`
- `feature.admin`

Permissions providers cloud:

- `provider.cloud.gradio`
- `provider.cloud.whisper`
- `provider.cloud.mistral`
- `provider.cloud.demeter_sante`

Permissions providers llm:

- `provider.llm.huggingface`
- `provider.llm.mistral`
- `provider.llm.demeter_sante`

## 9) Points d'attention

- `audit_logs` existe dans le schema mais n'est pas encore alimente par les handlers.
- En local, la base par defaut est `./backend.sqlite`.
- Les fichiers `*.sqlite`, `*.sqlite-wal`, `*.sqlite-shm` sont ignores par git.
