package mistral

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"demeter-backend/internal/backendperformance"
	"demeter-backend/internal/observability"
	"demeter-backend/internal/store"
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

func TestDoGet_PersistsPerformanceEvent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mistral-performance.sqlite")
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	backendperformance.RegisterSink(st)
	t.Cleanup(func() {
		backendperformance.RegisterSink(nil)
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/models" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		time.Sleep(10 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "key", time.Second, time.Second)
	ctx := observability.WithTraceID(context.Background(), "mistral-upstream-trace")
	status, body, err := client.DoGet(ctx, "/models")
	if err != nil {
		t.Fatalf("DoGet returned error: %v", err)
	}
	if status != http.StatusOK || string(body) != `{"models":[]}` {
		t.Fatalf("unexpected DoGet response: status=%d body=%q", status, string(body))
	}

	event := waitForMistralPerformanceEvent(t, st, "mistral-upstream-trace")
	if event == nil {
		t.Fatal("expected upstream request to persist a performance event")
	}
	if event.Component != "mistral" || event.Task != "mistral_models" || event.Route != "/models" {
		t.Fatalf("unexpected upstream performance event: %#v", event)
	}
	if event.Surface != "backend" || event.Status != "success" {
		t.Fatalf("unexpected upstream event surface/status: %#v", event)
	}
	if event.DurationMS <= 0 {
		t.Fatalf("expected upstream duration to be recorded, got %d", event.DurationMS)
	}
}

func TestDoGet_PersistsPerformanceEventWithGenericFallbackTask(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mistral-fallback-performance.sqlite")
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	backendperformance.RegisterSink(st)
	t.Cleanup(func() {
		backendperformance.RegisterSink(nil)
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		time.Sleep(10 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "key", time.Second, time.Second)
	ctx := observability.WithTraceID(context.Background(), "mistral-fallback-trace")
	status, body, err := client.DoGet(ctx, "/v1/embeddings")
	if err != nil {
		t.Fatalf("DoGet returned error: %v", err)
	}
	if status != http.StatusOK || string(body) != `{"data":[]}` {
		t.Fatalf("unexpected DoGet response: status=%d body=%q", status, string(body))
	}

	event := waitForMistralPerformanceEvent(t, st, "mistral-fallback-trace")
	if event == nil {
		t.Fatal("expected fallback request to persist a performance event")
	}
	if event.Component != "mistral" || event.Task != "mistral_request" || event.Route != "/v1/embeddings" {
		t.Fatalf("unexpected fallback performance event: %#v", event)
	}
	if event.Surface != "backend" || event.Status != "success" {
		t.Fatalf("unexpected fallback event surface/status: %#v", event)
	}
	if event.DurationMS <= 0 {
		t.Fatalf("expected fallback duration to be recorded, got %d", event.DurationMS)
	}
}

func TestDoJSON_PersistsPerformanceEventWithFamilyTaskPrefix(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mistral-json-performance.sqlite")
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	backendperformance.RegisterSink(st)
	t.Cleanup(func() {
		backendperformance.RegisterSink(nil)
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "key", time.Second, time.Second)
	ctx := observability.WithTraceID(context.Background(), "mistral-cr-trace")
	status, body, err := client.DoJSON(ctx, http.MethodPost, "/v1/chat/completions", []byte(`{"model":"mistral"}`))
	if err != nil {
		t.Fatalf("DoJSON returned error: %v", err)
	}
	if status != http.StatusOK || string(body) != `{"id":"chatcmpl-1"}` {
		t.Fatalf("unexpected DoJSON response: status=%d body=%q", status, string(body))
	}

	event := waitForMistralPerformanceEvent(t, st, "mistral-cr-trace")
	if event == nil {
		t.Fatal("expected chat completion request to persist a performance event")
	}
	if event.Task != "mistral_report_generation" || event.Route != "/v1/chat/completions" {
		t.Fatalf("unexpected chat completion performance event: %#v", event)
	}
	if event.Component != "mistral" || event.Status != "success" {
		t.Fatalf("unexpected chat completion event surface/status: %#v", event)
	}
}

func TestDoMultipart_PersistsPerformanceEventWithFamilyTaskPrefix(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mistral-multipart-performance.sqlite")
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	backendperformance.RegisterSink(st)
	t.Cleanup(func() {
		backendperformance.RegisterSink(nil)
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"text":"hello"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "key", time.Second, time.Second)
	ctx := observability.WithTraceID(context.Background(), "mistral-transcription-trace")
	status, body, err := client.DoMultipart(ctx, "/v1/audio/transcriptions", []byte("--body--"), "multipart/form-data; boundary=test")
	if err != nil {
		t.Fatalf("DoMultipart returned error: %v", err)
	}
	if status != http.StatusOK || string(body) != `{"text":"hello"}` {
		t.Fatalf("unexpected DoMultipart response: status=%d body=%q", status, string(body))
	}

	event := waitForMistralPerformanceEvent(t, st, "mistral-transcription-trace")
	if event == nil {
		t.Fatal("expected transcription request to persist a performance event")
	}
	if event.Task != "mistral_audio_transcription" || event.Route != "/v1/audio/transcriptions" {
		t.Fatalf("unexpected transcription performance event: %#v", event)
	}
	if event.Component != "mistral" || event.Status != "success" {
		t.Fatalf("unexpected transcription event surface/status: %#v", event)
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

func waitForMistralPerformanceEvent(t *testing.T, st *store.Store, traceID string) *store.PerformanceEvent {
	t.Helper()

	time.Sleep(150 * time.Millisecond)
	for i := 0; i < 20; i++ {
		var (
			event               store.PerformanceEvent
			userID, orgID, meta sql.NullString
		)
		err := st.DB.QueryRowContext(context.Background(), `
			SELECT event_id, trace_id, user_id, organization_id, surface, component, task, status, duration_ms, route, meta_json, occurred_at, day, created_at
			FROM performance_events
			WHERE trace_id = ?
			ORDER BY occurred_at DESC, event_id DESC
			LIMIT 1
		`, traceID).Scan(
			&event.EventID,
			&event.TraceID,
			&userID,
			&orgID,
			&event.Surface,
			&event.Component,
			&event.Task,
			&event.Status,
			&event.DurationMS,
			&event.Route,
			&meta,
			&event.OccurredAt,
			&event.Day,
			&event.CreatedAt,
		)
		if err == nil {
			event.UserID = userID.String
			event.OrganizationID = orgID.String
			event.MetaJSON = meta.String
			return &event
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("failed to query performance event: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}
