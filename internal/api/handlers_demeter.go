package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/textproto"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gofiber/fiber/v2"
)

const (
	demeterModelsUpstreamPath               = "/v1/models"
	demeterChatCompletionsUpstreamPath      = "/v1/chat/completions"
	demeterAudioTranscriptionsUpstreamPath  = "/v1/audio/transcriptions"
	demeterAudioTranscriptionsBackendPath   = "/audio/transcriptions/backend"
	defaultDemeterAudioTranscriptionModelID = "voxtral-mini-latest"
	demeterAudioTranscriptionMaxAttempts    = 5
	demeterAudioTranscriptionBaseDelay      = 2 * time.Second
)

var demeterAudioSequenceCounter uint64

type demeterAudioFileInfo struct {
	FileName  string
	MimeType  string
	SizeBytes int64
}

type demeterAudioValidationError struct {
	code    string
	message string
	file    demeterAudioFileInfo
}

func (e *demeterAudioValidationError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func (a *App) RegisterDemeterRoutes(router fiber.Router) {
	group := router.Group("/providers/demeter-sante", a.AppAuthRequired())
	group.Get("/models", RequireAnyPermission("provider.cloud.demeter_sante", "provider.llm.demeter_sante"), a.demeterModels)
	group.Post("/audio/transcriptions", RequirePermissions("feature.cloudupload", "provider.cloud.demeter_sante"), a.demeterAudioTranscriptions)
	group.Post("/audio/transcriptions/backend", RequirePermissions("feature.cloudupload", "provider.cloud.demeter_sante"), a.demeterAudioTranscriptions)
	group.Get("/audio/transcriptions/backend/operations/:operationId", RequirePermissions("feature.cloudupload", "provider.cloud.demeter_sante"), a.getDemeterAudioTranscriptionOperationStatus)
	group.Delete("/audio/transcriptions/backend/operations/:operationId", RequirePermissions("feature.cloudupload", "provider.cloud.demeter_sante"), a.cancelDemeterAudioTranscriptionOperation)
	group.Post("/chat/completions", RequirePermissions("feature.llmapi", "provider.llm.demeter_sante"), a.demeterChatCompletions)
}

func (a *App) demeterModels(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "demeter", route, "request_received", "models", nil)

	if !a.MistralClient.IsConfigured() {
		logDemeterRelayIssue(c, route, fiber.StatusServiceUnavailable, "mistral client is not configured")
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "mistral is not configured"})
	}
	logAPIStep(c, "demeter", route, "upstream_start", "models", map[string]any{"upstream": demeterModelsUpstreamPath})
	statusCode, body, err := a.MistralClient.DoGet(requestContext(c), demeterModelsUpstreamPath)
	if err != nil {
		logDemeterRelayIssue(c, route, fiber.StatusBadGateway, err.Error())
		return c.Status(fiber.StatusBadGateway).JSON(ErrorResponse{Error: "failed to reach mistral"})
	}
	logDemeterUpstreamStatus(c, route, statusCode)
	logAPIStep(c, "demeter", route, "response_ready", "models", map[string]any{
		"upstream":        demeterModelsUpstreamPath,
		"upstream_status": statusCode,
		"response_bytes":  len(body),
	})
	c.Status(statusCode)
	c.Type("json")
	return c.Send(body)
}

func (a *App) demeterChatCompletions(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	requestBody := c.Body()
	requestBytes := len(requestBody)
	logAPIStep(c, "demeter", route, "request_received", "chat_completions", map[string]any{
		"request_bytes": requestBytes,
	})

	if !a.MistralClient.IsConfigured() {
		logDemeterRelayIssue(c, route, fiber.StatusServiceUnavailable, "mistral client is not configured")
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "mistral is not configured"})
	}
	logAPIStep(c, "demeter", route, "upstream_start", "chat_completions", map[string]any{
		"upstream":      demeterChatCompletionsUpstreamPath,
		"request_bytes": requestBytes,
	})
	statusCode, body, err := a.MistralClient.DoJSON(requestContext(c), fiber.MethodPost, demeterChatCompletionsUpstreamPath, requestBody)
	if err != nil {
		logDemeterRelayIssue(c, route, fiber.StatusBadGateway, err.Error())
		logAPIStep(c, "demeter", route, "upstream_transport_error", "chat_completions", map[string]any{
			"upstream":      demeterChatCompletionsUpstreamPath,
			"request_bytes": requestBytes,
			"message":       err.Error(),
		})
		return c.Status(fiber.StatusBadGateway).JSON(ErrorResponse{Error: "failed to reach mistral"})
	}
	logDemeterUpstreamStatus(c, route, statusCode)
	logAPIStep(c, "demeter", route, "response_ready", "chat_completions", map[string]any{
		"upstream":        demeterChatCompletionsUpstreamPath,
		"upstream_status": statusCode,
		"request_bytes":   requestBytes,
		"response_bytes":  len(body),
	})
	c.Status(statusCode)
	c.Type("json")
	return c.Send(body)
}

func (a *App) demeterAudioTranscriptions(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	startedAt := time.Now()
	seq := nextDemeterAudioSequenceID()
	routeMode := demeterAudioRouteMode(route)
	audioDurationSec, audioDurationProvided := requestDemeterAudioDurationSec(c)
	contentType := strings.TrimSpace(c.Get(fiber.HeaderContentType))
	requestBytes := c.Request().Header.ContentLength()
	if requestBytes < 0 {
		requestBytes = 0
	}

	logDemeterAudioStage(c, route, seq, "front_received", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
		"content_type":  contentType,
		"request_bytes": requestBytes,
	}))

	if !a.MistralClient.IsConfigured() {
		logDemeterRelayIssue(c, route, fiber.StatusServiceUnavailable, "mistral client is not configured")
		logDemeterAudioStage(c, route, seq, "sequence_end", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"result":            "mistral_not_configured",
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
		}))
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "mistral is not configured"})
	}
	if !strings.HasPrefix(contentType, fiber.MIMEMultipartForm) {
		logDemeterRelayIssue(c, route, fiber.StatusBadRequest, "multipart/form-data is required")
		logDemeterAudioStage(c, route, seq, "sequence_end", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"result":            "invalid_content_type",
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
			"content_type":      contentType,
		}))
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "multipart/form-data is required"})
	}

	if routeMode == "backend_direct" {
		return a.demeterAudioTranscriptionsBackendDirect(c, route, seq, startedAt, routeMode, audioDurationSec, audioDurationProvided, requestBytes, contentType)
	}

	requestBody := c.Body()
	requestBytes = len(requestBody)

	normalizedBody, normalizedContentType, fileInfo, err := normalizeDemeterAudioTranscriptionRequest(requestBody, contentType)
	if err != nil {
		var validationErr *demeterAudioValidationError
		if errors.As(err, &validationErr) {
			return a.demeterAudioValidationFailure(c, route, seq, startedAt, requestBytes, contentType, routeMode, audioDurationSec, audioDurationProvided, validationErr)
		}
		logDemeterRelayIssue(c, route, fiber.StatusBadRequest, err.Error())
		logDemeterAudioStage(c, route, seq, "request_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"result":            "invalid_multipart",
			"status_code":       fiber.StatusBadRequest,
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
			"content_type":      contentType,
			"message":           err.Error(),
		}))
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error:   "invalid multipart form",
			Code:    "invalid_multipart",
			TraceID: requestTraceID(c),
			Path:    route,
		})
	}
	requestBody = normalizedBody
	contentType = normalizedContentType
	requestBytes = len(requestBody)

	logDemeterAudioStage(c, route, seq, "upstream_send_start", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
		"upstream":      demeterAudioTranscriptionsUpstreamPath,
		"request_bytes": requestBytes,
		"file_name":     fileInfo.FileName,
		"file_size":     fileInfo.SizeBytes,
		"mime_type":     fileInfo.MimeType,
	}))

	relayResult, err := a.demeterAudioTranscriptionWithRetry(newDemeterAudioLogContextFromFiber(c), route, seq, routeMode, audioDurationSec, audioDurationProvided, requestBody, contentType, requestBytes)
	if err != nil {
		logDemeterRelayIssue(c, route, fiber.StatusBadGateway, err.Error())
		logDemeterAudioStage(c, route, seq, "sequence_end", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"result":               "upstream_transport_error",
			"upstream":             demeterAudioTranscriptionsUpstreamPath,
			"upstream_duration_ms": 0,
			"total_duration_ms":    time.Since(startedAt).Milliseconds(),
			"request_bytes":        requestBytes,
			"message":              err.Error(),
		}))
		return c.Status(fiber.StatusBadGateway).JSON(ErrorResponse{Error: "failed to reach mistral"})
	}

	statusCode := relayResult.statusCode
	responseBody := relayResult.responseBody
	upstreamDurationMs := relayResult.upstreamDurationMs

	logDemeterAudioStage(c, route, seq, "upstream_response_received", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
		"upstream":             demeterAudioTranscriptionsUpstreamPath,
		"upstream_status":      statusCode,
		"upstream_duration_ms": upstreamDurationMs,
		"request_bytes":        requestBytes,
		"response_bytes":       len(responseBody),
		"attempts":             relayResult.attempts,
	}))

	logDemeterAudioStage(c, route, seq, "return_to_front", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
		"upstream":             demeterAudioTranscriptionsUpstreamPath,
		"upstream_status":      statusCode,
		"upstream_duration_ms": upstreamDurationMs,
		"request_bytes":        requestBytes,
		"response_bytes":       len(responseBody),
		"attempts":             relayResult.attempts,
	}))

	logDemeterUpstreamStatus(c, route, statusCode)
	c.Status(statusCode)
	c.Type("json")
	sendErr := c.Send(responseBody)
	result := "ok"
	if sendErr != nil {
		result = "front_send_error"
	} else if statusCode >= fiber.StatusBadRequest {
		result = "upstream_status_error"
	}
	logDemeterAudioStage(c, route, seq, "sequence_end", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
		"result":               result,
		"upstream":             demeterAudioTranscriptionsUpstreamPath,
		"upstream_status":      statusCode,
		"upstream_duration_ms": upstreamDurationMs,
		"total_duration_ms":    time.Since(startedAt).Milliseconds(),
		"request_bytes":        requestBytes,
		"response_bytes":       len(responseBody),
		"attempts":             relayResult.attempts,
	}))
	return sendErr
}

type demeterAudioTranscriptionRelayResult struct {
	statusCode         int
	responseBody       []byte
	upstreamDurationMs int64
	attempts           int
}

func (a *App) demeterAudioTranscriptionWithRetry(logCtx demeterAudioLogContext, route string, seq uint64, routeMode string, audioDurationSec float64, audioDurationProvided bool, requestBody []byte, contentType string, requestBytes int) (demeterAudioTranscriptionRelayResult, error) {
	ctx := logCtx.ctx
	for attempt := 1; attempt <= demeterAudioTranscriptionMaxAttempts; attempt++ {
		upstreamStartedAt := time.Now()
		statusCode, responseBody, err := a.MistralClient.DoMultipart(ctx, demeterAudioTranscriptionsUpstreamPath, requestBody, contentType)
		upstreamDurationMs := time.Since(upstreamStartedAt).Milliseconds()
		if err != nil {
			return demeterAudioTranscriptionRelayResult{}, err
		}
		if shouldRetryDemeterAudioTranscriptionResponse(statusCode, responseBody) {
			delay := demeterAudioTranscriptionRetryDelayForAttempt(attempt)
			fields := demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
				"upstream":             demeterAudioTranscriptionsUpstreamPath,
				"attempt":              attempt,
				"max_attempts":         demeterAudioTranscriptionMaxAttempts,
				"request_bytes":        requestBytes,
				"upstream_status":      statusCode,
				"upstream_duration_ms": upstreamDurationMs,
				"response_bytes":       len(responseBody),
				"retry_delay_ms":       delay.Milliseconds(),
			})
			if demeterAudioTranscriptionResponseIsCapacityExceeded(statusCode, responseBody) {
				fields["reason"] = demeterAudioTranscriptionCapacityErrorReason(statusCode)
				fields["message"] = strings.TrimSpace(string(responseBody))
				if attempt < demeterAudioTranscriptionMaxAttempts {
					fields["action"] = "retry"
					fields["next_attempt"] = attempt + 1
				} else {
					fields["action"] = "exhausted"
				}
				logDemeterAudioStageCtx(logCtx, route, seq, "upstream_capacity_error", fields)
			} else if attempt < demeterAudioTranscriptionMaxAttempts {
				fields["next_attempt"] = attempt + 1
				fields["reason"] = demeterAudioTranscriptionRetryReason(statusCode)
				logDemeterAudioStageCtx(logCtx, route, seq, "upstream_retry", fields)
			}
			if attempt < demeterAudioTranscriptionMaxAttempts {
				if err := sleepContext(ctx, delay); err != nil {
					return demeterAudioTranscriptionRelayResult{}, err
				}
				continue
			}
		}
		return demeterAudioTranscriptionRelayResult{
			statusCode:         statusCode,
			responseBody:       responseBody,
			upstreamDurationMs: upstreamDurationMs,
			attempts:           attempt,
		}, nil
	}
	return demeterAudioTranscriptionRelayResult{}, fmt.Errorf("retry loop exhausted unexpectedly")
}

type demeterMultipartPart struct {
	header textproto.MIMEHeader
	body   []byte
}

func normalizeDemeterAudioTranscriptionRequest(body []byte, contentType string) ([]byte, string, demeterAudioFileInfo, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, "", demeterAudioFileInfo{}, fmt.Errorf("invalid multipart content type: %w", err)
	}
	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return nil, "", demeterAudioFileInfo{}, fmt.Errorf("multipart boundary is missing")
	}

	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	parts := make([]demeterMultipartPart, 0)
	modelSeen := false
	var fileInfo demeterAudioFileInfo
	fileSeen := false

	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", demeterAudioFileInfo{}, fmt.Errorf("failed to read multipart body: %w", err)
		}
		data, err := io.ReadAll(part)
		if err != nil {
			return nil, "", demeterAudioFileInfo{}, fmt.Errorf("failed to read multipart part: %w", err)
		}
		name := strings.TrimSpace(part.FormName())
		if name == "file" {
			fileSeen = true
			fileInfo = demeterAudioFileInfo{
				FileName:  normalizeDemeterAudioFileName(part.FileName()),
				MimeType:  normalizeDemeterAudioMimeType(part.Header.Get("Content-Type"), part.FileName()),
				SizeBytes: int64(len(data)),
			}
			if err := validateDemeterAudioFileInfo(data, fileInfo); err != nil {
				return nil, "", fileInfo, err
			}
		}
		if name == "model" {
			if strings.TrimSpace(string(data)) != "" {
				modelSeen = true
				parts = append(parts, demeterMultipartPart{header: cloneMultipartHeader(part.Header), body: data})
			}
			continue
		}
		parts = append(parts, demeterMultipartPart{header: cloneMultipartHeader(part.Header), body: data})
	}

	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	if !modelSeen {
		if err := writer.WriteField("model", defaultDemeterAudioTranscriptionModelID); err != nil {
			return nil, "", demeterAudioFileInfo{}, fmt.Errorf("failed to inject default model: %w", err)
		}
	}
	if !fileSeen {
		return nil, "", demeterAudioFileInfo{}, fmt.Errorf("multipart file part is missing")
	}
	for _, part := range parts {
		dst, err := writer.CreatePart(part.header)
		if err != nil {
			return nil, "", demeterAudioFileInfo{}, fmt.Errorf("failed to rebuild multipart body: %w", err)
		}
		if _, err := dst.Write(part.body); err != nil {
			return nil, "", demeterAudioFileInfo{}, fmt.Errorf("failed to write multipart body: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", demeterAudioFileInfo{}, fmt.Errorf("failed to finalize multipart body: %w", err)
	}
	return buffer.Bytes(), writer.FormDataContentType(), fileInfo, nil
}

func cloneMultipartHeader(src textproto.MIMEHeader) textproto.MIMEHeader {
	dst := make(textproto.MIMEHeader, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func (a *App) demeterAudioValidationFailure(
	c *fiber.Ctx,
	route string,
	seq uint64,
	startedAt time.Time,
	requestBytes int,
	contentType string,
	routeMode string,
	audioDurationSec float64,
	audioDurationProvided bool,
	validationErr *demeterAudioValidationError,
) error {
	fileInfo := validationErr.file
	statusCode := demeterAudioValidationStatusCode(validationErr.code)
	fields := demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
		"result":            validationErr.code,
		"status_code":       statusCode,
		"total_duration_ms": time.Since(startedAt).Milliseconds(),
		"request_bytes":     requestBytes,
		"content_type":      contentType,
		"file_name":         fileInfo.FileName,
		"file_size_bytes":   fileInfo.SizeBytes,
		"mime_type":         fileInfo.MimeType,
		"message":           validationErr.message,
	})
	logDemeterAudioStage(c, route, seq, "request_failed", fields)
	logDemeterAudioStage(c, route, seq, "sequence_end", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
		"result":            validationErr.code,
		"status_code":       statusCode,
		"total_duration_ms": time.Since(startedAt).Milliseconds(),
		"request_bytes":     requestBytes,
		"content_type":      contentType,
		"file_name":         fileInfo.FileName,
		"file_size_bytes":   fileInfo.SizeBytes,
		"mime_type":         fileInfo.MimeType,
	}))
	fileSizeBytes := fileInfo.SizeBytes
	return c.Status(statusCode).JSON(ErrorResponse{
		Error:         validationErr.message,
		Code:          validationErr.code,
		TraceID:       requestTraceID(c),
		Path:          route,
		FileName:      fileInfo.FileName,
		FileSizeBytes: &fileSizeBytes,
		MimeType:      fileInfo.MimeType,
	})
}

func demeterAudioValidationStatusCode(code string) int {
	switch strings.TrimSpace(strings.ToLower(code)) {
	case "audio_pipeline_unavailable":
		return fiber.StatusServiceUnavailable
	default:
		return fiber.StatusBadRequest
	}
}

func normalizeDemeterAudioFileName(fileName string) string {
	normalized := strings.TrimSpace(fileName)
	if normalized == "" {
		return ""
	}
	return filepath.Base(normalized)
}

func normalizeDemeterAudioMimeType(rawMimeType, fileName string) string {
	normalized := strings.ToLower(strings.TrimSpace(rawMimeType))
	if normalized != "" {
		if mediaType, _, err := mime.ParseMediaType(normalized); err == nil {
			normalized = strings.ToLower(strings.TrimSpace(mediaType))
		}
	}
	if normalized != "" {
		return normalized
	}

	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(fileName), ".")) {
	case "wav", "wave", "x-wav":
		return "audio/wav"
	case "webm":
		return "audio/webm"
	case "ogg", "oga":
		return "audio/ogg"
	case "mp3":
		return "audio/mpeg"
	case "m4a", "mp4":
		return "audio/mp4"
	case "aac":
		return "audio/aac"
	default:
		return ""
	}
}

func validateDemeterAudioFileInfo(data []byte, fileInfo demeterAudioFileInfo) error {
	if len(data) == 0 {
		return &demeterAudioValidationError{
			code:    "empty_audio_file",
			message: "fichier audio vide",
			file:    fileInfo,
		}
	}

	return nil
}

func demeterAudioRouteMode(route string) string {
	normalizedRoute := strings.ToLower(strings.TrimSpace(route))
	if strings.Contains(normalizedRoute, "/backend") {
		return "backend_direct"
	}
	return "relay"
}

func requestDemeterAudioDurationSec(c *fiber.Ctx) (float64, bool) {
	if c == nil {
		return 0, false
	}

	raw := strings.TrimSpace(c.Get("X-Cloud-Audio-Duration-Sec"))
	if raw == "" {
		return 0, false
	}

	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	if value < 0 {
		value = 0
	}
	return value, true
}

func demeterAudioRequestBaseFields(routeMode string, audioDurationSec float64, audioDurationProvided bool, fields map[string]any) map[string]any {
	normalizedRouteMode := strings.TrimSpace(routeMode)
	if normalizedRouteMode == "" {
		normalizedRouteMode = "relay"
	}
	out := map[string]any{
		"route_mode": normalizedRouteMode,
	}
	if audioDurationProvided {
		out["audio_duration_sec"] = audioDurationSec
	}
	for key, value := range fields {
		out[key] = value
	}
	return out
}

func resolveDemeterAudioKind(fileName, mimeType string) string {
	normalizedMime := strings.ToLower(strings.TrimSpace(mimeType))
	extension := strings.ToLower(strings.TrimPrefix(filepath.Ext(fileName), "."))

	switch {
	case strings.HasPrefix(normalizedMime, "audio/wav"), extension == "wav", extension == "wave", extension == "x-wav":
		return "wav"
	case strings.HasPrefix(normalizedMime, "audio/webm"), extension == "webm":
		return "webm"
	case strings.HasPrefix(normalizedMime, "audio/ogg"), extension == "ogg", extension == "oga":
		return "ogg"
	case strings.HasPrefix(normalizedMime, "audio/mpeg"), strings.HasPrefix(normalizedMime, "audio/mp3"), extension == "mp3":
		return "mp3"
	case strings.HasPrefix(normalizedMime, "audio/mp4"), strings.HasPrefix(normalizedMime, "audio/x-m4a"), extension == "m4a", extension == "mp4":
		return "mp4"
	case strings.HasPrefix(normalizedMime, "audio/aac"), extension == "aac":
		return "aac"
	default:
		return ""
	}
}

func isKnownDemeterAudioSignature(data []byte) bool {
	return matchesDemeterAudioSignature(data, "wav") ||
		matchesDemeterAudioSignature(data, "webm") ||
		matchesDemeterAudioSignature(data, "ogg") ||
		matchesDemeterAudioSignature(data, "mp3") ||
		matchesDemeterAudioSignature(data, "mp4") ||
		matchesDemeterAudioSignature(data, "aac")
}

func matchesDemeterAudioSignature(data []byte, kind string) bool {
	switch kind {
	case "wav":
		return len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WAVE"))
	case "webm":
		return len(data) >= 4 && bytes.Equal(data[:4], []byte{0x1A, 0x45, 0xDF, 0xA3})
	case "ogg":
		return len(data) >= 4 && bytes.Equal(data[:4], []byte("OggS"))
	case "mp3":
		return len(data) >= 3 && bytes.Equal(data[:3], []byte("ID3")) ||
			(len(data) >= 2 && data[0] == 0xFF && (data[1]&0xE0) == 0xE0)
	case "mp4":
		return len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp"))
	case "aac":
		return len(data) >= 2 && data[0] == 0xFF && (data[1]&0xF6) == 0xF0
	default:
		return false
	}
}

func nextDemeterAudioSequenceID() uint64 {
	return atomic.AddUint64(&demeterAudioSequenceCounter, 1)
}

func logDemeterAudioStage(c *fiber.Ctx, route string, seq uint64, stage string, fields map[string]any) {
	logDemeterAudioStageCtx(newDemeterAudioLogContextFromFiber(c), route, seq, stage, fields)
}

func logDemeterRelayIssue(c *fiber.Ctx, route string, status int, message string) {
	logDemeterRelayIssueCtx(newDemeterAudioLogContextFromFiber(c), route, status, message)
}

func logDemeterUpstreamStatus(c *fiber.Ctx, route string, status int) {
	logDemeterUpstreamStatusCtx(newDemeterAudioLogContextFromFiber(c), route, status)
}

func shouldRetryDemeterAudioTranscriptionStatus(status int) bool {
	return shouldRetryDemeterAudioTranscriptionResponse(status, nil)
}

func shouldRetryDemeterAudioTranscriptionResponse(status int, responseBody []byte) bool {
	if demeterAudioTranscriptionResponseIsCapacityExceeded(status, responseBody) {
		return true
	}
	return status >= fiber.StatusInternalServerError && status < 600
}

func demeterAudioTranscriptionResponseIsCapacityExceeded(status int, responseBody []byte) bool {
	switch status {
	case fiber.StatusTooManyRequests, fiber.StatusServiceUnavailable:
		return true
	}
	if len(responseBody) == 0 {
		return false
	}
	normalizedBody := strings.ToLower(strings.TrimSpace(string(responseBody)))
	if normalizedBody == "" {
		return false
	}
	return strings.Contains(normalizedBody, "service tier capacity exceeded")
}

func demeterAudioTranscriptionRetryDelayForAttempt(attempt int) time.Duration {
	if attempt <= 1 {
		return demeterAudioTranscriptionBaseDelay
	}
	delay := demeterAudioTranscriptionBaseDelay
	for i := 1; i < attempt; i++ {
		delay *= 2
	}
	return delay
}

func demeterAudioTranscriptionCapacityErrorReason(status int) string {
	switch status {
	case fiber.StatusTooManyRequests:
		return "upstream_429"
	case fiber.StatusServiceUnavailable:
		return "upstream_503"
	default:
		return "service_tier_capacity_exceeded"
	}
}

func demeterAudioTranscriptionRetryReason(status int) string {
	switch status {
	case fiber.StatusTooManyRequests:
		return "upstream_429"
	case fiber.StatusServiceUnavailable:
		return "upstream_503"
	default:
		return "upstream_5xx"
	}
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func demeterActorIDs(c *fiber.Ctx) (string, string) {
	claims := MustClaims(c)
	if claims == nil {
		return "-", "-"
	}
	userID := strings.TrimSpace(claims.UserID)
	orgID := strings.TrimSpace(claims.OrgID)
	if userID == "" {
		userID = "-"
	}
	if orgID == "" {
		orgID = "-"
	}
	return userID, orgID
}
