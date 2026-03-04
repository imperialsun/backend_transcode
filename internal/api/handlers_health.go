package api

import (
	"context"

	"github.com/gofiber/fiber/v2"
)

func (a *App) RegisterHealthRoutes(router fiber.Router) {
	router.Get("/healthz", a.health)
	router.Get("/readyz", a.ready)
}

func (a *App) health(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"status": "ok"})
}

func (a *App) ready(c *fiber.Ctx) error {
	if err := a.Store.Ping(context.Background()); err != nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "database not ready"})
	}
	if !a.MistralClient.IsConfigured() {
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "mistral not configured"})
	}
	return c.JSON(fiber.Map{"status": "ready"})
}
