# Architecture

## Vue d ensemble

Le backend est un service `Go + Fiber + SQLite` organise en couches:

- `cmd/server/main.go`: bootstrap global, middleware, routes, shutdown.
- `internal/api/*`: handlers HTTP, middleware auth, validation, logging requetes, managers de queues async.
- `internal/store/*`: acces SQLite, migrations, seed RBAC, persistance settings, activity, audit, operations et observabilite.
- `internal/auth/*`: hash mot de passe, JWT, refresh tokens, CSRF.
- `internal/mistral/*`: client HTTP pour les routes `Demeter Sante`.
- `internal/rbac/*`: helpers de verification roles et permissions.

## Sequence de demarrage

1. `config.Load()` lit l environnement et applique les defaults.
2. `store.Open()` cree le dossier SQLite si besoin, active `foreign_keys`, `WAL`, `synchronous = NORMAL`, puis lance migration et seed.
3. Le bootstrap admin est applique seulement si la base est vide et que les variables bootstrap sont presentes.
4. `api.App` assemble `Config`, `Store`, et `MistralClient`.
5. Fiber enregistre:
   - request logger,
   - recover middleware,
   - filtre d origine admin,
   - CORS,
   - routes health, auth, settings, activity, performance, support, provider, mobile, admin et WebSocket admin.
6. Le process ecoute `PORT` puis attend `SIGINT` ou `SIGTERM` pour un shutdown grace sur 10 secondes.

## Middleware request path

Ordre des traitements:

1. journalisation requete / reponse,
2. protection panic,
3. blocage des origines non autorisees sur `/api/v1/admin*`,
4. CORS app + admin,
5. middleware auth selon le groupe,
6. middleware permissions / scope / CSRF,
7. handler metier.

## Groupes de routes

| Groupe | Prefixe | Role |
| --- | --- | --- |
| Health | `/healthz`, `/readyz` | liveness et readiness |
| App auth | `/api/v1/auth/*` | login app, refresh, logout, `me` |
| Admin auth | `/api/v1/admin/auth/*` | login admin, refresh, logout, `me` |
| Settings | `/api/v1/settings*` | lecture et ecriture des reglages utilisateur |
| Activity | `/api/v1/activity/*`, `/api/v1/admin/activity/*` | ingestion usage + agregats admin |
| Performance | `/api/v1/performance/*`, `/api/v1/admin/performance*` | ingestion timings frontend/backend + syntheses super-admin |
| Demeter | `/api/v1/providers/demeter-sante/*` | operations transcription audio et rapports via Mistral |
| Mobile | `/api/v1/mobile/*` | API simplifiee rapport/email et audio-report |
| Support | `/api/v1/support/*` | rapports d erreur frontend |
| Admin | `/api/v1/admin/*` | organisations, utilisateurs, roles, permissions, queues, erreurs |
| WebSockets admin | `/api/v1/admin/providers/demeter-sante/*/ws` | snapshots live des queues et commandes de settings |

## Frontieres techniques

| Zone | Dependances principales | Contrat |
| --- | --- | --- |
| API | Fiber, `internal/store`, `internal/auth` | parse JSON, controle auth, renvoie JSON ou codes HTTP |
| Store | `database/sql`, `modernc.org/sqlite` | source de verite pour users, roles, settings, sessions, activity, queues |
| Auth | JWT HMAC, Argon2id, random tokens | pas de session server-side autre que refresh en base |
| Client Mistral | `net/http` | appelle les endpoints upstream audio transcription et chat-completions pour les operations gerees backend |

## Multi-tenant et claims live

- Chaque utilisateur appartient a une seule organisation.
- Les claims du JWT ne sont pas le seul controle. A chaque requete protegee, le middleware recharge user, organisation, roles, et permissions depuis la base.
- Un user ou une org `inactive` est refuse meme si le JWT access n a pas expire.

## Persistance

- SQLite unique avec `SetMaxOpenConns(4)`, `SetMaxIdleConns(4)` et `busy_timeout=5000 ms`.
- `refresh_sessions` stocke seulement des hashes de refresh token.
- `user_settings` stocke un blob JSON opaque pour le frontend.
- `activity_usage_events` stocke les evenements idempotents par `event_id`.
- `performance_events` et `backend_error_events` stockent la telemetrie operationnelle visible dans l admin panel.
- `demeter_audio_transcription_operations`, `demeter_report_operations` et `mobile_operations` stockent l etat des jobs asynchrones et les reponses pollables.
- `audit_logs` capture les connexions admin et les mutations admin.

## Liens

- Auth et RBAC: [`authentication-rbac.md`](authentication-rbac.md)
- Base de donnees: [`database.md`](database.md)
- Deploiement: [`deployment-operations.md`](deployment-operations.md)
