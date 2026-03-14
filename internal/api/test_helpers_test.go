package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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
	"github.com/golang-jwt/jwt/v5"
)

type testResponse struct {
	StatusCode int
	Body       []byte
}

func openAPITestStore(t *testing.T, name string) *store.Store {
	t.Helper()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), name)
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})
	return st
}

func newAPIAppContext(t *testing.T, dbName string, cfg config.Config) (*App, *store.Store) {
	t.Helper()

	st := openAPITestStore(t, dbName)
	appCfg := cfg
	if strings.TrimSpace(appCfg.JWTSecret) == "" {
		appCfg.JWTSecret = "test-jwt-secret"
	}
	return &App{Config: appCfg, Store: st}, st
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

func issueSettingsToken(t *testing.T, secret string, user *store.User) string {
	t.Helper()
	return issueAccessToken(t, secret, auth.Claims{
		UserID: user.ID,
		OrgID:  user.OrganizationID,
		Email:  user.Email,
	})
}

func issueActivityToken(t *testing.T, secret string, user *store.User) string {
	t.Helper()
	return issueAccessToken(t, secret, auth.Claims{
		UserID: user.ID,
		OrgID:  user.OrganizationID,
		Email:  user.Email,
	})
}

func issueAdminActivityToken(t *testing.T, secret string, user *store.User) string {
	t.Helper()
	return issueAccessTokenForSession(t, secret, auth.SessionTypeAdmin, auth.Claims{
		UserID: user.ID,
		OrgID:  user.OrganizationID,
		Email:  user.Email,
	})
}

func performJSONRequest(t *testing.T, app *fiber.App, method, path, token, body string) testResponse {
	t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	}
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
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	return testResponse{StatusCode: resp.StatusCode, Body: raw}
}

func performJSONRequestWithHeaders(t *testing.T, app *fiber.App, method, path string, payload any, cookies []*http.Cookie, headers map[string]string) *http.Response {
	t.Helper()

	body := bytes.NewReader(nil)
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("failed to marshal payload: %v", err)
		}
		body = bytes.NewReader(raw)
	}

	req := httptest.NewRequest(method, path, body)
	if payload != nil {
		req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := app.Test(req, 5_000)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	t.Cleanup(func() {
		closeHTTPResponse(t, resp)
	})
	return resp
}

func closeHTTPResponse(t *testing.T, resp *http.Response) {
	t.Helper()
	if resp == nil || resp.Body == nil {
		return
	}
	if err := resp.Body.Close(); err != nil {
		t.Errorf("close response body: %v", err)
	}
}

func performLoginRequest(t *testing.T, app *fiber.App, path string, payload map[string]string) *http.Response {
	t.Helper()
	return performJSONRequestWithHeaders(t, app, http.MethodPost, path, payload, nil, nil)
}

func performPasswordResetRequest(
	t *testing.T,
	app *fiber.App,
	method string,
	path string,
	payload any,
	cookies []*http.Cookie,
	headers map[string]string,
) *http.Response {
	t.Helper()
	return performJSONRequestWithHeaders(t, app, method, path, payload, cookies, headers)
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

func createTestOrganization(t *testing.T, st *store.Store, name, code, status string) *store.Organization {
	t.Helper()
	org, err := st.CreateOrganization(context.Background(), name, code, status)
	if err != nil {
		t.Fatalf("failed to create organization: %v", err)
	}
	return org
}

func createTestUser(t *testing.T, st *store.Store, orgID, email, passwordHash, status string) *store.User {
	t.Helper()
	user, err := st.CreateUser(context.Background(), orgID, email, passwordHash, status)
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	return user
}
