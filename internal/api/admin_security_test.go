package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/config"
	"github.com/gofiber/fiber/v2"
)

func TestRequireAdminCSRF_RejectsMissingOrInvalidHeader(t *testing.T) {
	app := fiber.New()
	app.Post(
		"/admin-protected",
		func(c *fiber.Ctx) error {
			c.Locals(claimsContextKey, &auth.Claims{CSRFToken: "expected-token"})
			return c.Next()
		},
		RequireAdminCSRF(),
		func(c *fiber.Ctx) error {
			return c.SendStatus(fiber.StatusNoContent)
		},
	)

	missing := httptest.NewRequest(http.MethodPost, "/admin-protected", nil)
	missingResp, err := app.Test(missing, 5_000)
	if err != nil {
		t.Fatalf("missing-header request failed: %v", err)
	}
	if missingResp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 without csrf header, got %d", missingResp.StatusCode)
	}

	invalid := httptest.NewRequest(http.MethodPost, "/admin-protected", nil)
	invalid.Header.Set(auth.AdminCSRFHeaderName, "wrong-token")
	invalidResp, err := app.Test(invalid, 5_000)
	if err != nil {
		t.Fatalf("invalid-header request failed: %v", err)
	}
	if invalidResp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 with invalid csrf header, got %d", invalidResp.StatusCode)
	}
}

func TestRequireAdminCSRF_AllowsMatchingHeader(t *testing.T) {
	app := fiber.New()
	app.Post(
		"/admin-protected",
		func(c *fiber.Ctx) error {
			c.Locals(claimsContextKey, &auth.Claims{CSRFToken: "expected-token"})
			return c.Next()
		},
		RequireAdminCSRF(),
		func(c *fiber.Ctx) error {
			return c.SendStatus(fiber.StatusNoContent)
		},
	)

	req := httptest.NewRequest(http.MethodPost, "/admin-protected", nil)
	req.Header.Set(auth.AdminCSRFHeaderName, "expected-token")
	resp, err := app.Test(req, 5_000)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 with valid csrf header, got %d", resp.StatusCode)
	}
}

func TestEnforceAdminOrigin_RejectsUnexpectedOriginsForAdminRoutes(t *testing.T) {
	appCtx := &App{
		Config: config.Config{
			AdminCORSOrigins: []string{"https://admin.demeter.test"},
		},
	}
	app := fiber.New()
	app.Use(appCtx.EnforceAdminOrigin())
	app.Get("/api/v1/admin/ping", func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusNoContent)
	})
	app.Get("/api/v1/auth/ping", func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusNoContent)
	})

	blocked := httptest.NewRequest(http.MethodGet, "/api/v1/admin/ping", nil)
	blocked.Header.Set(fiber.HeaderOrigin, "https://front.demeter.test")
	blockedResp, err := app.Test(blocked, 5_000)
	if err != nil {
		t.Fatalf("blocked origin request failed: %v", err)
	}
	if blockedResp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 for unexpected admin origin, got %d", blockedResp.StatusCode)
	}

	allowed := httptest.NewRequest(http.MethodGet, "/api/v1/admin/ping", nil)
	allowed.Header.Set(fiber.HeaderOrigin, "https://admin.demeter.test")
	allowedResp, err := app.Test(allowed, 5_000)
	if err != nil {
		t.Fatalf("allowed origin request failed: %v", err)
	}
	if allowedResp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for allowed admin origin, got %d", allowedResp.StatusCode)
	}

	frontend := httptest.NewRequest(http.MethodGet, "/api/v1/auth/ping", nil)
	frontend.Header.Set(fiber.HeaderOrigin, "https://front.demeter.test")
	frontendResp, err := app.Test(frontend, 5_000)
	if err != nil {
		t.Fatalf("frontend origin request failed: %v", err)
	}
	if frontendResp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected non-admin route to bypass origin enforcement, got %d", frontendResp.StatusCode)
	}
}
