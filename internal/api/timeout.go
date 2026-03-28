package api

import (
	"context"
	"strings"
	"time"

	"demeter-backend/internal/auth"

	"github.com/gofiber/fiber/v2"
)

func (a *App) RequestTimeout(timeout time.Duration) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if timeout <= 0 || shouldSkipRequestTimeout(c.Path()) {
			return c.Next()
		}

		startedAt := time.Now().UTC()
		timeoutCtx, cancel := context.WithTimeout(requestContext(c), timeout)
		c.SetUserContext(timeoutCtx)
		defer cancel()

		err := c.Next()
		if timeoutErr := timeoutCtx.Err(); timeoutErr != nil {
			a.logRequestTimeout(c, startedAt, timeout, timeoutErr, err)
			return c.Status(fiber.StatusGatewayTimeout).JSON(ErrorResponse{Error: "request timeout"})
		}
		return err
	}
}

func shouldSkipRequestTimeout(path string) bool {
	normalized := strings.TrimSpace(path)
	if normalized == "" {
		return false
	}

	switch {
	case strings.HasPrefix(normalized, "/api/v1/providers/demeter-sante/"):
		return true
	case strings.HasPrefix(normalized, "/api/v1/meetings/"):
		return true
	case normalized == "/api/v1/auth/forgot-password":
		return true
	case normalized == "/api/v1/admin/auth/forgot-password":
		return true
	case strings.HasPrefix(normalized, "/api/v1/admin/organizations/") &&
		(strings.HasSuffix(normalized, "/users") || strings.HasSuffix(normalized, "/users/bulk")):
		return true
	case strings.HasPrefix(normalized, "/api/v1/admin/users/") && strings.HasSuffix(normalized, "/password-reset-email"):
		return true
	default:
		return false
	}
}

func (a *App) logRequestTimeout(c *fiber.Ctx, startedAt time.Time, timeout time.Duration, timeoutErr error, downstreamErr error) {
	claims := MustClaims(c)
	deadlineUTC := "-"
	if deadline, ok := c.UserContext().Deadline(); ok {
		deadlineUTC = deadline.UTC().Format(time.RFC3339Nano)
	}

	userAgent := strings.TrimSpace(c.Get(fiber.HeaderUserAgent))
	if userAgent == "" {
		userAgent = "-"
	}

	downstreamMsg := "-"
	if downstreamErr != nil {
		downstreamMsg = downstreamErr.Error()
	}

	logAPIStep(c, "http", requestRoutePath(c), "request_timeout", "timeout", map[string]any{
		"method":           c.Method(),
		"status":           fiber.StatusGatewayTimeout,
		"budget_ms":        timeout.Milliseconds(),
		"elapsed_ms":       time.Since(startedAt).Milliseconds(),
		"deadline_utc":     deadlineUTC,
		"cause":            timeoutErr,
		"downstream_error": downstreamMsg,
		"session":          requestSessionName(c, claims),
		"ip":               c.IP(),
		"ua":               userAgent,
	})
}

func requestSessionName(c *fiber.Ctx, claims *auth.Claims) string {
	if claims != nil {
		if auth.HasAudience(claims, auth.SessionTypeAdmin) {
			return auth.SessionTypeAdmin.String()
		}
		if auth.HasAudience(claims, auth.SessionTypeApp) {
			return auth.SessionTypeApp.String()
		}
	}
	if strings.HasPrefix(c.Path(), "/api/v1/admin") {
		return auth.SessionTypeAdmin.String()
	}
	if strings.HasPrefix(c.Path(), "/api/v1/") {
		return auth.SessionTypeApp.String()
	}
	return "-"
}
