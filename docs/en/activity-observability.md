# Activity and Observability

## Activity ingestion

The backend ingests application usage through `POST /api/v1/activity/events`.

Expected body:

```json
{
  "events": [
    {
      "eventId": "evt-001",
      "eventKind": "transcription",
      "sourceMode": "cloud_backend",
      "provider": "demeter_sante",
      "status": "success",
      "occurredAt": "2026-03-09T10:00:00Z",
      "meta": {}
    }
  ]
}
```

Response:

```json
{
  "accepted": 1,
  "duplicates": 0,
  "rejected": []
}
```

## Event validation

| `eventKind` | `sourceMode` | Allowed providers |
| --- | --- | --- |
| `transcription` | `local` | `local_upload`, `mic` |
| `transcription` | `cloud_direct` | `whisper`, `mistral` |
| `transcription` | `cloud_backend` | `demeter_sante` |
| `report` | `local` | `local` |
| `report` | `cloud_direct` | `huggingface`, `mistral` |
| `report` | `cloud_backend` | `demeter_sante` |

Other rules:

- `status` must be `success` or `error`,
- `occurredAt` must be `RFC3339` if provided,
- `meta` must be valid JSON if provided,
- `eventId` is the idempotency key.

## Admin aggregation

Available routes:

- `GET /api/v1/admin/activity/summary`
- `GET /api/v1/admin/activity/organizations/:id/summary`

The summary exposes:

- `totals`,
- `byDay`,
- `byUser`,
- `breakdown` by mode and provider.

## Time window

- default `to`: current UTC day.
- default `from`: `to - 29 days`.
- expected format: `YYYY-MM-DD`.

If `from > to`, the route returns `400`.

## Scope and isolation

- `super_admin` can read the global summary or target a specific org.
- `org_admin` stays limited to its organization.
- Ingested events always use `claims.OrgID` and `claims.UserID`, not values sent by the client.

## Technical observability

### Request logger

Each HTTP request logs:

- method,
- URL,
- status,
- duration,
- IP,
- user ID when known,
- org ID when known,
- user-agent.

### Audit logs

`audit_logs` is populated for:

- `admin.login`,
- organization create and update,
- user create and update,
- password changes,
- role and entitlement changes.

## Current limits

- no Prometheus metrics exporter,
- no streaming on provider routes,
- no detailed audit trail for non-admin app routes.

## Links

- API reference: [`api-reference.md`](api-reference.md)
- Database: [`database.md`](database.md)
- CI and quality: [`ci-quality-observability.md`](ci-quality-observability.md)
