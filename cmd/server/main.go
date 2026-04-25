package main

import (
	"context"
	"os"
	"time"

	"demeter-backend/internal/api"
	"demeter-backend/internal/config"
	"demeter-backend/internal/mailer"
	"demeter-backend/internal/mistral"
	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	recovermw "github.com/gofiber/fiber/v2/middleware/recover"
)

func main() {
	// The process loads configuration first so startup failures surface before
	// any network listeners or background workers are created.
	cfg := config.Load()
	if err := run(context.Background(), cfg); err != nil {
		os.Exit(1)
	}
}

// buildApp assembles the Fiber application with shared middleware, route
// groups, and the dependency bundle used by every handler.
func buildApp(cfg config.Config, st *store.Store, mistralClient *mistral.Client, appMailer mailer.Sender) *fiber.App {
	app, _ := buildAppBundle(cfg, st, mistralClient, appMailer)
	return app
}

func buildAppBundle(cfg config.Config, st *store.Store, mistralClient *mistral.Client, appMailer mailer.Sender) (*fiber.App, *api.App) {
	appCtx := &api.App{
		Config:        cfg,
		Store:         st,
		MistralClient: mistralClient,
		Mailer:        appMailer,
	}
	appCtx.EnsureDemeterQueueManager()

	app := fiber.New(fiber.Config{
		AppName:               "Demeter Backend",
		BodyLimit:             cfg.BodyLimitBytes,
		DisableStartupMessage: true,
	})
	app.Use(appCtx.RequestTrace())
	app.Use(appCtx.RequestLogger())
	app.Use(recovermw.New())
	app.Use(appCtx.EnforceAdminOrigin())
	app.Use(cors.New(cors.Config{
		AllowOrigins:     joinOrigins(combineOrigins(cfg.AppCORSOrigins, cfg.AdminCORSOrigins)),
		AllowCredentials: true,
		AllowHeaders:     "Origin, Content-Type, Accept, Authorization, X-Admin-CSRF, X-Cloud-Audio-Duration-Sec, X-Demeter-Transport, X-Demeter-Upload-Id, X-Demeter-Upload-Index, X-Demeter-Upload-Count, X-Demeter-Upload-Final",
		ExposeHeaders:    "X-Trace-Id",
		AllowMethods:     "GET,POST,PUT,PATCH,DELETE,OPTIONS",
	}))

	appCtx.RegisterHealthRoutes(app)

	apiV1Short := app.Group("/api/v1", appCtx.RequestTimeout(5*time.Second))
	apiV1Mail := app.Group("/api/v1")
	apiV1Long := app.Group("/api/v1")

	appCtx.RegisterAuthCoreRoutes(apiV1Short.Group("/auth"))
	appCtx.RegisterAuthForgotPasswordRoutes(apiV1Mail.Group("/auth"))
	appCtx.RegisterAdminAuthCoreRoutes(apiV1Short.Group("/admin/auth"))
	appCtx.RegisterAdminAuthForgotPasswordRoutes(apiV1Mail.Group("/admin/auth"))
	appCtx.RegisterSettingsRoutes(apiV1Short)
	appCtx.RegisterActivityRoutes(apiV1Short)
	appCtx.RegisterPerformanceRoutes(apiV1Short)
	appCtx.RegisterDemeterRoutes(apiV1Long)
	appCtx.RegisterMobileRoutes(apiV1Long)
	appCtx.RegisterSupportRoutes(apiV1Long)
	appCtx.RegisterAdminCoreRoutes(apiV1Short)
	appCtx.RegisterAdminMailRoutes(apiV1Mail)
	return app, appCtx
}

// joinOrigins turns a list of allowed origins into the comma-separated format
// expected by Fiber's CORS middleware.
func joinOrigins(origins []string) string {
	if len(origins) == 0 {
		return "*"
	}
	result := origins[0]
	for i := 1; i < len(origins); i++ {
		result += "," + origins[i]
	}
	return result
}

// combineOrigins merges multiple origin lists while preserving order and
// removing duplicates so app and admin CORS policy can share values safely.
func combineOrigins(groups ...[]string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for _, group := range groups {
		for _, origin := range group {
			if _, ok := seen[origin]; ok {
				continue
			}
			seen[origin] = struct{}{}
			out = append(out, origin)
		}
	}
	return out
}
