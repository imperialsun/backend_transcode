# Troubleshooting

## Symptomes frequents

| Symptome | Cause probable | Action |
| --- | --- | --- |
| `go run ./cmd/server` echoue au boot | `SQLITE_PATH` invalide ou dossier non accessible | verifier le chemin et les permissions du dossier parent |
| `/healthz` indisponible | process non lance ou port incorrect | verifier `PORT`, logs serveur, et port expose |
| `/readyz` retourne `database not ready` | SQLite non accessible | verifier `SQLITE_PATH`, volume Docker, et droits d ecriture |
| `/readyz` retourne `mistral not configured` | `MISTRAL_API_KEY` vide ou base URL vide | renseigner `MISTRAL_API_KEY` et verifier `MISTRAL_API_BASE_URL` |
| login retourne `invalid credentials` | email ou mot de passe incorrect | verifier les credentials et le bootstrap initial |
| login retourne `user is inactive` | user desactive | reactiver le user via l admin |
| admin login retourne `admin scope required` | user sans `feature.admin` ou sans role admin | attribuer `super_admin` ou `org_admin` + `feature.admin` |
| route admin mutante retourne `invalid csrf token` | header `X-Admin-CSRF` absent ou stale | reutiliser le token renvoye par login ou refresh admin |
| route admin retourne `forbidden origin` | `Origin` hors `ADMIN_CORS_ORIGINS` | corriger la configuration CORS admin |
| `PUT /settings` retourne `settings must be valid JSON` | champ `settings` non valide | envoyer un objet JSON valide |
| certains events activity sont rejetes | combinaison `eventKind` / `sourceMode` / `provider` invalide | verifier la matrice dans [`activity-observability.md`](activity-observability.md) |
| les refresh expirent apres mise a jour user | les mutations admin revoquent les refresh sessions | reconnecter le client pour obtenir une nouvelle session |

## Rejections activity possibles

- `event_id_required`
- `invalid_event_kind`
- `invalid_source_mode`
- `invalid_provider_for_mode`
- `invalid_status`
- `invalid_occurred_at`
- `invalid_meta_json`

## Quand regarder les logs

Chercher en priorite:

- erreurs `failed to open store`,
- logs `[auth] access denied ...`,
- logs `[http] ... status=5xx`,
- erreurs `failed to reach mistral`.

## Liens

- Demarrage rapide: [`getting-started.md`](getting-started.md)
- Provider Demeter: [`provider-demeter-sante.md`](provider-demeter-sante.md)
- Deploiement: [`deployment-operations.md`](deployment-operations.md)
