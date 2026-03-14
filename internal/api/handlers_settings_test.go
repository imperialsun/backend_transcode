package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

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

func TestSettingsRoutes_ResetRestoresEmptySettings(t *testing.T) {
	appCtx, app, st := setupSettingsTestApp(t)
	ctx := context.Background()

	org := createTestOrganization(t, st, "Org Reset", "org-reset", "active")
	user := createTestUser(t, st, org.ID, "reset@example.com", "hash", "active")
	if err := st.SetUserGlobalRoles(ctx, user.ID, []string{"user"}); err != nil {
		t.Fatalf("failed to set user role: %v", err)
	}
	token := issueSettingsToken(t, appCtx.Config.JWTSecret, user)

	putResp := performJSONRequest(t, app, http.MethodPut, "/api/v1/settings", token, `{
		"schemaVersion": 3,
		"settings": {
			"showSegments": true,
			"activePreset": "custom"
		}
	}`)
	if putResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 on PUT, got %d (%s)", putResp.StatusCode, string(putResp.Body))
	}

	resetResp := performJSONRequest(t, app, http.MethodPost, "/api/v1/settings/reset", token, "")
	if resetResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 on reset, got %d (%s)", resetResp.StatusCode, string(resetResp.Body))
	}

	var envelope SettingsEnvelope
	if err := json.Unmarshal(resetResp.Body, &envelope); err != nil {
		t.Fatalf("failed to decode reset response: %v", err)
	}
	if string(envelope.Settings) != "{}" {
		t.Fatalf("expected empty settings after reset, got %s", string(envelope.Settings))
	}
}

func TestSettingsRoutes_PreservesNonObjectJSONPayload(t *testing.T) {
	appCtx, app, st := setupSettingsTestApp(t)
	ctx := context.Background()

	org := createTestOrganization(t, st, "Org Array", "org-array", "active")
	user := createTestUser(t, st, org.ID, "array@example.com", "hash", "active")
	if err := st.SetUserGlobalRoles(ctx, user.ID, []string{"user"}); err != nil {
		t.Fatalf("failed to set user role: %v", err)
	}
	token := issueSettingsToken(t, appCtx.Config.JWTSecret, user)

	putResp := performJSONRequest(t, app, http.MethodPut, "/api/v1/settings", token, `{
		"schemaVersion": 1,
		"settings": ["preset-a", "preset-b"]
	}`)
	if putResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 on PUT with array payload, got %d (%s)", putResp.StatusCode, string(putResp.Body))
	}

	var envelope SettingsEnvelope
	if err := json.Unmarshal(putResp.Body, &envelope); err != nil {
		t.Fatalf("failed to decode array payload response: %v", err)
	}
	if string(envelope.Settings) != `["preset-a","preset-b"]` {
		t.Fatalf("expected array payload to be preserved, got %s", string(envelope.Settings))
	}
	if envelope.SchemaVersion != 1 {
		t.Fatalf("expected schema version 1, got %d", envelope.SchemaVersion)
	}
}

func TestSanitizeSettingsPayload(t *testing.T) {
	t.Run("empty becomes object", func(t *testing.T) {
		sanitized, err := sanitizeSettingsPayload(nil)
		if err != nil {
			t.Fatalf("sanitizeSettingsPayload returned error: %v", err)
		}
		if string(sanitized) != "{}" {
			t.Fatalf("expected empty payload to become {}, got %s", string(sanitized))
		}
	})

	t.Run("invalid json fails", func(t *testing.T) {
		if _, err := sanitizeSettingsPayload(json.RawMessage(`{"broken":`)); err == nil {
			t.Fatal("expected invalid JSON payload to fail")
		}
	})

	t.Run("legacy keys are removed", func(t *testing.T) {
		sanitized, err := sanitizeSettingsPayload(json.RawMessage(`{
			"cloudApiUrl": "https://example.test/gradio",
			"cloudContextPreset": "cardio",
			"showSegments": true,
			"activePreset": "balanced"
		}`))
		if err != nil {
			t.Fatalf("sanitizeSettingsPayload returned error: %v", err)
		}
		assertSanitizedSettings(t, sanitized)
	})

	t.Run("non object payload is preserved", func(t *testing.T) {
		sanitized, err := sanitizeSettingsPayload(json.RawMessage(`["preset-a","preset-b"]`))
		if err != nil {
			t.Fatalf("sanitizeSettingsPayload returned error: %v", err)
		}
		if string(sanitized) != `["preset-a","preset-b"]` {
			t.Fatalf("expected array payload to be preserved, got %s", string(sanitized))
		}
	})
}

func setupSettingsTestApp(t *testing.T) (*App, *fiber.App, *store.Store) {
	t.Helper()

	appCtx, st := newAPIAppContext(t, "settings.sqlite", config.Config{JWTSecret: "test-jwt-secret"})
	if err := st.SeedBaseCatalog(context.Background()); err != nil {
		t.Fatalf("failed to seed base catalog: %v", err)
	}
	app := fiber.New()
	apiV1 := app.Group("/api/v1")
	appCtx.RegisterSettingsRoutes(apiV1)
	return appCtx, app, st
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
