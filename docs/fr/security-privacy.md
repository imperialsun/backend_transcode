# Securite et confidentialite

## Modele de securite

Ce repo implemente une auth serveur complete:

- hash mot de passe Argon2id,
- JWT access token signes en HMAC,
- refresh tokens aleatoires hashes en base,
- verification live user / org / roles / permissions sur chaque requete protegee.

Acces SQLite:

- les lectures / ecritures SQL applicatives utilisent des placeholders `?`,
- les rares helpers internes dynamiques restants ont ete durcis avec des requetes de roles fixes et une whitelist stricte pour `PRAGMA table_info(...)`,
- aucun point d injection SQL exploitable n a ete identifie depuis les entrees HTTP actuelles.

## Sessions et cookies

- cookies `HttpOnly`,
- `SameSite=Strict`,
- `Secure` selon `COOKIE_SECURE`,
- separation stricte entre cookies app et admin,
- audience JWT distincte pour les sessions `app` et `admin`.

## Protections admin

- filtre d origine sur `/api/v1/admin*` via `ADMIN_CORS_ORIGINS`,
- CSRF obligatoire pour les routes admin mutantes via `X-Admin-CSRF`,
- scope admin limite a `super_admin` ou `org_admin` et `feature.admin`.

## Gestion des secrets

- `JWT_SECRET` ne doit jamais garder sa valeur par defaut hors dev.
- `MISTRAL_API_KEY` doit rester cote serveur.
- `.env` et les bases SQLite locales ne doivent pas etre commits.
- Les credentials bootstrap ne servent qu au premier demarrage d une base vide.

## Confidentialite des donnees

### Routes locales backend

- les settings, sessions, RBAC, et activity sont stockes localement en SQLite,
- aucune donnee n est envoyee a un provider externe via ces routes seules.

### Relay Demeter

- les contenus audio et prompts envoyes a `Demeter Sante` quittent le backend vers Mistral,
- la cle provider reste cachee cote serveur.

### Journalisation applicative

- les logs runtime utilisent un `trace_id` partage par requete pour relier le HTTP, les helpers internes, et les appels `mistral` / `mailer`,
- les logs d etapes restent limites aux routes, statuts, compteurs, identifiants techniques, et resumes compacts d erreur,
- les corps de mail, transcripts, mots de passe, tokens, et autres payloads sensibles ne sont pas journalises en clair.

## Image et runtime

- image finale `distroless` non-root,
- volume `/data` pour SQLite,
- port expose `8080`,
- pas de shell dans l image runtime.

## Verification continue

Workflows actuels:

- `ci.yml`
- `codeql.yml`
- `prod-smoke.yml`
- `trivy.yml`

Ils couvrent tests, lint, build, analyse statique, scans de vulnerabilites, et smoke HTTP.

## Recommandations operatoires

- activer `COOKIE_SECURE=true` en production,
- limiter `ADMIN_CORS_ORIGINS` aux vraies origines admin,
- proteger le volume SQLite et les sauvegardes,
- retirer ou faire tourner les credentials bootstrap apres initialisation.

## Liens

- Deploiement: [`deployment-operations.md`](deployment-operations.md)
- Auth et RBAC: [`authentication-rbac.md`](authentication-rbac.md)
- Root policy: [`SECURITY.md`](../../SECURITY.md)
