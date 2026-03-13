# Authentification et RBAC

## Types de session

| Session | Access cookie | Refresh cookie | Path access | Path refresh | Audience JWT | Runtime mode |
| --- | --- | --- | --- | --- | --- | --- |
| App | `tc_app_access` | `tc_app_refresh` | `/api/v1` | `/api/v1/auth` | `app` | `backend` |
| Admin | `tc_admin_access` | `tc_admin_refresh` | `/api/v1/admin` | `/api/v1/admin/auth` | `admin` | `admin` |

Toutes les cookies de session sont:

- `HttpOnly`,
- `SameSite=Strict`,
- `Secure` selon `COOKIE_SECURE`.

## Login, refresh, logout

### Login

- `POST /api/v1/auth/login` et `POST /api/v1/admin/auth/login` sont limites a 10 tentatives par minute et par IP.
- Le backend charge le user par email, verifie le hash Argon2id, refuse les users `inactive`, puis genere:
  - un access token JWT signe en `HS256`,
  - un refresh token aleatoire,
  - une ligne en base `refresh_sessions`.
- Le login admin ajoute un `csrfToken` a la reponse et ecrit un audit log `admin.login`.

### Refresh

- Le refresh lit uniquement la cookie refresh dediee.
- La session est refusee si elle est revokee, expiree, du mauvais type, ou si le hash ne correspond pas.
- Le refresh rotate le token et met a jour le hash stocke.

### Logout

- Le logout revoque la session refresh active si elle existe.
- Les cookies access et refresh sont aussitot expirees cote client.

### Reset mot de passe par email

- `POST /api/v1/auth/forgot-password` et `POST /api/v1/admin/auth/forgot-password` acceptent `{ email }` et repondent `204` pour toute requete syntaxiquement valide.
- Les demandes publiques ne revelent pas si l email existe, si le user est inactif, ou si l envoi SMTP a echoue.
- `POST /api/v1/auth/reset-password` et `POST /api/v1/admin/auth/reset-password` acceptent `{ token, password }`.
- Les tokens de reset sont:
  - one-shot,
  - stockes hashes en base dans `password_reset_tokens`,
  - limites dans le temps via `PASSWORD_RESET_TTL_MINUTES`,
  - scopes par namespace `app` ou `admin`.
- Un reset reussi:
  - met a jour le hash Argon2id du user,
  - revoque toutes ses refresh sessions,
  - invalide tous ses autres tokens de reset en attente.
- L admin dispose aussi de `POST /api/v1/admin/users/:id/password-reset-email` pour envoyer un lien de reset app a un utilisateur de son scope.
- L admin dispose aussi de `DELETE /api/v1/admin/users/:id` pour supprimer definitivement un utilisateur de son scope, avec protections contre l auto-suppression et la suppression du dernier admin requis.

## Configuration email et liens publics

Variables backend utilisees par le reset email:

- `SMTP_HOST`
- `SMTP_PORT`
- `SMTP_USERNAME`
- `SMTP_PASSWORD`
- `SMTP_FROM_EMAIL`
- `SMTP_FROM_NAME`
- `APP_PUBLIC_URL`
- `ADMIN_PUBLIC_URL`
- `PASSWORD_RESET_TTL_MINUTES`

## Chargement des access tokens

Le middleware accepte l access token depuis:

- la cookie dediee,
- ou un header `Authorization: Bearer <token>`.

Le refresh token n est jamais lu depuis `Authorization`.

## Protections admin supplementaires

### Verification d origine

Sur toutes les routes `/api/v1/admin*`, si un header `Origin` est present, il doit appartenir a `ADMIN_CORS_ORIGINS`.

### CSRF

Toutes les routes admin mutantes (`POST`, `PUT`, `PATCH`, `DELETE`) exigent:

- un `csrfToken` present dans les claims admin,
- un header `X-Admin-CSRF` strictement egal a ce token.

Les routes `GET`, `HEAD`, `OPTIONS` ne demandent pas ce header.

### Scope admin

L acces admin complet exige:

- la permission `feature.admin`,
- et un role `super_admin` ou `org_admin`.

## Catalogue RBAC seed au demarrage

### Roles globaux

- `super_admin`
- `user`

### Roles organisation

- `org_admin`
- `org_member`

### Permissions feature

- `feature.localupload`
- `feature.cloudupload`
- `feature.llmlocal`
- `feature.llmapi`
- `feature.settings`
- `feature.telemetry`
- `feature.admin`

### Permissions provider cloud

- `provider.cloud.whisper`
- `provider.cloud.mistral`
- `provider.cloud.demeter_sante`

### Permissions provider llm

- `provider.llm.huggingface`
- `provider.llm.mistral`
- `provider.llm.demeter_sante`

## Resolution des permissions effectives

Le backend calcule les permissions d un utilisateur comme suit:

1. union des permissions des roles globaux,
2. union des permissions des roles organisation,
3. application des overrides utilisateur:
   - `deny` retire une permission,
   - `allow` ajoute une permission.

Cette resolution est rechargee a chaque requete protegee.

## Scope organisation

- `super_admin` voit et modifie toutes les organisations.
- `org_admin` reste limite a `claims.OrgID`.
- Les routes app utilisent aussi `claims.OrgID` pour l ingestion activity et les settings.

## Liens

- Reference API: [`api-reference.md`](api-reference.md)
- Securite: [`security-privacy.md`](security-privacy.md)
- Base de donnees: [`database.md`](database.md)
