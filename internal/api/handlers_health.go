package api

import (
	"context"

	"github.com/gofiber/fiber/v2"
)

// RegisterHealthRoutes installs the liveness and readiness endpoints.
func (a *App) RegisterHealthRoutes(router fiber.Router) {
	router.Get("/healthz", a.health)
	router.Get("/readyz", a.ready)
}

// health returns the unconditional liveness response.
func (a *App) health(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"status": "ok"})
}

// ready only returns success once the database and upstream client are ready.
func (a *App) ready(c *fiber.Ctx) error {
	if err := a.Store.Ping(context.Background()); err != nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "database not ready"})
	}
	if !a.MistralClient.IsConfigured() {
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "mistral not configured"})
	}
	return c.JSON(fiber.Map{"status": "ready"})
}
