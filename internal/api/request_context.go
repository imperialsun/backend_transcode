package api

import (
	"context"

	"github.com/gofiber/fiber/v2"
)

// requestContext returns the request-scoped context Fiber is carrying, falling
// back to a background context when needed.
func requestContext(c *fiber.Ctx) context.Context {
	if c == nil {
		return context.Background()
	}
	ensureRequestTraceID(c)
	if ctx := c.UserContext(); ctx != nil {
		return ctx
	}
	return context.Background()
}
