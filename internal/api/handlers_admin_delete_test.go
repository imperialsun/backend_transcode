package api

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/config"
	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
)

type adminDeleteFixture struct {
	app            *fiber.App
	appCtx         *App
	store          *store.Store
	org            *store.Organization
	otherOrg       *store.Organization
	actor          *store.User
	member         *store.User
	otherOrgMember *store.User
	superAdminUser *store.User
	orgAdminUser   *store.User
}

func TestDeleteUser_SucceedsForScopedUserAndWritesAudit(t *testing.T) {
	fixture := setupAdminDeleteRoutesTest(t)
	if err := fixture.store.SetUserOrganizationRoles(context.Background(), fixture.actor.ID, []string{"org_admin"}); err != nil {
		t.Fatalf("failed to set actor organization roles: %v", err)
	}

	resp := performPasswordResetRequest(
		t,
		fixture.app,
		http.MethodDelete,
		"/api/v1/admin/users/"+fixture.member.ID,
		nil,
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for successful delete, got %d", resp.StatusCode)
	}

	reloaded, err := fixture.store.GetUserByID(context.Background(), fixture.member.ID)
	if err != nil {
		t.Fatalf("failed to reload deleted user: %v", err)
	}
	if reloaded != nil {
		t.Fatalf("expected member to be deleted, got %#v", reloaded)
	}

	var count int
	if err := fixture.store.DB.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM audit_logs WHERE action = 'admin.user.delete' AND target_id = ?
	`, fixture.member.ID).Scan(&count); err != nil {
		t.Fatalf("failed to count delete audits: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one delete audit, got %d", count)
	}
}

func TestDeleteUser_RejectsCrossOrganizationScope(t *testing.T) {
	fixture := setupAdminDeleteRoutesTest(t)
	if err := fixture.store.SetUserOrganizationRoles(context.Background(), fixture.actor.ID, []string{"org_admin"}); err != nil {
		t.Fatalf("failed to set actor organization roles: %v", err)
	}

	resp := performPasswordResetRequest(
		t,
		fixture.app,
		http.MethodDelete,
		"/api/v1/admin/users/"+fixture.otherOrgMember.ID,
		nil,
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 for cross-organization delete, got %d", resp.StatusCode)
	}
}

func TestDeleteUser_RejectsSelfDeletion(t *testing.T) {
	fixture := setupAdminDeleteRoutesTest(t)
	if err := fixture.store.SetUserGlobalRoles(context.Background(), fixture.actor.ID, []string{"super_admin", "user"}); err != nil {
		t.Fatalf("failed to set actor global roles: %v", err)
	}

	resp := performPasswordResetRequest(
		t,
		fixture.app,
		http.MethodDelete,
		"/api/v1/admin/users/"+fixture.actor.ID,
		nil,
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("expected 400 for self deletion, got %d", resp.StatusCode)
	}
}

func TestDeleteUser_RejectsDeletingLastActiveSuperAdmin(t *testing.T) {
	fixture := setupAdminDeleteRoutesTest(t)
	if err := fixture.store.SetUserOrganizationRoles(context.Background(), fixture.actor.ID, []string{"org_admin"}); err != nil {
		t.Fatalf("failed to set actor organization roles: %v", err)
	}
	if err := fixture.store.SetUserGlobalRoles(context.Background(), fixture.superAdminUser.ID, []string{"super_admin", "user"}); err != nil {
		t.Fatalf("failed to set target super admin roles: %v", err)
	}

	resp := performPasswordResetRequest(
		t,
		fixture.app,
		http.MethodDelete,
		"/api/v1/admin/users/"+fixture.superAdminUser.ID,
		nil,
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("expected 400 for last super admin deletion, got %d", resp.StatusCode)
	}
}

func TestDeleteUser_RejectsDeletingLastActiveOrganizationAdmin(t *testing.T) {
	fixture := setupAdminDeleteRoutesTest(t)
	if err := fixture.store.SetUserGlobalRoles(context.Background(), fixture.actor.ID, []string{"super_admin", "user"}); err != nil {
		t.Fatalf("failed to set actor global roles: %v", err)
	}
	if err := fixture.store.SetUserOrganizationRoles(context.Background(), fixture.orgAdminUser.ID, []string{"org_admin"}); err != nil {
		t.Fatalf("failed to set target org admin roles: %v", err)
	}

	resp := performPasswordResetRequest(
		t,
		fixture.app,
		http.MethodDelete,
		"/api/v1/admin/users/"+fixture.orgAdminUser.ID,
		nil,
		nil,
		adminHeaders(t, fixture.actor, fixture.appCtx.Config.JWTSecret),
	)
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("expected 400 for last organization admin deletion, got %d", resp.StatusCode)
	}
}

func setupAdminDeleteRoutesTest(t *testing.T) *adminDeleteFixture {
	t.Helper()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "admin-delete.sqlite")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	org, err := st.CreateOrganization(ctx, "Delete Org", "delete-org", "active")
	if err != nil {
		t.Fatalf("failed to create organization: %v", err)
	}
	otherOrg, err := st.CreateOrganization(ctx, "Delete Other Org", "delete-other-org", "active")
	if err != nil {
		t.Fatalf("failed to create other organization: %v", err)
	}

	passwordHash, err := auth.HashPassword("ChangeMe123!")
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}

	createUser := func(orgID, email, status string) *store.User {
		user, createErr := st.CreateUser(ctx, orgID, email, passwordHash, status)
		if createErr != nil {
			t.Fatalf("failed to create user %q: %v", email, createErr)
		}
		return user
	}

	actor := createUser(org.ID, "actor@example.com", "active")
	member := createUser(org.ID, "member@example.com", "active")
	otherOrgMember := createUser(otherOrg.ID, "other-member@example.com", "active")
	superAdminUser := createUser(org.ID, "super-admin@example.com", "active")
	orgAdminUser := createUser(org.ID, "org-admin@example.com", "active")

	appCtx := &App{
		Config: config.Config{
			JWTSecret:        "test-secret",
			AccessTTL:        15 * time.Minute,
			RefreshTTL:       24 * time.Hour,
			AdminAccessTTL:   10 * time.Minute,
			AdminRefreshTTL:  12 * time.Hour,
			CookieSecure:     true,
			AdminCORSOrigins: []string{"https://admin.demeter.test"},
		},
		Store: st,
	}

	app := fiber.New()
	api := app.Group("/api/v1")
	appCtx.RegisterAdminRoutes(api)

	return &adminDeleteFixture{
		app:            app,
		appCtx:         appCtx,
		store:          st,
		org:            org,
		otherOrg:       otherOrg,
		actor:          actor,
		member:         member,
		otherOrgMember: otherOrgMember,
		superAdminUser: superAdminUser,
		orgAdminUser:   orgAdminUser,
	}
}

func adminHeaders(t *testing.T, user *store.User, secret string) map[string]string {
	t.Helper()

	const csrfToken = "csrf-admin-delete"
	token := issueAccessTokenForSession(t, secret, auth.SessionTypeAdmin, auth.Claims{
		UserID:    user.ID,
		OrgID:     user.OrganizationID,
		Email:     user.Email,
		CSRFToken: csrfToken,
	})
	return map[string]string{
		fiber.HeaderAuthorization: "Bearer " + token,
		auth.AdminCSRFHeaderName:  csrfToken,
	}
}
