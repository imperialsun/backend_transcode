package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"demeter-backend/internal/config"
	"demeter-backend/internal/mailer"
	"demeter-backend/internal/mistral"
	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
)

func TestJoinOrigins(t *testing.T) {
	if joinOrigins(nil) != "*" {
		t.Fatal("expected wildcard when no origins")
	}
	if joinOrigins([]string{"a", "b"}) != "a,b" {
		t.Fatal("expected comma-separated origins")
	}
}

func TestCombineOrigins(t *testing.T) {
	out := combineOrigins([]string{"a", "b"}, []string{"b", "c"})
	if len(out) != 3 {
		t.Fatalf("expected 3 unique origins, got %d: %v", len(out), out)
	}
}

func TestBuildApp_RegistersRoutesAndAdminOriginGuard(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "server.sqlite"))
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()

	app := buildApp(config.Config{
		JWTSecret:        "test-secret",
		BodyLimitBytes:   1024 * 1024,
		AppCORSOrigins:   []string{"https://app.demeter.test"},
		AdminCORSOrigins: []string{"https://admin.demeter.test"},
		AccessTTL:        15 * time.Minute,
		RefreshTTL:       24 * time.Hour,
		AdminAccessTTL:   10 * time.Minute,
		AdminRefreshTTL:  12 * time.Hour,
	}, st, &mistral.Client{}, mailer.NewSMTPMailer(mailer.Config{}))

	healthResp, err := app.Test(httptest.NewRequest(http.MethodGet, "/healthz", nil), 5_000)
	if err != nil {
		t.Fatalf("health request failed: %v", err)
	}
	if healthResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for health route, got %d", healthResp.StatusCode)
	}

	guardedReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/catalog/roles", nil)
	guardedReq.Header.Set(fiber.HeaderOrigin, "https://forbidden.demeter.test")
	guardedResp, err := app.Test(guardedReq, 5_000)
	if err != nil {
		t.Fatalf("guarded request failed: %v", err)
	}
	if guardedResp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 for forbidden admin origin, got %d", guardedResp.StatusCode)
	}
}
