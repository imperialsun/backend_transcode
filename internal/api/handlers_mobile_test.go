package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"demeter-backend/internal/config"
	meetingreports "demeter-backend/internal/reports"
	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
)

func TestMobileOperationStatusRouteReturnsPollingShape(t *testing.T) {
	appCtx, app, st, user, token := setupMobileTestApp(t)
	ctx := context.Background()
	now := time.Date(2026, 4, 25, 10, 30, 0, 0, time.UTC)

	if _, err := st.CreateMobileOperationIfAbsent(ctx, &store.MobileOperationRecord{
		OperationID:      "mobile-op-1",
		OrganizationID:   user.OrganizationID,
		UserID:           user.ID,
		Kind:             "audio_report_email",
		Status:           store.MobileOperationStatusRunning,
		StatusCode:       fiber.StatusAccepted,
		Stage:            "transcription",
		Progress:         0.42,
		ChunkIndex:       2,
		ChunkCount:       5,
		Message:          sql.NullString{String: "transcription in progress", Valid: true},
		AudioOperationID: sql.NullString{String: "audio-op-1", Valid: true},
		CreatedAt:        now,
		UpdatedAt:        now,
	}); err != nil {
		t.Fatalf("failed to create mobile operation: %v", err)
	}

	resp := performJSONRequest(t, app, http.MethodGet, "/api/v1/mobile/operations/mobile-op-1", token, "")
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", resp.StatusCode, string(resp.Body))
	}

	var payload mobileOperationResponse
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("failed to decode operation response: %v", err)
	}
	if payload.OperationID != "mobile-op-1" || payload.Status != store.MobileOperationStatusRunning || payload.Stage != "transcription" {
		t.Fatalf("unexpected operation response: %+v", payload)
	}
	if payload.AudioOperationID != "audio-op-1" || payload.ChunkIndex != 2 || payload.ChunkCount != 5 {
		t.Fatalf("unexpected polling metadata: %+v", payload)
	}
	if appCtx == nil {
		t.Fatal("expected app context")
	}
}

func TestMobileReportEmailRouteValidatesTranscriptBeforeGeneration(t *testing.T) {
	_, app, _, _, token := setupMobileTestApp(t)

	resp := performJSONRequest(t, app, http.MethodPost, "/api/v1/mobile/reports/email", token, `{
		"meetingTitle": "Consultation mobile",
		"reportDetailLevels": {"CRI": "verbose", "CRO": "standard", "CRS": "exhaustive"}
	}`)
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("expected 400 for missing transcript, got %d (%s)", resp.StatusCode, string(resp.Body))
	}
}

func TestMobileRoutesReplaceLegacyMeetingFinalizeRoutes(t *testing.T) {
	_, app, _, _, token := setupMobileTestApp(t)

	for _, tc := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/api/v1/meetings/reports/drafts"},
		{method: http.MethodPost, path: "/api/v1/meetings/finalize"},
		{method: http.MethodGet, path: "/api/v1/meetings/finalize/operations/old-op"},
	} {
		resp := performJSONRequest(t, app, tc.method, tc.path, token, "")
		if resp.StatusCode != fiber.StatusNotFound {
			t.Fatalf("%s %s: expected 404, got %d (%s)", tc.method, tc.path, resp.StatusCode, string(resp.Body))
		}
	}
}

func TestLoadMobileReportSettingsUsesStoredValuesAndOverrides(t *testing.T) {
	appCtx, _, st, user, _ := setupMobileTestApp(t)
	ctx := context.Background()

	_, err := st.SaveUserSettings(ctx, user.ID, user.OrganizationID, json.RawMessage(`{
		"llmApiMistralModelId": "mistral-large-latest",
		"llmApiMistralTemperature": 0.4,
		"llmApiMistralMaxTokens": 300000,
		"llmApiReportMonoPassMaxTokens": 65536,
		"llmApiReportDetailLevels": {
			"CRI": "verbose",
			"CRO": "standard",
			"CRS": "exhaustive"
		}
	}`), 1)
	if err != nil {
		t.Fatalf("failed to save user settings: %v", err)
	}

	settings, err := appCtx.loadMobileReportSettings(ctx, user.ID, map[string]string{"CRO": "exhaustive"})
	if err != nil {
		t.Fatalf("loadMobileReportSettings returned error: %v", err)
	}
	if settings.ModelID != "mistral-large-latest" || settings.Temperature != 0.4 {
		t.Fatalf("unexpected model settings: %+v", settings)
	}
	if settings.MaxTokens != 65536 {
		t.Fatalf("expected mono-pass max tokens to be applied, got %d", settings.MaxTokens)
	}
	if settings.DetailLevels[meetingreports.ReportFormatCRI] != meetingreports.ReportDetailVerbose {
		t.Fatalf("expected CRI verbose, got %q", settings.DetailLevels[meetingreports.ReportFormatCRI])
	}
	if settings.DetailLevels[meetingreports.ReportFormatCRO] != meetingreports.ReportDetailExhaustive {
		t.Fatalf("expected CRO override exhaustive, got %q", settings.DetailLevels[meetingreports.ReportFormatCRO])
	}
	if settings.DetailLevels[meetingreports.ReportFormatCRS] != meetingreports.ReportDetailExhaustive {
		t.Fatalf("expected CRS exhaustive, got %q", settings.DetailLevels[meetingreports.ReportFormatCRS])
	}
}

func TestResolveMobileTranscriptForMailPrefersCorrectedText(t *testing.T) {
	if got := resolveMobileTranscriptForMail("transcription brute", "transcription corrigee"); got != "transcription corrigee" {
		t.Fatalf("expected corrected transcript in mail DOCX, got %q", got)
	}
	if got := resolveMobileTranscriptForMail("transcription brute", ""); got != "transcription brute" {
		t.Fatalf("expected raw transcript fallback, got %q", got)
	}
}

func TestRecordMobileActivityEventPersistsNonSensitiveMetadata(t *testing.T) {
	appCtx, _, _, user, _ := setupMobileTestApp(t)
	actor := mobileOperationActor{UserID: user.ID, OrgID: user.OrganizationID, Email: user.Email}

	err := appCtx.recordMobileActivityEvent(actor, "report", mobileReportSourceMode, mobileReportProvider, "success", map[string]any{
		"client":       "mobile",
		"operation_id": "mobile-op-activity",
		"formats":      []string{"CRI", "CRO", "CRS"},
	})
	if err != nil {
		t.Fatalf("recordMobileActivityEvent returned error: %v", err)
	}

	var count int
	if err := appCtx.Store.DB.QueryRow(`SELECT COUNT(*) FROM activity_usage_events WHERE user_id = ? AND provider = ? AND status = ?`, user.ID, mobileReportProvider, "success").Scan(&count); err != nil {
		t.Fatalf("failed to query activity events: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one mobile activity event, got %d", count)
	}
}

func setupMobileTestApp(t *testing.T) (*App, *fiber.App, *store.Store, *store.User, string) {
	t.Helper()

	appCtx, st := newAPIAppContext(t, "mobile.sqlite", config.Config{JWTSecret: "test-jwt-secret"})
	if err := st.SeedBaseCatalog(context.Background()); err != nil {
		t.Fatalf("failed to seed base catalog: %v", err)
	}
	org := createTestOrganization(t, st, "Mobile Org", "mobile-org", "active")
	user := createTestUser(t, st, org.ID, "mobile@example.com", "hash", "active")
	if err := st.SetUserGlobalRoles(context.Background(), user.ID, []string{"user"}); err != nil {
		t.Fatalf("failed to set user role: %v", err)
	}
	if err := st.SetUserPermissionOverrides(context.Background(), user.ID, []store.UserPermissionOverrideInput{
		{PermissionCode: "feature.llmapi", Effect: "allow"},
		{PermissionCode: "provider.llm.demeter_sante", Effect: "allow"},
		{PermissionCode: "feature.cloudupload", Effect: "allow"},
		{PermissionCode: "provider.cloud.demeter_sante", Effect: "allow"},
	}); err != nil {
		t.Fatalf("failed to set permission overrides: %v", err)
	}

	app := fiber.New()
	apiV1 := app.Group("/api/v1")
	appCtx.RegisterMobileRoutes(apiV1)
	return appCtx, app, st, user, issueSettingsToken(t, appCtx.Config.JWTSecret, user)
}
