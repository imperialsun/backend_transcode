package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"demeter-backend/internal/auth"
	"github.com/gofiber/fiber/v2"
)

func TestAdminListOrganizations_SuperAdminAndOrgAdminScopes(t *testing.T) {
	fixture := setupAdminDeleteRoutesTest(t)
	ctx := context.Background()

	if err := fixture.store.SetUserGlobalRoles(ctx, fixture.actor.ID, []string{"super_admin", "user"}); err != nil {
		t.Fatalf("failed to set super admin role: %v", err)
	}

	resp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodGet,
		"/api/v1/admin/organizations",
		nil,
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for super admin list, got %d", resp.StatusCode)
	}

	var orgs []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&orgs); err != nil {
		t.Fatalf("failed to decode organizations: %v", err)
	}
	if len(orgs) != 2 {
		t.Fatalf("expected 2 organizations for super admin, got %d", len(orgs))
	}

	if err := fixture.store.SetUserGlobalRoles(ctx, fixture.actor.ID, []string{"user"}); err != nil {
		t.Fatalf("failed to set actor roles: %v", err)
	}
	if err := fixture.store.SetUserOrganizationRoles(ctx, fixture.actor.ID, []string{"org_admin"}); err != nil {
		t.Fatalf("failed to set org admin role: %v", err)
	}

	resp = performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodGet,
		"/api/v1/admin/organizations",
		nil,
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for org admin list, got %d", resp.StatusCode)
	}

	orgs = nil
	if err := json.NewDecoder(resp.Body).Decode(&orgs); err != nil {
		t.Fatalf("failed to decode organizations: %v", err)
	}
	if len(orgs) != 1 {
		t.Fatalf("expected 1 organization for org admin, got %d", len(orgs))
	}
}

func TestAdminCreateAndPatchOrganization(t *testing.T) {
	fixture := setupAdminDeleteRoutesTest(t)
	ctx := context.Background()

	if err := fixture.store.SetUserGlobalRoles(ctx, fixture.actor.ID, []string{"super_admin", "user"}); err != nil {
		t.Fatalf("failed to set super admin role: %v", err)
	}

	badResp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPost,
		"/api/v1/admin/organizations",
		map[string]string{"name": ""},
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if badResp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("expected 400 for missing name, got %d", badResp.StatusCode)
	}

	createResp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPost,
		"/api/v1/admin/organizations",
		map[string]string{"name": "Created Org", "code": "created-org", "status": "active"},
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if createResp.StatusCode != fiber.StatusCreated {
		t.Fatalf("expected 201 for create organization, got %d", createResp.StatusCode)
	}

	var created map[string]any
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatalf("failed to decode organization create response: %v", err)
	}
	createdID, _ := created["id"].(string)
	if createdID == "" {
		t.Fatal("expected created organization id")
	}

	patchResp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPatch,
		"/api/v1/admin/organizations/"+createdID,
		map[string]string{"name": "Renamed Org", "status": "inactive"},
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if patchResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for patch organization, got %d", patchResp.StatusCode)
	}

	var patched map[string]any
	if err := json.NewDecoder(patchResp.Body).Decode(&patched); err != nil {
		t.Fatalf("failed to decode organization patch response: %v", err)
	}
	if patched["name"] != "Renamed Org" || patched["status"] != "inactive" {
		t.Fatalf("unexpected patched organization payload: %+v", patched)
	}

	missingResp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPatch,
		"/api/v1/admin/organizations/missing-org",
		map[string]string{"name": "Ghost Org"},
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if missingResp.StatusCode != fiber.StatusNotFound {
		t.Fatalf("expected 404 for missing organization, got %d", missingResp.StatusCode)
	}

	if err := fixture.store.SetUserGlobalRoles(ctx, fixture.actor.ID, []string{"user"}); err != nil {
		t.Fatalf("failed to downgrade actor roles: %v", err)
	}
	if err := fixture.store.SetUserOrganizationRoles(ctx, fixture.actor.ID, []string{"org_admin"}); err != nil {
		t.Fatalf("failed to set org admin role: %v", err)
	}
	forbiddenResp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPost,
		"/api/v1/admin/organizations",
		map[string]string{"name": "Forbidden Org", "code": "forbidden-org", "status": "active"},
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if forbiddenResp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 for org admin organization create, got %d", forbiddenResp.StatusCode)
	}
}

func TestAdminUserCrudAndAccessEndpoints(t *testing.T) {
	fixture := setupAdminDeleteRoutesTest(t)
	ctx := context.Background()

	if err := fixture.store.SetUserGlobalRoles(ctx, fixture.actor.ID, []string{"super_admin", "user"}); err != nil {
		t.Fatalf("failed to set super admin role: %v", err)
	}
	if err := fixture.store.SetUserOrganizationRoles(ctx, fixture.actor.ID, []string{"org_admin"}); err != nil {
		t.Fatalf("failed to set org admin role: %v", err)
	}

	listResp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodGet,
		"/api/v1/admin/organizations/"+fixture.org.ID+"/users",
		nil,
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if listResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for listing organization users, got %d", listResp.StatusCode)
	}

	createResp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPost,
		"/api/v1/admin/organizations/"+fixture.org.ID+"/users",
		map[string]string{"email": "created@example.com", "password": "ChangeMe123!", "status": "active"},
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if createResp.StatusCode != fiber.StatusCreated {
		t.Fatalf("expected 201 for create user, got %d", createResp.StatusCode)
	}

	var created map[string]any
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatalf("failed to decode user create response: %v", err)
	}
	createdUserID, _ := created["id"].(string)
	if createdUserID == "" {
		t.Fatal("expected created user id")
	}

	patchResp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPatch,
		"/api/v1/admin/users/"+createdUserID,
		map[string]string{"email": "patched@example.com", "status": "inactive"},
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if patchResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for patch user, got %d", patchResp.StatusCode)
	}

	passwordResp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPut,
		"/api/v1/admin/users/"+createdUserID+"/password",
		map[string]string{"password": "NewChangeMe123!"},
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if passwordResp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for password update, got %d", passwordResp.StatusCode)
	}

	rolesResp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPut,
		"/api/v1/admin/users/"+createdUserID+"/global-roles",
		map[string][]string{"codes": {"user"}},
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if rolesResp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for global role update, got %d", rolesResp.StatusCode)
	}

	orgRolesResp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPut,
		"/api/v1/admin/users/"+createdUserID+"/org-roles",
		map[string][]string{"codes": {"org_member"}},
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if orgRolesResp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for org role update, got %d", orgRolesResp.StatusCode)
	}

	entitlementsResp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPut,
		"/api/v1/admin/users/"+createdUserID+"/entitlements",
		map[string]any{"overrides": []map[string]string{{"permissionCode": "feature.settings", "effect": "allow"}}},
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if entitlementsResp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for entitlements update, got %d", entitlementsResp.StatusCode)
	}

	accessResp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodGet,
		"/api/v1/admin/users/"+createdUserID+"/access",
		nil,
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if accessResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for access endpoint, got %d", accessResp.StatusCode)
	}

	var access map[string]any
	if err := json.NewDecoder(accessResp.Body).Decode(&access); err != nil {
		t.Fatalf("failed to decode user access response: %v", err)
	}
	if access["user"] == nil {
		t.Fatalf("expected user payload in access response: %+v", access)
	}
}

func TestAdminCatalogEndpointsAndRoleRestrictions(t *testing.T) {
	fixture := setupAdminDeleteRoutesTest(t)
	ctx := context.Background()

	if err := fixture.store.SetUserGlobalRoles(ctx, fixture.actor.ID, []string{"super_admin", "user"}); err != nil {
		t.Fatalf("failed to set super admin role: %v", err)
	}

	rolesResp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodGet,
		"/api/v1/admin/catalog/roles",
		nil,
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if rolesResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for catalog roles, got %d", rolesResp.StatusCode)
	}

	permissionsResp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodGet,
		"/api/v1/admin/catalog/permissions",
		nil,
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if permissionsResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for catalog permissions, got %d", permissionsResp.StatusCode)
	}

	if err := fixture.store.SetUserGlobalRoles(ctx, fixture.actor.ID, []string{"user"}); err != nil {
		t.Fatalf("failed to downgrade actor roles: %v", err)
	}
	if err := fixture.store.SetUserOrganizationRoles(ctx, fixture.actor.ID, []string{"org_admin"}); err != nil {
		t.Fatalf("failed to set org admin role: %v", err)
	}

	forbidden := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPut,
		"/api/v1/admin/users/"+fixture.member.ID+"/global-roles",
		map[string][]string{"codes": {"user"}},
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if forbidden.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 for org admin updating global roles, got %d", forbidden.StatusCode)
	}
}

func TestAdminUpdateUserScopes(t *testing.T) {
	fixture := setupAdminDeleteRoutesTest(t)
	ctx := context.Background()

	if err := fixture.store.SetUserGlobalRoles(ctx, fixture.actor.ID, []string{"user"}); err != nil {
		t.Fatalf("failed to set actor roles: %v", err)
	}
	if err := fixture.store.SetUserOrganizationRoles(ctx, fixture.actor.ID, []string{"org_admin"}); err != nil {
		t.Fatalf("failed to set actor org admin role: %v", err)
	}

	forbidden := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPatch,
		"/api/v1/admin/users/"+fixture.otherOrgMember.ID,
		map[string]string{"status": "inactive"},
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if forbidden.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 for cross-org patch, got %d", forbidden.StatusCode)
	}

	invalidPassword := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPut,
		"/api/v1/admin/users/"+fixture.member.ID+"/password",
		map[string]string{"password": "short"},
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if invalidPassword.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("expected 400 for invalid password, got %d", invalidPassword.StatusCode)
	}

	missingAccess := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodGet,
		"/api/v1/admin/users/missing-user/access",
		nil,
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if missingAccess.StatusCode != fiber.StatusNotFound {
		t.Fatalf("expected 404 for missing access target, got %d", missingAccess.StatusCode)
	}

	missingOrgRoles := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPut,
		"/api/v1/admin/users/missing-user/org-roles",
		map[string][]string{"codes": {"org_member"}},
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if missingOrgRoles.StatusCode != fiber.StatusNotFound {
		t.Fatalf("expected 404 for missing user org-role update, got %d", missingOrgRoles.StatusCode)
	}

	missingEntitlements := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPut,
		"/api/v1/admin/users/missing-user/entitlements",
		map[string]any{"overrides": []map[string]string{{"permissionCode": "feature.settings", "effect": "allow"}}},
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if missingEntitlements.StatusCode != fiber.StatusNotFound {
		t.Fatalf("expected 404 for missing user entitlements update, got %d", missingEntitlements.StatusCode)
	}
}

func TestAdminUserEndpoints_ForbiddenAndValidationBranches(t *testing.T) {
	fixture := setupAdminDeleteRoutesTest(t)
	ctx := context.Background()

	if err := fixture.store.SetUserGlobalRoles(ctx, fixture.actor.ID, []string{"user"}); err != nil {
		t.Fatalf("failed to set actor roles: %v", err)
	}
	if err := fixture.store.SetUserOrganizationRoles(ctx, fixture.actor.ID, []string{"org_admin"}); err != nil {
		t.Fatalf("failed to set actor org admin role: %v", err)
	}

	listForbidden := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodGet,
		"/api/v1/admin/organizations/"+fixture.otherOrg.ID+"/users",
		nil,
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if listForbidden.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 for cross-org user listing, got %d", listForbidden.StatusCode)
	}

	createForbidden := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPost,
		"/api/v1/admin/organizations/"+fixture.otherOrg.ID+"/users",
		map[string]string{"email": "blocked@example.com", "password": "ChangeMe123!", "status": "active"},
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if createForbidden.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 for cross-org user creation, got %d", createForbidden.StatusCode)
	}

	badCreate := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPost,
		"/api/v1/admin/organizations/"+fixture.org.ID+"/users",
		map[string]string{"email": "bad@example.com", "password": "short", "status": "active"},
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if badCreate.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("expected 400 for invalid user password, got %d", badCreate.StatusCode)
	}

	missingPatch := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPatch,
		"/api/v1/admin/users/missing-user",
		map[string]string{"status": "inactive"},
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if missingPatch.StatusCode != fiber.StatusNotFound {
		t.Fatalf("expected 404 for missing patch target, got %d", missingPatch.StatusCode)
	}

	missingPassword := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPut,
		"/api/v1/admin/users/missing-user/password",
		map[string]string{"password": "NewChangeMe123!"},
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if missingPassword.StatusCode != fiber.StatusNotFound {
		t.Fatalf("expected 404 for missing password target, got %d", missingPassword.StatusCode)
	}

	forbiddenAccess := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodGet,
		"/api/v1/admin/users/"+fixture.otherOrgMember.ID+"/access",
		nil,
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if forbiddenAccess.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 for cross-org access lookup, got %d", forbiddenAccess.StatusCode)
	}
}

func TestAdminHeadersIssueAdminAudienceToken(t *testing.T) {
	fixture := setupAdminDeleteRoutesTest(t)
	headers := adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret)
	rawToken := headers[fiber.HeaderAuthorization][7:]

	claims, err := auth.ParseAccessToken(fixture.appCtx.Config.JWTSecret, rawToken)
	if err != nil {
		t.Fatalf("failed to parse admin token: %v", err)
	}
	if !auth.HasAudience(claims, auth.SessionTypeAdmin) {
		t.Fatal("expected adminHeaders to issue an admin-audience token")
	}
}
