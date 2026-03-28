package api

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"demeter-backend/internal/auth"

	"github.com/gofiber/fiber/v2"
)

func TestRequestLogger_LogsSuccessfulRequest(t *testing.T) {
	var buf bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(previousWriter)

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
