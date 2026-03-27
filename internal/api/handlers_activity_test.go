package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"demeter-backend/internal/config"
	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
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

func TestAdminActivitySummary_OrganizationQueryScopeAndValidation(t *testing.T) {
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
	if _, err := st.IngestActivityEvents(ctx, orgA.ID, orgAdmin.ID, []store.ActivityEventInput{{
		EventID:    "evt-org-query",
		EventKind:  "report",
		SourceMode: "cloud_backend",
		Provider:   "demeter_sante",
		Status:     "success",
		OccurredAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatalf("failed to ingest activity: %v", err)
	}

	token := issueAdminActivityToken(t, "test-jwt-secret", orgAdmin)

	resp := performJSONRequest(t, app, http.MethodGet, "/api/v1/admin/activity/summary?organizationId="+orgA.ID, token, "")
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for scoped organization query, got %d", resp.StatusCode)
	}

	forbidden := performJSONRequest(t, app, http.MethodGet, "/api/v1/admin/activity/summary?organizationId="+orgB.ID, token, "")
	if forbidden.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 for cross-org organization query, got %d", forbidden.StatusCode)
	}

	badRange := performJSONRequest(t, app, http.MethodGet, "/api/v1/admin/activity/summary?from=2026-03-10&to=2026-03-01", token, "")
	if badRange.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("expected 400 for invalid activity range, got %d", badRange.StatusCode)
	}
}

func TestAdminUserActivitySummary_OrganizationScopeAndValidation(t *testing.T) {
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
	otherUser, err := st.CreateUser(ctx, orgB.ID, "other@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("failed to create other user: %v", err)
	}
	if _, err := st.IngestActivityEvents(ctx, orgA.ID, orgAdmin.ID, []store.ActivityEventInput{
		{
			EventID:    "user-summary-a",
			EventKind:  "transcription",
			SourceMode: "local",
			Provider:   "local_upload",
			Status:     "success",
			OccurredAt: time.Date(2026, time.March, 14, 8, 0, 0, 0, time.UTC),
		},
		{
			EventID:    "user-summary-b",
			EventKind:  "report",
			SourceMode: "cloud_direct",
			Provider:   "mistral",
			Status:     "error",
			OccurredAt: time.Date(2026, time.March, 15, 9, 0, 0, 0, time.UTC),
		},
	}); err != nil {
		t.Fatalf("failed to ingest activity: %v", err)
	}
	if _, err := st.IngestActivityEvents(ctx, orgB.ID, otherUser.ID, []store.ActivityEventInput{{
		EventID:    "user-summary-other",
		EventKind:  "transcription",
		SourceMode: "cloud_backend",
		Provider:   "demeter_sante",
		Status:     "success",
		OccurredAt: time.Date(2026, time.March, 14, 8, 0, 0, 0, time.UTC),
	}}); err != nil {
		t.Fatalf("failed to ingest other activity: %v", err)
	}

	token := issueAdminActivityToken(t, "test-jwt-secret", orgAdmin)
	resp := performJSONRequest(t, app, http.MethodGet, "/api/v1/admin/users/"+orgAdmin.ID+"/activity/summary?from=2026-03-14&to=2026-03-15", token, "")
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for user summary, got %d", resp.StatusCode)
	}
	var summary store.UserActivitySummary
	if err := json.Unmarshal(resp.Body, &summary); err != nil {
		t.Fatalf("failed to decode user summary: %v", err)
	}
	if summary.User.ID != orgAdmin.ID {
		t.Fatalf("unexpected user summary target: %+v", summary.User)
	}
	if summary.Totals.Transcriptions != 1 || summary.Totals.Reports != 1 {
		t.Fatalf("unexpected user totals: %+v", summary.Totals)
	}
	if len(summary.ByDay) != 2 {
		t.Fatalf("expected 2 day buckets, got %+v", summary.ByDay)
	}
	if got := summary.Breakdown.ReportsByProvider["mistral"]; got != 1 {
		t.Fatalf("unexpected report provider breakdown: %d", got)
	}

	forbidden := performJSONRequest(t, app, http.MethodGet, "/api/v1/admin/users/"+otherUser.ID+"/activity/summary?from=2026-03-14&to=2026-03-15", token, "")
	if forbidden.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 for cross-org user summary, got %d", forbidden.StatusCode)
	}
}

func TestDeleteUserActivity_SucceedsAndWritesAudit(t *testing.T) {
	appCtx, app, st := setupActivityTestApp(t)
	ctx := context.Background()

	org, err := st.CreateOrganization(ctx, "Org A", "org-a", "active")
	if err != nil {
		t.Fatalf("failed to create org: %v", err)
	}
	actor, err := st.CreateUser(ctx, org.ID, "actor@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("failed to create actor: %v", err)
	}
	target, err := st.CreateUser(ctx, org.ID, "target@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("failed to create target user: %v", err)
	}
	if err := st.SetUserGlobalRoles(ctx, actor.ID, []string{"user"}); err != nil {
		t.Fatalf("failed to set actor global roles: %v", err)
	}
	if err := st.SetUserOrganizationRoles(ctx, actor.ID, []string{"org_admin"}); err != nil {
		t.Fatalf("failed to set actor org roles: %v", err)
	}
	if _, err := st.IngestActivityEvents(ctx, org.ID, target.ID, []store.ActivityEventInput{
		{
			EventID:    "purge-a",
			EventKind:  "transcription",
			SourceMode: "local",
			Provider:   "local_upload",
			Status:     "success",
			OccurredAt: time.Now().UTC(),
		},
	}); err != nil {
		t.Fatalf("failed to ingest target activity: %v", err)
	}

	resp := performPasswordResetRequest(
		t,
		app,
		http.MethodDelete,
		"/api/v1/admin/users/"+target.ID+"/activity",
		nil,
		nil,
		adminHeaders(t, actor, appCtx.Config.JWTSecret),
	)
	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for successful activity purge, got %d", resp.StatusCode)
	}

	var count int
	if err := st.DB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM activity_usage_events WHERE user_id = ?
	`, target.ID).Scan(&count); err != nil {
		t.Fatalf("failed to count remaining activity rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected all activity rows to be deleted, got %d", count)
	}

	var auditCount int
	if err := st.DB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM audit_logs WHERE action = 'admin.user.activity.delete' AND target_id = ?
	`, target.ID).Scan(&auditCount); err != nil {
		t.Fatalf("failed to count purge audits: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("expected one purge audit, got %d", auditCount)
	}
}

func TestValidateActivityEvent(t *testing.T) {
	tests := []struct {
		name       string
		payload    activityEventPayload
		wantReason string
	}{
		{
			name:       "missing event id",
			payload:    activityEventPayload{EventKind: "transcription", SourceMode: "local", Provider: "local_upload", Status: "success"},
			wantReason: "event_id_required",
		},
		{
			name:       "invalid event kind",
			payload:    activityEventPayload{EventID: "evt-1", EventKind: "other", SourceMode: "local", Provider: "local_upload", Status: "success"},
			wantReason: "invalid_event_kind",
		},
		{
			name:       "invalid source mode",
			payload:    activityEventPayload{EventID: "evt-1", EventKind: "transcription", SourceMode: "desktop", Provider: "local_upload", Status: "success"},
			wantReason: "invalid_source_mode",
		},
		{
			name:       "invalid status",
			payload:    activityEventPayload{EventID: "evt-1", EventKind: "transcription", SourceMode: "local", Provider: "local_upload", Status: "pending"},
			wantReason: "invalid_status",
		},
		{
			name:       "invalid provider",
			payload:    activityEventPayload{EventID: "evt-1", EventKind: "report", SourceMode: "local", Provider: "mistral", Status: "success"},
			wantReason: "invalid_provider_for_mode",
		},
		{
			name:       "invalid occurred at",
			payload:    activityEventPayload{EventID: "evt-1", EventKind: "transcription", SourceMode: "local", Provider: "local_upload", Status: "success", OccurredAt: "not-a-date"},
			wantReason: "invalid_occurred_at",
		},
		{
			name:       "invalid meta json",
			payload:    activityEventPayload{EventID: "evt-1", EventKind: "transcription", SourceMode: "local", Provider: "local_upload", Status: "success", Meta: json.RawMessage(`{"broken":`)},
			wantReason: "invalid_meta_json",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, reason := validateActivityEvent(tc.payload)
			if reason != tc.wantReason {
				t.Fatalf("expected reason %q, got %q", tc.wantReason, reason)
			}
		})
	}

	t.Run("valid event normalizes fields and defaults occurred at", func(t *testing.T) {
		input, reason := validateActivityEvent(activityEventPayload{
			EventID:    " evt-ok ",
			EventKind:  " Transcription ",
			SourceMode: " Local ",
			Provider:   " Local_Upload ",
			Status:     " Success ",
		})
		if reason != "" {
			t.Fatalf("expected no validation error, got %q", reason)
		}
		if input.EventID != "evt-ok" || input.EventKind != "transcription" || input.SourceMode != "local" || input.Provider != "local_upload" || input.Status != "success" {
			t.Fatalf("unexpected normalized input: %+v", input)
		}
		if input.OccurredAt.IsZero() {
			t.Fatal("expected occurred at to default to now")
		}
	})
}

func TestResolveActivityRange(t *testing.T) {
	t.Run("defaults from last 30 days", func(t *testing.T) {
		fromDay, toDay, err := resolveActivityRange("", "")
		if err != nil {
			t.Fatalf("resolveActivityRange returned error: %v", err)
		}
		toDate, err := time.Parse(activityDayLayout, toDay)
		if err != nil {
			t.Fatalf("failed to parse to date: %v", err)
		}
		fromDate, err := time.Parse(activityDayLayout, fromDay)
		if err != nil {
			t.Fatalf("failed to parse from date: %v", err)
		}
		if toDate.Sub(fromDate) != 29*24*time.Hour {
			t.Fatalf("expected 29 day default range, got from=%s to=%s", fromDay, toDay)
		}
	})

	t.Run("rejects invalid formats and inverted ranges", func(t *testing.T) {
		if _, _, err := resolveActivityRange("2026/03/01", "2026-03-10"); err == nil {
			t.Fatal("expected invalid from format to fail")
		}
		if _, _, err := resolveActivityRange("2026-03-01", "2026/03/10"); err == nil {
			t.Fatal("expected invalid to format to fail")
		}
		if _, _, err := resolveActivityRange("2026-03-10", "2026-03-01"); err == nil {
			t.Fatal("expected inverted range to fail")
		}
	})
}

func setupActivityTestApp(t *testing.T) (*App, *fiber.App, *store.Store) {
	t.Helper()

	appCtx, st := newAPIAppContext(t, "activity.sqlite", config.Config{JWTSecret: "test-jwt-secret"})
	app := fiber.New()
	apiV1 := app.Group("/api/v1")
	appCtx.RegisterActivityRoutes(apiV1)
	appCtx.RegisterAdminRoutes(apiV1)
	return appCtx, app, st
}
