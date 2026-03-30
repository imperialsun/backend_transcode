package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gofiber/fiber/v2"
)

const (
	demeterModelsUpstreamPath               = "/v1/models"
	demeterChatCompletionsUpstreamPath      = "/v1/chat/completions"
	demeterAudioTranscriptionsUpstreamPath  = "/v1/audio/transcriptions"
	defaultDemeterAudioTranscriptionModelID = "voxtral-mini-latest"
	demeterAudioTranscriptionMaxAttempts    = 2
	demeterAudioTranscriptionRetryDelay     = 250 * time.Millisecond
)

var demeterAudioSequenceCounter uint64

func (a *App) RegisterDemeterRoutes(router fiber.Router) {
	group := router.Group("/providers/demeter-sante", a.AppAuthRequired())
	group.Get("/models", RequireAnyPermission("provider.cloud.demeter_sante", "provider.llm.demeter_sante"), a.demeterModels)
	group.Post("/audio/transcriptions", RequirePermissions("feature.cloudupload", "provider.cloud.demeter_sante"), a.demeterAudioTranscriptions)
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
	contentType := strings.TrimSpace(c.Get(fiber.HeaderContentType))
	requestBody := c.Body()
	requestBytes := len(requestBody)

	logDemeterAudioStage(c, route, seq, "front_received", map[string]any{
		"content_type":  contentType,
		"request_bytes": requestBytes,
	})

	if !a.MistralClient.IsConfigured() {
		logDemeterRelayIssue(c, route, fiber.StatusServiceUnavailable, "mistral client is not configured")
		logDemeterAudioStage(c, route, seq, "sequence_end", map[string]any{
			"result":            "mistral_not_configured",
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
		})
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "mistral is not configured"})
	}
	if !strings.HasPrefix(contentType, fiber.MIMEMultipartForm) {
		logDemeterRelayIssue(c, route, fiber.StatusBadRequest, "multipart/form-data is required")
		logDemeterAudioStage(c, route, seq, "sequence_end", map[string]any{
			"result":            "invalid_content_type",
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
			"content_type":      contentType,
		})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "multipart/form-data is required"})
	}

	normalizedBody, normalizedContentType, err := normalizeDemeterAudioTranscriptionRequest(requestBody, contentType)
	if err != nil {
		logDemeterRelayIssue(c, route, fiber.StatusBadRequest, err.Error())
		logDemeterAudioStage(c, route, seq, "sequence_end", map[string]any{
			"result":            "invalid_multipart",
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
			"content_type":      contentType,
			"message":           err.Error(),
		})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid multipart form"})
	}
	requestBody = normalizedBody
	contentType = normalizedContentType
	requestBytes = len(requestBody)

	logDemeterAudioStage(c, route, seq, "upstream_send_start", map[string]any{
		"upstream":      demeterAudioTranscriptionsUpstreamPath,
		"request_bytes": requestBytes,
	})

	relayResult, err := a.demeterAudioTranscriptionWithRetry(c, route, seq, requestBody, contentType, requestBytes)
	if err != nil {
		logDemeterRelayIssue(c, route, fiber.StatusBadGateway, err.Error())
		logDemeterAudioStage(c, route, seq, "sequence_end", map[string]any{
			"result":               "upstream_transport_error",
			"upstream":             demeterAudioTranscriptionsUpstreamPath,
			"upstream_duration_ms": 0,
			"total_duration_ms":    time.Since(startedAt).Milliseconds(),
			"request_bytes":        requestBytes,
			"message":              err.Error(),
		})
		return c.Status(fiber.StatusBadGateway).JSON(ErrorResponse{Error: "failed to reach mistral"})
	}

	statusCode := relayResult.statusCode
	responseBody := relayResult.responseBody
	upstreamDurationMs := relayResult.upstreamDurationMs

	logDemeterAudioStage(c, route, seq, "upstream_response_received", map[string]any{
		"upstream":             demeterAudioTranscriptionsUpstreamPath,
		"upstream_status":      statusCode,
		"upstream_duration_ms": upstreamDurationMs,
		"request_bytes":        requestBytes,
		"response_bytes":       len(responseBody),
		"attempts":             relayResult.attempts,
	})

	logDemeterAudioStage(c, route, seq, "return_to_front", map[string]any{
		"upstream":             demeterAudioTranscriptionsUpstreamPath,
		"upstream_status":      statusCode,
		"upstream_duration_ms": upstreamDurationMs,
		"request_bytes":        requestBytes,
		"response_bytes":       len(responseBody),
		"attempts":             relayResult.attempts,
	})

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
	logDemeterAudioStage(c, route, seq, "sequence_end", map[string]any{
		"result":               result,
		"upstream":             demeterAudioTranscriptionsUpstreamPath,
		"upstream_status":      statusCode,
		"upstream_duration_ms": upstreamDurationMs,
		"total_duration_ms":    time.Since(startedAt).Milliseconds(),
		"request_bytes":        requestBytes,
		"response_bytes":       len(responseBody),
		"attempts":             relayResult.attempts,
	})
	return sendErr
}

type demeterAudioTranscriptionRelayResult struct {
	statusCode         int
	responseBody       []byte
	upstreamDurationMs int64
	attempts           int
}

func (a *App) demeterAudioTranscriptionWithRetry(c *fiber.Ctx, route string, seq uint64, requestBody []byte, contentType string, requestBytes int) (demeterAudioTranscriptionRelayResult, error) {
	ctx := requestContext(c)
	for attempt := 1; attempt <= demeterAudioTranscriptionMaxAttempts; attempt++ {
		upstreamStartedAt := time.Now()
		statusCode, responseBody, err := a.MistralClient.DoMultipart(ctx, demeterAudioTranscriptionsUpstreamPath, requestBody, contentType)
		upstreamDurationMs := time.Since(upstreamStartedAt).Milliseconds()
		if err != nil {
			return demeterAudioTranscriptionRelayResult{}, err
		}
		if shouldRetryDemeterAudioTranscriptionStatus(statusCode) && attempt < demeterAudioTranscriptionMaxAttempts {
			delayMs := demeterAudioTranscriptionRetryDelay.Milliseconds()
			logDemeterAudioStage(c, route, seq, "upstream_retry", map[string]any{
				"upstream":             demeterAudioTranscriptionsUpstreamPath,
				"attempt":              attempt,
				"next_attempt":         attempt + 1,
				"max_attempts":         demeterAudioTranscriptionMaxAttempts,
				"request_bytes":        requestBytes,
				"upstream_status":      statusCode,
				"upstream_duration_ms": upstreamDurationMs,
				"response_bytes":       len(responseBody),
				"retry_delay_ms":       delayMs,
				"reason":               "upstream_5xx",
			})
			if err := sleepContext(ctx, demeterAudioTranscriptionRetryDelay); err != nil {
				return demeterAudioTranscriptionRelayResult{}, err
			}
			continue
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

func normalizeDemeterAudioTranscriptionRequest(body []byte, contentType string) ([]byte, string, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, "", fmt.Errorf("invalid multipart content type: %w", err)
	}
	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return nil, "", fmt.Errorf("multipart boundary is missing")
	}

	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	parts := make([]demeterMultipartPart, 0)
	modelSeen := false

	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", fmt.Errorf("failed to read multipart body: %w", err)
		}
		data, err := io.ReadAll(part)
		if err != nil {
			return nil, "", fmt.Errorf("failed to read multipart part: %w", err)
		}
		name := strings.TrimSpace(part.FormName())
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
			return nil, "", fmt.Errorf("failed to inject default model: %w", err)
		}
	}
	for _, part := range parts {
		dst, err := writer.CreatePart(part.header)
		if err != nil {
			return nil, "", fmt.Errorf("failed to rebuild multipart body: %w", err)
		}
		if _, err := dst.Write(part.body); err != nil {
			return nil, "", fmt.Errorf("failed to write multipart body: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("failed to finalize multipart body: %w", err)
	}
	return buffer.Bytes(), writer.FormDataContentType(), nil
}

func cloneMultipartHeader(src textproto.MIMEHeader) textproto.MIMEHeader {
	dst := make(textproto.MIMEHeader, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func nextDemeterAudioSequenceID() uint64 {
	return atomic.AddUint64(&demeterAudioSequenceCounter, 1)
}

func logDemeterAudioStage(c *fiber.Ctx, route string, seq uint64, stage string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["seq"] = seq
	logAPIStep(c, "demeter", route, stage, "audio_transcription", fields)
}

func logDemeterRelayIssue(c *fiber.Ctx, route string, status int, message string) {
	logAPIStep(c, "demeter", route, "relay_issue", "relay", map[string]any{
		"status":  status,
		"message": message,
	})
}

func logDemeterUpstreamStatus(c *fiber.Ctx, route string, status int) {
	if status < fiber.StatusBadRequest {
		return
	}
	logAPIStep(c, "demeter", route, "upstream_error", "upstream", map[string]any{
		"status": status,
	})
}

func shouldRetryDemeterAudioTranscriptionStatus(status int) bool {
	return status >= fiber.StatusInternalServerError && status < 600
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
