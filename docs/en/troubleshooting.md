# Troubleshooting

## Common symptoms

| Symptom | Likely cause | Action |
| --- | --- | --- |
| `go run ./cmd/server` fails at boot | invalid `SQLITE_PATH` or inaccessible directory | check the path and the parent directory permissions |
| `/healthz` is unavailable | process not started or wrong port | check `PORT`, server logs, and the exposed port |
| `/readyz` returns `database not ready` | SQLite cannot be accessed | check `SQLITE_PATH`, the Docker volume, and write permissions |
| `/readyz` returns `mistral not configured` | empty `MISTRAL_API_KEY` or empty base URL | set `MISTRAL_API_KEY` and verify `MISTRAL_API_BASE_URL` |
| login returns `invalid credentials` | wrong email or password | verify credentials and the initial bootstrap state |
| login returns `user is inactive` | disabled user | reactivate the user through admin |
| admin login returns `admin scope required` | user lacks `feature.admin` or an admin role | grant `super_admin` or `org_admin` plus `feature.admin` |
| mutating admin route returns `invalid csrf token` | missing or stale `X-Admin-CSRF` header | reuse the token returned by admin login or refresh |
| admin route returns `forbidden origin` | `Origin` outside `ADMIN_CORS_ORIGINS` | fix the admin CORS configuration |
| `PUT /settings` returns `settings must be valid JSON` | invalid `settings` field | send a valid JSON object |
| Demeter transcription returns `context deadline exceeded (Client.Timeout exceeded while awaiting headers)` | backend Mistral timeout is too short for a long file | increase `MISTRAL_AUDIO_TRANSCRIPTION_TIMEOUT_SECONDS` and also verify upstream proxy timeouts |
| some activity events are rejected | invalid `eventKind` / `sourceMode` / `provider` combination | check the matrix in [`activity-observability.md`](activity-observability.md) |
| refresh sessions stop working after user updates | admin mutations revoke refresh sessions | log the client in again to obtain a new session |

## Possible activity rejection reasons

- `event_id_required`
- `invalid_event_kind`
- `invalid_source_mode`
- `invalid_provider_for_mode`
- `invalid_status`
- `invalid_occurred_at`
- `invalid_meta_json`

## When to inspect logs

Look first for:

- `failed to open store`,
- `[auth] access denied ...`,
- `[http] ... status=5xx`,
- `failed to reach mistral`.

## Links

- Getting started: [`getting-started.md`](getting-started.md)
- Demeter provider: [`provider-demeter-sante.md`](provider-demeter-sante.md)
- Deployment: [`deployment-operations.md`](deployment-operations.md)
