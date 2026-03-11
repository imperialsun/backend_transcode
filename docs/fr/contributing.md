# Contribuer au backend

## Workflow

- travailler sur une branche dediee,
- garder des commits atomiques,
- inclure dans la PR: impact, risques, validation, et mise a jour doc si necessaire.

## Gate locale obligatoire avant PR

```bash
make docs-check
go test ./...
go test ./... -race -coverprofile=coverage.out
golangci-lint run
go vet ./...
test -z "$(gofmt -l .)"
go build ./cmd/server
```

## Regles documentation

- toute modification de comportement backend doit etre documentee en FR et en EN,
- les liens du portail docs doivent rester valides,
- les nouvelles routes, permissions, variables d environnement, ou changements de schema doivent etre reflectes dans les pages de reference.

## Politique commit / push

Avant `git commit` ou `git push`, partager:

- le diff courant,
- les sorties des checks ci-dessus.

Puis attendre l accord explicite si le workflow de collaboration l exige.

## Rappels securite

- ne jamais commit de secret,
- garder `JWT_SECRET` et `MISTRAL_API_KEY` dans l environnement,
- ne pas publier de details de vuln hors canal prive.

## Liens

- Root guide: [`CONTRIBUTING.md`](../../CONTRIBUTING.md)
- Politique securite: [`SECURITY.md`](../../SECURITY.md)
- CI et qualite: [`ci-quality-observability.md`](ci-quality-observability.md)
