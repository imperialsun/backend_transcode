package api

import (
	"log"
	"strings"
	"time"

	"demeter-backend/internal/observability"

	"github.com/gofiber/fiber/v2"
)

// RequestLogger logs every HTTP request handled by the backend.
func (a *App) RequestLogger() fiber.Handler {
	return func(c *fiber.Ctx) error {
		traceID := requestTraceID(c)
		route := requestRoutePath(c)
		startedAt := time.Now()
		err := c.Next()

		durationMs := time.Since(startedAt).Milliseconds()
		status := c.Response().StatusCode()
		if status == 0 {
			status = fiber.StatusOK
		}
		if err != nil && status < fiber.StatusBadRequest {
			status = fiber.StatusInternalServerError
		}

		userID := "-"
		orgID := "-"
		if claims := MustClaims(c); claims != nil {
			if strings.TrimSpace(claims.UserID) != "" {
				userID = claims.UserID
			}
			if strings.TrimSpace(claims.OrgID) != "" {
				orgID = claims.OrgID
			}
		}

		userAgent := strings.TrimSpace(c.Get(fiber.HeaderUserAgent))
		if userAgent == "" {
			userAgent = "-"
		}

		fields := map[string]any{
			"method":      c.Method(),
			"status":      status,
			"duration_ms": durationMs,
			"ip":          c.IP(),
			"ua":          userAgent,
		}
		if err != nil {
			fields["error"] = err
			log.Print(observability.FormatStepLine("http", route, "request_failed", traceID, userID, orgID, "request", fields))
			return err
		}

		log.Print(observability.FormatStepLine("http", route, "request_completed", traceID, userID, orgID, "request", fields))
		return nil
	}
}
