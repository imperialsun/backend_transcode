package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

func setupLoginRoutesTest(t *testing.T) (*fiber.App, *App, *store.Store, *store.User) {
	t.Helper()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "auth-routes.sqlite")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	org, err := st.CreateOrganization(ctx, "Org Test", "org-test", "active")
	if err != nil {
		t.Fatalf("failed to create organization: %v", err)
	}
	passwordHash, err := auth.HashPassword("ChangeMe123!")
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}
	user, err := st.CreateUser(ctx, org.ID, "admin@example.com", passwordHash, "active")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	appCtx := &App{
		Config: config.Config{
			JWTSecret:        "test-secret",
			AccessTTL:        15 * time.Minute,
			RefreshTTL:       24 * time.Hour,
			AdminAccessTTL:   10 * time.Minute,
			AdminRefreshTTL:  12 * time.Hour,
			CookieSecure:     true,
			AdminCORSOrigins: []string{"https://admin.demeter.test"},
		},
		Store: st,
	}

	app := fiber.New()
	api := app.Group("/api/v1")
	appCtx.RegisterAuthRoutes(api.Group("/auth"))
	appCtx.RegisterAdminAuthRoutes(api.Group("/admin/auth"))

	return app, appCtx, st, user
}

func performLoginRequest(t *testing.T, app *fiber.App, path string, payload map[string]string) *http.Response {
	t.Helper()

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)

	resp, err := app.Test(req, 5_000)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	t.Cleanup(func() {
		_ = resp.Body.Close()
	})
	return resp
}

func findCookie(t *testing.T, resp *http.Response, name string) *http.Cookie {
	t.Helper()

	for _, cookie := range resp.Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("cookie %q not found", name)
	return nil
}

func assertSetCookieContains(t *testing.T, resp *http.Response, cookieName, fragment string) {
	t.Helper()

	expectedPrefix := strings.ToLower(cookieName) + "="
	expectedFragment := strings.ToLower(fragment)
	for _, header := range resp.Header.Values("Set-Cookie") {
		lowerHeader := strings.ToLower(header)
		if strings.HasPrefix(lowerHeader, expectedPrefix) && strings.Contains(lowerHeader, expectedFragment) {
			return
		}
	}
	t.Fatalf("expected Set-Cookie for %q to contain %q, got %v", cookieName, fragment, resp.Header.Values("Set-Cookie"))
}
