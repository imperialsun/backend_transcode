package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"demeter-backend/internal/auth"
)

func TestStoreCoreFlows(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "integration.sqlite")
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer closeTestStore(t, st)

	// Ensure basic catalog and roles are present
	orgs, err := st.ListOrganizations(ctx)
	if err != nil {
		t.Fatalf("ListOrganizations failed: %v", err)
	}
	if len(orgs) != 0 {
		t.Fatalf("expected no organizations initially, got %d", len(orgs))
	}

	// Create and query organization
	org, err := st.CreateOrganization(ctx, "Int Org", "int-org", "active")
	if err != nil {
		t.Fatalf("CreateOrganization failed: %v", err)
	}
	if org.ID == "" {
		t.Fatal("expected organization id")
	}
	byID, err := st.GetOrganizationByID(ctx, org.ID)
	if err != nil {
		t.Fatalf("GetOrganizationByID failed: %v", err)
	}
	if byID == nil || byID.ID != org.ID {
		t.Fatalf("unexpected org returned: %+v", byID)
	}

	// Create user and update
	passwordHash, err := auth.HashPassword("Pass1234!")
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}
	user, err := st.CreateUser(ctx, org.ID, "int@example.com", passwordHash, "active")
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}
	if user.Email != "int@example.com" {
		t.Fatalf("unexpected user email: %q", user.Email)
	}

	// Find user by email
	found, err := st.FindUserByEmail(ctx, "int@example.com")
	if err != nil {
		t.Fatalf("FindUserByEmail failed: %v", err)
	}
	if found == nil || found.ID != user.ID {
		t.Fatalf("expected found user, got %+v", found)
	}

	// Update user status and email
	newEmail := "changed@example.com"
	newStatus := "inactive"
	updated, err := st.UpdateUser(ctx, user.ID, UpdateUserInput{Email: &newEmail, Status: &newStatus})
	if err != nil {
		t.Fatalf("UpdateUser failed: %v", err)
	}
	if updated.Email != newEmail || updated.Status != newStatus {
		t.Fatalf("unexpected updated user: %+v", updated)
	}

	if ok, err := st.IsUserInOrganization(ctx, user.ID, org.ID); err != nil {
		t.Fatalf("IsUserInOrganization failed: %v", err)
	} else if !ok {
		t.Fatal("expected user to be in organization")
	}

	// Global roles / organization roles
	if err := st.SetUserGlobalRoles(ctx, user.ID, []string{"user"}); err != nil {
		t.Fatalf("SetUserGlobalRoles failed: %v", err)
	}
	if err := st.SetUserOrganizationRoles(ctx, user.ID, []string{"org_member"}); err != nil {
		t.Fatalf("SetUserOrganizationRoles failed: %v", err)
	}
	if codes, err := st.GetGlobalRoleCodesByUser(ctx, user.ID); err != nil {
		t.Fatalf("GetGlobalRoleCodesByUser failed: %v", err)
	} else if len(codes) == 0 {
		t.Fatal("expected global role codes")
	}
	if codes, err := st.GetOrganizationRoleCodesByUser(ctx, user.ID); err != nil {
		t.Fatalf("GetOrganizationRoleCodesByUser failed: %v", err)
	} else if len(codes) == 0 {
		t.Fatal("expected organization role codes")
	}

	// Permission overrides
	if err := st.SetUserPermissionOverrides(ctx, user.ID, []UserPermissionOverrideInput{{PermissionCode: "feature.settings", Effect: "allow"}}); err != nil {
		t.Fatalf("SetUserPermissionOverrides failed: %v", err)
	}
	po, err := st.GetUserPermissionOverrides(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserPermissionOverrides failed: %v", err)
	}
	if len(po) != 1 || po[0].PermissionCode != "feature.settings" {
		t.Fatalf("unexpected permission overrides: %+v", po)
	}

	// Settings
	settings, err := st.SaveUserSettings(ctx, user.ID, org.ID, json.RawMessage(`{"opt":true}`), 2)
	if err != nil {
		t.Fatalf("SaveUserSettings failed: %v", err)
	}
	if settings.SchemaVersion != 2 {
		t.Fatalf("unexpected schema version: %d", settings.SchemaVersion)
	}
	loaded, err := st.GetUserSettings(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserSettings failed: %v", err)
	}
	if loaded == nil || loaded.SchemaVersion != 2 {
		t.Fatalf("expected loaded settings, got %+v", loaded)
	}

	// Refresh sessions and password resets
	refresh, err := auth.NewRefreshToken(time.Hour)
	if err != nil {
		t.Fatalf("NewRefreshToken failed: %v", err)
	}
	if err := st.SaveRefreshSession(ctx, RefreshSession{
		ID:             refresh.SessionID,
		UserID:         user.ID,
		OrganizationID: org.ID,
		SessionType:    auth.SessionTypeApp.String(),
		TokenHash:      refresh.Hash,
		ExpiresAt:      refresh.ExpiresAt,
		CreatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveRefreshSession failed: %v", err)
	}
	got, err := st.GetRefreshSessionByID(ctx, refresh.SessionID)
	if err != nil {
		t.Fatalf("GetRefreshSessionByID failed: %v", err)
	}
	if got == nil || got.UserID != user.ID {
		t.Fatalf("unexpected refresh session record: %+v", got)
	}
	if err := st.RotateRefreshSession(ctx, refresh.SessionID, "newhash", time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("RotateRefreshSession failed: %v", err)
	}
	if err := st.RevokeRefreshSession(ctx, refresh.SessionID); err != nil {
		t.Fatalf("RevokeRefreshSession failed: %v", err)
	}
	if err := st.RevokeRefreshSessionsByUser(ctx, user.ID); err != nil {
		t.Fatalf("RevokeRefreshSessionsByUser failed: %v", err)
	}

	// Password reset token flows
	token := auth.HashPasswordResetToken("reset123")
	if err := st.SavePasswordResetToken(ctx, PasswordResetToken{UserID: user.ID, TokenHash: token, ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatalf("SavePasswordResetToken failed: %v", err)
	}
	if got, err := st.GetPasswordResetTokenByHash(ctx, token); err != nil {
		t.Fatalf("GetPasswordResetTokenByHash failed: %v", err)
	} else if got == nil {
		t.Fatal("expected token record")
	}

	if err := st.ConsumePasswordResetToken(ctx, got.ID); err != nil {
		t.Fatalf("ConsumePasswordResetToken failed: %v", err)
	}
	if err := st.RevokePasswordResetTokensByUser(ctx, user.ID, ""); err != nil {
		t.Fatalf("RevokePasswordResetTokensByUser failed: %v", err)
	}

	// Apply password reset should succeed when record exists (create fresh one)
	newToken := auth.HashPasswordResetToken("reset-apply")
	if err := st.SavePasswordResetToken(ctx, PasswordResetToken{UserID: user.ID, TokenHash: newToken, ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatalf("SavePasswordResetToken failed: %v", err)
	}
	applied, err := st.ApplyPasswordReset(ctx, newToken, "new-hash", auth.SessionTypeApp.String())
	if err != nil {
		t.Fatalf("ApplyPasswordReset failed: %v", err)
	}
	if applied == nil || !applied.UsedAt.Valid {
		t.Fatalf("expected applied token record, got %+v", applied)
	}

	// Catalog listing helpers
	if _, err := st.ListGlobalRolesCatalog(ctx); err != nil {
		t.Fatalf("ListGlobalRolesCatalog failed: %v", err)
	}
	if _, err := st.ListOrganizationRolesCatalog(ctx); err != nil {
		t.Fatalf("ListOrganizationRolesCatalog failed: %v", err)
	}
	if _, err := st.ListPermissionsCatalog(ctx); err != nil {
		t.Fatalf("ListPermissionsCatalog failed: %v", err)
	}
}

func TestStoreSessionAndReset_DefaultsAndMissingPaths(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t, "session-defaults.sqlite")

	org := createOrg(t, st, "Defaults Org", "defaults-org", "active")
	user := createUserWithPassword(t, st, org.ID, "defaults@example.com", "ChangeMe123!", "active")

	if settings, err := st.GetUserSettings(ctx, "missing-user"); err != nil {
		t.Fatalf("GetUserSettings failed: %v", err)
	} else if settings != nil {
		t.Fatalf("expected no settings for missing user, got %+v", settings)
	}

	savedSettings, err := st.SaveUserSettings(ctx, user.ID, org.ID, nil, 0)
	if err != nil {
		t.Fatalf("SaveUserSettings failed: %v", err)
	}
	if savedSettings.SchemaVersion != 1 || string(savedSettings.Settings) != "{}" {
		t.Fatalf("expected default schema/settings, got %+v", savedSettings)
	}

	refresh, err := auth.NewRefreshToken(time.Hour)
	if err != nil {
		t.Fatalf("NewRefreshToken failed: %v", err)
	}
	if err := st.SaveRefreshSession(ctx, RefreshSession{
		ID:             refresh.SessionID,
		UserID:         user.ID,
		OrganizationID: org.ID,
		TokenHash:      refresh.Hash,
		ExpiresAt:      refresh.ExpiresAt,
		CreatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveRefreshSession failed: %v", err)
	}
	gotRefresh, err := st.GetRefreshSessionByID(ctx, refresh.SessionID)
	if err != nil {
		t.Fatalf("GetRefreshSessionByID failed: %v", err)
	}
	if gotRefresh == nil || gotRefresh.SessionType != auth.SessionTypeApp.String() {
		t.Fatalf("expected default app session type, got %+v", gotRefresh)
	}
	missingRefresh, err := st.GetRefreshSessionByID(ctx, "missing-session")
	if err != nil {
		t.Fatalf("GetRefreshSessionByID for missing session failed: %v", err)
	}
	if missingRefresh != nil {
		t.Fatalf("expected nil missing refresh session, got %+v", missingRefresh)
	}

	resetHash := auth.HashPasswordResetToken("defaults-reset")
	if err := st.SavePasswordResetToken(ctx, PasswordResetToken{
		UserID:      user.ID,
		TokenHash:   resetHash,
		ExpiresAt:   time.Now().UTC().Add(time.Hour),
		CreatedAt:   time.Now().UTC(),
		SessionType: "",
	}); err != nil {
		t.Fatalf("SavePasswordResetToken failed: %v", err)
	}
	gotReset, err := st.GetPasswordResetTokenByHash(ctx, resetHash)
	if err != nil {
		t.Fatalf("GetPasswordResetTokenByHash failed: %v", err)
	}
	if gotReset == nil || gotReset.SessionType != auth.SessionTypeApp.String() {
		t.Fatalf("expected default app reset session type, got %+v", gotReset)
	}
	missingReset, err := st.GetPasswordResetTokenByHash(ctx, "missing-hash")
	if err != nil {
		t.Fatalf("GetPasswordResetTokenByHash for missing hash failed: %v", err)
	}
	if missingReset != nil {
		t.Fatalf("expected nil missing reset token, got %+v", missingReset)
	}
}

func TestOrganizationAndUserLookups_HandleMissingAndCaseInsensitiveEmail(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t, "lookup-paths.sqlite")

	org := createOrg(t, st, "Lookup Org", "lookup-org", "active")
	user := createUserWithPassword(t, st, org.ID, "Lookup@Example.com", "ChangeMe123!", "active")

	missingOrg, err := st.GetOrganizationByID(ctx, "missing-org")
	if err != nil {
		t.Fatalf("GetOrganizationByID failed: %v", err)
	}
	if missingOrg != nil {
		t.Fatalf("expected nil missing organization, got %+v", missingOrg)
	}

	found, err := st.FindUserByEmail(ctx, "lookup@example.com")
	if err != nil {
		t.Fatalf("FindUserByEmail failed: %v", err)
	}
	if found == nil || found.ID != user.ID {
		t.Fatalf("expected case-insensitive email lookup to find user, got %+v", found)
	}

	missingUser, err := st.FindUserByEmail(ctx, "missing@example.com")
	if err != nil {
		t.Fatalf("FindUserByEmail failed: %v", err)
	}
	if missingUser != nil {
		t.Fatalf("expected nil missing user, got %+v", missingUser)
	}
}

func TestApplyPasswordReset_RejectsWrongNamespaceExpiredAndUsed(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t, "apply-reset.sqlite")

	org := createOrg(t, st, "Apply Org", "apply-org", "active")
	user := createUserWithPassword(t, st, org.ID, "apply@example.com", "ChangeMe123!", "active")

	makeToken := func(raw string, expiresAt time.Time, sessionType string) string {
		hash := auth.HashPasswordResetToken(raw)
		if err := st.SavePasswordResetToken(ctx, PasswordResetToken{
			UserID:      user.ID,
			SessionType: sessionType,
			TokenHash:   hash,
			ExpiresAt:   expiresAt,
			CreatedAt:   time.Now().UTC(),
		}); err != nil {
			t.Fatalf("SavePasswordResetToken failed for %s: %v", raw, err)
		}
		return hash
	}

	wrongNamespace := makeToken("wrong-namespace", time.Now().UTC().Add(time.Hour), auth.SessionTypeAdmin.String())
	if record, err := st.ApplyPasswordReset(ctx, wrongNamespace, "new-hash", auth.SessionTypeApp.String()); err != nil {
		t.Fatalf("ApplyPasswordReset failed: %v", err)
	} else if record != nil {
		t.Fatalf("expected nil record for wrong namespace, got %+v", record)
	}

	expired := makeToken("expired", time.Now().UTC().Add(-time.Minute), auth.SessionTypeApp.String())
	if record, err := st.ApplyPasswordReset(ctx, expired, "new-hash", auth.SessionTypeApp.String()); err != nil {
		t.Fatalf("ApplyPasswordReset failed: %v", err)
	} else if record != nil {
		t.Fatalf("expected nil record for expired token, got %+v", record)
	}

	used := makeToken("used", time.Now().UTC().Add(time.Hour), auth.SessionTypeApp.String())
	usedRecord, err := st.GetPasswordResetTokenByHash(ctx, used)
	if err != nil {
		t.Fatalf("GetPasswordResetTokenByHash failed: %v", err)
	}
	if err := st.ConsumePasswordResetToken(ctx, usedRecord.ID); err != nil {
		t.Fatalf("ConsumePasswordResetToken failed: %v", err)
	}
	if record, err := st.ApplyPasswordReset(ctx, used, "new-hash", auth.SessionTypeApp.String()); err != nil {
		t.Fatalf("ApplyPasswordReset failed: %v", err)
	} else if record != nil {
		t.Fatalf("expected nil record for used token, got %+v", record)
	}
}

func TestApplyPasswordReset_UpdatesPasswordAndRevokesSessionsAndTokens(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t, "apply-reset-success.sqlite")

	org := createOrg(t, st, "Apply Success Org", "apply-success-org", "active")
	user := createUserWithPassword(t, st, org.ID, "apply-success@example.com", "ChangeMe123!", "active")

	if record, err := st.ApplyPasswordReset(ctx, "missing-token", "ignored", auth.SessionTypeApp.String()); err != nil {
		t.Fatalf("ApplyPasswordReset for missing token failed: %v", err)
	} else if record != nil {
		t.Fatalf("expected nil record for missing token, got %+v", record)
	}

	appRefresh, err := auth.NewRefreshToken(time.Hour)
	if err != nil {
		t.Fatalf("NewRefreshToken failed: %v", err)
	}
	adminRefresh, err := auth.NewRefreshToken(time.Hour)
	if err != nil {
		t.Fatalf("NewRefreshToken failed: %v", err)
	}
	for _, session := range []RefreshSession{
		{
			ID:             appRefresh.SessionID,
			UserID:         user.ID,
			OrganizationID: org.ID,
			SessionType:    auth.SessionTypeApp.String(),
			TokenHash:      appRefresh.Hash,
			ExpiresAt:      appRefresh.ExpiresAt,
			CreatedAt:      time.Now().UTC(),
		},
		{
			ID:             adminRefresh.SessionID,
			UserID:         user.ID,
			OrganizationID: org.ID,
			SessionType:    auth.SessionTypeAdmin.String(),
			TokenHash:      adminRefresh.Hash,
			ExpiresAt:      adminRefresh.ExpiresAt,
			CreatedAt:      time.Now().UTC(),
		},
	} {
		if err := st.SaveRefreshSession(ctx, session); err != nil {
			t.Fatalf("SaveRefreshSession failed: %v", err)
		}
	}

	appResetHash := auth.HashPasswordResetToken("apply-success-app")
	adminResetHash := auth.HashPasswordResetToken("apply-success-admin")
	for _, token := range []PasswordResetToken{
		{
			UserID:      user.ID,
			SessionType: auth.SessionTypeApp.String(),
			TokenHash:   appResetHash,
			ExpiresAt:   time.Now().UTC().Add(time.Hour),
			CreatedAt:   time.Now().UTC(),
		},
		{
			UserID:      user.ID,
			SessionType: auth.SessionTypeAdmin.String(),
			TokenHash:   adminResetHash,
			ExpiresAt:   time.Now().UTC().Add(time.Hour),
			CreatedAt:   time.Now().UTC(),
		},
	} {
		if err := st.SavePasswordResetToken(ctx, token); err != nil {
			t.Fatalf("SavePasswordResetToken failed: %v", err)
		}
	}

	newPasswordHash, err := auth.HashPassword("EvenBetter123!")
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}

	applied, err := st.ApplyPasswordReset(ctx, appResetHash, newPasswordHash, auth.SessionTypeApp.String())
	if err != nil {
		t.Fatalf("ApplyPasswordReset failed: %v", err)
	}
	if applied == nil || !applied.UsedAt.Valid || applied.ID == "" {
		t.Fatalf("expected applied token record, got %+v", applied)
	}

	reloadedUser, err := st.GetUserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserByID failed: %v", err)
	}
	if reloadedUser == nil || !auth.VerifyPassword(reloadedUser.PasswordHash, "EvenBetter123!") {
		t.Fatalf("expected password hash to be updated, got %+v", reloadedUser)
	}

	for _, sessionID := range []string{appRefresh.SessionID, adminRefresh.SessionID} {
		record, err := st.GetRefreshSessionByID(ctx, sessionID)
		if err != nil {
			t.Fatalf("GetRefreshSessionByID failed: %v", err)
		}
		if record == nil || !record.RevokedAt.Valid {
			t.Fatalf("expected refresh session %s to be revoked, got %+v", sessionID, record)
		}
	}

	for _, tokenHash := range []string{appResetHash, adminResetHash} {
		record, err := st.GetPasswordResetTokenByHash(ctx, tokenHash)
		if err != nil {
			t.Fatalf("GetPasswordResetTokenByHash failed: %v", err)
		}
		if record == nil || !record.UsedAt.Valid {
			t.Fatalf("expected password reset token %s to be used, got %+v", tokenHash, record)
		}
	}
}

func TestRevokePasswordResetTokensByUser_FiltersBySessionType(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t, "revoke-reset-filter.sqlite")

	org := createOrg(t, st, "Reset Org", "reset-org", "active")
	user := createUserWithPassword(t, st, org.ID, "resetfilter@example.com", "ChangeMe123!", "active")

	appHash := auth.HashPasswordResetToken("app-token")
	adminHash := auth.HashPasswordResetToken("admin-token")
	for _, item := range []struct {
		hash        string
		sessionType string
	}{
		{hash: appHash, sessionType: auth.SessionTypeApp.String()},
		{hash: adminHash, sessionType: auth.SessionTypeAdmin.String()},
	} {
		if err := st.SavePasswordResetToken(ctx, PasswordResetToken{
			UserID:      user.ID,
			SessionType: item.sessionType,
			TokenHash:   item.hash,
			ExpiresAt:   time.Now().UTC().Add(time.Hour),
			CreatedAt:   time.Now().UTC(),
		}); err != nil {
			t.Fatalf("SavePasswordResetToken failed: %v", err)
		}
	}

	if err := st.RevokePasswordResetTokensByUser(ctx, user.ID, auth.SessionTypeApp.String()); err != nil {
		t.Fatalf("RevokePasswordResetTokensByUser failed: %v", err)
	}

	appToken, err := st.GetPasswordResetTokenByHash(ctx, appHash)
	if err != nil {
		t.Fatalf("GetPasswordResetTokenByHash failed: %v", err)
	}
	adminToken, err := st.GetPasswordResetTokenByHash(ctx, adminHash)
	if err != nil {
		t.Fatalf("GetPasswordResetTokenByHash failed: %v", err)
	}
	if appToken == nil || !appToken.UsedAt.Valid {
		t.Fatalf("expected app token to be revoked, got %+v", appToken)
	}
	if adminToken == nil || adminToken.UsedAt.Valid {
		t.Fatalf("expected admin token to remain active, got %+v", adminToken)
	}
}
