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

// Store wraps the single SQLite database used by the backend.
type Store struct {
	DB *sql.DB
}

// Organization is the persisted tenant record used by app and admin flows.
type Organization struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Code      string    `json:"code"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// User is the persisted account record.
type User struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organizationId"`
	Email          string    `json:"email"`
	PasswordHash   string    `json:"-"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// SettingsRecord stores the raw JSON settings document and its versioning
// metadata.
type SettingsRecord struct {
	Version       int             `json:"version"`
	SchemaVersion int             `json:"schemaVersion"`
	UpdatedAt     time.Time       `json:"updatedAt"`
	Settings      json.RawMessage `json:"settings"`
}

// RefreshSession stores the hashed refresh-token state for one login session.
type RefreshSession struct {
	ID             string
	UserID         string
	OrganizationID string
	SessionType    string
	TokenHash      string
	ExpiresAt      time.Time
	RevokedAt      sql.NullTime
	CreatedAt      time.Time
}

// PasswordResetToken stores one password-reset token and its lifecycle state.
type PasswordResetToken struct {
	ID                string
	UserID            string
	SessionType       string
	TokenHash         string
	ExpiresAt         time.Time
	UsedAt            sql.NullTime
	RequestedByUserID sql.NullString
	CreatedAt         time.Time
}

// AuditLogInput captures the data required to write one audit-log row.
type AuditLogInput struct {
	ActorUserID    string
	OrganizationID string
	Action         string
	TargetType     string
	TargetID       string
	Payload        any
	PayloadJSON    json.RawMessage
}

// UserPermissionOverrideInput represents one allow or deny override for a
// single permission code.
type UserPermissionOverrideInput struct {
	PermissionCode string `json:"permissionCode"`
	Effect         string `json:"effect"`
}

// UpdateUserInput carries the optional user fields accepted by the admin update
// endpoint.
type UpdateUserInput struct {
	Email          *string `json:"email"`
	Status         *string `json:"status"`
	OrganizationID *string `json:"organizationId"`
}

// ActivityEventInput represents one ingested usage event before persistence.
type ActivityEventInput struct {
	EventID    string
	EventKind  string
	SourceMode string
	Provider   string
	Status     string
	OccurredAt time.Time
	MetaJSON   json.RawMessage
}

// ActivityIngestResult reports how many usage events were accepted or treated
// as duplicates.
type ActivityIngestResult struct {
	Accepted   int `json:"accepted"`
	Duplicates int `json:"duplicates"`
}

// ActivityRange identifies the inclusive day window used by activity summaries.
type ActivityRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ActivityTotals aggregates high-level activity counts.
type ActivityTotals struct {
	Transcriptions int `json:"transcriptions"`
	Reports        int `json:"reports"`
}

// ActivityByDayItem breaks activity totals down by day.
type ActivityByDayItem struct {
	Day            string `json:"day"`
	Transcriptions int    `json:"transcriptions"`
	Reports        int    `json:"reports"`
}

// ActivityByUserItem breaks activity totals down by user.
type ActivityByUserItem struct {
	UserID         string `json:"userId"`
	Email          string `json:"email"`
	Transcriptions int    `json:"transcriptions"`
	Reports        int    `json:"reports"`
}

// ActivityBreakdown groups counts by mode and provider for each event family.
type ActivityBreakdown struct {
	TranscriptionsByMode     map[string]int `json:"transcriptionsByMode"`
	TranscriptionsByProvider map[string]int `json:"transcriptionsByProvider"`
	ReportsByMode            map[string]int `json:"reportsByMode"`
	ReportsByProvider        map[string]int `json:"reportsByProvider"`
}

// ActivitySummary is the organization-level activity view used by admin pages.
type ActivitySummary struct {
	OrganizationID string               `json:"organizationId"`
	Range          ActivityRange        `json:"range"`
	Totals         ActivityTotals       `json:"totals"`
	ByDay          []ActivityByDayItem  `json:"byDay"`
	ByUser         []ActivityByUserItem `json:"byUser"`
	Breakdown      ActivityBreakdown    `json:"breakdown"`
}

// UserActivitySummary is the user-level variant of the activity view.
type UserActivitySummary struct {
	User      User                `json:"user"`
	Range     ActivityRange       `json:"range"`
	Totals    ActivityTotals      `json:"totals"`
	ByDay     []ActivityByDayItem `json:"byDay"`
	Breakdown ActivityBreakdown   `json:"breakdown"`
}

const (
	sqliteMaxOpenConns = 4
	sqliteMaxIdleConns = 4
	sqliteBusyTimeout  = 5 * time.Second
)

// Open initializes the SQLite database, applies migrations, and seeds the base
// RBAC catalog.
func Open(ctx context.Context, path string) (*Store, error) {
	logStoreStep(ctx, "open_start", "store", map[string]any{"path": path})
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logStoreStep(ctx, "open_error", "store", map[string]any{"path": path, "error": err})
		return nil, fmt.Errorf("mkdir db dir: %w", err)
	}
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		logStoreStep(ctx, "open_error", "store", map[string]any{"path": path, "error": err})
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(sqliteMaxOpenConns)
	db.SetMaxIdleConns(sqliteMaxIdleConns)
	store := &Store{DB: db}
	if err := store.Migrate(ctx); err != nil {
		logStoreStep(ctx, "open_error", "store", map[string]any{"path": path, "error": err})
		return nil, err
	}
	if err := store.SeedBaseCatalog(ctx); err != nil {
		logStoreStep(ctx, "open_error", "store", map[string]any{"path": path, "error": err})
		return nil, err
	}
	logStoreStep(ctx, "open_success", "store", map[string]any{"path": path})
	return store, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error {
	if s.DB == nil {
		return nil
	}
	return s.DB.Close()
}

// Ping verifies that the database can accept queries.
func (s *Store) Ping(ctx context.Context) error {
	return s.DB.PingContext(ctx)
}

// sqliteDSN enables the pragmas the backend expects on every SQLite connection.
func sqliteDSN(path string) string {
	query := "_pragma=foreign_keys%3dON&_pragma=journal_mode%3dWAL&_pragma=synchronous%3dNORMAL&_pragma=busy_timeout%3d" + fmt.Sprintf("%d", sqliteBusyTimeout.Milliseconds())
	if strings.Contains(path, "?") {
		return path + "&" + query
	}
	return path + "?" + query
}

// Migrate creates every table and index the backend needs.
func (s *Store) Migrate(ctx context.Context) error {
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
			session_type TEXT NOT NULL DEFAULT 'app',
			refresh_hash TEXT NOT NULL,
			expires_at DATETIME NOT NULL,
			revoked_at DATETIME,
			created_at DATETIME NOT NULL,
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
			FOREIGN KEY(organization_id) REFERENCES organizations(id)
		);`,
		`CREATE TABLE IF NOT EXISTS password_reset_tokens (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			session_type TEXT NOT NULL DEFAULT 'app',
			token_hash TEXT NOT NULL UNIQUE,
			expires_at DATETIME NOT NULL,
			used_at DATETIME,
			requested_by_user_id TEXT,
			created_at DATETIME NOT NULL,
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
			FOREIGN KEY(requested_by_user_id) REFERENCES users(id)
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
		`CREATE TABLE IF NOT EXISTS backend_error_events (
			id TEXT PRIMARY KEY,
			trace_id TEXT NOT NULL,
			user_id TEXT,
			organization_id TEXT,
			component TEXT NOT NULL,
			route TEXT NOT NULL,
			step TEXT NOT NULL,
			title TEXT NOT NULL,
			status_code INTEGER,
			duration_ms INTEGER,
			error_message TEXT,
			payload_json TEXT NOT NULL DEFAULT '{}',
			annex_json TEXT NOT NULL DEFAULT '{}',
			recovery_status TEXT,
			created_at DATETIME NOT NULL,
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE SET NULL,
			FOREIGN KEY(organization_id) REFERENCES organizations(id) ON DELETE SET NULL
		);`,
		`CREATE TABLE IF NOT EXISTS performance_events (
			event_id TEXT PRIMARY KEY,
			trace_id TEXT NOT NULL,
			user_id TEXT,
			organization_id TEXT,
			surface TEXT NOT NULL,
			component TEXT NOT NULL,
			task TEXT NOT NULL,
			status TEXT NOT NULL,
			duration_ms INTEGER NOT NULL,
			route TEXT NOT NULL,
			meta_json TEXT NOT NULL DEFAULT '{}',
			occurred_at DATETIME NOT NULL,
			day TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE SET NULL,
			FOREIGN KEY(organization_id) REFERENCES organizations(id) ON DELETE SET NULL
		);`,
		`CREATE TABLE IF NOT EXISTS activity_usage_events (
			event_id TEXT PRIMARY KEY,
			organization_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			event_kind TEXT NOT NULL,
			source_mode TEXT NOT NULL,
			provider TEXT NOT NULL,
			status TEXT NOT NULL,
			occurred_at DATETIME NOT NULL,
			day TEXT NOT NULL,
			meta_json TEXT,
			created_at DATETIME NOT NULL,
			FOREIGN KEY(organization_id) REFERENCES organizations(id) ON DELETE CASCADE,
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS meeting_finalize_operations (
			operation_id TEXT PRIMARY KEY,
			organization_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			status TEXT NOT NULL,
			status_code INTEGER NOT NULL,
			response_json TEXT,
			error_message TEXT,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			completed_at DATETIME,
			terminal_expires_at DATETIME,
			FOREIGN KEY(organization_id) REFERENCES organizations(id) ON DELETE CASCADE,
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS demeter_audio_transcription_operations (
			operation_id TEXT PRIMARY KEY,
			organization_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			queue_id INTEGER NOT NULL DEFAULT 0,
			queue_payload_json TEXT,
			status TEXT NOT NULL,
			stage TEXT NOT NULL,
			chunk_index INTEGER NOT NULL DEFAULT 0,
			chunk_count INTEGER NOT NULL DEFAULT 0,
			progress REAL NOT NULL DEFAULT 0,
			response_json TEXT,
			last_error TEXT,
			status_code INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			finished_at DATETIME,
			FOREIGN KEY(organization_id) REFERENCES organizations(id) ON DELETE CASCADE,
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS demeter_audio_queue_settings (
			id INTEGER PRIMARY KEY CHECK(id = 1),
			parallelism INTEGER NOT NULL DEFAULT 1,
			updated_at DATETIME NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_users_org ON users(organization_id);`,
		`CREATE INDEX IF NOT EXISTS idx_refresh_user ON refresh_sessions(user_id);`,
		`CREATE INDEX IF NOT EXISTS idx_password_reset_user ON password_reset_tokens(user_id);`,
		`CREATE INDEX IF NOT EXISTS idx_password_reset_hash ON password_reset_tokens(token_hash);`,
		`CREATE INDEX IF NOT EXISTS idx_activity_org_day ON activity_usage_events(organization_id, day);`,
		`CREATE INDEX IF NOT EXISTS idx_activity_user_day ON activity_usage_events(user_id, day);`,
		`CREATE INDEX IF NOT EXISTS idx_activity_kind_org_day ON activity_usage_events(event_kind, organization_id, day);`,
		`CREATE INDEX IF NOT EXISTS idx_activity_provider_org_day ON activity_usage_events(provider, organization_id, day);`,
		`CREATE INDEX IF NOT EXISTS idx_backend_error_created_at ON backend_error_events(created_at);`,
		`CREATE INDEX IF NOT EXISTS idx_backend_error_org_created_at ON backend_error_events(organization_id, created_at);`,
		`CREATE INDEX IF NOT EXISTS idx_backend_error_user_created_at ON backend_error_events(user_id, created_at);`,
		`CREATE INDEX IF NOT EXISTS idx_backend_error_component_created_at ON backend_error_events(component, created_at);`,
		`CREATE INDEX IF NOT EXISTS idx_backend_error_route_created_at ON backend_error_events(route, created_at);`,
		`CREATE INDEX IF NOT EXISTS idx_backend_error_trace_id ON backend_error_events(trace_id);`,
		`CREATE INDEX IF NOT EXISTS idx_performance_occurred_at ON performance_events(occurred_at);`,
		`CREATE INDEX IF NOT EXISTS idx_performance_user_occurred_at ON performance_events(user_id, occurred_at);`,
		`CREATE INDEX IF NOT EXISTS idx_performance_org_occurred_at ON performance_events(organization_id, occurred_at);`,
		`CREATE INDEX IF NOT EXISTS idx_performance_org_user_occurred_at ON performance_events(organization_id, user_id, occurred_at);`,
		`CREATE INDEX IF NOT EXISTS idx_performance_surface_occurred_at ON performance_events(surface, occurred_at);`,
		`CREATE INDEX IF NOT EXISTS idx_performance_component_occurred_at ON performance_events(component, occurred_at);`,
		`CREATE INDEX IF NOT EXISTS idx_performance_task_occurred_at ON performance_events(task, occurred_at);`,
		`CREATE INDEX IF NOT EXISTS idx_performance_route_occurred_at ON performance_events(route, occurred_at);`,
		`CREATE INDEX IF NOT EXISTS idx_performance_trace_id ON performance_events(trace_id);`,
		`CREATE INDEX IF NOT EXISTS idx_meeting_finalize_operations_owner ON meeting_finalize_operations(organization_id, user_id);`,
		`CREATE INDEX IF NOT EXISTS idx_meeting_finalize_operations_pending_created ON meeting_finalize_operations(status, created_at);`,
		`CREATE INDEX IF NOT EXISTS idx_meeting_finalize_operations_status ON meeting_finalize_operations(status, terminal_expires_at);`,
		`CREATE INDEX IF NOT EXISTS idx_demeter_audio_transcription_operations_owner ON demeter_audio_transcription_operations(organization_id, user_id);`,
		`CREATE INDEX IF NOT EXISTS idx_demeter_audio_transcription_operations_status ON demeter_audio_transcription_operations(status, updated_at);`,
	}
	logStoreStep(ctx, "migrate_start", "schema", map[string]any{"statement_count": len(stmts)})
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		logStoreStep(ctx, "migrate_error", "schema", map[string]any{"error": err})
		return err
	}
	defer rollbackTx(tx)

	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			logStoreStep(ctx, "migrate_error", "schema", map[string]any{"error": err})
			return err
		}
	}

	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_performance_day;`,
		`DROP INDEX IF EXISTS idx_performance_org_day;`,
		`DROP INDEX IF EXISTS idx_performance_surface_day;`,
		`DROP INDEX IF EXISTS idx_performance_component_day;`,
		`DROP INDEX IF EXISTS idx_performance_task_day;`,
		`DROP INDEX IF EXISTS idx_performance_route_day;`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			logStoreStep(ctx, "migrate_error", "schema", map[string]any{"error": err})
			return err
		}
	}

	if err := ensureColumnExists(ctx, tx, "refresh_sessions", "session_type", `ALTER TABLE refresh_sessions ADD COLUMN session_type TEXT NOT NULL DEFAULT 'app'`); err != nil {
		logStoreStep(ctx, "migrate_error", "schema", map[string]any{"error": err})
		return err
	}
	if err := ensureColumnExists(ctx, tx, "backend_error_events", "annex_json", `ALTER TABLE backend_error_events ADD COLUMN annex_json TEXT NOT NULL DEFAULT '{}'`); err != nil {
		logStoreStep(ctx, "migrate_error", "schema", map[string]any{"error": err})
		return err
	}
	if err := ensureColumnExists(ctx, tx, "backend_error_events", "recovery_status", `ALTER TABLE backend_error_events ADD COLUMN recovery_status TEXT`); err != nil {
		logStoreStep(ctx, "migrate_error", "schema", map[string]any{"error": err})
		return err
	}
	if err := ensureColumnMissing(ctx, tx, "demeter_audio_transcription_operations", "partial_text", `ALTER TABLE demeter_audio_transcription_operations DROP COLUMN partial_text`); err != nil {
		logStoreStep(ctx, "migrate_error", "schema", map[string]any{"error": err})
		return err
	}
	if err := ensureColumnExists(ctx, tx, "demeter_audio_transcription_operations", "queue_id", `ALTER TABLE demeter_audio_transcription_operations ADD COLUMN queue_id INTEGER NOT NULL DEFAULT 0`); err != nil {
		logStoreStep(ctx, "migrate_error", "schema", map[string]any{"error": err})
		return err
	}
	if err := ensureColumnExists(ctx, tx, "demeter_audio_transcription_operations", "queue_payload_json", `ALTER TABLE demeter_audio_transcription_operations ADD COLUMN queue_payload_json TEXT`); err != nil {
		logStoreStep(ctx, "migrate_error", "schema", map[string]any{"error": err})
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_demeter_audio_transcription_operations_queue ON demeter_audio_transcription_operations(queue_id, status, created_at);`); err != nil {
		logStoreStep(ctx, "migrate_error", "schema", map[string]any{"error": err})
		return err
	}

	if err := tx.Commit(); err != nil {
		logStoreStep(ctx, "migrate_error", "schema", map[string]any{"error": err})
		return err
	}
	logStoreStep(ctx, "migrate_success", "schema", map[string]any{"statement_count": len(stmts)})
	return nil
}

// SeedBaseCatalog inserts the seed permissions and roles that the backend
// expects to exist on every database.
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
		{"provider.cloud.whisper", "Provider cloud Whisper", "provider_cloud"},
		{"provider.cloud.mistral", "Provider cloud Mistral", "provider_cloud"},
		{"provider.cloud.demeter_sante", "Provider cloud Demeter Sante", "provider_cloud"},
		{"provider.llm.huggingface", "Provider llm Hugging Face", "provider_llm"},
		{"provider.llm.mistral", "Provider llm Mistral", "provider_llm"},
		{"provider.llm.demeter_sante", "Provider llm Demeter Sante", "provider_llm"},
	}
	logStoreStep(ctx, "seed_start", "catalog", map[string]any{
		"permission_count":  len(permissions),
		"global_role_count": 2,
		"org_role_count":    2,
	})
	for _, p := range permissions {
		if err := s.upsertPermission(ctx, p.Code, p.Label, p.Scope); err != nil {
			logStoreStep(ctx, "seed_error", "catalog", map[string]any{"error": err})
			return err
		}
	}
	if err := s.upsertGlobalRole(ctx, "super_admin", "Super Admin"); err != nil {
		logStoreStep(ctx, "seed_error", "catalog", map[string]any{"error": err})
		return err
	}
	if err := s.upsertGlobalRole(ctx, "user", "User"); err != nil {
		logStoreStep(ctx, "seed_error", "catalog", map[string]any{"error": err})
		return err
	}
	if err := s.upsertOrganizationRole(ctx, "org_admin", "Organization Admin"); err != nil {
		logStoreStep(ctx, "seed_error", "catalog", map[string]any{"error": err})
		return err
	}
	if err := s.upsertOrganizationRole(ctx, "org_member", "Organization Member"); err != nil {
		logStoreStep(ctx, "seed_error", "catalog", map[string]any{"error": err})
		return err
	}

	allCodes := make([]string, 0, len(permissions))
	for _, p := range permissions {
		allCodes = append(allCodes, p.Code)
	}
	if err := s.SetGlobalRolePermissionsByCode(ctx, "super_admin", allCodes); err != nil {
		logStoreStep(ctx, "seed_error", "catalog", map[string]any{"error": err})
		return err
	}
	if err := s.SetGlobalRolePermissionsByCode(ctx, "user", []string{
		"feature.localupload",
		"feature.cloudupload",
		"feature.llmlocal",
		"feature.llmapi",
		"feature.settings",
		"feature.telemetry",
		"provider.cloud.whisper",
		"provider.cloud.mistral",
		"provider.cloud.demeter_sante",
		"provider.llm.huggingface",
		"provider.llm.mistral",
		"provider.llm.demeter_sante",
	}); err != nil {
		logStoreStep(ctx, "seed_error", "catalog", map[string]any{"error": err})
		return err
	}
	if err := s.SetOrganizationRolePermissionsByCode(ctx, "org_admin", allCodes); err != nil {
		logStoreStep(ctx, "seed_error", "catalog", map[string]any{"error": err})
		return err
	}
	if err := s.SetOrganizationRolePermissionsByCode(ctx, "org_member", []string{
		"feature.localupload",
		"feature.cloudupload",
		"feature.llmlocal",
		"feature.llmapi",
		"feature.settings",
		"feature.telemetry",
		"provider.cloud.whisper",
		"provider.cloud.mistral",
		"provider.cloud.demeter_sante",
		"provider.llm.huggingface",
		"provider.llm.mistral",
		"provider.llm.demeter_sante",
	}); err != nil {
		logStoreStep(ctx, "seed_error", "catalog", map[string]any{"error": err})
		return err
	}
	logStoreStep(ctx, "seed_success", "catalog", map[string]any{"permission_count": len(permissions)})
	return nil
}

// EnsureBootstrap creates the initial organization and admin account when the
// database is still empty.
func (s *Store) EnsureBootstrap(ctx context.Context, adminEmail, passwordHash, orgName string) error {
	logStoreStep(ctx, "bootstrap_start", "bootstrap", map[string]any{
		"admin_email_present": strings.TrimSpace(adminEmail) != "",
		"password_present":    strings.TrimSpace(passwordHash) != "",
		"org_name_present":    strings.TrimSpace(orgName) != "",
	})
	var count int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		logStoreStep(ctx, "bootstrap_error", "bootstrap", map[string]any{"error": err})
		return err
	}
	if count > 0 {
		logStoreStep(ctx, "bootstrap_skipped", "bootstrap", map[string]any{"reason": "existing_users", "user_count": count})
		return nil
	}
	if strings.TrimSpace(adminEmail) == "" || strings.TrimSpace(passwordHash) == "" {
		logStoreStep(ctx, "bootstrap_skipped", "bootstrap", map[string]any{"reason": "missing_credentials"})
		return nil
	}
	org, err := s.CreateOrganization(ctx, strings.TrimSpace(orgName), normalizeOrgCode(orgName), "active")
	if err != nil {
		logStoreStep(ctx, "bootstrap_error", "bootstrap", map[string]any{"error": err})
		return err
	}
	user, err := s.CreateUser(ctx, org.ID, strings.ToLower(strings.TrimSpace(adminEmail)), passwordHash, "active")
	if err != nil {
		logStoreStep(ctx, "bootstrap_error", "bootstrap", map[string]any{"error": err, "organization_id": org.ID})
		return err
	}
	if err := s.SetUserGlobalRoles(ctx, user.ID, []string{"super_admin", "user"}); err != nil {
		logStoreStep(ctx, "bootstrap_error", "bootstrap", map[string]any{"error": err, "user_id": user.ID})
		return err
	}
	if err := s.SetUserOrganizationRoles(ctx, user.ID, []string{"org_admin", "org_member"}); err != nil {
		logStoreStep(ctx, "bootstrap_error", "bootstrap", map[string]any{"error": err, "user_id": user.ID})
		return err
	}
	logStoreStep(ctx, "bootstrap_success", "bootstrap", map[string]any{"organization_id": org.ID, "user_id": user.ID})
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

// ListOrganizations returns every organization currently stored in the
// database.
func (s *Store) ListOrganizations(ctx context.Context) ([]Organization, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, name, code, status, created_at, updated_at
		FROM organizations
		ORDER BY name ASC
	`)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)
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

// GetOrganizationByID loads a single organization by primary key.
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

// CreateOrganization inserts a new organization record with normalized values.
func (s *Store) CreateOrganization(ctx context.Context, name, code, status string) (*Organization, error) {
	logStoreStep(ctx, "create_start", "organization", map[string]any{
		"status": normalizeStatus(status),
		"code":   normalizeOrgCode(code),
	})
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
		logStoreStep(ctx, "create_error", "organization", map[string]any{"error": err, "code": org.Code})
		return nil, err
	}
	logStoreStep(ctx, "create_success", "organization", map[string]any{"organization_id": org.ID, "code": org.Code, "status": org.Status})
	return org, nil
}

// UpdateOrganization applies partial updates to an organization and returns the
// stored row after the change.
func (s *Store) UpdateOrganization(ctx context.Context, id string, name, code, status *string) (*Organization, error) {
	logStoreStep(ctx, "update_start", "organization", map[string]any{
		"organization_id": id,
		"has_name":        name != nil,
		"has_code":        code != nil,
		"has_status":      status != nil,
	})
	current, err := s.GetOrganizationByID(ctx, id)
	if err != nil || current == nil {
		if err != nil {
			logStoreStep(ctx, "update_error", "organization", map[string]any{"error": err, "organization_id": id})
		} else {
			logStoreStep(ctx, "update_missing", "organization", map[string]any{"organization_id": id})
		}
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
		logStoreStep(ctx, "update_error", "organization", map[string]any{"error": err, "organization_id": id})
		return nil, err
	}
	logStoreStep(ctx, "update_success", "organization", map[string]any{"organization_id": current.ID, "code": current.Code, "status": current.Status})
	return current, nil
}

// FindUserByEmail locates a user account without exposing password hashes to
// callers.
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

// GetUserByID loads one user account by primary key.
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

// ListUsersByOrganization returns the users that belong to a single tenant.
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
	defer closeRows(rows)
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

// CreateUser inserts a new account and leaves role assignment to the caller.
func (s *Store) CreateUser(ctx context.Context, organizationID, email, passwordHash, status string) (*User, error) {
	logStoreStep(ctx, "create_start", "user", map[string]any{
		"organization_id": strings.TrimSpace(organizationID),
		"status":          normalizeStatus(status),
	})
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
		logStoreStep(ctx, "create_error", "user", map[string]any{"error": err, "organization_id": u.OrganizationID})
		return nil, err
	}
	logStoreStep(ctx, "create_success", "user", map[string]any{"user_id": u.ID, "organization_id": u.OrganizationID, "status": u.Status})
	return u, nil
}

// CreateUserWithRoles inserts a user and attaches the requested catalog roles in
// the same transaction.
func (s *Store) CreateUserWithRoles(ctx context.Context, organizationID, email, passwordHash, status string, globalRoleCodes, organizationRoleCodes []string) (*User, error) {
	logStoreStep(ctx, "create_start", "user_roles", map[string]any{
		"organization_id":         strings.TrimSpace(organizationID),
		"global_role_count":       len(globalRoleCodes),
		"organization_role_count": len(organizationRoleCodes),
		"status":                  normalizeStatus(status),
	})
	now := time.Now().UTC()
	u := &User{
		ID:             uuid.NewString(),
		OrganizationID: strings.TrimSpace(organizationID),
		Email:          strings.ToLower(strings.TrimSpace(email)),
		PasswordHash:   passwordHash,
		Status:         normalizeStatus(status),
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		logStoreStep(ctx, "create_error", "user_roles", map[string]any{"error": err, "organization_id": u.OrganizationID})
		return nil, err
	}
	defer rollbackTx(tx)

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO users(id, organization_id, email, password_hash, status, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)
	`, u.ID, u.OrganizationID, u.Email, u.PasswordHash, u.Status, u.CreatedAt, u.UpdatedAt); err != nil {
		logStoreStep(ctx, "create_error", "user_roles", map[string]any{"error": err, "organization_id": u.OrganizationID})
		return nil, err
	}
	if err := s.setUserGlobalRolesTx(ctx, tx, u.ID, globalRoleCodes); err != nil {
		logStoreStep(ctx, "create_error", "user_roles", map[string]any{"error": err, "organization_id": u.OrganizationID})
		return nil, err
	}
	if err := s.setUserOrganizationRolesTx(ctx, tx, u.ID, organizationRoleCodes); err != nil {
		logStoreStep(ctx, "create_error", "user_roles", map[string]any{"error": err, "organization_id": u.OrganizationID})
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		logStoreStep(ctx, "create_error", "user_roles", map[string]any{"error": err, "organization_id": u.OrganizationID})
		return nil, err
	}
	logStoreStep(ctx, "create_success", "user_roles", map[string]any{"user_id": u.ID, "organization_id": u.OrganizationID})
	return u, nil
}

// CreateUserWithRolesAndOverrides inserts a user and applies both role and
// permission override state atomically.
func (s *Store) CreateUserWithRolesAndOverrides(ctx context.Context, organizationID, email, passwordHash, status string, globalRoleCodes, organizationRoleCodes []string, overrides []UserPermissionOverrideInput) (*User, error) {
	logStoreStep(ctx, "create_start", "user_roles_overrides", map[string]any{
		"organization_id":         strings.TrimSpace(organizationID),
		"global_role_count":       len(globalRoleCodes),
		"organization_role_count": len(organizationRoleCodes),
		"override_count":          len(overrides),
		"status":                  normalizeStatus(status),
	})
	now := time.Now().UTC()
	u := &User{
		ID:             uuid.NewString(),
		OrganizationID: strings.TrimSpace(organizationID),
		Email:          strings.ToLower(strings.TrimSpace(email)),
		PasswordHash:   passwordHash,
		Status:         normalizeStatus(status),
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		logStoreStep(ctx, "create_error", "user_roles_overrides", map[string]any{"error": err, "organization_id": u.OrganizationID})
		return nil, err
	}
	defer rollbackTx(tx)

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO users(id, organization_id, email, password_hash, status, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)
	`, u.ID, u.OrganizationID, u.Email, u.PasswordHash, u.Status, u.CreatedAt, u.UpdatedAt); err != nil {
		logStoreStep(ctx, "create_error", "user_roles_overrides", map[string]any{"error": err, "organization_id": u.OrganizationID})
		return nil, err
	}
	if err := s.setUserGlobalRolesTx(ctx, tx, u.ID, globalRoleCodes); err != nil {
		logStoreStep(ctx, "create_error", "user_roles_overrides", map[string]any{"error": err, "organization_id": u.OrganizationID})
		return nil, err
	}
	if err := s.setUserOrganizationRolesTx(ctx, tx, u.ID, organizationRoleCodes); err != nil {
		logStoreStep(ctx, "create_error", "user_roles_overrides", map[string]any{"error": err, "organization_id": u.OrganizationID})
		return nil, err
	}
	if _, err := s.setUserPermissionOverridesTx(ctx, tx, u.ID, overrides); err != nil {
		logStoreStep(ctx, "create_error", "user_roles_overrides", map[string]any{"error": err, "organization_id": u.OrganizationID})
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		logStoreStep(ctx, "create_error", "user_roles_overrides", map[string]any{"error": err, "organization_id": u.OrganizationID})
		return nil, err
	}
	logStoreStep(ctx, "create_success", "user_roles_overrides", map[string]any{"user_id": u.ID, "organization_id": u.OrganizationID})
	return u, nil
}

// UpdateUser applies partial account changes and returns the refreshed record.
func (s *Store) UpdateUser(ctx context.Context, userID string, input UpdateUserInput) (*User, error) {
	logStoreStep(ctx, "update_start", "user", map[string]any{
		"user_id":             strings.TrimSpace(userID),
		"has_email":           input.Email != nil,
		"has_status":          input.Status != nil,
		"has_organization_id": input.OrganizationID != nil,
	})
	current, err := s.GetUserByID(ctx, userID)
	if err != nil || current == nil {
		if err != nil {
			logStoreStep(ctx, "update_error", "user", map[string]any{"error": err, "user_id": userID})
		} else {
			logStoreStep(ctx, "update_missing", "user", map[string]any{"user_id": userID})
		}
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
		logStoreStep(ctx, "update_error", "user", map[string]any{"error": err, "user_id": userID})
		return nil, err
	}
	logStoreStep(ctx, "update_success", "user", map[string]any{"user_id": current.ID, "organization_id": current.OrganizationID, "status": current.Status})
	return current, nil
}

// UpdateUserPassword replaces the stored password hash for one account.
func (s *Store) UpdateUserPassword(ctx context.Context, userID, passwordHash string) error {
	logStoreStep(ctx, "password_update_start", "user", map[string]any{"user_id": strings.TrimSpace(userID)})
	_, err := s.DB.ExecContext(ctx, `
		UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?
	`, passwordHash, time.Now().UTC(), userID)
	if err != nil {
		logStoreStep(ctx, "password_update_error", "user", map[string]any{"error": err, "user_id": strings.TrimSpace(userID)})
		return err
	}
	logStoreStep(ctx, "password_update_success", "user", map[string]any{"user_id": strings.TrimSpace(userID)})
	return err
}

// ChangeUserPassword updates the password and clears state that depends on the
// previous secret.
func (s *Store) ChangeUserPassword(ctx context.Context, userID, passwordHash string) error {
	logStoreStep(ctx, "password_change_start", "user", map[string]any{"user_id": strings.TrimSpace(userID)})
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		logStoreStep(ctx, "password_change_error", "user", map[string]any{"error": err, "user_id": strings.TrimSpace(userID)})
		return err
	}
	defer rollbackTx(tx)

	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?
	`, passwordHash, now, userID); err != nil {
		logStoreStep(ctx, "password_change_error", "user", map[string]any{"error": err, "user_id": strings.TrimSpace(userID)})
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE refresh_sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL
	`, now, userID); err != nil {
		logStoreStep(ctx, "password_change_error", "user", map[string]any{"error": err, "user_id": strings.TrimSpace(userID)})
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE password_reset_tokens SET used_at = ? WHERE user_id = ? AND used_at IS NULL
	`, now, userID); err != nil {
		logStoreStep(ctx, "password_change_error", "user", map[string]any{"error": err, "user_id": strings.TrimSpace(userID)})
		return err
	}
	if err := tx.Commit(); err != nil {
		logStoreStep(ctx, "password_change_error", "user", map[string]any{"error": err, "user_id": strings.TrimSpace(userID)})
		return err
	}
	logStoreStep(ctx, "password_change_success", "user", map[string]any{"user_id": strings.TrimSpace(userID)})
	return nil
}

// IsUserInOrganization reports whether the user belongs to the given tenant.
func (s *Store) IsUserInOrganization(ctx context.Context, userID, organizationID string) (bool, error) {
	var count int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ? AND organization_id = ?`, userID, organizationID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetGlobalRoleCodesByUser returns the global roles assigned to one user.
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
	defer closeRows(rows)
	return scanStringRows(rows)
}

// GetOrganizationRoleCodesByUser returns the organization-scoped roles
// assigned to one user.
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
	defer closeRows(rows)
	return scanStringRows(rows)
}

// ResolveEffectivePermissions computes the union of catalog permissions,
// roles, and user-level overrides for one account.
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
			closeRows(rows)
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
	defer closeRows(overrides)
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

// SetUserGlobalRoles replaces the user's global role assignments.
func (s *Store) SetUserGlobalRoles(ctx context.Context, userID string, roleCodes []string) error {
	logStoreStep(ctx, "update_start", "user_global_roles", map[string]any{
		"user_id":    strings.TrimSpace(userID),
		"role_count": len(roleCodes),
	})
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		logStoreStep(ctx, "update_error", "user_global_roles", map[string]any{"error": err, "user_id": strings.TrimSpace(userID)})
		return err
	}
	defer rollbackTx(tx)
	if err := s.setUserGlobalRolesTx(ctx, tx, userID, roleCodes); err != nil {
		logStoreStep(ctx, "update_error", "user_global_roles", map[string]any{"error": err, "user_id": strings.TrimSpace(userID)})
		return err
	}
	if err := tx.Commit(); err != nil {
		logStoreStep(ctx, "update_error", "user_global_roles", map[string]any{"error": err, "user_id": strings.TrimSpace(userID)})
		return err
	}
	logStoreStep(ctx, "update_success", "user_global_roles", map[string]any{
		"user_id":    strings.TrimSpace(userID),
		"role_count": len(roleCodes),
	})
	return nil
}

// setUserGlobalRolesTx performs the same update inside an existing transaction.
func (s *Store) setUserGlobalRolesTx(ctx context.Context, tx *sql.Tx, userID string, roleCodes []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_global_roles WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, code := range uniqueNormalizedCodes(roleCodes) {
		roleID, err := s.lookupGlobalRoleID(ctx, tx, code)
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
	return nil
}

// SetUserOrganizationRoles replaces the user's organization-scoped roles.
func (s *Store) SetUserOrganizationRoles(ctx context.Context, userID string, roleCodes []string) error {
	logStoreStep(ctx, "update_start", "user_org_roles", map[string]any{
		"user_id":    strings.TrimSpace(userID),
		"role_count": len(roleCodes),
	})
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		logStoreStep(ctx, "update_error", "user_org_roles", map[string]any{"error": err, "user_id": strings.TrimSpace(userID)})
		return err
	}
	defer rollbackTx(tx)
	if err := s.setUserOrganizationRolesTx(ctx, tx, userID, roleCodes); err != nil {
		logStoreStep(ctx, "update_error", "user_org_roles", map[string]any{"error": err, "user_id": strings.TrimSpace(userID)})
		return err
	}
	if err := tx.Commit(); err != nil {
		logStoreStep(ctx, "update_error", "user_org_roles", map[string]any{"error": err, "user_id": strings.TrimSpace(userID)})
		return err
	}
	logStoreStep(ctx, "update_success", "user_org_roles", map[string]any{
		"user_id":    strings.TrimSpace(userID),
		"role_count": len(roleCodes),
	})
	return nil
}

// setUserOrganizationRolesTx performs the same update inside an existing
// transaction.
func (s *Store) setUserOrganizationRolesTx(ctx context.Context, tx *sql.Tx, userID string, roleCodes []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_organization_roles WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, code := range uniqueNormalizedCodes(roleCodes) {
		roleID, err := s.lookupOrganizationRoleID(ctx, tx, code)
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
	return nil
}

// SetUserPermissionOverrides replaces all explicit allow and deny overrides for
// one user.
func (s *Store) SetUserPermissionOverrides(ctx context.Context, userID string, overrides []UserPermissionOverrideInput) error {
	normalizedOverrides := NormalizePermissionOverrideInputs(overrides)
	logStoreStep(ctx, "update_start", "user_overrides", map[string]any{
		"user_id":        strings.TrimSpace(userID),
		"override_count": len(normalizedOverrides),
	})
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		logStoreStep(ctx, "update_error", "user_overrides", map[string]any{"error": err, "user_id": strings.TrimSpace(userID)})
		return err
	}
	defer rollbackTx(tx)
	appliedCount, err := s.setUserPermissionOverridesTx(ctx, tx, userID, normalizedOverrides)
	if err != nil {
		logStoreStep(ctx, "update_error", "user_overrides", map[string]any{"error": err, "user_id": strings.TrimSpace(userID)})
		return err
	}
	if err := tx.Commit(); err != nil {
		logStoreStep(ctx, "update_error", "user_overrides", map[string]any{"error": err, "user_id": strings.TrimSpace(userID)})
		return err
	}
	logStoreStep(ctx, "update_success", "user_overrides", map[string]any{
		"user_id":        strings.TrimSpace(userID),
		"override_count": len(normalizedOverrides),
		"applied_count":  appliedCount,
	})
	return nil
}

// setUserPermissionOverridesTx writes the override rows inside a transaction
// and returns how many records were inserted.
func (s *Store) setUserPermissionOverridesTx(ctx context.Context, tx *sql.Tx, userID string, overrides []UserPermissionOverrideInput) (int, error) {
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_permission_overrides WHERE user_id = ?`, userID); err != nil {
		return 0, err
	}
	appliedCount := 0
	for _, override := range overrides {
		permID, err := s.lookupPermissionID(ctx, tx, override.PermissionCode)
		if err != nil {
			return 0, err
		}
		if permID == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO user_permission_overrides(user_id, permission_id, effect)
			VALUES(?, ?, ?)
		`, userID, permID, override.Effect); err != nil {
			return 0, err
		}
		appliedCount++
	}
	return appliedCount, nil
}

// SetGlobalRolePermissionsByCode replaces the permissions granted by one
// global role.
func (s *Store) SetGlobalRolePermissionsByCode(ctx context.Context, roleCode string, permissionCodes []string) error {
	logStoreStep(ctx, "update_start", "global_role_permissions", map[string]any{
		"role_code":        strings.TrimSpace(roleCode),
		"permission_count": len(permissionCodes),
	})
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		logStoreStep(ctx, "update_error", "global_role_permissions", map[string]any{"error": err, "role_code": strings.TrimSpace(roleCode)})
		return err
	}
	defer rollbackTx(tx)
	roleID, err := s.lookupGlobalRoleID(ctx, tx, roleCode)
	if err != nil {
		logStoreStep(ctx, "update_error", "global_role_permissions", map[string]any{"error": err, "role_code": strings.TrimSpace(roleCode)})
		return err
	}
	if roleID == "" {
		logStoreStep(ctx, "update_skipped", "global_role_permissions", map[string]any{"role_code": strings.TrimSpace(roleCode), "reason": "missing_role"})
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM global_role_permissions WHERE global_role_id = ?`, roleID); err != nil {
		logStoreStep(ctx, "update_error", "global_role_permissions", map[string]any{"error": err, "role_code": strings.TrimSpace(roleCode)})
		return err
	}
	mappedCount := 0
	for _, code := range uniqueNormalizedCodes(permissionCodes) {
		permID, err := s.lookupPermissionID(ctx, tx, code)
		if err != nil {
			logStoreStep(ctx, "update_error", "global_role_permissions", map[string]any{"error": err, "role_code": strings.TrimSpace(roleCode)})
			return err
		}
		if permID == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO global_role_permissions(global_role_id, permission_id) VALUES(?, ?)`, roleID, permID); err != nil {
			logStoreStep(ctx, "update_error", "global_role_permissions", map[string]any{"error": err, "role_code": strings.TrimSpace(roleCode)})
			return err
		}
		mappedCount++
	}
	if err := tx.Commit(); err != nil {
		logStoreStep(ctx, "update_error", "global_role_permissions", map[string]any{"error": err, "role_code": strings.TrimSpace(roleCode)})
		return err
	}
	logStoreStep(ctx, "update_success", "global_role_permissions", map[string]any{
		"role_code":        strings.TrimSpace(roleCode),
		"permission_count": len(permissionCodes),
		"mapped_count":     mappedCount,
	})
	return nil
}

// SetOrganizationRolePermissionsByCode replaces the permissions granted by one
// organization role.
func (s *Store) SetOrganizationRolePermissionsByCode(ctx context.Context, roleCode string, permissionCodes []string) error {
	logStoreStep(ctx, "update_start", "org_role_permissions", map[string]any{
		"role_code":        strings.TrimSpace(roleCode),
		"permission_count": len(permissionCodes),
	})
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		logStoreStep(ctx, "update_error", "org_role_permissions", map[string]any{"error": err, "role_code": strings.TrimSpace(roleCode)})
		return err
	}
	defer rollbackTx(tx)
	roleID, err := s.lookupOrganizationRoleID(ctx, tx, roleCode)
	if err != nil {
		logStoreStep(ctx, "update_error", "org_role_permissions", map[string]any{"error": err, "role_code": strings.TrimSpace(roleCode)})
		return err
	}
	if roleID == "" {
		logStoreStep(ctx, "update_skipped", "org_role_permissions", map[string]any{"role_code": strings.TrimSpace(roleCode), "reason": "missing_role"})
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM organization_role_permissions WHERE organization_role_id = ?`, roleID); err != nil {
		logStoreStep(ctx, "update_error", "org_role_permissions", map[string]any{"error": err, "role_code": strings.TrimSpace(roleCode)})
		return err
	}
	mappedCount := 0
	for _, code := range uniqueNormalizedCodes(permissionCodes) {
		permID, err := s.lookupPermissionID(ctx, tx, code)
		if err != nil {
			logStoreStep(ctx, "update_error", "org_role_permissions", map[string]any{"error": err, "role_code": strings.TrimSpace(roleCode)})
			return err
		}
		if permID == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO organization_role_permissions(organization_role_id, permission_id) VALUES(?, ?)`, roleID, permID); err != nil {
			logStoreStep(ctx, "update_error", "org_role_permissions", map[string]any{"error": err, "role_code": strings.TrimSpace(roleCode)})
			return err
		}
		mappedCount++
	}
	if err := tx.Commit(); err != nil {
		logStoreStep(ctx, "update_error", "org_role_permissions", map[string]any{"error": err, "role_code": strings.TrimSpace(roleCode)})
		return err
	}
	logStoreStep(ctx, "update_success", "org_role_permissions", map[string]any{
		"role_code":        strings.TrimSpace(roleCode),
		"permission_count": len(permissionCodes),
		"mapped_count":     mappedCount,
	})
	return nil
}

// ListGlobalRolesCatalog returns the seeded global role catalog.
func (s *Store) ListGlobalRolesCatalog(ctx context.Context) ([]map[string]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT code, label FROM global_roles ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)
	return scanCatalogRows(rows)
}

// ListOrganizationRolesCatalog returns the seeded organization role catalog.
func (s *Store) ListOrganizationRolesCatalog(ctx context.Context) ([]map[string]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT code, label FROM organization_roles ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)
	return scanCatalogRows(rows)
}

// ListPermissionsCatalog returns the seeded permission catalog.
func (s *Store) ListPermissionsCatalog(ctx context.Context) ([]map[string]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT code, label, scope FROM permissions ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)
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

// SaveRefreshSession stores a refresh-session hash and its expiry.
func (s *Store) SaveRefreshSession(ctx context.Context, session RefreshSession) error {
	if strings.TrimSpace(session.SessionType) == "" {
		session.SessionType = "app"
	}
	logStoreStep(ctx, "save_start", "refresh_session", map[string]any{
		"session_id":      strings.TrimSpace(session.ID),
		"user_id":         strings.TrimSpace(session.UserID),
		"organization_id": strings.TrimSpace(session.OrganizationID),
		"session_type":    strings.TrimSpace(session.SessionType),
		"expires_at":      session.ExpiresAt.UTC().Format(time.RFC3339Nano),
	})
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO refresh_sessions(id, user_id, organization_id, session_type, refresh_hash, expires_at, revoked_at, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
	`, session.ID, session.UserID, session.OrganizationID, session.SessionType, session.TokenHash, session.ExpiresAt, nil, session.CreatedAt)
	if err != nil {
		logStoreStep(ctx, "save_error", "refresh_session", map[string]any{"error": err, "session_id": strings.TrimSpace(session.ID), "user_id": strings.TrimSpace(session.UserID)})
		return err
	}
	logStoreStep(ctx, "save_success", "refresh_session", map[string]any{
		"session_id":   strings.TrimSpace(session.ID),
		"user_id":      strings.TrimSpace(session.UserID),
		"session_type": strings.TrimSpace(session.SessionType),
	})
	return err
}

// GetRefreshSessionByID loads one refresh-session row by its session ID.
func (s *Store) GetRefreshSessionByID(ctx context.Context, id string) (*RefreshSession, error) {
	var rec RefreshSession
	err := s.DB.QueryRowContext(ctx, `
		SELECT id, user_id, organization_id, session_type, refresh_hash, expires_at, revoked_at, created_at
		FROM refresh_sessions WHERE id = ?
	`, id).Scan(&rec.ID, &rec.UserID, &rec.OrganizationID, &rec.SessionType, &rec.TokenHash, &rec.ExpiresAt, &rec.RevokedAt, &rec.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if strings.TrimSpace(rec.SessionType) == "" {
		rec.SessionType = "app"
	}
	return &rec, nil
}

// RotateRefreshSession replaces the stored refresh token hash after a refresh.
func (s *Store) RotateRefreshSession(ctx context.Context, id, newHash string, expiresAt time.Time) error {
	logStoreStep(ctx, "rotate_start", "refresh_session", map[string]any{
		"session_id": strings.TrimSpace(id),
		"expires_at": expiresAt.UTC().Format(time.RFC3339Nano),
	})
	_, err := s.DB.ExecContext(ctx, `
		UPDATE refresh_sessions
		SET refresh_hash = ?, expires_at = ?, revoked_at = NULL
		WHERE id = ?
	`, newHash, expiresAt, id)
	if err != nil {
		logStoreStep(ctx, "rotate_error", "refresh_session", map[string]any{"error": err, "session_id": strings.TrimSpace(id)})
		return err
	}
	logStoreStep(ctx, "rotate_success", "refresh_session", map[string]any{"session_id": strings.TrimSpace(id)})
	return err
}

// RevokeRefreshSession marks one refresh session as revoked.
func (s *Store) RevokeRefreshSession(ctx context.Context, id string) error {
	logStoreStep(ctx, "revoke_start", "refresh_session", map[string]any{"session_id": strings.TrimSpace(id)})
	_, err := s.DB.ExecContext(ctx, `UPDATE refresh_sessions SET revoked_at = ? WHERE id = ?`, time.Now().UTC(), id)
	if err != nil {
		logStoreStep(ctx, "revoke_error", "refresh_session", map[string]any{"error": err, "session_id": strings.TrimSpace(id)})
		return err
	}
	logStoreStep(ctx, "revoke_success", "refresh_session", map[string]any{"session_id": strings.TrimSpace(id)})
	return err
}

// RevokeRefreshSessionsByUser invalidates every refresh session owned by the
// given user.
func (s *Store) RevokeRefreshSessionsByUser(ctx context.Context, userID string) error {
	logStoreStep(ctx, "revoke_start", "refresh_sessions", map[string]any{"user_id": strings.TrimSpace(userID)})
	_, err := s.DB.ExecContext(ctx, `UPDATE refresh_sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, time.Now().UTC(), userID)
	if err != nil {
		logStoreStep(ctx, "revoke_error", "refresh_sessions", map[string]any{"error": err, "user_id": strings.TrimSpace(userID)})
		return err
	}
	logStoreStep(ctx, "revoke_success", "refresh_sessions", map[string]any{"user_id": strings.TrimSpace(userID)})
	return err
}

// SavePasswordResetToken stores a one-time password reset token.
func (s *Store) SavePasswordResetToken(ctx context.Context, token PasswordResetToken) error {
	if strings.TrimSpace(token.ID) == "" {
		token.ID = uuid.NewString()
	}
	if strings.TrimSpace(token.SessionType) == "" {
		token.SessionType = "app"
	}
	if token.CreatedAt.IsZero() {
		token.CreatedAt = time.Now().UTC()
	}
	logStoreStep(ctx, "save_start", "password_reset", map[string]any{
		"token_id":        strings.TrimSpace(token.ID),
		"user_id":         strings.TrimSpace(token.UserID),
		"session_type":    strings.TrimSpace(token.SessionType),
		"requested_by_id": strings.TrimSpace(token.RequestedByUserID.String),
		"expires_at":      token.ExpiresAt.UTC().Format(time.RFC3339Nano),
	})
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO password_reset_tokens(
			id, user_id, session_type, token_hash, expires_at, used_at, requested_by_user_id, created_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?)
	`, token.ID, token.UserID, token.SessionType, token.TokenHash, token.ExpiresAt, nil, nullableString(token.RequestedByUserID.String), token.CreatedAt)
	if err != nil {
		logStoreStep(ctx, "save_error", "password_reset", map[string]any{"error": err, "token_id": strings.TrimSpace(token.ID), "user_id": strings.TrimSpace(token.UserID)})
		return err
	}
	logStoreStep(ctx, "save_success", "password_reset", map[string]any{
		"token_id":     strings.TrimSpace(token.ID),
		"user_id":      strings.TrimSpace(token.UserID),
		"session_type": strings.TrimSpace(token.SessionType),
	})
	return err
}

// GetPasswordResetTokenByHash loads a password-reset token by its hashed value.
func (s *Store) GetPasswordResetTokenByHash(ctx context.Context, hash string) (*PasswordResetToken, error) {
	var record PasswordResetToken
	err := s.DB.QueryRowContext(ctx, `
		SELECT id, user_id, session_type, token_hash, expires_at, used_at, requested_by_user_id, created_at
		FROM password_reset_tokens
		WHERE token_hash = ?
	`, strings.TrimSpace(hash)).Scan(
		&record.ID,
		&record.UserID,
		&record.SessionType,
		&record.TokenHash,
		&record.ExpiresAt,
		&record.UsedAt,
		&record.RequestedByUserID,
		&record.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if strings.TrimSpace(record.SessionType) == "" {
		record.SessionType = "app"
	}
	return &record, nil
}

// ConsumePasswordResetToken marks a password-reset token as used.
func (s *Store) ConsumePasswordResetToken(ctx context.Context, id string) error {
	logStoreStep(ctx, "consume_start", "password_reset", map[string]any{"token_id": strings.TrimSpace(id)})
	result, err := s.DB.ExecContext(ctx, `
		UPDATE password_reset_tokens
		SET used_at = ?
		WHERE id = ? AND used_at IS NULL
	`, time.Now().UTC(), strings.TrimSpace(id))
	if err != nil {
		logStoreStep(ctx, "consume_error", "password_reset", map[string]any{"error": err, "token_id": strings.TrimSpace(id)})
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		logStoreStep(ctx, "consume_error", "password_reset", map[string]any{"error": err, "token_id": strings.TrimSpace(id)})
		return err
	}
	if affected == 0 {
		logStoreStep(ctx, "consume_skipped", "password_reset", map[string]any{"token_id": strings.TrimSpace(id), "reason": "missing_or_used"})
		return nil
	}
	logStoreStep(ctx, "consume_success", "password_reset", map[string]any{"token_id": strings.TrimSpace(id), "updated_count": affected})
	return err
}

// RevokePasswordResetTokensByUser invalidates outstanding tokens for one user
// and session family.
func (s *Store) RevokePasswordResetTokensByUser(ctx context.Context, userID string, sessionType string) error {
	logStoreStep(ctx, "revoke_start", "password_reset_tokens", map[string]any{
		"user_id":      strings.TrimSpace(userID),
		"session_type": strings.TrimSpace(sessionType),
	})
	query := `
		UPDATE password_reset_tokens
		SET used_at = ?
		WHERE user_id = ? AND used_at IS NULL
	`
	args := []any{time.Now().UTC(), strings.TrimSpace(userID)}
	if strings.TrimSpace(sessionType) != "" {
		query += ` AND session_type = ?`
		args = append(args, strings.TrimSpace(sessionType))
	}
	result, err := s.DB.ExecContext(ctx, query, args...)
	if err != nil {
		logStoreStep(ctx, "revoke_error", "password_reset_tokens", map[string]any{"error": err, "user_id": strings.TrimSpace(userID), "session_type": strings.TrimSpace(sessionType)})
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		logStoreStep(ctx, "revoke_error", "password_reset_tokens", map[string]any{"error": err, "user_id": strings.TrimSpace(userID), "session_type": strings.TrimSpace(sessionType)})
		return err
	}
	logStoreStep(ctx, "revoke_success", "password_reset_tokens", map[string]any{
		"user_id":       strings.TrimSpace(userID),
		"session_type":  strings.TrimSpace(sessionType),
		"revoked_count": affected,
	})
	return err
}

// ApplyPasswordReset consumes a valid reset token, updates the password, and
// clears related refresh sessions.
func (s *Store) ApplyPasswordReset(ctx context.Context, tokenHash string, passwordHash string, sessionType string) (*PasswordResetToken, error) {
	logStoreStep(ctx, "apply_start", "password_reset", map[string]any{
		"session_type": strings.TrimSpace(sessionType),
	})
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		logStoreStep(ctx, "apply_error", "password_reset", map[string]any{"error": err, "session_type": strings.TrimSpace(sessionType)})
		return nil, err
	}
	defer rollbackTx(tx)

	var record PasswordResetToken
	err = tx.QueryRowContext(ctx, `
		SELECT id, user_id, session_type, token_hash, expires_at, used_at, requested_by_user_id, created_at
		FROM password_reset_tokens
		WHERE token_hash = ?
	`, strings.TrimSpace(tokenHash)).Scan(
		&record.ID,
		&record.UserID,
		&record.SessionType,
		&record.TokenHash,
		&record.ExpiresAt,
		&record.UsedAt,
		&record.RequestedByUserID,
		&record.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			logStoreStep(ctx, "apply_skipped", "password_reset", map[string]any{"session_type": strings.TrimSpace(sessionType), "reason": "not_found"})
			return nil, nil
		}
		logStoreStep(ctx, "apply_error", "password_reset", map[string]any{"error": err, "session_type": strings.TrimSpace(sessionType)})
		return nil, err
	}
	if strings.TrimSpace(record.SessionType) == "" {
		record.SessionType = "app"
	}

	now := time.Now().UTC()
	if record.SessionType != strings.TrimSpace(sessionType) {
		logStoreStep(ctx, "apply_skipped", "password_reset", map[string]any{"token_id": record.ID, "user_id": record.UserID, "session_type": strings.TrimSpace(sessionType), "reason": "session_type_mismatch"})
		return nil, nil
	}
	if record.UsedAt.Valid {
		logStoreStep(ctx, "apply_skipped", "password_reset", map[string]any{"token_id": record.ID, "user_id": record.UserID, "session_type": strings.TrimSpace(sessionType), "reason": "already_used"})
		return nil, nil
	}
	if record.ExpiresAt.Before(now) {
		logStoreStep(ctx, "apply_skipped", "password_reset", map[string]any{"token_id": record.ID, "user_id": record.UserID, "session_type": strings.TrimSpace(sessionType), "reason": "expired"})
		return nil, nil
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?
	`, passwordHash, now, record.UserID)
	if err != nil {
		logStoreStep(ctx, "apply_error", "password_reset", map[string]any{"error": err, "token_id": record.ID, "user_id": record.UserID, "session_type": strings.TrimSpace(sessionType)})
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		logStoreStep(ctx, "apply_error", "password_reset", map[string]any{"error": err, "token_id": record.ID, "user_id": record.UserID, "session_type": strings.TrimSpace(sessionType)})
		return nil, err
	}
	if affected == 0 {
		logStoreStep(ctx, "apply_skipped", "password_reset", map[string]any{"token_id": record.ID, "user_id": record.UserID, "session_type": strings.TrimSpace(sessionType), "reason": "user_missing"})
		return nil, nil
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE refresh_sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL
	`, now, record.UserID); err != nil {
		logStoreStep(ctx, "apply_error", "password_reset", map[string]any{"error": err, "token_id": record.ID, "user_id": record.UserID, "session_type": strings.TrimSpace(sessionType)})
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE password_reset_tokens
		SET used_at = ?
		WHERE user_id = ? AND used_at IS NULL
	`, now, record.UserID); err != nil {
		logStoreStep(ctx, "apply_error", "password_reset", map[string]any{"error": err, "token_id": record.ID, "user_id": record.UserID, "session_type": strings.TrimSpace(sessionType)})
		return nil, err
	}

	record.UsedAt = sql.NullTime{Time: now, Valid: true}
	if err := tx.Commit(); err != nil {
		logStoreStep(ctx, "apply_error", "password_reset", map[string]any{"error": err, "token_id": record.ID, "user_id": record.UserID, "session_type": strings.TrimSpace(sessionType)})
		return nil, err
	}
	logStoreStep(ctx, "apply_success", "password_reset", map[string]any{
		"token_id":     record.ID,
		"user_id":      record.UserID,
		"session_type": strings.TrimSpace(sessionType),
	})
	return &record, nil
}

// GetUserSettings loads the opaque JSON settings document for one user.
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

// SaveUserSettings upserts the user's JSON settings payload.
func (s *Store) SaveUserSettings(ctx context.Context, userID, organizationID string, settings json.RawMessage, schemaVersion int) (*SettingsRecord, error) {
	now := time.Now().UTC()
	payload := strings.TrimSpace(string(settings))
	if payload == "" {
		payload = "{}"
	}
	if schemaVersion <= 0 {
		schemaVersion = 1
	}
	logStoreStep(ctx, "save_start", "settings", map[string]any{
		"user_id":         strings.TrimSpace(userID),
		"organization_id": strings.TrimSpace(organizationID),
		"schema_version":  schemaVersion,
		"settings_bytes":  len(payload),
	})
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
		logStoreStep(ctx, "save_error", "settings", map[string]any{"error": err, "user_id": strings.TrimSpace(userID), "organization_id": strings.TrimSpace(organizationID)})
		return nil, err
	}
	record, err := s.GetUserSettings(ctx, userID)
	if err != nil {
		logStoreStep(ctx, "save_error", "settings", map[string]any{"error": err, "user_id": strings.TrimSpace(userID), "organization_id": strings.TrimSpace(organizationID)})
		return nil, err
	}
	logStoreStep(ctx, "save_success", "settings", map[string]any{
		"user_id":         strings.TrimSpace(userID),
		"organization_id": strings.TrimSpace(organizationID),
		"version":         record.Version,
		"schema_version":  record.SchemaVersion,
	})
	return record, nil
}

// ResetUserSettings replaces the settings document with an empty object.
func (s *Store) ResetUserSettings(ctx context.Context, userID, organizationID string) (*SettingsRecord, error) {
	return s.SaveUserSettings(ctx, userID, organizationID, json.RawMessage(`{}`), 1)
}

// IngestActivityEvents stores the validated usage events and ignores duplicate
// event IDs.
func (s *Store) IngestActivityEvents(
	ctx context.Context,
	organizationID string,
	userID string,
	events []ActivityEventInput,
) (ActivityIngestResult, error) {
	result := ActivityIngestResult{}
	logStoreStep(ctx, "ingest_start", "activity", map[string]any{
		"organization_id": strings.TrimSpace(organizationID),
		"user_id":         strings.TrimSpace(userID),
		"event_count":     len(events),
	})
	if len(events) == 0 {
		logStoreStep(ctx, "ingest_success", "activity", map[string]any{
			"organization_id": strings.TrimSpace(organizationID),
			"user_id":         strings.TrimSpace(userID),
			"accepted":        0,
			"duplicates":      0,
		})
		return result, nil
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		logStoreStep(ctx, "ingest_error", "activity", map[string]any{"error": err, "organization_id": strings.TrimSpace(organizationID), "user_id": strings.TrimSpace(userID)})
		return result, err
	}
	defer rollbackTx(tx)

	now := time.Now().UTC()
	for _, event := range events {
		eventID := strings.TrimSpace(event.EventID)
		if eventID == "" {
			continue
		}

		occurredAt := event.OccurredAt.UTC()
		if occurredAt.IsZero() {
			occurredAt = now
		}

		metaJSON := strings.TrimSpace(string(event.MetaJSON))
		if metaJSON == "" {
			metaJSON = "{}"
		}

		day := occurredAt.Format("2006-01-02")
		_, err := tx.ExecContext(ctx, `
			INSERT INTO activity_usage_events(
				event_id, organization_id, user_id, event_kind, source_mode,
				provider, status, occurred_at, day, meta_json, created_at
			) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, eventID, organizationID, userID, event.EventKind, event.SourceMode, event.Provider, event.Status, occurredAt, day, metaJSON, now)
		if err != nil {
			if isActivityEventDuplicateErr(err) {
				result.Duplicates++
				continue
			}
			logStoreStep(ctx, "ingest_error", "activity", map[string]any{"error": err, "organization_id": strings.TrimSpace(organizationID), "user_id": strings.TrimSpace(userID), "accepted": result.Accepted, "duplicates": result.Duplicates})
			return result, err
		}
		result.Accepted++
	}

	if err := tx.Commit(); err != nil {
		logStoreStep(ctx, "ingest_error", "activity", map[string]any{"error": err, "organization_id": strings.TrimSpace(organizationID), "user_id": strings.TrimSpace(userID), "accepted": result.Accepted, "duplicates": result.Duplicates})
		return result, err
	}
	logStoreStep(ctx, "ingest_success", "activity", map[string]any{
		"organization_id": strings.TrimSpace(organizationID),
		"user_id":         strings.TrimSpace(userID),
		"accepted":        result.Accepted,
		"duplicates":      result.Duplicates,
	})
	return result, nil
}

// GetOrganizationActivitySummary returns the organization-scoped activity
// aggregate for the requested time window.
func (s *Store) GetOrganizationActivitySummary(
	ctx context.Context,
	organizationID string,
	fromDay string,
	toDay string,
) (*ActivitySummary, error) {
	summary := &ActivitySummary{
		OrganizationID: organizationID,
		Range: ActivityRange{
			From: fromDay,
			To:   toDay,
		},
		Breakdown: ActivityBreakdown{
			TranscriptionsByMode:     map[string]int{},
			TranscriptionsByProvider: map[string]int{},
			ReportsByMode:            map[string]int{},
			ReportsByProvider:        map[string]int{},
		},
		ByDay:  []ActivityByDayItem{},
		ByUser: []ActivityByUserItem{},
	}

	err := s.DB.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN event_kind = 'transcription' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN event_kind = 'report' THEN 1 ELSE 0 END), 0)
		FROM activity_usage_events
		WHERE organization_id = ? AND day BETWEEN ? AND ?
	`, organizationID, fromDay, toDay).Scan(&summary.Totals.Transcriptions, &summary.Totals.Reports)
	if err != nil {
		return nil, err
	}

	byDayRows, err := s.DB.QueryContext(ctx, `
		SELECT
			day,
			COALESCE(SUM(CASE WHEN event_kind = 'transcription' THEN 1 ELSE 0 END), 0) AS transcriptions,
			COALESCE(SUM(CASE WHEN event_kind = 'report' THEN 1 ELSE 0 END), 0) AS reports
		FROM activity_usage_events
		WHERE organization_id = ? AND day BETWEEN ? AND ?
		GROUP BY day
		ORDER BY day ASC
	`, organizationID, fromDay, toDay)
	if err != nil {
		return nil, err
	}
	defer closeRows(byDayRows)
	for byDayRows.Next() {
		var item ActivityByDayItem
		if err := byDayRows.Scan(&item.Day, &item.Transcriptions, &item.Reports); err != nil {
			return nil, err
		}
		summary.ByDay = append(summary.ByDay, item)
	}
	if err := byDayRows.Err(); err != nil {
		return nil, err
	}

	byUserRows, err := s.DB.QueryContext(ctx, `
		SELECT
			e.user_id,
			COALESCE(u.email, '') AS email,
			COALESCE(SUM(CASE WHEN e.event_kind = 'transcription' THEN 1 ELSE 0 END), 0) AS transcriptions,
			COALESCE(SUM(CASE WHEN e.event_kind = 'report' THEN 1 ELSE 0 END), 0) AS reports
		FROM activity_usage_events e
		LEFT JOIN users u ON u.id = e.user_id
		WHERE e.organization_id = ? AND e.day BETWEEN ? AND ?
		GROUP BY e.user_id, u.email
		ORDER BY (transcriptions + reports) DESC, email ASC
	`, organizationID, fromDay, toDay)
	if err != nil {
		return nil, err
	}
	defer closeRows(byUserRows)
	for byUserRows.Next() {
		var item ActivityByUserItem
		if err := byUserRows.Scan(&item.UserID, &item.Email, &item.Transcriptions, &item.Reports); err != nil {
			return nil, err
		}
		summary.ByUser = append(summary.ByUser, item)
	}
	if err := byUserRows.Err(); err != nil {
		return nil, err
	}

	if err := s.scanActivityBreakdown(ctx, summary.Breakdown.TranscriptionsByMode, `
		SELECT source_mode, COUNT(*)
		FROM activity_usage_events
		WHERE organization_id = ? AND day BETWEEN ? AND ? AND event_kind = 'transcription'
		GROUP BY source_mode
	`, organizationID, fromDay, toDay); err != nil {
		return nil, err
	}
	if err := s.scanActivityBreakdown(ctx, summary.Breakdown.TranscriptionsByProvider, `
		SELECT provider, COUNT(*)
		FROM activity_usage_events
		WHERE organization_id = ? AND day BETWEEN ? AND ? AND event_kind = 'transcription'
		GROUP BY provider
	`, organizationID, fromDay, toDay); err != nil {
		return nil, err
	}
	if err := s.scanActivityBreakdown(ctx, summary.Breakdown.ReportsByMode, `
		SELECT source_mode, COUNT(*)
		FROM activity_usage_events
		WHERE organization_id = ? AND day BETWEEN ? AND ? AND event_kind = 'report'
		GROUP BY source_mode
	`, organizationID, fromDay, toDay); err != nil {
		return nil, err
	}
	if err := s.scanActivityBreakdown(ctx, summary.Breakdown.ReportsByProvider, `
		SELECT provider, COUNT(*)
		FROM activity_usage_events
		WHERE organization_id = ? AND day BETWEEN ? AND ? AND event_kind = 'report'
		GROUP BY provider
	`, organizationID, fromDay, toDay); err != nil {
		return nil, err
	}

	return summary, nil
}

// GetUserActivitySummary returns the user-scoped activity aggregate for the
// requested time window.
func (s *Store) GetUserActivitySummary(
	ctx context.Context,
	userID string,
	fromDay string,
	toDay string,
) (*UserActivitySummary, error) {
	user, err := s.GetUserByID(ctx, strings.TrimSpace(userID))
	if err != nil || user == nil {
		return nil, err
	}

	summary := &UserActivitySummary{
		User: *user,
		Range: ActivityRange{
			From: fromDay,
			To:   toDay,
		},
		Breakdown: ActivityBreakdown{
			TranscriptionsByMode:     map[string]int{},
			TranscriptionsByProvider: map[string]int{},
			ReportsByMode:            map[string]int{},
			ReportsByProvider:        map[string]int{},
		},
		ByDay: []ActivityByDayItem{},
	}

	err = s.DB.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN event_kind = 'transcription' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN event_kind = 'report' THEN 1 ELSE 0 END), 0)
		FROM activity_usage_events
		WHERE user_id = ? AND day BETWEEN ? AND ?
	`, userID, fromDay, toDay).Scan(&summary.Totals.Transcriptions, &summary.Totals.Reports)
	if err != nil {
		return nil, err
	}

	byDayRows, err := s.DB.QueryContext(ctx, `
		SELECT
			day,
			COALESCE(SUM(CASE WHEN event_kind = 'transcription' THEN 1 ELSE 0 END), 0) AS transcriptions,
			COALESCE(SUM(CASE WHEN event_kind = 'report' THEN 1 ELSE 0 END), 0) AS reports
		FROM activity_usage_events
		WHERE user_id = ? AND day BETWEEN ? AND ?
		GROUP BY day
		ORDER BY day ASC
	`, userID, fromDay, toDay)
	if err != nil {
		return nil, err
	}
	defer closeRows(byDayRows)
	for byDayRows.Next() {
		var item ActivityByDayItem
		if err := byDayRows.Scan(&item.Day, &item.Transcriptions, &item.Reports); err != nil {
			return nil, err
		}
		summary.ByDay = append(summary.ByDay, item)
	}
	if err := byDayRows.Err(); err != nil {
		return nil, err
	}

	if err := s.scanActivityBreakdown(ctx, summary.Breakdown.TranscriptionsByMode, `
		SELECT source_mode, COUNT(*)
		FROM activity_usage_events
		WHERE user_id = ? AND day BETWEEN ? AND ? AND event_kind = 'transcription'
		GROUP BY source_mode
	`, userID, fromDay, toDay); err != nil {
		return nil, err
	}
	if err := s.scanActivityBreakdown(ctx, summary.Breakdown.TranscriptionsByProvider, `
		SELECT provider, COUNT(*)
		FROM activity_usage_events
		WHERE user_id = ? AND day BETWEEN ? AND ? AND event_kind = 'transcription'
		GROUP BY provider
	`, userID, fromDay, toDay); err != nil {
		return nil, err
	}
	if err := s.scanActivityBreakdown(ctx, summary.Breakdown.ReportsByMode, `
		SELECT source_mode, COUNT(*)
		FROM activity_usage_events
		WHERE user_id = ? AND day BETWEEN ? AND ? AND event_kind = 'report'
		GROUP BY source_mode
	`, userID, fromDay, toDay); err != nil {
		return nil, err
	}
	if err := s.scanActivityBreakdown(ctx, summary.Breakdown.ReportsByProvider, `
		SELECT provider, COUNT(*)
		FROM activity_usage_events
		WHERE user_id = ? AND day BETWEEN ? AND ? AND event_kind = 'report'
		GROUP BY provider
	`, userID, fromDay, toDay); err != nil {
		return nil, err
	}

	return summary, nil
}

// scanActivityBreakdown loads the per-mode and per-provider buckets that back
// the activity summaries.
func (s *Store) scanActivityBreakdown(
	ctx context.Context,
	out map[string]int,
	query string,
	args ...any,
) error {
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer closeRows(rows)

	for rows.Next() {
		var key string
		var count int
		if err := rows.Scan(&key, &count); err != nil {
			return err
		}
		out[key] = count
	}
	return rows.Err()
}

// lookupRoleID resolves a role code to its database identifier inside a
// transaction.
func (s *Store) lookupRoleID(ctx context.Context, tx *sql.Tx, query string, code string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, query, code).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func (s *Store) lookupGlobalRoleID(ctx context.Context, tx *sql.Tx, code string) (string, error) {
	return s.lookupRoleID(ctx, tx, `SELECT id FROM global_roles WHERE code = ?`, code)
}

func (s *Store) lookupOrganizationRoleID(ctx context.Context, tx *sql.Tx, code string) (string, error) {
	return s.lookupRoleID(ctx, tx, `SELECT id FROM organization_roles WHERE code = ?`, code)
}

func (s *Store) lookupPermissionID(ctx context.Context, tx *sql.Tx, code string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM permissions WHERE code = ?`, code).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// normalizeStatus maps blank values to the backend's default active state.
func normalizeStatus(value string) string {
	s := strings.ToLower(strings.TrimSpace(value))
	if s == "inactive" || s == "disabled" {
		return "inactive"
	}
	return "active"
}

// normalizeOrgCode converts organization codes into a predictable lowercase
// slug-like form.
func normalizeOrgCode(value string) string {
	code := strings.ToLower(strings.TrimSpace(value))
	code = strings.ReplaceAll(code, " ", "-")
	code = strings.ReplaceAll(code, "_", "-")
	if code == "" {
		return "org-" + uuid.NewString()[:8]
	}
	return code
}

// uniqueNormalizedCodes trims, deduplicates, and preserves the incoming order
// of role or permission codes.
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

// NormalizePermissionOverrideInputs trims and deduplicates override payloads
// before they reach persistence code.
func NormalizePermissionOverrideInputs(overrides []UserPermissionOverrideInput) []UserPermissionOverrideInput {
	seen := map[string]int{}
	out := make([]UserPermissionOverrideInput, 0, len(overrides))
	for _, override := range overrides {
		code := strings.TrimSpace(override.PermissionCode)
		effect := strings.ToLower(strings.TrimSpace(override.Effect))
		if code == "" || (effect != "allow" && effect != "deny") {
			continue
		}
		if index, exists := seen[code]; exists {
			out[index].Effect = effect
			continue
		}
		seen[code] = len(out)
		out = append(out, UserPermissionOverrideInput{
			PermissionCode: code,
			Effect:         effect,
		})
	}
	return out
}

// scanStringRows is a tiny helper used by the catalog and role readers.
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

// scanCatalogRows converts catalog rows into the map shape used by the API.
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

// sortStrings keeps catalog output stable for tests and consumers.
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

// isActivityEventDuplicateErr recognizes the unique constraint used for
// idempotent activity ingestion.
func isActivityEventDuplicateErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint failed") && strings.Contains(msg, "activity_usage_events.event_id")
}

// closeRows closes a result set and intentionally ignores the close error.
func closeRows(rows *sql.Rows) {
	if rows == nil {
		return
	}
	_ = rows.Close()
}

// rollbackTx rolls back the transaction if it is still open.
func rollbackTx(tx *sql.Tx) {
	if tx == nil {
		return
	}
	_ = tx.Rollback()
}
