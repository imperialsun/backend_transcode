package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"demeter-backend/internal/auth"
)

func TestDeleteUser_NullifiesReferencesAndCascadesUserScopedData(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "delete-user.sqlite")
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	org, err := st.CreateOrganization(ctx, "Delete Org", "delete-org", "active")
	if err != nil {
		t.Fatalf("failed to create organization: %v", err)
	}
	passwordHash, err := auth.HashPassword("ChangeMe123!")
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}
	target, err := st.CreateUser(ctx, org.ID, "target@example.com", passwordHash, "active")
	if err != nil {
		t.Fatalf("failed to create target user: %v", err)
	}
	other, err := st.CreateUser(ctx, org.ID, "other@example.com", passwordHash, "active")
	if err != nil {
		t.Fatalf("failed to create secondary user: %v", err)
	}

	if err := st.SetUserGlobalRoles(ctx, target.ID, []string{"user"}); err != nil {
		t.Fatalf("failed to set global roles: %v", err)
	}
	if err := st.SetUserOrganizationRoles(ctx, target.ID, []string{"org_member"}); err != nil {
		t.Fatalf("failed to set organization roles: %v", err)
	}
	if err := st.SetUserPermissionOverrides(ctx, target.ID, []UserPermissionOverrideInput{
		{PermissionCode: "feature.settings", Effect: "allow"},
	}); err != nil {
		t.Fatalf("failed to set permission overrides: %v", err)
	}
	if _, err := st.SaveUserSettings(ctx, target.ID, org.ID, json.RawMessage(`{"theme":"dark"}`), 1); err != nil {
		t.Fatalf("failed to save user settings: %v", err)
	}

	refresh, err := auth.NewRefreshToken(2 * time.Hour)
	if err != nil {
		t.Fatalf("failed to create refresh token: %v", err)
	}
	if err := st.SaveRefreshSession(ctx, RefreshSession{
		ID:             refresh.SessionID,
		UserID:         target.ID,
		OrganizationID: org.ID,
		SessionType:    auth.SessionTypeApp.String(),
		TokenHash:      refresh.Hash,
		ExpiresAt:      refresh.ExpiresAt,
		CreatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("failed to save refresh session: %v", err)
	}
	if err := st.SavePasswordResetToken(ctx, PasswordResetToken{
		UserID:      target.ID,
		SessionType: auth.SessionTypeApp.String(),
		TokenHash:   auth.HashPasswordResetToken("owned-by-target"),
		ExpiresAt:   time.Now().UTC().Add(time.Hour),
		CreatedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("failed to save owned password reset token: %v", err)
	}
	if err := st.SavePasswordResetToken(ctx, PasswordResetToken{
		UserID:            other.ID,
		SessionType:       auth.SessionTypeApp.String(),
		TokenHash:         auth.HashPasswordResetToken("requested-by-target"),
		ExpiresAt:         time.Now().UTC().Add(time.Hour),
		RequestedByUserID: nullableSQLString(target.ID),
		CreatedAt:         time.Now().UTC(),
	}); err != nil {
		t.Fatalf("failed to save requested-by password reset token: %v", err)
	}

	if err := st.InsertAuditLog(ctx, AuditLogInput{
		ActorUserID:    target.ID,
		OrganizationID: org.ID,
		Action:         "admin.test",
		TargetType:     "user",
		TargetID:       other.ID,
		Payload:        map[string]any{"ok": true},
	}); err != nil {
		t.Fatalf("failed to insert audit log: %v", err)
	}

	ingested, err := st.IngestActivityEvents(ctx, org.ID, target.ID, []ActivityEventInput{
		{
			EventID:    "activity-delete-test",
			EventKind:  "transcription",
			SourceMode: "local",
			Provider:   "local_upload",
			Status:     "success",
			OccurredAt: time.Now().UTC(),
			MetaJSON:   json.RawMessage(`{"kind":"test"}`),
		},
	})
	if err != nil {
		t.Fatalf("failed to ingest activity event: %v", err)
	}
	if ingested.Accepted != 1 {
		t.Fatalf("expected one accepted event, got %+v", ingested)
	}

	deleted, err := st.DeleteUser(ctx, target.ID)
	if err != nil {
		t.Fatalf("DeleteUser returned error: %v", err)
	}
	if !deleted {
		t.Fatal("expected DeleteUser to report success")
	}

	reloadedTarget, err := st.GetUserByID(ctx, target.ID)
	if err != nil {
		t.Fatalf("failed to reload deleted user: %v", err)
	}
	if reloadedTarget != nil {
		t.Fatalf("expected user to be deleted, got %#v", reloadedTarget)
	}

	assertTableCount(t, st, "refresh_sessions", "user_id", target.ID, 0)
	assertTableCount(t, st, "user_settings", "user_id", target.ID, 0)
	assertTableCount(t, st, "user_global_roles", "user_id", target.ID, 0)
	assertTableCount(t, st, "user_organization_roles", "user_id", target.ID, 0)
	assertTableCount(t, st, "user_permission_overrides", "user_id", target.ID, 0)
	assertTableCount(t, st, "password_reset_tokens", "user_id", target.ID, 0)
	assertTableCount(t, st, "activity_usage_events", "user_id", target.ID, 0)

	var requestedBy any
	if err := st.DB.QueryRowContext(ctx, `
		SELECT requested_by_user_id
		FROM password_reset_tokens
		WHERE token_hash = ?
	`, auth.HashPasswordResetToken("requested-by-target")).Scan(&requestedBy); err != nil {
		t.Fatalf("failed to reload password reset token requester: %v", err)
	}
	if requestedBy != nil {
		t.Fatalf("expected requested_by_user_id to be null, got %#v", requestedBy)
	}

	var actorUserID any
	if err := st.DB.QueryRowContext(ctx, `
		SELECT actor_user_id
		FROM audit_logs
		WHERE action = 'admin.test'
	`).Scan(&actorUserID); err != nil {
		t.Fatalf("failed to reload audit log actor: %v", err)
	}
	if actorUserID != nil {
		t.Fatalf("expected actor_user_id to be null, got %#v", actorUserID)
	}
}

func TestCountActiveUsersByRoles(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "count-user-roles.sqlite")
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	org, err := st.CreateOrganization(ctx, "Count Org", "count-org", "active")
	if err != nil {
		t.Fatalf("failed to create organization: %v", err)
	}
	passwordHash, err := auth.HashPassword("ChangeMe123!")
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}
	activeSuper, err := st.CreateUser(ctx, org.ID, "super@example.com", passwordHash, "active")
	if err != nil {
		t.Fatalf("failed to create active super admin: %v", err)
	}
	inactiveSuper, err := st.CreateUser(ctx, org.ID, "super-inactive@example.com", passwordHash, "inactive")
	if err != nil {
		t.Fatalf("failed to create inactive super admin: %v", err)
	}
	activeOrgAdmin, err := st.CreateUser(ctx, org.ID, "org-admin@example.com", passwordHash, "active")
	if err != nil {
		t.Fatalf("failed to create active org admin: %v", err)
	}

	if err := st.SetUserGlobalRoles(ctx, activeSuper.ID, []string{"super_admin"}); err != nil {
		t.Fatalf("failed to set active super admin role: %v", err)
	}
	if err := st.SetUserGlobalRoles(ctx, inactiveSuper.ID, []string{"super_admin"}); err != nil {
		t.Fatalf("failed to set inactive super admin role: %v", err)
	}
	if err := st.SetUserOrganizationRoles(ctx, activeOrgAdmin.ID, []string{"org_admin"}); err != nil {
		t.Fatalf("failed to set org admin role: %v", err)
	}

	superAdmins, err := st.CountActiveUsersByGlobalRole(ctx, "super_admin")
	if err != nil {
		t.Fatalf("failed to count active super admins: %v", err)
	}
	if superAdmins != 1 {
		t.Fatalf("expected 1 active super admin, got %d", superAdmins)
	}

	orgAdmins, err := st.CountActiveUsersByOrganizationRole(ctx, org.ID, "org_admin")
	if err != nil {
		t.Fatalf("failed to count active org admins: %v", err)
	}
	if orgAdmins != 1 {
		t.Fatalf("expected 1 active org admin, got %d", orgAdmins)
	}
}

func assertTableCount(t *testing.T, st *Store, tableName, columnName, value string, expected int) {
	t.Helper()

	var count int
	query := "SELECT COUNT(*) FROM " + tableName + " WHERE " + columnName + " = ?"
	if err := st.DB.QueryRowContext(context.Background(), query, value).Scan(&count); err != nil {
		t.Fatalf("failed to count rows in %s: %v", tableName, err)
	}
	if count != expected {
		t.Fatalf("expected %d rows in %s for %s=%q, got %d", expected, tableName, columnName, value, count)
	}
}

func nullableSQLString(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}
