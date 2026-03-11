package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/config"
	"demeter-backend/internal/store"
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
)

type protectedPayload struct {
	UserID      string   `json:"userId"`
	OrgID       string   `json:"orgId"`
	Permissions []string `json:"permissions"`
}

type protectedResponse struct {
	StatusCode int
	Body       []byte
}

func TestAuthRequired_MissingTokenReturns401(t *testing.T) {
	appCtx, _, _, _, _ := setupAuthMiddlewareTest(t)
	app := newProtectedTestApp(appCtx, false)

	resp := performProtectedRequest(t, app, "")
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestAuthRequired_InvalidTokenReturns401(t *testing.T) {
	appCtx, _, _, _, _ := setupAuthMiddlewareTest(t)
	app := newProtectedTestApp(appCtx, false)

	resp := performProtectedRequest(t, app, "not-a-valid-token")
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestAuthRequired_UnknownUserReturns403(t *testing.T) {
	appCtx, _, _, _, secret := setupAuthMiddlewareTest(t)
	app := newProtectedTestApp(appCtx, false)
	token := issueAccessToken(t, secret, auth.Claims{
		UserID: "missing-user",
		OrgID:  "missing-org",
		Email:  "missing@example.com",
	})

	resp := performProtectedRequest(t, app, token)
	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestAuthRequired_InactiveUserReturns403(t *testing.T) {
	appCtx, st, _, user, secret := setupAuthMiddlewareTest(t)
	app := newProtectedTestApp(appCtx, false)
	token := issueAccessToken(t, secret, auth.Claims{
		UserID: user.ID,
		OrgID:  user.OrganizationID,
		Email:  user.Email,
	})

	inactive := "inactive"
	if _, err := st.UpdateUser(context.Background(), user.ID, store.UpdateUserInput{Status: &inactive}); err != nil {
		t.Fatalf("failed to deactivate user: %v", err)
	}

	resp := performProtectedRequest(t, app, token)
	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestAuthRequired_InactiveOrganizationReturns403(t *testing.T) {
	appCtx, st, org, user, secret := setupAuthMiddlewareTest(t)
	app := newProtectedTestApp(appCtx, false)
	token := issueAccessToken(t, secret, auth.Claims{
		UserID: user.ID,
		OrgID:  user.OrganizationID,
		Email:  user.Email,
	})

	inactive := "inactive"
	if _, err := st.UpdateOrganization(context.Background(), org.ID, nil, nil, &inactive); err != nil {
		t.Fatalf("failed to deactivate organization: %v", err)
	}

	resp := performProtectedRequest(t, app, token)
	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestAuthRequired_RefreshesClaimsFromDB(t *testing.T) {
	appCtx, st, org, user, secret := setupAuthMiddlewareTest(t)
	app := newProtectedTestApp(appCtx, false)

	if err := st.SetUserGlobalRoles(context.Background(), user.ID, []string{"user"}); err != nil {
		t.Fatalf("failed to set global roles: %v", err)
	}
	if err := st.SetUserOrganizationRoles(context.Background(), user.ID, []string{"org_member"}); err != nil {
		t.Fatalf("failed to set org roles: %v", err)
	}

	token := issueAccessToken(t, secret, auth.Claims{
		UserID:      user.ID,
		OrgID:       "stale-org-id",
		Email:       "stale@example.com",
		GlobalRoles: []string{"super_admin"},
		Permissions: []string{"feature.admin"},
	})

	resp := performProtectedRequest(t, app, token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var payload protectedPayload
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if payload.UserID != user.ID {
		t.Fatalf("expected userId=%q, got %q", user.ID, payload.UserID)
	}
	if payload.OrgID != org.ID {
		t.Fatalf("expected orgId=%q, got %q", org.ID, payload.OrgID)
	}
	if !hasCode(payload.Permissions, "feature.settings") {
		t.Fatalf("expected live permissions to include feature.settings, got %v", payload.Permissions)
	}
	if hasCode(payload.Permissions, "feature.admin") {
		t.Fatalf("expected stale token permission feature.admin to be ignored, got %v", payload.Permissions)
	}
}

func TestAuthRequired_UsesLivePermissionsWhenRemovedAfterLogin(t *testing.T) {
	appCtx, st, _, user, secret := setupAuthMiddlewareTest(t)
	app := newProtectedTestApp(appCtx, true)

	if err := st.SetUserGlobalRoles(context.Background(), user.ID, []string{"user"}); err != nil {
		t.Fatalf("failed to set global roles: %v", err)
	}

	token := issueAccessToken(t, secret, auth.Claims{
		UserID:      user.ID,
		OrgID:       user.OrganizationID,
		Email:       user.Email,
		Permissions: []string{"feature.settings"},
	})

	allowed := performProtectedRequest(t, app, token)
	if allowed.StatusCode != fiber.StatusOK {
		t.Fatalf("expected first request to pass with 200, got %d", allowed.StatusCode)
	}

	err := st.SetUserPermissionOverrides(context.Background(), user.ID, []store.UserPermissionOverrideInput{
		{PermissionCode: "feature.settings", Effect: "deny"},
	})
	if err != nil {
		t.Fatalf("failed to set permission override: %v", err)
	}

	denied := performProtectedRequest(t, app, token)
	if denied.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected second request to be forbidden, got %d", denied.StatusCode)
	}
}

func TestAuthRequired_UsesLivePermissionsWhenAddedAfterLogin(t *testing.T) {
	appCtx, st, _, user, secret := setupAuthMiddlewareTest(t)
	app := newProtectedTestApp(appCtx, true)

	token := issueAccessToken(t, secret, auth.Claims{
		UserID: user.ID,
		OrgID:  user.OrganizationID,
		Email:  user.Email,
	})

	denied := performProtectedRequest(t, app, token)
	if denied.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected first request to be forbidden, got %d", denied.StatusCode)
	}

	err := st.SetUserPermissionOverrides(context.Background(), user.ID, []store.UserPermissionOverrideInput{
		{PermissionCode: "feature.settings", Effect: "allow"},
	})
	if err != nil {
		t.Fatalf("failed to add permission override: %v", err)
	}

	allowed := performProtectedRequest(t, app, token)
	if allowed.StatusCode != fiber.StatusOK {
		t.Fatalf("expected second request to pass with 200, got %d", allowed.StatusCode)
	}
}

func TestAppAuthRequired_RejectsAdminAudience(t *testing.T) {
	appCtx, _, _, user, secret := setupAuthMiddlewareTest(t)
	app := newProtectedTestApp(appCtx, false)

	token := issueAccessTokenForSession(t, secret, auth.SessionTypeAdmin, auth.Claims{
		UserID: user.ID,
		OrgID:  user.OrganizationID,
		Email:  user.Email,
	})

	resp := performProtectedRequest(t, app, token)
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("expected 401 for admin token on app route, got %d", resp.StatusCode)
	}
}

func TestAdminAuthRequired_RejectsAppAudience(t *testing.T) {
	appCtx, _, _, user, secret := setupAuthMiddlewareTest(t)
	app := fiber.New()
	app.Get("/admin-protected", appCtx.AdminAuthRequired(), func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})

	token := issueAccessTokenForSession(t, secret, auth.SessionTypeApp, auth.Claims{
		UserID: user.ID,
		OrgID:  user.OrganizationID,
		Email:  user.Email,
	})

	req := httptest.NewRequest(http.MethodGet, "/admin-protected", nil)
	req.Header.Set(fiber.HeaderAuthorization, "Bearer "+token)
	resp, err := app.Test(req, 5_000)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("expected 401 for app token on admin route, got %d", resp.StatusCode)
	}
}

func setupAuthMiddlewareTest(t *testing.T) (*App, *store.Store, *store.Organization, *store.User, string) {
	t.Helper()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.sqlite")
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

	user, err := st.CreateUser(ctx, org.ID, "user@example.com", "unused", "active")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	secret := "test-jwt-secret"
	appCtx := &App{
		Config: config.Config{JWTSecret: secret},
		Store:  st,
	}
	return appCtx, st, org, user, secret
}

func newProtectedTestApp(appCtx *App, withPermissionCheck bool) *fiber.App {
	app := fiber.New()
	handlers := []fiber.Handler{appCtx.AppAuthRequired()}
	if withPermissionCheck {
		handlers = append(handlers, RequirePermissions("feature.settings"))
	}
	handlers = append(handlers, func(c *fiber.Ctx) error {
		claims := MustClaims(c)
		if claims == nil {
			return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
		}
		return c.JSON(protectedPayload{
			UserID:      claims.UserID,
			OrgID:       claims.OrgID,
			Permissions: claims.Permissions,
		})
	})
	app.Get("/protected", handlers...)
	return app
}

func performProtectedRequest(t *testing.T, app *fiber.App, token string) *protectedResponse {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	if token != "" {
		req.Header.Set(fiber.HeaderAuthorization, "Bearer "+token)
	}
	resp, err := app.Test(req, 5_000)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	return &protectedResponse{
		StatusCode: resp.StatusCode,
		Body:       body,
	}
}

func issueAccessToken(t *testing.T, secret string, claims auth.Claims) string {
	t.Helper()
	return issueAccessTokenForSession(t, secret, auth.SessionTypeApp, claims)
}

func issueAccessTokenForSession(t *testing.T, secret string, sessionType auth.SessionType, claims auth.Claims) string {
	t.Helper()
	claims.RegisteredClaims = jwt.RegisteredClaims{
		Audience: jwt.ClaimStrings{sessionType.String()},
	}
	token, _, err := auth.NewAccessToken(secret, time.Hour, claims)
	if err != nil {
		t.Fatalf("failed to issue access token: %v", err)
	}
	return token
}

func hasCode(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
