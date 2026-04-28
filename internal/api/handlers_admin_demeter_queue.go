package api

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
)

type demeterAudioQueueSettingsUpdateRequest struct {
	Parallelism *int `json:"parallelism"`
	Settings    *struct {
		Parallelism *int `json:"parallelism"`
	} `json:"settings"`
}

func (r demeterAudioQueueSettingsUpdateRequest) resolvedParallelism() (int, bool) {
	if r.Parallelism != nil {
		return *r.Parallelism, true
	}
	if r.Settings != nil && r.Settings.Parallelism != nil {
		return *r.Settings.Parallelism, true
	}
	return 0, false
}

// registerAdminDemeterQueueRoutes installs the super-admin queue snapshot and
// configuration routes for the Demeter provider.
func (a *App) registerAdminDemeterQueueRoutes(group fiber.Router) {
	group.Get("/providers/demeter-sante/queue", RequireSuperAdminScope(), a.getAdminDemeterQueue)
	group.Put("/providers/demeter-sante/queue/settings", RequireSuperAdminScope(), a.putAdminDemeterQueueSettings)
	group.Delete("/providers/demeter-sante/queue", RequireSuperAdminScope(), a.deleteAdminDemeterQueueOperations)
}

func (a *App) getAdminDemeterQueue(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "demeter_audio_queue", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "admin", route, "request_unauthorized", "demeter_audio_queue", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	if !isSuperAdmin(claims) {
		logAPIStep(c, "admin", route, "request_forbidden", "demeter_audio_queue", nil)
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden"})
	}

	limit := 200
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			logAPIStep(c, "admin", route, "request_validation_error", "demeter_audio_queue", map[string]any{
				"reason": "invalid_limit",
				"limit":  raw,
			})
			return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid limit"})
		}
		if parsed > 500 {
			parsed = 500
		}
		limit = parsed
	}

	manager := a.EnsureDemeterQueueManager()
	snapshot, err := manager.Snapshot(requestContext(c), limit)
	if err != nil {
		logAPIStep(c, "admin", route, "load_error", "demeter_audio_queue", map[string]any{"error": err})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load demeter queue"})
	}

	logAPIStep(c, "admin", route, "response_ready", "demeter_audio_queue", map[string]any{
		"parallelism": snapshot.Settings.Parallelism,
		"workers":     len(snapshot.Workers),
		"operations":  len(snapshot.Operations),
	})
	return c.JSON(snapshot)
}

func (a *App) putAdminDemeterQueueSettings(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "demeter_audio_queue_settings", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "admin", route, "request_unauthorized", "demeter_audio_queue_settings", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	if !isSuperAdmin(claims) {
		logAPIStep(c, "admin", route, "request_forbidden", "demeter_audio_queue_settings", nil)
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden"})
	}

	var req demeterAudioQueueSettingsUpdateRequest
	if err := c.BodyParser(&req); err != nil {
		logAPIStep(c, "admin", route, "request_parse_error", "demeter_audio_queue_settings", map[string]any{"error": err})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	parallelism, ok := req.resolvedParallelism()
	if !ok {
		logAPIStep(c, "admin", route, "request_validation_error", "demeter_audio_queue_settings", map[string]any{
			"reason": "missing_parallelism",
		})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "parallelism is required"})
	}

	manager := a.EnsureDemeterQueueManager()
	if err := manager.Resize(requestContext(c), route, parallelism); err != nil {
		logAPIStep(c, "admin", route, "update_error", "demeter_audio_queue_settings", map[string]any{
			"error":       err,
			"parallelism": parallelism,
		})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to update demeter queue settings"})
	}

	snapshot, err := manager.Snapshot(requestContext(c), 200)
	if err != nil {
		logAPIStep(c, "admin", route, "load_error", "demeter_audio_queue_settings", map[string]any{"error": err})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load demeter queue"})
	}

	logAPIStep(c, "admin", route, "response_ready", "demeter_audio_queue_settings", map[string]any{
		"parallelism": snapshot.Settings.Parallelism,
		"workers":     len(snapshot.Workers),
		"operations":  len(snapshot.Operations),
	})
	return c.JSON(snapshot)
}

func (a *App) deleteAdminDemeterQueueOperations(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "purge_demeter_audio_queue", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "admin", route, "request_unauthorized", "purge_demeter_audio_queue", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	if !isSuperAdmin(claims) {
		logAPIStep(c, "admin", route, "request_forbidden", "purge_demeter_audio_queue", nil)
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden"})
	}

	scope, err := parseDemeterQueuePurgeScope(c.Query("scope"))
	if err != nil {
		logAPIStep(c, "admin", route, "request_validation_error", "purge_demeter_audio_queue", map[string]any{
			"reason": "invalid_scope",
			"scope":  strings.TrimSpace(c.Query("scope")),
		})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid scope"})
	}

	ctx := requestContext(c)
	manager := a.EnsureDemeterQueueManager()
	logFields := map[string]any{"scope": string(scope)}

	switch scope {
	case demeterQueuePurgeScopeAll:
		logAPIStep(c, "admin", route, "delete_start", "purge_demeter_audio_queue", logFields)
		deletedCount, err := a.Store.PurgeAllDemeterAudioTranscriptionOperations(ctx)
		if err != nil {
			logAPIStep(c, "admin", route, "delete_error", "purge_demeter_audio_queue", map[string]any{"error": err, "scope": string(scope)})
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to purge demeter queue"})
		}
		a.writeAdminAudit(ctx, claims, "admin.demeter_audio_queue.purge", "demeter_audio_queue", "", fiber.Map{
			"scope":        string(scope),
			"deletedCount": deletedCount,
		})
		manager.notifySnapshotChanged()
		logAPIStep(c, "admin", route, "response_ready", "purge_demeter_audio_queue", map[string]any{
			"scope":         string(scope),
			"deleted_count": deletedCount,
		})
		return c.SendStatus(fiber.StatusNoContent)
	default:
		logAPIStep(c, "admin", route, "delete_start", "purge_demeter_audio_queue", logFields)
		deletedCount, err := a.Store.PurgeCompletedDemeterAudioTranscriptionOperations(ctx)
		if err != nil {
			logAPIStep(c, "admin", route, "delete_error", "purge_demeter_audio_queue", map[string]any{"error": err, "scope": string(scope)})
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to purge demeter queue"})
		}
		a.writeAdminAudit(ctx, claims, "admin.demeter_audio_queue.purge", "demeter_audio_queue", "", fiber.Map{
			"scope":        string(scope),
			"deletedCount": deletedCount,
		})
		manager.notifySnapshotChanged()
		logAPIStep(c, "admin", route, "response_ready", "purge_demeter_audio_queue", map[string]any{
			"scope":         string(scope),
			"deleted_count": deletedCount,
		})
		return c.SendStatus(fiber.StatusNoContent)
	}
}

// demeterAudioQueueSnapshotResponse ensures the compiler keeps the JSON shape
// stable in tests that only need a typed reference.
func demeterAudioQueueSnapshotResponse() string {
	raw, _ := json.Marshal(demeterAudioQueueSnapshot{})
	return string(raw)
}
