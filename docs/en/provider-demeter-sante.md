# Demeter Sante Provider

## Overview

The backend exposes a provider named `Demeter Sante`, but the actual transport goes through the configured Mistral client in `internal/mistral/client.go`.

## Runtime dependencies

- `MISTRAL_API_KEY` must be set.
- `MISTRAL_API_BASE_URL` defaults to `https://api.mistral.ai`.
- `readyz` returns `503` while the Mistral client is not configured.
- The backend runtime image must provide `ffmpeg` and `ffprobe` for `/audio/transcriptions/backend`, which accepts `slice-v1` transport in 5 MiB chunks, reconstructs the audio server-side, and then chunks long audio before calling Mistral.

## Relay endpoints

| Backend route | Upstream | Permission |
| --- | --- | --- |
| `GET /api/v1/providers/demeter-sante/models` | `GET /v1/models` | `provider.cloud.demeter_sante` or `provider.llm.demeter_sante` |
| `POST /api/v1/providers/demeter-sante/audio/transcriptions` | `POST /v1/audio/transcriptions` | `feature.cloudupload` + `provider.cloud.demeter_sante` |
| `POST /api/v1/providers/demeter-sante/audio/transcriptions/backend` | `POST /v1/audio/transcriptions` | `feature.cloudupload` + `provider.cloud.demeter_sante` |
| `POST /api/v1/providers/demeter-sante/chat/completions` | `POST /v1/chat/completions` | `feature.llmapi` + `provider.llm.demeter_sante` |

## Relay behavior

- The backend does not reinterpret JSON payloads.
- For audio, it requires `multipart/form-data` and relays the body as-is.
- The `/audio/transcriptions/backend` route is slice-only: the frontend sends 5 MiB transport slices with `X-Demeter-Transport: slice-v1`, and the backend reconstructs the source file before processing.
- The backend preserves the uploaded audio format and does not re-encode the file.
- Common formats include `wav`, `mp3`, `m4a/mp4`, `aac`, `ogg/opus`, and `webm` as long as the file is non-empty and still decodable upstream.
- The upstream status code and body are returned to the client unchanged.
- Empty files are rejected with a `400 empty_audio_file`.
- If the network call to Mistral fails, the backend returns `502`.
- If Mistral is not configured, the backend returns `503`.

## Data and secrets

- The Mistral key stays server-side.
- The frontend never sees `MISTRAL_API_KEY`.
- Audio content or prompts sent to these routes therefore leave the workstation and transit through the backend to Mistral.

## Operational guidance

- Restrict these routes to users that actually need Demeter permissions.
- Watch for `502` errors to separate network outages from upstream rejections.
- Do not use `readyz` as a generic application check if you intentionally deploy without Mistral: it stays red by current design.

## Links

- API reference: [`api-reference.md`](api-reference.md)
- Security: [`security-privacy.md`](security-privacy.md)
- Deployment: [`deployment-operations.md`](deployment-operations.md)
