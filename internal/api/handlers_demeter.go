package api

import (
	"context"
	"strings"

	"github.com/gofiber/fiber/v2"
)

func (a *App) RegisterDemeterRoutes(router fiber.Router) {
	group := router.Group("/providers/demeter-sante", a.AuthRequired())
	group.Get("/models", RequireAnyPermission("provider.cloud.demeter_sante", "provider.llm.demeter_sante"), a.demeterModels)
	group.Post("/audio/transcriptions", RequirePermissions("feature.cloudupload", "provider.cloud.demeter_sante"), a.demeterAudioTranscriptions)
	group.Post("/chat/completions", RequirePermissions("feature.llmapi", "provider.llm.demeter_sante"), a.demeterChatCompletions)
}

func (a *App) demeterModels(c *fiber.Ctx) error {
	if !a.MistralClient.IsConfigured() {
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "mistral is not configured"})
	}
	statusCode, body, err := a.MistralClient.DoGet(context.Background(), "/v1/models")
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(ErrorResponse{Error: "failed to reach mistral"})
	}
	c.Status(statusCode)
	c.Type("json")
	return c.Send(body)
}

func (a *App) demeterChatCompletions(c *fiber.Ctx) error {
	if !a.MistralClient.IsConfigured() {
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "mistral is not configured"})
	}
	statusCode, body, err := a.MistralClient.DoJSON(context.Background(), fiber.MethodPost, "/v1/chat/completions", c.Body())
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(ErrorResponse{Error: "failed to reach mistral"})
	}
	c.Status(statusCode)
	c.Type("json")
	return c.Send(body)
}

func (a *App) demeterAudioTranscriptions(c *fiber.Ctx) error {
	if !a.MistralClient.IsConfigured() {
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "mistral is not configured"})
	}
	contentType := strings.TrimSpace(c.Get(fiber.HeaderContentType))
	if !strings.HasPrefix(contentType, fiber.MIMEMultipartForm) {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "multipart/form-data is required"})
	}
	statusCode, body, err := a.MistralClient.DoMultipart(context.Background(), "/v1/audio/transcriptions", c.Body(), contentType)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(ErrorResponse{Error: "failed to reach mistral"})
	}
	c.Status(statusCode)
	c.Type("json")
	return c.Send(body)
}
