# Security Policy

## Reporting a vulnerability

- Do not open a public issue with exploit details.
- Report suspected vulnerabilities privately to the repository maintainers first.
- Include affected endpoints, configuration, reproduction steps, and impact when possible.

## Secret handling

- Keep `JWT_SECRET`, `MISTRAL_API_KEY`, and bootstrap credentials in environment variables only.
- Never commit `.env`, database files, cookies, or captured tokens.
- Rotate bootstrap credentials after the first production bootstrap if they were used.

## Security posture covered by this repo

- Password hashing with Argon2id.
- Signed JWT access tokens and hashed refresh tokens.
- Dedicated admin CSRF protection and admin origin restrictions.
- Distroless non-root runtime image.
- Automated CodeQL, Trivy, CI, and production smoke workflows.

## Extended references

- French guide: [`docs/fr/security-privacy.md`](docs/fr/security-privacy.md)
- English guide: [`docs/en/security-privacy.md`](docs/en/security-privacy.md)
- Deployment notes: [`docs/en/deployment-operations.md`](docs/en/deployment-operations.md)
