# CI, qualite et observabilite

## Gate locale recommandee

```bash
make docs-check
go test ./...
go test ./... -race -coverprofile=coverage.out
golangci-lint run
go vet ./...
test -z "$(gofmt -l .)"
go build ./cmd/server
```

## Cibles Makefile

| Cible | Role |
| --- | --- |
| `docs-check` | valide la structure FR/EN et les liens markdown |
| `fmt-check` | verifie `gofmt -l .` vide |
| `test` | `go test ./...` |
| `test-race` | `go test ./... -race -coverprofile=coverage.out` |
| `lint` | `golangci-lint run` |
| `vet` | `go vet ./...` |
| `build` | `go build ./cmd/server` |
| `ci` | enchaine tous les checks ci-dessus |

## Workflows GitHub

### `ci.yml`

- checkout,
- setup Go,
- download dependencies,
- `make docs-check`,
- tests unitaires,
- test race + couverture,
- lint,
- vet,
- format check,
- build,
- upload de `coverage.out`.

### `codeql.yml`

- analyse statique Go,
- execution sur PR, push `main`, planification, et manuel.

### `prod-smoke.yml`

- build image Docker,
- lance le conteneur backend,
- verifie `healthz` et `readyz`,
- couvre aussi un login, `auth/me`, `settings`, et `settings/reset`.

### `trivy.yml`

- scan filesystem,
- scan image Docker,
- publication SARIF dans GitHub code scanning.

## Observabilite runtime

- `RequestLogger` loggue toutes les requetes HTTP avec contexte user/org si disponible.
- `GET /healthz` et `GET /readyz` servent de probes.
- `audit_logs` capture l activite admin sensible.
- Les endpoints activity admin servent d observabilite metier lightweight.

## Attentes pour les contributions

- toute evolution fonctionnelle doit mettre a jour la doc FR et EN,
- les liens docs doivent rester valides,
- une nouvelle route ou variable d environnement doit etre documentee dans les pages de reference correspondantes.

## Liens

- Contribution detaillee: [`contributing.md`](contributing.md)
- Activite: [`activity-observability.md`](activity-observability.md)
- Securite: [`security-privacy.md`](security-privacy.md)
