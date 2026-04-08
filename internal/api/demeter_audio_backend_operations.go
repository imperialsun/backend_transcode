package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"time"

	"demeter-backend/internal/observability"
	"demeter-backend/internal/requestmeta"
	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

const demeterAudioTranscriptionOperationStatusRoute = "/api/v1/providers/demeter-sante/audio/transcriptions/operations/:operationId"

var demeterAudioTranscriptionOperationCancels sync.Map

type demeterAudioTranscriptionOperationResponse struct {
	OperationID string                               `json:"operationId"`
	Status      string                               `json:"status"`
	StatusCode  int                                  `json:"statusCode"`
	Stage       string                               `json:"stage"`
	ChunkIndex  int                                  `json:"chunkIndex"`
	ChunkCount  int                                  `json:"chunkCount"`
	Progress    float64                              `json:"progress"`
	PartialText string                               `json:"partialText,omitempty"`
	LastError   string                               `json:"lastError,omitempty"`
	UpdatedAt   string                               `json:"updatedAt,omitempty"`
	FinishedAt  string                               `json:"finishedAt,omitempty"`
	Response    *demeterBackendTranscriptionResponse `json:"response,omitempty"`
}

type demeterAudioTranscriptionOperationStartResponse struct {
	demeterAudioTranscriptionOperationResponse
}

func demeterAudioTranscriptionOperationResponseFromRecord(record *store.DemeterAudioTranscriptionOperationRecord) demeterAudioTranscriptionOperationResponse {
	response := demeterAudioTranscriptionOperationResponse{}
	if record == nil {
		return response
	}
	response.OperationID = record.OperationID
	response.Status = record.Status
	response.StatusCode = record.StatusCode
	response.Stage = record.Stage
	response.ChunkIndex = record.ChunkIndex
	response.ChunkCount = record.ChunkCount
	response.Progress = record.Progress
	response.UpdatedAt = record.UpdatedAt.UTC().Format(time.RFC3339)
	if strings.TrimSpace(record.PartialText.String) != "" {
		response.PartialText = strings.TrimSpace(record.PartialText.String)
	}
	if record.LastError.Valid && strings.TrimSpace(record.LastError.String) != "" {
		response.LastError = strings.TrimSpace(record.LastError.String)
	}
	if record.FinishedAt.Valid {
		response.FinishedAt = record.FinishedAt.Time.UTC().Format(time.RFC3339)
	}
	if record.ResponseJSON.Valid && strings.TrimSpace(record.ResponseJSON.String) != "" {
		var backendResponse demeterBackendTranscriptionResponse
		if err := json.Unmarshal([]byte(record.ResponseJSON.String), &backendResponse); err == nil {
			response.Response = &backendResponse
		}
	}
	return response
}

func (a *App) startDemeterAudioTranscriptionOperation(
	c *fiber.Ctx,
	logCtx demeterAudioLogContext,
	route string,
	seq uint64,
	routeMode string,
	audioDurationSec float64,
	audioDurationProvided bool,
	requestBytes int,
	upload *demeterBackendAudioUpload,
	chunkPlans []demeterBackendChunkPlan,
	startedAt time.Time,
	operationID string,
) error {
	claims := MustClaims(c)
	if claims == nil {
		logDemeterRelayIssueCtx(logCtx, route, fiber.StatusUnauthorized, "missing claims")
		logDemeterAudioStageCtx(logCtx, route, seq, "sequence_end", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"result":            "unauthorized",
			"status_code":       fiber.StatusUnauthorized,
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
		}))
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized", Code: "unauthorized", TraceID: requestTraceID(c), Path: route})
	}

	operationID = cloneDemeterRequestString(operationID)
	var existingOwnershipErr *store.DemeterAudioTranscriptionOperationOwnershipError
	if operationID != "" {
		if existing, err := a.Store.GetDemeterAudioTranscriptionOperation(requestContext(c), operationID, claims.OrgID, claims.UserID); err == nil {
			return c.Status(existing.StatusCode).JSON(demeterAudioTranscriptionOperationStartResponse{
				demeterAudioTranscriptionOperationResponse: demeterAudioTranscriptionOperationResponseFromRecord(existing),
			})
		} else if errors.As(err, &existingOwnershipErr) {
			logDemeterOwnershipStageCtx(logCtx, route, seq, "ownership_start_error", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
				"operation_id":    operationID,
				"status_code":     fiber.StatusInternalServerError,
				"reason":          existingOwnershipErr.Reason,
				"source":          "api_start",
				"request_user_id": claims.UserID,
				"request_org_id":  claims.OrgID,
				"stored_user_id":  existingOwnershipErr.StoredUserID,
				"stored_org_id":   existingOwnershipErr.StoredOrganizationID,
			}))
		}
	}
	if operationID == "" {
		operationID = "demeter-audio-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	now := time.Now().UTC()
	initialResponse := &store.DemeterAudioTranscriptionOperationRecord{
		OperationID:    operationID,
		OrganizationID: claims.OrgID,
		UserID:         claims.UserID,
		Status:         store.DemeterAudioTranscriptionOperationStatusRunning,
		Stage:          "queued",
		ChunkIndex:     0,
		ChunkCount:     len(chunkPlans),
		Progress:       0,
		PartialText:    sql.NullString{String: "", Valid: false},
		StatusCode:     fiber.StatusAccepted,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := a.Store.CreateDemeterAudioTranscriptionOperation(requestContext(c), initialResponse); err != nil {
		if existingOwnershipErr != nil {
			logDemeterOwnershipStageCtx(logCtx, route, seq, "ownership_start_error", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
				"operation_id":      operationID,
				"reason":            "ownership_mismatch",
				"source":            "api_start",
				"status_code":       fiber.StatusInternalServerError,
				"request_user_id":   claims.UserID,
				"request_org_id":    claims.OrgID,
				"stored_user_id":    existingOwnershipErr.StoredUserID,
				"stored_org_id":     existingOwnershipErr.StoredOrganizationID,
				"message":           err.Error(),
				"total_duration_ms": time.Since(startedAt).Milliseconds(),
				"request_bytes":     requestBytes,
			}))
			logDemeterAudioStageCtx(logCtx, route, seq, "sequence_end", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
				"result":            "operation_create_error",
				"reason":            "ownership_mismatch",
				"status_code":       fiber.StatusInternalServerError,
				"operation_id":      operationID,
				"total_duration_ms": time.Since(startedAt).Milliseconds(),
				"request_bytes":     requestBytes,
				"message":           err.Error(),
			}))
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to create backend transcription operation"})
		}
		if existing, loadErr := a.Store.GetDemeterAudioTranscriptionOperation(requestContext(c), operationID, claims.OrgID, claims.UserID); loadErr == nil {
			return c.Status(existing.StatusCode).JSON(demeterAudioTranscriptionOperationStartResponse{
				demeterAudioTranscriptionOperationResponse: demeterAudioTranscriptionOperationResponseFromRecord(existing),
			})
		}
		logDemeterRelayIssueCtx(logCtx, route, fiber.StatusInternalServerError, err.Error())
		logDemeterAudioStageCtx(logCtx, route, seq, "sequence_end", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"result":            "operation_create_error",
			"status_code":       fiber.StatusInternalServerError,
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
			"message":           err.Error(),
		}))
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to create backend transcription operation"})
	}

	workerBaseCtx := requestmeta.WithActor(observability.WithTraceID(context.Background(), requestTraceID(c)), claims.UserID, claims.OrgID)
	workerCtx, cancel := context.WithCancel(workerBaseCtx)
	demeterAudioTranscriptionOperationCancels.Store(operationID, cancel)

	go a.runDemeterAudioTranscriptionOperation(
		workerCtx,
		logCtx,
		cancel,
		operationID,
		route,
		seq,
		routeMode,
		audioDurationSec,
		audioDurationProvided,
		requestBytes,
		upload,
		chunkPlans,
	)

	logDemeterAudioStageCtx(logCtx, route, seq, "operation_started", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
		"operation_id":      operationID,
		"chunk_count":       len(chunkPlans),
		"total_duration_ms": time.Since(startedAt).Milliseconds(),
		"request_bytes":     requestBytes,
	}))
	logDemeterAudioStageCtx(logCtx, route, seq, "sequence_end", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
		"result":            "accepted",
		"status_code":       fiber.StatusAccepted,
		"operation_id":      operationID,
		"chunk_count":       len(chunkPlans),
		"total_duration_ms": time.Since(startedAt).Milliseconds(),
		"request_bytes":     requestBytes,
	}))

	return c.Status(fiber.StatusAccepted).JSON(demeterAudioTranscriptionOperationStartResponse{
		demeterAudioTranscriptionOperationResponse: demeterAudioTranscriptionOperationResponse{
			OperationID: operationID,
			Status:      store.DemeterAudioTranscriptionOperationStatusRunning,
			StatusCode:  fiber.StatusAccepted,
			Stage:       "queued",
			ChunkIndex:  0,
			ChunkCount:  len(chunkPlans),
			Progress:    0,
			UpdatedAt:   now.Format(time.RFC3339),
		},
	})
}

func (a *App) getDemeterAudioTranscriptionOperationStatus(c *fiber.Ctx) error {
	logCtx := newDemeterAudioLogContextFromFiber(c)
	traceID := requestTraceID(c)
	route := requestRoutePath(c)
	logDemeterAudioStageCtx(logCtx, route, 0, "operation_status_request_received", nil)

	claims := MustClaims(c)
	if claims == nil {
		logDemeterAudioStageCtx(logCtx, route, 0, "request_unauthorized", map[string]any{
			"error": "unauthorized",
		})
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}

	operationID := strings.TrimSpace(c.Params("operationId"))
	if operationID == "" {
		logDemeterAudioStageCtx(logCtx, route, 0, "operation_status_missing", map[string]any{
			"error":       "missing operation id",
			"status_code": fiber.StatusBadRequest,
		})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "missing operation id"})
	}

	record, err := a.Store.GetDemeterAudioTranscriptionOperation(requestContext(c), operationID, claims.OrgID, claims.UserID)
	if err != nil {
		var ownershipErr *store.DemeterAudioTranscriptionOperationOwnershipError
		if errors.As(err, &ownershipErr) {
			logDemeterOwnershipStageCtx(logCtx, route, 0, "ownership_status_error", map[string]any{
				"operation_id":    operationID,
				"status_code":     fiber.StatusNotFound,
				"reason":          ownershipErr.Reason,
				"source":          "api_status",
				"request_user_id": claims.UserID,
				"request_org_id":  claims.OrgID,
				"stored_user_id":  ownershipErr.StoredUserID,
				"stored_org_id":   ownershipErr.StoredOrganizationID,
			})
			return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "operation not found"})
		}
		if errors.Is(err, sql.ErrNoRows) {
			logDemeterAudioStageCtx(logCtx, route, 0, "operation_status_missing", map[string]any{
				"operation_id": operationID,
				"reason":       "not_found",
				"status_code":  fiber.StatusNotFound,
			})
			return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "operation not found"})
		}
		logDemeterAudioStageCtx(logCtx, route, 0, "operation_status_error", map[string]any{
			"operation_id": operationID,
			"error":        err.Error(),
			"status_code":  fiber.StatusInternalServerError,
		})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load transcription status", TraceID: traceID, Path: route})
	}

	logDemeterAudioStageCtx(logCtx, route, 0, "operation_status_ready", map[string]any{
		"operation_id": operationID,
		"status":       record.Status,
		"stage":        record.Stage,
		"chunk_index":  record.ChunkIndex,
		"chunk_count":  record.ChunkCount,
	})
	return c.Status(fiber.StatusOK).JSON(demeterAudioTranscriptionOperationResponseFromRecord(record))
}

func (a *App) cancelDemeterAudioTranscriptionOperation(c *fiber.Ctx) error {
	logCtx := newDemeterAudioLogContextFromFiber(c)
	traceID := requestTraceID(c)
	route := requestRoutePath(c)
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized", TraceID: traceID, Path: route})
	}

	operationID := strings.TrimSpace(c.Params("operationId"))
	if operationID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "missing operation id", TraceID: traceID, Path: route})
	}

	record, err := a.Store.CancelDemeterAudioTranscriptionOperation(requestContext(c), operationID, claims.OrgID, claims.UserID, time.Now().UTC())
	if err != nil {
		var ownershipErr *store.DemeterAudioTranscriptionOperationOwnershipError
		if errors.As(err, &ownershipErr) {
			logDemeterOwnershipStageCtx(logCtx, route, 0, "ownership_cancel_error", map[string]any{
				"operation_id":    operationID,
				"status_code":     fiber.StatusNotFound,
				"reason":          ownershipErr.Reason,
				"source":          "api_cancel",
				"request_user_id": claims.UserID,
				"request_org_id":  claims.OrgID,
				"stored_user_id":  ownershipErr.StoredUserID,
				"stored_org_id":   ownershipErr.StoredOrganizationID,
			})
			return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "operation not found", TraceID: traceID, Path: requestRoutePath(c)})
		}
		if errors.Is(err, sql.ErrNoRows) {
			return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "operation not found", TraceID: traceID, Path: requestRoutePath(c)})
		}
		logDemeterAudioStageCtx(logCtx, route, 0, "operation_cancel_error", map[string]any{
			"operation_id": operationID,
			"error":        err.Error(),
		})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to cancel backend transcription operation", TraceID: traceID, Path: route})
	}

	if cancelValue, ok := demeterAudioTranscriptionOperationCancels.Load(operationID); ok {
		if cancel, ok := cancelValue.(context.CancelFunc); ok {
			cancel()
		}
		demeterAudioTranscriptionOperationCancels.Delete(operationID)
	}

	return c.Status(fiber.StatusOK).JSON(demeterAudioTranscriptionOperationResponse{
		OperationID: record.OperationID,
		Status:      record.Status,
		StatusCode:  record.StatusCode,
		Stage:       record.Stage,
		ChunkIndex:  record.ChunkIndex,
		ChunkCount:  record.ChunkCount,
		Progress:    record.Progress,
		PartialText: strings.TrimSpace(record.PartialText.String),
		LastError:   strings.TrimSpace(record.LastError.String),
		UpdatedAt:   record.UpdatedAt.UTC().Format(time.RFC3339),
		FinishedAt: func() string {
			if record.FinishedAt.Valid {
				return record.FinishedAt.Time.UTC().Format(time.RFC3339)
			}
			return ""
		}(),
	})
}

func (a *App) runDemeterAudioTranscriptionOperation(
	ctx context.Context,
	baseLogCtx demeterAudioLogContext,
	cancel context.CancelFunc,
	operationID string,
	route string,
	seq uint64,
	routeMode string,
	audioDurationSec float64,
	audioDurationProvided bool,
	requestBytes int,
	upload *demeterBackendAudioUpload,
	chunkPlans []demeterBackendChunkPlan,
) {
	defer cancel()
	defer demeterAudioTranscriptionOperationCancels.Delete(operationID)
	defer upload.cleanup()

	logCtx := demeterAudioLogContext{
		ctx:     ctx,
		traceID: baseLogCtx.traceID,
		userID:  baseLogCtx.userID,
		orgID:   baseLogCtx.orgID,
	}
	logDemeterAudioStageCtx(logCtx, route, seq, "operation_worker_start", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
		"operation_id":  operationID,
		"chunk_count":   len(chunkPlans),
		"request_bytes": requestBytes,
	}))

	var lastResponse demeterBackendTranscriptionResponse
	progressUpdater := func(completedChunks int, chunkCount int, response *demeterBackendTranscriptionResponse) {
		if response == nil {
			return
		}
		lastResponse = *response
		text := strings.TrimSpace(response.Text)
		progress := 0.0
		if chunkCount > 0 {
			progress = math.Min(1, math.Max(0, float64(completedChunks)/float64(chunkCount)))
		}
		record := &store.DemeterAudioTranscriptionOperationRecord{
			OperationID:    operationID,
			OrganizationID: baseLogCtx.orgID,
			UserID:         baseLogCtx.userID,
			Status:         store.DemeterAudioTranscriptionOperationStatusRunning,
			Stage:          "chunk_completed",
			ChunkIndex:     completedChunks,
			ChunkCount:     chunkCount,
			Progress:       progress,
			PartialText:    sql.NullString{String: text, Valid: text != ""},
			StatusCode:     fiber.StatusAccepted,
			UpdatedAt:      time.Now().UTC(),
		}
		if raw, err := json.Marshal(response); err == nil {
			record.ResponseJSON = sql.NullString{String: string(raw), Valid: true}
		}
		fallbackUsed, err := a.updateDemeterAudioTranscriptionOperationStateWithFallback(ctx, record)
		if fallbackUsed {
			logDemeterOwnershipStageCtx(logCtx, route, seq, "ownership_fallback_used", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
				"operation_id": record.OperationID,
				"stage":        record.Stage,
				"status":       record.Status,
				"status_code":  record.StatusCode,
				"chunk_index":  completedChunks,
				"chunk_count":  chunkCount,
				"progress":     progress,
				"source":       "worker_update",
			}))
		}
		if err != nil {
			logDemeterAudioStageCtx(logCtx, route, seq, "operation_progress_update_error", map[string]any{
				"operation_id": operationID,
				"error":        err.Error(),
				"chunk_index":  completedChunks,
				"chunk_count":  chunkCount,
			})
			return
		}
		logDemeterAudioStageCtx(logCtx, route, seq, "operation_progress_update", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"operation_id": operationID,
			"chunk_index":  completedChunks,
			"chunk_count":  chunkCount,
			"progress":     progress,
		}))
	}

	response, statusCode, _, upstreamDurationMs, err := a.demeterBackendTranscribeChunks(
		logCtx,
		route,
		seq,
		routeMode,
		audioDurationSec,
		audioDurationProvided,
		upload,
		chunkPlans,
		progressUpdater,
	)
	now := time.Now().UTC()
	if err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			if existing, loadErr := a.Store.GetDemeterAudioTranscriptionOperation(ctx, operationID, baseLogCtx.orgID, baseLogCtx.userID); loadErr == nil && existing.Status == store.DemeterAudioTranscriptionOperationStatusCancelled {
				logDemeterAudioStageCtx(logCtx, route, seq, "operation_cancelled", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
					"operation_id": operationID,
					"chunk_count":  len(chunkPlans),
				}))
				return
			}
			logDemeterAudioStageCtx(logCtx, route, seq, "operation_cancelled", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
				"operation_id": operationID,
				"chunk_count":  len(chunkPlans),
			}))
			return
		}

		lastError := err.Error()
		progress := 0.0
		if len(chunkPlans) > 0 {
			progress = math.Min(1, math.Max(0, float64(len(lastResponse.Chunks))/float64(len(chunkPlans))))
		}
		if lastResponse.Text != "" || len(lastResponse.Chunks) > 0 {
			if raw, marshalErr := json.Marshal(lastResponse); marshalErr == nil {
				fallbackUsed, updateErr := a.updateDemeterAudioTranscriptionOperationStateWithFallback(ctx, &store.DemeterAudioTranscriptionOperationRecord{
					OperationID:    operationID,
					OrganizationID: baseLogCtx.orgID,
					UserID:         baseLogCtx.userID,
					Status:         store.DemeterAudioTranscriptionOperationStatusFailed,
					Stage:          "failed",
					ChunkIndex:     len(lastResponse.Chunks),
					ChunkCount:     len(chunkPlans),
					Progress:       progress,
					PartialText:    sql.NullString{String: strings.TrimSpace(lastResponse.Text), Valid: strings.TrimSpace(lastResponse.Text) != ""},
					ResponseJSON:   sql.NullString{String: string(raw), Valid: true},
					LastError:      sql.NullString{String: lastError, Valid: true},
					StatusCode:     statusCode,
					UpdatedAt:      now,
					FinishedAt:     sql.NullTime{Time: now, Valid: true},
				})
				if fallbackUsed {
					logDemeterOwnershipStageCtx(logCtx, route, seq, "ownership_fallback_used", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
						"operation_id": operationID,
						"stage":        "failed",
						"status":       store.DemeterAudioTranscriptionOperationStatusFailed,
						"status_code":  statusCode,
						"chunk_index":  len(lastResponse.Chunks),
						"chunk_count":  len(chunkPlans),
						"source":       "worker_update",
					}))
				}
				_ = updateErr
			}
		} else {
			fallbackUsed, updateErr := a.updateDemeterAudioTranscriptionOperationStateWithFallback(ctx, &store.DemeterAudioTranscriptionOperationRecord{
				OperationID:    operationID,
				OrganizationID: baseLogCtx.orgID,
				UserID:         baseLogCtx.userID,
				Status:         store.DemeterAudioTranscriptionOperationStatusFailed,
				Stage:          "failed",
				ChunkIndex:     0,
				ChunkCount:     len(chunkPlans),
				Progress:       0,
				LastError:      sql.NullString{String: lastError, Valid: true},
				StatusCode:     statusCode,
				UpdatedAt:      now,
				FinishedAt:     sql.NullTime{Time: now, Valid: true},
			})
			if fallbackUsed {
				logDemeterOwnershipStageCtx(logCtx, route, seq, "ownership_fallback_used", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
					"operation_id": operationID,
					"stage":        "failed",
					"status":       store.DemeterAudioTranscriptionOperationStatusFailed,
					"status_code":  statusCode,
					"chunk_index":  0,
					"chunk_count":  len(chunkPlans),
					"source":       "worker_update",
				}))
			}
			_ = updateErr
		}
		logDemeterAudioStageCtx(logCtx, route, seq, "operation_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"operation_id":         operationID,
			"status_code":          statusCode,
			"upstream_duration_ms": upstreamDurationMs,
			"total_duration_ms":    0,
			"chunk_count":          len(chunkPlans),
			"message":              lastError,
		}))
		return
	}

	if raw, marshalErr := json.Marshal(response); marshalErr == nil {
		fallbackUsed, updateErr := a.updateDemeterAudioTranscriptionOperationStateWithFallback(ctx, &store.DemeterAudioTranscriptionOperationRecord{
			OperationID:    operationID,
			OrganizationID: baseLogCtx.orgID,
			UserID:         baseLogCtx.userID,
			Status:         store.DemeterAudioTranscriptionOperationStatusCompleted,
			Stage:          "completed",
			ChunkIndex:     len(response.Chunks),
			ChunkCount:     len(chunkPlans),
			Progress:       1,
			PartialText:    sql.NullString{String: strings.TrimSpace(response.Text), Valid: strings.TrimSpace(response.Text) != ""},
			ResponseJSON:   sql.NullString{String: string(raw), Valid: true},
			StatusCode:     fiber.StatusOK,
			UpdatedAt:      now,
			FinishedAt:     sql.NullTime{Time: now, Valid: true},
		})
		if fallbackUsed {
			logDemeterOwnershipStageCtx(logCtx, route, seq, "ownership_fallback_used", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
				"operation_id": operationID,
				"stage":        "completed",
				"status":       store.DemeterAudioTranscriptionOperationStatusCompleted,
				"status_code":  fiber.StatusOK,
				"chunk_index":  len(response.Chunks),
				"chunk_count":  len(chunkPlans),
				"source":       "worker_update",
			}))
		}
		_ = updateErr
	}

	logDemeterAudioStageCtx(logCtx, route, seq, "operation_completed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
		"operation_id":         operationID,
		"status_code":          fiber.StatusOK,
		"upstream_duration_ms": upstreamDurationMs,
		"total_duration_ms":    0,
		"chunk_count":          len(chunkPlans),
		"segments_count": func() int {
			total := 0
			for _, chunk := range response.Chunks {
				total += len(chunk.Segments)
			}
			return total
		}(),
		"response_bytes": func() int { raw, _ := json.Marshal(response); return len(raw) }(),
	}))
}
