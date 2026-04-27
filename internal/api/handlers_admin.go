package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/mail"
	"strings"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/mailer"
	"demeter-backend/internal/rbac"
	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
)

// createOrganizationRequest carries the minimal fields required to create a new
// tenant.
type createOrganizationRequest struct {
	Name   string `json:"name"`
	Code   string `json:"code"`
	Status string `json:"status"`
}

// patchOrganizationRequest carries the optional fields accepted by the update
// endpoint.
type patchOrganizationRequest struct {
	Name   *string `json:"name"`
	Code   *string `json:"code"`
	Status *string `json:"status"`
}

// createUserRequest carries the new account data used by the admin create-user
// flow.
type createUserRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Status   string `json:"status"`
}

// bulkCreateUsersRequest carries a batch of email addresses and permission
// overrides for bulk provisioning.
type bulkCreateUsersRequest struct {
	Emails    []string                            `json:"emails"`
	Overrides []store.UserPermissionOverrideInput `json:"overrides"`
}

// bulkCreateUsersResponse summarizes which users were created and which
// requests failed.
type bulkCreateUsersResponse struct {
	Created []bulkCreateUsersResponseItem `json:"created"`
	Failed  []bulkCreateUsersFailedItem   `json:"failed"`
}

// bulkCreateUsersResponseItem describes one successfully created user.
type bulkCreateUsersResponseItem struct {
	ID     string `json:"id"`
	Email  string `json:"email"`
	Status string `json:"status"`
}

// bulkCreateUsersFailedItem describes one failed bulk-provisioning entry.
type bulkCreateUsersFailedItem struct {
	Email  string `json:"email"`
	Error  string `json:"error"`
	UserID string `json:"userId,omitempty"`
}

// patchUserRequest carries the optional fields accepted by the user-update
// endpoint.
type patchUserRequest struct {
	Email          *string `json:"email"`
	Status         *string `json:"status"`
	OrganizationID *string `json:"organizationId"`
}

// updatePasswordRequest carries the replacement password for an admin-managed
// account.
type updatePasswordRequest struct {
	Password string `json:"password"`
}

// updateRoleCodesRequest carries a list of role codes to assign.
type updateRoleCodesRequest struct {
	Codes []string `json:"codes"`
}

// updateEntitlementsRequest carries the permission override list.
type updateEntitlementsRequest struct {
	Overrides []store.UserPermissionOverrideInput `json:"overrides"`
}

// RegisterAdminRoutes installs the admin CRUD and mail routes.
func (a *App) RegisterAdminRoutes(router fiber.Router) {
	a.RegisterAdminCoreRoutes(router)
	a.RegisterAdminMailRoutes(router)
}

// RegisterAdminCoreRoutes installs the core admin CRUD and catalog routes.
func (a *App) RegisterAdminCoreRoutes(router fiber.Router) {
	group := a.adminRouteGroup(router)
	group.Get("/organizations", a.listOrganizations)
	group.Post("/organizations", a.createOrganization)
	group.Patch("/organizations/:id", a.patchOrganization)
	a.registerAdminActivityRoutes(group)
	a.registerBackendErrorRoutes(group)
	a.registerAdminPerformanceRoutes(group)
	a.registerAdminDemeterQueueRoutes(group)
	a.registerAdminDemeterReportQueueRoutes(group)
	a.registerAdminUserSettingsRoutes(group)
	group.Get("/organizations/:id/users", a.listOrganizationUsers)
	group.Get("/users/:id/access", a.getUserAccess)
	group.Patch("/users/:id", a.patchUser)
	group.Delete("/users/:id", a.deleteUser)
	group.Get("/users/:id/activity/summary", a.userActivitySummary)
	group.Delete("/users/:id/activity", a.deleteUserActivity)
	group.Put("/users/:id/password", a.updateUserPassword)
	group.Put("/users/:id/global-roles", a.updateUserGlobalRoles)
	group.Put("/users/:id/org-roles", a.updateUserOrgRoles)
	group.Put("/users/:id/entitlements", a.updateUserEntitlements)
	group.Get("/catalog/roles", a.catalogRoles)
	group.Get("/catalog/permissions", a.catalogPermissions)
}

// RegisterAdminMailRoutes installs the admin routes that trigger transactional
// email sends.
func (a *App) RegisterAdminMailRoutes(router fiber.Router) {
	group := a.adminRouteGroup(router)
	group.Post("/organizations/:id/users", a.createOrganizationUser)
	group.Post("/organizations/:id/users/bulk", a.createOrganizationUsersBulk)
	group.Post("/users/:id/password-reset-email", a.sendUserPasswordResetEmail)
}

// adminRouteGroup applies the common admin session, permission, scope, and CSRF
// checks used by every admin route.
func (a *App) adminRouteGroup(router fiber.Router) fiber.Router {
	return router.Group("/admin", a.AdminAuthRequired(), RequirePermissions("feature.admin"), RequireAdminScope(), RequireAdminCSRF())
}

// listOrganizations returns either every organization or only the caller's
// organization depending on scope.
func (a *App) listOrganizations(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "list_organizations", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "admin", route, "request_unauthorized", "list_organizations", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	ctx := requestContext(c)
	if isSuperAdmin(claims) {
		logAPIStep(c, "admin", route, "load_start", "list_organizations", map[string]any{"scope": "all"})
		orgs, err := a.Store.ListOrganizations(ctx)
		if err != nil {
			logAPIStep(c, "admin", route, "load_error", "list_organizations", map[string]any{"error": err})
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to list organizations"})
		}
		logAPIStep(c, "admin", route, "response_ready", "list_organizations", map[string]any{"organization_count": len(orgs)})
		return c.JSON(orgs)
	}
	logAPIStep(c, "admin", route, "load_start", "list_organizations", map[string]any{"scope": "organization"})
	org, err := a.Store.GetOrganizationByID(ctx, claims.OrgID)
	if err != nil {
		logAPIStep(c, "admin", route, "load_error", "list_organizations", map[string]any{"error": err})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load organization"})
	}
	if org == nil {
		logAPIStep(c, "admin", route, "response_ready", "list_organizations", map[string]any{"organization_count": 0})
		return c.JSON([]store.Organization{})
	}
	logAPIStep(c, "admin", route, "response_ready", "list_organizations", map[string]any{"organization_count": 1})
	return c.JSON([]store.Organization{*org})
}

// createOrganization is reserved to super admins because it creates a new
// tenant.
func (a *App) createOrganization(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "create_organization", nil)

	claims := MustClaims(c)
	if !isSuperAdmin(claims) {
		logAPIStep(c, "admin", route, "request_forbidden", "create_organization", nil)
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "only super admin can create organizations"})
	}
	var req createOrganizationRequest
	if err := c.BodyParser(&req); err != nil {
		logAPIStep(c, "admin", route, "request_parse_error", "create_organization", map[string]any{"error": err})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	if strings.TrimSpace(req.Name) == "" {
		logAPIStep(c, "admin", route, "request_validation_error", "create_organization", map[string]any{"reason": "missing_name"})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "name is required"})
	}
	ctx := requestContext(c)
	logAPIStep(c, "admin", route, "create_start", "create_organization", nil)
	org, err := a.Store.CreateOrganization(ctx, req.Name, req.Code, req.Status)
	if err != nil {
		logAPIStep(c, "admin", route, "create_error", "create_organization", map[string]any{"error": err})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "failed to create organization"})
	}
	logAPIStep(c, "admin", route, "response_ready", "create_organization", map[string]any{"organization_id": org.ID})
	a.writeAdminAudit(ctx, claims, "admin.organization.create", "organization", org.ID, fiber.Map{
		"name":   org.Name,
		"code":   org.Code,
		"status": org.Status,
	})
	return c.Status(fiber.StatusCreated).JSON(org)
}

// patchOrganization updates the organization record after validating
// super-admin scope.
func (a *App) patchOrganization(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "patch_organization", nil)

	claims := MustClaims(c)
	if !isSuperAdmin(claims) {
		logAPIStep(c, "admin", route, "request_forbidden", "patch_organization", nil)
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "only super admin can update organizations"})
	}
	id := strings.TrimSpace(c.Params("id"))
	var req patchOrganizationRequest
	if err := c.BodyParser(&req); err != nil {
		logAPIStep(c, "admin", route, "request_parse_error", "patch_organization", map[string]any{"error": err})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	ctx := requestContext(c)
	logAPIStep(c, "admin", route, "update_start", "patch_organization", map[string]any{"organization_id": id})
	updated, err := a.Store.UpdateOrganization(ctx, id, req.Name, req.Code, req.Status)
	if err != nil {
		logAPIStep(c, "admin", route, "update_error", "patch_organization", map[string]any{"error": err, "organization_id": id})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "failed to update organization"})
	}
	if updated == nil {
		logAPIStep(c, "admin", route, "update_missing", "patch_organization", map[string]any{"organization_id": id})
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "organization not found"})
	}
	logAPIStep(c, "admin", route, "response_ready", "patch_organization", map[string]any{"organization_id": updated.ID})
	a.writeAdminAudit(ctx, claims, "admin.organization.update", "organization", updated.ID, fiber.Map{
		"name":   updated.Name,
		"code":   updated.Code,
		"status": updated.Status,
	})
	return c.JSON(updated)
}

// listOrganizationUsers returns the users visible within one organization
// scope.
func (a *App) listOrganizationUsers(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "list_organization_users", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "admin", route, "request_unauthorized", "list_organization_users", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	orgID := strings.TrimSpace(c.Params("id"))
	if orgID == "" {
		logAPIStep(c, "admin", route, "request_validation_error", "list_organization_users", map[string]any{"reason": "missing_organization_id"})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "organization id is required"})
	}
	if !isSuperAdmin(claims) && claims.OrgID != orgID {
		logAPIStep(c, "admin", route, "request_forbidden", "list_organization_users", map[string]any{"organization_id": orgID})
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}
	logAPIStep(c, "admin", route, "load_start", "list_organization_users", map[string]any{"organization_id": orgID})
	users, err := a.Store.ListUsersByOrganization(requestContext(c), orgID)
	if err != nil {
		logAPIStep(c, "admin", route, "load_error", "list_organization_users", map[string]any{"error": err, "organization_id": orgID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to list organization users"})
	}
	for i := range users {
		users[i].PasswordHash = ""
	}
	logAPIStep(c, "admin", route, "response_ready", "list_organization_users", map[string]any{"organization_id": orgID, "user_count": len(users)})
	return c.JSON(users)
}

// createOrganizationUser provisions a single account and optionally sends the
// onboarding email.
func (a *App) createOrganizationUser(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "create_organization_user", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "admin", route, "request_unauthorized", "create_organization_user", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	orgID := strings.TrimSpace(c.Params("id"))
	if orgID == "" {
		logAPIStep(c, "admin", route, "request_validation_error", "create_organization_user", map[string]any{"reason": "missing_organization_id"})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "organization id is required"})
	}
	if !isSuperAdmin(claims) && claims.OrgID != orgID {
		logAPIStep(c, "admin", route, "request_forbidden", "create_organization_user", map[string]any{"organization_id": orgID})
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}
	var req createUserRequest
	if err := c.BodyParser(&req); err != nil {
		logAPIStep(c, "admin", route, "request_parse_error", "create_organization_user", map[string]any{"error": err, "organization_id": orgID})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	logAPIStep(c, "admin", route, "password_hash_start", "create_organization_user", map[string]any{"organization_id": orgID})
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		logAPIStep(c, "admin", route, "password_hash_error", "create_organization_user", map[string]any{"error": err, "organization_id": orgID})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: err.Error()})
	}
	ctx := requestContext(c)
	logAPIStep(c, "admin", route, "create_start", "create_organization_user", map[string]any{"organization_id": orgID})
	created, err := a.Store.CreateUserWithRoles(ctx, orgID, req.Email, hash, req.Status, []string{"user"}, []string{"org_member"})
	if err != nil {
		logAPIStep(c, "admin", route, "create_error", "create_organization_user", map[string]any{"error": err, "organization_id": orgID})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "failed to create user"})
	}
	created.PasswordHash = ""
	logAPIStep(c, "admin", route, "response_ready", "create_organization_user", map[string]any{"organization_id": created.OrganizationID, "user_id": created.ID})
	a.writeAdminAudit(ctx, claims, "admin.user.create", "user", created.ID, fiber.Map{
		"organizationId": created.OrganizationID,
		"email":          created.Email,
		"status":         created.Status,
	})
	return c.Status(fiber.StatusCreated).JSON(created)
}

// createOrganizationUsersBulk provisions a batch of users and attempts to send
// a provisioning email for each address.
func (a *App) createOrganizationUsersBulk(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "create_organization_users_bulk", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "admin", route, "request_unauthorized", "create_organization_users_bulk", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	orgID := strings.TrimSpace(c.Params("id"))
	if orgID == "" {
		logAPIStep(c, "admin", route, "request_validation_error", "create_organization_users_bulk", map[string]any{"reason": "missing_organization_id"})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "organization id is required"})
	}
	if !isSuperAdmin(claims) && claims.OrgID != orgID {
		logAPIStep(c, "admin", route, "request_forbidden", "create_organization_users_bulk", map[string]any{"organization_id": orgID})
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}
	applicationURL, err := a.applicationPublicURL()
	if err != nil {
		logAPIStep(c, "admin", route, "application_url_unavailable", "create_organization_users_bulk", map[string]any{"organization_id": orgID, "error": err})
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "provisioning email unavailable"})
	}
	if a.Mailer == nil || a.Mailer.Ready() != nil {
		logAPIStep(c, "admin", route, "mailer_unavailable", "create_organization_users_bulk", map[string]any{"organization_id": orgID})
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "provisioning email unavailable"})
	}

	var req bulkCreateUsersRequest
	if err := c.BodyParser(&req); err != nil {
		logAPIStep(c, "admin", route, "request_parse_error", "create_organization_users_bulk", map[string]any{"error": err, "organization_id": orgID})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	if len(req.Emails) == 0 {
		logAPIStep(c, "admin", route, "request_validation_error", "create_organization_users_bulk", map[string]any{"reason": "missing_emails", "organization_id": orgID})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "at least one email is required"})
	}
	normalizedOverrides := store.NormalizePermissionOverrideInputs(req.Overrides)

	ctx := requestContext(c)
	logAPIStep(c, "admin", route, "bulk_start", "create_organization_users_bulk", map[string]any{
		"organization_id": orgID,
		"email_count":     len(req.Emails),
		"override_count":  len(normalizedOverrides),
	})
	result := bulkCreateUsersResponse{
		Created: []bulkCreateUsersResponseItem{},
		Failed:  []bulkCreateUsersFailedItem{},
	}
	seenEmails := map[string]struct{}{}

	for index, rawEmail := range req.Emails {
		normalizedEmail, err := normalizeBulkEmail(rawEmail)
		if err != nil {
			if strings.TrimSpace(rawEmail) == "" {
				continue
			}
			logAPIStep(c, "admin", route, "email_validation_error", "create_organization_users_bulk", map[string]any{"organization_id": orgID, "error": err, "email_index": index + 1})
			result.Failed = append(result.Failed, bulkCreateUsersFailedItem{
				Email: strings.TrimSpace(rawEmail),
				Error: err.Error(),
			})
			continue
		}
		if _, exists := seenEmails[normalizedEmail]; exists {
			logAPIStep(c, "admin", route, "email_duplicate", "create_organization_users_bulk", map[string]any{"organization_id": orgID, "email_index": index + 1})
			result.Failed = append(result.Failed, bulkCreateUsersFailedItem{
				Email: normalizedEmail,
				Error: "duplicate email in request",
			})
			continue
		}
		seenEmails[normalizedEmail] = struct{}{}

		logAPIStep(c, "admin", route, "email_lookup_start", "create_organization_users_bulk", map[string]any{"organization_id": orgID})
		existing, err := a.Store.FindUserByEmail(ctx, normalizedEmail)
		if err != nil {
			logAPIStep(c, "admin", route, "email_lookup_error", "create_organization_users_bulk", map[string]any{"organization_id": orgID, "error": err, "email_index": index + 1})
			result.Failed = append(result.Failed, bulkCreateUsersFailedItem{
				Email: normalizedEmail,
				Error: "failed to check existing user",
			})
			continue
		}
		if existing != nil {
			logAPIStep(c, "admin", route, "email_exists", "create_organization_users_bulk", map[string]any{"organization_id": orgID, "email_index": index + 1})
			result.Failed = append(result.Failed, bulkCreateUsersFailedItem{
				Email: normalizedEmail,
				Error: "email already exists",
			})
			continue
		}

		logAPIStep(c, "admin", route, "temporary_password_start", "create_organization_users_bulk", map[string]any{"organization_id": orgID})
		temporaryPassword, err := auth.GenerateTemporaryPassword(24)
		if err != nil {
			logAPIStep(c, "admin", route, "temporary_password_error", "create_organization_users_bulk", map[string]any{"organization_id": orgID, "error": err})
			result.Failed = append(result.Failed, bulkCreateUsersFailedItem{
				Email: normalizedEmail,
				Error: "failed to generate temporary password",
			})
			continue
		}
		logAPIStep(c, "admin", route, "password_hash_start", "create_organization_users_bulk", map[string]any{"organization_id": orgID})
		hash, err := auth.HashPassword(temporaryPassword)
		if err != nil {
			logAPIStep(c, "admin", route, "password_hash_error", "create_organization_users_bulk", map[string]any{"organization_id": orgID, "error": err})
			result.Failed = append(result.Failed, bulkCreateUsersFailedItem{
				Email: normalizedEmail,
				Error: err.Error(),
			})
			continue
		}

		logAPIStep(c, "admin", route, "create_start", "create_organization_users_bulk", map[string]any{"organization_id": orgID, "email_index": index + 1})
		created, err := a.Store.CreateUserWithRolesAndOverrides(ctx, orgID, normalizedEmail, hash, "active", []string{"user"}, []string{"org_member"}, normalizedOverrides)
		if err != nil {
			logAPIStep(c, "admin", route, "create_error", "create_organization_users_bulk", map[string]any{"organization_id": orgID, "error": err, "email_index": index + 1})
			result.Failed = append(result.Failed, bulkCreateUsersFailedItem{
				Email: normalizedEmail,
				Error: "failed to create user",
			})
			continue
		}

		logAPIStep(c, "admin", route, "provisioning_email_start", "create_organization_users_bulk", map[string]any{"organization_id": orgID, "user_id": created.ID})
		if err := a.Mailer.SendUserProvisioningEmail(ctx, mailer.UserProvisioningEmail{
			ToEmail:           created.Email,
			Login:             created.Email,
			TemporaryPassword: temporaryPassword,
			ApplicationURL:    applicationURL,
		}); err != nil {
			logAPIStep(c, "admin", route, "provisioning_email_error", "create_organization_users_bulk", map[string]any{"organization_id": orgID, "user_id": created.ID, "error": err, "email_index": index + 1})
			result.Failed = append(result.Failed, bulkCreateUsersFailedItem{
				Email:  created.Email,
				Error:  "failed to send provisioning email",
				UserID: created.ID,
			})
			a.writeAdminAudit(ctx, claims, "admin.user.create", "user", created.ID, fiber.Map{
				"organizationId":        created.OrganizationID,
				"email":                 created.Email,
				"status":                created.Status,
				"provisioningMode":      "bulk",
				"provisioningEmailSent": false,
			})
			continue
		}

		created.PasswordHash = ""
		logAPIStep(c, "admin", route, "provisioning_email_success", "create_organization_users_bulk", map[string]any{"organization_id": orgID, "user_id": created.ID})
		result.Created = append(result.Created, bulkCreateUsersResponseItem{
			ID:     created.ID,
			Email:  created.Email,
			Status: created.Status,
		})
		a.writeAdminAudit(ctx, claims, "admin.user.create", "user", created.ID, fiber.Map{
			"organizationId":        created.OrganizationID,
			"email":                 created.Email,
			"status":                created.Status,
			"provisioningMode":      "bulk",
			"provisioningEmailSent": true,
		})
	}

	logAPIStep(c, "admin", route, "response_ready", "create_organization_users_bulk", map[string]any{
		"organization_id": orgID,
		"created_count":   len(result.Created),
		"failed_count":    len(result.Failed),
	})
	return c.JSON(result)
}

func normalizeBulkEmail(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("email is required")
	}
	addr, err := mail.ParseAddress(trimmed)
	if err != nil {
		return "", errors.New("invalid email")
	}
	email := strings.ToLower(strings.TrimSpace(addr.Address))
	if email == "" {
		return "", errors.New("invalid email")
	}
	return email, nil
}

// patchUser applies optional identity or organization changes to a user.
func (a *App) patchUser(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "patch_user", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "admin", route, "request_unauthorized", "patch_user", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	ctx := requestContext(c)
	userID := strings.TrimSpace(c.Params("id"))
	logAPIStep(c, "admin", route, "load_start", "patch_user", map[string]any{"user_id": userID})
	target, err := a.Store.GetUserByID(ctx, userID)
	if err != nil {
		logAPIStep(c, "admin", route, "load_error", "patch_user", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if target == nil {
		logAPIStep(c, "admin", route, "load_missing", "patch_user", map[string]any{"user_id": userID})
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "user not found"})
	}
	if !isSuperAdmin(claims) && claims.OrgID != target.OrganizationID {
		logAPIStep(c, "admin", route, "request_forbidden", "patch_user", map[string]any{"user_id": userID, "organization_id": target.OrganizationID})
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}
	var req patchUserRequest
	if err := c.BodyParser(&req); err != nil {
		logAPIStep(c, "admin", route, "request_parse_error", "patch_user", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	if !isSuperAdmin(claims) {
		req.OrganizationID = nil
	}
	logAPIStep(c, "admin", route, "update_start", "patch_user", map[string]any{"user_id": userID})
	updated, err := a.Store.UpdateUser(ctx, userID, store.UpdateUserInput{
		Email:          req.Email,
		Status:         req.Status,
		OrganizationID: req.OrganizationID,
	})
	if err != nil {
		logAPIStep(c, "admin", route, "update_error", "patch_user", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "failed to update user"})
	}
	updated.PasswordHash = ""
	logAPIStep(c, "admin", route, "refresh_sessions_revoke_start", "patch_user", map[string]any{"user_id": updated.ID})
	if revokeErr := a.Store.RevokeRefreshSessionsByUser(ctx, updated.ID); revokeErr != nil {
		logAPIStep(c, "admin", route, "refresh_sessions_revoke_error", "patch_user", map[string]any{"error": revokeErr, "user_id": updated.ID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to revoke user sessions"})
	}
	logAPIStep(c, "admin", route, "response_ready", "patch_user", map[string]any{"user_id": updated.ID, "organization_id": updated.OrganizationID})
	a.writeAdminAudit(ctx, claims, "admin.user.update", "user", updated.ID, fiber.Map{
		"organizationId": updated.OrganizationID,
		"email":          updated.Email,
		"status":         updated.Status,
	})
	return c.JSON(updated)
}

// updateUserPassword replaces an admin-managed account password and revokes
// active refresh sessions.
func (a *App) updateUserPassword(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "update_user_password", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "admin", route, "request_unauthorized", "update_user_password", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	ctx := requestContext(c)
	userID := strings.TrimSpace(c.Params("id"))
	logAPIStep(c, "admin", route, "load_start", "update_user_password", map[string]any{"user_id": userID})
	target, err := a.Store.GetUserByID(ctx, userID)
	if err != nil {
		logAPIStep(c, "admin", route, "load_error", "update_user_password", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if target == nil {
		logAPIStep(c, "admin", route, "load_missing", "update_user_password", map[string]any{"user_id": userID})
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "user not found"})
	}
	if !isSuperAdmin(claims) && claims.OrgID != target.OrganizationID {
		logAPIStep(c, "admin", route, "request_forbidden", "update_user_password", map[string]any{"user_id": userID, "organization_id": target.OrganizationID})
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}
	var req updatePasswordRequest
	if err := c.BodyParser(&req); err != nil {
		logAPIStep(c, "admin", route, "request_parse_error", "update_user_password", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	logAPIStep(c, "admin", route, "password_hash_start", "update_user_password", map[string]any{"user_id": userID})
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		logAPIStep(c, "admin", route, "password_hash_error", "update_user_password", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: err.Error()})
	}
	logAPIStep(c, "admin", route, "password_update_start", "update_user_password", map[string]any{"user_id": userID})
	if err := a.Store.UpdateUserPassword(ctx, userID, hash); err != nil {
		logAPIStep(c, "admin", route, "password_update_error", "update_user_password", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to update password"})
	}
	logAPIStep(c, "admin", route, "refresh_sessions_revoke_start", "update_user_password", map[string]any{"user_id": userID})
	if err := a.Store.RevokeRefreshSessionsByUser(ctx, userID); err != nil {
		logAPIStep(c, "admin", route, "refresh_sessions_revoke_error", "update_user_password", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to revoke user sessions"})
	}
	logAPIStep(c, "admin", route, "response_ready", "update_user_password", map[string]any{"user_id": userID})
	a.writeAdminAudit(ctx, claims, "admin.user.password.update", "user", userID, fiber.Map{
		"organizationId": target.OrganizationID,
	})
	return c.SendStatus(fiber.StatusNoContent)
}

// deleteUser removes the user after checking that the action will not break
// critical admin coverage.
func (a *App) deleteUser(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "delete_user", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "admin", route, "request_unauthorized", "delete_user", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}

	ctx := requestContext(c)
	userID := strings.TrimSpace(c.Params("id"))
	logAPIStep(c, "admin", route, "load_start", "delete_user", map[string]any{"user_id": userID})
	target, err := a.Store.GetUserByID(ctx, userID)
	if err != nil {
		logAPIStep(c, "admin", route, "load_error", "delete_user", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if target == nil {
		logAPIStep(c, "admin", route, "load_missing", "delete_user", map[string]any{"user_id": userID})
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "user not found"})
	}
	if !isSuperAdmin(claims) && claims.OrgID != target.OrganizationID {
		logAPIStep(c, "admin", route, "request_forbidden", "delete_user", map[string]any{"user_id": userID, "organization_id": target.OrganizationID})
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}
	if claims.UserID == target.ID {
		logAPIStep(c, "admin", route, "request_validation_error", "delete_user", map[string]any{"reason": "self_deletion", "user_id": userID})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "cannot delete your own account"})
	}

	logAPIStep(c, "admin", route, "role_load_start", "delete_user", map[string]any{"user_id": userID})
	globalRoles, err := a.Store.GetGlobalRoleCodesByUser(ctx, target.ID)
	if err != nil {
		logAPIStep(c, "admin", route, "role_load_error", "delete_user", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load global roles"})
	}
	orgRoles, err := a.Store.GetOrganizationRoleCodesByUser(ctx, target.ID)
	if err != nil {
		logAPIStep(c, "admin", route, "role_load_error", "delete_user", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load organization roles"})
	}

	if target.Status == "active" && rbac.HasRole(globalRoles, "super_admin") {
		logAPIStep(c, "admin", route, "protection_check_start", "delete_user", map[string]any{"user_id": userID, "protection": "last_super_admin"})
		activeSuperAdmins, err := a.Store.CountActiveUsersByGlobalRole(ctx, "super_admin")
		if err != nil {
			logAPIStep(c, "admin", route, "protection_check_error", "delete_user", map[string]any{"error": err, "user_id": userID, "protection": "last_super_admin"})
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to verify super admin protections"})
		}
		if activeSuperAdmins <= 1 {
			logAPIStep(c, "admin", route, "request_validation_error", "delete_user", map[string]any{"reason": "last_super_admin", "user_id": userID})
			return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "cannot delete the last active super admin"})
		}
	}

	if target.Status == "active" && rbac.HasRole(orgRoles, "org_admin") {
		logAPIStep(c, "admin", route, "protection_check_start", "delete_user", map[string]any{"user_id": userID, "protection": "last_org_admin"})
		activeOrgAdmins, err := a.Store.CountActiveUsersByOrganizationRole(ctx, target.OrganizationID, "org_admin")
		if err != nil {
			logAPIStep(c, "admin", route, "protection_check_error", "delete_user", map[string]any{"error": err, "user_id": userID, "protection": "last_org_admin"})
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to verify organization admin protections"})
		}
		if activeOrgAdmins <= 1 {
			logAPIStep(c, "admin", route, "request_validation_error", "delete_user", map[string]any{"reason": "last_org_admin", "user_id": userID})
			return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "cannot delete the last active organization admin"})
		}
	}

	logAPIStep(c, "admin", route, "delete_start", "delete_user", map[string]any{"user_id": userID})
	deleted, err := a.Store.DeleteUser(ctx, target.ID)
	if err != nil {
		logAPIStep(c, "admin", route, "delete_error", "delete_user", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to delete user"})
	}
	if !deleted {
		logAPIStep(c, "admin", route, "load_missing", "delete_user", map[string]any{"user_id": userID})
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "user not found"})
	}

	logAPIStep(c, "admin", route, "response_ready", "delete_user", map[string]any{"user_id": userID})
	a.writeAdminAudit(ctx, claims, "admin.user.delete", "user", target.ID, fiber.Map{
		"email":            target.Email,
		"organizationId":   target.OrganizationID,
		"globalRoles":      globalRoles,
		"orgRoles":         orgRoles,
		"status":           target.Status,
		"actorGlobalRoles": claims.GlobalRoles,
		"actorOrgRoles":    claims.OrgRoles,
	})
	return c.SendStatus(fiber.StatusNoContent)
}

// userActivitySummary returns the activity summary for one selected user.
func (a *App) userActivitySummary(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "user_activity_summary", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "admin", route, "request_unauthorized", "user_activity_summary", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}

	ctx := requestContext(c)
	userID := strings.TrimSpace(c.Params("id"))
	logAPIStep(c, "admin", route, "load_start", "user_activity_summary", map[string]any{"user_id": userID})
	target, err := a.Store.GetUserByID(ctx, userID)
	if err != nil {
		logAPIStep(c, "admin", route, "load_error", "user_activity_summary", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if target == nil {
		logAPIStep(c, "admin", route, "load_missing", "user_activity_summary", map[string]any{"user_id": userID})
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "user not found"})
	}
	if !isSuperAdmin(claims) && claims.OrgID != target.OrganizationID {
		logAPIStep(c, "admin", route, "request_forbidden", "user_activity_summary", map[string]any{"user_id": userID, "organization_id": target.OrganizationID})
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}

	fromDay, toDay, err := resolveActivityRange(c.Query("from"), c.Query("to"))
	if err != nil {
		logAPIStep(c, "admin", route, "range_error", "user_activity_summary", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: err.Error()})
	}

	logAPIStep(c, "admin", route, "summary_load_start", "user_activity_summary", map[string]any{
		"user_id": userID,
		"from":    fromDay,
		"to":      toDay,
	})
	summary, err := a.Store.GetUserActivitySummary(ctx, target.ID, fromDay, toDay)
	if err != nil {
		logAPIStep(c, "admin", route, "summary_load_error", "user_activity_summary", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user activity summary"})
	}
	logAPIStep(c, "admin", route, "response_ready", "user_activity_summary", map[string]any{"user_id": userID})
	return c.JSON(summary)
}

// deleteUserActivity purges activity rows for one user while keeping the
// account itself.
func (a *App) deleteUserActivity(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "delete_user_activity", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "admin", route, "request_unauthorized", "delete_user_activity", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}

	ctx := requestContext(c)
	userID := strings.TrimSpace(c.Params("id"))
	logAPIStep(c, "admin", route, "load_start", "delete_user_activity", map[string]any{"user_id": userID})
	target, err := a.Store.GetUserByID(ctx, userID)
	if err != nil {
		logAPIStep(c, "admin", route, "load_error", "delete_user_activity", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if target == nil {
		logAPIStep(c, "admin", route, "load_missing", "delete_user_activity", map[string]any{"user_id": userID})
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "user not found"})
	}
	if !isSuperAdmin(claims) && claims.OrgID != target.OrganizationID {
		logAPIStep(c, "admin", route, "request_forbidden", "delete_user_activity", map[string]any{"user_id": userID, "organization_id": target.OrganizationID})
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}

	logAPIStep(c, "admin", route, "delete_start", "delete_user_activity", map[string]any{"user_id": userID})
	deletedCount, err := a.Store.DeleteUserActivity(ctx, target.ID)
	if err != nil {
		logAPIStep(c, "admin", route, "delete_error", "delete_user_activity", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to delete user activity"})
	}

	logAPIStep(c, "admin", route, "response_ready", "delete_user_activity", map[string]any{"user_id": userID, "deleted_count": deletedCount})
	a.writeAdminAudit(ctx, claims, "admin.user.activity.delete", "user", target.ID, fiber.Map{
		"email":          target.Email,
		"organizationId": target.OrganizationID,
		"deletedCount":   deletedCount,
	})
	return c.SendStatus(fiber.StatusNoContent)
}

// sendUserPasswordResetEmail sends an admin-triggered password-reset message to
// the selected user.
func (a *App) sendUserPasswordResetEmail(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "send_user_password_reset_email", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "admin", route, "request_unauthorized", "send_user_password_reset_email", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	ctx := requestContext(c)
	userID := strings.TrimSpace(c.Params("id"))
	logAPIStep(c, "admin", route, "load_start", "send_user_password_reset_email", map[string]any{"user_id": userID})
	target, err := a.Store.GetUserByID(ctx, userID)
	if err != nil {
		logAPIStep(c, "admin", route, "load_error", "send_user_password_reset_email", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if target == nil {
		logAPIStep(c, "admin", route, "load_missing", "send_user_password_reset_email", map[string]any{"user_id": userID})
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "user not found"})
	}
	if !isSuperAdmin(claims) && claims.OrgID != target.OrganizationID {
		logAPIStep(c, "admin", route, "request_forbidden", "send_user_password_reset_email", map[string]any{"user_id": userID, "organization_id": target.OrganizationID})
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}
	if target.Status != "active" {
		logAPIStep(c, "admin", route, "request_validation_error", "send_user_password_reset_email", map[string]any{"user_id": userID, "reason": "inactive_user"})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "user is inactive"})
	}

	logAPIStep(c, "admin", route, "mail_start", "send_user_password_reset_email", map[string]any{"user_id": userID})
	if err := a.sendPasswordResetForUser(ctx, route, target, auth.SessionTypeApp, claims.UserID); err != nil {
		if errors.Is(err, errPasswordResetUnavailable) {
			logAPIStep(c, "admin", route, "mail_unavailable", "send_user_password_reset_email", map[string]any{"user_id": userID})
			return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "password reset email unavailable"})
		}
		if fiberErr, ok := err.(*fiber.Error); ok {
			logAPIStep(c, "admin", route, "mail_error", "send_user_password_reset_email", map[string]any{"error": err, "user_id": userID})
			return c.Status(fiberErr.Code).JSON(ErrorResponse{Error: fiberErr.Message})
		}
		logAPIStep(c, "admin", route, "mail_error", "send_user_password_reset_email", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to send password reset email"})
	}

	logAPIStep(c, "admin", route, "response_ready", "send_user_password_reset_email", map[string]any{"user_id": userID})
	a.writeAdminAudit(ctx, claims, "admin.user.password_reset_email.send", "user", target.ID, fiber.Map{
		"email": target.Email,
	})
	return c.SendStatus(fiber.StatusNoContent)
}

// updateUserGlobalRoles replaces the user's global role assignment set.
func (a *App) updateUserGlobalRoles(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "update_user_global_roles", nil)

	claims := MustClaims(c)
	if !isSuperAdmin(claims) {
		logAPIStep(c, "admin", route, "request_forbidden", "update_user_global_roles", nil)
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "only super admin can update global roles"})
	}
	ctx := requestContext(c)
	userID := strings.TrimSpace(c.Params("id"))
	var req updateRoleCodesRequest
	if err := c.BodyParser(&req); err != nil {
		logAPIStep(c, "admin", route, "request_parse_error", "update_user_global_roles", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	logAPIStep(c, "admin", route, "update_start", "update_user_global_roles", map[string]any{"user_id": userID, "code_count": len(req.Codes)})
	if err := a.Store.SetUserGlobalRoles(ctx, userID, req.Codes); err != nil {
		logAPIStep(c, "admin", route, "update_error", "update_user_global_roles", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to update global roles"})
	}
	logAPIStep(c, "admin", route, "refresh_sessions_revoke_start", "update_user_global_roles", map[string]any{"user_id": userID})
	if err := a.Store.RevokeRefreshSessionsByUser(ctx, userID); err != nil {
		logAPIStep(c, "admin", route, "refresh_sessions_revoke_error", "update_user_global_roles", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to revoke user sessions"})
	}
	logAPIStep(c, "admin", route, "response_ready", "update_user_global_roles", map[string]any{"user_id": userID})
	a.writeAdminAudit(ctx, claims, "admin.user.global_roles.update", "user", userID, fiber.Map{
		"codes": req.Codes,
	})
	return c.SendStatus(fiber.StatusNoContent)
}

// updateUserOrgRoles replaces the user's organization role assignment set.
func (a *App) updateUserOrgRoles(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "update_user_org_roles", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "admin", route, "request_unauthorized", "update_user_org_roles", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	ctx := requestContext(c)
	userID := strings.TrimSpace(c.Params("id"))
	logAPIStep(c, "admin", route, "load_start", "update_user_org_roles", map[string]any{"user_id": userID})
	target, err := a.Store.GetUserByID(ctx, userID)
	if err != nil {
		logAPIStep(c, "admin", route, "load_error", "update_user_org_roles", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if target == nil {
		logAPIStep(c, "admin", route, "load_missing", "update_user_org_roles", map[string]any{"user_id": userID})
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "user not found"})
	}
	if !isSuperAdmin(claims) && claims.OrgID != target.OrganizationID {
		logAPIStep(c, "admin", route, "request_forbidden", "update_user_org_roles", map[string]any{"user_id": userID, "organization_id": target.OrganizationID})
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}
	var req updateRoleCodesRequest
	if err := c.BodyParser(&req); err != nil {
		logAPIStep(c, "admin", route, "request_parse_error", "update_user_org_roles", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	logAPIStep(c, "admin", route, "update_start", "update_user_org_roles", map[string]any{"user_id": userID, "code_count": len(req.Codes)})
	if err := a.Store.SetUserOrganizationRoles(ctx, userID, req.Codes); err != nil {
		logAPIStep(c, "admin", route, "update_error", "update_user_org_roles", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to update organization roles"})
	}
	logAPIStep(c, "admin", route, "refresh_sessions_revoke_start", "update_user_org_roles", map[string]any{"user_id": userID})
	if err := a.Store.RevokeRefreshSessionsByUser(ctx, userID); err != nil {
		logAPIStep(c, "admin", route, "refresh_sessions_revoke_error", "update_user_org_roles", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to revoke user sessions"})
	}
	logAPIStep(c, "admin", route, "response_ready", "update_user_org_roles", map[string]any{"user_id": userID})
	a.writeAdminAudit(ctx, claims, "admin.user.org_roles.update", "user", userID, fiber.Map{
		"codes": req.Codes,
	})
	return c.SendStatus(fiber.StatusNoContent)
}

// updateUserEntitlements replaces the user's explicit permission overrides.
func (a *App) updateUserEntitlements(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "update_user_entitlements", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "admin", route, "request_unauthorized", "update_user_entitlements", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	ctx := requestContext(c)
	userID := strings.TrimSpace(c.Params("id"))
	logAPIStep(c, "admin", route, "load_start", "update_user_entitlements", map[string]any{"user_id": userID})
	target, err := a.Store.GetUserByID(ctx, userID)
	if err != nil {
		logAPIStep(c, "admin", route, "load_error", "update_user_entitlements", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if target == nil {
		logAPIStep(c, "admin", route, "load_missing", "update_user_entitlements", map[string]any{"user_id": userID})
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "user not found"})
	}
	if !isSuperAdmin(claims) && claims.OrgID != target.OrganizationID {
		logAPIStep(c, "admin", route, "request_forbidden", "update_user_entitlements", map[string]any{"user_id": userID, "organization_id": target.OrganizationID})
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}
	var req updateEntitlementsRequest
	if err := c.BodyParser(&req); err != nil {
		logAPIStep(c, "admin", route, "request_parse_error", "update_user_entitlements", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	logAPIStep(c, "admin", route, "update_start", "update_user_entitlements", map[string]any{"user_id": userID, "override_count": len(req.Overrides)})
	if err := a.Store.SetUserPermissionOverrides(ctx, userID, req.Overrides); err != nil {
		logAPIStep(c, "admin", route, "update_error", "update_user_entitlements", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to update entitlements"})
	}
	logAPIStep(c, "admin", route, "refresh_sessions_revoke_start", "update_user_entitlements", map[string]any{"user_id": userID})
	if err := a.Store.RevokeRefreshSessionsByUser(ctx, userID); err != nil {
		logAPIStep(c, "admin", route, "refresh_sessions_revoke_error", "update_user_entitlements", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to revoke user sessions"})
	}
	logAPIStep(c, "admin", route, "response_ready", "update_user_entitlements", map[string]any{"user_id": userID})
	a.writeAdminAudit(ctx, claims, "admin.user.entitlements.update", "user", userID, fiber.Map{
		"overrides": req.Overrides,
	})
	return c.SendStatus(fiber.StatusNoContent)
}

// getUserAccess returns the full live authorization context for the user.
func (a *App) getUserAccess(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "get_user_access", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "admin", route, "request_unauthorized", "get_user_access", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	ctx := requestContext(c)
	userID := strings.TrimSpace(c.Params("id"))
	logAPIStep(c, "admin", route, "load_start", "get_user_access", map[string]any{"user_id": userID})
	target, err := a.Store.GetUserByID(ctx, userID)
	if err != nil {
		logAPIStep(c, "admin", route, "load_error", "get_user_access", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if target == nil {
		logAPIStep(c, "admin", route, "load_missing", "get_user_access", map[string]any{"user_id": userID})
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "user not found"})
	}
	if !isSuperAdmin(claims) && claims.OrgID != target.OrganizationID {
		logAPIStep(c, "admin", route, "request_forbidden", "get_user_access", map[string]any{"user_id": userID, "organization_id": target.OrganizationID})
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}
	logAPIStep(c, "admin", route, "role_load_start", "get_user_access", map[string]any{"user_id": userID})
	globalRoles, err := a.Store.GetGlobalRoleCodesByUser(ctx, userID)
	if err != nil {
		logAPIStep(c, "admin", route, "role_load_error", "get_user_access", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load global roles"})
	}
	orgRoles, err := a.Store.GetOrganizationRoleCodesByUser(ctx, userID)
	if err != nil {
		logAPIStep(c, "admin", route, "role_load_error", "get_user_access", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load organization roles"})
	}
	overrides, err := a.Store.GetUserPermissionOverrides(ctx, userID)
	if err != nil {
		logAPIStep(c, "admin", route, "load_error", "get_user_access", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load permission overrides"})
	}
	effectivePermissions, err := a.Store.ResolveEffectivePermissions(ctx, userID)
	if err != nil {
		logAPIStep(c, "admin", route, "load_error", "get_user_access", map[string]any{"error": err, "user_id": userID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to resolve effective permissions"})
	}
	target.PasswordHash = ""
	logAPIStep(c, "admin", route, "response_ready", "get_user_access", map[string]any{"user_id": userID})
	return c.JSON(fiber.Map{
		"user":                 target,
		"globalRoles":          globalRoles,
		"orgRoles":             orgRoles,
		"overrides":            overrides,
		"effectivePermissions": effectivePermissions,
	})
}

// catalogRoles returns the current global and organization role catalogs.
func (a *App) catalogRoles(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "catalog_roles", nil)

	ctx := requestContext(c)
	logAPIStep(c, "admin", route, "load_start", "catalog_roles", nil)
	globalRoles, err := a.Store.ListGlobalRolesCatalog(ctx)
	if err != nil {
		logAPIStep(c, "admin", route, "load_error", "catalog_roles", map[string]any{"error": err})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to list global roles"})
	}
	organizationRoles, err := a.Store.ListOrganizationRolesCatalog(ctx)
	if err != nil {
		logAPIStep(c, "admin", route, "load_error", "catalog_roles", map[string]any{"error": err})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to list organization roles"})
	}
	logAPIStep(c, "admin", route, "response_ready", "catalog_roles", map[string]any{"global_count": len(globalRoles), "organization_count": len(organizationRoles)})
	return c.JSON(fiber.Map{
		"global":       globalRoles,
		"organization": organizationRoles,
	})
}

// catalogPermissions returns the seeded permission catalog.
func (a *App) catalogPermissions(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "catalog_permissions", nil)

	logAPIStep(c, "admin", route, "load_start", "catalog_permissions", nil)
	permissions, err := a.Store.ListPermissionsCatalog(requestContext(c))
	if err != nil {
		logAPIStep(c, "admin", route, "load_error", "catalog_permissions", map[string]any{"error": err})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to list permissions"})
	}
	logAPIStep(c, "admin", route, "response_ready", "catalog_permissions", map[string]any{"permission_count": len(permissions)})
	return c.JSON(permissions)
}

func isSuperAdmin(claims *auth.Claims) bool {
	if claims == nil {
		return false
	}
	return rbac.HasRole(claims.GlobalRoles, "super_admin")
}

// writeAdminAudit writes a structured audit log for admin mutations.
func (a *App) writeAdminAudit(ctx context.Context, claims *auth.Claims, action, targetType, targetID string, payload any) {
	if claims == nil {
		return
	}
	safePayload := json.RawMessage(`{}`)
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err == nil && len(strings.TrimSpace(string(raw))) > 0 {
			safePayload = raw
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_ = a.Store.InsertAuditLog(ctx, store.AuditLogInput{
		ActorUserID:    claims.UserID,
		OrganizationID: claims.OrgID,
		Action:         action,
		TargetType:     targetType,
		TargetID:       targetID,
		PayloadJSON:    safePayload,
	})
}
