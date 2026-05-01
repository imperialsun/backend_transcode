package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net/mail"
	"strings"
	"time"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/backenderrors"
	"demeter-backend/internal/backendperformance"
	"demeter-backend/internal/mailer"
	"demeter-backend/internal/observability"
	meetingreports "demeter-backend/internal/reports"
	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

const (
	mobileReportEmailRoute        = "/api/v1/mobile/reports/email"
	mobileAudioReportBackendRoute = "/api/v1/mobile/audio/reports/backend"
	mobileOperationStatusRoute    = "/api/v1/mobile/operations/:operationId"

	mobileReportSourceMode       = "cloud_backend"
	mobileReportProvider         = "demeter_sante"
	mobileReportProviderMaxToken = 131072
)

type mobileOperationActor struct {
	UserID string
	OrgID  string
	Email  string
}

type mobileReportEmailRequest struct {
	OperationID          string                             `json:"operationId,omitempty"`
	MeetingTitle         string                             `json:"meetingTitle,omitempty"`
	Participants         []string                           `json:"participants,omitempty"`
	SelectedFormats      []string                           `json:"selectedFormats,omitempty"`
	RawTranscriptText    string                             `json:"rawTranscriptText,omitempty"`
	EditedTranscriptText string                             `json:"editedTranscriptText,omitempty"`
	TranscriptSegments   []meetingreports.TranscriptSegment `json:"transcriptSegments,omitempty"`
	SpeakerAssignments   []meetingreports.SpeakerAssignment `json:"speakerAssignments,omitempty"`
	ReportDetailLevels   map[string]string                  `json:"reportDetailLevels,omitempty"`
}

type mobileAudioReportRequest struct {
	MeetingTitle       string
	Participants       []string
	SelectedFormats    []string
	ReportDetailLevels map[string]string
}

type mobileReportEnvelope struct {
	Format           string                    `json:"format"`
	Report           meetingreports.ReportJson `json:"report"`
	Raw              string                    `json:"raw,omitempty"`
	ModelID          string                    `json:"modelId,omitempty"`
	GeneratedAt      string                    `json:"generatedAt,omitempty"`
	SourceMode       string                    `json:"sourceMode,omitempty"`
	Provider         string                    `json:"provider,omitempty"`
	SourceTokenCount int                       `json:"sourceTokenCount,omitempty"`
	DetailLevel      string                    `json:"detailLevel,omitempty"`
}

type mobileFileResponse struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	SizeBytes   int    `json:"sizeBytes"`
}

type mobileOperationResponse struct {
	OperationID      string               `json:"operationId"`
	Status           string               `json:"status"`
	StatusCode       int                  `json:"statusCode"`
	Stage            string               `json:"stage"`
	Progress         float64              `json:"progress"`
	ChunkIndex       int                  `json:"chunkIndex"`
	ChunkCount       int                  `json:"chunkCount"`
	Message          string               `json:"message,omitempty"`
	LastError        string               `json:"lastError,omitempty"`
	AudioOperationID string               `json:"audioOperationId,omitempty"`
	Files            []mobileFileResponse `json:"files,omitempty"`
	UpdatedAt        string               `json:"updatedAt,omitempty"`
	FinishedAt       string               `json:"finishedAt,omitempty"`
}

type mobileReportEmailResponse struct {
	OperationID  string                 `json:"operationId"`
	MeetingTitle string                 `json:"meetingTitle"`
	SentTo       string                 `json:"sentTo"`
	SentToEmails []string               `json:"sentToEmails"`
	Files        []mobileFileResponse   `json:"files"`
	Reports      []mobileReportEnvelope `json:"reports,omitempty"`
	GeneratedAt  string                 `json:"generatedAt"`
	SourceMode   string                 `json:"sourceMode"`
	Provider     string                 `json:"provider"`
}

type mobileReportSettings struct {
	ModelID           string
	Temperature       float64
	MaxTokens         int
	DetailLevels      map[meetingreports.ReportFormat]meetingreports.ReportDetailLevel
	MonoPassMaxTokens int
}

// RegisterMobileRoutes installs the simplified mobile API surface.
func (a *App) RegisterMobileRoutes(router fiber.Router) {
	group := router.Group("/mobile", a.AppAuthRequired())
	group.Post("/reports/email", RequirePermissions("feature.llmapi", "provider.llm.demeter_sante"), a.postMobileReportEmail)
	group.Post("/audio/reports/backend", RequirePermissions("feature.cloudupload", "provider.cloud.demeter_sante", "feature.llmapi", "provider.llm.demeter_sante"), a.postMobileAudioReportsBackend)
	group.Get("/operations/:operationId", RequireAnyPermission("feature.llmapi", "feature.cloudupload"), a.getMobileOperationStatus)
}

func (a *App) postMobileReportEmail(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	traceID := requestTraceID(c)
	startedAt := time.Now()
	logMobileStage(c, route, traceID, "request_received", "report_email", nil)

	claims := MustClaims(c)
	if claims == nil {
		logMobileStage(c, route, traceID, "request_unauthorized", "report_email", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	var req mobileReportEmailRequest
	if err := c.BodyParser(&req); err != nil {
		logMobileStage(c, route, traceID, "request_parse_error", "report_email", map[string]any{"error": err.Error()})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	operationID := mobileOperationID(c, req.OperationID)
	title := normalizeMobileMeetingTitle(req.MeetingTitle)
	rawTranscript := resolveMobileTranscriptForMail(req.RawTranscriptText, req.EditedTranscriptText)
	reportSourceText := buildMobileReportSourceText(req.EditedTranscriptText, req.RawTranscriptText, req.SpeakerAssignments)
	if strings.TrimSpace(rawTranscript) == "" || strings.TrimSpace(reportSourceText) == "" {
		logMobileStage(c, route, traceID, "validation_error", title, map[string]any{"operation_id": operationID, "reason": "missing_transcript"})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "transcript text is required"})
	}

	now := time.Now().UTC()
	result, err := a.Store.CreateMobileOperationIfAbsent(requestContext(c), &store.MobileOperationRecord{
		OperationID:    operationID,
		OrganizationID: claims.OrgID,
		UserID:         claims.UserID,
		Kind:           "report_email",
		Status:         store.MobileOperationStatusRunning,
		StatusCode:     fiber.StatusAccepted,
		Stage:          "queued",
		Progress:       0.05,
		Message:        sql.NullString{String: "generation queued", Valid: true},
		CreatedAt:      now,
		UpdatedAt:      now,
	})
	if err != nil {
		logMobileStage(c, route, traceID, "operation_create_error", title, map[string]any{"operation_id": operationID, "error": err.Error()})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to create mobile operation"})
	}
	if !result.Created {
		logMobileStage(c, route, traceID, "operation_reused", title, map[string]any{"operation_id": operationID, "status": result.Record.Status, "stage": result.Record.Stage})
		return c.Status(result.Record.StatusCode).JSON(mobileOperationResponseFromRecord(result.Record))
	}

	actor := mobileActorFromClaims(claims)
	job := req
	job.OperationID = operationID
	go a.runMobileReportEmailOperation(observability.WithTraceID(context.Background(), traceID), traceID, route, actor, job)

	logMobileStage(c, route, traceID, "response_ready", title, map[string]any{
		"operation_id":       operationID,
		"status_code":        fiber.StatusAccepted,
		"duration_ms":        time.Since(startedAt).Milliseconds(),
		"transcript_bytes":   len(rawTranscript),
		"participants_count": len(req.Participants),
	})
	return c.Status(fiber.StatusAccepted).JSON(mobileOperationResponseFromRecord(result.Record))
}

func (a *App) postMobileAudioReportsBackend(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	traceID := requestTraceID(c)
	startedAt := time.Now()
	seq := nextDemeterAudioSequenceID()
	routeMode := demeterAudioRouteMode(route)
	audioDurationSec, audioDurationProvided := requestDemeterAudioDurationSec(c)
	contentType := strings.TrimSpace(c.Get(fiber.HeaderContentType))
	requestBytes := c.Request().Header.ContentLength()
	if requestBytes < 0 {
		requestBytes = 0
	}
	logMobileStage(c, route, traceID, "request_received", "audio_report_backend", map[string]any{
		"content_type":  contentType,
		"request_bytes": requestBytes,
	})

	claims := MustClaims(c)
	if claims == nil {
		logMobileStage(c, route, traceID, "request_unauthorized", "audio_report_backend", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	if !a.MistralClient.IsConfigured() {
		logMobileStage(c, route, traceID, "request_failed", "audio_report_backend", map[string]any{"reason": "mistral_not_configured"})
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "mistral is not configured"})
	}
	if !strings.HasPrefix(contentType, fiber.MIMEMultipartForm) {
		logMobileStage(c, route, traceID, "request_failed", "audio_report_backend", map[string]any{"reason": "invalid_content_type", "content_type": contentType})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "multipart/form-data is required"})
	}
	if !isDemeterAudioSliceTransport(c) {
		logMobileStage(c, route, traceID, "request_failed", "audio_report_backend", map[string]any{"reason": "invalid_transport"})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "X-Demeter-Transport: slice-v1 is required"})
	}
	if requestBytes > demeterAudioTransportMaxRequestBytes {
		logMobileStage(c, route, traceID, "request_failed", "audio_report_backend", map[string]any{"reason": "payload_too_large", "request_bytes": requestBytes})
		return c.Status(fiber.StatusRequestEntityTooLarge).JSON(ErrorResponse{Error: "multipart payload too large"})
	}

	logCtx := newDemeterAudioLogContextFromFiber(c)
	req, err := parseDemeterAudioTransportSliceRequest(c)
	if err != nil {
		logMobileStage(c, route, traceID, "validation_error", "audio_report_backend", map[string]any{"error": err.Error()})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: err.Error(), Code: "invalid_transport", TraceID: traceID, Path: route})
	}
	audioReq := parseMobileAudioReportRequest(c)
	operationID := strings.TrimSpace(req.UploadID)
	cleanupExpiredDemeterAudioTransportSessions(logCtx, time.Now().UTC())

	if req.Final {
		if existing, loadErr := a.Store.GetMobileOperation(requestContext(c), operationID, claims.OrgID, claims.UserID); loadErr == nil {
			if isMobileOperationTerminal(existing.Status) || strings.TrimSpace(existing.Stage) != "uploading" || existing.AudioOperationID.Valid {
				logMobileStage(c, route, traceID, "operation_reused", "audio_report_backend", map[string]any{"operation_id": operationID, "status": existing.Status, "stage": existing.Stage})
				return c.Status(existing.StatusCode).JSON(mobileOperationResponseFromRecord(existing))
			}
		} else if !errors.Is(loadErr, sql.ErrNoRows) && !errors.Is(loadErr, store.ErrMobileOperationOwnership) {
			logMobileStage(c, route, traceID, "operation_load_error", "audio_report_backend", map[string]any{"operation_id": operationID, "error": loadErr.Error()})
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load mobile operation"})
		}
	}

	session, err := getOrCreateDemeterAudioTransportSession(operationID, claims.OrgID, claims.UserID, route, routeMode, req)
	if err != nil {
		logMobileStage(c, route, traceID, "transport_session_error", "audio_report_backend", map[string]any{"operation_id": operationID, "error": err.Error()})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: err.Error(), Code: "transport_session_error", TraceID: traceID, Path: route})
	}
	form, err := c.MultipartForm()
	if err != nil {
		logMobileStage(c, route, traceID, "request_parse_error", "audio_report_backend", map[string]any{"operation_id": operationID, "error": err.Error()})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid multipart form"})
	}
	fileHeaders := form.File["file"]
	if len(fileHeaders) == 0 {
		logMobileStage(c, route, traceID, "validation_error", "audio_report_backend", map[string]any{"operation_id": operationID, "reason": "missing_file"})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "multipart file part is missing"})
	}
	storedBytes, err := storeDemeterAudioTransportSlice(logCtx, route, seq, session, req, fileHeaders[0])
	if err != nil {
		var validationErr *demeterAudioValidationError
		if errors.As(err, &validationErr) {
			return a.demeterAudioValidationFailure(c, route, seq, startedAt, requestBytes, contentType, routeMode, audioDurationSec, audioDurationProvided, validationErr)
		}
		logMobileStage(c, route, traceID, "slice_store_error", "audio_report_backend", map[string]any{"operation_id": operationID, "error": err.Error()})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: err.Error(), Code: "transport_store_error", TraceID: traceID, Path: route})
	}

	progress := 0.02
	if req.SliceCount > 0 {
		progress = minFloat64(0.1, float64(req.SliceIndex+1)/float64(req.SliceCount)*0.1)
	}
	now := time.Now().UTC()
	createResult, err := a.Store.CreateMobileOperationIfAbsent(requestContext(c), &store.MobileOperationRecord{
		OperationID:    operationID,
		OrganizationID: claims.OrgID,
		UserID:         claims.UserID,
		Kind:           "audio_report_email",
		Status:         store.MobileOperationStatusRunning,
		StatusCode:     fiber.StatusAccepted,
		Stage:          "uploading",
		Progress:       progress,
		ChunkIndex:     req.SliceIndex + 1,
		ChunkCount:     req.SliceCount,
		Message:        sql.NullString{String: "audio upload in progress", Valid: true},
		CreatedAt:      now,
		UpdatedAt:      now,
	})
	if err != nil {
		logMobileStage(c, route, traceID, "operation_create_error", "audio_report_backend", map[string]any{"operation_id": operationID, "error": err.Error()})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to create mobile operation"})
	}
	if !createResult.Created && !isMobileOperationTerminal(createResult.Record.Status) {
		_ = a.Store.UpdateMobileOperation(requestContext(c), &store.MobileOperationRecord{
			OperationID:      operationID,
			OrganizationID:   claims.OrgID,
			UserID:           claims.UserID,
			Kind:             createResult.Record.Kind,
			Status:           store.MobileOperationStatusRunning,
			StatusCode:       fiber.StatusAccepted,
			Stage:            "uploading",
			Progress:         progress,
			ChunkIndex:       req.SliceIndex + 1,
			ChunkCount:       req.SliceCount,
			Message:          sql.NullString{String: "audio upload in progress", Valid: true},
			AudioOperationID: createResult.Record.AudioOperationID,
			UpdatedAt:        now,
		})
	}

	logMobileStage(c, route, traceID, "upload_slice_stored", "audio_report_backend", map[string]any{
		"operation_id": operationID,
		"slice_index":  req.SliceIndex,
		"slice_count":  req.SliceCount,
		"slice_final":  req.Final,
		"slice_bytes":  storedBytes,
	})
	if !req.Final {
		record, _ := a.Store.GetMobileOperation(requestContext(c), operationID, claims.OrgID, claims.UserID)
		if record == nil {
			record = createResult.Record
		}
		return c.Status(fiber.StatusAccepted).JSON(mobileOperationResponseFromRecord(record))
	}

	session.mu.Lock()
	session.finalizing = true
	session.mu.Unlock()

	if err := a.Store.UpdateMobileOperation(requestContext(c), &store.MobileOperationRecord{
		OperationID:    operationID,
		OrganizationID: claims.OrgID,
		UserID:         claims.UserID,
		Kind:           "audio_report_email",
		Status:         store.MobileOperationStatusRunning,
		StatusCode:     fiber.StatusAccepted,
		Stage:          "queue",
		Progress:       0.12,
		ChunkIndex:     req.SliceCount,
		ChunkCount:     req.SliceCount,
		Message:        sql.NullString{String: "audio queued for transcription", Valid: true},
		UpdatedAt:      time.Now().UTC(),
	}); err != nil {
		logMobileStage(c, route, traceID, "operation_update_error", "audio_report_backend", map[string]any{"operation_id": operationID, "error": err.Error()})
	}
	logMobileStage(c, route, traceID, "queue_start", "audio_report_backend", map[string]any{
		"operation_id": operationID,
		"slice_count":  req.SliceCount,
	})

	audioRecord, err := a.startMobileDemeterAudioTransportOperation(requestContext(c), logCtx, route, seq, routeMode, audioDurationSec, audioDurationProvided, requestBytes, session, startedAt, claims)
	if err != nil {
		session.mu.Lock()
		session.finalizing = false
		session.mu.Unlock()
		cleanupDemeterAudioTransportSession(logCtx, route, seq, "mobile_transport_start_failed", session)
		demeterAudioTransportSessions.Delete(operationID)
		a.failMobileOperation(requestContext(c), route, actorlessClaims(claims), operationID, fiber.StatusBadGateway, err.Error())
		return c.Status(fiber.StatusBadGateway).JSON(ErrorResponse{Error: "failed to queue transcription"})
	}
	session.mu.Lock()
	session.finalized = true
	session.mu.Unlock()

	if err := a.Store.UpdateMobileOperation(requestContext(c), &store.MobileOperationRecord{
		OperationID:      operationID,
		OrganizationID:   claims.OrgID,
		UserID:           claims.UserID,
		Kind:             "audio_report_email",
		Status:           store.MobileOperationStatusRunning,
		StatusCode:       fiber.StatusAccepted,
		Stage:            "transcription",
		Progress:         0.15,
		ChunkIndex:       audioRecord.ChunkIndex,
		ChunkCount:       audioRecord.ChunkCount,
		Message:          sql.NullString{String: "transcription queued", Valid: true},
		AudioOperationID: sql.NullString{String: audioRecord.OperationID, Valid: true},
		UpdatedAt:        time.Now().UTC(),
	}); err != nil {
		logMobileStage(c, route, traceID, "operation_update_error", "audio_report_backend", map[string]any{"operation_id": operationID, "error": err.Error()})
	}
	logMobileStage(c, route, traceID, "queue_enqueued", "audio_report_backend", map[string]any{
		"operation_id":       operationID,
		"audio_operation_id": audioRecord.OperationID,
		"chunk_count":        audioRecord.ChunkCount,
	})

	actor := mobileActorFromClaims(claims)
	go a.runMobileAudioReportOperation(observability.WithTraceID(context.Background(), traceID), traceID, route, actor, operationID, audioRecord.OperationID, audioReq)

	record, loadErr := a.Store.GetMobileOperation(requestContext(c), operationID, claims.OrgID, claims.UserID)
	if loadErr != nil {
		logMobileStage(c, route, traceID, "operation_load_error", "audio_report_backend", map[string]any{"operation_id": operationID, "error": loadErr.Error()})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load mobile operation"})
	}
	logMobileStage(c, route, traceID, "response_ready", "audio_report_backend", map[string]any{
		"operation_id":       operationID,
		"audio_operation_id": audioRecord.OperationID,
		"status_code":        fiber.StatusAccepted,
		"duration_ms":        time.Since(startedAt).Milliseconds(),
	})
	return c.Status(fiber.StatusAccepted).JSON(mobileOperationResponseFromRecord(record))
}

func (a *App) getMobileOperationStatus(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	traceID := requestTraceID(c)
	logMobileStage(c, route, traceID, "operation_status_request_received", "mobile_operation", nil)
	claims := MustClaims(c)
	if claims == nil {
		logMobileStage(c, route, traceID, "request_unauthorized", "mobile_operation", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	operationID := strings.TrimSpace(c.Params("operationId"))
	if operationID == "" {
		logMobileStage(c, route, traceID, "operation_status_missing", "mobile_operation", nil)
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "missing operation id"})
	}
	record, err := a.Store.GetMobileOperation(requestContext(c), operationID, claims.OrgID, claims.UserID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, store.ErrMobileOperationOwnership) {
			logMobileStage(c, route, traceID, "operation_status_missing", "mobile_operation", map[string]any{"operation_id": operationID})
			return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "operation not found"})
		}
		logMobileStage(c, route, traceID, "operation_status_error", "mobile_operation", map[string]any{"operation_id": operationID, "error": err.Error()})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load mobile operation"})
	}
	logMobileStage(c, route, traceID, "operation_status_ready", "mobile_operation", map[string]any{
		"operation_id": operationID,
		"status":       record.Status,
		"stage":        record.Stage,
		"progress":     record.Progress,
	})
	return c.Status(fiber.StatusOK).JSON(mobileOperationResponseFromRecord(record))
}

func (a *App) runMobileReportEmailOperation(ctx context.Context, traceID string, route string, actor mobileOperationActor, req mobileReportEmailRequest) {
	operationID := strings.TrimSpace(req.OperationID)
	title := normalizeMobileMeetingTitle(req.MeetingTitle)
	logMobileStageCtx(ctx, route, "operation_worker_start", title, map[string]any{"operation_id": operationID, "kind": "report_email"})
	_ = a.updateMobileOperationProgress(ctx, actor, operationID, "generation", 0.2, 0, 0, "generating reports", "")

	response, err := a.generateAndSendMobileReports(ctx, traceID, route, actor, operationID, title, req.Participants, req.SelectedFormats, req.RawTranscriptText, req.EditedTranscriptText, req.SpeakerAssignments, req.TranscriptSegments, req.ReportDetailLevels)
	if err != nil {
		_ = a.recordMobileActivityEvent(actor, "report", mobileReportSourceMode, mobileReportProvider, "error", map[string]any{
			"client":       "mobile",
			"operation_id": operationID,
			"stage":        "generation_email",
			"formats":      []string{"CRI", "CRO", "CRS"},
		})
		a.failMobileOperation(ctx, route, actor, operationID, mobileFailureStatusCode(err), err.Error())
		return
	}
	raw, _ := json.Marshal(response)
	if _, err := a.Store.CompleteMobileOperation(ctx, operationID, actor.OrgID, actor.UserID, fiber.StatusOK, raw, time.Now().UTC()); err != nil {
		logMobileStageCtx(ctx, route, "operation_complete_error", title, map[string]any{"operation_id": operationID, "error": err.Error()})
		return
	}
	logMobileStageCtx(ctx, route, "operation_completed", title, map[string]any{
		"operation_id": operationID,
		"file_count":   len(response.Files),
	})
}

func (a *App) runMobileAudioReportOperation(ctx context.Context, traceID string, route string, actor mobileOperationActor, operationID string, audioOperationID string, req mobileAudioReportRequest) {
	title := normalizeMobileMeetingTitle(req.MeetingTitle)
	logMobileStageCtx(ctx, route, "orchestration_start", title, map[string]any{
		"operation_id":       operationID,
		"audio_operation_id": audioOperationID,
	})
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	deadline := time.NewTimer(3 * time.Hour)
	defer deadline.Stop()

	for {
		select {
		case <-ctx.Done():
			a.failMobileOperation(context.Background(), route, actor, operationID, fiber.StatusGatewayTimeout, "mobile operation cancelled")
			return
		case <-deadline.C:
			a.failMobileOperation(ctx, route, actor, operationID, fiber.StatusGatewayTimeout, "mobile operation timed out")
			return
		case <-ticker.C:
			record, err := a.Store.GetDemeterAudioTranscriptionOperation(ctx, audioOperationID, actor.OrgID, actor.UserID)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					a.failMobileOperation(ctx, route, actor, operationID, fiber.StatusNotFound, "transcription operation not found")
					return
				}
				logMobileStageCtx(ctx, route, "polling_error", title, map[string]any{"operation_id": operationID, "audio_operation_id": audioOperationID, "error": err.Error()})
				continue
			}
			progress := 0.15 + minFloat64(0.55, record.Progress*0.55)
			_ = a.updateMobileOperationProgress(ctx, actor, operationID, "transcription", progress, record.ChunkIndex, record.ChunkCount, "transcription in progress", audioOperationID)
			logMobileStageCtx(ctx, route, "polling_update", title, map[string]any{
				"operation_id":       operationID,
				"audio_operation_id": audioOperationID,
				"status":             record.Status,
				"stage":              record.Stage,
				"progress":           record.Progress,
				"chunk_index":        record.ChunkIndex,
				"chunk_count":        record.ChunkCount,
			})
			switch record.Status {
			case store.DemeterAudioTranscriptionOperationStatusCompleted:
				transcriptionResponse := demeterAudioTranscriptionOperationResponseFromRecord(record).Response
				transcript := ""
				if transcriptionResponse != nil {
					transcript = strings.TrimSpace(transcriptionResponse.Text)
				}
				if transcript == "" {
					a.cleanupConsumedDemeterAudioOperation(ctx, route, 0, "mobile_direct_empty_transcription", record)
					a.failMobileOperation(ctx, route, actor, operationID, fiber.StatusBadGateway, "empty transcription")
					return
				}
				logMobileStageCtx(ctx, route, "transcription_consumed", title, map[string]any{
					"operation_id":       operationID,
					"audio_operation_id": audioOperationID,
					"chunk_count":        len(transcriptionResponse.Chunks),
				})
				a.cleanupConsumedDemeterAudioOperation(ctx, route, 0, "mobile_direct_consumed", record)
				_ = a.recordMobileActivityEvent(actor, "transcription", mobileReportSourceMode, mobileReportProvider, "success", map[string]any{
					"client":             "mobile",
					"operation_id":       operationID,
					"audio_operation_id": audioOperationID,
					"sourceMode":         mobileReportSourceMode,
					"provider":           mobileReportProvider,
					"duration_sec":       transcriptionResponse.Duration,
					"chunk_count":        len(transcriptionResponse.Chunks),
				})
				_ = a.updateMobileOperationProgress(ctx, actor, operationID, "generation", 0.72, record.ChunkIndex, record.ChunkCount, "generating reports", audioOperationID)
				response, err := a.generateAndSendMobileReports(ctx, traceID, route, actor, operationID, title, req.Participants, req.SelectedFormats, transcript, "", nil, nil, req.ReportDetailLevels)
				if err != nil {
					_ = a.recordMobileActivityEvent(actor, "report", mobileReportSourceMode, mobileReportProvider, "error", map[string]any{
						"client":             "mobile",
						"operation_id":       operationID,
						"audio_operation_id": audioOperationID,
						"formats":            []string{"CRI", "CRO", "CRS"},
					})
					a.failMobileOperation(ctx, route, actor, operationID, mobileFailureStatusCode(err), err.Error())
					return
				}
				raw, _ := json.Marshal(response)
				if _, err := a.Store.CompleteMobileOperation(ctx, operationID, actor.OrgID, actor.UserID, fiber.StatusOK, raw, time.Now().UTC()); err != nil {
					logMobileStageCtx(ctx, route, "operation_complete_error", title, map[string]any{"operation_id": operationID, "error": err.Error()})
				}
				logMobileStageCtx(ctx, route, "orchestration_completed", title, map[string]any{"operation_id": operationID, "file_count": len(response.Files)})
				return
			case store.DemeterAudioTranscriptionOperationStatusFailed, store.DemeterAudioTranscriptionOperationStatusCancelled:
				a.cleanupConsumedDemeterAudioOperation(ctx, route, 0, "mobile_direct_failed", record)
				lastError := "transcription failed"
				if record.LastError.Valid && strings.TrimSpace(record.LastError.String) != "" {
					lastError = strings.TrimSpace(record.LastError.String)
				}
				_ = a.recordMobileActivityEvent(actor, "transcription", mobileReportSourceMode, mobileReportProvider, "error", map[string]any{
					"client":             "mobile",
					"operation_id":       operationID,
					"audio_operation_id": audioOperationID,
					"status":             record.Status,
				})
				a.failMobileOperation(ctx, route, actor, operationID, record.StatusCode, lastError)
				return
			}
		}
	}
}

func (a *App) startMobileDemeterAudioTransportOperation(
	ctx context.Context,
	logCtx demeterAudioLogContext,
	route string,
	seq uint64,
	routeMode string,
	audioDurationSec float64,
	audioDurationProvided bool,
	requestBytes int,
	session *demeterAudioTransportSession,
	startedAt time.Time,
	claims *auth.Claims,
) (*store.DemeterAudioTranscriptionOperationRecord, error) {
	if session == nil {
		return nil, fmt.Errorf("missing transport session")
	}
	if claims == nil {
		return nil, fmt.Errorf("missing claims")
	}
	if existing, err := a.Store.GetDemeterAudioTranscriptionOperation(ctx, session.uploadID, claims.OrgID, claims.UserID); err == nil {
		return existing, nil
	} else {
		var ownershipErr *store.DemeterAudioTranscriptionOperationOwnershipError
		if errors.As(err, &ownershipErr) {
			return nil, err
		}
	}

	now := time.Now().UTC()
	initialResponse := &store.DemeterAudioTranscriptionOperationRecord{
		OperationID:    session.uploadID,
		OrganizationID: claims.OrgID,
		UserID:         claims.UserID,
		Status:         store.DemeterAudioTranscriptionOperationStatusRunning,
		Stage:          demeterAudioTransportFinalizationStage,
		ChunkIndex:     0,
		ChunkCount:     0,
		Progress:       0,
		StatusCode:     fiber.StatusAccepted,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := a.Store.CreateDemeterAudioTranscriptionOperation(ctx, initialResponse); err != nil {
		if existing, loadErr := a.Store.GetDemeterAudioTranscriptionOperation(ctx, session.uploadID, claims.OrgID, claims.UserID); loadErr == nil {
			return existing, nil
		}
		return nil, err
	}
	a.runDemeterAudioTransportTranscriptionOperation(ctx, logCtx, nil, session, route, seq, routeMode, audioDurationSec, audioDurationProvided, requestBytes, startedAt)
	record, err := a.Store.GetDemeterAudioTranscriptionOperation(ctx, session.uploadID, claims.OrgID, claims.UserID)
	if err != nil {
		return nil, err
	}
	return record, nil
}

func (a *App) generateAndSendMobileReports(
	ctx context.Context,
	traceID string,
	route string,
	actor mobileOperationActor,
	operationID string,
	title string,
	participants []string,
	selectedFormats []string,
	rawTranscriptText string,
	editedTranscriptText string,
	speakerAssignments []meetingreports.SpeakerAssignment,
	transcriptSegments []meetingreports.TranscriptSegment,
	detailOverrides map[string]string,
) (mobileReportEmailResponse, error) {
	startedAt := time.Now()
	rawTranscript := resolveMobileTranscriptForMail(rawTranscriptText, editedTranscriptText)
	reportSourceText := buildMobileReportSourceText(editedTranscriptText, rawTranscriptText, speakerAssignments)
	if strings.TrimSpace(rawTranscript) == "" || strings.TrimSpace(reportSourceText) == "" {
		return mobileReportEmailResponse{}, fmt.Errorf("transcript text is required")
	}
	formats, err := normalizeMobileSelectedReportFormats(selectedFormats)
	if err != nil {
		return mobileReportEmailResponse{}, err
	}
	participants = normalizeStringList(participants)
	title = normalizeMobileMeetingTitle(title)
	settings, err := a.loadMobileReportSettings(ctx, actor.UserID, detailOverrides)
	if err != nil {
		return mobileReportEmailResponse{}, err
	}
	logMobileStageCtx(ctx, route, "settings_resolved", title, map[string]any{
		"operation_id":        operationID,
		"model_id":            settings.ModelID,
		"temperature":         settings.Temperature,
		"max_tokens":          settings.MaxTokens,
		"provider_max_tokens": mobileReportProviderMaxToken,
		"format_count":        len(formats),
	})

	attachments, reportEnvelopes, generatedAt, err := a.buildMobileDocuments(ctx, traceID, route, actor, operationID, settings, title, participants, rawTranscript, reportSourceText, transcriptSegments, formats)
	if err != nil {
		return mobileReportEmailResponse{}, err
	}
	files := summarizeMobileAttachments(attachments)
	recipient, err := a.resolveMobileRecipientEmail(ctx, actor)
	if err != nil {
		return mobileReportEmailResponse{}, err
	}
	if a.Mailer == nil {
		return mobileReportEmailResponse{}, fmt.Errorf("mailer unavailable")
	}
	if ready, ok := a.Mailer.(interface{ Ready() error }); ok {
		if err := ready.Ready(); err != nil {
			return mobileReportEmailResponse{}, fmt.Errorf("mailer unavailable")
		}
	}
	textBody, htmlBody := buildMobileEmailBodies(title, formats, reportEnvelopes)
	_ = a.updateMobileOperationProgress(ctx, actor, operationID, "email", 0.92, 0, len(formats), "sending email", "")
	logMobileStageCtx(ctx, route, "email_send_start", title, map[string]any{
		"operation_id": operationID,
		"file_count":   len(attachments),
		"formats":      selectedMobileFormatsToStrings(formats),
	})
	if err := a.Mailer.SendMeetingSummaryEmail(ctx, mailer.MeetingSummaryEmail{
		ToEmail:     recipient,
		Subject:     buildMobileSubject(title),
		TextBody:    textBody,
		HTMLBody:    htmlBody,
		Attachments: attachments,
	}); err != nil {
		logMobileStageCtx(ctx, route, "email_send_error", title, map[string]any{"operation_id": operationID, "error": err.Error()})
		return mobileReportEmailResponse{}, fmt.Errorf("failed to send meeting email")
	}
	logMobileStageCtx(ctx, route, "email_send_success", title, map[string]any{
		"operation_id": operationID,
		"file_count":   len(attachments),
		"duration_ms":  time.Since(startedAt).Milliseconds(),
	})
	_ = a.recordMobileActivityEvent(actor, "report", mobileReportSourceMode, mobileReportProvider, "success", map[string]any{
		"client":       "mobile",
		"operation_id": operationID,
		"sourceMode":   mobileReportSourceMode,
		"provider":     mobileReportProvider,
		"formats":      selectedMobileFormatsToStrings(formats),
		"format_count": len(formats),
		"duration_ms":  time.Since(startedAt).Milliseconds(),
	})
	return mobileReportEmailResponse{
		OperationID:  operationID,
		MeetingTitle: title,
		SentTo:       recipient,
		SentToEmails: []string{recipient},
		Files:        files,
		GeneratedAt:  generatedAt,
		SourceMode:   mobileReportSourceMode,
		Provider:     mobileReportProvider,
	}, nil
}

func (a *App) buildMobileDocuments(
	ctx context.Context,
	traceID string,
	route string,
	actor mobileOperationActor,
	operationID string,
	settings mobileReportSettings,
	title string,
	participants []string,
	rawTranscript string,
	reportSourceText string,
	transcriptSegments []meetingreports.TranscriptSegment,
	formats []meetingreports.ReportFormat,
) ([]mailer.MailAttachment, map[meetingreports.ReportFormat]mobileReportEnvelope, string, error) {
	if a.MistralClient == nil || !a.MistralClient.IsConfigured() {
		return nil, nil, "", fmt.Errorf("mistral client is not configured")
	}
	if len(formats) == 0 {
		return nil, nil, "", fmt.Errorf("selected formats are required")
	}
	now := time.Now().UTC()
	generatedAt := now.Format(time.RFC3339)
	reportEnvelopes, err := a.generateMobileReportEnvelopesWithQueue(ctx, traceID, route, operationID, actor.OrgID, actor.UserID, settings, title, participants, reportSourceText, formats)
	if err != nil {
		return nil, nil, "", err
	}

	attachments := make([]mailer.MailAttachment, 0, len(formats)+1)
	transcriptDocx, err := meetingreports.BuildTranscriptDocx(title, participants, mobileReportSourceMode, rawTranscript, transcriptSegments, meetingreports.TranscriptDocxMetadata{
		Title:            title,
		GeneratedAt:      generatedAt,
		SourceMode:       mobileReportSourceMode,
		SourceTokenCount: approximateTokenCount(rawTranscript),
	})
	if err != nil {
		return nil, nil, "", err
	}
	attachments = append(attachments, mailer.MailAttachment{
		Filename:    meetingreports.TranscriptDocxFilename(now),
		ContentType: mailer.DocxContentType,
		Data:        transcriptDocx,
	})
	for _, format := range formats {
		envelope, ok := reportEnvelopes[format]
		if !ok {
			return nil, nil, "", fmt.Errorf("missing report for format %s", meetingreports.ReportFormatDisplayName(format))
		}
		docx, err := meetingreports.BuildReportDocx(envelope.Report, meetingreports.ReportDocxMetadata{
			Format:           format,
			ModelID:          envelope.ModelID,
			GeneratedAt:      envelope.GeneratedAt,
			SourceMode:       envelope.SourceMode,
			SourceTokenCount: envelope.SourceTokenCount,
		})
		if err != nil {
			return nil, nil, "", err
		}
		attachments = append(attachments, mailer.MailAttachment{
			Filename:    meetingreports.ReportDocxFilename(meetingreports.ReportFormatKey(format), now),
			ContentType: mailer.DocxContentType,
			Data:        docx,
		})
	}
	return attachments, reportEnvelopes, generatedAt, nil
}

func (a *App) loadMobileReportSettings(ctx context.Context, userID string, overrides map[string]string) (mobileReportSettings, error) {
	settings := mobileReportSettings{
		ModelID:           meetingreports.DefaultReportModelID,
		Temperature:       0.2,
		MaxTokens:         meetingreports.DefaultReportMaxTokens,
		DetailLevels:      meetingreports.NormalizeReportDetailLevels(nil),
		MonoPassMaxTokens: meetingreports.DefaultReportMaxTokens,
	}
	record, err := a.Store.GetUserSettings(ctx, strings.TrimSpace(userID))
	if err != nil {
		return settings, err
	}
	if record != nil && len(strings.TrimSpace(string(record.Settings))) > 0 {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(record.Settings, &raw); err == nil {
			settings.ModelID = readStringSetting(raw, "llmApiMistralModelId", settings.ModelID)
			settings.Temperature = readFloatSetting(raw, "llmApiMistralTemperature", settings.Temperature)
			settings.MonoPassMaxTokens = readIntSetting(raw, "llmApiReportMonoPassMaxTokens", settings.MonoPassMaxTokens)
			settings.MaxTokens = readIntSetting(raw, "llmApiMistralMaxTokens", settings.MaxTokens)
			if detailRaw, ok := raw["llmApiReportDetailLevels"]; ok {
				settings.DetailLevels = parseMobileDetailLevelRaw(detailRaw, settings.DetailLevels)
			}
		}
	}
	settings.DetailLevels = mergeMobileDetailOverrides(settings.DetailLevels, overrides)
	if settings.MonoPassMaxTokens > 0 {
		settings.MaxTokens = settings.MonoPassMaxTokens
	}
	settings.ModelID = strings.TrimSpace(settings.ModelID)
	if settings.ModelID == "" {
		settings.ModelID = meetingreports.DefaultReportModelID
	}
	if settings.Temperature < 0 || settings.Temperature > 2 {
		settings.Temperature = 0.2
	}
	if settings.MaxTokens <= 0 {
		settings.MaxTokens = meetingreports.DefaultReportMaxTokens
	}
	if settings.MaxTokens > mobileReportProviderMaxToken {
		settings.MaxTokens = mobileReportProviderMaxToken
	}
	return settings, nil
}

func (a *App) cleanupConsumedDemeterAudioOperation(ctx context.Context, route string, seq uint64, cleanupScope string, record *store.DemeterAudioTranscriptionOperationRecord) {
	if record == nil {
		return
	}
	logCtx := newDemeterAudioLogContext(ctx)
	payload, payloadErr := decodeDemeterAudioQueuePayload(record.QueuePayloadJSON)
	if payloadErr == nil && payload != nil {
		sourceDir := strings.TrimSpace(payload.Upload.SourceDir)
		if sourceDir != "" {
			cleanupDemeterAudioTempPath(logCtx, route, seq, cleanupScope, "backend_upload_dir", sourceDir, map[string]any{
				"operation_id": record.OperationID,
			})
		}
	} else if payloadErr != nil {
		logMobileStageCtx(ctx, route, "cleanup_payload_decode_error", "transcription_cleanup", map[string]any{
			"operation_id": record.OperationID,
			"error":        payloadErr.Error(),
		})
	}
	if sessionValue, ok := demeterAudioTransportSessions.LoadAndDelete(record.OperationID); ok {
		if session, ok := sessionValue.(*demeterAudioTransportSession); ok {
			cleanupDemeterAudioTransportSession(logCtx, route, seq, cleanupScope, session)
		}
	}
	startedAt := time.Now()
	deletedCount, deleteErr := a.Store.DeleteDemeterAudioTranscriptionOperation(ctx, record.OperationID)
	fields := map[string]any{
		"operation_id":     record.OperationID,
		"deleted_count":    deletedCount,
		"cleanup_scope":    cleanupScope,
		"duration_ms":      time.Since(startedAt).Milliseconds(),
		"operation_status": record.Status,
	}
	if payloadErr != nil {
		fields["payload_cleanup_error"] = payloadErr.Error()
	}
	if deleteErr != nil {
		fields["message"] = deleteErr.Error()
		logDemeterAudioPerformanceTaskCtx(logCtx, route, seq, "operation_cleanup_failed", "suppression_operation_transcription", fields)
		logMobileStageCtx(ctx, route, "cleanup_error", "transcription_cleanup", map[string]any{
			"operation_id": record.OperationID,
			"error":        deleteErr.Error(),
		})
		return
	}
	logDemeterAudioPerformanceTaskCtx(logCtx, route, seq, "operation_cleanup_completed", "suppression_operation_transcription", fields)
	logMobileStageCtx(ctx, route, "cleanup_completed", "transcription_cleanup", map[string]any{
		"operation_id":  record.OperationID,
		"deleted_count": deletedCount,
	})
}

func mobileOperationResponseFromRecord(record *store.MobileOperationRecord) mobileOperationResponse {
	response := mobileOperationResponse{}
	if record == nil {
		return response
	}
	response.OperationID = record.OperationID
	response.Status = record.Status
	response.StatusCode = record.StatusCode
	response.Stage = record.Stage
	response.Progress = record.Progress
	response.ChunkIndex = record.ChunkIndex
	response.ChunkCount = record.ChunkCount
	response.UpdatedAt = record.UpdatedAt.UTC().Format(time.RFC3339)
	if record.Message.Valid {
		response.Message = strings.TrimSpace(record.Message.String)
	}
	if record.LastError.Valid {
		response.LastError = strings.TrimSpace(record.LastError.String)
	}
	if record.AudioOperationID.Valid {
		response.AudioOperationID = strings.TrimSpace(record.AudioOperationID.String)
	}
	if record.FinishedAt.Valid {
		response.FinishedAt = record.FinishedAt.Time.UTC().Format(time.RFC3339)
	}
	if record.ResponseJSON.Valid && strings.TrimSpace(record.ResponseJSON.String) != "" {
		var completed mobileReportEmailResponse
		if err := json.Unmarshal([]byte(record.ResponseJSON.String), &completed); err == nil {
			response.Files = completed.Files
		}
	}
	return response
}

func (a *App) updateMobileOperationProgress(ctx context.Context, actor mobileOperationActor, operationID string, stage string, progress float64, chunkIndex int, chunkCount int, message string, audioOperationID string) error {
	audioID := sql.NullString{}
	if strings.TrimSpace(audioOperationID) != "" {
		audioID = sql.NullString{String: strings.TrimSpace(audioOperationID), Valid: true}
	} else if existing, err := a.Store.GetMobileOperation(ctx, operationID, actor.OrgID, actor.UserID); err == nil && existing.AudioOperationID.Valid {
		audioID = existing.AudioOperationID
	}
	return a.Store.UpdateMobileOperation(ctx, &store.MobileOperationRecord{
		OperationID:      operationID,
		OrganizationID:   actor.OrgID,
		UserID:           actor.UserID,
		Kind:             "mobile",
		Status:           store.MobileOperationStatusRunning,
		StatusCode:       fiber.StatusAccepted,
		Stage:            stage,
		Progress:         progress,
		ChunkIndex:       chunkIndex,
		ChunkCount:       chunkCount,
		Message:          sql.NullString{String: message, Valid: strings.TrimSpace(message) != ""},
		AudioOperationID: audioID,
		UpdatedAt:        time.Now().UTC(),
	})
}

func (a *App) failMobileOperation(ctx context.Context, route string, actor mobileOperationActor, operationID string, statusCode int, message string) {
	if statusCode <= 0 {
		statusCode = fiber.StatusBadGateway
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = "mobile operation failed"
	}
	body, _ := json.Marshal(ErrorResponse{Error: message})
	if _, err := a.Store.FailMobileOperation(ctx, operationID, actor.OrgID, actor.UserID, statusCode, body, message, time.Now().UTC()); err != nil {
		logMobileStageCtx(ctx, route, "operation_fail_error", "mobile_operation", map[string]any{
			"operation_id": operationID,
			"status_code":  statusCode,
			"error":        err.Error(),
		})
		return
	}
	logMobileStageCtx(ctx, route, "operation_failed", "mobile_operation", map[string]any{
		"operation_id": operationID,
		"status_code":  statusCode,
	})
}

func (a *App) resolveMobileRecipientEmail(ctx context.Context, actor mobileOperationActor) (string, error) {
	candidate := strings.TrimSpace(actor.Email)
	if candidate == "" && a.Store != nil {
		user, err := a.Store.GetUserByID(ctx, actor.UserID)
		if err != nil {
			return "", err
		}
		if user != nil {
			candidate = strings.TrimSpace(user.Email)
		}
	}
	if candidate == "" {
		return "", fmt.Errorf("recipient email unavailable")
	}
	addr, err := mail.ParseAddress(candidate)
	if err != nil {
		return "", fmt.Errorf("invalid recipient email")
	}
	return strings.ToLower(strings.TrimSpace(addr.Address)), nil
}

func (a *App) recordMobileActivityEvent(actor mobileOperationActor, eventKind string, sourceMode string, provider string, status string, meta map[string]any) error {
	if a.Store == nil || strings.TrimSpace(actor.OrgID) == "" || strings.TrimSpace(actor.UserID) == "" {
		return fmt.Errorf("activity store unavailable")
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	_, err = a.Store.IngestActivityEvents(context.Background(), actor.OrgID, actor.UserID, []store.ActivityEventInput{{
		EventID:    uuid.NewString(),
		EventKind:  strings.ToLower(strings.TrimSpace(eventKind)),
		SourceMode: strings.ToLower(strings.TrimSpace(sourceMode)),
		Provider:   strings.ToLower(strings.TrimSpace(provider)),
		Status:     strings.ToLower(strings.TrimSpace(status)),
		OccurredAt: time.Now().UTC(),
		MetaJSON:   metaJSON,
	}})
	return err
}

func logMobileStage(c *fiber.Ctx, route, traceID, step, title string, fields map[string]any) {
	userID, orgID := demeterActorIDs(c)
	ctx := requestContext(c)
	log.Print(observability.FormatStepLine("mobile", route, step, traceID, userID, orgID, title, fields))
	backenderrors.RecordLog(ctx, "mobile", route, step, title, fields)
	backendperformance.RecordLog(ctx, "mobile", route, step, title, fields)
}

func logMobileStageCtx(ctx context.Context, route, step, title string, fields map[string]any) {
	log.Print(observability.FormatStepLine("mobile", route, step, observability.TraceIDFromContext(ctx), observability.DefaultTraceID, observability.DefaultTraceID, title, fields))
	backenderrors.RecordLog(ctx, "mobile", route, step, title, fields)
	backendperformance.RecordLog(ctx, "mobile", route, step, title, fields)
}

func mobileActorFromClaims(claims *auth.Claims) mobileOperationActor {
	if claims == nil {
		return mobileOperationActor{}
	}
	return mobileOperationActor{
		UserID: strings.TrimSpace(claims.UserID),
		OrgID:  strings.TrimSpace(claims.OrgID),
		Email:  strings.TrimSpace(claims.Email),
	}
}

func actorlessClaims(claims *auth.Claims) mobileOperationActor {
	return mobileActorFromClaims(claims)
}

func mobileOperationID(c *fiber.Ctx, preferred string) string {
	if value := strings.TrimSpace(preferred); value != "" {
		return value
	}
	if value := strings.TrimSpace(c.Get("X-Idempotency-Key")); value != "" {
		return value
	}
	return uuid.NewString()
}

func parseMobileAudioReportRequest(c *fiber.Ctx) mobileAudioReportRequest {
	req := mobileAudioReportRequest{
		MeetingTitle: strings.TrimSpace(c.FormValue("meetingTitle")),
	}
	req.Participants = parseMobileParticipants(c.FormValue("participants"))
	if raw := strings.TrimSpace(c.FormValue("selectedFormats")); raw != "" {
		req.SelectedFormats = parseMobileParticipants(raw)
	}
	if raw := strings.TrimSpace(c.FormValue("reportDetailLevels")); raw != "" {
		var parsed map[string]string
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
			req.ReportDetailLevels = parsed
		}
	}
	return req
}

func parseMobileParticipants(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var parsed []string
	if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
		return normalizeStringList(parsed)
	}
	return normalizeStringList(strings.Split(raw, ","))
}

func normalizeMobileMeetingTitle(value string) string {
	title := strings.TrimSpace(value)
	if title == "" {
		return "Reunion " + time.Now().UTC().Format("2006-01-02")
	}
	return title
}

func normalizeStringList(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func resolveMobileSourceText(editedText, rawText string) string {
	if trimmed := strings.TrimSpace(editedText); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(rawText)
}

func resolveMobileTranscriptForMail(rawText, editedText string) string {
	if trimmed := strings.TrimSpace(editedText); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(rawText)
}

func buildMobileReportSourceText(editedText, rawText string, speakerAssignments []meetingreports.SpeakerAssignment) string {
	transcript := resolveMobileSourceText(editedText, rawText)
	parts := make([]string, 0, 2)
	if len(speakerAssignments) > 0 {
		lines := []string{"Assignation des speakers:"}
		for _, assignment := range speakerAssignments {
			speakerID := strings.TrimSpace(assignment.SpeakerID)
			label := joinMobileNameParts(assignment.FirstName, assignment.LastName)
			if label == "" {
				label = speakerID
			}
			if speakerID != "" && label != "" {
				lines = append(lines, fmt.Sprintf("- %s: %s", speakerID, label))
			} else if label != "" {
				lines = append(lines, "- "+label)
			}
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}
	if transcript != "" {
		parts = append(parts, "Transcription:\n"+transcript)
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func joinMobileNameParts(parts ...string) string {
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			filtered = append(filtered, trimmed)
		}
	}
	return strings.Join(filtered, " ")
}

func selectedMobileFormatsToStrings(formats []meetingreports.ReportFormat) []string {
	out := make([]string, 0, len(formats))
	for _, format := range formats {
		out = append(out, string(format))
	}
	return out
}

func buildMobileSubject(title string) string {
	title = normalizeMobileMeetingTitle(title)
	return "Compte rendu Demeter - " + title
}

func buildMobileEmailBodies(title string, formats []meetingreports.ReportFormat, reports map[meetingreports.ReportFormat]mobileReportEnvelope) (string, string) {
	selectedEnvelopes := buildMobileReportEnvelopeList(formats, reports)
	selectedFormats := make([]string, 0, len(selectedEnvelopes))
	for _, envelope := range selectedEnvelopes {
		selectedFormats = append(selectedFormats, envelope.Format)
	}
	highlights := collectMobileHighlights(reports)
	textLines := []string{
		"Bonjour,",
		"",
		fmt.Sprintf("Les comptes rendus \"%s\" sont prets.", title),
		fmt.Sprintf("Documents joints: transcription DOCX, %s.", strings.Join(selectedFormats, ", ")),
		"",
		"Resume:",
	}
	if len(highlights) == 0 {
		textLines = append(textLines, "- Aucun point saillant supplementaire n a ete extrait.")
	} else {
		for _, highlight := range highlights {
			textLines = append(textLines, "- "+highlight)
		}
	}
	textLines = append(textLines, "", "Cordialement,", "Demeter Sante")

	htmlItems := make([]string, 0, len(highlights))
	for _, highlight := range highlights {
		htmlItems = append(htmlItems, "<li>"+html.EscapeString(highlight)+"</li>")
	}
	if len(htmlItems) == 0 {
		htmlItems = append(htmlItems, "<li>Aucun point saillant supplementaire n a ete extrait.</li>")
	}
	htmlBody := "<html><body style=\"font-family:Arial,sans-serif;color:#1f2937;line-height:1.5\">" +
		"<p>Bonjour,</p>" +
		"<p>Les comptes rendus <strong>" + html.EscapeString(title) + "</strong> sont prets.</p>" +
		"<p><strong>Documents joints :</strong> transcription DOCX, " + html.EscapeString(strings.Join(selectedFormats, ", ")) + ".</p>" +
		"<p><strong>Resume :</strong></p><ul>" + strings.Join(htmlItems, "") + "</ul>" +
		"<p>Cordialement,<br/>Demeter Sante</p>" +
		"</body></html>"
	return strings.Join(textLines, "\n"), htmlBody
}

func collectMobileHighlights(reports map[meetingreports.ReportFormat]mobileReportEnvelope) []string {
	highlights := make([]string, 0, 3)
	seen := map[string]struct{}{}
	for _, format := range meetingreports.AllReportFormats() {
		envelope, ok := reports[format]
		if !ok {
			continue
		}
		for _, value := range append(append([]string{}, envelope.Report.KeyPoints...), envelope.Report.ActionItems...) {
			trimmed := strings.TrimSpace(value)
			if trimmed == "" {
				continue
			}
			key := strings.ToLower(trimmed)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			highlights = append(highlights, trimmed)
			if len(highlights) >= 3 {
				return highlights
			}
		}
	}
	return highlights
}

func extractMobileReportFormatsFromMap(reports map[meetingreports.ReportFormat]mobileReportEnvelope) []string {
	out := make([]string, 0, len(reports))
	for _, format := range meetingreports.AllReportFormats() {
		if _, ok := reports[format]; ok {
			out = append(out, string(format))
		}
	}
	return out
}

func buildMobileReportEnvelopeList(formats []meetingreports.ReportFormat, reports map[meetingreports.ReportFormat]mobileReportEnvelope) []mobileReportEnvelope {
	out := make([]mobileReportEnvelope, 0, len(formats))
	for _, format := range formats {
		if envelope, ok := reports[format]; ok {
			out = append(out, envelope)
		}
	}
	return out
}

func summarizeMobileAttachments(attachments []mailer.MailAttachment) []mobileFileResponse {
	out := make([]mobileFileResponse, 0, len(attachments))
	for _, attachment := range attachments {
		out = append(out, mobileFileResponse{
			Filename:    attachment.Filename,
			ContentType: attachment.ContentType,
			SizeBytes:   len(attachment.Data),
		})
	}
	return out
}

func approximateTokenCount(text string) int {
	return len(strings.Fields(strings.TrimSpace(text)))
}

func generatorModelID(generator *meetingreports.Generator) string {
	if generator == nil || strings.TrimSpace(generator.ModelID) == "" {
		return meetingreports.DefaultReportModelID
	}
	return strings.TrimSpace(generator.ModelID)
}

func parseMobileDetailLevelRaw(raw json.RawMessage, fallback map[meetingreports.ReportFormat]meetingreports.ReportDetailLevel) map[meetingreports.ReportFormat]meetingreports.ReportDetailLevel {
	out := meetingreports.NormalizeReportDetailLevels(fallback)
	var values map[string]string
	if err := json.Unmarshal(raw, &values); err != nil {
		return out
	}
	return mergeMobileDetailOverrides(out, values)
}

func mergeMobileDetailOverrides(base map[meetingreports.ReportFormat]meetingreports.ReportDetailLevel, overrides map[string]string) map[meetingreports.ReportFormat]meetingreports.ReportDetailLevel {
	out := meetingreports.NormalizeReportDetailLevels(base)
	for key, value := range overrides {
		format, ok := meetingreports.ParseReportFormat(key)
		if !ok {
			format, ok = meetingreports.ParseReportFormat(strings.ToUpper(key))
		}
		if !ok {
			continue
		}
		level, ok := meetingreports.ParseReportDetailLevel(value)
		if !ok {
			continue
		}
		out[format] = level
	}
	return out
}

func readStringSetting(values map[string]json.RawMessage, key string, fallback string) string {
	raw, ok := values[key]
	if !ok {
		return fallback
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return fallback
	}
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	return fallback
}

func readFloatSetting(values map[string]json.RawMessage, key string, fallback float64) float64 {
	raw, ok := values[key]
	if !ok {
		return fallback
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return fallback
	}
	return value
}

func readIntSetting(values map[string]json.RawMessage, key string, fallback int) int {
	raw, ok := values[key]
	if !ok {
		return fallback
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		var asFloat float64
		if err := json.Unmarshal(raw, &asFloat); err != nil {
			return fallback
		}
		value = int(asFloat)
	}
	if value <= 0 {
		return fallback
	}
	return value
}

func mobileFailureStatusCode(err error) int {
	if err == nil {
		return fiber.StatusInternalServerError
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "mailer unavailable"):
		return fiber.StatusServiceUnavailable
	case strings.Contains(msg, "not configured"):
		return fiber.StatusServiceUnavailable
	case strings.Contains(msg, "selected formats are required"), strings.Contains(msg, "invalid selected format"):
		return fiber.StatusBadRequest
	case strings.Contains(msg, "transcript text is required"), strings.Contains(msg, "invalid recipient"):
		return fiber.StatusBadRequest
	default:
		return fiber.StatusBadGateway
	}
}

func isMobileOperationTerminal(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case store.MobileOperationStatusCompleted, store.MobileOperationStatusFailed, store.MobileOperationStatusCancelled:
		return true
	default:
		return false
	}
}

func minFloat64(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
