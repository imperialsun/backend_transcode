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

- `RequestTrace` recupere `X-Request-ID` ou genere un `trace_id` et le propage dans tout le backend.
- `RequestLogger` loggue chaque requete HTTP comme une ligne tracee avec le chemin de route, `step=request_completed` ou `step=request_failed`, `trace_id`, contexte user/org si disponible, et les champs de statut/duree finaux.
- `RequestTimeout` emet `step=request_timeout`, et les refus d auth emettent `step=access_denied`, pour relier une requete de bout en bout.
- Les handlers non triviaux ecrivent des logs d etapes courtes sur les flux auth, settings, activity, admin, demeter, meetings, mistral, mailer, lifecycle store, et generation reports.
- Les steps de lifecycle terminant par `_success` sont des logs de routine et ne sont pas persistés dans `backend_error_events`; seules les etapes d erreur, d echec, de timeout, et les statuts HTTP 5xx sont captures.
- Les services sortants ne logguent ni corps de mail, ni transcripts, ni mots de passe, ni tokens; seulement des compteurs, statuts, et resumes compacts d erreur.
- Les logs restent limites aux frontieres et utilisent des routes stables plutot que des URLs brutes, afin de ne jamais exposer de query strings.
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
