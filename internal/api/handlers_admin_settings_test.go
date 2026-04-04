package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestAdminUserSettingsRoutes_SuperAdminAndOrgAdminScopes(t *testing.T) {
	fixture := setupAdminDeleteRoutesTest(t)
	ctx := context.Background()

	if err := fixture.store.SetUserGlobalRoles(ctx, fixture.actor.ID, []string{"super_admin", "user"}); err != nil {
		t.Fatalf("failed to set super admin role: %v", err)
	}

	putResp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPut,
		"/api/v1/admin/users/"+fixture.member.ID+"/settings",
		map[string]any{
			"schemaVersion": 1,
			"settings": map[string]any{
				"showSegments":     true,
				"cloudApiUrl":      "https://legacy.example.test",
				"activePreset":     "balanced",
				"chunkDurationSec": 15,
			},
		},
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if putResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for admin settings put, got %d", putResp.StatusCode)
	}

	var putEnvelope SettingsEnvelope
	if err := json.NewDecoder(putResp.Body).Decode(&putEnvelope); err != nil {
		t.Fatalf("failed to decode put response: %v", err)
	}
	if putEnvelope.SchemaVersion != 1 {
		t.Fatalf("expected schema version 1, got %d", putEnvelope.SchemaVersion)
	}
	var putSettings map[string]any
	if err := json.Unmarshal(putEnvelope.Settings, &putSettings); err != nil {
		t.Fatalf("failed to decode settings payload: %v", err)
	}
	if _, ok := putSettings["cloudApiUrl"]; ok {
		t.Fatalf("expected legacy key to be removed, got %+v", putSettings)
	}
	if putSettings["showSegments"] != true {
		t.Fatalf("expected showSegments=true, got %+v", putSettings)
	}

	getResp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodGet,
		"/api/v1/admin/users/"+fixture.member.ID+"/settings",
		nil,
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if getResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for admin settings get, got %d", getResp.StatusCode)
	}

	var getEnvelope SettingsEnvelope
	if err := json.NewDecoder(getResp.Body).Decode(&getEnvelope); err != nil {
		t.Fatalf("failed to decode get response: %v", err)
	}
	if getEnvelope.Version != putEnvelope.Version {
		t.Fatalf("expected same version after readback, got %d vs %d", getEnvelope.Version, putEnvelope.Version)
	}

	if err := fixture.store.SetUserGlobalRoles(ctx, fixture.actor.ID, []string{"user"}); err != nil {
		t.Fatalf("failed to downgrade actor role: %v", err)
	}
	if err := fixture.store.SetUserOrganizationRoles(ctx, fixture.actor.ID, []string{"org_admin"}); err != nil {
		t.Fatalf("failed to set actor org admin role: %v", err)
	}

	orgAdminGet := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodGet,
		"/api/v1/admin/users/"+fixture.member.ID+"/settings",
		nil,
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if orgAdminGet.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for org admin same-org settings get, got %d", orgAdminGet.StatusCode)
	}
}

func TestAdminUserSettingsRoutes_RejectCrossOrganizationScope(t *testing.T) {
	fixture := setupAdminDeleteRoutesTest(t)
	ctx := context.Background()

	if err := fixture.store.SetUserOrganizationRoles(ctx, fixture.actor.ID, []string{"org_admin"}); err != nil {
		t.Fatalf("failed to set actor org admin role: %v", err)
	}

	forbiddenGet := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodGet,
		"/api/v1/admin/users/"+fixture.otherOrgMember.ID+"/settings",
		nil,
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if forbiddenGet.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 for cross-org settings get, got %d", forbiddenGet.StatusCode)
	}

	forbiddenPut := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPut,
		"/api/v1/admin/users/"+fixture.otherOrgMember.ID+"/settings",
		map[string]any{
			"schemaVersion": 1,
			"settings":      map[string]any{"showSegments": false},
		},
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if forbiddenPut.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 for cross-org settings put, got %d", forbiddenPut.StatusCode)
	}
}

func TestAdminUserSettingsRoutes_Reset(t *testing.T) {
	fixture := setupAdminDeleteRoutesTest(t)
	ctx := context.Background()

	if err := fixture.store.SetUserGlobalRoles(ctx, fixture.actor.ID, []string{"super_admin", "user"}); err != nil {
		t.Fatalf("failed to set super admin role: %v", err)
	}

	resetResp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPost,
		"/api/v1/admin/users/"+fixture.member.ID+"/settings/reset",
		nil,
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if resetResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for settings reset, got %d", resetResp.StatusCode)
	}

	var envelope SettingsEnvelope
	if err := json.NewDecoder(resetResp.Body).Decode(&envelope); err != nil {
		t.Fatalf("failed to decode reset response: %v", err)
	}
	if string(envelope.Settings) != "{}" {
		t.Fatalf("expected empty settings after reset, got %s", string(envelope.Settings))
	}
}
