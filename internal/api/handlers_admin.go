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

type createOrganizationRequest struct {
	Name   string `json:"name"`
	Code   string `json:"code"`
	Status string `json:"status"`
}

type patchOrganizationRequest struct {
	Name   *string `json:"name"`
	Code   *string `json:"code"`
	Status *string `json:"status"`
}

type createUserRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Status   string `json:"status"`
}

type bulkCreateUsersRequest struct {
	Emails []string `json:"emails"`
}

type bulkCreateUsersResponse struct {
	Created []bulkCreateUsersResponseItem `json:"created"`
	Failed  []bulkCreateUsersFailedItem   `json:"failed"`
}

type bulkCreateUsersResponseItem struct {
	ID     string `json:"id"`
	Email  string `json:"email"`
	Status string `json:"status"`
}

type bulkCreateUsersFailedItem struct {
	Email  string `json:"email"`
	Error  string `json:"error"`
	UserID string `json:"userId,omitempty"`
}

type patchUserRequest struct {
	Email          *string `json:"email"`
	Status         *string `json:"status"`
	OrganizationID *string `json:"organizationId"`
}

type updatePasswordRequest struct {
	Password string `json:"password"`
}

type updateRoleCodesRequest struct {
	Codes []string `json:"codes"`
}

type updateEntitlementsRequest struct {
	Overrides []store.UserPermissionOverrideInput `json:"overrides"`
}

func (a *App) RegisterAdminRoutes(router fiber.Router) {
	a.RegisterAdminCoreRoutes(router)
	a.RegisterAdminMailRoutes(router)
}

func (a *App) RegisterAdminCoreRoutes(router fiber.Router) {
	group := a.adminRouteGroup(router)
	group.Get("/organizations", a.listOrganizations)
	group.Post("/organizations", a.createOrganization)
	group.Patch("/organizations/:id", a.patchOrganization)
	a.registerAdminActivityRoutes(group)
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

func (a *App) RegisterAdminMailRoutes(router fiber.Router) {
	group := a.adminRouteGroup(router)
	group.Post("/organizations/:id/users", a.createOrganizationUser)
	group.Post("/organizations/:id/users/bulk", a.createOrganizationUsersBulk)
	group.Post("/users/:id/password-reset-email", a.sendUserPasswordResetEmail)
}

func (a *App) adminRouteGroup(router fiber.Router) fiber.Router {
	return router.Group("/admin", a.AdminAuthRequired(), RequirePermissions("feature.admin"), RequireAdminScope(), RequireAdminCSRF())
}

func (a *App) listOrganizations(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	ctx := requestContext(c)
	if isSuperAdmin(claims) {
		orgs, err := a.Store.ListOrganizations(ctx)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to list organizations"})
		}
		return c.JSON(orgs)
	}
	org, err := a.Store.GetOrganizationByID(ctx, claims.OrgID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load organization"})
	}
	if org == nil {
		return c.JSON([]store.Organization{})
	}
	return c.JSON([]store.Organization{*org})
}

func (a *App) createOrganization(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if !isSuperAdmin(claims) {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "only super admin can create organizations"})
	}
	var req createOrganizationRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	if strings.TrimSpace(req.Name) == "" {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "name is required"})
	}
	ctx := requestContext(c)
	org, err := a.Store.CreateOrganization(ctx, req.Name, req.Code, req.Status)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "failed to create organization"})
	}
	a.writeAdminAudit(ctx, claims, "admin.organization.create", "organization", org.ID, fiber.Map{
		"name":   org.Name,
		"code":   org.Code,
		"status": org.Status,
	})
	return c.Status(fiber.StatusCreated).JSON(org)
}

func (a *App) patchOrganization(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if !isSuperAdmin(claims) {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "only super admin can update organizations"})
	}
	id := strings.TrimSpace(c.Params("id"))
	var req patchOrganizationRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	ctx := requestContext(c)
	updated, err := a.Store.UpdateOrganization(ctx, id, req.Name, req.Code, req.Status)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "failed to update organization"})
	}
	if updated == nil {
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "organization not found"})
	}
	a.writeAdminAudit(ctx, claims, "admin.organization.update", "organization", updated.ID, fiber.Map{
		"name":   updated.Name,
		"code":   updated.Code,
		"status": updated.Status,
	})
	return c.JSON(updated)
}

func (a *App) listOrganizationUsers(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	orgID := strings.TrimSpace(c.Params("id"))
	if orgID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "organization id is required"})
	}
	if !isSuperAdmin(claims) && claims.OrgID != orgID {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}
	users, err := a.Store.ListUsersByOrganization(requestContext(c), orgID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to list organization users"})
	}
	for i := range users {
		users[i].PasswordHash = ""
	}
	return c.JSON(users)
}

func (a *App) createOrganizationUser(c *fiber.Ctx) error {
	claims := MustClaims(c)
	orgID := strings.TrimSpace(c.Params("id"))
	if orgID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "organization id is required"})
	}
	if !isSuperAdmin(claims) && claims.OrgID != orgID {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}
	var req createUserRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: err.Error()})
	}
	ctx := requestContext(c)
	created, err := a.Store.CreateUserWithRoles(ctx, orgID, req.Email, hash, req.Status, []string{"user"}, []string{"org_member"})
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "failed to create user"})
	}
	created.PasswordHash = ""
	a.writeAdminAudit(ctx, claims, "admin.user.create", "user", created.ID, fiber.Map{
		"organizationId": created.OrganizationID,
		"email":          created.Email,
		"status":         created.Status,
	})
	return c.Status(fiber.StatusCreated).JSON(created)
}

func (a *App) createOrganizationUsersBulk(c *fiber.Ctx) error {
	claims := MustClaims(c)
	orgID := strings.TrimSpace(c.Params("id"))
	if orgID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "organization id is required"})
	}
	if !isSuperAdmin(claims) && claims.OrgID != orgID {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}
	if a.Mailer == nil || a.Mailer.Ready() != nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "provisioning email unavailable"})
	}

	var req bulkCreateUsersRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	if len(req.Emails) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "at least one email is required"})
	}

	ctx := requestContext(c)
	result := bulkCreateUsersResponse{
		Created: []bulkCreateUsersResponseItem{},
		Failed:  []bulkCreateUsersFailedItem{},
	}
	seenEmails := map[string]struct{}{}

	for _, rawEmail := range req.Emails {
		normalizedEmail, err := normalizeBulkEmail(rawEmail)
		if err != nil {
			if strings.TrimSpace(rawEmail) == "" {
				continue
			}
			result.Failed = append(result.Failed, bulkCreateUsersFailedItem{
				Email: strings.TrimSpace(rawEmail),
				Error: err.Error(),
			})
			continue
		}
		if _, exists := seenEmails[normalizedEmail]; exists {
			result.Failed = append(result.Failed, bulkCreateUsersFailedItem{
				Email: normalizedEmail,
				Error: "duplicate email in request",
			})
			continue
		}
		seenEmails[normalizedEmail] = struct{}{}

		existing, err := a.Store.FindUserByEmail(ctx, normalizedEmail)
		if err != nil {
			result.Failed = append(result.Failed, bulkCreateUsersFailedItem{
				Email: normalizedEmail,
				Error: "failed to check existing user",
			})
			continue
		}
		if existing != nil {
			result.Failed = append(result.Failed, bulkCreateUsersFailedItem{
				Email: normalizedEmail,
				Error: "email already exists",
			})
			continue
		}

		temporaryPassword, err := auth.GenerateTemporaryPassword(24)
		if err != nil {
			result.Failed = append(result.Failed, bulkCreateUsersFailedItem{
				Email: normalizedEmail,
				Error: "failed to generate temporary password",
			})
			continue
		}
		hash, err := auth.HashPassword(temporaryPassword)
		if err != nil {
			result.Failed = append(result.Failed, bulkCreateUsersFailedItem{
				Email: normalizedEmail,
				Error: err.Error(),
			})
			continue
		}

		created, err := a.Store.CreateUserWithRoles(ctx, orgID, normalizedEmail, hash, "active", []string{"user"}, []string{"org_member"})
		if err != nil {
			result.Failed = append(result.Failed, bulkCreateUsersFailedItem{
				Email: normalizedEmail,
				Error: "failed to create user",
			})
			continue
		}

		if err := a.Mailer.SendUserProvisioningEmail(ctx, mailer.UserProvisioningEmail{
			ToEmail:           created.Email,
			Login:             created.Email,
			TemporaryPassword: temporaryPassword,
		}); err != nil {
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

func (a *App) patchUser(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	ctx := requestContext(c)
	userID := strings.TrimSpace(c.Params("id"))
	target, err := a.Store.GetUserByID(ctx, userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if target == nil {
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "user not found"})
	}
	if !isSuperAdmin(claims) && claims.OrgID != target.OrganizationID {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}
	var req patchUserRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	if !isSuperAdmin(claims) {
		req.OrganizationID = nil
	}
	updated, err := a.Store.UpdateUser(ctx, userID, store.UpdateUserInput{
		Email:          req.Email,
		Status:         req.Status,
		OrganizationID: req.OrganizationID,
	})
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "failed to update user"})
	}
	updated.PasswordHash = ""
	if revokeErr := a.Store.RevokeRefreshSessionsByUser(ctx, updated.ID); revokeErr != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to revoke user sessions"})
	}
	a.writeAdminAudit(ctx, claims, "admin.user.update", "user", updated.ID, fiber.Map{
		"organizationId": updated.OrganizationID,
		"email":          updated.Email,
		"status":         updated.Status,
	})
	return c.JSON(updated)
}

func (a *App) updateUserPassword(c *fiber.Ctx) error {
	claims := MustClaims(c)
	ctx := requestContext(c)
	userID := strings.TrimSpace(c.Params("id"))
	target, err := a.Store.GetUserByID(ctx, userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if target == nil {
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "user not found"})
	}
	if !isSuperAdmin(claims) && claims.OrgID != target.OrganizationID {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}
	var req updatePasswordRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: err.Error()})
	}
	if err := a.Store.UpdateUserPassword(ctx, userID, hash); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to update password"})
	}
	if err := a.Store.RevokeRefreshSessionsByUser(ctx, userID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to revoke user sessions"})
	}
	a.writeAdminAudit(ctx, claims, "admin.user.password.update", "user", userID, fiber.Map{
		"organizationId": target.OrganizationID,
	})
	return c.SendStatus(fiber.StatusNoContent)
}

func (a *App) deleteUser(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}

	ctx := requestContext(c)
	userID := strings.TrimSpace(c.Params("id"))
	target, err := a.Store.GetUserByID(ctx, userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if target == nil {
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "user not found"})
	}
	if !isSuperAdmin(claims) && claims.OrgID != target.OrganizationID {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}
	if claims.UserID == target.ID {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "cannot delete your own account"})
	}

	globalRoles, err := a.Store.GetGlobalRoleCodesByUser(ctx, target.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load global roles"})
	}
	orgRoles, err := a.Store.GetOrganizationRoleCodesByUser(ctx, target.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load organization roles"})
	}

	if target.Status == "active" && rbac.HasRole(globalRoles, "super_admin") {
		activeSuperAdmins, err := a.Store.CountActiveUsersByGlobalRole(ctx, "super_admin")
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to verify super admin protections"})
		}
		if activeSuperAdmins <= 1 {
			return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "cannot delete the last active super admin"})
		}
	}

	if target.Status == "active" && rbac.HasRole(orgRoles, "org_admin") {
		activeOrgAdmins, err := a.Store.CountActiveUsersByOrganizationRole(ctx, target.OrganizationID, "org_admin")
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to verify organization admin protections"})
		}
		if activeOrgAdmins <= 1 {
			return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "cannot delete the last active organization admin"})
		}
	}

	deleted, err := a.Store.DeleteUser(ctx, target.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to delete user"})
	}
	if !deleted {
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "user not found"})
	}

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

func (a *App) userActivitySummary(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}

	ctx := requestContext(c)
	userID := strings.TrimSpace(c.Params("id"))
	target, err := a.Store.GetUserByID(ctx, userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if target == nil {
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "user not found"})
	}
	if !isSuperAdmin(claims) && claims.OrgID != target.OrganizationID {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}

	fromDay, toDay, err := resolveActivityRange(c.Query("from"), c.Query("to"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: err.Error()})
	}

	summary, err := a.Store.GetUserActivitySummary(ctx, target.ID, fromDay, toDay)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user activity summary"})
	}
	return c.JSON(summary)
}

func (a *App) deleteUserActivity(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}

	ctx := requestContext(c)
	userID := strings.TrimSpace(c.Params("id"))
	target, err := a.Store.GetUserByID(ctx, userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if target == nil {
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "user not found"})
	}
	if !isSuperAdmin(claims) && claims.OrgID != target.OrganizationID {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}

	deletedCount, err := a.Store.DeleteUserActivity(ctx, target.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to delete user activity"})
	}

	a.writeAdminAudit(ctx, claims, "admin.user.activity.delete", "user", target.ID, fiber.Map{
		"email":          target.Email,
		"organizationId": target.OrganizationID,
		"deletedCount":   deletedCount,
	})
	return c.SendStatus(fiber.StatusNoContent)
}

func (a *App) sendUserPasswordResetEmail(c *fiber.Ctx) error {
	claims := MustClaims(c)
	ctx := requestContext(c)
	userID := strings.TrimSpace(c.Params("id"))
	target, err := a.Store.GetUserByID(ctx, userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if target == nil {
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "user not found"})
	}
	if !isSuperAdmin(claims) && claims.OrgID != target.OrganizationID {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}
	if target.Status != "active" {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "user is inactive"})
	}

	if err := a.sendPasswordResetForUser(ctx, target, auth.SessionTypeApp, claims.UserID); err != nil {
		if errors.Is(err, errPasswordResetUnavailable) {
			return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "password reset email unavailable"})
		}
		if fiberErr, ok := err.(*fiber.Error); ok {
			return c.Status(fiberErr.Code).JSON(ErrorResponse{Error: fiberErr.Message})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to send password reset email"})
	}

	a.writeAdminAudit(ctx, claims, "admin.user.password_reset_email.send", "user", target.ID, fiber.Map{
		"email": target.Email,
	})
	return c.SendStatus(fiber.StatusNoContent)
}

func (a *App) updateUserGlobalRoles(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if !isSuperAdmin(claims) {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "only super admin can update global roles"})
	}
	ctx := requestContext(c)
	userID := strings.TrimSpace(c.Params("id"))
	var req updateRoleCodesRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	if err := a.Store.SetUserGlobalRoles(ctx, userID, req.Codes); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to update global roles"})
	}
	if err := a.Store.RevokeRefreshSessionsByUser(ctx, userID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to revoke user sessions"})
	}
	a.writeAdminAudit(ctx, claims, "admin.user.global_roles.update", "user", userID, fiber.Map{
		"codes": req.Codes,
	})
	return c.SendStatus(fiber.StatusNoContent)
}

func (a *App) updateUserOrgRoles(c *fiber.Ctx) error {
	claims := MustClaims(c)
	ctx := requestContext(c)
	userID := strings.TrimSpace(c.Params("id"))
	target, err := a.Store.GetUserByID(ctx, userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if target == nil {
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "user not found"})
	}
	if !isSuperAdmin(claims) && claims.OrgID != target.OrganizationID {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}
	var req updateRoleCodesRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	if err := a.Store.SetUserOrganizationRoles(ctx, userID, req.Codes); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to update organization roles"})
	}
	if err := a.Store.RevokeRefreshSessionsByUser(ctx, userID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to revoke user sessions"})
	}
	a.writeAdminAudit(ctx, claims, "admin.user.org_roles.update", "user", userID, fiber.Map{
		"codes": req.Codes,
	})
	return c.SendStatus(fiber.StatusNoContent)
}

func (a *App) updateUserEntitlements(c *fiber.Ctx) error {
	claims := MustClaims(c)
	ctx := requestContext(c)
	userID := strings.TrimSpace(c.Params("id"))
	target, err := a.Store.GetUserByID(ctx, userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if target == nil {
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "user not found"})
	}
	if !isSuperAdmin(claims) && claims.OrgID != target.OrganizationID {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}
	var req updateEntitlementsRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	if err := a.Store.SetUserPermissionOverrides(ctx, userID, req.Overrides); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to update entitlements"})
	}
	if err := a.Store.RevokeRefreshSessionsByUser(ctx, userID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to revoke user sessions"})
	}
	a.writeAdminAudit(ctx, claims, "admin.user.entitlements.update", "user", userID, fiber.Map{
		"overrides": req.Overrides,
	})
	return c.SendStatus(fiber.StatusNoContent)
}

func (a *App) getUserAccess(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	ctx := requestContext(c)
	userID := strings.TrimSpace(c.Params("id"))
	target, err := a.Store.GetUserByID(ctx, userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if target == nil {
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "user not found"})
	}
	if !isSuperAdmin(claims) && claims.OrgID != target.OrganizationID {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}
	globalRoles, err := a.Store.GetGlobalRoleCodesByUser(ctx, userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load global roles"})
	}
	orgRoles, err := a.Store.GetOrganizationRoleCodesByUser(ctx, userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load organization roles"})
	}
	overrides, err := a.Store.GetUserPermissionOverrides(ctx, userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load permission overrides"})
	}
	effectivePermissions, err := a.Store.ResolveEffectivePermissions(ctx, userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to resolve effective permissions"})
	}
	target.PasswordHash = ""
	return c.JSON(fiber.Map{
		"user":                 target,
		"globalRoles":          globalRoles,
		"orgRoles":             orgRoles,
		"overrides":            overrides,
		"effectivePermissions": effectivePermissions,
	})
}

func (a *App) catalogRoles(c *fiber.Ctx) error {
	ctx := requestContext(c)
	globalRoles, err := a.Store.ListGlobalRolesCatalog(ctx)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to list global roles"})
	}
	organizationRoles, err := a.Store.ListOrganizationRolesCatalog(ctx)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to list organization roles"})
	}
	return c.JSON(fiber.Map{
		"global":       globalRoles,
		"organization": organizationRoles,
	})
}

func (a *App) catalogPermissions(c *fiber.Ctx) error {
	permissions, err := a.Store.ListPermissionsCatalog(requestContext(c))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to list permissions"})
	}
	return c.JSON(permissions)
}

func isSuperAdmin(claims *auth.Claims) bool {
	if claims == nil {
		return false
	}
	return rbac.HasRole(claims.GlobalRoles, "super_admin")
}

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
