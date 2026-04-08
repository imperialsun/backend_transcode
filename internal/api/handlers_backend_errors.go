package api

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
)

// backendErrorEventsResponse is the paginated admin response for captured
// backend errors.
type backendErrorEventsResponse struct {
	Items    []store.BackendErrorEvent `json:"items"`
	Total    int                       `json:"total"`
	Page     int                       `json:"page"`
	PageSize int                       `json:"pageSize"`
}

// registerBackendErrorRoutes installs the admin backend-error listing and
// purge routes.
func (a *App) registerBackendErrorRoutes(group fiber.Router) {
	group.Get("/backend-errors", RequireSuperAdminScope(), a.listBackendErrorEvents)
	group.Delete("/backend-errors", RequireSuperAdminScope(), a.deleteBackendErrorEvents)
}

// listBackendErrorEvents returns the paginated backend-error history.
func (a *App) listBackendErrorEvents(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "list_backend_errors", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "admin", route, "request_unauthorized", "list_backend_errors", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}

	from, to, err := resolveBackendErrorRange(c.Query("from"), c.Query("to"))
	if err != nil {
		logAPIStep(c, "admin", route, "range_error", "list_backend_errors", map[string]any{"error": err})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: err.Error()})
	}

	page, pageSize, err := resolveBackendErrorPagination(c.Query("page"), c.Query("pageSize"))
	if err != nil {
		logAPIStep(c, "admin", route, "request_validation_error", "list_backend_errors", map[string]any{"error": err})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: err.Error()})
	}

	filters := store.BackendErrorEventFilters{
		OrganizationID: strings.TrimSpace(c.Query("organizationId")),
		UserID:         strings.TrimSpace(c.Query("userId")),
		Component:      strings.TrimSpace(c.Query("component")),
		Route:          strings.TrimSpace(c.Query("route")),
		Query:          strings.TrimSpace(c.Query("q")),
		From:           from,
		To:             to,
		Limit:          pageSize,
		Offset:         (page - 1) * pageSize,
	}

	logAPIStep(c, "admin", route, "load_start", "list_backend_errors", map[string]any{
		"organization_id": filters.OrganizationID,
		"user_id":         filters.UserID,
		"component":       filters.Component,
		"route":           filters.Route,
		"query":           filters.Query,
		"from":            filters.From,
		"to":              filters.To,
		"page":            page,
		"page_size":       pageSize,
	})
	result, err := a.Store.ListBackendErrorEvents(requestContext(c), filters)
	if err != nil {
		logAPIStep(c, "admin", route, "load_error", "list_backend_errors", map[string]any{"error": err})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to list backend errors"})
	}

	logAPIStep(c, "admin", route, "response_ready", "list_backend_errors", map[string]any{
		"organization_id": filters.OrganizationID,
		"user_id":         filters.UserID,
		"total":           result.Total,
		"item_count":      len(result.Items),
		"page":            page,
		"page_size":       pageSize,
	})
	return c.JSON(backendErrorEventsResponse{
		Items:    result.Items,
		Total:    result.Total,
		Page:     page,
		PageSize: pageSize,
	})
}

// deleteBackendErrorEvents purges backend-error rows that match the supplied
// filters.
func (a *App) deleteBackendErrorEvents(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "purge_backend_errors", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "admin", route, "request_unauthorized", "purge_backend_errors", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}

	from, to, err := resolveBackendErrorRange(c.Query("from"), c.Query("to"))
	if err != nil {
		logAPIStep(c, "admin", route, "range_error", "purge_backend_errors", map[string]any{"error": err})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: err.Error()})
	}

	filters := store.BackendErrorEventFilters{
		OrganizationID: strings.TrimSpace(c.Query("organizationId")),
		UserID:         strings.TrimSpace(c.Query("userId")),
		Component:      strings.TrimSpace(c.Query("component")),
		Route:          strings.TrimSpace(c.Query("route")),
		Query:          strings.TrimSpace(c.Query("q")),
		From:           from,
		To:             to,
	}

	logAPIStep(c, "admin", route, "delete_start", "purge_backend_errors", map[string]any{
		"organization_id": filters.OrganizationID,
		"user_id":         filters.UserID,
		"component":       filters.Component,
		"route":           filters.Route,
		"query":           filters.Query,
		"from":            filters.From,
		"to":              filters.To,
	})
	deletedCount, err := a.Store.DeleteBackendErrorEvents(requestContext(c), filters)
	if err != nil {
		logAPIStep(c, "admin", route, "delete_error", "purge_backend_errors", map[string]any{"error": err})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to purge backend errors"})
	}

	a.writeAdminAudit(requestContext(c), claims, "admin.backend_error.purge", "backend_error_events", "", fiber.Map{
		"organizationId": filters.OrganizationID,
		"userId":         filters.UserID,
		"component":      filters.Component,
		"route":          filters.Route,
		"query":          filters.Query,
		"from":           rangeAuditValue(filters.From),
		"to":             rangeAuditValue(filters.To),
		"deletedCount":   deletedCount,
	})

	logAPIStep(c, "admin", route, "response_ready", "purge_backend_errors", map[string]any{
		"organization_id": filters.OrganizationID,
		"user_id":         filters.UserID,
		"deleted_count":   deletedCount,
	})
	return c.SendStatus(fiber.StatusNoContent)
}

// resolveBackendErrorRange normalizes inclusive date filters into UTC
// timestamps.
func resolveBackendErrorRange(fromRaw string, toRaw string) (time.Time, time.Time, error) {
	fromRaw = strings.TrimSpace(fromRaw)
	toRaw = strings.TrimSpace(toRaw)
	if fromRaw == "" && toRaw == "" {
		return time.Time{}, time.Time{}, nil
	}

	var from time.Time
	var to time.Time
	var err error
	if fromRaw != "" {
		from, err = time.Parse(time.DateOnly, fromRaw)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid from date")
		}
		from = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	}
	if toRaw != "" {
		to, err = time.Parse(time.DateOnly, toRaw)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid to date")
		}
		to = time.Date(to.Year(), to.Month(), to.Day(), 23, 59, 59, 999999999, time.UTC)
	}
	if !from.IsZero() && !to.IsZero() && to.Before(from) {
		return time.Time{}, time.Time{}, fmt.Errorf("from date must be before or equal to to date")
	}
	return from, to, nil
}

// resolveBackendErrorPagination validates and bounds the page controls.
func resolveBackendErrorPagination(pageRaw, pageSizeRaw string) (int, int, error) {
	page := 1
	pageSize := 25

	if trimmed := strings.TrimSpace(pageRaw); trimmed != "" {
		parsed, err := strconv.Atoi(trimmed)
		if err != nil || parsed <= 0 {
			return 0, 0, fmt.Errorf("invalid page")
		}
		page = parsed
	}

	if trimmed := strings.TrimSpace(pageSizeRaw); trimmed != "" {
		parsed, err := strconv.Atoi(trimmed)
		if err != nil || parsed <= 0 {
			return 0, 0, fmt.Errorf("invalid page size")
		}
		if parsed > 100 {
			parsed = 100
		}
		pageSize = parsed
	}

	return page, pageSize, nil
}

// rangeAuditValue converts an optional range boundary into a serializable
// audit-log value.
func rangeAuditValue(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}
