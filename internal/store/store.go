package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

type Store struct {
	DB *sql.DB
}

type Organization struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Code      string    `json:"code"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type User struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organizationId"`
	Email          string    `json:"email"`
	PasswordHash   string    `json:"-"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type SettingsRecord struct {
	Version       int             `json:"version"`
	SchemaVersion int             `json:"schemaVersion"`
	UpdatedAt     time.Time       `json:"updatedAt"`
	Settings      json.RawMessage `json:"settings"`
}

type RefreshSession struct {
	ID             string
	UserID         string
	OrganizationID string
	TokenHash      string
	ExpiresAt      time.Time
	RevokedAt      sql.NullTime
	CreatedAt      time.Time
}

type UserPermissionOverrideInput struct {
	PermissionCode string `json:"permissionCode"`
	Effect         string `json:"effect"`
}

type UpdateUserInput struct {
	Email          *string `json:"email"`
	Status         *string `json:"status"`
	OrganizationID *string `json:"organizationId"`
}

func Open(ctx context.Context, path string) (*Store, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir db dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON;`); err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode = WAL;`); err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, `PRAGMA synchronous = NORMAL;`); err != nil {
		return nil, err
	}
	store := &Store{DB: db}
	if err := store.Migrate(ctx); err != nil {
		return nil, err
	}
	if err := store.SeedBaseCatalog(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s.DB == nil {
		return nil
	}
	return s.DB.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.DB.PingContext(ctx)
}

func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS organizations (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			code TEXT NOT NULL UNIQUE,
			status TEXT NOT NULL DEFAULT 'active',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			organization_id TEXT NOT NULL,
			email TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY(organization_id) REFERENCES organizations(id)
		);`,
		`CREATE TABLE IF NOT EXISTS global_roles (
			id TEXT PRIMARY KEY,
			code TEXT NOT NULL UNIQUE,
			label TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS organization_roles (
			id TEXT PRIMARY KEY,
			code TEXT NOT NULL UNIQUE,
			label TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS permissions (
			id TEXT PRIMARY KEY,
			code TEXT NOT NULL UNIQUE,
			label TEXT NOT NULL,
			scope TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS user_global_roles (
			user_id TEXT NOT NULL,
			global_role_id TEXT NOT NULL,
			PRIMARY KEY(user_id, global_role_id),
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
			FOREIGN KEY(global_role_id) REFERENCES global_roles(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS user_organization_roles (
			user_id TEXT NOT NULL,
			organization_role_id TEXT NOT NULL,
			PRIMARY KEY(user_id, organization_role_id),
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
			FOREIGN KEY(organization_role_id) REFERENCES organization_roles(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS global_role_permissions (
			global_role_id TEXT NOT NULL,
			permission_id TEXT NOT NULL,
			PRIMARY KEY(global_role_id, permission_id),
			FOREIGN KEY(global_role_id) REFERENCES global_roles(id) ON DELETE CASCADE,
			FOREIGN KEY(permission_id) REFERENCES permissions(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS organization_role_permissions (
			organization_role_id TEXT NOT NULL,
			permission_id TEXT NOT NULL,
			PRIMARY KEY(organization_role_id, permission_id),
			FOREIGN KEY(organization_role_id) REFERENCES organization_roles(id) ON DELETE CASCADE,
			FOREIGN KEY(permission_id) REFERENCES permissions(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS user_permission_overrides (
			user_id TEXT NOT NULL,
			permission_id TEXT NOT NULL,
			effect TEXT NOT NULL,
			PRIMARY KEY(user_id, permission_id),
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
			FOREIGN KEY(permission_id) REFERENCES permissions(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS user_settings (
			user_id TEXT PRIMARY KEY,
			organization_id TEXT NOT NULL,
			settings_json TEXT NOT NULL,
			version INTEGER NOT NULL,
			schema_version INTEGER NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
			FOREIGN KEY(organization_id) REFERENCES organizations(id)
		);`,
		`CREATE TABLE IF NOT EXISTS refresh_sessions (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			organization_id TEXT NOT NULL,
			refresh_hash TEXT NOT NULL,
			expires_at DATETIME NOT NULL,
			revoked_at DATETIME,
			created_at DATETIME NOT NULL,
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
			FOREIGN KEY(organization_id) REFERENCES organizations(id)
		);`,
		`CREATE TABLE IF NOT EXISTS audit_logs (
			id TEXT PRIMARY KEY,
			actor_user_id TEXT,
			organization_id TEXT,
			action TEXT NOT NULL,
			target_type TEXT,
			target_id TEXT,
			payload_json TEXT,
			created_at DATETIME NOT NULL,
			FOREIGN KEY(actor_user_id) REFERENCES users(id),
			FOREIGN KEY(organization_id) REFERENCES organizations(id)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_users_org ON users(organization_id);`,
		`CREATE INDEX IF NOT EXISTS idx_refresh_user ON refresh_sessions(user_id);`,
	}

	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *Store) SeedBaseCatalog(ctx context.Context) error {
	permissions := []struct {
		Code  string
		Label string
		Scope string
	}{
		{"feature.localupload", "Local upload", "feature"},
		{"feature.cloudupload", "Cloud upload", "feature"},
		{"feature.llmlocal", "LLM local", "feature"},
		{"feature.llmapi", "LLM cloud", "feature"},
		{"feature.settings", "Settings", "feature"},
		{"feature.telemetry", "Telemetry", "feature"},
		{"feature.admin", "Administration", "feature"},
		{"provider.cloud.gradio", "Provider cloud Gradio", "provider_cloud"},
		{"provider.cloud.whisper", "Provider cloud Whisper", "provider_cloud"},
		{"provider.cloud.mistral", "Provider cloud Mistral", "provider_cloud"},
		{"provider.cloud.demeter_sante", "Provider cloud Demeter Sante", "provider_cloud"},
		{"provider.llm.huggingface", "Provider llm Hugging Face", "provider_llm"},
		{"provider.llm.mistral", "Provider llm Mistral", "provider_llm"},
		{"provider.llm.demeter_sante", "Provider llm Demeter Sante", "provider_llm"},
	}
	for _, p := range permissions {
		if err := s.upsertPermission(ctx, p.Code, p.Label, p.Scope); err != nil {
			return err
		}
	}
	if err := s.upsertGlobalRole(ctx, "super_admin", "Super Admin"); err != nil {
		return err
	}
	if err := s.upsertGlobalRole(ctx, "user", "User"); err != nil {
		return err
	}
	if err := s.upsertOrganizationRole(ctx, "org_admin", "Organization Admin"); err != nil {
		return err
	}
	if err := s.upsertOrganizationRole(ctx, "org_member", "Organization Member"); err != nil {
		return err
	}

	allCodes := make([]string, 0, len(permissions))
	for _, p := range permissions {
		allCodes = append(allCodes, p.Code)
	}
	if err := s.SetGlobalRolePermissionsByCode(ctx, "super_admin", allCodes); err != nil {
		return err
	}
	if err := s.SetGlobalRolePermissionsByCode(ctx, "user", []string{
		"feature.localupload",
		"feature.cloudupload",
		"feature.llmlocal",
		"feature.llmapi",
		"feature.settings",
		"feature.telemetry",
		"provider.cloud.gradio",
		"provider.cloud.whisper",
		"provider.cloud.mistral",
		"provider.cloud.demeter_sante",
		"provider.llm.huggingface",
		"provider.llm.mistral",
		"provider.llm.demeter_sante",
	}); err != nil {
		return err
	}
	if err := s.SetOrganizationRolePermissionsByCode(ctx, "org_admin", allCodes); err != nil {
		return err
	}
	if err := s.SetOrganizationRolePermissionsByCode(ctx, "org_member", []string{
		"feature.localupload",
		"feature.cloudupload",
		"feature.llmlocal",
		"feature.llmapi",
		"feature.settings",
		"feature.telemetry",
		"provider.cloud.gradio",
		"provider.cloud.whisper",
		"provider.cloud.mistral",
		"provider.cloud.demeter_sante",
		"provider.llm.huggingface",
		"provider.llm.mistral",
		"provider.llm.demeter_sante",
	}); err != nil {
		return err
	}
	return nil
}

func (s *Store) EnsureBootstrap(ctx context.Context, adminEmail, passwordHash, orgName string) error {
	var count int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	if strings.TrimSpace(adminEmail) == "" || strings.TrimSpace(passwordHash) == "" {
		return nil
	}
	org, err := s.CreateOrganization(ctx, strings.TrimSpace(orgName), normalizeOrgCode(orgName), "active")
	if err != nil {
		return err
	}
	user, err := s.CreateUser(ctx, org.ID, strings.ToLower(strings.TrimSpace(adminEmail)), passwordHash, "active")
	if err != nil {
		return err
	}
	if err := s.SetUserGlobalRoles(ctx, user.ID, []string{"super_admin", "user"}); err != nil {
		return err
	}
	if err := s.SetUserOrganizationRoles(ctx, user.ID, []string{"org_admin", "org_member"}); err != nil {
		return err
	}
	return nil
}

func (s *Store) upsertPermission(ctx context.Context, code, label, scope string) error {
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO permissions(id, code, label, scope)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(code) DO UPDATE SET label=excluded.label, scope=excluded.scope
	`, uuid.NewString(), code, label, scope)
	return err
}

func (s *Store) upsertGlobalRole(ctx context.Context, code, label string) error {
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO global_roles(id, code, label)
		VALUES(?, ?, ?)
		ON CONFLICT(code) DO UPDATE SET label=excluded.label
	`, uuid.NewString(), code, label)
	return err
}

func (s *Store) upsertOrganizationRole(ctx context.Context, code, label string) error {
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO organization_roles(id, code, label)
		VALUES(?, ?, ?)
		ON CONFLICT(code) DO UPDATE SET label=excluded.label
	`, uuid.NewString(), code, label)
	return err
}

func (s *Store) ListOrganizations(ctx context.Context) ([]Organization, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, name, code, status, created_at, updated_at
		FROM organizations
		ORDER BY name ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Organization
	for rows.Next() {
		var o Organization
		if err := rows.Scan(&o.ID, &o.Name, &o.Code, &o.Status, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Store) GetOrganizationByID(ctx context.Context, id string) (*Organization, error) {
	var o Organization
	err := s.DB.QueryRowContext(ctx, `
		SELECT id, name, code, status, created_at, updated_at
		FROM organizations WHERE id = ?
	`, id).Scan(&o.ID, &o.Name, &o.Code, &o.Status, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &o, nil
}

func (s *Store) CreateOrganization(ctx context.Context, name, code, status string) (*Organization, error) {
	now := time.Now().UTC()
	org := &Organization{
		ID:        uuid.NewString(),
		Name:      strings.TrimSpace(name),
		Code:      normalizeOrgCode(code),
		Status:    normalizeStatus(status),
		CreatedAt: now,
		UpdatedAt: now,
	}
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO organizations(id, name, code, status, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?)
	`, org.ID, org.Name, org.Code, org.Status, org.CreatedAt, org.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return org, nil
}

func (s *Store) UpdateOrganization(ctx context.Context, id string, name, code, status *string) (*Organization, error) {
	current, err := s.GetOrganizationByID(ctx, id)
	if err != nil || current == nil {
		return current, err
	}
	if name != nil {
		current.Name = strings.TrimSpace(*name)
	}
	if code != nil {
		current.Code = normalizeOrgCode(*code)
	}
	if status != nil {
		current.Status = normalizeStatus(*status)
	}
	current.UpdatedAt = time.Now().UTC()
	_, err = s.DB.ExecContext(ctx, `
		UPDATE organizations SET name = ?, code = ?, status = ?, updated_at = ? WHERE id = ?
	`, current.Name, current.Code, current.Status, current.UpdatedAt, current.ID)
	if err != nil {
		return nil, err
	}
	return current, nil
}

func (s *Store) FindUserByEmail(ctx context.Context, email string) (*User, error) {
	var u User
	err := s.DB.QueryRowContext(ctx, `
		SELECT id, organization_id, email, password_hash, status, created_at, updated_at
		FROM users WHERE lower(email) = lower(?)
	`, strings.TrimSpace(email)).Scan(&u.ID, &u.OrganizationID, &u.Email, &u.PasswordHash, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &u, nil
}

func (s *Store) GetUserByID(ctx context.Context, id string) (*User, error) {
	var u User
	err := s.DB.QueryRowContext(ctx, `
		SELECT id, organization_id, email, password_hash, status, created_at, updated_at
		FROM users WHERE id = ?
	`, id).Scan(&u.ID, &u.OrganizationID, &u.Email, &u.PasswordHash, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &u, nil
}

func (s *Store) ListUsersByOrganization(ctx context.Context, organizationID string) ([]User, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, organization_id, email, password_hash, status, created_at, updated_at
		FROM users
		WHERE organization_id = ?
		ORDER BY email ASC
	`, organizationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]User, 0)
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.OrganizationID, &u.Email, &u.PasswordHash, &u.Status, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) CreateUser(ctx context.Context, organizationID, email, passwordHash, status string) (*User, error) {
	now := time.Now().UTC()
	u := &User{
		ID:             uuid.NewString(),
		OrganizationID: organizationID,
		Email:          strings.ToLower(strings.TrimSpace(email)),
		PasswordHash:   passwordHash,
		Status:         normalizeStatus(status),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO users(id, organization_id, email, password_hash, status, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)
	`, u.ID, u.OrganizationID, u.Email, u.PasswordHash, u.Status, u.CreatedAt, u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Store) UpdateUser(ctx context.Context, userID string, input UpdateUserInput) (*User, error) {
	current, err := s.GetUserByID(ctx, userID)
	if err != nil || current == nil {
		return current, err
	}
	if input.Email != nil {
		current.Email = strings.ToLower(strings.TrimSpace(*input.Email))
	}
	if input.Status != nil {
		current.Status = normalizeStatus(*input.Status)
	}
	if input.OrganizationID != nil {
		current.OrganizationID = strings.TrimSpace(*input.OrganizationID)
	}
	current.UpdatedAt = time.Now().UTC()
	_, err = s.DB.ExecContext(ctx, `
		UPDATE users SET organization_id = ?, email = ?, status = ?, updated_at = ? WHERE id = ?
	`, current.OrganizationID, current.Email, current.Status, current.UpdatedAt, current.ID)
	if err != nil {
		return nil, err
	}
	return current, nil
}

func (s *Store) UpdateUserPassword(ctx context.Context, userID, passwordHash string) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?
	`, passwordHash, time.Now().UTC(), userID)
	return err
}

func (s *Store) IsUserInOrganization(ctx context.Context, userID, organizationID string) (bool, error) {
	var count int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ? AND organization_id = ?`, userID, organizationID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *Store) GetGlobalRoleCodesByUser(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT gr.code
		FROM user_global_roles ugr
		JOIN global_roles gr ON gr.id = ugr.global_role_id
		WHERE ugr.user_id = ?
		ORDER BY gr.code
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStringRows(rows)
}

func (s *Store) GetOrganizationRoleCodesByUser(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT orr.code
		FROM user_organization_roles uor
		JOIN organization_roles orr ON orr.id = uor.organization_role_id
		WHERE uor.user_id = ?
		ORDER BY orr.code
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStringRows(rows)
}

func (s *Store) ResolveEffectivePermissions(ctx context.Context, userID string) ([]string, error) {
	base := map[string]struct{}{}

	rows, err := s.DB.QueryContext(ctx, `
		SELECT DISTINCT p.code
		FROM permissions p
		JOIN global_role_permissions grp ON grp.permission_id = p.id
		JOIN user_global_roles ugr ON ugr.global_role_id = grp.global_role_id
		WHERE ugr.user_id = ?
		UNION
		SELECT DISTINCT p.code
		FROM permissions p
		JOIN organization_role_permissions orp ON orp.permission_id = p.id
		JOIN user_organization_roles uor ON uor.organization_role_id = orp.organization_role_id
		WHERE uor.user_id = ?
	`, userID, userID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			rows.Close()
			return nil, err
		}
		base[code] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	overrides, err := s.DB.QueryContext(ctx, `
		SELECT p.code, upo.effect
		FROM user_permission_overrides upo
		JOIN permissions p ON p.id = upo.permission_id
		WHERE upo.user_id = ?
	`, userID)
	if err != nil {
		return nil, err
	}
	defer overrides.Close()
	for overrides.Next() {
		var code, effect string
		if err := overrides.Scan(&code, &effect); err != nil {
			return nil, err
		}
		if effect == "deny" {
			delete(base, code)
			continue
		}
		if effect == "allow" {
			base[code] = struct{}{}
		}
	}

	out := make([]string, 0, len(base))
	for k := range base {
		out = append(out, k)
	}
	sortStrings(out)
	return out, nil
}

func (s *Store) SetUserGlobalRoles(ctx context.Context, userID string, roleCodes []string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_global_roles WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, code := range uniqueNormalizedCodes(roleCodes) {
		roleID, err := s.lookupRoleID(ctx, tx, "global_roles", code)
		if err != nil {
			return err
		}
		if roleID == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_global_roles(user_id, global_role_id) VALUES(?, ?)`, userID, roleID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetUserOrganizationRoles(ctx context.Context, userID string, roleCodes []string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_organization_roles WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, code := range uniqueNormalizedCodes(roleCodes) {
		roleID, err := s.lookupRoleID(ctx, tx, "organization_roles", code)
		if err != nil {
			return err
		}
		if roleID == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_organization_roles(user_id, organization_role_id) VALUES(?, ?)`, userID, roleID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetUserPermissionOverrides(ctx context.Context, userID string, overrides []UserPermissionOverrideInput) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_permission_overrides WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, override := range overrides {
		code := strings.TrimSpace(override.PermissionCode)
		effect := strings.ToLower(strings.TrimSpace(override.Effect))
		if code == "" || (effect != "allow" && effect != "deny") {
			continue
		}
		permID, err := s.lookupPermissionID(ctx, tx, code)
		if err != nil {
			return err
		}
		if permID == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO user_permission_overrides(user_id, permission_id, effect)
			VALUES(?, ?, ?)
		`, userID, permID, effect); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetGlobalRolePermissionsByCode(ctx context.Context, roleCode string, permissionCodes []string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	roleID, err := s.lookupRoleID(ctx, tx, "global_roles", roleCode)
	if err != nil {
		return err
	}
	if roleID == "" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM global_role_permissions WHERE global_role_id = ?`, roleID); err != nil {
		return err
	}
	for _, code := range uniqueNormalizedCodes(permissionCodes) {
		permID, err := s.lookupPermissionID(ctx, tx, code)
		if err != nil {
			return err
		}
		if permID == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO global_role_permissions(global_role_id, permission_id) VALUES(?, ?)`, roleID, permID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetOrganizationRolePermissionsByCode(ctx context.Context, roleCode string, permissionCodes []string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	roleID, err := s.lookupRoleID(ctx, tx, "organization_roles", roleCode)
	if err != nil {
		return err
	}
	if roleID == "" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM organization_role_permissions WHERE organization_role_id = ?`, roleID); err != nil {
		return err
	}
	for _, code := range uniqueNormalizedCodes(permissionCodes) {
		permID, err := s.lookupPermissionID(ctx, tx, code)
		if err != nil {
			return err
		}
		if permID == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO organization_role_permissions(organization_role_id, permission_id) VALUES(?, ?)`, roleID, permID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListGlobalRolesCatalog(ctx context.Context) ([]map[string]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT code, label FROM global_roles ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCatalogRows(rows)
}

func (s *Store) ListOrganizationRolesCatalog(ctx context.Context) ([]map[string]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT code, label FROM organization_roles ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCatalogRows(rows)
}

func (s *Store) ListPermissionsCatalog(ctx context.Context) ([]map[string]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT code, label, scope FROM permissions ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]string{}
	for rows.Next() {
		var code, label, scope string
		if err := rows.Scan(&code, &label, &scope); err != nil {
			return nil, err
		}
		out = append(out, map[string]string{"code": code, "label": label, "scope": scope})
	}
	return out, rows.Err()
}

func (s *Store) SaveRefreshSession(ctx context.Context, session RefreshSession) error {
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO refresh_sessions(id, user_id, organization_id, refresh_hash, expires_at, revoked_at, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)
	`, session.ID, session.UserID, session.OrganizationID, session.TokenHash, session.ExpiresAt, nil, session.CreatedAt)
	return err
}

func (s *Store) GetRefreshSessionByID(ctx context.Context, id string) (*RefreshSession, error) {
	var rec RefreshSession
	err := s.DB.QueryRowContext(ctx, `
		SELECT id, user_id, organization_id, refresh_hash, expires_at, revoked_at, created_at
		FROM refresh_sessions WHERE id = ?
	`, id).Scan(&rec.ID, &rec.UserID, &rec.OrganizationID, &rec.TokenHash, &rec.ExpiresAt, &rec.RevokedAt, &rec.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &rec, nil
}

func (s *Store) RotateRefreshSession(ctx context.Context, id, newHash string, expiresAt time.Time) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE refresh_sessions
		SET refresh_hash = ?, expires_at = ?, revoked_at = NULL
		WHERE id = ?
	`, newHash, expiresAt, id)
	return err
}

func (s *Store) RevokeRefreshSession(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE refresh_sessions SET revoked_at = ? WHERE id = ?`, time.Now().UTC(), id)
	return err
}

func (s *Store) GetUserSettings(ctx context.Context, userID string) (*SettingsRecord, error) {
	var record SettingsRecord
	var payload string
	err := s.DB.QueryRowContext(ctx, `
		SELECT version, schema_version, updated_at, settings_json
		FROM user_settings
		WHERE user_id = ?
	`, userID).Scan(&record.Version, &record.SchemaVersion, &record.UpdatedAt, &payload)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	record.Settings = json.RawMessage(payload)
	return &record, nil
}

func (s *Store) SaveUserSettings(ctx context.Context, userID, organizationID string, settings json.RawMessage, schemaVersion int) (*SettingsRecord, error) {
	now := time.Now().UTC()
	payload := strings.TrimSpace(string(settings))
	if payload == "" {
		payload = "{}"
	}
	if schemaVersion <= 0 {
		schemaVersion = 1
	}
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO user_settings(user_id, organization_id, settings_json, version, schema_version, updated_at)
		VALUES(?, ?, ?, 1, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			organization_id = excluded.organization_id,
			settings_json = excluded.settings_json,
			version = user_settings.version + 1,
			schema_version = excluded.schema_version,
			updated_at = excluded.updated_at
	`, userID, organizationID, payload, schemaVersion, now)
	if err != nil {
		return nil, err
	}
	return s.GetUserSettings(ctx, userID)
}

func (s *Store) ResetUserSettings(ctx context.Context, userID, organizationID string) (*SettingsRecord, error) {
	return s.SaveUserSettings(ctx, userID, organizationID, json.RawMessage(`{}`), 1)
}

func (s *Store) lookupRoleID(ctx context.Context, tx *sql.Tx, table, code string) (string, error) {
	query := fmt.Sprintf(`SELECT id FROM %s WHERE code = ?`, table)
	var id string
	err := tx.QueryRowContext(ctx, query, code).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func (s *Store) lookupPermissionID(ctx context.Context, tx *sql.Tx, code string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM permissions WHERE code = ?`, code).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func normalizeStatus(value string) string {
	s := strings.ToLower(strings.TrimSpace(value))
	if s == "inactive" || s == "disabled" {
		return "inactive"
	}
	return "active"
}

func normalizeOrgCode(value string) string {
	code := strings.ToLower(strings.TrimSpace(value))
	code = strings.ReplaceAll(code, " ", "-")
	code = strings.ReplaceAll(code, "_", "-")
	if code == "" {
		return "org-" + uuid.NewString()[:8]
	}
	return code
}

func uniqueNormalizedCodes(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		code := strings.TrimSpace(v)
		if code == "" {
			continue
		}
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		out = append(out, code)
	}
	return out
}

func scanStringRows(rows *sql.Rows) ([]string, error) {
	out := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func scanCatalogRows(rows *sql.Rows) ([]map[string]string, error) {
	out := []map[string]string{}
	for rows.Next() {
		var code, label string
		if err := rows.Scan(&code, &label); err != nil {
			return nil, err
		}
		out = append(out, map[string]string{"code": code, "label": label})
	}
	return out, rows.Err()
}

func sortStrings(values []string) {
	if len(values) < 2 {
		return
	}
	for i := 0; i < len(values)-1; i++ {
		for j := i + 1; j < len(values); j++ {
			if values[j] < values[i] {
				values[i], values[j] = values[j], values[i]
			}
		}
	}
}
