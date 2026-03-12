package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"demeter-backend/internal/api"
	"demeter-backend/internal/auth"
	"demeter-backend/internal/config"
	"demeter-backend/internal/mistral"
	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	recovermw "github.com/gofiber/fiber/v2/middleware/recover"
)

func main() {
	cfg := config.Load()
	ctx := context.Background()

	st, err := store.Open(ctx, cfg.SQLitePath)
	if err != nil {
		log.Fatalf("failed to open store: %v", err)
	}
	defer func() {
		_ = st.Close()
	}()

	bootstrapHash := ""
	if cfg.BootstrapAdminPassword != "" {
		bootstrapHash, err = auth.HashPassword(cfg.BootstrapAdminPassword)
		if err != nil {
			log.Fatalf("invalid bootstrap admin password: %v", err)
		}
	}
	if err := st.EnsureBootstrap(ctx, cfg.BootstrapAdminEmail, bootstrapHash, cfg.BootstrapOrgName); err != nil {
		log.Fatalf("failed to bootstrap catalog/admin: %v", err)
	}

	appCtx := &api.App{
		Config:        cfg,
		Store:         st,
		MistralClient: mistral.NewClient(cfg.MistralAPIBaseURL, cfg.MistralAPIKey),
	}

	app := fiber.New(fiber.Config{
		AppName:               "Demeter Backend",
		DisableStartupMessage: true,
	})
	app.Use(appCtx.RequestLogger())
	app.Use(recovermw.New())
	app.Use(appCtx.EnforceAdminOrigin())
	app.Use(cors.New(cors.Config{
		AllowOrigins:     joinOrigins(combineOrigins(cfg.AppCORSOrigins, cfg.AdminCORSOrigins)),
		AllowCredentials: true,
		AllowHeaders:     "Origin, Content-Type, Accept, Authorization, X-Admin-CSRF",
		AllowMethods:     "GET,POST,PUT,PATCH,DELETE,OPTIONS",
	}))

	appCtx.RegisterHealthRoutes(app)

	apiV1 := app.Group("/api/v1")
	appCtx.RegisterAuthRoutes(apiV1.Group("/auth"))
	appCtx.RegisterAdminAuthRoutes(apiV1.Group("/admin/auth"))
	appCtx.RegisterSettingsRoutes(apiV1)
	appCtx.RegisterActivityRoutes(apiV1)
	appCtx.RegisterDemeterRoutes(apiV1)
	appCtx.RegisterAdminRoutes(apiV1)

	go func() {
		if err := app.Listen(":" + cfg.Port); err != nil {
			log.Printf("fiber listen stopped: %v", err)
		}
	}()
	log.Printf("backend started on :%s", cfg.Port)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.ShutdownWithContext(shutdownCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}

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
