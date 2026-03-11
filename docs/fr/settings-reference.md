# Reference des reglages

## Role du backend

Le backend est la source de verite pour les reglages utilisateur persistants. Il ne connait pas le schema metier detaille du frontend: il stocke un blob JSON opaque par utilisateur.

## Envelope renvoyee

```json
{
  "version": 1,
  "schemaVersion": 1,
  "updatedAt": "2026-03-09T10:00:00Z",
  "settings": {}
}
```

## Champs

| Champ | Type | Comportement |
| --- | --- | --- |
| `version` | entier | incremente a chaque sauvegarde ou reset |
| `schemaVersion` | entier | force a `1` si absent ou `<= 0` |
| `updatedAt` | RFC3339 | derive de l horloge serveur |
| `settings` | JSON | document utilisateur opaque |

## Semantique des routes

### `GET /api/v1/settings`

- exige `feature.settings`,
- lit `user_settings` par `claims.UserID`,
- si aucune ligne n existe, renvoie un envelope virtuel avec `settings: {}`.

### `PUT /api/v1/settings`

- exige `feature.settings`,
- valide seulement que `settings` est un JSON valide,
- transforme un payload vide en `{}`,
- fait un upsert sur `user_settings`,
- met a jour `organization_id`, `schema_version`, `updated_at`,
- incremente `version` si la ligne existe deja.

### `POST /api/v1/settings/reset`

- exige `feature.settings`,
- remplace le document par `{}`,
- repasse `schemaVersion` a `1`.

## Responsabilites frontend / backend

Le backend:

- persiste le document,
- gere la version et le timestamp,
- isole les donnees par utilisateur et organisation.

Le frontend:

- definit les defaults applicatifs,
- gere la migration fine du schema fonctionnel,
- choisit quelles cles ecrire dans `settings`.

## Limites actuelles

- pas de validation de schema JSON cote serveur,
- pas de controle de concurrence optimiste,
- pas de creation automatique de la ligne `user_settings` a la creation du user.

## Liens

- Reference API: [`api-reference.md`](api-reference.md)
- Architecture: [`architecture.md`](architecture.md)
- Base de donnees: [`database.md`](database.md)
