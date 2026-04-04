package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"demeter-backend/internal/backenderrors"
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

		failure := err != nil || status >= fiber.StatusInternalServerError
		fields := map[string]any{
			"method":      c.Method(),
			"status":      status,
			"duration_ms": durationMs,
			"ip":          c.IP(),
			"ua":          userAgent,
		}
		if failure {
			if err != nil {
				fields["error"] = err
			} else if message, preview := extractRequestFailureDetails(c.Response().Body()); message != "" {
				fields["error"] = message
				if preview != "" {
					fields["response_preview"] = preview
				}
			} else if preview := compactRequestFailurePreview(c.Response().Body()); preview != "" {
				fields["error"] = preview
				fields["response_preview"] = preview
			} else {
				fields["error"] = http.StatusText(status)
			}
			log.Print(observability.FormatStepLine("http", route, "request_failed", traceID, userID, orgID, "request", fields))
			backenderrors.RecordLog(requestContext(c), "http", route, "request_failed", "request", fields)
			return err
		}

		log.Print(observability.FormatStepLine("http", route, "request_completed", traceID, userID, orgID, "request", fields))
		backenderrors.RecordLog(requestContext(c), "http", route, "request_completed", "request", fields)
		return nil
	}
}

func extractRequestFailureDetails(body []byte) (string, string) {
	preview := compactRequestFailurePreview(body)
	if len(body) == 0 {
		return "", preview
	}

	var envelope struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil {
		for _, candidate := range []string{envelope.Error, envelope.Message, envelope.Detail} {
			if value := strings.TrimSpace(candidate); value != "" {
				return value, preview
			}
		}
	}
	return "", preview
}

func compactRequestFailurePreview(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	preview := strings.TrimSpace(string(body))
	preview = strings.ReplaceAll(preview, "\r", " ")
	preview = strings.ReplaceAll(preview, "\n", " ")
	if len(preview) > 256 {
		preview = preview[:256]
	}
	return preview
}
