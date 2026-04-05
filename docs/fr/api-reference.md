# Reference API

## Conventions generales

- Base locale par defaut: `http://localhost:8080`
- Prefixe principal: `/api/v1`
- Erreur JSON standard:

```json
{
  "error": "message"
}
```

- Les routes protegees acceptent l access token via cookie ou `Authorization: Bearer`.
- Les routes admin mutantes exigent en plus `X-Admin-CSRF`.

## Exemple de login app

```json
POST /api/v1/auth/login
{
  "email": "admin@demeter.local",
  "password": "ChangeMe123!"
}
```

Reponse:

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

| Methode | Route | Auth | Notes |
| --- | --- | --- | --- |
| `GET` | `/healthz` | aucune | renvoie `{"status":"ok"}` |
| `GET` | `/readyz` | aucune | renvoie `{"status":"ready"}` si DB + Mistral OK |

## Auth app

| Methode | Route | Auth | Permissions | Notes |
| --- | --- | --- | --- | --- |
| `POST` | `/api/v1/auth/login` | aucune | aucune | login app, rate limit 10/min/IP |
| `POST` | `/api/v1/auth/refresh` | cookie refresh app | aucune | rotate refresh token |
| `POST` | `/api/v1/auth/logout` | cookie refresh app optionnelle | aucune | revoque la session refresh si trouvee |
| `PUT` | `/api/v1/auth/me/password` | session app | aucune | change le mot de passe courant, revoque les refresh sessions et les tokens de reset encore actifs |
| `GET` | `/api/v1/auth/me` | session app | aucune | renvoie le contexte utilisateur courant |

## Auth admin

| Methode | Route | Auth | Permissions | Notes |
| --- | --- | --- | --- | --- |
| `POST` | `/api/v1/admin/auth/login` | aucune | aucune | login admin, rate limit 10/min/IP, renvoie `csrfToken` |
| `POST` | `/api/v1/admin/auth/refresh` | cookie refresh admin | aucune | rotate refresh token admin et renouvelle `csrfToken` |
| `POST` | `/api/v1/admin/auth/logout` | cookie refresh admin optionnelle | aucune | revoque la session refresh admin |
| `GET` | `/api/v1/admin/auth/me` | session admin | scope admin effectif | renvoie le contexte admin courant |

## Settings

| Methode | Route | Auth | Permissions | Notes |
| --- | --- | --- | --- | --- |
| `GET` | `/api/v1/settings` | session app | `feature.settings` | si aucun enregistrement, renvoie `{}` dans `settings` |
| `PUT` | `/api/v1/settings` | session app | `feature.settings` | accepte tout JSON valide |
| `POST` | `/api/v1/settings/reset` | session app | `feature.settings` | remplace le document par `{}` |

Exemple `PUT /api/v1/settings`:

```json
{
  "schemaVersion": 1,
  "settings": {
    "cloudProvider": "demeter_sante"
  }
}
```

## Activity

| Methode | Route | Auth | Permissions | Notes |
| --- | --- | --- | --- | --- |
| `POST` | `/api/v1/activity/events` | session app | aucune | ingere un batch d evenements avec idempotence sur `eventId` |
| `GET` | `/api/v1/admin/activity/summary` | session admin | `feature.admin` + scope admin | resume global pour `super_admin`, sinon resume org courant |
| `GET` | `/api/v1/admin/activity/organizations/:id/summary` | session admin | `feature.admin` + scope admin | resume force pour une organisation donnee |
| `GET` | `/api/v1/admin/users/:id/activity/summary` | session admin | `feature.admin` + scope admin | resume d activite pour le compte selectionne |

Exemple ingestion:

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

## Provider `Demeter Sante`

| Methode | Route | Auth | Permissions | Notes |
| --- | --- | --- | --- | --- |
| `GET` | `/api/v1/providers/demeter-sante/models` | session app | `provider.cloud.demeter_sante` ou `provider.llm.demeter_sante` | proxy `GET /v1/models` |
| `POST` | `/api/v1/providers/demeter-sante/audio/transcriptions/backend` | session app | `feature.cloudupload` + `provider.cloud.demeter_sante` | transport `slice-v1` reserve aux morceaux de 5 Mo; le backend reconstitue l audio cote serveur puis expose le resultat via polling |
| `POST` | `/api/v1/providers/demeter-sante/chat/completions` | session app | `feature.llmapi` + `provider.llm.demeter_sante` | proxy JSON vers Mistral |

## Admin

Toutes les routes ci-dessous exigent:

- session admin valide,
- permission `feature.admin`,
- scope `super_admin` ou `org_admin`,
- header `X-Admin-CSRF` sur les routes mutantes.

| Methode | Route | Notes |
| --- | --- | --- |
| `GET` | `/api/v1/admin/organizations` | toutes orgs pour `super_admin`, sinon org courante |
| `POST` | `/api/v1/admin/organizations` | creation org, reserve a `super_admin` |
| `PATCH` | `/api/v1/admin/organizations/:id` | update org, reserve a `super_admin` |
| `GET` | `/api/v1/admin/organizations/:id/users` | liste les users d une org |
| `POST` | `/api/v1/admin/organizations/:id/users` | cree un user, lui attribue `user` + `org_member` |
| `POST` | `/api/v1/admin/organizations/:id/users/bulk` | cree plusieurs users depuis une liste d emails, leur attribue `user` + `org_member`, et envoie a chacun un mot de passe temporaire par email; les resultats partiels sont retournes par adresse |
| `PATCH` | `/api/v1/admin/users/:id` | met a jour email, statut, org (org seulement pour `super_admin`) |
| `DELETE` | `/api/v1/admin/users/:id` | supprime definitivement l utilisateur; refuse l auto-suppression et la suppression du dernier admin requis |
| `DELETE` | `/api/v1/admin/users/:id/activity` | purge tous les evenements d activite du user tout en conservant le compte |
| `PUT` | `/api/v1/admin/users/:id/password` | change le mot de passe et revoque les refresh sessions |
| `PUT` | `/api/v1/admin/users/:id/global-roles` | reserve a `super_admin` |
| `PUT` | `/api/v1/admin/users/:id/org-roles` | maj roles organisation |
| `PUT` | `/api/v1/admin/users/:id/entitlements` | maj overrides `allow` / `deny` |
| `GET` | `/api/v1/admin/users/:id/access` | contexte complet d acces utilisateur |
| `GET` | `/api/v1/admin/catalog/roles` | catalogue roles globaux et organisation |
| `GET` | `/api/v1/admin/catalog/permissions` | catalogue permissions seedees |

Exemple overrides:

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

## Codes de statut notables

- `400`: payload invalide, JSON invalide, date activity invalide, mauvais content-type.
- `401`: access token manquant ou invalide, refresh absent ou invalide.
- `403`: user ou org inactive, permission manquante, scope admin insuffisant, origine admin interdite, CSRF invalide.
- `404`: organisation ou utilisateur introuvable.
- `429`: trop de tentatives de login.
- `502`: echec reseau vers Mistral.
- `503`: Mistral non configure ou base non prete pour `readyz`.

## Liens

- Auth et RBAC: [`authentication-rbac.md`](authentication-rbac.md)
- Reglages: [`settings-reference.md`](settings-reference.md)
- Provider Demeter: [`provider-demeter-sante.md`](provider-demeter-sante.md)
