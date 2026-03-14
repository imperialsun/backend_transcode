package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStoreBasicsAndUserUpdates(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, t.TempDir()+"/basic.sqlite")
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	if err := st.Ping(ctx); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}

	// Ensure bootstrap does nothing when users exist
	if err := st.EnsureBootstrap(ctx, "", "", ""); err != nil {
		t.Fatalf("EnsureBootstrap failed: %v", err)
	}

	org, err := st.CreateOrganization(ctx, "Update Org", "upd-org", "active")
	if err != nil {
		t.Fatalf("CreateOrganization failed: %v", err)
	}

	updated, err := st.UpdateOrganization(ctx, org.ID, ptrString("Updated Org"), ptrString("upd-org-2"), ptrString("inactive"))
	if err != nil {
		t.Fatalf("UpdateOrganization failed: %v", err)
	}
	if updated.Name != "Updated Org" || updated.Code != "upd-org-2" || updated.Status != "inactive" {
		t.Fatalf("unexpected updated org: %+v", updated)
	}

	user, err := st.CreateUser(ctx, org.ID, "u1@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	// List users by org
	users, err := st.ListUsersByOrganization(ctx, org.ID)
	if err != nil {
		t.Fatalf("ListUsersByOrganization failed: %v", err)
	}
	if len(users) != 1 || users[0].ID != user.ID {
		t.Fatalf("unexpected users: %+v", users)
	}

	// Update user and password
	newEmail := "u2@example.com"
	newStatus := "inactive"
	updatedUser, err := st.UpdateUser(ctx, user.ID, UpdateUserInput{Email: &newEmail, Status: &newStatus})
	if err != nil {
		t.Fatalf("UpdateUser failed: %v", err)
	}
	if updatedUser.Email != newEmail || updatedUser.Status != newStatus {
		t.Fatalf("unexpected updated user: %+v", updatedUser)
	}

	// Update password and verify hash changes
	origHash := updatedUser.PasswordHash
	if err := st.UpdateUserPassword(ctx, user.ID, "new-hash"); err != nil {
		t.Fatalf("UpdateUserPassword failed: %v", err)
	}
	changed, err := st.GetUserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserByID failed: %v", err)
	}
	if changed.PasswordHash == origHash {
		t.Fatal("expected password hash to change")
	}
}

func TestResolveEffectivePermissionsAndSettingsReset(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, t.TempDir()+"/perm.sqlite")
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	org, err := st.CreateOrganization(ctx, "Perm Org", "perm-org", "active")
	if err != nil {
		t.Fatalf("CreateOrganization failed: %v", err)
	}
	user, err := st.CreateUser(ctx, org.ID, "perm@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	// Set global + org roles and verify effective permissions include expected codes
	if err := st.SetUserGlobalRoles(ctx, user.ID, []string{"super_admin"}); err != nil {
		t.Fatalf("SetUserGlobalRoles failed: %v", err)
	}
	if err := st.SetUserOrganizationRoles(ctx, user.ID, []string{"org_admin"}); err != nil {
		t.Fatalf("SetUserOrganizationRoles failed: %v", err)
	}

	perms, err := st.ResolveEffectivePermissions(ctx, user.ID)
	if err != nil {
		t.Fatalf("ResolveEffectivePermissions failed: %v", err)
	}
	if len(perms) == 0 {
		t.Fatalf("expected some permissions, got none")
	}

	// Apply explicit overrides
	if err := st.SetUserPermissionOverrides(ctx, user.ID, []UserPermissionOverrideInput{{PermissionCode: "feature.settings", Effect: "deny"}}); err != nil {
		t.Fatalf("SetUserPermissionOverrides failed: %v", err)
	}
	perms, err = st.ResolveEffectivePermissions(ctx, user.ID)
	if err != nil {
		t.Fatalf("ResolveEffectivePermissions failed: %v", err)
	}
	for _, p := range perms {
		if p == "feature.settings" {
			t.Fatal("expected feature.settings to be denied")
		}
	}

	// Settings reset
	if _, err := st.SaveUserSettings(ctx, user.ID, org.ID, []byte(`{"a":1}`), 2); err != nil {
		t.Fatalf("SaveUserSettings failed: %v", err)
	}
	if _, err := st.ResetUserSettings(ctx, user.ID, org.ID); err != nil {
		t.Fatalf("ResetUserSettings failed: %v", err)
	}

	// Insert audit log and verify it persists
	if err := st.InsertAuditLog(ctx, AuditLogInput{ActorUserID: user.ID, OrganizationID: org.ID, Action: "test.action", TargetType: "user", TargetID: user.ID, Payload: map[string]any{"ok": true}}); err != nil {
		t.Fatalf("InsertAuditLog failed: %v", err)
	}
	var count int
	if err := st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs WHERE action = ?`, "test.action").Scan(&count); err != nil {
		t.Fatalf("failed to query audit log: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 audit log, got %d", count)
	}

	// Get global activity summary must work even when no events
	if _, err := st.GetGlobalActivitySummary(ctx, "2024-01-01", "2024-01-31"); err != nil {
		t.Fatalf("GetGlobalActivitySummary failed: %v", err)
	}
}

func TestGetGlobalActivitySummary_ReturnsBreakdownsByDayAndUser(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, t.TempDir()+"/global-activity.sqlite")
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	orgA := createOrg(t, st, "Org A", "org-a", "active")
	orgB := createOrg(t, st, "Org B", "org-b", "active")
	userA := createUserWithPassword(t, st, orgA.ID, "user-a@example.com", "ChangeMe123!", "active")
	userB := createUserWithPassword(t, st, orgB.ID, "user-b@example.com", "ChangeMe123!", "active")

	dayOne := time.Date(2026, 3, 10, 8, 0, 0, 0, time.UTC)
	dayTwo := time.Date(2026, 3, 11, 9, 0, 0, 0, time.UTC)
	_, err = st.IngestActivityEvents(ctx, orgA.ID, userA.ID, []ActivityEventInput{
		{EventID: "evt-a-1", EventKind: "transcription", SourceMode: "local", Provider: "local_upload", Status: "success", OccurredAt: dayOne},
		{EventID: "evt-a-2", EventKind: "report", SourceMode: "cloud_backend", Provider: "demeter_sante", Status: "success", OccurredAt: dayTwo},
	})
	if err != nil {
		t.Fatalf("failed to ingest org A activity: %v", err)
	}
	_, err = st.IngestActivityEvents(ctx, orgB.ID, userB.ID, []ActivityEventInput{
		{EventID: "evt-b-1", EventKind: "transcription", SourceMode: "cloud_direct", Provider: "mistral", Status: "success", OccurredAt: dayTwo},
	})
	if err != nil {
		t.Fatalf("failed to ingest org B activity: %v", err)
	}

	summary, err := st.GetGlobalActivitySummary(ctx, "2026-03-10", "2026-03-11")
	if err != nil {
		t.Fatalf("GetGlobalActivitySummary failed: %v", err)
	}
	if summary.Totals.Transcriptions != 2 || summary.Totals.Reports != 1 {
		t.Fatalf("unexpected totals: %+v", summary.Totals)
	}
	if len(summary.ByDay) != 2 {
		t.Fatalf("expected 2 by-day items, got %+v", summary.ByDay)
	}
	if len(summary.ByUser) != 2 {
		t.Fatalf("expected 2 by-user items, got %+v", summary.ByUser)
	}
	if summary.Breakdown.TranscriptionsByMode["local"] != 1 || summary.Breakdown.TranscriptionsByMode["cloud_direct"] != 1 {
		t.Fatalf("unexpected transcription mode breakdown: %+v", summary.Breakdown.TranscriptionsByMode)
	}
	if summary.Breakdown.ReportsByProvider["demeter_sante"] != 1 {
		t.Fatalf("unexpected report provider breakdown: %+v", summary.Breakdown.ReportsByProvider)
	}
}

func TestGetGlobalActivitySummary_OrdersUsersByTotalsThenEmail(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t, "global-order.sqlite")

	orgA := createOrg(t, st, "Org A", "org-a", "active")
	orgB := createOrg(t, st, "Org B", "org-b", "active")
	alpha := createUserWithPassword(t, st, orgA.ID, "alpha@example.com", "ChangeMe123!", "active")
	beta := createUserWithPassword(t, st, orgB.ID, "beta@example.com", "ChangeMe123!", "active")
	day := time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC)

	_, err := st.IngestActivityEvents(ctx, orgA.ID, alpha.ID, []ActivityEventInput{
		{EventID: "g-1", EventKind: "transcription", SourceMode: "local", Provider: "local_upload", Status: "success", OccurredAt: day},
		{EventID: "g-2", EventKind: "report", SourceMode: "local", Provider: "local", Status: "success", OccurredAt: day},
	})
	if err != nil {
		t.Fatalf("IngestActivityEvents failed: %v", err)
	}
	_, err = st.IngestActivityEvents(ctx, orgB.ID, beta.ID, []ActivityEventInput{
		{EventID: "g-3", EventKind: "transcription", SourceMode: "cloud_direct", Provider: "mistral", Status: "success", OccurredAt: day},
		{EventID: "g-4", EventKind: "report", SourceMode: "cloud_backend", Provider: "demeter_sante", Status: "success", OccurredAt: day},
	})
	if err != nil {
		t.Fatalf("IngestActivityEvents failed: %v", err)
	}

	summary, err := st.GetGlobalActivitySummary(ctx, "2026-03-13", "2026-03-13")
	if err != nil {
		t.Fatalf("GetGlobalActivitySummary failed: %v", err)
	}
	if len(summary.ByUser) != 2 {
		t.Fatalf("expected 2 users, got %+v", summary.ByUser)
	}
	if summary.ByUser[0].Email != "alpha@example.com" || summary.ByUser[1].Email != "beta@example.com" {
		t.Fatalf("expected by-user ordering by email on equal totals, got %+v", summary.ByUser)
	}
}

func TestInsertAuditLog_UsesExplicitPayloadJSONAndDefaultsToEmptyObject(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t, "audit-log.sqlite")

	if err := st.InsertAuditLog(ctx, AuditLogInput{
		Action:      "audit.explicit",
		TargetType:  "user",
		TargetID:    "user-1",
		PayloadJSON: json.RawMessage(`{"mode":"explicit"}`),
	}); err != nil {
		t.Fatalf("InsertAuditLog with explicit payload failed: %v", err)
	}

	if err := st.InsertAuditLog(ctx, AuditLogInput{
		Action:     "audit.empty",
		TargetType: "user",
		TargetID:   "user-2",
	}); err != nil {
		t.Fatalf("InsertAuditLog with empty payload failed: %v", err)
	}

	var explicitPayload string
	if err := st.DB.QueryRowContext(ctx, `SELECT payload_json FROM audit_logs WHERE action = ?`, "audit.explicit").Scan(&explicitPayload); err != nil {
		t.Fatalf("failed to read explicit payload audit log: %v", err)
	}
	if explicitPayload != `{"mode":"explicit"}` {
		t.Fatalf("unexpected explicit payload: %s", explicitPayload)
	}

	var emptyPayload string
	if err := st.DB.QueryRowContext(ctx, `SELECT payload_json FROM audit_logs WHERE action = ?`, "audit.empty").Scan(&emptyPayload); err != nil {
		t.Fatalf("failed to read empty payload audit log: %v", err)
	}
	if emptyPayload != `{}` {
		t.Fatalf("expected empty payload to default to {}, got %s", emptyPayload)
	}
}

func TestRoleAndOverrideSetters_NormalizeAndIgnoreUnknownCodes(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t, "setters.sqlite")

	org := createOrg(t, st, "Setter Org", "setter-org", "active")
	user := createUserWithPassword(t, st, org.ID, "setter@example.com", "ChangeMe123!", "active")

	if err := st.SetGlobalRolePermissionsByCode(ctx, "user", []string{" feature.settings ", "feature.settings", "missing.permission"}); err != nil {
		t.Fatalf("SetGlobalRolePermissionsByCode failed: %v", err)
	}
	if err := st.SetOrganizationRolePermissionsByCode(ctx, "org_member", []string{" feature.llmapi ", "feature.llmapi", "missing.permission"}); err != nil {
		t.Fatalf("SetOrganizationRolePermissionsByCode failed: %v", err)
	}

	if err := st.SetUserGlobalRoles(ctx, user.ID, []string{" user ", "user", "missing_role"}); err != nil {
		t.Fatalf("SetUserGlobalRoles failed: %v", err)
	}
	if err := st.SetUserOrganizationRoles(ctx, user.ID, []string{" org_member ", "org_member", "missing_role"}); err != nil {
		t.Fatalf("SetUserOrganizationRoles failed: %v", err)
	}
	if err := st.SetUserPermissionOverrides(ctx, user.ID, []UserPermissionOverrideInput{
		{PermissionCode: " feature.admin ", Effect: "allow"},
		{PermissionCode: "feature.settings", Effect: "deny"},
		{PermissionCode: "missing.permission", Effect: "allow"},
		{PermissionCode: "feature.llmapi", Effect: "maybe"},
	}); err != nil {
		t.Fatalf("SetUserPermissionOverrides failed: %v", err)
	}

	globalRoles, err := st.GetGlobalRoleCodesByUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetGlobalRoleCodesByUser failed: %v", err)
	}
	if len(globalRoles) != 1 || globalRoles[0] != "user" {
		t.Fatalf("unexpected global roles: %+v", globalRoles)
	}

	orgRoles, err := st.GetOrganizationRoleCodesByUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetOrganizationRoleCodesByUser failed: %v", err)
	}
	if len(orgRoles) != 1 || orgRoles[0] != "org_member" {
		t.Fatalf("unexpected organization roles: %+v", orgRoles)
	}

	overrides, err := st.GetUserPermissionOverrides(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserPermissionOverrides failed: %v", err)
	}
	if len(overrides) != 2 {
		t.Fatalf("expected 2 valid overrides, got %+v", overrides)
	}

	permissions, err := st.ResolveEffectivePermissions(ctx, user.ID)
	if err != nil {
		t.Fatalf("ResolveEffectivePermissions failed: %v", err)
	}
	joined := strings.Join(permissions, ",")
	if !strings.Contains(joined, "feature.admin") {
		t.Fatalf("expected feature.admin to be allowed, got %v", permissions)
	}
	if !strings.Contains(joined, "feature.llmapi") {
		t.Fatalf("expected feature.llmapi from org role permissions, got %v", permissions)
	}
	if strings.Contains(joined, "feature.settings") {
		t.Fatalf("expected feature.settings to be denied, got %v", permissions)
	}
}

func TestRolePermissionSetters_NoOpForUnknownRoleCodes(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t, "setters-missing-role.sqlite")

	if err := st.SetGlobalRolePermissionsByCode(ctx, "missing_global_role", []string{"feature.settings"}); err != nil {
		t.Fatalf("SetGlobalRolePermissionsByCode failed: %v", err)
	}
	if err := st.SetOrganizationRolePermissionsByCode(ctx, "missing_org_role", []string{"feature.llmapi"}); err != nil {
		t.Fatalf("SetOrganizationRolePermissionsByCode failed: %v", err)
	}

	var globalCount int
	if err := st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM global_role_permissions`).Scan(&globalCount); err != nil {
		t.Fatalf("failed to count global role permissions: %v", err)
	}
	if globalCount == 0 {
		t.Fatal("expected seeded global role permissions to remain present")
	}

	var orgCount int
	if err := st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM organization_role_permissions`).Scan(&orgCount); err != nil {
		t.Fatalf("failed to count organization role permissions: %v", err)
	}
	if orgCount == 0 {
		t.Fatal("expected seeded organization role permissions to remain present")
	}

	var unknownGlobal int
	if err := st.DB.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM global_role_permissions grp
		JOIN global_roles gr ON gr.id = grp.global_role_id
		WHERE gr.code = ?
	`, "missing_global_role").Scan(&unknownGlobal); err != nil {
		t.Fatalf("failed to count unknown global role permissions: %v", err)
	}
	if unknownGlobal != 0 {
		t.Fatalf("expected no permissions for missing global role, got %d", unknownGlobal)
	}

	var unknownOrg int
	if err := st.DB.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM organization_role_permissions orp
		JOIN organization_roles orr ON orr.id = orp.organization_role_id
		WHERE orr.code = ?
	`, "missing_org_role").Scan(&unknownOrg); err != nil {
		t.Fatalf("failed to count unknown organization role permissions: %v", err)
	}
	if unknownOrg != 0 {
		t.Fatalf("expected no permissions for missing organization role, got %d", unknownOrg)
	}
}

func TestStoreMetaHelpers(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, t.TempDir()+"/meta.sqlite")
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	// hasColumn should work for refresh_sessions
	ok, err := hasColumn(ctx, st.DB, "refresh_sessions", "session_type")
	if err != nil {
		t.Fatalf("hasColumn failed: %v", err)
	}
	if !ok {
		t.Fatal("expected refresh_sessions to have session_type")
	}

	// should error for unsupported table
	if _, err := hasColumn(ctx, st.DB, "unknown", "x"); err == nil {
		t.Fatal("expected error for unsupported table")
	}

	ok, err = st.RefreshSessionHasTypeColumn(ctx)
	if err != nil {
		t.Fatalf("RefreshSessionHasTypeColumn failed: %v", err)
	}
	if !ok {
		t.Fatal("expected RefreshSessionHasTypeColumn to be true")
	}

	if !isNoRows(sql.ErrNoRows) {
		t.Fatal("expected isNoRows to recognize sql.ErrNoRows")
	}
}

func ptrString(v string) *string { return &v }
