package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/config"
	"demeter-backend/internal/mailer"
	"demeter-backend/internal/mistral"
	"demeter-backend/internal/observability"
	"demeter-backend/internal/store"
)

const serverShutdownTimeout = 10 * time.Second

type serverRuntime struct {
	listen   func() error
	shutdown func(context.Context) error
	signalCh <-chan os.Signal
}

func serverLogStep(traceID, step, title string, fields map[string]any) {
	log.Print(observability.FormatStepLine("server", "lifecycle", step, traceID, observability.DefaultTraceID, observability.DefaultTraceID, title, fields))
}

func run(ctx context.Context, cfg config.Config) error {
	processTraceID := observability.NewTraceID()
	serverLogStep(processTraceID, "boot_start", "server", map[string]any{"port": cfg.Port})

	st, err := store.Open(ctx, cfg.SQLitePath)
	if err != nil {
		serverLogStep(processTraceID, "boot_error", "server", map[string]any{"port": cfg.Port, "error": err})
		return err
	}
	defer func() {
		if closeErr := st.Close(); closeErr != nil {
			serverLogStep(processTraceID, "shutdown_error", "server", map[string]any{"phase": "close_store", "error": closeErr})
		}
	}()

	bootstrapHash := ""
	if cfg.BootstrapAdminPassword != "" {
		bootstrapHash, err = auth.HashPassword(cfg.BootstrapAdminPassword)
		if err != nil {
			serverLogStep(processTraceID, "boot_error", "server", map[string]any{"port": cfg.Port, "error": err})
			return err
		}
	}
	if err := st.EnsureBootstrap(ctx, cfg.BootstrapAdminEmail, bootstrapHash, cfg.BootstrapOrgName); err != nil {
		serverLogStep(processTraceID, "boot_error", "server", map[string]any{"port": cfg.Port, "error": err})
		return err
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

	serverLogStep(processTraceID, "boot_success", "server", map[string]any{"port": cfg.Port})

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	runtime := serverRuntime{
		listen: func() error {
			return app.Listen(":" + cfg.Port)
		},
		shutdown: func(shutdownCtx context.Context) error {
			return app.ShutdownWithContext(shutdownCtx)
		},
		signalCh: sigCh,
	}
	return runServerLifecycle(ctx, processTraceID, cfg.Port, runtime)
}

func runServerLifecycle(ctx context.Context, processTraceID, port string, runtime serverRuntime) error {
	serverLogStep(processTraceID, "listen_start", "server", map[string]any{"port": port})

	listenErrCh := make(chan error, 1)
	go func() {
		listenErrCh <- runtime.listen()
	}()

	select {
	case listenErr := <-listenErrCh:
		if listenErr != nil {
			serverLogStep(processTraceID, "listen_error", "server", map[string]any{"port": port, "error": listenErr})
			return listenErr
		}
		serverLogStep(processTraceID, "listen_stopped", "server", map[string]any{"port": port, "status": "stopped"})
		return nil
	case sig, ok := <-runtime.signalCh:
		if ok {
			serverLogStep(processTraceID, "signal_received", "server", map[string]any{"port": port, "signal": sig.String()})
		} else {
			serverLogStep(processTraceID, "signal_received", "server", map[string]any{"port": port, "signal": "closed"})
		}
	case <-ctx.Done():
		serverLogStep(processTraceID, "signal_received", "server", map[string]any{"port": port, "signal": "context_cancelled"})
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, serverShutdownTimeout)
	defer cancel()

	serverLogStep(processTraceID, "shutdown_start", "server", map[string]any{
		"port":       port,
		"timeout_ms": serverShutdownTimeout.Milliseconds(),
	})
	if err := runtime.shutdown(shutdownCtx); err != nil {
		serverLogStep(processTraceID, "shutdown_error", "server", map[string]any{"port": port, "error": err})
		return err
	}

	listenErr := <-listenErrCh
	fields := map[string]any{"port": port, "status": "stopped"}
	if listenErr != nil {
		fields["error"] = listenErr
	}
	serverLogStep(processTraceID, "listen_stopped", "server", fields)
	serverLogStep(processTraceID, "shutdown_success", "server", map[string]any{"port": port})
	return nil
}
