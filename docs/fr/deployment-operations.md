# Deploiement et operations

## Vue d ensemble

Le repo fournit:

- un binaire local via `go run ./cmd/server`,
- un helper `scripts/run-local-backend.sh`,
- une image Docker multi-stage `golang -> distroless`.

## Variables d environnement

| Variable | Default | Usage |
| --- | --- | --- |
| `APP_ENV` | `development` | influence certains defaults, dont `COOKIE_SECURE` |
| `PORT` | `8080` | port HTTP d ecoute |
| `SQLITE_PATH` | `./backend.sqlite` ou `/data/backend.sqlite` en image | chemin SQLite |
| `JWT_SECRET` | valeur dev insecure | secret de signature JWT |
| `ACCESS_TTL_MINUTES` | `15` | TTL access app |
| `REFRESH_TTL_HOURS` | `720` | TTL refresh app |
| `ADMIN_ACCESS_TTL_MINUTES` | `10` | TTL access admin |
| `ADMIN_REFRESH_TTL_HOURS` | `12` | TTL refresh admin |
| `COOKIE_SECURE` | `false` en dev, `true` en prod | flag `Secure` des cookies |
| `APP_CORS_ORIGINS` | `http://localhost:3000,http://localhost:5173` | origines app |
| `ADMIN_CORS_ORIGINS` | `http://localhost:4173` | origines admin + filtre d origine |
| `CORS_ORIGINS` | meme valeur que app origins | fallback legacy pour `APP_CORS_ORIGINS` |
| `MISTRAL_API_BASE_URL` | `https://api.mistral.ai` | base URL upstream |
| `MISTRAL_API_KEY` | vide | cle requise pour Demeter et `readyz` |
| `BOOTSTRAP_ADMIN_EMAIL` | vide | email admin cree au premier boot |
| `BOOTSTRAP_ADMIN_PASSWORD` | vide | mot de passe admin cree au premier boot |
| `BOOTSTRAP_ORG_NAME` | `Default Organization` | nom org bootstrap |

## Lancement local

```bash
cp .env.example .env
./scripts/run-local-backend.sh
```

Le script:

- charge `.env` s il existe,
- force des defaults pour `APP_ENV`, `PORT`, `SQLITE_PATH`,
- lance `go run ./cmd/server`.

## Image Docker

### Build

```bash
docker build -t demeter-backend .
```

### Run

```bash
docker run --rm \
  -p 8080:8080 \
  -v "$(pwd)/data:/data" \
  -e APP_ENV=production \
  -e JWT_SECRET=change-me \
  -e MISTRAL_API_KEY=change-me \
  demeter-backend
```

Caracteristiques de l image:

- build avec `CGO_ENABLED=0`,
- binaire `/server`,
- runtime `gcr.io/distroless/base-debian12:nonroot`,
- `VOLUME ["/data"]`,
- `EXPOSE 8080`.

## Runbook minimal

### Demarrage

1. definir les variables d environnement,
2. monter un volume persistant pour SQLite,
3. lancer le process,
4. verifier:

```bash
curl -fsS http://localhost:8080/healthz
curl -fsS http://localhost:8080/readyz
```

### Arret

Le serveur ecoute `SIGINT` et `SIGTERM`, puis tente un shutdown grace sur 10 secondes.

### Donnees

- SQLite utilise le mode WAL,
- les fichiers `*.sqlite-wal` et `*.sqlite-shm` sont normaux,
- sauvegarder le repertoire de donnees complet, pas seulement `backend.sqlite`.

## Point d attention readiness

`/readyz` n est pas un simple health applicatif:

- il ping la base,
- il exige aussi `MISTRAL_API_KEY` et une base URL non vide.

Un deploiement sans provider Mistral aura donc `healthz` vert mais `readyz` rouge.

## Liens

- Securite: [`security-privacy.md`](security-privacy.md)
- CI et qualite: [`ci-quality-observability.md`](ci-quality-observability.md)
- Depannage: [`troubleshooting.md`](troubleshooting.md)
