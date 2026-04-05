package api

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/backenderrors"
	"demeter-backend/internal/backendperformance"
	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
)

func TestRequestLogger_LogsSuccessfulRequest(t *testing.T) {
	var buf bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(previousWriter)

	st := openAPITestStore(t, "request-logger-performance.sqlite")
	backendperformance.RegisterSink(st)
	t.Cleanup(func() {
		backendperformance.RegisterSink(nil)
	})

	appCtx := &App{}
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(claimsContextKey, &auth.Claims{UserID: "user-1", OrgID: "org-1"})
		return c.Next()
	})
	app.Use(appCtx.RequestLogger())
	app.Get("/ping", func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set(fiber.HeaderUserAgent, "test-agent")
	req.Header.Set(fiber.HeaderXRequestID, "trace-success")
	resp, err := app.Test(req, 5_000)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_ = resp.Body.Close()

	logged := buf.String()
	if !strings.Contains(logged, "[http] route=/ping step=request_completed") {
		t.Fatalf("expected trace-shaped success log line, got %q", logged)
	}
	if !strings.Contains(logged, "method=\"GET\"") || !strings.Contains(logged, "status=204") {
		t.Fatalf("expected success log line, got %q", logged)
	}
	if !strings.Contains(logged, "user=user-1") || !strings.Contains(logged, "org=org-1") {
		t.Fatalf("expected actor information in log, got %q", logged)
	}
	if !strings.Contains(logged, "trace_id=trace-success") {
		t.Fatalf("expected trace id in log, got %q", logged)
	}

	event := waitForRequestLoggerPerformanceEvent(t, st, "trace-success")
	if event == nil {
		t.Fatal("expected request logger to persist a performance event")
	}
	if event.Component != "http" || event.Task != "http_request" || event.Route != "/ping" {
		t.Fatalf("unexpected performance event: %#v", event)
	}
	if event.Surface != "backend" || event.Status != "success" {
		t.Fatalf("unexpected performance event surface/status: %#v", event)
	}
}

func TestRequestLogger_LogsErrors(t *testing.T) {
	var buf bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(previousWriter)

	appCtx := &App{}
	app := fiber.New()
	app.Use(appCtx.RequestLogger())
	app.Get("/boom", func(c *fiber.Ctx) error {
		return errors.New("boom")
	})

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	req.Header.Set(fiber.HeaderXRequestID, "trace-error")
	resp, err := app.Test(req, 5_000)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != fiber.StatusInternalServerError {
		t.Fatalf("expected 500 for handler error, got %d", resp.StatusCode)
	}

	logged := buf.String()
	if !strings.Contains(logged, "[http] route=/boom step=request_failed") {
		t.Fatalf("expected trace-shaped error log line, got %q", logged)
	}
	if !strings.Contains(logged, "error=\"boom\"") {
		t.Fatalf("expected error log line, got %q", logged)
	}
	if !strings.Contains(logged, "ua=\"-\"") {
		t.Fatalf("expected default user-agent placeholder, got %q", logged)
	}
	if !strings.Contains(logged, "status=500") {
		t.Fatalf("expected error log to record final 500 status, got %q", logged)
	}
	if !strings.Contains(logged, "trace_id=trace-error") {
		t.Fatalf("expected trace id in error log, got %q", logged)
	}
}

func TestRequestLogger_SkipsAuthRefreshRequests(t *testing.T) {
	var buf bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(previousWriter)

	st := openAPITestStore(t, "request-logger-refresh-skip.sqlite")
	backenderrors.RegisterSink(st)
	t.Cleanup(func() {
		backenderrors.RegisterSink(nil)
	})
	backendperformance.RegisterSink(st)
	t.Cleanup(func() {
		backendperformance.RegisterSink(nil)
	})

	appCtx := &App{}
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(claimsContextKey, &auth.Claims{UserID: "user-1", OrgID: "org-1"})
		return c.Next()
	})
	app.Use(appCtx.RequestLogger())
	app.Group("/api/v1").Group("/auth").Post("/refresh", func(c *fiber.Ctx) error {
		return errors.New("refresh boom")
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	req.Header.Set(fiber.HeaderUserAgent, "test-agent")
	req.Header.Set(fiber.HeaderXRequestID, "trace-refresh")
	resp, err := app.Test(req, 5_000)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != fiber.StatusInternalServerError {
		t.Fatalf("expected 500 for refresh handler error, got %d", resp.StatusCode)
	}

	if event := waitForRequestLoggerPerformanceEvent(t, st, "trace-refresh"); event != nil {
		t.Fatalf("expected refresh request to be skipped by performance capture, got %#v", event)
	}

	var backendErrorCount int
	if err := st.DB.QueryRowContext(context.Background(), `
		SELECT COUNT(*)
		FROM backend_error_events
		WHERE trace_id = ?
	`, "trace-refresh").Scan(&backendErrorCount); err != nil {
		t.Fatalf("failed to count backend error events: %v", err)
	}
	if backendErrorCount != 0 {
		t.Fatalf("expected no backend error events for refresh route, got %d", backendErrorCount)
	}

	logged := buf.String()
	if !strings.Contains(logged, "trace_id=trace-refresh") {
		t.Fatalf("expected refresh request trace in log, got %q", logged)
	}
}

func waitForRequestLoggerPerformanceEvent(t *testing.T, st *store.Store, traceID string) *store.PerformanceEvent {
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
