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
	router.Get("/settings", a.AppAuthRequired(), a.getSettings)
	router.Put("/settings", a.AppAuthRequired(), a.putSettings)
	router.Post("/settings/reset", a.AppAuthRequired(), a.resetSettings)
}

func (a *App) getSettings(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "settings", route, "request_received", "", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "settings", route, "request_unauthorized", "", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	logAPIStep(c, "settings", route, "load_start", "", nil)
	record, err := a.Store.GetUserSettings(requestContext(c), claims.UserID)
	if err != nil {
		logAPIStep(c, "settings", route, "load_error", "", map[string]any{"error": err})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load settings"})
	}
	fields := map[string]any{"schema_version": 0}
	if record != nil {
		fields["schema_version"] = record.SchemaVersion
		fields["settings_present"] = len(record.Settings) > 0
	}
	logAPIStep(c, "settings", route, "response_ready", "", fields)
	return c.JSON(toSettingsEnvelope(record))
}

func (a *App) putSettings(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "settings", route, "request_received", "", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "settings", route, "request_unauthorized", "", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	var req putSettingsRequest
	if err := c.BodyParser(&req); err != nil {
		logAPIStep(c, "settings", route, "request_parse_error", "", map[string]any{"error": err})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	payload := strings.TrimSpace(string(req.Settings))
	if payload == "" {
		payload = "{}"
	}
	if !json.Valid([]byte(payload)) {
		logAPIStep(c, "settings", route, "request_validation_error", "", map[string]any{"reason": "invalid_json"})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "settings must be valid JSON"})
	}
	logAPIStep(c, "settings", route, "sanitize_start", "", map[string]any{
		"schema_version": req.SchemaVersion,
		"payload_bytes":  len(payload),
	})
	sanitizedPayload, err := sanitizeSettingsPayload(json.RawMessage(payload))
	if err != nil {
		logAPIStep(c, "settings", route, "sanitize_error", "", map[string]any{"error": err})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "settings must be valid JSON"})
	}
	logAPIStep(c, "settings", route, "save_start", "", map[string]any{
		"schema_version": req.SchemaVersion,
		"payload_bytes":  len(sanitizedPayload),
	})
	record, err := a.Store.SaveUserSettings(requestContext(c), claims.UserID, claims.OrgID, sanitizedPayload, req.SchemaVersion)
	if err != nil {
		logAPIStep(c, "settings", route, "save_error", "", map[string]any{"error": err})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to save settings"})
	}
	logAPIStep(c, "settings", route, "response_ready", "", map[string]any{"schema_version": record.SchemaVersion})
	return c.JSON(toSettingsEnvelope(record))
}

func (a *App) resetSettings(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "settings", route, "request_received", "", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "settings", route, "request_unauthorized", "", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	logAPIStep(c, "settings", route, "reset_start", "", nil)
	record, err := a.Store.ResetUserSettings(requestContext(c), claims.UserID, claims.OrgID)
	if err != nil {
		logAPIStep(c, "settings", route, "reset_error", "", map[string]any{"error": err})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to reset settings"})
	}
	logAPIStep(c, "settings", route, "response_ready", "", map[string]any{"schema_version": record.SchemaVersion})
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
