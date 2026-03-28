package mistral

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"demeter-backend/internal/observability"
)

func TestClient_IsConfigured(t *testing.T) {
	c := NewClient("", "", 0, 0)
	if c.IsConfigured() {
		t.Fatal("expected client to be not configured")
	}
	c = NewClient("https://api.mistral.ai", "key", 0, 0)
	if !c.IsConfigured() {
		t.Fatal("expected client to be configured")
	}
}

func TestDoJSON_RedirectsResponseBody(t *testing.T) {
	var logBuf bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"error":"teapot"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "key", 1*time.Second, 1*time.Second)
	ctx := observability.WithTraceID(context.Background(), "mistral-test-trace")
	status, body, err := client.DoJSON(ctx, http.MethodPost, "/test", []byte(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != http.StatusTeapot {
		t.Fatalf("expected status 418, got %d", status)
	}
	if string(body) != `{"error":"teapot"}` {
		t.Fatalf("unexpected body: %q", body)
	}
	logged := logBuf.String()
	if !strings.Contains(logged, "trace_id=mistral-test-trace") {
		t.Fatalf("expected trace id in mistral logs, got %q", logged)
	}
	if !strings.Contains(logged, "step=upstream_error_response") {
		t.Fatalf("expected upstream error response log, got %q", logged)
	}
}

func TestDoJSON_InvalidBaseURL(t *testing.T) {
	client := NewClient("", "key", 0, 0)
	_, _, err := client.DoJSON(context.Background(), http.MethodPost, "/test", []byte(`{}`))
	if err == nil {
		t.Fatal("expected error due to missing base URL")
	}
}

func TestDoGet_SuccessAndUnconfigured(t *testing.T) {
	client := NewClient("", "", time.Second, time.Second)
	status, body, err := client.DoGet(context.Background(), "/models")
	if err == nil || status != http.StatusServiceUnavailable || body != nil {
		t.Fatalf("expected unconfigured DoGet to fail with 503, got status=%d body=%v err=%v", status, body, err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer key" {
			t.Fatalf("unexpected authorization header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer server.Close()

	client = NewClient(server.URL, "key", time.Second, time.Second)
	status, body, err = client.DoGet(context.Background(), "/models")
	if err != nil {
		t.Fatalf("DoGet returned error: %v", err)
	}
	if status != http.StatusOK || string(body) != `{"models":[]}` {
		t.Fatalf("unexpected DoGet response: status=%d body=%q", status, string(body))
	}
}

func TestHelperFunctions_LogTimeoutPreviewAndClose(t *testing.T) {
	var logBuf bytes.Buffer
	originalWriter := log.Writer()
	originalFlags := log.Flags()
	log.SetOutput(&logBuf)
	log.SetFlags(0)
	defer log.SetOutput(originalWriter)
	defer log.SetFlags(originalFlags)

	ctx := observability.WithTraceID(context.Background(), "mistral-helper-trace")
	logUpstreamReadError(ctx, http.MethodGet, "https://example.test/models", http.StatusBadGateway, 25*time.Millisecond, errors.New("read failed"))
	if !strings.Contains(logBuf.String(), "step=read_error") {
		t.Fatalf("expected read error log output, got %q", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "trace_id=mistral-helper-trace") {
		t.Fatalf("expected trace id in helper log output, got %q", logBuf.String())
	}

	longBody := []byte(strings.Repeat("  too-long ", 80))
	preview := compactPreview(longBody)
	if !strings.HasSuffix(preview, "...") || len(preview) <= maxLoggedBodyPreview {
		t.Fatalf("expected compactPreview to truncate output, got length=%d preview=%q", len(preview), preview)
	}

	ctx, cancel := withTimeout(context.TODO(), 25*time.Millisecond)
	defer cancel()
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("expected withTimeout to add a deadline")
	}

	baseCtx, baseCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer baseCancel()
	derivedCtx, derivedCancel := withTimeout(baseCtx, time.Second)
	defer derivedCancel()
	if derivedDeadline, _ := derivedCtx.Deadline(); derivedDeadline.IsZero() {
		t.Fatal("expected derived context to preserve earlier deadline")
	}

	closeSilently(nil)
	closeSilently(io.NopCloser(strings.NewReader("ok")))
}
