package api

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"demeter-backend/internal/store"
	"github.com/gofiber/fiber/v2"
)

const activityDayLayout = "2006-01-02"

type activityEventsRequest struct {
	Events []activityEventPayload `json:"events"`
}

type activityEventPayload struct {
	EventID    string          `json:"eventId"`
	EventKind  string          `json:"eventKind"`
	SourceMode string          `json:"sourceMode"`
	Provider   string          `json:"provider"`
	Status     string          `json:"status"`
	OccurredAt string          `json:"occurredAt"`
	Meta       json.RawMessage `json:"meta"`
}

type activityRejectedEvent struct {
	EventID string `json:"eventId"`
	Reason  string `json:"reason"`
}

type activityEventsResponse struct {
	Accepted   int                     `json:"accepted"`
	Duplicates int                     `json:"duplicates"`
	Rejected   []activityRejectedEvent `json:"rejected"`
}

var allowedProvidersByKindAndMode = map[string]map[string]map[string]struct{}{
	"transcription": {
		"local": {
			"local_upload": {},
			"mic":          {},
		},
		"cloud_direct": {
			"gradio":  {},
			"whisper": {},
			"mistral": {},
		},
		"cloud_backend": {
			"demeter_sante": {},
		},
	},
	"report": {
		"local": {
			"local": {},
		},
		"cloud_direct": {
			"huggingface": {},
			"mistral":     {},
		},
		"cloud_backend": {
			"demeter_sante": {},
		},
	},
}

func (a *App) RegisterActivityRoutes(router fiber.Router) {
	router.Post("/activity/events", a.AppAuthRequired(), a.postActivityEvents)
}

func (a *App) registerAdminActivityRoutes(group fiber.Router) {
	group.Get("/activity/organizations/:id/summary", a.adminOrganizationActivitySummary)
	group.Get("/activity/summary", a.adminActivitySummary)
}

func (a *App) postActivityEvents(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}

	var req activityEventsRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	if len(req.Events) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "events are required"})
	}

	validInputs := make([]store.ActivityEventInput, 0, len(req.Events))
	rejected := make([]activityRejectedEvent, 0)
	for _, raw := range req.Events {
		input, reason := validateActivityEvent(raw)
		if reason != "" {
			rejected = append(rejected, activityRejectedEvent{
				EventID: strings.TrimSpace(raw.EventID),
				Reason:  reason,
			})
			continue
		}
		validInputs = append(validInputs, input)
	}

	result := store.ActivityIngestResult{}
	if len(validInputs) > 0 {
		var err error
		result, err = a.Store.IngestActivityEvents(context.Background(), claims.OrgID, claims.UserID, validInputs)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to ingest activity events"})
		}
	}

	return c.JSON(activityEventsResponse{
		Accepted:   result.Accepted,
		Duplicates: result.Duplicates,
		Rejected:   rejected,
	})
}

func (a *App) adminOrganizationActivitySummary(c *fiber.Ctx) error {
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

	org, err := a.Store.GetOrganizationByID(context.Background(), orgID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load organization"})
	}
	if org == nil {
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "organization not found"})
	}

	fromDay, toDay, err := resolveActivityRange(c.Query("from"), c.Query("to"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: err.Error()})
	}

	summary, err := a.Store.GetOrganizationActivitySummary(context.Background(), orgID, fromDay, toDay)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load organization activity summary"})
	}
	return c.JSON(summary)
}

func (a *App) adminActivitySummary(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}

	fromDay, toDay, err := resolveActivityRange(c.Query("from"), c.Query("to"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: err.Error()})
	}

	orgID := strings.TrimSpace(c.Query("organizationId"))
	if orgID != "" {
		if !isSuperAdmin(claims) && claims.OrgID != orgID {
			return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
		}
		org, err := a.Store.GetOrganizationByID(context.Background(), orgID)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load organization"})
		}
		if org == nil {
			return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "organization not found"})
		}
		summary, err := a.Store.GetOrganizationActivitySummary(context.Background(), orgID, fromDay, toDay)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load activity summary"})
		}
		return c.JSON(summary)
	}

	if isSuperAdmin(claims) {
		summary, err := a.Store.GetGlobalActivitySummary(context.Background(), fromDay, toDay)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load activity summary"})
		}
		return c.JSON(summary)
	}

	summary, err := a.Store.GetOrganizationActivitySummary(context.Background(), claims.OrgID, fromDay, toDay)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load activity summary"})
	}
	return c.JSON(summary)
}

func validateActivityEvent(raw activityEventPayload) (store.ActivityEventInput, string) {
	eventID := strings.TrimSpace(raw.EventID)
	if eventID == "" {
		return store.ActivityEventInput{}, "event_id_required"
	}

	eventKind := strings.ToLower(strings.TrimSpace(raw.EventKind))
	mode := strings.ToLower(strings.TrimSpace(raw.SourceMode))
	provider := strings.ToLower(strings.TrimSpace(raw.Provider))
	status := strings.ToLower(strings.TrimSpace(raw.Status))

	if eventKind != "transcription" && eventKind != "report" {
		return store.ActivityEventInput{}, "invalid_event_kind"
	}
	if mode != "local" && mode != "cloud_direct" && mode != "cloud_backend" {
		return store.ActivityEventInput{}, "invalid_source_mode"
	}
	if status != "success" && status != "error" {
		return store.ActivityEventInput{}, "invalid_status"
	}

	allowedByMode, ok := allowedProvidersByKindAndMode[eventKind]
	if !ok {
		return store.ActivityEventInput{}, "invalid_event_kind"
	}
	allowedProviders, ok := allowedByMode[mode]
	if !ok {
		return store.ActivityEventInput{}, "invalid_source_mode"
	}
	if _, ok := allowedProviders[provider]; !ok {
		return store.ActivityEventInput{}, "invalid_provider_for_mode"
	}

	var occurredAt time.Time
	if value := strings.TrimSpace(raw.OccurredAt); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return store.ActivityEventInput{}, "invalid_occurred_at"
		}
		occurredAt = parsed.UTC()
	} else {
		occurredAt = time.Now().UTC()
	}

	meta := raw.Meta
	if len(strings.TrimSpace(string(meta))) > 0 && !json.Valid(meta) {
		return store.ActivityEventInput{}, "invalid_meta_json"
	}

	return store.ActivityEventInput{
		EventID:    eventID,
		EventKind:  eventKind,
		SourceMode: mode,
		Provider:   provider,
		Status:     status,
		OccurredAt: occurredAt,
		MetaJSON:   meta,
	}, ""
}

func resolveActivityRange(fromRaw string, toRaw string) (string, string, error) {
	nowUTC := time.Now().UTC()
	toDay := strings.TrimSpace(toRaw)
	if toDay == "" {
		toDay = nowUTC.Format(activityDayLayout)
	}
	toDate, err := time.Parse(activityDayLayout, toDay)
	if err != nil {
		return "", "", fiber.NewError(fiber.StatusBadRequest, "invalid to date format, expected YYYY-MM-DD")
	}

	fromDay := strings.TrimSpace(fromRaw)
	if fromDay == "" {
		fromDay = toDate.AddDate(0, 0, -29).Format(activityDayLayout)
	}
	fromDate, err := time.Parse(activityDayLayout, fromDay)
	if err != nil {
		return "", "", fiber.NewError(fiber.StatusBadRequest, "invalid from date format, expected YYYY-MM-DD")
	}
	if fromDate.After(toDate) {
		return "", "", fiber.NewError(fiber.StatusBadRequest, "from date must be before or equal to to date")
	}
	return fromDate.Format(activityDayLayout), toDate.Format(activityDayLayout), nil
}
