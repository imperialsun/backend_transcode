package api

import (
	"encoding/json"
	"strings"
	"time"

	"demeter-backend/internal/backenderrors"

	"github.com/gofiber/fiber/v2"
)

const frontendErrorReportRoute = "/support/frontend-error-reports"

type frontendErrorReportRequest struct {
	TraceID          string                   `json:"traceId,omitempty"`
	Provider         string                   `json:"provider,omitempty"`
	BackendError     frontendErrorReportError `json:"backendError"`
	OriginalFile     frontendErrorReportFile  `json:"originalFile"`
	ProcessedFile    frontendErrorReportFile  `json:"processedFile"`
	RawFile          *frontendErrorReportFile `json:"rawFile,omitempty"`
	Retry            frontendErrorReportRetry `json:"retry"`
	DiagnosticBundle json.RawMessage          `json:"diagnosticBundle,omitempty"`
}

type frontendErrorReportError struct {
	Status  int    `json:"status,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Path    string `json:"path,omitempty"`
	Method  string `json:"method,omitempty"`
	TraceID string `json:"traceId,omitempty"`
}

type frontendErrorReportFile struct {
	Name      string `json:"name,omitempty"`
	SizeBytes int64  `json:"sizeBytes"`
	MimeType  string `json:"mimeType,omitempty"`
	Source    string `json:"source,omitempty"`
}

type frontendErrorReportRetry struct {
	Attempted   bool `json:"attempted"`
	Succeeded   bool `json:"succeeded"`
	UsedRawFile bool `json:"usedRawFile,omitempty"`
}

func (a *App) RegisterSupportRoutes(router fiber.Router) {
	group := router.Group("/support", a.AppAuthRequired())
	group.Post("/frontend-error-reports", a.submitFrontendErrorReport)
}

func (a *App) submitFrontendErrorReport(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "frontend", route, "request_received", "frontend_error_report", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "frontend", route, "request_unauthorized", "frontend_error_report", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized", Code: "unauthorized", TraceID: requestTraceID(c), Path: route})
	}

	var req frontendErrorReportRequest
	if err := c.BodyParser(&req); err != nil {
		logAPIStep(c, "frontend", route, "request_failed", "frontend_error_report", map[string]any{
			"status_code": fiber.StatusBadRequest,
			"error":       err,
		})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error:   "invalid frontend error report payload",
			Code:    "invalid_report_payload",
			TraceID: requestTraceID(c),
			Path:    route,
		})
	}

	traceID := strings.TrimSpace(req.TraceID)
	recoveryStatus := frontendReportRecoveryStatus(req.Retry)
	annexSummary := map[string]any{
		"source":         "frontend",
		"reportTraceId":  traceID,
		"requestTraceId": requestTraceID(c),
		"provider":       strings.TrimSpace(req.Provider),
		"backendError":   req.BackendError,
		"originalFile":   req.OriginalFile,
		"processedFile":  req.ProcessedFile,
		"retry":          req.Retry,
		"createdAt":      time.Now().UTC().Format(time.RFC3339Nano),
	}
	if req.RawFile != nil {
		annexSummary["rawFile"] = req.RawFile
	}
	if recoveryStatus != "" {
		annexSummary["recoveryStatus"] = recoveryStatus
	}

	payload, err := json.Marshal(req)
	if err != nil {
		logAPIStep(c, "frontend", route, "request_failed", "frontend_error_report", map[string]any{
			"status_code": fiber.StatusBadRequest,
			"error":       err,
		})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error:   "invalid frontend error report payload",
			Code:    "invalid_report_payload",
			TraceID: requestTraceID(c),
			Path:    route,
		})
	}

	if traceID != "" {
		annexPayload, err := json.Marshal(annexSummary)
		if err != nil {
			logAPIStep(c, "frontend", route, "annex_error", "frontend_error_report", map[string]any{
				"error": err,
			})
		} else {
			if rowsAffected, err := a.Store.AttachBackendErrorAnnex(requestContext(c), traceID, annexPayload, recoveryStatus); err != nil {
				logAPIStep(c, "frontend", route, "annex_error", "frontend_error_report", map[string]any{
					"error":    err,
					"trace_id": traceID,
				})
			} else {
				logAPIStep(c, "frontend", route, "annex_attached", "frontend_error_report", map[string]any{
					"trace_id":        traceID,
					"rows_affected":   rowsAffected,
					"recovery_status": recoveryStatus,
				})
			}
		}
	}

	reportTraceID := traceID
	if reportTraceID == "" {
		reportTraceID = requestTraceID(c)
	}
	fileName, fileSizeBytes, mimeType := summarizeFrontendReportFile(req)
	statusCode := req.BackendError.Status
	if statusCode == 0 {
		statusCode = fiber.StatusBadRequest
	}
	errorMessage := strings.TrimSpace(req.BackendError.Message)
	if errorMessage == "" {
		if req.Retry.Attempted && req.Retry.Succeeded {
			errorMessage = "frontend audio report recovered after raw retry"
		} else if req.OriginalFile.SizeBytes == 0 {
			errorMessage = "fichier audio vide"
		} else {
			errorMessage = "frontend audio report"
		}
	}

	event := backenderrors.Event{
		TraceID:        reportTraceID,
		UserID:         claims.UserID,
		OrganizationID: claims.OrgID,
		Component:      "frontend",
		Route:          route,
		Step:           "report_received",
		Title:          "frontend_audio_retry_report",
		StatusCode:     statusCode,
		ErrorMessage:   errorMessage,
		PayloadJSON:    payload,
		AnnexJSON:      mustMarshalJSON(annexSummary),
		RecoveryStatus: recoveryStatus,
		CreatedAt:      time.Now().UTC(),
	}

	if err := a.Store.InsertBackendErrorEvent(requestContext(c), event); err != nil {
		logAPIStep(c, "frontend", route, "request_failed", "frontend_error_report", map[string]any{
			"status_code": fiber.StatusInternalServerError,
			"error":       err,
			"trace_id":    reportTraceID,
		})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
			Error:   "failed to store frontend error report",
			Code:    "frontend_report_store_failed",
			TraceID: requestTraceID(c),
			Path:    route,
		})
	}

	logAPIStep(c, "frontend", route, "response_ready", "frontend_error_report", map[string]any{
		"trace_id":        reportTraceID,
		"status_code":     statusCode,
		"recovery_status": recoveryStatus,
		"file_name":       fileName,
		"file_size_bytes": fileSizeBytes,
		"mime_type":       mimeType,
	})
	return c.SendStatus(fiber.StatusNoContent)
}

func summarizeFrontendReportFile(req frontendErrorReportRequest) (string, int64, string) {
	if req.ProcessedFile.Name != "" {
		return req.ProcessedFile.Name, req.ProcessedFile.SizeBytes, req.ProcessedFile.MimeType
	}
	if req.RawFile != nil && req.RawFile.Name != "" {
		return req.RawFile.Name, req.RawFile.SizeBytes, req.RawFile.MimeType
	}
	if req.OriginalFile.Name != "" {
		return req.OriginalFile.Name, req.OriginalFile.SizeBytes, req.OriginalFile.MimeType
	}
	return "", 0, ""
}

func frontendReportRecoveryStatus(retry frontendErrorReportRetry) string {
	if !retry.Attempted {
		return ""
	}
	if retry.Succeeded {
		return "raw_retry_succeeded"
	}
	return "raw_retry_failed"
}

func mustMarshalJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		return []byte(`{}`)
	}
	return raw
}
