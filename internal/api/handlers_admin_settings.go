package api

import (
	"encoding/json"
	"strings"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/store"
	"github.com/gofiber/fiber/v2"
)

// adminUserSettingsRequest carries the raw JSON settings document for an admin
// scoped user settings update.
type adminUserSettingsRequest struct {
	SchemaVersion int             `json:"schemaVersion"`
	Settings      json.RawMessage `json:"settings"`
}

// registerAdminUserSettingsRoutes installs the admin routes for inspecting and
// editing a user's settings document.
func (a *App) registerAdminUserSettingsRoutes(group fiber.Router) {
	group.Get("/users/:id/settings", a.getAdminUserSettings)
	group.Put("/users/:id/settings", a.putAdminUserSettings)
	group.Post("/users/:id/settings/reset", a.resetAdminUserSettings)
}

// getAdminUserSettings reuses the shared loader for the admin GET endpoint.
func (a *App) getAdminUserSettings(c *fiber.Ctx) error {
	return a.loadAdminScopedUserSettings(c)
}

// putAdminUserSettings saves the settings document for the selected user.
func (a *App) putAdminUserSettings(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "put_user_settings", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "admin", route, "request_unauthorized", "put_user_settings", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}

	target, appErr := a.loadAdminSettingsTargetUser(c, claims, "put_user_settings")
	if appErr != nil {
		return c.Status(appErr.Code).JSON(ErrorResponse{Error: appErr.Message})
	}
	var req adminUserSettingsRequest
	if err := c.BodyParser(&req); err != nil {
		logAPIStep(c, "admin", route, "request_parse_error", "put_user_settings", map[string]any{"error": err, "user_id": target.ID})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}

	payload := strings.TrimSpace(string(req.Settings))
	if payload == "" {
		payload = "{}"
	}
	if !json.Valid([]byte(payload)) {
		logAPIStep(c, "admin", route, "request_validation_error", "put_user_settings", map[string]any{"reason": "invalid_json", "user_id": target.ID})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "settings must be valid JSON"})
	}

	sanitizedPayload, err := sanitizeSettingsPayload(json.RawMessage(payload))
	if err != nil {
		logAPIStep(c, "admin", route, "sanitize_error", "put_user_settings", map[string]any{"error": err, "user_id": target.ID})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "settings must be valid JSON"})
	}

	logAPIStep(c, "admin", route, "save_start", "put_user_settings", map[string]any{
		"user_id":         target.ID,
		"organization_id": target.OrganizationID,
		"schema_version":  req.SchemaVersion,
		"payload_bytes":   len(sanitizedPayload),
	})
	record, err := a.Store.SaveUserSettings(requestContext(c), target.ID, target.OrganizationID, sanitizedPayload, req.SchemaVersion)
	if err != nil {
		logAPIStep(c, "admin", route, "save_error", "put_user_settings", map[string]any{"error": err, "user_id": target.ID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to save settings"})
	}

	a.writeAdminAudit(requestContext(c), claims, "admin.user.settings.update", "user", target.ID, fiber.Map{
		"schemaVersion": record.SchemaVersion,
	})
	logAPIStep(c, "admin", route, "response_ready", "put_user_settings", map[string]any{"user_id": target.ID, "schema_version": record.SchemaVersion})
	return c.JSON(toSettingsEnvelope(record))
}

// resetAdminUserSettings clears the selected user's settings document.
func (a *App) resetAdminUserSettings(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "reset_user_settings", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "admin", route, "request_unauthorized", "reset_user_settings", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}

	target, appErr := a.loadAdminSettingsTargetUser(c, claims, "reset_user_settings")
	if appErr != nil {
		return c.Status(appErr.Code).JSON(ErrorResponse{Error: appErr.Message})
	}

	logAPIStep(c, "admin", route, "reset_start", "reset_user_settings", map[string]any{"user_id": target.ID, "organization_id": target.OrganizationID})
	record, err := a.Store.ResetUserSettings(requestContext(c), target.ID, target.OrganizationID)
	if err != nil {
		logAPIStep(c, "admin", route, "reset_error", "reset_user_settings", map[string]any{"error": err, "user_id": target.ID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to reset settings"})
	}

	a.writeAdminAudit(requestContext(c), claims, "admin.user.settings.reset", "user", target.ID, nil)
	logAPIStep(c, "admin", route, "response_ready", "reset_user_settings", map[string]any{"user_id": target.ID, "schema_version": record.SchemaVersion})
	return c.JSON(toSettingsEnvelope(record))
}

// loadAdminScopedUserSettings returns the current settings document for the
// selected user while enforcing organization scope.
func (a *App) loadAdminScopedUserSettings(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "get_user_settings", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "admin", route, "request_unauthorized", "get_user_settings", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}

	target, appErr := a.loadAdminSettingsTargetUser(c, claims, "get_user_settings")
	if appErr != nil {
		return c.Status(appErr.Code).JSON(ErrorResponse{Error: appErr.Message})
	}

	record, err := a.Store.GetUserSettings(requestContext(c), target.ID)
	if err != nil {
		logAPIStep(c, "admin", route, "load_error", "get_user_settings", map[string]any{"error": err, "user_id": target.ID})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load settings"})
	}

	logAPIStep(c, "admin", route, "response_ready", "get_user_settings", map[string]any{
		"user_id": target.ID,
		"schema_version": func() int {
			if record == nil {
				return 0
			}
			return record.SchemaVersion
		}(),
		"settings_present": func() bool {
			if record == nil {
				return false
			}
			return len(record.Settings) > 0
		}(),
	})
	return c.JSON(toSettingsEnvelope(record))
}

// loadAdminSettingsTargetUser resolves the target user and validates that the
// caller is allowed to touch it.
func (a *App) loadAdminSettingsTargetUser(c *fiber.Ctx, claims *auth.Claims, step string) (*store.User, *fiber.Error) {
	route := requestRoutePath(c)
	userID := strings.TrimSpace(c.Params("id"))
	logAPIStep(c, "admin", route, "load_start", step, map[string]any{"user_id": userID})
	target, err := a.Store.GetUserByID(requestContext(c), userID)
	if err != nil {
		logAPIStep(c, "admin", route, "load_error", step, map[string]any{"error": err, "user_id": userID})
		return nil, fiber.NewError(fiber.StatusInternalServerError, "failed to load user")
	}
	if target == nil {
		logAPIStep(c, "admin", route, "load_missing", step, map[string]any{"user_id": userID})
		return nil, fiber.NewError(fiber.StatusNotFound, "user not found")
	}
	if !isSuperAdmin(claims) && claims.OrgID != target.OrganizationID {
		logAPIStep(c, "admin", route, "request_forbidden", step, map[string]any{"user_id": userID, "organization_id": target.OrganizationID})
		return nil, fiber.NewError(fiber.StatusForbidden, "forbidden organization scope")
	}
	return target, nil
}
