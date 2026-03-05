package api

import (
	"log"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

// RequestLogger logs every HTTP request handled by the backend.
func (a *App) RequestLogger() fiber.Handler {
	return func(c *fiber.Ctx) error {
		startedAt := time.Now()
		err := c.Next()

		durationMs := time.Since(startedAt).Milliseconds()
		status := c.Response().StatusCode()
		if status == 0 {
			status = fiber.StatusOK
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

		if err != nil {
			log.Printf(
				"[http] method=%s path=%q status=%d duration_ms=%d ip=%s user=%s org=%s ua=%q error=%q",
				c.Method(),
				c.OriginalURL(),
				status,
				durationMs,
				c.IP(),
				userID,
				orgID,
				userAgent,
				err.Error(),
			)
			return err
		}

		log.Printf(
			"[http] method=%s path=%q status=%d duration_ms=%d ip=%s user=%s org=%s ua=%q",
			c.Method(),
			c.OriginalURL(),
			status,
			durationMs,
			c.IP(),
			userID,
			orgID,
			userAgent,
		)
		return nil
	}
}
