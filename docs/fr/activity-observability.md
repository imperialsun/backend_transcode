# Activite et observabilite

## Ingestion activity

Le backend ingere les usages applicatifs via `POST /api/v1/activity/events`.

Corps attendu:

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
      "meta": {}
    }
  ]
}
```

Reponse:

```json
{
  "accepted": 1,
  "duplicates": 0,
  "rejected": []
}
```

## Validation des evenements

| `eventKind` | `sourceMode` | Providers autorises |
| --- | --- | --- |
| `transcription` | `local` | `local_upload`, `mic` |
| `transcription` | `cloud_direct` | `whisper`, `mistral` |
| `transcription` | `cloud_backend` | `demeter_sante` |
| `report` | `local` | `local` |
| `report` | `cloud_direct` | `huggingface`, `mistral` |
| `report` | `cloud_backend` | `demeter_sante` |

Autres regles:

- `status` doit etre `success` ou `error`,
- `occurredAt` doit etre en `RFC3339` si fourni,
- `meta` doit etre un JSON valide si fourni,
- `eventId` sert d identifiant idempotent.

## Aggregation admin

Routes disponibles:

- `GET /api/v1/admin/activity/summary`
- `GET /api/v1/admin/activity/organizations/:id/summary`
- `GET /api/v1/admin/users/:id/activity/summary`
- `DELETE /api/v1/admin/users/:id/activity`

Le resume expose:

- `totals`,
- `byDay`,
- `byUser`,
- `breakdown` par mode et provider.

Le resume specifique a un utilisateur retourne le champ `user` selectionne avec les memes mesures de periode, et la route de purge supprime les lignes d activite de ce compte sans supprimer le compte.

## Fenetre temporelle

- `to` par defaut: jour UTC courant.
- `from` par defaut: `to - 29 jours`.
- Format attendu: `YYYY-MM-DD`.

Si `from > to`, la route retourne `400`.

## Scope et isolation

- `super_admin` peut lire la synthese globale ou cibler une org.
- `org_admin` reste limite a son organisation.
- Les evenements ingeres utilisent toujours `claims.OrgID` et `claims.UserID`, pas des valeurs envoyees par le client.
- Purger l activite d un utilisateur conserve le compte, les refresh sessions, les roles et les permissions.

## Observabilite technique

### Request logger

Chaque requete HTTP loggue:

- methode,
- URL,
- status,
- duree,
- IP,
- user ID si connu,
- org ID si connu,
- user-agent.

### Audit logs

`audit_logs` est alimente pour:

- `admin.login`,
- creation et mise a jour d organisations,
- creation et mise a jour de users,
- changements de mot de passe,
- changements de roles et overrides.

## Limites actuelles

- pas d exporter metrics Prometheus,
- pas de streaming sur les routes provider,
- pas d audit detaille pour les routes app hors administration.

## Liens

- Reference API: [`api-reference.md`](api-reference.md)
- Base de donnees: [`database.md`](database.md)
- CI et qualite: [`ci-quality-observability.md`](ci-quality-observability.md)
