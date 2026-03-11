package api

import (
	"context"
	"encoding/json"
	"strings"

	"demeter-backend/internal/auth"
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
	group := router.Group("/admin", a.AdminAuthRequired(), RequirePermissions("feature.admin"), RequireAdminScope(), RequireAdminCSRF())
	group.Get("/organizations", a.listOrganizations)
	group.Post("/organizations", a.createOrganization)
	group.Patch("/organizations/:id", a.patchOrganization)
	a.registerAdminActivityRoutes(group)
	group.Get("/organizations/:id/users", a.listOrganizationUsers)
	group.Get("/users/:id/access", a.getUserAccess)
	group.Post("/organizations/:id/users", a.createOrganizationUser)
	group.Patch("/users/:id", a.patchUser)
	group.Put("/users/:id/password", a.updateUserPassword)
	group.Put("/users/:id/global-roles", a.updateUserGlobalRoles)
	group.Put("/users/:id/org-roles", a.updateUserOrgRoles)
	group.Put("/users/:id/entitlements", a.updateUserEntitlements)
	group.Get("/catalog/roles", a.catalogRoles)
	group.Get("/catalog/permissions", a.catalogPermissions)
}

func (a *App) listOrganizations(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	ctx := context.Background()
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
	org, err := a.Store.CreateOrganization(context.Background(), req.Name, req.Code, req.Status)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "failed to create organization"})
	}
	a.writeAdminAudit(claims, "admin.organization.create", "organization", org.ID, fiber.Map{
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
	updated, err := a.Store.UpdateOrganization(context.Background(), id, req.Name, req.Code, req.Status)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "failed to update organization"})
	}
	if updated == nil {
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "organization not found"})
	}
	a.writeAdminAudit(claims, "admin.organization.update", "organization", updated.ID, fiber.Map{
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
	users, err := a.Store.ListUsersByOrganization(context.Background(), orgID)
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
	created, err := a.Store.CreateUser(context.Background(), orgID, req.Email, hash, req.Status)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "failed to create user"})
	}
	_ = a.Store.SetUserGlobalRoles(context.Background(), created.ID, []string{"user"})
	_ = a.Store.SetUserOrganizationRoles(context.Background(), created.ID, []string{"org_member"})
	created.PasswordHash = ""
	a.writeAdminAudit(claims, "admin.user.create", "user", created.ID, fiber.Map{
		"organizationId": created.OrganizationID,
		"email":          created.Email,
		"status":         created.Status,
	})
	return c.Status(fiber.StatusCreated).JSON(created)
}

func (a *App) patchUser(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	userID := strings.TrimSpace(c.Params("id"))
	target, err := a.Store.GetUserByID(context.Background(), userID)
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
	updated, err := a.Store.UpdateUser(context.Background(), userID, store.UpdateUserInput{
		Email:          req.Email,
		Status:         req.Status,
		OrganizationID: req.OrganizationID,
	})
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "failed to update user"})
	}
	updated.PasswordHash = ""
	if revokeErr := a.Store.RevokeRefreshSessionsByUser(context.Background(), updated.ID); revokeErr != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to revoke user sessions"})
	}
	a.writeAdminAudit(claims, "admin.user.update", "user", updated.ID, fiber.Map{
		"organizationId": updated.OrganizationID,
		"email":          updated.Email,
		"status":         updated.Status,
	})
	return c.JSON(updated)
}

func (a *App) updateUserPassword(c *fiber.Ctx) error {
	claims := MustClaims(c)
	userID := strings.TrimSpace(c.Params("id"))
	target, err := a.Store.GetUserByID(context.Background(), userID)
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
	if err := a.Store.UpdateUserPassword(context.Background(), userID, hash); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to update password"})
	}
	if err := a.Store.RevokeRefreshSessionsByUser(context.Background(), userID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to revoke user sessions"})
	}
	a.writeAdminAudit(claims, "admin.user.password.update", "user", userID, fiber.Map{
		"organizationId": target.OrganizationID,
	})
	return c.SendStatus(fiber.StatusNoContent)
}

func (a *App) updateUserGlobalRoles(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if !isSuperAdmin(claims) {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "only super admin can update global roles"})
	}
	userID := strings.TrimSpace(c.Params("id"))
	var req updateRoleCodesRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	if err := a.Store.SetUserGlobalRoles(context.Background(), userID, req.Codes); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to update global roles"})
	}
	if err := a.Store.RevokeRefreshSessionsByUser(context.Background(), userID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to revoke user sessions"})
	}
	a.writeAdminAudit(claims, "admin.user.global_roles.update", "user", userID, fiber.Map{
		"codes": req.Codes,
	})
	return c.SendStatus(fiber.StatusNoContent)
}

func (a *App) updateUserOrgRoles(c *fiber.Ctx) error {
	claims := MustClaims(c)
	userID := strings.TrimSpace(c.Params("id"))
	target, err := a.Store.GetUserByID(context.Background(), userID)
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
	if err := a.Store.SetUserOrganizationRoles(context.Background(), userID, req.Codes); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to update organization roles"})
	}
	if err := a.Store.RevokeRefreshSessionsByUser(context.Background(), userID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to revoke user sessions"})
	}
	a.writeAdminAudit(claims, "admin.user.org_roles.update", "user", userID, fiber.Map{
		"codes": req.Codes,
	})
	return c.SendStatus(fiber.StatusNoContent)
}

func (a *App) updateUserEntitlements(c *fiber.Ctx) error {
	claims := MustClaims(c)
	userID := strings.TrimSpace(c.Params("id"))
	target, err := a.Store.GetUserByID(context.Background(), userID)
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
	if err := a.Store.SetUserPermissionOverrides(context.Background(), userID, req.Overrides); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to update entitlements"})
	}
	if err := a.Store.RevokeRefreshSessionsByUser(context.Background(), userID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to revoke user sessions"})
	}
	a.writeAdminAudit(claims, "admin.user.entitlements.update", "user", userID, fiber.Map{
		"overrides": req.Overrides,
	})
	return c.SendStatus(fiber.StatusNoContent)
}

func (a *App) getUserAccess(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	userID := strings.TrimSpace(c.Params("id"))
	target, err := a.Store.GetUserByID(context.Background(), userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if target == nil {
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "user not found"})
	}
	if !isSuperAdmin(claims) && claims.OrgID != target.OrganizationID {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}
	globalRoles, err := a.Store.GetGlobalRoleCodesByUser(context.Background(), userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load global roles"})
	}
	orgRoles, err := a.Store.GetOrganizationRoleCodesByUser(context.Background(), userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load organization roles"})
	}
	overrides, err := a.Store.GetUserPermissionOverrides(context.Background(), userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load permission overrides"})
	}
	effectivePermissions, err := a.Store.ResolveEffectivePermissions(context.Background(), userID)
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
	globalRoles, err := a.Store.ListGlobalRolesCatalog(context.Background())
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to list global roles"})
	}
	organizationRoles, err := a.Store.ListOrganizationRolesCatalog(context.Background())
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to list organization roles"})
	}
	return c.JSON(fiber.Map{
		"global":       globalRoles,
		"organization": organizationRoles,
	})
}

func (a *App) catalogPermissions(c *fiber.Ctx) error {
	permissions, err := a.Store.ListPermissionsCatalog(context.Background())
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

func (a *App) writeAdminAudit(claims *auth.Claims, action, targetType, targetID string, payload any) {
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
	_ = a.Store.InsertAuditLog(context.Background(), store.AuditLogInput{
		ActorUserID:    claims.UserID,
		OrganizationID: claims.OrgID,
		Action:         action,
		TargetType:     targetType,
		TargetID:       targetID,
		PayloadJSON:    safePayload,
	})
}
