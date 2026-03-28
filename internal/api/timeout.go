package api

import (
	"context"
	"log"
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
	userID := "-"
	orgID := "-"
	if claims != nil {
		if strings.TrimSpace(claims.UserID) != "" {
			userID = claims.UserID
		}
		if strings.TrimSpace(claims.OrgID) != "" {
			orgID = claims.OrgID
		}
	}

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

	log.Printf(
		"[http-timeout] method=%s route=%q url=%q status=%d budget_ms=%d elapsed_ms=%d deadline_utc=%s cause=%q downstream_error=%q session=%s user=%s org=%s ip=%s ua=%q",
		c.Method(),
		c.Path(),
		c.OriginalURL(),
		fiber.StatusGatewayTimeout,
		timeout.Milliseconds(),
		time.Since(startedAt).Milliseconds(),
		deadlineUTC,
		timeoutErr.Error(),
		downstreamMsg,
		requestSessionName(c, claims),
		userID,
		orgID,
		c.IP(),
		userAgent,
	)
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
