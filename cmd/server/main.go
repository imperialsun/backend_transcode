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
	"demeter-backend/internal/mailer"
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

	app := buildApp(cfg, st, mistral.NewClient(
		cfg.MistralAPIBaseURL,
		cfg.MistralAPIKey,
		cfg.MistralRequestTimeout,
		cfg.MistralAudioTimeout,
	), mailer.NewSMTPMailer(mailer.Config{
		Host:      cfg.SMTPHost,
		Port:      cfg.SMTPPort,
		Username:  cfg.SMTPUsername,
		Password:  cfg.SMTPPassword,
		FromEmail: cfg.SMTPFromEmail,
		FromName:  cfg.SMTPFromName,
	}))

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

func buildApp(cfg config.Config, st *store.Store, mistralClient *mistral.Client, appMailer mailer.Sender) *fiber.App {
	appCtx := &api.App{
		Config:        cfg,
		Store:         st,
		MistralClient: mistralClient,
		Mailer:        appMailer,
	}

	app := fiber.New(fiber.Config{
		AppName:               "Demeter Backend",
		BodyLimit:             cfg.BodyLimitBytes,
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

	apiV1Short := app.Group("/api/v1", appCtx.RequestTimeout(5*time.Second))
	apiV1Mail := app.Group("/api/v1")
	apiV1Long := app.Group("/api/v1")

	appCtx.RegisterAuthCoreRoutes(apiV1Short.Group("/auth"))
	appCtx.RegisterAuthForgotPasswordRoutes(apiV1Mail.Group("/auth"))
	appCtx.RegisterAdminAuthCoreRoutes(apiV1Short.Group("/admin/auth"))
	appCtx.RegisterAdminAuthForgotPasswordRoutes(apiV1Mail.Group("/admin/auth"))
	appCtx.RegisterSettingsRoutes(apiV1Short)
	appCtx.RegisterActivityRoutes(apiV1Short)
	appCtx.RegisterDemeterRoutes(apiV1Long)
	appCtx.RegisterMeetingRoutes(apiV1Long)
	appCtx.RegisterAdminCoreRoutes(apiV1Short)
	appCtx.RegisterAdminMailRoutes(apiV1Mail)
	return app
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
