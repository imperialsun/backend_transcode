# Glossaire

| Terme | Definition |
| --- | --- |
| Access token | JWT court terme signe avec `JWT_SECRET`, lu depuis cookie ou `Authorization` |
| Refresh token | token long terme aleatoire, stocke en base seulement sous forme de hash |
| App session | session utilisateur standard pour les routes `/api/v1/auth` et applicatives |
| Admin session | session dediee au panel admin avec cookies, audience, et CSRF distincts |
| Runtime mode | valeur renvoyee dans `AuthResponse`, `backend` pour app et `admin` pour admin |
| Global role | role RBAC non limite a une organisation, ex. `super_admin` |
| Organization role | role RBAC applique dans le scope d une organisation, ex. `org_admin` |
| Effective permissions | union roles globaux + roles org + overrides utilisateur |
| Override | regle `allow` ou `deny` sur une permission pour un user |
| Demeter Sante | famille provider backend pour les operations audio et rapport adossees a Mistral |
| Source mode | origine d un event activity: `local`, `cloud_direct`, `cloud_backend` |
| Activity summary | agregat admin par periode sur transcriptions et reports |
| Bootstrap admin | creation initiale automatique d une org et d un admin sur base vide |
| WAL | mode SQLite `write-ahead logging`, produit les fichiers `-wal` et `-shm` |
