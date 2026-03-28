package api

import (
	"encoding/json"
	"strings"

	"github.com/gofiber/fiber/v2"
)

type putSettingsRequest struct {
	SchemaVersion int             `json:"schemaVersion"`
	Settings      json.RawMessage `json:"settings"`
}

var removedLegacySettingsKeys = map[string]struct{}{
	"cloudApiUrl":        {},
	"cloudContextPreset": {},
}

func (a *App) RegisterSettingsRoutes(router fiber.Router) {
	router.Get("/settings", a.AppAuthRequired(), RequirePermissions("feature.settings"), a.getSettings)
	router.Put("/settings", a.AppAuthRequired(), RequirePermissions("feature.settings"), a.putSettings)
	router.Post("/settings/reset", a.AppAuthRequired(), RequirePermissions("feature.settings"), a.resetSettings)
}

func (a *App) getSettings(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	record, err := a.Store.GetUserSettings(requestContext(c), claims.UserID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load settings"})
	}
	return c.JSON(toSettingsEnvelope(record))
}

func (a *App) putSettings(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	var req putSettingsRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	payload := strings.TrimSpace(string(req.Settings))
	if payload == "" {
		payload = "{}"
	}
	if !json.Valid([]byte(payload)) {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "settings must be valid JSON"})
	}
	sanitizedPayload, err := sanitizeSettingsPayload(json.RawMessage(payload))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "settings must be valid JSON"})
	}
	record, err := a.Store.SaveUserSettings(requestContext(c), claims.UserID, claims.OrgID, sanitizedPayload, req.SchemaVersion)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to save settings"})
	}
	return c.JSON(toSettingsEnvelope(record))
}

func (a *App) resetSettings(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	record, err := a.Store.ResetUserSettings(requestContext(c), claims.UserID, claims.OrgID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to reset settings"})
	}
	return c.JSON(toSettingsEnvelope(record))
}

func sanitizeSettingsPayload(payload json.RawMessage) (json.RawMessage, error) {
	if len(strings.TrimSpace(string(payload))) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if !json.Valid(payload) {
		return nil, fiber.ErrBadRequest
	}

	trimmed := strings.TrimSpace(string(payload))
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return payload, nil
	}

	var settings map[string]json.RawMessage
	if err := json.Unmarshal(payload, &settings); err != nil {
		return nil, err
	}
	for key := range removedLegacySettingsKeys {
		delete(settings, key)
	}
	sanitized, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(sanitized), nil
}
