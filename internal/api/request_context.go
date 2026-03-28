package api

import (
	"context"

	"github.com/gofiber/fiber/v2"
)

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
