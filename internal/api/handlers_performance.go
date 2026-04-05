package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"demeter-backend/internal/backendperformance"
	"demeter-backend/internal/store"
	"github.com/gofiber/fiber/v2"
)

type performanceEventsRequest struct {
	Events []performanceEventPayload `json:"events"`
}

type performanceEventPayload struct {
	EventID    string          `json:"eventId"`
	TraceID    string          `json:"traceId"`
	Surface    string          `json:"surface"`
	Component  string          `json:"component"`
	Task       string          `json:"task"`
	Status     string          `json:"status"`
	DurationMS int64           `json:"durationMs"`
	Route      string          `json:"route"`
	OccurredAt string          `json:"occurredAt"`
	Meta       json.RawMessage `json:"meta"`
}

type performanceRejectedEvent struct {
	EventID string `json:"eventId"`
	Reason  string `json:"reason"`
}

type performanceEventsResponse struct {
	Accepted   int                        `json:"accepted"`
	Duplicates int                        `json:"duplicates"`
	Rejected   []performanceRejectedEvent `json:"rejected"`
}

func (a *App) RegisterPerformanceRoutes(router fiber.Router) {
	router.Post("/performance/events", a.AppAuthRequired(), a.postPerformanceEvents)
}

func (a *App) registerAdminPerformanceRoutes(group fiber.Router) {
	group.Get("/performance/summary", RequireSuperAdminScope(), a.adminPerformanceSummary)
	group.Delete("/performance", RequireSuperAdminScope(), a.deletePerformanceEvents)
}

func (a *App) postPerformanceEvents(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "performance", route, "request_received", "ingest_performance_events", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "performance", route, "request_unauthorized", "ingest_performance_events", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}

	var req performanceEventsRequest
	if err := c.BodyParser(&req); err != nil {
		logAPIStep(c, "performance", route, "request_parse_error", "ingest_performance_events", map[string]any{"error": err})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	if len(req.Events) == 0 {
		logAPIStep(c, "performance", route, "request_validation_error", "ingest_performance_events", map[string]any{"reason": "missing_events"})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "events are required"})
	}

	logAPIStep(c, "performance", route, "validation_start", "ingest_performance_events", map[string]any{"event_count": len(req.Events)})
	validInputs := make([]backendperformance.Event, 0, len(req.Events))
	rejected := make([]performanceRejectedEvent, 0)
	for _, raw := range req.Events {
		input, reason := validatePerformanceEvent(raw, requestTraceID(c))
		if reason != "" {
			rejected = append(rejected, performanceRejectedEvent{
				EventID: strings.TrimSpace(raw.EventID),
				Reason:  reason,
			})
			continue
		}
		validInputs = append(validInputs, input)
	}

	result := store.PerformanceIngestResult{}
	if len(validInputs) > 0 {
		logAPIStep(c, "performance", route, "ingest_start", "ingest_performance_events", map[string]any{
			"valid_count":    len(validInputs),
			"rejected_count": len(rejected),
		})
		var err error
		result, err = a.Store.IngestPerformanceEvents(requestContext(c), claims.OrgID, claims.UserID, validInputs)
		if err != nil {
			logAPIStep(c, "performance", route, "ingest_error", "ingest_performance_events", map[string]any{"error": err})
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to ingest performance events"})
		}
	}

	logAPIStep(c, "performance", route, "response_ready", "ingest_performance_events", map[string]any{
		"accepted":   result.Accepted,
		"duplicates": result.Duplicates,
		"rejected":   len(rejected),
	})
	return c.JSON(performanceEventsResponse{
		Accepted:   result.Accepted,
		Duplicates: result.Duplicates,
		Rejected:   rejected,
	})
}

func (a *App) adminPerformanceSummary(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "performance_summary", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "admin", route, "request_unauthorized", "performance_summary", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}

	from, to, err := resolvePerformanceWindow(c.Query("from"), c.Query("to"))
	if err != nil {
		logAPIStep(c, "admin", route, "range_error", "performance_summary", map[string]any{"error": err})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: err.Error()})
	}

	organizationID := strings.TrimSpace(c.Query("organizationId"))
	userID := strings.TrimSpace(c.Query("userId"))
	task := strings.TrimSpace(c.Query("task"))
	if userID != "" && organizationID == "" {
		logAPIStep(c, "admin", route, "range_error", "performance_summary", map[string]any{
			"reason":  "user_id_requires_organization_id",
			"user_id": userID,
		})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "userId requires organizationId"})
	}
	if organizationID != "" {
		logAPIStep(c, "admin", route, "load_start", "performance_summary", map[string]any{
			"organization_id": organizationID,
			"user_id":         userID,
			"from":            from,
			"to":              to,
			"task":            task,
		})
		org, err := a.Store.GetOrganizationByID(requestContext(c), organizationID)
		if err != nil {
			logAPIStep(c, "admin", route, "load_error", "performance_summary", map[string]any{"error": err})
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load organization"})
		}
		if org == nil {
			logAPIStep(c, "admin", route, "load_missing", "performance_summary", map[string]any{"organization_id": organizationID})
			return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "organization not found"})
		}
	} else {
		logAPIStep(c, "admin", route, "load_start", "performance_summary", map[string]any{
			"scope":   "global",
			"user_id": userID,
			"from":    from,
			"to":      to,
			"task":    task,
		})
	}

	if userID != "" {
		user, err := a.Store.GetUserByID(requestContext(c), userID)
		if err != nil {
			logAPIStep(c, "admin", route, "load_error", "performance_summary", map[string]any{"error": err})
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
		}
		if user == nil || user.OrganizationID != organizationID {
			logAPIStep(c, "admin", route, "load_missing", "performance_summary", map[string]any{
				"organization_id": organizationID,
				"user_id":         userID,
			})
			return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "user not found"})
		}
	}

	summary, err := a.Store.GetPerformanceSummary(requestContext(c), store.PerformanceSummaryFilters{
		OrganizationID: organizationID,
		UserID:         userID,
		From:           from,
		To:             to,
		Task:           task,
		TopLimit:       10,
		RecentLimit:    20,
	})
	if err != nil {
		logAPIStep(c, "admin", route, "load_error", "performance_summary", map[string]any{"error": err})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load performance summary"})
	}

	logAPIStep(c, "admin", route, "response_ready", "performance_summary", map[string]any{
		"organization_id": organizationID,
		"user_id":         userID,
		"task":            task,
		"total":           summary.Totals.Events,
		"task_options":    len(summary.TaskOptions),
		"top_tasks":       len(summary.TopTasks),
		"recent_events":   len(summary.RecentEvents),
	})
	return c.JSON(summary)
}

func (a *App) deletePerformanceEvents(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "purge_performance", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "admin", route, "request_unauthorized", "purge_performance", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}

	from, to, err := resolvePerformanceWindow(c.Query("from"), c.Query("to"))
	if err != nil {
		logAPIStep(c, "admin", route, "range_error", "purge_performance", map[string]any{"error": err})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: err.Error()})
	}

	filters := store.PerformanceSummaryFilters{
		OrganizationID: strings.TrimSpace(c.Query("organizationId")),
		UserID:         strings.TrimSpace(c.Query("userId")),
		From:           from,
		To:             to,
		Task:           strings.TrimSpace(c.Query("task")),
	}

	logAPIStep(c, "admin", route, "delete_start", "purge_performance", map[string]any{
		"organization_id": filters.OrganizationID,
		"user_id":         filters.UserID,
		"from":            filters.From,
		"to":              filters.To,
		"task":            filters.Task,
	})
	deletedCount, err := a.Store.DeletePerformanceEvents(requestContext(c), filters)
	if err != nil {
		logAPIStep(c, "admin", route, "delete_error", "purge_performance", map[string]any{"error": err})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to purge performance events"})
	}

	a.writeAdminAudit(requestContext(c), claims, "admin.performance.purge", "performance_events", "", fiber.Map{
		"organizationId": filters.OrganizationID,
		"userId":         filters.UserID,
		"from":           filters.From,
		"to":             filters.To,
		"task":           filters.Task,
		"deletedCount":   deletedCount,
	})

	logAPIStep(c, "admin", route, "response_ready", "purge_performance", map[string]any{
		"organization_id": filters.OrganizationID,
		"deleted_count":   deletedCount,
	})
	return c.SendStatus(fiber.StatusNoContent)
}

func validatePerformanceEvent(raw performanceEventPayload, fallbackTraceID string) (backendperformance.Event, string) {
	eventID := strings.TrimSpace(raw.EventID)
	if eventID == "" {
		return backendperformance.Event{}, "missing_event_id"
	}

	surface := normalizePerformanceToken(raw.Surface)
	if surface == "" {
		return backendperformance.Event{}, "missing_surface"
	}
	if surface != "frontend" {
		return backendperformance.Event{}, "invalid_surface"
	}

	component := normalizePerformanceToken(raw.Component)
	if component == "" {
		return backendperformance.Event{}, "missing_component"
	}

	task := normalizePerformanceToken(raw.Task)
	if task == "" {
		return backendperformance.Event{}, "missing_task"
	}

	if raw.DurationMS < 0 {
		return backendperformance.Event{}, "invalid_duration"
	}

	occurredAt := time.Now().UTC()
	if trimmed := strings.TrimSpace(raw.OccurredAt); trimmed != "" {
		parsed, err := time.Parse(time.RFC3339Nano, trimmed)
		if err != nil {
			parsed, err = time.Parse(time.RFC3339, trimmed)
			if err != nil {
				return backendperformance.Event{}, "invalid_occurred_at"
			}
		}
		occurredAt = parsed.UTC()
	}

	status := normalizePerformanceToken(raw.Status)
	if status == "" {
		status = "success"
	}

	traceID := strings.TrimSpace(raw.TraceID)
	if traceID == "" {
		traceID = strings.TrimSpace(fallbackTraceID)
	}
	if traceID == "" {
		traceID = eventID
	}

	metaJSON := strings.TrimSpace(string(raw.Meta))
	if metaJSON == "" {
		metaJSON = "{}"
	}

	route := strings.TrimSpace(raw.Route)
	if route == "" {
		route = "-"
	}

	return backendperformance.Event{
		EventID:    eventID,
		TraceID:    traceID,
		Surface:    surface,
		Component:  component,
		Task:       task,
		Status:     status,
		DurationMS: raw.DurationMS,
		Route:      route,
		MetaJSON:   json.RawMessage(metaJSON),
		OccurredAt: occurredAt,
	}, ""
}

func normalizePerformanceToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func resolvePerformanceWindow(fromRaw string, toRaw string) (time.Time, time.Time, error) {
	fromRaw = strings.TrimSpace(fromRaw)
	toRaw = strings.TrimSpace(toRaw)
	if fromRaw == "" && toRaw == "" {
		now := time.Now().UTC()
		return now.Add(-24 * time.Hour), now, nil
	}

	var from time.Time
	var to time.Time
	var err error
	if fromRaw != "" {
		from, err = parsePerformanceTimeValue(fromRaw, false)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid from value")
		}
	}
	if toRaw != "" {
		to, err = parsePerformanceTimeValue(toRaw, true)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid to value")
		}
	}
	if !from.IsZero() && !to.IsZero() && to.Before(from) {
		return time.Time{}, time.Time{}, fmt.Errorf("from value must be before or equal to to value")
	}
	if from.IsZero() {
		from = to.Add(-24 * time.Hour)
	}
	if to.IsZero() {
		to = from.Add(24 * time.Hour)
	}
	return from.UTC(), to.UTC(), nil
}

func parsePerformanceTimeValue(raw string, endOfDay bool) (time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, time.DateTime, time.DateOnly} {
		parsed, err := time.Parse(layout, trimmed)
		if err != nil {
			continue
		}
		if layout == time.DateOnly {
			if endOfDay {
				return time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 23, 59, 59, 999999999, time.UTC), nil
			}
			return time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, time.UTC), nil
		}
		return parsed.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("unsupported time format")
}
