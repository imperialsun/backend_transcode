package api

import (
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

func TestPostActivityEvents_Unauthorized(t *testing.T) {
	_, app, _ := setupActivityTestApp(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/activity/events", strings.NewReader(`{"events":[]}`))
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	resp, err := app.Test(req, 5_000)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestPostActivityEvents_AcceptsDuplicatesAndRejectsInvalid(t *testing.T) {
	_, app, st := setupActivityTestApp(t)
	ctx := context.Background()

	org, err := st.CreateOrganization(ctx, "Org One", "org-one", "active")
	if err != nil {
		t.Fatalf("failed to create organization: %v", err)
	}
	user, err := st.CreateUser(ctx, org.ID, "u1@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	token := issueActivityToken(t, "test-jwt-secret", user)

	firstBody := `{
		"events": [
			{
				"eventId": "evt-1",
				"eventKind": "transcription",
				"sourceMode": "local",
				"provider": "local_upload",
				"status": "success"
			},
			{
				"eventId": "evt-invalid",
				"eventKind": "transcription",
				"sourceMode": "local",
				"provider": "whisper",
				"status": "success"
			}
		]
	}`
	firstResp := performJSONRequest(t, app, http.MethodPost, "/api/v1/activity/events", token, firstBody)
	if firstResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 on first request, got %d", firstResp.StatusCode)
	}
	var firstPayload activityEventsResponse
	if err := json.Unmarshal(firstResp.Body, &firstPayload); err != nil {
		t.Fatalf("failed to decode first response: %v", err)
	}
	if firstPayload.Accepted != 1 || firstPayload.Duplicates != 0 || len(firstPayload.Rejected) != 1 {
		t.Fatalf("unexpected first response: %+v", firstPayload)
	}
	if firstPayload.Rejected[0].EventID != "evt-invalid" || firstPayload.Rejected[0].Reason != "invalid_provider_for_mode" {
		t.Fatalf("unexpected rejection payload: %+v", firstPayload.Rejected[0])
	}

	secondBody := `{
		"events": [
			{
				"eventId": "evt-1",
				"eventKind": "transcription",
				"sourceMode": "local",
				"provider": "local_upload",
				"status": "success"
			}
		]
	}`
	secondResp := performJSONRequest(t, app, http.MethodPost, "/api/v1/activity/events", token, secondBody)
	if secondResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 on second request, got %d", secondResp.StatusCode)
	}
	var secondPayload activityEventsResponse
	if err := json.Unmarshal(secondResp.Body, &secondPayload); err != nil {
		t.Fatalf("failed to decode second response: %v", err)
	}
	if secondPayload.Accepted != 0 || secondPayload.Duplicates != 1 || len(secondPayload.Rejected) != 0 {
		t.Fatalf("unexpected second response: %+v", secondPayload)
	}
}

func TestPostActivityEvents_RejectsGradioProvider(t *testing.T) {
	_, app, st := setupActivityTestApp(t)
	ctx := context.Background()

	org, err := st.CreateOrganization(ctx, "Org One", "org-one", "active")
	if err != nil {
		t.Fatalf("failed to create organization: %v", err)
	}
	user, err := st.CreateUser(ctx, org.ID, "u1@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	token := issueActivityToken(t, "test-jwt-secret", user)

	resp := performJSONRequest(t, app, http.MethodPost, "/api/v1/activity/events", token, `{
		"events": [
			{
				"eventId": "evt-gradio",
				"eventKind": "transcription",
				"sourceMode": "cloud_direct",
				"provider": "gradio",
				"status": "success"
			}
		]
	}`)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var payload activityEventsResponse
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if payload.Accepted != 0 || payload.Duplicates != 0 || len(payload.Rejected) != 1 {
		t.Fatalf("unexpected response: %+v", payload)
	}
	if payload.Rejected[0].EventID != "evt-gradio" || payload.Rejected[0].Reason != "invalid_provider_for_mode" {
		t.Fatalf("unexpected rejection payload: %+v", payload.Rejected[0])
	}
}

func TestAdminOrganizationActivitySummary_OrgScope(t *testing.T) {
	_, app, st := setupActivityTestApp(t)
	ctx := context.Background()

	orgA, err := st.CreateOrganization(ctx, "Org A", "org-a", "active")
	if err != nil {
		t.Fatalf("failed to create org A: %v", err)
	}
	orgB, err := st.CreateOrganization(ctx, "Org B", "org-b", "active")
	if err != nil {
		t.Fatalf("failed to create org B: %v", err)
	}
	orgAdmin, err := st.CreateUser(ctx, orgA.ID, "org-admin@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("failed to create org admin: %v", err)
	}
	if err := st.SetUserGlobalRoles(ctx, orgAdmin.ID, []string{"user"}); err != nil {
		t.Fatalf("failed to set global roles: %v", err)
	}
	if err := st.SetUserOrganizationRoles(ctx, orgAdmin.ID, []string{"org_admin"}); err != nil {
		t.Fatalf("failed to set org roles: %v", err)
	}
	if _, err := st.IngestActivityEvents(ctx, orgA.ID, orgAdmin.ID, []store.ActivityEventInput{
		{
			EventID:    "evt-org-a",
			EventKind:  "transcription",
			SourceMode: "local",
			Provider:   "local_upload",
			Status:     "success",
			OccurredAt: time.Now().UTC(),
		},
	}); err != nil {
		t.Fatalf("failed to ingest activity: %v", err)
	}

	adminToken := issueAdminActivityToken(t, "test-jwt-secret", orgAdmin)

	okPath := "/api/v1/admin/activity/organizations/" + orgA.ID + "/summary"
	okResp := performJSONRequest(t, app, http.MethodGet, okPath, adminToken, "")
	if okResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 on org scope, got %d", okResp.StatusCode)
	}

	forbiddenPath := "/api/v1/admin/activity/organizations/" + orgB.ID + "/summary"
	forbiddenResp := performJSONRequest(t, app, http.MethodGet, forbiddenPath, adminToken, "")
	if forbiddenResp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 for cross-org access, got %d", forbiddenResp.StatusCode)
	}
}

func TestAdminOrganizationActivitySummary_SuperAdminCanAccessAnyOrg(t *testing.T) {
	_, app, st := setupActivityTestApp(t)
	ctx := context.Background()

	orgA, err := st.CreateOrganization(ctx, "Org A", "org-a", "active")
	if err != nil {
		t.Fatalf("failed to create org A: %v", err)
	}
	orgB, err := st.CreateOrganization(ctx, "Org B", "org-b", "active")
	if err != nil {
		t.Fatalf("failed to create org B: %v", err)
	}
	superAdmin, err := st.CreateUser(ctx, orgA.ID, "super-admin@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("failed to create super admin: %v", err)
	}
	if err := st.SetUserGlobalRoles(ctx, superAdmin.ID, []string{"super_admin", "user"}); err != nil {
		t.Fatalf("failed to set global roles: %v", err)
	}
	if err := st.SetUserOrganizationRoles(ctx, superAdmin.ID, []string{"org_member"}); err != nil {
		t.Fatalf("failed to set org roles: %v", err)
	}

	otherUser, err := st.CreateUser(ctx, orgB.ID, "user-b@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("failed to create user in org B: %v", err)
	}
	if _, err := st.IngestActivityEvents(ctx, orgB.ID, otherUser.ID, []store.ActivityEventInput{
		{
			EventID:    "evt-org-b",
			EventKind:  "report",
			SourceMode: "cloud_backend",
			Provider:   "demeter_sante",
			Status:     "error",
			OccurredAt: time.Now().UTC(),
		},
	}); err != nil {
		t.Fatalf("failed to ingest org B activity: %v", err)
	}

	token := issueAdminActivityToken(t, "test-jwt-secret", superAdmin)
	path := "/api/v1/admin/activity/organizations/" + orgB.ID + "/summary"
	resp := performJSONRequest(t, app, http.MethodGet, path, token, "")
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for super admin access, got %d", resp.StatusCode)
	}
}

func TestAdminActivitySummary_GlobalScopeForSuperAdmin(t *testing.T) {
	_, app, st := setupActivityTestApp(t)
	ctx := context.Background()

	orgA, _ := st.CreateOrganization(ctx, "Org A", "org-a", "active")
	orgB, _ := st.CreateOrganization(ctx, "Org B", "org-b", "active")
	superAdmin, _ := st.CreateUser(ctx, orgA.ID, "super-admin@example.com", "hash", "active")
	if err := st.SetUserGlobalRoles(ctx, superAdmin.ID, []string{"super_admin", "user"}); err != nil {
		t.Fatalf("failed to set global roles: %v", err)
	}
	if err := st.SetUserOrganizationRoles(ctx, superAdmin.ID, []string{"org_admin"}); err != nil {
		t.Fatalf("failed to set org roles: %v", err)
	}
	userB, _ := st.CreateUser(ctx, orgB.ID, "user-b@example.com", "hash", "active")
	_, _ = st.IngestActivityEvents(ctx, orgA.ID, superAdmin.ID, []store.ActivityEventInput{{
		EventID:    "evt-global-a",
		EventKind:  "transcription",
		SourceMode: "local",
		Provider:   "local_upload",
		Status:     "success",
		OccurredAt: time.Now().UTC(),
	}})
	_, _ = st.IngestActivityEvents(ctx, orgB.ID, userB.ID, []store.ActivityEventInput{{
		EventID:    "evt-global-b",
		EventKind:  "report",
		SourceMode: "cloud_backend",
		Provider:   "demeter_sante",
		Status:     "success",
		OccurredAt: time.Now().UTC(),
	}})

	token := issueAdminActivityToken(t, "test-jwt-secret", superAdmin)
	resp := performJSONRequest(t, app, http.MethodGet, "/api/v1/admin/activity/summary", token, "")
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for global summary, got %d", resp.StatusCode)
	}
	var summary store.ActivitySummary
	if err := json.Unmarshal(resp.Body, &summary); err != nil {
		t.Fatalf("failed to decode summary: %v", err)
	}
	if summary.Totals.Transcriptions != 1 || summary.Totals.Reports != 1 {
		t.Fatalf("unexpected totals: %+v", summary.Totals)
	}
}

func setupActivityTestApp(t *testing.T) (*App, *fiber.App, *store.Store) {
	t.Helper()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "activity.sqlite")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	appCtx := &App{
		Config: config.Config{JWTSecret: "test-jwt-secret"},
		Store:  st,
	}
	app := fiber.New()
	apiV1 := app.Group("/api/v1")
	appCtx.RegisterActivityRoutes(apiV1)
	appCtx.RegisterAdminRoutes(apiV1)
	return appCtx, app, st
}

func issueActivityToken(t *testing.T, secret string, user *store.User) string {
	t.Helper()
	token, _, err := auth.NewAccessToken(secret, time.Hour, auth.Claims{
		UserID: user.ID,
		OrgID:  user.OrganizationID,
		Email:  user.Email,
	})
	if err != nil {
		t.Fatalf("failed to issue token: %v", err)
	}
	return token
}

func issueAdminActivityToken(t *testing.T, secret string, user *store.User) string {
	t.Helper()
	token, _, err := auth.NewAccessToken(secret, time.Hour, auth.Claims{
		UserID: user.ID,
		OrgID:  user.OrganizationID,
		Email:  user.Email,
		RegisteredClaims: jwt.RegisteredClaims{
			Audience: jwt.ClaimStrings{auth.SessionTypeAdmin.String()},
		},
	})
	if err != nil {
		t.Fatalf("failed to issue admin token: %v", err)
	}
	return token
}

type testResponse struct {
	StatusCode int
	Body       []byte
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
