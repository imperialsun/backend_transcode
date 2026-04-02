package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/config"
	"demeter-backend/internal/mailer"
	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
)

type fakePasswordResetMailer struct {
	readyErr        error
	sendErr         error
	sent            []mailer.PasswordResetEmail
	provisionedSent []mailer.UserProvisioningEmail
}

func (m *fakePasswordResetMailer) Ready() error {
	return m.readyErr
}

func (m *fakePasswordResetMailer) SendPasswordResetEmail(_ context.Context, input mailer.PasswordResetEmail) error {
	if m.sendErr != nil {
		return m.sendErr
	}
	m.sent = append(m.sent, input)
	return nil
}

func (m *fakePasswordResetMailer) SendMeetingSummaryEmail(_ context.Context, input mailer.MeetingSummaryEmail) error {
	if m.sendErr != nil {
		return m.sendErr
	}
	_ = input
	return nil
}

func (m *fakePasswordResetMailer) SendUserProvisioningEmail(_ context.Context, input mailer.UserProvisioningEmail) error {
	if m.sendErr != nil {
		return m.sendErr
	}
	m.provisionedSent = append(m.provisionedSent, input)
	return nil
}

type passwordResetFixture struct {
	app          *fiber.App
	appCtx       *App
	store        *store.Store
	mailer       *fakePasswordResetMailer
	org          *store.Organization
	otherOrg     *store.Organization
	adminUser    *store.User
	activeUser   *store.User
	inactiveUser *store.User
	otherUser    *store.User
}

func TestForgotPassword_NonEnumeratingForExistingAbsentAndInactiveUsers(t *testing.T) {
	fixture := setupPasswordResetRoutesTest(t)

	existingResp := performPasswordResetRequest(
		t,
		fixture.app,
		http.MethodPost,
		"/api/v1/auth/forgot-password",
		map[string]string{"email": fixture.activeUser.Email},
		nil,
		nil,
	)
	if existingResp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for existing user, got %d", existingResp.StatusCode)
	}
	if len(fixture.mailer.sent) != 1 {
		t.Fatalf("expected one email for existing user, got %d", len(fixture.mailer.sent))
	}
	if fixture.mailer.sent[0].SessionType != auth.SessionTypeApp {
		t.Fatalf("expected app session type email, got %s", fixture.mailer.sent[0].SessionType)
	}
	if fixture.mailer.sent[0].ResetURL[:24] != "https://app.demeter.test" {
		t.Fatalf("expected app public url, got %q", fixture.mailer.sent[0].ResetURL)
	}
	if fixture.mailer.sent[0].ApplicationURL != "https://app.demeter.test/" {
		t.Fatalf("expected normalized app public url, got %q", fixture.mailer.sent[0].ApplicationURL)
	}

	fixture.mailer.sent = nil
	absentResp := performPasswordResetRequest(
		t,
		fixture.app,
		http.MethodPost,
		"/api/v1/auth/forgot-password",
		map[string]string{"email": "missing@example.com"},
		nil,
		nil,
	)
	if absentResp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for absent user, got %d", absentResp.StatusCode)
	}
	if len(fixture.mailer.sent) != 0 {
		t.Fatalf("expected no email for absent user, got %d", len(fixture.mailer.sent))
	}

	inactiveResp := performPasswordResetRequest(
		t,
		fixture.app,
		http.MethodPost,
		"/api/v1/auth/forgot-password",
		map[string]string{"email": fixture.inactiveUser.Email},
		nil,
		nil,
	)
	if inactiveResp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for inactive user, got %d", inactiveResp.StatusCode)
	}
	if len(fixture.mailer.sent) != 0 {
		t.Fatalf("expected no email for inactive user, got %d", len(fixture.mailer.sent))
	}

	adminResp := performPasswordResetRequest(
		t,
		fixture.app,
		http.MethodPost,
		"/api/v1/admin/auth/forgot-password",
		map[string]string{"email": fixture.adminUser.Email},
		nil,
		nil,
	)
	if adminResp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for admin forgot password, got %d", adminResp.StatusCode)
	}
	if len(fixture.mailer.sent) != 1 {
		t.Fatalf("expected one admin email, got %d", len(fixture.mailer.sent))
	}
	if fixture.mailer.sent[0].SessionType != auth.SessionTypeAdmin {
		t.Fatalf("expected admin session type email, got %s", fixture.mailer.sent[0].SessionType)
	}
	if fixture.mailer.sent[0].ResetURL[:26] != "https://admin.demeter.test" {
		t.Fatalf("expected admin public url, got %q", fixture.mailer.sent[0].ResetURL)
	}
	if fixture.mailer.sent[0].ApplicationURL != "https://app.demeter.test/" {
		t.Fatalf("expected normalized app public url in admin reset email, got %q", fixture.mailer.sent[0].ApplicationURL)
	}
}

func TestForgotPassword_RejectsInvalidPayloadAndMissingEmail(t *testing.T) {
	fixture := setupPasswordResetRoutesTest(t)

	invalidPayload := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPost,
		"/api/v1/auth/forgot-password",
		nil,
		nil,
		map[string]string{fiber.HeaderContentType: fiber.MIMEApplicationJSON},
	)
	if invalidPayload.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("expected 400 for invalid forgot-password payload, got %d", invalidPayload.StatusCode)
	}

	missingEmail := performPasswordResetRequest(
		t,
		fixture.app,
		http.MethodPost,
		"/api/v1/auth/forgot-password",
		map[string]string{"email": "   "},
		nil,
		nil,
	)
	if missingEmail.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("expected 400 for missing email, got %d", missingEmail.StatusCode)
	}
}

func TestResetPassword_RejectsMalformedExpiredUsedAndWrongNamespaceTokens(t *testing.T) {
	fixture := setupPasswordResetRoutesTest(t)

	expiredToken := "expired-reset-token"
	if err := fixture.store.SavePasswordResetToken(context.Background(), store.PasswordResetToken{
		UserID:      fixture.activeUser.ID,
		SessionType: auth.SessionTypeApp.String(),
		TokenHash:   auth.HashPasswordResetToken(expiredToken),
		ExpiresAt:   time.Now().UTC().Add(-time.Minute),
		CreatedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("failed to save expired token: %v", err)
	}

	usedToken := "used-reset-token"
	usedTokenHash := auth.HashPasswordResetToken(usedToken)
	if err := fixture.store.SavePasswordResetToken(context.Background(), store.PasswordResetToken{
		UserID:      fixture.activeUser.ID,
		SessionType: auth.SessionTypeApp.String(),
		TokenHash:   usedTokenHash,
		ExpiresAt:   time.Now().UTC().Add(time.Hour),
		CreatedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("failed to save used token: %v", err)
	}
	usedRecord, err := fixture.store.GetPasswordResetTokenByHash(context.Background(), usedTokenHash)
	if err != nil {
		t.Fatalf("failed to load used token record: %v", err)
	}
	if usedRecord == nil {
		t.Fatal("expected used token record to exist")
	}
	if err := fixture.store.ConsumePasswordResetToken(context.Background(), usedRecord.ID); err != nil {
		t.Fatalf("failed to consume used token: %v", err)
	}

	namespaceToken := "namespace-reset-token"
	if err := fixture.store.SavePasswordResetToken(context.Background(), store.PasswordResetToken{
		UserID:      fixture.activeUser.ID,
		SessionType: auth.SessionTypeApp.String(),
		TokenHash:   auth.HashPasswordResetToken(namespaceToken),
		ExpiresAt:   time.Now().UTC().Add(time.Hour),
		CreatedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("failed to save namespace token: %v", err)
	}

	testCases := []struct {
		name string
		path string
		body map[string]string
	}{
		{
			name: "malformed",
			path: "/api/v1/auth/reset-password",
			body: map[string]string{"token": "missing-token", "password": "NewPass123!"},
		},
		{
			name: "expired",
			path: "/api/v1/auth/reset-password",
			body: map[string]string{"token": expiredToken, "password": "NewPass123!"},
		},
		{
			name: "used",
			path: "/api/v1/auth/reset-password",
			body: map[string]string{"token": usedToken, "password": "NewPass123!"},
		},
		{
			name: "wrong-namespace",
			path: "/api/v1/admin/auth/reset-password",
			body: map[string]string{"token": namespaceToken, "password": "NewPass123!"},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			resp := performPasswordResetRequest(t, fixture.app, http.MethodPost, testCase.path, testCase.body, nil, nil)
			if resp.StatusCode != fiber.StatusBadRequest {
				t.Fatalf("expected 400 for %s token, got %d", testCase.name, resp.StatusCode)
			}
		})
	}
}

func TestResetPassword_UpdatesHashRevokesSessionsAndInvalidatesOtherTokens(t *testing.T) {
	fixture := setupPasswordResetRoutesTest(t)
	ctx := context.Background()

	appRefresh, err := auth.NewRefreshToken(2 * time.Hour)
	if err != nil {
		t.Fatalf("failed to create app refresh token: %v", err)
	}
	if err := fixture.store.SaveRefreshSession(ctx, store.RefreshSession{
		ID:             appRefresh.SessionID,
		UserID:         fixture.activeUser.ID,
		OrganizationID: fixture.activeUser.OrganizationID,
		SessionType:    auth.SessionTypeApp.String(),
		TokenHash:      appRefresh.Hash,
		ExpiresAt:      appRefresh.ExpiresAt,
		CreatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("failed to save app refresh session: %v", err)
	}

	adminRefresh, err := auth.NewRefreshToken(2 * time.Hour)
	if err != nil {
		t.Fatalf("failed to create admin refresh token: %v", err)
	}
	if err := fixture.store.SaveRefreshSession(ctx, store.RefreshSession{
		ID:             adminRefresh.SessionID,
		UserID:         fixture.activeUser.ID,
		OrganizationID: fixture.activeUser.OrganizationID,
		SessionType:    auth.SessionTypeAdmin.String(),
		TokenHash:      adminRefresh.Hash,
		ExpiresAt:      adminRefresh.ExpiresAt,
		CreatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("failed to save admin refresh session: %v", err)
	}

	primaryToken := "primary-reset-token"
	secondaryToken := "secondary-reset-token"
	adminToken := "admin-reset-token"
	for _, token := range []struct {
		raw         string
		sessionType auth.SessionType
	}{
		{raw: primaryToken, sessionType: auth.SessionTypeApp},
		{raw: secondaryToken, sessionType: auth.SessionTypeApp},
		{raw: adminToken, sessionType: auth.SessionTypeAdmin},
	} {
		if err := fixture.store.SavePasswordResetToken(ctx, store.PasswordResetToken{
			UserID:      fixture.activeUser.ID,
			SessionType: token.sessionType.String(),
			TokenHash:   auth.HashPasswordResetToken(token.raw),
			ExpiresAt:   time.Now().UTC().Add(time.Hour),
			CreatedAt:   time.Now().UTC(),
		}); err != nil {
			t.Fatalf("failed to save password reset token %q: %v", token.raw, err)
		}
	}

	resp := performPasswordResetRequest(
		t,
		fixture.app,
		http.MethodPost,
		"/api/v1/auth/reset-password",
		map[string]string{"token": primaryToken, "password": "NewPass456!"},
		nil,
		nil,
	)
	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for successful password reset, got %d", resp.StatusCode)
	}

	updatedUser, err := fixture.store.GetUserByID(ctx, fixture.activeUser.ID)
	if err != nil {
		t.Fatalf("failed to reload user: %v", err)
	}
	if updatedUser == nil || !auth.VerifyPassword(updatedUser.PasswordHash, "NewPass456!") {
		t.Fatalf("expected password hash to be updated, got %#v", updatedUser)
	}
	if auth.VerifyPassword(updatedUser.PasswordHash, "ChangeMe123!") {
		t.Fatal("expected old password to be rejected")
	}

	for _, sessionID := range []string{appRefresh.SessionID, adminRefresh.SessionID} {
		session, err := fixture.store.GetRefreshSessionByID(ctx, sessionID)
		if err != nil {
			t.Fatalf("failed to reload refresh session %q: %v", sessionID, err)
		}
		if session == nil || !session.RevokedAt.Valid {
			t.Fatalf("expected refresh session %q to be revoked, got %#v", sessionID, session)
		}
	}

	for _, token := range []string{primaryToken, secondaryToken, adminToken} {
		record, err := fixture.store.GetPasswordResetTokenByHash(ctx, auth.HashPasswordResetToken(token))
		if err != nil {
			t.Fatalf("failed to reload token %q: %v", token, err)
		}
		if record == nil || !record.UsedAt.Valid {
			t.Fatalf("expected token %q to be invalidated, got %#v", token, record)
		}
	}
}

func TestAdminSendUserPasswordResetEmail_EnforcesScopeAndSurfacesMailerErrors(t *testing.T) {
	fixture := setupPasswordResetRoutesTest(t)

	loginResp := performLoginRequest(t, fixture.app, "/api/v1/admin/auth/login", map[string]string{
		"email":    fixture.adminUser.Email,
		"password": "ChangeMe123!",
	})
	var sessionPayload AuthResponse
	if err := json.NewDecoder(loginResp.Body).Decode(&sessionPayload); err != nil {
		t.Fatalf("failed to decode admin login payload: %v", err)
	}

	cookies := []*http.Cookie{
		findCookie(t, loginResp, auth.AdminAccessCookieName),
		findCookie(t, loginResp, auth.AdminRefreshCookieName),
	}
	headers := map[string]string{
		auth.AdminCSRFHeaderName: sessionPayload.CsrfToken,
	}

	okResp := performPasswordResetRequest(
		t,
		fixture.app,
		http.MethodPost,
		"/api/v1/admin/users/"+fixture.activeUser.ID+"/password-reset-email",
		nil,
		cookies,
		headers,
	)
	if okResp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for scoped admin email send, got %d", okResp.StatusCode)
	}
	if len(fixture.mailer.sent) != 1 {
		t.Fatalf("expected one password reset email, got %d", len(fixture.mailer.sent))
	}
	sentURL, err := url.Parse(fixture.mailer.sent[0].ResetURL)
	if err != nil {
		t.Fatalf("failed to parse reset url: %v", err)
	}
	tokenValue := sentURL.Query().Get("token")
	if tokenValue == "" {
		t.Fatalf("expected token query parameter in reset url %q", fixture.mailer.sent[0].ResetURL)
	}
	if fixture.mailer.sent[0].ApplicationURL != "https://app.demeter.test/" {
		t.Fatalf("expected normalized app public url in admin password reset email, got %q", fixture.mailer.sent[0].ApplicationURL)
	}
	record, err := fixture.store.GetPasswordResetTokenByHash(context.Background(), auth.HashPasswordResetToken(tokenValue))
	if err != nil {
		t.Fatalf("failed to load stored reset token: %v", err)
	}
	if record == nil || !record.RequestedByUserID.Valid || record.RequestedByUserID.String != fixture.adminUser.ID {
		t.Fatalf("expected admin requester id to be stored, got %#v", record)
	}

	forbiddenResp := performPasswordResetRequest(
		t,
		fixture.app,
		http.MethodPost,
		"/api/v1/admin/users/"+fixture.otherUser.ID+"/password-reset-email",
		nil,
		cookies,
		headers,
	)
	if forbiddenResp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 for cross-organization reset email, got %d", forbiddenResp.StatusCode)
	}

	fixture.mailer.sendErr = errors.New("smtp down")
	errorResp := performPasswordResetRequest(
		t,
		fixture.app,
		http.MethodPost,
		"/api/v1/admin/users/"+fixture.activeUser.ID+"/password-reset-email",
		nil,
		cookies,
		headers,
	)
	if errorResp.StatusCode != fiber.StatusInternalServerError {
		t.Fatalf("expected 500 when mailer send fails, got %d", errorResp.StatusCode)
	}
}

func TestAdminSendUserPasswordResetEmail_ReturnsServiceUnavailableWhenResetUnavailable(t *testing.T) {
	fixture := setupPasswordResetRoutesTest(t)
	fixture.mailer.readyErr = errors.New("not ready")

	loginResp := performLoginRequest(t, fixture.app, "/api/v1/admin/auth/login", map[string]string{
		"email":    fixture.adminUser.Email,
		"password": "ChangeMe123!",
	})
	var sessionPayload AuthResponse
	if err := json.NewDecoder(loginResp.Body).Decode(&sessionPayload); err != nil {
		t.Fatalf("failed to decode admin login payload: %v", err)
	}

	resp := performPasswordResetRequest(
		t,
		fixture.app,
		http.MethodPost,
		"/api/v1/admin/users/"+fixture.activeUser.ID+"/password-reset-email",
		nil,
		[]*http.Cookie{
			findCookie(t, loginResp, auth.AdminAccessCookieName),
			findCookie(t, loginResp, auth.AdminRefreshCookieName),
		},
		map[string]string{auth.AdminCSRFHeaderName: sessionPayload.CsrfToken},
	)
	if resp.StatusCode != fiber.StatusServiceUnavailable {
		t.Fatalf("expected 503 when password reset is unavailable, got %d", resp.StatusCode)
	}
}

func TestAdminSendUserPasswordResetEmail_RejectsInactiveAndMissingUsers(t *testing.T) {
	fixture := setupPasswordResetRoutesTest(t)

	loginResp := performLoginRequest(t, fixture.app, "/api/v1/admin/auth/login", map[string]string{
		"email":    fixture.adminUser.Email,
		"password": "ChangeMe123!",
	})
	var sessionPayload AuthResponse
	if err := json.NewDecoder(loginResp.Body).Decode(&sessionPayload); err != nil {
		t.Fatalf("failed to decode admin login payload: %v", err)
	}

	cookies := []*http.Cookie{
		findCookie(t, loginResp, auth.AdminAccessCookieName),
		findCookie(t, loginResp, auth.AdminRefreshCookieName),
	}
	headers := map[string]string{auth.AdminCSRFHeaderName: sessionPayload.CsrfToken}

	inactiveResp := performPasswordResetRequest(
		t,
		fixture.app,
		http.MethodPost,
		"/api/v1/admin/users/"+fixture.inactiveUser.ID+"/password-reset-email",
		nil,
		cookies,
		headers,
	)
	if inactiveResp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("expected 400 for inactive target user, got %d", inactiveResp.StatusCode)
	}

	missingResp := performPasswordResetRequest(
		t,
		fixture.app,
		http.MethodPost,
		"/api/v1/admin/users/missing-user/password-reset-email",
		nil,
		cookies,
		headers,
	)
	if missingResp.StatusCode != fiber.StatusNotFound {
		t.Fatalf("expected 404 for missing target user, got %d", missingResp.StatusCode)
	}
}

func setupPasswordResetRoutesTest(t *testing.T) *passwordResetFixture {
	t.Helper()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "password-reset.sqlite")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	org, err := st.CreateOrganization(ctx, "Org Reset", "org-reset", "active")
	if err != nil {
		t.Fatalf("failed to create organization: %v", err)
	}
	otherOrg, err := st.CreateOrganization(ctx, "Org Other", "org-other", "active")
	if err != nil {
		t.Fatalf("failed to create other organization: %v", err)
	}

	passwordHash, err := auth.HashPassword("ChangeMe123!")
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}
	activeUser, err := st.CreateUser(ctx, org.ID, "active@example.com", passwordHash, "active")
	if err != nil {
		t.Fatalf("failed to create active user: %v", err)
	}
	inactiveUser, err := st.CreateUser(ctx, org.ID, "inactive@example.com", passwordHash, "inactive")
	if err != nil {
		t.Fatalf("failed to create inactive user: %v", err)
	}
	adminUser, err := st.CreateUser(ctx, org.ID, "admin@example.com", passwordHash, "active")
	if err != nil {
		t.Fatalf("failed to create admin user: %v", err)
	}
	otherUser, err := st.CreateUser(ctx, otherOrg.ID, "other@example.com", passwordHash, "active")
	if err != nil {
		t.Fatalf("failed to create other organization user: %v", err)
	}

	if err := st.SetUserGlobalRoles(ctx, adminUser.ID, []string{"user"}); err != nil {
		t.Fatalf("failed to set admin global roles: %v", err)
	}
	if err := st.SetUserOrganizationRoles(ctx, adminUser.ID, []string{"org_admin"}); err != nil {
		t.Fatalf("failed to set admin org roles: %v", err)
	}

	mailerStub := &fakePasswordResetMailer{}
	appCtx := &App{
		Config: config.Config{
			JWTSecret:        "test-secret",
			AccessTTL:        15 * time.Minute,
			RefreshTTL:       24 * time.Hour,
			AdminAccessTTL:   10 * time.Minute,
			AdminRefreshTTL:  12 * time.Hour,
			CookieSecure:     true,
			AppCORSOrigins:   []string{"https://app.demeter.test"},
			AdminCORSOrigins: []string{"https://admin.demeter.test"},
			AppPublicURL:     "https://app.demeter.test",
			AdminPublicURL:   "https://admin.demeter.test",
			PasswordResetTTL: time.Hour,
		},
		Store:  st,
		Mailer: mailerStub,
	}

	app := fiber.New()
	apiV1 := app.Group("/api/v1")
	appCtx.RegisterAuthRoutes(apiV1.Group("/auth"))
	appCtx.RegisterAdminAuthRoutes(apiV1.Group("/admin/auth"))
	appCtx.RegisterAdminRoutes(apiV1)

	return &passwordResetFixture{
		app:          app,
		appCtx:       appCtx,
		store:        st,
		mailer:       mailerStub,
		org:          org,
		otherOrg:     otherOrg,
		adminUser:    adminUser,
		activeUser:   activeUser,
		inactiveUser: inactiveUser,
		otherUser:    otherUser,
	}
}
