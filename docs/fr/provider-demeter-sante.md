# Provider Demeter Sante

## Vue d ensemble

Le backend expose un provider nomme `Demeter Sante`, mais le transport reel passe par le client Mistral configure dans `internal/mistral/client.go`.

## Dependances runtime

- `MISTRAL_API_KEY` doit etre renseigne.
- `MISTRAL_API_BASE_URL` pointe par defaut vers `https://api.mistral.ai`.
- `readyz` retourne `503` tant que le client Mistral n est pas configure.

## Endpoints relay

| Route backend | Upstream | Permission |
| --- | --- | --- |
| `GET /api/v1/providers/demeter-sante/models` | `GET /v1/models` | `provider.cloud.demeter_sante` ou `provider.llm.demeter_sante` |
| `POST /api/v1/providers/demeter-sante/audio/transcriptions` | `POST /v1/audio/transcriptions` | `feature.cloudupload` + `provider.cloud.demeter_sante` |
| `POST /api/v1/providers/demeter-sante/chat/completions` | `POST /v1/chat/completions` | `feature.llmapi` + `provider.llm.demeter_sante` |

## Comportement de relay

- Le backend ne reinterprette pas les payloads JSON.
- Pour l audio, il exige `multipart/form-data` puis relaie le corps tel quel.
- Le status code et le body upstream sont renvoyes tels quels au client.
- En cas d echec reseau vers Mistral, le backend retourne `502`.
- Si Mistral n est pas configure, le backend retourne `503`.

## Donnees et secrets

- La cle Mistral reste cote serveur.
- Le client frontend ne voit jamais `MISTRAL_API_KEY`.
- Les contenus audio ou prompts envoyes a ces routes quittent donc le poste client et transitent par le backend vers Mistral.

## Bonnes pratiques

- Restreindre cette route aux utilisateurs qui ont vraiment les permissions Demeter.
- Surveiller les erreurs `502` pour distinguer les pannes reseau des refus upstream.
- Ne pas utiliser `readyz` comme simple test applicatif si vous deployez sans Mistral: il restera rouge par conception actuelle.

## Liens

- Reference API: [`api-reference.md`](api-reference.md)
- Securite: [`security-privacy.md`](security-privacy.md)
- Deploiement: [`deployment-operations.md`](deployment-operations.md)
