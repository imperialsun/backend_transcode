# Glossary

| Term | Definition |
| --- | --- |
| Access token | short-lived JWT signed with `JWT_SECRET`, read from a cookie or `Authorization` |
| Refresh token | long-lived random token, stored in the database only as a hash |
| App session | standard user session for `/api/v1/auth` and application routes |
| Admin session | dedicated session for the admin panel with separate cookies, audience, and CSRF |
| Runtime mode | value returned in `AuthResponse`, `backend` for app and `admin` for admin |
| Global role | RBAC role not limited to one organization, for example `super_admin` |
| Organization role | RBAC role applied inside one organization scope, for example `org_admin` |
| Effective permissions | union of global role permissions, org role permissions, and user overrides |
| Override | `allow` or `deny` rule applied to one permission for one user |
| Demeter Sante | backend relay endpoint family that forwards traffic to the Mistral upstream |
| Source mode | origin of an activity event: `local`, `cloud_direct`, `cloud_backend` |
| Activity summary | time-window aggregate for transcriptions and reports exposed to admins |
| Bootstrap admin | automatic first organization and admin creation on an empty database |
| WAL | SQLite `write-ahead logging` mode, creating `-wal` and `-shm` files |
