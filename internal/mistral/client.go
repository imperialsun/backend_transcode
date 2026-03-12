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
)

const maxLoggedBodyPreview = 512

const (
	defaultRequestTimeout   = 8 * time.Minute
	defaultMultipartTimeout = 20 * time.Minute
)

type Client struct {
	HTTP             *http.Client
	BaseURL          string
	APIKey           string
	RequestTimeout   time.Duration
	MultipartTimeout time.Duration
}

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

func (c *Client) IsConfigured() bool {
	return c.APIKey != "" && c.BaseURL != ""
}

func (c *Client) DoJSON(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	if !c.IsConfigured() {
		return http.StatusServiceUnavailable, nil, errors.New("mistral client is not configured")
	}
	ctx, cancel := withTimeout(ctx, c.RequestTimeout)
	defer cancel()
	if len(body) == 0 {
		body = []byte("{}")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")

	startedAt := time.Now()
	res, err := c.HTTP.Do(req)
	if err != nil {
		logUpstreamTransportError(method, c.BaseURL+path, time.Since(startedAt), err)
		return 0, nil, err
	}
	defer closeSilently(res.Body)
	data, err := io.ReadAll(res.Body)
	if err != nil {
		logUpstreamReadError(method, c.BaseURL+path, res.StatusCode, time.Since(startedAt), err)
		return 0, nil, err
	}
	logUpstreamResponse(method, c.BaseURL+path, res.StatusCode, res.Header.Get("Content-Type"), time.Since(startedAt), data)
	return res.StatusCode, data, nil
}

func (c *Client) DoMultipart(ctx context.Context, path string, body []byte, contentType string) (int, []byte, error) {
	if !c.IsConfigured() {
		return http.StatusServiceUnavailable, nil, errors.New("mistral client is not configured")
	}
	ctx, cancel := withTimeout(ctx, c.MultipartTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
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
		logUpstreamTransportError(http.MethodPost, c.BaseURL+path, time.Since(startedAt), err)
		return 0, nil, err
	}
	defer closeSilently(res.Body)
	data, err := io.ReadAll(res.Body)
	if err != nil {
		logUpstreamReadError(http.MethodPost, c.BaseURL+path, res.StatusCode, time.Since(startedAt), err)
		return 0, nil, err
	}
	logUpstreamResponse(http.MethodPost, c.BaseURL+path, res.StatusCode, res.Header.Get("Content-Type"), time.Since(startedAt), data)
	return res.StatusCode, data, nil
}

func (c *Client) DoGet(ctx context.Context, path string) (int, []byte, error) {
	if !c.IsConfigured() {
		return http.StatusServiceUnavailable, nil, errors.New("mistral client is not configured")
	}
	ctx, cancel := withTimeout(ctx, c.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	startedAt := time.Now()
	res, err := c.HTTP.Do(req)
	if err != nil {
		logUpstreamTransportError(http.MethodGet, c.BaseURL+path, time.Since(startedAt), err)
		return 0, nil, err
	}
	defer closeSilently(res.Body)
	data, err := io.ReadAll(res.Body)
	if err != nil {
		logUpstreamReadError(http.MethodGet, c.BaseURL+path, res.StatusCode, time.Since(startedAt), err)
		return 0, nil, err
	}
	logUpstreamResponse(http.MethodGet, c.BaseURL+path, res.StatusCode, res.Header.Get("Content-Type"), time.Since(startedAt), data)
	return res.StatusCode, data, nil
}

func logUpstreamTransportError(method, url string, duration time.Duration, err error) {
	log.Printf(
		"[mistral] upstream transport error method=%s url=%q duration_ms=%d error=%q",
		method,
		url,
		duration.Milliseconds(),
		err,
	)
}

func logUpstreamReadError(method, url string, status int, duration time.Duration, err error) {
	log.Printf(
		"[mistral] upstream read error method=%s url=%q status=%d duration_ms=%d error=%q",
		method,
		url,
		status,
		duration.Milliseconds(),
		err,
	)
}

func logUpstreamResponse(method, url string, status int, contentType string, duration time.Duration, body []byte) {
	if status < http.StatusBadRequest {
		return
	}
	summary, preview := summarizeUpstreamBody(body)
	log.Printf(
		"[mistral] upstream error response method=%s url=%q status=%d duration_ms=%d content_type=%q summary=%q body_preview=%q",
		method,
		url,
		status,
		duration.Milliseconds(),
		strings.TrimSpace(contentType),
		summary,
		preview,
	)
}

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

func firstNonEmptyMessage(values ...any) string {
	for _, value := range values {
		if msg := messageFromAny(value); msg != "" {
			return msg
		}
	}
	return ""
}

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

func closeSilently(closer io.Closer) {
	if closer == nil {
		return
	}
	_ = closer.Close()
}

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
