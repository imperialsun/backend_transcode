package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/config"
	"demeter-backend/internal/store"
	"github.com/gofiber/fiber/v2"
)

func TestSettingsRoutes_SanitizeLegacyGradioFields(t *testing.T) {
	appCtx, app, st := setupSettingsTestApp(t)
	ctx := context.Background()

	org, err := st.CreateOrganization(ctx, "Org One", "org-one", "active")
	if err != nil {
		t.Fatalf("failed to create organization: %v", err)
	}
	user, err := st.CreateUser(ctx, org.ID, "u1@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	if err := st.SetUserGlobalRoles(ctx, user.ID, []string{"user"}); err != nil {
		t.Fatalf("failed to set user role: %v", err)
	}
	token := issueSettingsToken(t, appCtx.Config.JWTSecret, user)

	putResp := performJSONRequest(t, app, http.MethodPut, "/api/v1/settings", token, `{
		"schemaVersion": 1,
		"settings": {
			"cloudApiUrl": "https://example.test/gradio",
			"cloudContextPreset": "cardio",
			"showSegments": true,
			"activePreset": "balanced"
		}
	}`)
	if putResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 on PUT, got %d (%s)", putResp.StatusCode, string(putResp.Body))
	}

	var putEnvelope SettingsEnvelope
	if err := json.Unmarshal(putResp.Body, &putEnvelope); err != nil {
		t.Fatalf("failed to decode PUT response: %v", err)
	}
	assertSanitizedSettings(t, putEnvelope.Settings)

	getResp := performJSONRequest(t, app, http.MethodGet, "/api/v1/settings", token, "")
	if getResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 on GET, got %d (%s)", getResp.StatusCode, string(getResp.Body))
	}

	var getEnvelope SettingsEnvelope
	if err := json.Unmarshal(getResp.Body, &getEnvelope); err != nil {
		t.Fatalf("failed to decode GET response: %v", err)
	}
	assertSanitizedSettings(t, getEnvelope.Settings)
}

func setupSettingsTestApp(t *testing.T) (*App, *fiber.App, *store.Store) {
	t.Helper()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "settings.sqlite")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite store: %v", err)
	}
	if err := st.SeedBaseCatalog(ctx); err != nil {
		t.Fatalf("failed to seed base catalog: %v", err)
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
	appCtx.RegisterSettingsRoutes(apiV1)
	return appCtx, app, st
}

func issueSettingsToken(t *testing.T, secret string, user *store.User) string {
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

func assertSanitizedSettings(t *testing.T, payload json.RawMessage) {
	t.Helper()

	var settings map[string]any
	if err := json.Unmarshal(payload, &settings); err != nil {
		t.Fatalf("failed to decode settings payload: %v", err)
	}
	if _, ok := settings["cloudApiUrl"]; ok {
		t.Fatalf("expected cloudApiUrl to be removed, got %v", settings)
	}
	if _, ok := settings["cloudContextPreset"]; ok {
		t.Fatalf("expected cloudContextPreset to be removed, got %v", settings)
	}
	if settings["showSegments"] != true {
		t.Fatalf("expected showSegments to remain, got %v", settings)
	}
	if settings["activePreset"] != "balanced" {
		t.Fatalf("expected activePreset to remain, got %v", settings)
	}
}
