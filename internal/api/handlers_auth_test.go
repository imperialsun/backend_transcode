package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/config"
	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
)

func TestAdminLogin_SetsDedicatedCookiesAndSessionType(t *testing.T) {
	app, appCtx, st, user := setupLoginRoutesTest(t)
	if err := st.SetUserGlobalRoles(context.Background(), user.ID, []string{"super_admin"}); err != nil {
		t.Fatalf("failed to set global roles: %v", err)
	}
	if err := st.SetUserOrganizationRoles(context.Background(), user.ID, []string{"org_admin"}); err != nil {
		t.Fatalf("failed to set org roles: %v", err)
	}

	resp := performLoginRequest(t, app, "/api/v1/admin/auth/login", map[string]string{
		"email":    user.Email,
		"password": "ChangeMe123!",
	})

	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var payload AuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if payload.RuntimeMode != "admin" {
		t.Fatalf("expected runtime mode admin, got %q", payload.RuntimeMode)
	}
	if strings.TrimSpace(payload.CsrfToken) == "" {
		t.Fatalf("expected csrf token in admin login response")
	}

	assertSetCookieContains(t, resp, auth.AdminAccessCookieName, "Path="+auth.AdminAccessCookiePath)
	assertSetCookieContains(t, resp, auth.AdminAccessCookieName, "HttpOnly")
	assertSetCookieContains(t, resp, auth.AdminAccessCookieName, "Secure")
	assertSetCookieContains(t, resp, auth.AdminAccessCookieName, "SameSite=Strict")
	assertSetCookieContains(t, resp, auth.AdminRefreshCookieName, "Path="+auth.AdminRefreshCookiePath)
	assertSetCookieContains(t, resp, auth.AdminRefreshCookieName, "HttpOnly")
	assertSetCookieContains(t, resp, auth.AdminRefreshCookieName, "Secure")
	assertSetCookieContains(t, resp, auth.AdminRefreshCookieName, "SameSite=Strict")

	adminRefresh := findCookie(t, resp, auth.AdminRefreshCookieName)
	sessionID, _, err := auth.ParseRefreshToken(adminRefresh.Value)
	if err != nil {
		t.Fatalf("failed to parse refresh cookie: %v", err)
	}
	session, err := appCtx.Store.GetRefreshSessionByID(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("failed to load refresh session: %v", err)
	}
	if session == nil || session.SessionType != auth.SessionTypeAdmin.String() {
		t.Fatalf("expected admin refresh session type, got %#v", session)
	}
}

func TestAppLogin_SetsDedicatedCookiesWithoutCSRFToken(t *testing.T) {
	app, appCtx, st, user := setupLoginRoutesTest(t)
	if err := st.SetUserGlobalRoles(context.Background(), user.ID, []string{"user"}); err != nil {
		t.Fatalf("failed to set global roles: %v", err)
	}

	resp := performLoginRequest(t, app, "/api/v1/auth/login", map[string]string{
		"email":    user.Email,
		"password": "ChangeMe123!",
	})

	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var payload AuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if payload.RuntimeMode != "backend" {
		t.Fatalf("expected runtime mode backend, got %q", payload.RuntimeMode)
	}
	if strings.TrimSpace(payload.CsrfToken) != "" {
		t.Fatalf("expected no csrf token for app login, got %q", payload.CsrfToken)
	}

	assertSetCookieContains(t, resp, auth.AppAccessCookieName, "Path="+auth.AppAccessCookiePath)
	assertSetCookieContains(t, resp, auth.AppRefreshCookieName, "Path="+auth.AppRefreshCookiePath)
	assertSetCookieContains(t, resp, auth.AppAccessCookieName, "SameSite=Strict")
	assertSetCookieContains(t, resp, auth.AppRefreshCookieName, "SameSite=Strict")

	appRefresh := findCookie(t, resp, auth.AppRefreshCookieName)
	sessionID, _, err := auth.ParseRefreshToken(appRefresh.Value)
	if err != nil {
		t.Fatalf("failed to parse refresh cookie: %v", err)
	}
	session, err := appCtx.Store.GetRefreshSessionByID(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("failed to load refresh session: %v", err)
	}
	if session == nil || session.SessionType != auth.SessionTypeApp.String() {
		t.Fatalf("expected app refresh session type, got %#v", session)
	}
}

func TestAppAuthSessionLifecycle(t *testing.T) {
	app, _, st, user := setupLoginRoutesTest(t)
	if err := st.SetUserGlobalRoles(context.Background(), user.ID, []string{"user"}); err != nil {
		t.Fatalf("failed to set global roles: %v", err)
	}

	loginResp := performLoginRequest(t, app, "/api/v1/auth/login", map[string]string{
		"email":    user.Email,
		"password": "ChangeMe123!",
	})
	if loginResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for login, got %d", loginResp.StatusCode)
	}

	accessCookie := findCookie(t, loginResp, auth.AppAccessCookieName)
	refreshCookie := findCookie(t, loginResp, auth.AppRefreshCookieName)

	meResp := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodGet,
		"/api/v1/auth/me",
		nil,
		[]*http.Cookie{accessCookie},
		nil,
	)
	if meResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for me, got %d", meResp.StatusCode)
	}
	var mePayload AuthResponse
	if err := json.NewDecoder(meResp.Body).Decode(&mePayload); err != nil {
		t.Fatalf("failed to decode me response: %v", err)
	}
	if mePayload.RuntimeMode != "backend" || mePayload.User.ID != user.ID {
		t.Fatalf("unexpected me payload: %+v", mePayload)
	}

	refreshResp := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodPost,
		"/api/v1/auth/refresh",
		nil,
		[]*http.Cookie{refreshCookie},
		nil,
	)
	if refreshResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for refresh, got %d", refreshResp.StatusCode)
	}
	assertSetCookieContains(t, refreshResp, auth.AppAccessCookieName, "Path="+auth.AppAccessCookiePath)
	assertSetCookieContains(t, refreshResp, auth.AppRefreshCookieName, "Path="+auth.AppRefreshCookiePath)

	logoutResp := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodPost,
		"/api/v1/auth/logout",
		nil,
		[]*http.Cookie{refreshCookie},
		nil,
	)
	if logoutResp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for logout, got %d", logoutResp.StatusCode)
	}
	assertSetCookieContains(t, logoutResp, auth.AppAccessCookieName, "expires=")
	assertSetCookieContains(t, logoutResp, auth.AppRefreshCookieName, "expires=")
}

func TestAppChangePassword_UpdatesPasswordAndRevokesSessions(t *testing.T) {
	app, _, st, user := setupLoginRoutesTest(t)
	ctx := context.Background()
	if err := st.SetUserGlobalRoles(ctx, user.ID, []string{"user"}); err != nil {
		t.Fatalf("failed to set global roles: %v", err)
	}

	loginResp := performLoginRequest(t, app, "/api/v1/auth/login", map[string]string{
		"email":    user.Email,
		"password": "ChangeMe123!",
	})
	if loginResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for login, got %d", loginResp.StatusCode)
	}

	accessCookie := findCookie(t, loginResp, auth.AppAccessCookieName)
	refreshCookie := findCookie(t, loginResp, auth.AppRefreshCookieName)

	now := time.Now().UTC()
	appResetHash := auth.HashPasswordResetToken("app-reset-token")
	adminResetHash := auth.HashPasswordResetToken("admin-reset-token")
	for _, token := range []struct {
		hash        string
		sessionType string
	}{
		{hash: appResetHash, sessionType: auth.SessionTypeApp.String()},
		{hash: adminResetHash, sessionType: auth.SessionTypeAdmin.String()},
	} {
		if err := st.SavePasswordResetToken(ctx, store.PasswordResetToken{
			UserID:      user.ID,
			SessionType: token.sessionType,
			TokenHash:   token.hash,
			ExpiresAt:   now.Add(time.Hour),
			CreatedAt:   now,
		}); err != nil {
			t.Fatalf("SavePasswordResetToken failed: %v", err)
		}
	}

	changeResp := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodPut,
		"/api/v1/auth/me/password",
		map[string]string{
			"currentPassword": "ChangeMe123!",
			"password":        "NewChangeMe123!",
		},
		[]*http.Cookie{accessCookie},
		nil,
	)
	if changeResp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for password change, got %d", changeResp.StatusCode)
	}

	refreshResp := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodPost,
		"/api/v1/auth/refresh",
		nil,
		[]*http.Cookie{refreshCookie},
		nil,
	)
	if refreshResp.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("expected 401 for revoked refresh session, got %d", refreshResp.StatusCode)
	}

	for _, hash := range []string{appResetHash, adminResetHash} {
		record, err := st.GetPasswordResetTokenByHash(ctx, hash)
		if err != nil {
			t.Fatalf("GetPasswordResetTokenByHash failed: %v", err)
		}
		if record == nil || !record.UsedAt.Valid {
			t.Fatalf("expected password reset token %s to be revoked, got %+v", hash, record)
		}
	}

	invalidLogin := performLoginRequest(t, app, "/api/v1/auth/login", map[string]string{
		"email":    user.Email,
		"password": "ChangeMe123!",
	})
	if invalidLogin.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("expected 401 for old password, got %d", invalidLogin.StatusCode)
	}

	newLogin := performLoginRequest(t, app, "/api/v1/auth/login", map[string]string{
		"email":    user.Email,
		"password": "NewChangeMe123!",
	})
	if newLogin.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for new password, got %d", newLogin.StatusCode)
	}
}

func TestAppChangePassword_RejectsWrongCurrentPassword(t *testing.T) {
	app, _, st, user := setupLoginRoutesTest(t)
	ctx := context.Background()
	if err := st.SetUserGlobalRoles(ctx, user.ID, []string{"user"}); err != nil {
		t.Fatalf("failed to set global roles: %v", err)
	}

	loginResp := performLoginRequest(t, app, "/api/v1/auth/login", map[string]string{
		"email":    user.Email,
		"password": "ChangeMe123!",
	})
	if loginResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for login, got %d", loginResp.StatusCode)
	}

	accessCookie := findCookie(t, loginResp, auth.AppAccessCookieName)

	changeResp := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodPut,
		"/api/v1/auth/me/password",
		map[string]string{
			"currentPassword": "wrong-password",
			"password":        "NewChangeMe123!",
		},
		[]*http.Cookie{accessCookie},
		nil,
	)
	if changeResp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("expected 400 for wrong current password, got %d", changeResp.StatusCode)
	}

	unchangedLogin := performLoginRequest(t, app, "/api/v1/auth/login", map[string]string{
		"email":    user.Email,
		"password": "ChangeMe123!",
	})
	if unchangedLogin.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for original password after failed change, got %d", unchangedLogin.StatusCode)
	}
}

func TestAdminAuthSessionLifecycle(t *testing.T) {
	app, _, st, user := setupLoginRoutesTest(t)
	ctx := context.Background()
	if err := st.SetUserGlobalRoles(ctx, user.ID, []string{"super_admin"}); err != nil {
		t.Fatalf("failed to set global roles: %v", err)
	}
	if err := st.SetUserOrganizationRoles(ctx, user.ID, []string{"org_admin"}); err != nil {
		t.Fatalf("failed to set org roles: %v", err)
	}

	loginResp := performLoginRequest(t, app, "/api/v1/admin/auth/login", map[string]string{
		"email":    user.Email,
		"password": "ChangeMe123!",
	})
	if loginResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for admin login, got %d", loginResp.StatusCode)
	}

	accessCookie := findCookie(t, loginResp, auth.AdminAccessCookieName)
	refreshCookie := findCookie(t, loginResp, auth.AdminRefreshCookieName)

	meResp := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodGet,
		"/api/v1/admin/auth/me",
		nil,
		[]*http.Cookie{accessCookie},
		nil,
	)
	if meResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for admin me, got %d", meResp.StatusCode)
	}
	var mePayload AuthResponse
	if err := json.NewDecoder(meResp.Body).Decode(&mePayload); err != nil {
		t.Fatalf("failed to decode admin me response: %v", err)
	}
	if mePayload.RuntimeMode != "admin" || strings.TrimSpace(mePayload.CsrfToken) == "" {
		t.Fatalf("unexpected admin me payload: %+v", mePayload)
	}

	refreshResp := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodPost,
		"/api/v1/admin/auth/refresh",
		nil,
		[]*http.Cookie{refreshCookie},
		nil,
	)
	if refreshResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for admin refresh, got %d", refreshResp.StatusCode)
	}
	assertSetCookieContains(t, refreshResp, auth.AdminAccessCookieName, "Path="+auth.AdminAccessCookiePath)
	assertSetCookieContains(t, refreshResp, auth.AdminRefreshCookieName, "Path="+auth.AdminRefreshCookiePath)

	logoutResp := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodPost,
		"/api/v1/admin/auth/logout",
		nil,
		[]*http.Cookie{refreshCookie},
		nil,
	)
	if logoutResp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for admin logout, got %d", logoutResp.StatusCode)
	}
	assertSetCookieContains(t, logoutResp, auth.AdminAccessCookieName, "expires=")
	assertSetCookieContains(t, logoutResp, auth.AdminRefreshCookieName, "expires=")
}

func TestRefreshRejectsMissingRefreshToken(t *testing.T) {
	app, _, _, _ := setupLoginRoutesTest(t)

	resp := performJSONRequestWithHeaders(t, app, http.MethodPost, "/api/v1/auth/refresh", nil, nil, nil)
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("expected 401 for missing refresh token, got %d", resp.StatusCode)
	}

	adminResp := performJSONRequestWithHeaders(t, app, http.MethodPost, "/api/v1/admin/auth/refresh", nil, nil, nil)
	if adminResp.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("expected 401 for missing admin refresh token, got %d", adminResp.StatusCode)
	}
}

func TestLoginRejectsInvalidPayloadCredentialsAndAdminScope(t *testing.T) {
	app, _, st, user := setupLoginRoutesTest(t)
	ctx := context.Background()

	invalidPayload := performJSONRequest(t, app, http.MethodPost, "/api/v1/auth/login", "", `{"email":`)
	if invalidPayload.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("expected 400 for invalid login payload, got %d", invalidPayload.StatusCode)
	}

	missingFields := performLoginRequest(t, app, "/api/v1/auth/login", map[string]string{"email": user.Email})
	if missingFields.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("expected 400 for missing password, got %d", missingFields.StatusCode)
	}

	wrongPassword := performLoginRequest(t, app, "/api/v1/auth/login", map[string]string{
		"email":    user.Email,
		"password": "wrong-password",
	})
	if wrongPassword.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong password, got %d", wrongPassword.StatusCode)
	}

	inactiveStatus := "inactive"
	if _, err := st.UpdateUser(ctx, user.ID, store.UpdateUserInput{Status: &inactiveStatus}); err != nil {
		t.Fatalf("UpdateUser failed: %v", err)
	}
	inactiveResp := performLoginRequest(t, app, "/api/v1/auth/login", map[string]string{
		"email":    user.Email,
		"password": "ChangeMe123!",
	})
	if inactiveResp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 for inactive user, got %d", inactiveResp.StatusCode)
	}

	activeStatus := "active"
	if _, err := st.UpdateUser(ctx, user.ID, store.UpdateUserInput{Status: &activeStatus}); err != nil {
		t.Fatalf("UpdateUser failed: %v", err)
	}
	if err := st.SetUserGlobalRoles(ctx, user.ID, []string{"user"}); err != nil {
		t.Fatalf("SetUserGlobalRoles failed: %v", err)
	}
	adminResp := performLoginRequest(t, app, "/api/v1/admin/auth/login", map[string]string{
		"email":    user.Email,
		"password": "ChangeMe123!",
	})
	if adminResp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 for admin login without admin scope, got %d", adminResp.StatusCode)
	}
}

func TestRefreshRejectsInvalidAndWrongAudienceTokens(t *testing.T) {
	app, appCtx, st, user := setupLoginRoutesTest(t)
	ctx := context.Background()
	if err := st.SetUserGlobalRoles(ctx, user.ID, []string{"user"}); err != nil {
		t.Fatalf("SetUserGlobalRoles failed: %v", err)
	}

	loginResp := performLoginRequest(t, app, "/api/v1/auth/login", map[string]string{
		"email":    user.Email,
		"password": "ChangeMe123!",
	})
	if loginResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for login, got %d", loginResp.StatusCode)
	}

	refreshCookie := findCookie(t, loginResp, auth.AppRefreshCookieName)
	invalidFormat := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodPost,
		"/api/v1/auth/refresh",
		nil,
		[]*http.Cookie{{Name: auth.AppRefreshCookieName, Value: "bad-token"}},
		nil,
	)
	if invalidFormat.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid refresh format, got %d", invalidFormat.StatusCode)
	}

	sessionID, _, err := auth.ParseRefreshToken(refreshCookie.Value)
	if err != nil {
		t.Fatalf("ParseRefreshToken failed: %v", err)
	}
	session, err := appCtx.Store.GetRefreshSessionByID(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetRefreshSessionByID failed: %v", err)
	}
	if session == nil {
		t.Fatal("expected refresh session to exist")
	}
	session.SessionType = auth.SessionTypeAdmin.String()
	if _, err := appCtx.Store.DB.ExecContext(ctx, `UPDATE refresh_sessions SET session_type = ? WHERE id = ?`, session.SessionType, session.ID); err != nil {
		t.Fatalf("failed to update refresh session type: %v", err)
	}

	wrongAudience := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodPost,
		"/api/v1/auth/refresh",
		nil,
		[]*http.Cookie{refreshCookie},
		nil,
	)
	if wrongAudience.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong refresh session audience, got %d", wrongAudience.StatusCode)
	}
}

func TestRefreshRejectsRevokedExpiredInvalidHashAndInactiveUser(t *testing.T) {
	app, appCtx, st, user := setupLoginRoutesTest(t)
	ctx := context.Background()
	if err := st.SetUserGlobalRoles(ctx, user.ID, []string{"user"}); err != nil {
		t.Fatalf("SetUserGlobalRoles failed: %v", err)
	}

	login := func() (*http.Cookie, string) {
		resp := performLoginRequest(t, app, "/api/v1/auth/login", map[string]string{
			"email":    user.Email,
			"password": "ChangeMe123!",
		})
		if resp.StatusCode != fiber.StatusOK {
			t.Fatalf("expected 200 for login, got %d", resp.StatusCode)
		}
		refreshCookie := findCookie(t, resp, auth.AppRefreshCookieName)
		sessionID, _, err := auth.ParseRefreshToken(refreshCookie.Value)
		if err != nil {
			t.Fatalf("ParseRefreshToken failed: %v", err)
		}
		return refreshCookie, sessionID
	}

	t.Run("revoked session", func(t *testing.T) {
		refreshCookie, sessionID := login()
		if _, err := appCtx.Store.DB.ExecContext(ctx, `UPDATE refresh_sessions SET revoked_at = ? WHERE id = ?`, time.Now().UTC(), sessionID); err != nil {
			t.Fatalf("failed to revoke session: %v", err)
		}
		resp := performJSONRequestWithHeaders(t, app, http.MethodPost, "/api/v1/auth/refresh", nil, []*http.Cookie{refreshCookie}, nil)
		if resp.StatusCode != fiber.StatusUnauthorized {
			t.Fatalf("expected 401 for revoked session, got %d", resp.StatusCode)
		}
	})

	t.Run("expired session", func(t *testing.T) {
		refreshCookie, sessionID := login()
		if _, err := appCtx.Store.DB.ExecContext(ctx, `UPDATE refresh_sessions SET revoked_at = NULL, expires_at = ? WHERE id = ?`, time.Now().UTC().Add(-time.Minute), sessionID); err != nil {
			t.Fatalf("failed to expire session: %v", err)
		}
		resp := performJSONRequestWithHeaders(t, app, http.MethodPost, "/api/v1/auth/refresh", nil, []*http.Cookie{refreshCookie}, nil)
		if resp.StatusCode != fiber.StatusUnauthorized {
			t.Fatalf("expected 401 for expired session, got %d", resp.StatusCode)
		}
	})

	t.Run("invalid hash revokes session", func(t *testing.T) {
		refreshCookie, sessionID := login()
		if _, err := appCtx.Store.DB.ExecContext(ctx, `UPDATE refresh_sessions SET revoked_at = NULL, expires_at = ?, refresh_hash = ? WHERE id = ?`, time.Now().UTC().Add(time.Hour), "not-the-right-hash", sessionID); err != nil {
			t.Fatalf("failed to poison session hash: %v", err)
		}
		resp := performJSONRequestWithHeaders(t, app, http.MethodPost, "/api/v1/auth/refresh", nil, []*http.Cookie{refreshCookie}, nil)
		if resp.StatusCode != fiber.StatusUnauthorized {
			t.Fatalf("expected 401 for invalid hash, got %d", resp.StatusCode)
		}
		record, err := appCtx.Store.GetRefreshSessionByID(ctx, sessionID)
		if err != nil {
			t.Fatalf("GetRefreshSessionByID failed: %v", err)
		}
		if record == nil || !record.RevokedAt.Valid {
			t.Fatalf("expected invalid hash to revoke session, got %+v", record)
		}
	})

	t.Run("inactive user revokes session", func(t *testing.T) {
		refreshCookie, sessionID := login()
		inactive := "inactive"
		if _, err := st.UpdateUser(ctx, user.ID, store.UpdateUserInput{Status: &inactive}); err != nil {
			t.Fatalf("UpdateUser failed: %v", err)
		}
		resp := performJSONRequestWithHeaders(t, app, http.MethodPost, "/api/v1/auth/refresh", nil, []*http.Cookie{refreshCookie}, nil)
		if resp.StatusCode != fiber.StatusUnauthorized {
			t.Fatalf("expected 401 for inactive user, got %d", resp.StatusCode)
		}
		record, err := appCtx.Store.GetRefreshSessionByID(ctx, sessionID)
		if err != nil {
			t.Fatalf("GetRefreshSessionByID failed: %v", err)
		}
		if record == nil || !record.RevokedAt.Valid {
			t.Fatalf("expected inactive user refresh to revoke session, got %+v", record)
		}
		active := "active"
		if _, err := st.UpdateUser(ctx, user.ID, store.UpdateUserInput{Status: &active}); err != nil {
			t.Fatalf("UpdateUser failed: %v", err)
		}
	})
}

func TestAdminRefreshRejectsLostAdminScope(t *testing.T) {
	app, appCtx, st, user := setupLoginRoutesTest(t)
	ctx := context.Background()
	if err := st.SetUserGlobalRoles(ctx, user.ID, []string{"super_admin"}); err != nil {
		t.Fatalf("SetUserGlobalRoles failed: %v", err)
	}
	if err := st.SetUserOrganizationRoles(ctx, user.ID, []string{"org_admin"}); err != nil {
		t.Fatalf("SetUserOrganizationRoles failed: %v", err)
	}

	loginResp := performLoginRequest(t, app, "/api/v1/admin/auth/login", map[string]string{
		"email":    user.Email,
		"password": "ChangeMe123!",
	})
	if loginResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for admin login, got %d", loginResp.StatusCode)
	}
	refreshCookie := findCookie(t, loginResp, auth.AdminRefreshCookieName)
	sessionID, _, err := auth.ParseRefreshToken(refreshCookie.Value)
	if err != nil {
		t.Fatalf("ParseRefreshToken failed: %v", err)
	}

	if err := st.SetUserGlobalRoles(ctx, user.ID, []string{"user"}); err != nil {
		t.Fatalf("SetUserGlobalRoles failed: %v", err)
	}
	if err := st.SetUserOrganizationRoles(ctx, user.ID, []string{"org_member"}); err != nil {
		t.Fatalf("SetUserOrganizationRoles failed: %v", err)
	}
	if err := st.SetUserPermissionOverrides(ctx, user.ID, []store.UserPermissionOverrideInput{{PermissionCode: "feature.settings", Effect: "allow"}}); err != nil {
		t.Fatalf("SetUserPermissionOverrides failed: %v", err)
	}

	resp := performJSONRequestWithHeaders(t, app, http.MethodPost, "/api/v1/admin/auth/refresh", nil, []*http.Cookie{refreshCookie}, nil)
	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 for admin refresh without admin scope, got %d", resp.StatusCode)
	}

	record, err := appCtx.Store.GetRefreshSessionByID(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetRefreshSessionByID failed: %v", err)
	}
	if record == nil || !record.RevokedAt.Valid {
		t.Fatalf("expected admin scope loss to revoke refresh session, got %+v", record)
	}
}

func TestBuildAuthResponseFailsWhenOrganizationMissing(t *testing.T) {
	_, appCtx, st, user := setupLoginRoutesTest(t)
	ctx := context.Background()
	missingOrgUser := *user
	missingOrgUser.OrganizationID = "missing-org"

	if _, err := appCtx.buildAuthResponse(ctx, &missingOrgUser, "backend", ""); err == nil {
		t.Fatal("expected buildAuthResponse to fail when organization is missing")
	}

	var remaining sql.NullString
	if err := st.DB.QueryRowContext(ctx, `SELECT id FROM organizations WHERE id = ?`, user.OrganizationID).Scan(&remaining); err == nil {
		if !remaining.Valid || remaining.String != user.OrganizationID {
			t.Fatalf("expected original organization to remain present, got %v", remaining)
		}
	}
}

func TestBuildAuthResponseIssueSessionAndAuditLogLogin(t *testing.T) {
	_, appCtx, st, user := setupLoginRoutesTest(t)
	ctx := context.Background()
	if err := st.SetUserGlobalRoles(ctx, user.ID, []string{"super_admin"}); err != nil {
		t.Fatalf("SetUserGlobalRoles failed: %v", err)
	}
	if err := st.SetUserOrganizationRoles(ctx, user.ID, []string{"org_admin"}); err != nil {
		t.Fatalf("SetUserOrganizationRoles failed: %v", err)
	}
	if err := st.SetUserPermissionOverrides(ctx, user.ID, []store.UserPermissionOverrideInput{{PermissionCode: "feature.admin", Effect: "allow"}}); err != nil {
		t.Fatalf("SetUserPermissionOverrides failed: %v", err)
	}

	response, err := appCtx.buildAuthResponse(ctx, user, "admin", "csrf-token")
	if err != nil {
		t.Fatalf("buildAuthResponse failed: %v", err)
	}
	if response.RuntimeMode != "admin" || response.CsrfToken != "csrf-token" {
		t.Fatalf("unexpected auth response: %+v", response)
	}
	if !strings.Contains(strings.Join(response.Permissions, ","), "feature.admin") {
		t.Fatalf("expected feature.admin in permissions, got %+v", response.Permissions)
	}

	payload, status, err := appCtx.issueSession(ctx, user, auth.SessionTypeAdmin)
	if err != nil || status != fiber.StatusOK {
		t.Fatalf("issueSession failed: status=%d err=%v", status, err)
	}
	if payload == nil || payload.SessionType != auth.SessionTypeAdmin || strings.TrimSpace(payload.Response.CsrfToken) == "" {
		t.Fatalf("unexpected admin session payload: %+v", payload)
	}

	appCtx.auditLogLogin(ctx, payload.Response)
	var count int
	if err := st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs WHERE action = ?`, "admin.login").Scan(&count); err != nil {
		t.Fatalf("failed to query audit log count: %v", err)
	}
	if count == 0 {
		t.Fatal("expected admin login audit log to be inserted")
	}
}

func TestIssueSessionRejectsAdminWithoutScope(t *testing.T) {
	_, appCtx, st, user := setupLoginRoutesTest(t)
	ctx := context.Background()
	if err := st.SetUserGlobalRoles(ctx, user.ID, []string{"user"}); err != nil {
		t.Fatalf("SetUserGlobalRoles failed: %v", err)
	}

	payload, status, err := appCtx.issueSession(ctx, user, auth.SessionTypeAdmin)
	if err == nil || status != fiber.StatusForbidden || payload != nil {
		t.Fatalf("expected forbidden admin issueSession, got payload=%+v status=%d err=%v", payload, status, err)
	}
}

func TestMeForSession_DirectBranches(t *testing.T) {
	_, appCtx, st, user := setupLoginRoutesTest(t)
	ctx := context.Background()
	if err := st.SetUserGlobalRoles(ctx, user.ID, []string{"user"}); err != nil {
		t.Fatalf("SetUserGlobalRoles failed: %v", err)
	}

	app := fiber.New()
	app.Get("/app-me", func(c *fiber.Ctx) error {
		return appCtx.meForSession(c, auth.SessionTypeApp)
	})
	app.Get("/admin-me", func(c *fiber.Ctx) error {
		c.Locals(claimsContextKey, &auth.Claims{UserID: user.ID, OrgID: user.OrganizationID, Email: user.Email})
		return appCtx.meForSession(c, auth.SessionTypeAdmin)
	})
	app.Get("/app-me-auth", func(c *fiber.Ctx) error {
		c.Locals(claimsContextKey, &auth.Claims{UserID: user.ID, OrgID: user.OrganizationID, Email: user.Email})
		return appCtx.meForSession(c, auth.SessionTypeApp)
	})

	unauthorizedResp, err := app.Test(httptest.NewRequest(http.MethodGet, "/app-me", nil), 5_000)
	if err != nil {
		t.Fatalf("app me request failed: %v", err)
	}
	defer closeHTTPResponse(t, unauthorizedResp)
	if unauthorizedResp.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("expected 401 when claims are missing, got %d", unauthorizedResp.StatusCode)
	}

	adminForbiddenResp, err := app.Test(httptest.NewRequest(http.MethodGet, "/admin-me", nil), 5_000)
	if err != nil {
		t.Fatalf("admin me request failed: %v", err)
	}
	defer closeHTTPResponse(t, adminForbiddenResp)
	if adminForbiddenResp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 when admin scope is missing, got %d", adminForbiddenResp.StatusCode)
	}

	active := "active"
	if _, err := st.UpdateUser(ctx, user.ID, store.UpdateUserInput{Status: &active}); err != nil {
		t.Fatalf("UpdateUser failed: %v", err)
	}
	appResp, err := app.Test(httptest.NewRequest(http.MethodGet, "/app-me-auth", nil), 5_000)
	if err != nil {
		t.Fatalf("authorized app me request failed: %v", err)
	}
	defer closeHTTPResponse(t, appResp)
	if appResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for direct app me, got %d", appResp.StatusCode)
	}
}

func TestAuthHandlers_ReturnServerErrorsWhenStoreIsClosed(t *testing.T) {
	app, appCtx, st, user := setupLoginRoutesTest(t)
	ctx := context.Background()
	if err := st.SetUserGlobalRoles(ctx, user.ID, []string{"user"}); err != nil {
		t.Fatalf("SetUserGlobalRoles failed: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	loginResp := performLoginRequest(t, app, "/api/v1/auth/login", map[string]string{
		"email":    user.Email,
		"password": "ChangeMe123!",
	})
	if loginResp.StatusCode != fiber.StatusInternalServerError {
		t.Fatalf("expected 500 for login with closed store, got %d", loginResp.StatusCode)
	}

	meApp := fiber.New()
	meApp.Get("/app-me-auth", func(c *fiber.Ctx) error {
		c.Locals(claimsContextKey, &auth.Claims{UserID: user.ID, OrgID: user.OrganizationID, Email: user.Email})
		return appCtx.meForSession(c, auth.SessionTypeApp)
	})
	meResp, err := meApp.Test(httptest.NewRequest(http.MethodGet, "/app-me-auth", nil), 5_000)
	if err != nil {
		t.Fatalf("direct me request failed: %v", err)
	}
	defer closeHTTPResponse(t, meResp)
	if meResp.StatusCode != fiber.StatusInternalServerError {
		t.Fatalf("expected 500 for meForSession with closed store, got %d", meResp.StatusCode)
	}

	if _, err := appCtx.buildAuthResponse(ctx, user, "backend", ""); err == nil {
		t.Fatal("expected buildAuthResponse to fail with closed store")
	}
	if payload, status, err := appCtx.issueSession(ctx, user, auth.SessionTypeApp); err == nil || status != fiber.StatusInternalServerError || payload != nil {
		t.Fatalf("expected issueSession to fail with closed store, got payload=%+v status=%d err=%v", payload, status, err)
	}
}

func TestMeRejectsUnavailableUserAndCanUseAdminPanel(t *testing.T) {
	app, appCtx, st, user := setupLoginRoutesTest(t)
	ctx := context.Background()
	if err := st.SetUserGlobalRoles(ctx, user.ID, []string{"user"}); err != nil {
		t.Fatalf("SetUserGlobalRoles failed: %v", err)
	}

	accessToken := issueAccessTokenForSession(t, appCtx.Config.JWTSecret, auth.SessionTypeApp, auth.Claims{
		UserID:      user.ID,
		OrgID:       user.OrganizationID,
		Email:       user.Email,
		Permissions: []string{"feature.settings"},
	})
	meResp := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodGet,
		"/api/v1/auth/me",
		nil,
		[]*http.Cookie{{Name: auth.AppAccessCookieName, Value: accessToken}},
		nil,
	)
	if meResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for active user me, got %d", meResp.StatusCode)
	}

	inactiveStatus := "inactive"
	if _, err := st.UpdateUser(ctx, user.ID, store.UpdateUserInput{Status: &inactiveStatus}); err != nil {
		t.Fatalf("UpdateUser failed: %v", err)
	}
	unavailableResp := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodGet,
		"/api/v1/auth/me",
		nil,
		[]*http.Cookie{{Name: auth.AppAccessCookieName, Value: accessToken}},
		nil,
	)
	if unavailableResp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 for unavailable user, got %d", unavailableResp.StatusCode)
	}

	if canUseAdminPanel(AuthResponse{Permissions: []string{"feature.admin"}, GlobalRoles: []string{"super_admin"}}) != true {
		t.Fatal("expected super admin with feature.admin to access admin panel")
	}
	if canUseAdminPanel(AuthResponse{Permissions: []string{"feature.admin"}, OrgRoles: []string{"org_admin"}}) != true {
		t.Fatal("expected org admin with feature.admin to access admin panel")
	}
	if canUseAdminPanel(AuthResponse{Permissions: []string{"feature.settings"}, GlobalRoles: []string{"super_admin"}}) {
		t.Fatal("expected missing feature.admin permission to deny admin panel")
	}
	if canUseAdminPanel(AuthResponse{Permissions: []string{"feature.admin"}, GlobalRoles: []string{"user"}, OrgRoles: []string{"org_member"}}) {
		t.Fatal("expected missing admin roles to deny admin panel")
	}
}

func setupLoginRoutesTest(t *testing.T) (*fiber.App, *App, *store.Store, *store.User) {
	t.Helper()

	appCtx, st := newAPIAppContext(t, "auth-routes.sqlite", config.Config{
		JWTSecret:        "test-secret",
		AccessTTL:        15 * time.Minute,
		RefreshTTL:       24 * time.Hour,
		AdminAccessTTL:   10 * time.Minute,
		AdminRefreshTTL:  12 * time.Hour,
		CookieSecure:     true,
		AdminCORSOrigins: []string{"https://admin.demeter.test"},
	})

	org, err := st.CreateOrganization(context.Background(), "Org Test", "org-test", "active")
	if err != nil {
		t.Fatalf("failed to create organization: %v", err)
	}
	passwordHash, err := auth.HashPassword("ChangeMe123!")
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}
	user, err := st.CreateUser(context.Background(), org.ID, "admin@example.com", passwordHash, "active")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	app := fiber.New()
	api := app.Group("/api/v1")
	appCtx.RegisterAuthRoutes(api.Group("/auth"))
	appCtx.RegisterAdminAuthRoutes(api.Group("/admin/auth"))

	return app, appCtx, st, user
}
