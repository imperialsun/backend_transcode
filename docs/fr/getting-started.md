# Demarrage rapide

## Prerequis

- Go `1.25.7` ou compatible avec `go.mod`.
- `make` pour lancer les commandes standard du repo.
- Docker optionnel pour executer les stacks conteneurisees en local.
- Une cle `MISTRAL_API_KEY` si vous voulez utiliser les routes Demeter ou obtenir un `readyz` vert.

## Setup local

```bash
cp .env.example .env
go run ./cmd/server
```

Alternative avec chargement automatique de `.env`:

```bash
./scripts/run-local-backend.sh
```

Par defaut, l API ecoute sur `http://localhost:8080` et les routes metier vivent sous `/api/v1`.

## Verification minimale

```bash
curl -fsS http://localhost:8080/healthz
curl -fsS http://localhost:8080/readyz
```

`/healthz` verifie seulement que le process repond.
`/readyz` exige une base SQLite ouverte et un client Mistral configure.

## Commandes utiles

- `make docs-check`: verifie la structure et les liens de la documentation.
- `make test`: lance `go test ./...`.
- `make ci`: enchaine docs, format, tests, lint, vet, et build.
- `./scripts/run-local-backend.sh`: charge `.env` puis lance `go run ./cmd/server`.

## Bootstrap admin

Au premier demarrage seulement, le backend peut creer une organisation et un admin si:

- la table `users` est vide,
- `BOOTSTRAP_ADMIN_EMAIL` est renseigne,
- `BOOTSTRAP_ADMIN_PASSWORD` est renseigne.

Le mot de passe bootstrap est hash par Argon2id avant insertion.

## Execution Docker

```bash
docker build -t transcode-backend:local .
docker run --rm \
  -p 8080:8080 \
  -v "$(pwd)/data:/data" \
  -e APP_ENV=production \
  -e JWT_SECRET=change-me \
  -e MISTRAL_API_KEY=change-me \
  transcode-backend:local
```

L image ecrit SQLite dans `/data/backend.sqlite` par defaut.

## Docker Compose

Lancement prod-like avec l image finale existante:

```bash
docker compose up --build
```

Lancement developpement avec `go run` dans `golang:1.25.7` et montage du repo:

```bash
docker compose -f compose.dev.yml up
```

Les fichiers Compose gardent toutes les variables dans `environment:`. Les secrets restent injectables via substitution de variables comme `JWT_SECRET` et `MISTRAL_API_KEY`, sans etre ecrits en dur dans le YAML.

Il n y a volontairement pas de `healthcheck` Docker. Utilisez `GET /healthz` pour un controle manuel generique. `GET /readyz` depend aussi de SQLite et d un client Mistral configure, donc il reste rouge si `MISTRAL_API_KEY` est vide.

## Erreurs frequentes de setup

### `readyz` retourne 503

Ca arrive si:

- `MISTRAL_API_KEY` est vide,
- `MISTRAL_API_BASE_URL` est invalide,
- SQLite ne peut pas etre ouverte.

### Aucun admin bootstrap n apparait

Ca arrive si la base contient deja au moins un utilisateur. `EnsureBootstrap` ne s execute qu une fois sur une base vide.

### Les cookies ne sont pas poses en local

Verifiez `COOKIE_SECURE=false` si vous testez en HTTP local.

## Suite recommandee

- Architecture: [`architecture.md`](architecture.md)
- Auth et RBAC: [`authentication-rbac.md`](authentication-rbac.md)
- Deploiement: [`deployment-operations.md`](deployment-operations.md)
