package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestCreateOrganizationUsersBulk_CreatesUsersAndSendsProvisioningEmails(t *testing.T) {
	fixture := setupPasswordResetRoutesTest(t)
	ctx := context.Background()

	resp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPost,
		"/api/v1/admin/organizations/"+fixture.org.ID+"/users/bulk",
		map[string]any{
			"emails": []string{
				"bulk.one@example.com",
				"bulk.two@example.com",
			},
			"overrides": []map[string]string{
				{
					"permissionCode": "feature.settings",
					"effect":         "deny",
				},
				{
					"permissionCode": "provider.cloud.whisper",
					"effect":         "deny",
				},
			},
		},
		nil,
		adminHeaders(t, fixture.adminUser, fixture.appCtx.Config.JWTSecret),
	)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for bulk create, got %d", resp.StatusCode)
	}

	var payload bulkCreateUsersResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode bulk response: %v", err)
	}
	if len(payload.Created) != 2 {
		t.Fatalf("expected 2 created users, got %+v", payload.Created)
	}
	if len(payload.Failed) != 0 {
		t.Fatalf("expected no failures, got %+v", payload.Failed)
	}
	if len(fixture.mailer.provisionedSent) != 2 {
		t.Fatalf("expected 2 provisioning emails, got %d", len(fixture.mailer.provisionedSent))
	}
	for _, sent := range fixture.mailer.provisionedSent {
		if sent.ApplicationURL != "https://app.demeter.test/" {
			t.Fatalf("expected normalized app public url in provisioning email, got %q", sent.ApplicationURL)
		}
	}

	for _, email := range []string{"bulk.one@example.com", "bulk.two@example.com"} {
		createdUser, err := fixture.store.FindUserByEmail(ctx, email)
		if err != nil {
			t.Fatalf("failed to reload created user %s: %v", email, err)
		}
		if createdUser == nil {
			t.Fatalf("expected created user %s to exist", email)
		}
		globalRoles, err := fixture.store.GetGlobalRoleCodesByUser(ctx, createdUser.ID)
		if err != nil {
			t.Fatalf("failed to load global roles for %s: %v", email, err)
		}
		if len(globalRoles) != 1 || globalRoles[0] != "user" {
			t.Fatalf("expected default global role for %s, got %v", email, globalRoles)
		}
		orgRoles, err := fixture.store.GetOrganizationRoleCodesByUser(ctx, createdUser.ID)
		if err != nil {
			t.Fatalf("failed to load organization roles for %s: %v", email, err)
		}
		if len(orgRoles) != 1 || orgRoles[0] != "org_member" {
			t.Fatalf("expected default organization role for %s, got %v", email, orgRoles)
		}
		overrides, err := fixture.store.GetUserPermissionOverrides(ctx, createdUser.ID)
		if err != nil {
			t.Fatalf("failed to load overrides for %s: %v", email, err)
		}
		if len(overrides) != 2 {
			t.Fatalf("expected 2 overrides for %s, got %+v", email, overrides)
		}
		perms, err := fixture.store.ResolveEffectivePermissions(ctx, createdUser.ID)
		if err != nil {
			t.Fatalf("failed to resolve permissions for %s: %v", email, err)
		}
		for _, forbidden := range []string{"feature.settings", "provider.cloud.whisper"} {
			for _, perm := range perms {
				if perm == forbidden {
					t.Fatalf("expected %s to be denied for %s, got %v", forbidden, email, perms)
				}
			}
		}
	}
}

func TestCreateOrganizationUsersBulk_AllowsSuperAdminAcrossOrganizations(t *testing.T) {
	fixture := setupPasswordResetRoutesTest(t)
	ctx := context.Background()

	if err := fixture.store.SetUserGlobalRoles(ctx, fixture.adminUser.ID, []string{"super_admin", "user"}); err != nil {
		t.Fatalf("failed to set super admin role: %v", err)
	}

	resp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPost,
		"/api/v1/admin/organizations/"+fixture.otherOrg.ID+"/users/bulk",
		map[string]any{
			"emails": []string{"super.bulk@example.com"},
		},
		nil,
		adminHeaders(t, fixture.adminUser, fixture.appCtx.Config.JWTSecret),
	)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for super admin bulk create, got %d", resp.StatusCode)
	}

	var payload bulkCreateUsersResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode bulk response: %v", err)
	}
	if len(payload.Created) != 1 {
		t.Fatalf("expected one created user, got %+v", payload.Created)
	}

	createdUser, err := fixture.store.FindUserByEmail(ctx, "super.bulk@example.com")
	if err != nil {
		t.Fatalf("failed to reload created user: %v", err)
	}
	if createdUser == nil {
		t.Fatal("expected created user to exist")
	}
	if createdUser.OrganizationID != fixture.otherOrg.ID {
		t.Fatalf("expected user to be created in target org, got %s", createdUser.OrganizationID)
	}
}

func TestCreateOrganizationUsersBulk_ReportsDuplicatesAndInvalidEmails(t *testing.T) {
	fixture := setupPasswordResetRoutesTest(t)
	ctx := context.Background()

	resp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPost,
		"/api/v1/admin/organizations/"+fixture.org.ID+"/users/bulk",
		map[string]any{
			"emails": []string{
				fixture.activeUser.Email,
				"new.bulk@example.com",
				"new.bulk@example.com",
				"not-an-email",
				"   ",
			},
		},
		nil,
		adminHeaders(t, fixture.adminUser, fixture.appCtx.Config.JWTSecret),
	)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for partial bulk create, got %d", resp.StatusCode)
	}

	var payload bulkCreateUsersResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode bulk response: %v", err)
	}
	if len(payload.Created) != 1 {
		t.Fatalf("expected one created user, got %+v", payload.Created)
	}
	if len(payload.Failed) != 3 {
		t.Fatalf("expected three failures, got %+v", payload.Failed)
	}

	var seenExisting, seenDuplicate, seenInvalid bool
	for _, failed := range payload.Failed {
		switch failed.Error {
		case "email already exists":
			seenExisting = failed.Email == fixture.activeUser.Email
		case "duplicate email in request":
			seenDuplicate = failed.Email == "new.bulk@example.com"
		case "invalid email":
			seenInvalid = failed.Email == "not-an-email"
		}
	}
	if !seenExisting || !seenDuplicate || !seenInvalid {
		t.Fatalf("unexpected failure payload: %+v", payload.Failed)
	}

	createdUser, err := fixture.store.FindUserByEmail(ctx, "new.bulk@example.com")
	if err != nil {
		t.Fatalf("failed to reload created bulk user: %v", err)
	}
	if createdUser == nil {
		t.Fatal("expected new bulk user to exist")
	}
}

func TestCreateOrganizationUsersBulk_PreservesCreatedUsersWhenProvisioningEmailFails(t *testing.T) {
	fixture := setupPasswordResetRoutesTest(t)
	ctx := context.Background()
	fixture.mailer.sendErr = errors.New("smtp down")

	resp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPost,
		"/api/v1/admin/organizations/"+fixture.org.ID+"/users/bulk",
		map[string]any{
			"emails": []string{"smtp.fail@example.com"},
		},
		nil,
		adminHeaders(t, fixture.adminUser, fixture.appCtx.Config.JWTSecret),
	)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for bulk create with smtp failure, got %d", resp.StatusCode)
	}

	var payload bulkCreateUsersResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode bulk response: %v", err)
	}
	if len(payload.Created) != 0 {
		t.Fatalf("expected no created results, got %+v", payload.Created)
	}
	if len(payload.Failed) != 1 {
		t.Fatalf("expected one failure, got %+v", payload.Failed)
	}
	if payload.Failed[0].UserID == "" {
		t.Fatalf("expected failure to reference created user id, got %+v", payload.Failed[0])
	}

	createdUser, err := fixture.store.FindUserByEmail(ctx, "smtp.fail@example.com")
	if err != nil {
		t.Fatalf("failed to reload created user: %v", err)
	}
	if createdUser == nil {
		t.Fatal("expected user to remain in database after smtp failure")
	}
}

func TestCreateOrganizationUsersBulk_RequiresApplicationPublicURL(t *testing.T) {
	fixture := setupPasswordResetRoutesTest(t)
	fixture.appCtx.Config.AppPublicURL = ""

	resp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodPost,
		"/api/v1/admin/organizations/"+fixture.org.ID+"/users/bulk",
		map[string]any{
			"emails": []string{"app.url.fail@example.com"},
		},
		nil,
		adminHeaders(t, fixture.adminUser, fixture.appCtx.Config.JWTSecret),
	)
	if resp.StatusCode != fiber.StatusServiceUnavailable {
		t.Fatalf("expected 503 when app public url is unavailable, got %d", resp.StatusCode)
	}
	if len(fixture.mailer.provisionedSent) != 0 {
		t.Fatalf("expected no provisioning email when app public url is unavailable, got %d", len(fixture.mailer.provisionedSent))
	}
}
