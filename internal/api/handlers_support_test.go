package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/backenderrors"
	"demeter-backend/internal/config"
	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
)

func TestSubmitFrontendErrorReport_AttachesAnnexAndStoresReport(t *testing.T) {
	app, _, st, user := setupSupportRoutesTest(t)
	if err := st.SetUserGlobalRoles(context.Background(), user.ID, []string{"user"}); err != nil {
		t.Fatalf("failed to set user roles: %v", err)
	}

	loginResp := performLoginRequest(t, app, "/api/v1/auth/login", map[string]string{
		"email":    user.Email,
		"password": "ChangeMe123!",
	})
	if loginResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for login, got %d", loginResp.StatusCode)
	}
	accessCookie := findCookie(t, loginResp, auth.AppAccessCookieName)

	traceID := "trace-audio-attach"
	mustInsertBackendErrorEvent(t, st, backenderrors.Event{
		TraceID:        traceID,
		UserID:         user.ID,
		OrganizationID: user.OrganizationID,
		Component:      "http",
		Route:          "/api/v1/providers/demeter-sante/audio/transcriptions/backend",
		Step:           "request_failed",
		Title:          "audio_transcription",
		StatusCode:     400,
		ErrorMessage:   "fichier audio vide",
		PayloadJSON:    json.RawMessage(`{"error":"fichier audio vide"}`),
		CreatedAt:      time.Now().UTC().Add(-time.Minute),
	})

	report := map[string]any{
		"traceId":  traceID,
		"provider": "demeter_sante",
		"backendError": map[string]any{
			"status":  400,
			"code":    "empty_audio_file",
			"message": "Fichier audio vide",
			"path":    "/providers/demeter-sante/audio/transcriptions/backend",
			"method":  "POST",
			"traceId": traceID,
		},
		"originalFile": map[string]any{
			"name":      "audio.wav",
			"sizeBytes": 0,
			"mimeType":  "audio/wav",
			"source":    "source",
		},
		"processedFile": map[string]any{
			"name":      "audio-cloud.wav",
			"sizeBytes": 0,
			"mimeType":  "audio/wav",
			"source":    "processed",
		},
		"rawFile": map[string]any{
			"name":      "segment_0.webm",
			"sizeBytes": 456,
			"mimeType":  "audio/webm",
			"source":    "raw",
		},
		"retry": map[string]any{
			"attempted":   true,
			"succeeded":   true,
			"usedRawFile": true,
		},
		"diagnosticBundle": map[string]any{
			"schemaVersion": 1,
			"session":       map[string]any{"status": "error"},
			"settings":      map[string]any{"provider": "demeter_sante"},
			"telemetry":     map[string]any{"sessionId": "telemetry-1"},
		},
	}

	resp := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodPost,
		"/api/v1/support/frontend-error-reports",
		report,
		[]*http.Cookie{accessCookie},
		nil,
	)
	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 from frontend report, got %d", resp.StatusCode)
	}

	var attachedCount int
	if err := st.DB.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM backend_error_events
		WHERE trace_id = ? AND component = 'http' AND annex_json <> '{}'
	`, traceID).Scan(&attachedCount); err != nil {
		t.Fatalf("failed to query attached backend error: %v", err)
	}
	if attachedCount != 1 {
		t.Fatalf("expected one backend error with annex, got %d", attachedCount)
	}

	var recoveryStatus string
	var annexJSON string
	if err := st.DB.QueryRowContext(context.Background(), `
		SELECT annex_json, recovery_status
		FROM backend_error_events
		WHERE trace_id = ? AND component = 'http'
		ORDER BY created_at DESC
		LIMIT 1
	`, traceID).Scan(&annexJSON, &recoveryStatus); err != nil {
		t.Fatalf("failed to load annex row: %v", err)
	}
	if !strings.Contains(annexJSON, `"provider":"demeter_sante"`) {
		t.Fatalf("expected annex json to contain frontend summary, got %s", annexJSON)
	}
	if recoveryStatus != "raw_retry_succeeded" {
		t.Fatalf("expected recovery status to be persisted, got %q", recoveryStatus)
	}

	var reportCount int
	if err := st.DB.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM backend_error_events
		WHERE trace_id = ? AND component = 'frontend'
	`, traceID).Scan(&reportCount); err != nil {
		t.Fatalf("failed to count report rows: %v", err)
	}
	if reportCount != 1 {
		t.Fatalf("expected one frontend report row, got %d", reportCount)
	}

}

func TestSubmitFrontendErrorReport_CreatesStandaloneRowWithoutTraceMatch(t *testing.T) {
	app, _, st, user := setupSupportRoutesTest(t)
	if err := st.SetUserGlobalRoles(context.Background(), user.ID, []string{"user"}); err != nil {
		t.Fatalf("failed to set user roles: %v", err)
	}

	loginResp := performLoginRequest(t, app, "/api/v1/auth/login", map[string]string{
		"email":    user.Email,
		"password": "ChangeMe123!",
	})
	if loginResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for login, got %d", loginResp.StatusCode)
	}
	accessCookie := findCookie(t, loginResp, auth.AppAccessCookieName)

	report := map[string]any{
		"provider": "demeter_sante",
		"backendError": map[string]any{
			"status":  400,
			"code":    "empty_audio_file",
			"message": "Fichier audio vide",
			"path":    "/providers/demeter-sante/audio/transcriptions/backend",
			"method":  "POST",
		},
		"originalFile": map[string]any{
			"name":      "empty.wav",
			"sizeBytes": 0,
			"mimeType":  "audio/wav",
			"source":    "source",
		},
		"processedFile": map[string]any{
			"name":      "empty-cloud.wav",
			"sizeBytes": 0,
			"mimeType":  "audio/wav",
			"source":    "processed",
		},
		"retry": map[string]any{
			"attempted":   false,
			"succeeded":   false,
			"usedRawFile": false,
		},
		"diagnosticBundle": map[string]any{
			"schemaVersion": 1,
			"session":       map[string]any{"status": "error"},
			"settings":      map[string]any{"provider": "demeter_sante"},
			"telemetry":     map[string]any{"sessionId": "telemetry-2"},
		},
	}

	resp := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodPost,
		"/api/v1/support/frontend-error-reports",
		report,
		[]*http.Cookie{accessCookie},
		nil,
	)
	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 from standalone frontend report, got %d", resp.StatusCode)
	}

	var count int
	if err := st.DB.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM backend_error_events
		WHERE component = 'frontend' AND recovery_status IS NULL
	`).Scan(&count); err != nil {
		t.Fatalf("failed to count standalone frontend report rows: %v", err)
	}
	if count == 0 {
		t.Fatal("expected a standalone frontend report row to be stored")
	}
}

func TestSubmitAndroidErrorReport_StoresAndroidClientMetadata(t *testing.T) {
	app, _, st, user := setupSupportRoutesTest(t)
	if err := st.SetUserGlobalRoles(context.Background(), user.ID, []string{"user"}); err != nil {
		t.Fatalf("failed to set user roles: %v", err)
	}

	loginResp := performLoginRequest(t, app, "/api/v1/auth/login", map[string]string{
		"email":    user.Email,
		"password": "ChangeMe123!",
	})
	if loginResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for login, got %d", loginResp.StatusCode)
	}
	accessCookie := findCookie(t, loginResp, auth.AppAccessCookieName)

	traceID := "trace-android-support"
	report := map[string]any{
		"client":   "android",
		"provider": "android",
		"backendError": map[string]any{
			"status":  500,
			"code":    "uncaught_exception",
			"message": "NullPointerException",
			"path":    "android://uncaught-exception",
			"method":  "PROCESS",
			"traceId": traceID,
		},
		"originalFile": map[string]any{
			"name":      "",
			"sizeBytes": 0,
			"mimeType":  "",
			"source":    "android",
		},
		"processedFile": map[string]any{
			"name":      "",
			"sizeBytes": 0,
			"mimeType":  "",
			"source":    "android",
		},
		"retry": map[string]any{
			"attempted":   false,
			"succeeded":   false,
			"usedRawFile": false,
		},
		"diagnosticBundle": map[string]any{
			"schemaVersion": 1,
			"client":        "android",
			"session":       map[string]any{"route": "android://uncaught-exception"},
			"settings":      map[string]any{"lastEmail": user.Email},
			"telemetry":     map[string]any{"threadName": "main"},
		},
	}

	resp := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodPost,
		"/api/v1/support/frontend-error-reports",
		report,
		[]*http.Cookie{accessCookie},
		nil,
	)
	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 from android report, got %d", resp.StatusCode)
	}

	var component, errorMessage, payloadJSON string
	if err := st.DB.QueryRowContext(context.Background(), `
		SELECT component, error_message, payload_json
		FROM backend_error_events
		WHERE trace_id = ? AND component = 'android'
		ORDER BY created_at DESC
		LIMIT 1
	`, traceID).Scan(&component, &errorMessage, &payloadJSON); err != nil {
		t.Fatalf("failed to load android report row: %v", err)
	}
	if component != "android" {
		t.Fatalf("expected android component, got %q", component)
	}
	if errorMessage != "NullPointerException" {
		t.Fatalf("expected android error message to be stored, got %q", errorMessage)
	}
	if !strings.Contains(payloadJSON, `"client":"android"`) {
		t.Fatalf("expected payload json to contain android client, got %s", payloadJSON)
	}
}

func setupSupportRoutesTest(t *testing.T) (*fiber.App, *App, *store.Store, *store.User) {
	t.Helper()

	appCtx, st := newAPIAppContext(t, "support-routes.sqlite", config.Config{
		JWTSecret:        "test-secret",
		AccessTTL:        15 * time.Minute,
		RefreshTTL:       24 * time.Hour,
		AdminAccessTTL:   10 * time.Minute,
		AdminRefreshTTL:  12 * time.Hour,
		CookieSecure:     true,
		AdminCORSOrigins: []string{"https://admin.demeter.test"},
	})

	org, err := st.CreateOrganization(context.Background(), "Support Org", "support-org", "active")
	if err != nil {
		t.Fatalf("failed to create organization: %v", err)
	}
	passwordHash, err := auth.HashPassword("ChangeMe123!")
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}
	user, err := st.CreateUser(context.Background(), org.ID, "support@example.com", passwordHash, "active")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	app := fiber.New()
	api := app.Group("/api/v1")
	appCtx.RegisterAuthRoutes(api.Group("/auth"))
	appCtx.RegisterSupportRoutes(api)
	return app, appCtx, st, user
}
