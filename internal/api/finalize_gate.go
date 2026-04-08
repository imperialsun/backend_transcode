package api

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
)

const (
	meetingFinalizeOperationHeader      = "X-Idempotency-Key"
	meetingFinalizeOperationLocalKey    = "meeting-finalize-operation-id"
	meetingFinalizeOperationStatusRoute = "/api/v1/meetings/finalize/operations/:operationId"
)

type meetingFinalizeOperationStatusResponse struct {
	OperationID string          `json:"operationId"`
	Status      string          `json:"status"`
	StatusCode  int             `json:"statusCode"`
	Response    json.RawMessage `json:"response,omitempty"`
	Error       string          `json:"error,omitempty"`
	UpdatedAt   string          `json:"updatedAt,omitempty"`
	ExpiresAt   string          `json:"expiresAt,omitempty"`
}

// finalizeMeetingOperationGate claims an idempotent meeting finalization
// operation before the main handler runs.
func (a *App) finalizeMeetingOperationGate() fiber.Handler {
	return func(c *fiber.Ctx) error {
		traceID := requestTraceID(c)
		claims := MustClaims(c)
		if claims == nil {
			return c.Next()
		}

		operationID := requestFinalizeOperationID(c)
		if operationID == "" {
			logMeetingStage(c, meetingFinalizeRoute, traceID, "operation_id_missing", "", map[string]any{
				"error":       "missing idempotency key",
				"status_code": fiber.StatusBadRequest,
			})
			return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "missing idempotency key"})
		}

		result, err := a.Store.ClaimMeetingFinalizeOperation(requestContext(c), operationID, claims.OrgID, claims.UserID, time.Now().UTC())
		if err != nil {
			if err == store.ErrMeetingFinalizeOperationOwnership {
				logMeetingStage(c, meetingFinalizeRoute, traceID, "operation_not_found", "", map[string]any{
					"operation_id": operationID,
					"reason":       "ownership_mismatch",
					"status_code":  fiber.StatusNotFound,
				})
				return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "operation not found"})
			}
			logMeetingStage(c, meetingFinalizeRoute, traceID, "operation_claim_error", "", map[string]any{
				"operation_id": operationID,
				"error":        err.Error(),
			})
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to prepare meeting finalization"})
		}

		if result == nil || result.Record == nil {
			logMeetingStage(c, meetingFinalizeRoute, traceID, "operation_claim_error", "", map[string]any{
				"operation_id": operationID,
				"error":        "missing operation record",
			})
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to prepare meeting finalization"})
		}

		c.Locals(meetingFinalizeOperationLocalKey, operationID)
		switch result.Record.Status {
		case store.MeetingFinalizeOperationStatusPending:
			if result.Claimed {
				logMeetingStage(c, meetingFinalizeRoute, traceID, "operation_claimed", "", map[string]any{
					"operation_id": operationID,
					"status":       result.Record.Status,
					"status_code":  result.Record.StatusCode,
				})
				return c.Next()
			}
			logMeetingStage(c, meetingFinalizeRoute, traceID, "operation_pending", "", map[string]any{
				"operation_id": operationID,
				"status":       result.Record.Status,
				"status_code":  result.Record.StatusCode,
			})
			return c.Status(fiber.StatusConflict).JSON(meetingFinalizeOperationStatusResponse{
				OperationID: operationID,
				Status:      store.MeetingFinalizeOperationStatusPending,
				StatusCode:  fiber.StatusAccepted,
				Error:       "meeting finalization already in progress",
			})
		case store.MeetingFinalizeOperationStatusCompleted, store.MeetingFinalizeOperationStatusFailed:
			logMeetingStage(c, meetingFinalizeRoute, traceID, "operation_replayed", "", map[string]any{
				"operation_id": operationID,
				"status":       result.Record.Status,
				"status_code":  result.Record.StatusCode,
			})
			return sendStoredMeetingFinalizeOperation(c, result.Record)
		default:
			logMeetingStage(c, meetingFinalizeRoute, traceID, "operation_state_error", "", map[string]any{
				"operation_id": operationID,
				"status":       result.Record.Status,
				"status_code":  result.Record.StatusCode,
			})
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "unexpected meeting finalization state"})
		}
	}
}

// getFinalizeMeetingOperationStatus returns the stored state for one finalize
// operation.
func (a *App) getFinalizeMeetingOperationStatus(c *fiber.Ctx) error {
	traceID := requestTraceID(c)
	logMeetingStage(c, meetingFinalizeOperationStatusRoute, traceID, "request_received", "", nil)

	claims := MustClaims(c)
	if claims == nil {
		logMeetingStage(c, meetingFinalizeOperationStatusRoute, traceID, "request_unauthorized", "", map[string]any{
			"error": "unauthorized",
		})
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}

	operationID := requestFinalizeOperationID(c)
	if operationID == "" {
		logMeetingStage(c, meetingFinalizeOperationStatusRoute, traceID, "operation_status_missing", "", map[string]any{
			"error":       "missing operation id",
			"reason":      "operation_id_missing",
			"status_code": fiber.StatusBadRequest,
		})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "missing operation id"})
	}

	record, err := a.Store.GetMeetingFinalizeOperation(requestContext(c), operationID, claims.OrgID, claims.UserID, time.Now().UTC())
	if err != nil {
		if err == store.ErrMeetingFinalizeOperationOwnership || err == sql.ErrNoRows {
			logMeetingStage(c, meetingFinalizeOperationStatusRoute, traceID, "operation_status_missing", "", map[string]any{
				"operation_id": operationID,
				"reason":       "not_found",
				"status_code":  fiber.StatusNotFound,
			})
			return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "operation not found"})
		}
		logMeetingStage(c, meetingFinalizeOperationStatusRoute, traceID, "operation_status_error", "", map[string]any{
			"operation_id": operationID,
			"error":        err.Error(),
			"status_code":  fiber.StatusInternalServerError,
		})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load meeting finalization status"})
	}

	response := meetingFinalizeOperationStatusResponse{
		OperationID: record.OperationID,
		Status:      record.Status,
		StatusCode:  record.StatusCode,
		UpdatedAt:   record.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if record.TerminalExpiresAt.Valid {
		response.ExpiresAt = record.TerminalExpiresAt.Time.UTC().Format(time.RFC3339)
	}
	if record.ResponseJSON.Valid && strings.TrimSpace(record.ResponseJSON.String) != "" {
		response.Response = json.RawMessage(record.ResponseJSON.String)
	}
	if record.ErrorMessage.Valid {
		response.Error = strings.TrimSpace(record.ErrorMessage.String)
	}

	logMeetingStage(c, meetingFinalizeOperationStatusRoute, traceID, "operation_status_ready", "", map[string]any{
		"operation_id": operationID,
		"status":       record.Status,
		"status_code":  record.StatusCode,
	})
	return c.Status(fiber.StatusOK).JSON(response)
}

// requestFinalizeOperationID resolves the idempotency key from the path, the
// dedicated header, or Fiber locals.
func requestFinalizeOperationID(c *fiber.Ctx) string {
	if c == nil {
		return ""
	}
	if value := strings.TrimSpace(c.Params("operationId")); value != "" {
		return value
	}
	if value := strings.TrimSpace(c.Get(meetingFinalizeOperationHeader)); value != "" {
		return value
	}
	if value, ok := c.Locals(meetingFinalizeOperationLocalKey).(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

// sendStoredMeetingFinalizeOperation replays a previously completed finalize
// response verbatim.
func sendStoredMeetingFinalizeOperation(c *fiber.Ctx, record *store.MeetingFinalizeOperationRecord) error {
	if c == nil || record == nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to replay meeting finalization")
	}
	body := strings.TrimSpace(record.ResponseJSON.String)
	if body == "" {
		body = `{"error":"meeting finalization unavailable"}`
	}
	c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSONCharsetUTF8)
	return c.Status(record.StatusCode).Send([]byte(body))
}
