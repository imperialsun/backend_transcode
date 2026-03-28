package api

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"demeter-backend/internal/config"
	"demeter-backend/internal/mailer"
	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
)

type slowTestMailer struct {
	resetDelay time.Duration

	mu                 sync.Mutex
	passwordResetSent  int
	provisioningSent   int
	meetingSummarySent int
}

func (m *slowTestMailer) Ready() error {
	return nil
}

func (m *slowTestMailer) SendPasswordResetEmail(ctx context.Context, _ mailer.PasswordResetEmail) error {
	select {
	case <-time.After(m.resetDelay):
		m.mu.Lock()
		m.passwordResetSent++
		m.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *slowTestMailer) SendMeetingSummaryEmail(ctx context.Context, _ mailer.MeetingSummaryEmail) error {
	select {
	case <-time.After(m.resetDelay):
		m.mu.Lock()
		m.meetingSummarySent++
		m.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *slowTestMailer) SendUserProvisioningEmail(ctx context.Context, _ mailer.UserProvisioningEmail) error {
	select {
	case <-time.After(m.resetDelay):
		m.mu.Lock()
		m.provisioningSent++
		m.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *slowTestMailer) PasswordResetSent() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.passwordResetSent
}

func TestRequestTimeout_LogsAndPropagatesContext(t *testing.T) {
	var buf bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
	})

	appCtx := &App{}
	app := fiber.New()
	ctxObserved := make(chan struct{}, 1)
	app.Group("/api/v1", appCtx.RequestTimeout(10*time.Millisecond)).Get("/slow", func(c *fiber.Ctx) error {
		<-c.UserContext().Done()
		ctxObserved <- struct{}{}
		return c.SendStatus(fiber.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/slow", nil)
	req.Header.Set(fiber.HeaderXRequestID, "timeout-trace")
	resp, err := app.Test(req, 1_000)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != fiber.StatusGatewayTimeout {
		t.Fatalf("expected 504 when timeout expires, got %d", resp.StatusCode)
	}

	select {
	case <-ctxObserved:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected handler to observe request context cancellation")
	}

	logged := buf.String()
	if !strings.Contains(logged, "[http] route=/api/v1/slow step=request_timeout") {
		t.Fatalf("expected trace-shaped timeout log line, got %q", logged)
	}
	if !strings.Contains(logged, "title=\"timeout\"") {
		t.Fatalf("expected timeout log line, got %q", logged)
	}
	if !strings.Contains(logged, "route=/api/v1/slow") {
		t.Fatalf("expected route in timeout log, got %q", logged)
	}
	if !strings.Contains(logged, "budget_ms=10") {
		t.Fatalf("expected timeout budget in log, got %q", logged)
	}
	if !strings.Contains(logged, "status=504") {
		t.Fatalf("expected timeout status in log, got %q", logged)
	}
	if !strings.Contains(logged, "trace_id=timeout-trace") {
		t.Fatalf("expected trace id in timeout log, got %q", logged)
	}
}

func TestAuthForgotPassword_IsExcludedFromTimeout(t *testing.T) {
	var buf bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
	})

	appCtx, st := newAPIAppContext(t, "timeout-auth-mail.sqlite", config.Config{
		JWTSecret:        "test-jwt-secret",
		AppPublicURL:     "https://app.demeter.test",
		PasswordResetTTL: time.Hour,
	})
	org, err := st.CreateOrganization(context.Background(), "Timeout Org", "timeout-org", "active")
	if err != nil {
		t.Fatalf("failed to create organization: %v", err)
	}
	user, err := st.CreateUser(context.Background(), org.ID, "mail-user@example.com", "unused", "active")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	mailerStub := &slowTestMailer{resetDelay: 50 * time.Millisecond}
	appCtx.Mailer = mailerStub

	app := fiber.New()
	short := app.Group("/api/v1", appCtx.RequestTimeout(10*time.Millisecond))
	mail := app.Group("/api/v1")
	appCtx.RegisterAuthCoreRoutes(short.Group("/auth"))
	appCtx.RegisterAuthForgotPasswordRoutes(mail.Group("/auth"))

	resp := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodPost,
		"/api/v1/auth/forgot-password",
		map[string]string{"email": user.Email},
		nil,
		nil,
	)
	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for excluded forgot-password route, got %d", resp.StatusCode)
	}
	if got := mailerStub.PasswordResetSent(); got != 1 {
		t.Fatalf("expected one password reset email send, got %d", got)
	}
	if strings.Contains(buf.String(), "step=request_timeout") {
		t.Fatalf("did not expect timeout log for excluded mail route, got %q", buf.String())
	}
}

func TestAdminPasswordResetEmail_IsExcludedFromTimeout(t *testing.T) {
	var buf bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
	})

	appCtx, st := newAPIAppContext(t, "timeout-admin-mail.sqlite", config.Config{
		JWTSecret:        "test-jwt-secret",
		AppPublicURL:     "https://app.demeter.test",
		AdminPublicURL:   "https://admin.demeter.test",
		PasswordResetTTL: time.Hour,
		AdminCORSOrigins: []string{"https://admin.demeter.test"},
	})

	org, err := st.CreateOrganization(context.Background(), "Timeout Admin Org", "timeout-admin-org", "active")
	if err != nil {
		t.Fatalf("failed to create organization: %v", err)
	}
	actor, err := st.CreateUser(context.Background(), org.ID, "actor@example.com", "unused", "active")
	if err != nil {
		t.Fatalf("failed to create actor: %v", err)
	}
	target, err := st.CreateUser(context.Background(), org.ID, "target@example.com", "unused", "active")
	if err != nil {
		t.Fatalf("failed to create target: %v", err)
	}
	if err := st.SetUserGlobalRoles(context.Background(), actor.ID, []string{"super_admin", "user"}); err != nil {
		t.Fatalf("failed to set actor global roles: %v", err)
	}
	if err := st.SetUserOrganizationRoles(context.Background(), actor.ID, []string{"org_admin"}); err != nil {
		t.Fatalf("failed to set actor org roles: %v", err)
	}
	if err := st.SetUserPermissionOverrides(context.Background(), actor.ID, []store.UserPermissionOverrideInput{{PermissionCode: "feature.admin", Effect: "allow"}}); err != nil {
		t.Fatalf("failed to set feature.admin permission: %v", err)
	}

	mailerStub := &slowTestMailer{resetDelay: 50 * time.Millisecond}
	appCtx.Mailer = mailerStub

	app := fiber.New()
	short := app.Group("/api/v1", appCtx.RequestTimeout(10*time.Millisecond))
	mail := app.Group("/api/v1")
	appCtx.RegisterAdminCoreRoutes(short)
	appCtx.RegisterAdminMailRoutes(mail)

	resp := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodPost,
		"/api/v1/admin/users/"+target.ID+"/password-reset-email",
		nil,
		nil,
		adminHeaders(t, actor, appCtx.Config.JWTSecret),
	)
	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for excluded admin mail route, got %d", resp.StatusCode)
	}
	if got := mailerStub.PasswordResetSent(); got != 1 {
		t.Fatalf("expected one admin password reset email send, got %d", got)
	}
	if strings.Contains(buf.String(), "step=request_timeout") {
		t.Fatalf("did not expect timeout log for excluded admin mail route, got %q", buf.String())
	}
}
