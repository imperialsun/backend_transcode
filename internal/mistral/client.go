package mistral

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"demeter-backend/internal/backenderrors"
	"demeter-backend/internal/observability"
)

const maxLoggedBodyPreview = 512

const (
	defaultRequestTimeout   = 8 * time.Minute
	defaultMultipartTimeout = 20 * time.Minute
)

// Client wraps the upstream HTTP API and carries the auth key plus the
// timeout budget used by the different request shapes.
type Client struct {
	HTTP             *http.Client
	BaseURL          string
	APIKey           string
	RequestTimeout   time.Duration
	MultipartTimeout time.Duration
}

// NewClient applies safe defaults when a caller leaves either timeout unset.
func NewClient(baseURL, apiKey string, requestTimeout, multipartTimeout time.Duration) *Client {
	if requestTimeout <= 0 {
		requestTimeout = defaultRequestTimeout
	}
	if multipartTimeout <= 0 {
		multipartTimeout = defaultMultipartTimeout
	}
	return &Client{
		HTTP:             &http.Client{},
		BaseURL:          strings.TrimRight(baseURL, "/"),
		APIKey:           strings.TrimSpace(apiKey),
		RequestTimeout:   requestTimeout,
		MultipartTimeout: multipartTimeout,
	}
}

// IsConfigured reports whether the client has enough information to make an
// upstream request.
func (c *Client) IsConfigured() bool {
	return c.APIKey != "" && c.BaseURL != ""
}

// DoJSON performs a JSON request against the upstream API and records the
// request and response shape for observability.
func (c *Client) DoJSON(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	if !c.IsConfigured() {
		return http.StatusServiceUnavailable, nil, errors.New("mistral client is not configured")
	}
	ctx, cancel := withTimeout(ctx, c.RequestTimeout)
	defer cancel()
	if len(body) == 0 {
		body = []byte("{}")
	}
	logUpstreamStep(ctx, path, "request_start", map[string]any{
		"method":        method,
		"request_bytes": len(body),
	})
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		logUpstreamStep(ctx, path, "request_error", map[string]any{
			"method": method,
			"error":  err,
		})
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")

	startedAt := time.Now()
	res, err := c.HTTP.Do(req)
	if err != nil {
		logUpstreamTransportError(ctx, method, path, time.Since(startedAt), err)
		return 0, nil, err
	}
	defer closeSilently(res.Body)
	data, err := io.ReadAll(res.Body)
	if err != nil {
		logUpstreamReadError(ctx, method, path, res.StatusCode, time.Since(startedAt), err)
		return 0, nil, err
	}
	logUpstreamResponse(ctx, method, path, res.StatusCode, res.Header.Get("Content-Type"), time.Since(startedAt), data)
	return res.StatusCode, data, nil
}

// DoMultipart posts multipart or binary payloads to the upstream API using the
// longer timeout budget required by audio requests.
func (c *Client) DoMultipart(ctx context.Context, path string, body []byte, contentType string) (int, []byte, error) {
	if !c.IsConfigured() {
		return http.StatusServiceUnavailable, nil, errors.New("mistral client is not configured")
	}
	ctx, cancel := withTimeout(ctx, c.MultipartTimeout)
	defer cancel()
	logUpstreamStep(ctx, path, "request_start", map[string]any{
		"method":        http.MethodPost,
		"request_bytes": len(body),
		"content_type":  strings.TrimSpace(contentType),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		logUpstreamStep(ctx, path, "request_error", map[string]any{
			"method": http.MethodPost,
			"error":  err,
		})
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	if strings.TrimSpace(contentType) == "" {
		return 0, nil, fmt.Errorf("missing content-type for multipart request")
	}
	req.Header.Set("Content-Type", contentType)
	startedAt := time.Now()
	res, err := c.HTTP.Do(req)
	if err != nil {
		logUpstreamTransportError(ctx, http.MethodPost, path, time.Since(startedAt), err)
		return 0, nil, err
	}
	defer closeSilently(res.Body)
	data, err := io.ReadAll(res.Body)
	if err != nil {
		logUpstreamReadError(ctx, http.MethodPost, path, res.StatusCode, time.Since(startedAt), err)
		return 0, nil, err
	}
	logUpstreamResponse(ctx, http.MethodPost, path, res.StatusCode, res.Header.Get("Content-Type"), time.Since(startedAt), data)
	return res.StatusCode, data, nil
}

// DoGet performs a plain GET request using the standard request timeout.
func (c *Client) DoGet(ctx context.Context, path string) (int, []byte, error) {
	if !c.IsConfigured() {
		return http.StatusServiceUnavailable, nil, errors.New("mistral client is not configured")
	}
	ctx, cancel := withTimeout(ctx, c.RequestTimeout)
	defer cancel()
	logUpstreamStep(ctx, path, "request_start", map[string]any{
		"method": http.MethodGet,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		logUpstreamStep(ctx, path, "request_error", map[string]any{
			"method": http.MethodGet,
			"error":  err,
		})
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	startedAt := time.Now()
	res, err := c.HTTP.Do(req)
	if err != nil {
		logUpstreamTransportError(ctx, http.MethodGet, path, time.Since(startedAt), err)
		return 0, nil, err
	}
	defer closeSilently(res.Body)
	data, err := io.ReadAll(res.Body)
	if err != nil {
		logUpstreamReadError(ctx, http.MethodGet, path, res.StatusCode, time.Since(startedAt), err)
		return 0, nil, err
	}
	logUpstreamResponse(ctx, http.MethodGet, path, res.StatusCode, res.Header.Get("Content-Type"), time.Since(startedAt), data)
	return res.StatusCode, data, nil
}

// logUpstreamStep emits a normalized upstream event that can be persisted in
// the backend-error and performance pipelines.
func logUpstreamStep(ctx context.Context, route, step string, fields map[string]any) {
	log.Print(observability.FormatStepLine("mistral", route, step, observability.TraceIDFromContext(ctx), observability.DefaultTraceID, observability.DefaultTraceID, "", fields))
	backenderrors.RecordLog(ctx, "mistral", route, step, performanceTaskForRoute(route, step), fields)
}

// performanceTaskForRoute maps upstream paths to the high-level performance task
// used in capture records.
func performanceTaskForRoute(route, step string) string {
	normalizedRoute := strings.TrimSpace(route)
	switch {
	case strings.HasPrefix(normalizedRoute, "/v1/models"), strings.HasPrefix(normalizedRoute, "/models"):
		return "mistral_models"
	case strings.HasPrefix(normalizedRoute, "/v1/audio/transcriptions"):
		return "mistral_audio_transcription"
	case strings.HasPrefix(normalizedRoute, "/v1/chat/completions"):
		return "mistral_report_generation"
	}
	return "mistral_request"
}

// logUpstreamTransportError records connection or protocol failures.
func logUpstreamTransportError(ctx context.Context, method, route string, duration time.Duration, err error) {
	logUpstreamStep(ctx, route, "transport_error", map[string]any{
		"method":      method,
		"duration_ms": duration.Milliseconds(),
		"error":       err,
	})
}

// logUpstreamReadError records failures encountered while reading the upstream
// response body.
func logUpstreamReadError(ctx context.Context, method, route string, status int, duration time.Duration, err error) {
	logUpstreamStep(ctx, route, "read_error", map[string]any{
		"method":      method,
		"status":      status,
		"duration_ms": duration.Milliseconds(),
		"error":       err,
	})
}

// logUpstreamResponse records the final upstream status and a safe preview of
// the body when the response is an error.
func logUpstreamResponse(ctx context.Context, method, route string, status int, contentType string, duration time.Duration, body []byte) {
	fields := map[string]any{
		"method":         method,
		"status":         status,
		"duration_ms":    duration.Milliseconds(),
		"content_type":   strings.TrimSpace(contentType),
		"response_bytes": len(body),
	}
	if status >= http.StatusBadRequest {
		summary, preview := summarizeUpstreamBody(body)
		fields["summary"] = summary
		if preview != "" {
			fields["preview"] = preview
		}
		logUpstreamStep(ctx, route, "upstream_error_response", fields)
		return
	}
	logUpstreamStep(ctx, route, "response_received", fields)
}

// summarizeUpstreamBody extracts a human-readable message when the upstream
// body looks like a structured error.
func summarizeUpstreamBody(body []byte) (string, string) {
	preview := compactPreview(body)
	if len(body) == 0 {
		return "empty upstream body", preview
	}

	type upstreamErrorEnvelope struct {
		Error   any `json:"error"`
		Message any `json:"message"`
		Detail  any `json:"detail"`
	}

	var parsed upstreamErrorEnvelope
	if err := json.Unmarshal(body, &parsed); err == nil {
		if msg := firstNonEmptyMessage(parsed.Message, parsed.Error, parsed.Detail); msg != "" {
			return msg, preview
		}
	}

	if preview == "" {
		return "unparseable upstream body", preview
	}
	return preview, preview
}

// firstNonEmptyMessage returns the first non-empty string-like value from a
// list of candidate error fields.
func firstNonEmptyMessage(values ...any) string {
	for _, value := range values {
		if msg := messageFromAny(value); msg != "" {
			return msg
		}
	}
	return ""
}

// messageFromAny converts common JSON shapes to a trimmed string.
func messageFromAny(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if msg := messageFromAny(item); msg != "" {
				parts = append(parts, msg)
			}
		}
		return strings.Join(parts, " | ")
	case map[string]any:
		return firstNonEmptyMessage(typed["message"], typed["error"], typed["detail"], typed["msg"])
	default:
		return ""
	}
}

// compactPreview produces a short one-line preview for logging.
func compactPreview(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return ""
	}
	compacted := strings.Join(strings.Fields(trimmed), " ")
	if len(compacted) <= maxLoggedBodyPreview {
		return compacted
	}
	return compacted[:maxLoggedBodyPreview] + "..."
}

// closeSilently closes response bodies and intentionally ignores secondary
// close errors because the main request outcome is already known.
func closeSilently(closer io.Closer) {
	if closer == nil {
		return
	}
	_ = closer.Close()
}

// withTimeout preserves the caller context while enforcing a deadline.
func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		return ctx, func() {}
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= timeout {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}
