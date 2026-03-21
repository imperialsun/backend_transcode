package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/textproto"
	"sort"
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
	defaultDemeterAudioTranscriptionModelID = "voxtral-mini-latest"
)

var demeterAudioSequenceCounter uint64

func (a *App) RegisterDemeterRoutes(router fiber.Router) {
	group := router.Group("/providers/demeter-sante", a.AppAuthRequired())
	group.Get("/models", RequireAnyPermission("provider.cloud.demeter_sante", "provider.llm.demeter_sante"), a.demeterModels)
	group.Post("/audio/transcriptions", RequirePermissions("feature.cloudupload", "provider.cloud.demeter_sante"), a.demeterAudioTranscriptions)
	group.Post("/chat/completions", RequirePermissions("feature.llmapi", "provider.llm.demeter_sante"), a.demeterChatCompletions)
}

func (a *App) demeterModels(c *fiber.Ctx) error {
	if !a.MistralClient.IsConfigured() {
		logDemeterRelayIssue(c, demeterModelsUpstreamPath, fiber.StatusServiceUnavailable, "mistral client is not configured")
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "mistral is not configured"})
	}
	statusCode, body, err := a.MistralClient.DoGet(context.Background(), demeterModelsUpstreamPath)
	if err != nil {
		logDemeterRelayIssue(c, demeterModelsUpstreamPath, fiber.StatusBadGateway, err.Error())
		return c.Status(fiber.StatusBadGateway).JSON(ErrorResponse{Error: "failed to reach mistral"})
	}
	logDemeterUpstreamStatus(c, demeterModelsUpstreamPath, statusCode)
	c.Status(statusCode)
	c.Type("json")
	return c.Send(body)
}

func (a *App) demeterChatCompletions(c *fiber.Ctx) error {
	if !a.MistralClient.IsConfigured() {
		logDemeterRelayIssue(c, demeterChatCompletionsUpstreamPath, fiber.StatusServiceUnavailable, "mistral client is not configured")
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "mistral is not configured"})
	}
	statusCode, body, err := a.MistralClient.DoJSON(context.Background(), fiber.MethodPost, demeterChatCompletionsUpstreamPath, c.Body())
	if err != nil {
		logDemeterRelayIssue(c, demeterChatCompletionsUpstreamPath, fiber.StatusBadGateway, err.Error())
		return c.Status(fiber.StatusBadGateway).JSON(ErrorResponse{Error: "failed to reach mistral"})
	}
	logDemeterUpstreamStatus(c, demeterChatCompletionsUpstreamPath, statusCode)
	c.Status(statusCode)
	c.Type("json")
	return c.Send(body)
}

func (a *App) demeterAudioTranscriptions(c *fiber.Ctx) error {
	startedAt := time.Now()
	seq := nextDemeterAudioSequenceID()
	contentType := strings.TrimSpace(c.Get(fiber.HeaderContentType))
	requestBody := c.Body()
	requestBytes := len(requestBody)

	logDemeterAudioStage(c, seq, "front_received", map[string]string{
		"content_type":  contentType,
		"request_bytes": strconv.Itoa(requestBytes),
	})

	if !a.MistralClient.IsConfigured() {
		logDemeterRelayIssue(c, demeterAudioTranscriptionsUpstreamPath, fiber.StatusServiceUnavailable, "mistral client is not configured")
		logDemeterAudioStage(c, seq, "sequence_end", map[string]string{
			"result":            "mistral_not_configured",
			"total_duration_ms": strconv.FormatInt(time.Since(startedAt).Milliseconds(), 10),
			"request_bytes":     strconv.Itoa(requestBytes),
		})
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "mistral is not configured"})
	}
	if !strings.HasPrefix(contentType, fiber.MIMEMultipartForm) {
		logDemeterRelayIssue(c, demeterAudioTranscriptionsUpstreamPath, fiber.StatusBadRequest, "multipart/form-data is required")
		logDemeterAudioStage(c, seq, "sequence_end", map[string]string{
			"result":            "invalid_content_type",
			"total_duration_ms": strconv.FormatInt(time.Since(startedAt).Milliseconds(), 10),
			"request_bytes":     strconv.Itoa(requestBytes),
			"content_type":      contentType,
		})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "multipart/form-data is required"})
	}

	normalizedBody, normalizedContentType, err := normalizeDemeterAudioTranscriptionRequest(requestBody, contentType)
	if err != nil {
		logDemeterRelayIssue(c, demeterAudioTranscriptionsUpstreamPath, fiber.StatusBadRequest, err.Error())
		logDemeterAudioStage(c, seq, "sequence_end", map[string]string{
			"result":            "invalid_multipart",
			"total_duration_ms": strconv.FormatInt(time.Since(startedAt).Milliseconds(), 10),
			"request_bytes":     strconv.Itoa(requestBytes),
			"content_type":      contentType,
			"message":           err.Error(),
		})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid multipart form"})
	}
	requestBody = normalizedBody
	contentType = normalizedContentType
	requestBytes = len(requestBody)

	logDemeterAudioStage(c, seq, "upstream_send_start", map[string]string{
		"upstream":      demeterAudioTranscriptionsUpstreamPath,
		"request_bytes": strconv.Itoa(requestBytes),
	})

	upstreamStartedAt := time.Now()
	statusCode, responseBody, err := a.MistralClient.DoMultipart(context.Background(), demeterAudioTranscriptionsUpstreamPath, requestBody, contentType)
	upstreamDurationMs := time.Since(upstreamStartedAt).Milliseconds()
	if err != nil {
		logDemeterRelayIssue(c, demeterAudioTranscriptionsUpstreamPath, fiber.StatusBadGateway, err.Error())
		logDemeterAudioStage(c, seq, "sequence_end", map[string]string{
			"result":               "upstream_transport_error",
			"upstream":             demeterAudioTranscriptionsUpstreamPath,
			"upstream_duration_ms": strconv.FormatInt(upstreamDurationMs, 10),
			"total_duration_ms":    strconv.FormatInt(time.Since(startedAt).Milliseconds(), 10),
			"request_bytes":        strconv.Itoa(requestBytes),
			"message":              err.Error(),
		})
		return c.Status(fiber.StatusBadGateway).JSON(ErrorResponse{Error: "failed to reach mistral"})
	}

	logDemeterAudioStage(c, seq, "upstream_response_received", map[string]string{
		"upstream":             demeterAudioTranscriptionsUpstreamPath,
		"upstream_status":      strconv.Itoa(statusCode),
		"upstream_duration_ms": strconv.FormatInt(upstreamDurationMs, 10),
		"response_bytes":       strconv.Itoa(len(responseBody)),
	})

	logDemeterAudioStage(c, seq, "return_to_front", map[string]string{
		"upstream":             demeterAudioTranscriptionsUpstreamPath,
		"upstream_status":      strconv.Itoa(statusCode),
		"upstream_duration_ms": strconv.FormatInt(upstreamDurationMs, 10),
		"response_bytes":       strconv.Itoa(len(responseBody)),
	})

	logDemeterUpstreamStatus(c, demeterAudioTranscriptionsUpstreamPath, statusCode)
	c.Status(statusCode)
	c.Type("json")
	sendErr := c.Send(responseBody)
	result := "ok"
	if sendErr != nil {
		result = "front_send_error"
	}
	logDemeterAudioStage(c, seq, "sequence_end", map[string]string{
		"result":               result,
		"upstream":             demeterAudioTranscriptionsUpstreamPath,
		"upstream_status":      strconv.Itoa(statusCode),
		"upstream_duration_ms": strconv.FormatInt(upstreamDurationMs, 10),
		"total_duration_ms":    strconv.FormatInt(time.Since(startedAt).Milliseconds(), 10),
		"request_bytes":        strconv.Itoa(requestBytes),
		"response_bytes":       strconv.Itoa(len(responseBody)),
	})
	return sendErr
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

func logDemeterAudioStage(c *fiber.Ctx, seq uint64, stage string, fields map[string]string) {
	userID, orgID := demeterActorIDs(c)
	var builder strings.Builder
	builder.WriteString("[demeter][audio] seq=")
	builder.WriteString(strconv.FormatUint(seq, 10))
	builder.WriteString(" stage=")
	builder.WriteString(stage)
	builder.WriteString(" method=")
	builder.WriteString(c.Method())
	builder.WriteString(" route=")
	builder.WriteString(strconv.Quote(c.OriginalURL()))
	builder.WriteString(" ip=")
	builder.WriteString(c.IP())
	builder.WriteString(" user=")
	builder.WriteString(userID)
	builder.WriteString(" org=")
	builder.WriteString(orgID)
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := fields[key]
		builder.WriteByte(' ')
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(strconv.Quote(value))
	}
	log.Print(builder.String())
}

func logDemeterRelayIssue(c *fiber.Ctx, upstreamPath string, status int, message string) {
	userID, orgID := demeterActorIDs(c)
	log.Printf(
		"[demeter] relay issue method=%s route=%q upstream=%q status=%d ip=%s user=%s org=%s message=%q",
		c.Method(),
		c.OriginalURL(),
		upstreamPath,
		status,
		c.IP(),
		userID,
		orgID,
		message,
	)
}

func logDemeterUpstreamStatus(c *fiber.Ctx, upstreamPath string, status int) {
	if status < fiber.StatusBadRequest {
		return
	}
	userID, orgID := demeterActorIDs(c)
	log.Printf(
		"[demeter] upstream error method=%s route=%q upstream=%q status=%d ip=%s user=%s org=%s",
		c.Method(),
		c.OriginalURL(),
		upstreamPath,
		status,
		c.IP(),
		userID,
		orgID,
	)
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
