package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/backenderrors"
	"demeter-backend/internal/backendperformance"
	"demeter-backend/internal/config"
	"demeter-backend/internal/mailer"
	"demeter-backend/internal/mistral"
	"demeter-backend/internal/observability"
	"demeter-backend/internal/store"
)

const serverShutdownTimeout = 10 * time.Second
const meetingFinalizeCleanupInterval = 15 * time.Minute
const performanceCleanupInterval = 30 * time.Minute
const backendErrorCleanupInterval = 30 * time.Minute

const (
	managedBackendTmpAudioPattern    = "demeter-audio-*"
	managedBackendTmpChunkPattern    = "demeter-chunk-*"
	managedBackendTmpTransportFolder = "demeter-transport"
)

// serverRuntime isolates the Fiber run and shutdown hooks so lifecycle tests
// can exercise the orchestration without opening a real listener.
type serverRuntime struct {
	listen   func() error
	shutdown func(context.Context) error
	signalCh <-chan os.Signal
}

// managedBackendTmpPurgeSummary reports how many backend-owned temp targets
// were considered, removed, or left behind because of errors.
type managedBackendTmpPurgeSummary struct {
	TargetCount  int
	RemovedCount int
	ErrorCount   int
}

// managedBackendTmpPurgeTarget describes a family of temporary files or a
// fixed directory owned by the backend process.
type managedBackendTmpPurgeTarget struct {
	name    string
	pattern string
	glob    bool
}

var managedBackendTmpPurgeTargets = []managedBackendTmpPurgeTarget{
	{name: "demeter-audio", pattern: managedBackendTmpAudioPattern, glob: true},
	{name: "demeter-chunk", pattern: managedBackendTmpChunkPattern, glob: true},
	{name: "demeter-transport", pattern: managedBackendTmpTransportFolder, glob: false},
}

// purgeManagedBackendTmpDirsFn is kept as an overridable hook so tests can
// assert boot and shutdown cleanup behavior without touching real temp data.
var purgeManagedBackendTmpDirsFn = purgeManagedBackendTmpDirs

type meetingFinalizeOperationPurger interface {
	PurgeExpiredMeetingFinalizeOperations(context.Context, time.Time) (int64, error)
}

type performanceEventPurger interface {
	PurgeExpiredPerformanceEvents(context.Context, time.Time) (int64, error)
}

type backendErrorEventPurger interface {
	PurgeExpiredBackendErrorEvents(context.Context, time.Time) (int64, error)
}

// serverLogStep emits lifecycle events both to stdout and to the structured
// backend-error pipeline so startup and shutdown problems remain visible.
func serverLogStep(traceID, step, title string, fields map[string]any) {
	log.Print(observability.FormatStepLine("server", "lifecycle", step, traceID, observability.DefaultTraceID, observability.DefaultTraceID, title, fields))
	backenderrors.RecordLog(observability.WithTraceID(context.Background(), traceID), "server", "lifecycle", step, title, fields)
}

// run wires the database, bootstrap data, background cleanup loops, and HTTP
// listener into one startup sequence.
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
	backenderrors.RegisterSink(st)
	backendperformance.RegisterSink(st)

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

	runMeetingFinalizeOperationCleanup(ctx, processTraceID, st)
	runPerformanceCleanup(ctx, processTraceID, st)
	runBackendErrorCleanup(ctx, processTraceID, st)

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
	cleanupCtx, cleanupCancel := context.WithCancel(ctx)
	defer cleanupCancel()
	go runMeetingFinalizeOperationCleanupLoop(cleanupCtx, processTraceID, st, meetingFinalizeCleanupInterval)
	go runPerformanceCleanupLoop(cleanupCtx, processTraceID, st, performanceCleanupInterval)
	go runBackendErrorCleanupLoop(cleanupCtx, processTraceID, st, backendErrorCleanupInterval)

	return runServerLifecycleWithManagedTmpCleanup(ctx, processTraceID, cfg.Port, runtime)
}

// runServerLifecycleWithManagedTmpCleanup clears backend-owned temp artifacts
// before and after the main lifecycle so interrupted runs do not leak state.
func runServerLifecycleWithManagedTmpCleanup(ctx context.Context, processTraceID, port string, runtime serverRuntime) error {
	tmpDir := os.TempDir()
	purgeManagedBackendTmpDirsFn(tmpDir, processTraceID, "boot")
	defer purgeManagedBackendTmpDirsFn(tmpDir, processTraceID, "shutdown")
	return runServerLifecycle(ctx, processTraceID, port, runtime)
}

// runMeetingFinalizeOperationCleanup performs one immediate pass so stale
// finalize operations do not wait for the periodic loop.
func runMeetingFinalizeOperationCleanup(ctx context.Context, traceID string, purger meetingFinalizeOperationPurger) {
	runMeetingFinalizeOperationCleanupOnce(ctx, traceID, purger)
}

// runMeetingFinalizeOperationCleanupLoop keeps expired finalize operations
// bounded while the process stays alive.
func runMeetingFinalizeOperationCleanupLoop(ctx context.Context, traceID string, purger meetingFinalizeOperationPurger, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runMeetingFinalizeOperationCleanupOnce(ctx, traceID, purger)
		}
	}
}

// runMeetingFinalizeOperationCleanupOnce executes the purge and records the
// outcome as a structured lifecycle event.
func runMeetingFinalizeOperationCleanupOnce(ctx context.Context, traceID string, purger meetingFinalizeOperationPurger) {
	if purger == nil {
		return
	}
	purged, err := purger.PurgeExpiredMeetingFinalizeOperations(ctx, time.Now().UTC())
	if err != nil {
		serverLogStep(traceID, "meeting_finalize_cleanup_error", "server", map[string]any{"error": err})
		return
	}
	serverLogStep(traceID, "meeting_finalize_cleanup_success", "server", map[string]any{"purged": purged})
}

// runPerformanceCleanup performs one immediate sweep before the periodic loop
// starts, which prevents old metrics from lingering after a restart.
func runPerformanceCleanup(ctx context.Context, traceID string, purger performanceEventPurger) {
	runPerformanceCleanupOnce(ctx, traceID, purger)
}

// runPerformanceCleanupLoop periodically prunes expired performance events.
func runPerformanceCleanupLoop(ctx context.Context, traceID string, purger performanceEventPurger, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runPerformanceCleanupOnce(ctx, traceID, purger)
		}
	}
}

// runPerformanceCleanupOnce performs a single retention sweep for performance
// data.
func runPerformanceCleanupOnce(ctx context.Context, traceID string, purger performanceEventPurger) {
	if purger == nil {
		return
	}
	purged, err := purger.PurgeExpiredPerformanceEvents(ctx, time.Now().UTC())
	if err != nil {
		serverLogStep(traceID, "performance_cleanup_error", "server", map[string]any{"error": err})
		return
	}
	serverLogStep(traceID, "performance_cleanup_success", "server", map[string]any{"purged": purged})
}

// runBackendErrorCleanup performs one immediate purge of expired backend
// error rows so startup does not leave old operational noise behind.
func runBackendErrorCleanup(ctx context.Context, traceID string, purger backendErrorEventPurger) {
	runBackendErrorCleanupOnce(ctx, traceID, purger)
}

// runBackendErrorCleanupLoop periodically removes expired backend-error rows.
func runBackendErrorCleanupLoop(ctx context.Context, traceID string, purger backendErrorEventPurger, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runBackendErrorCleanupOnce(ctx, traceID, purger)
		}
	}
}

// runBackendErrorCleanupOnce performs a single retention sweep for backend
// error events.
func runBackendErrorCleanupOnce(ctx context.Context, traceID string, purger backendErrorEventPurger) {
	if purger == nil {
		return
	}
	purged, err := purger.PurgeExpiredBackendErrorEvents(ctx, time.Now().UTC())
	if err != nil {
		serverLogStep(traceID, "backend_error_cleanup_error", "server", map[string]any{"error": err})
		return
	}
	serverLogStep(traceID, "backend_error_cleanup_success", "server", map[string]any{"purged": purged})
}

// runServerLifecycle waits for either a process signal, a context
// cancellation, or an early listener failure before triggering shutdown.
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

// purgeManagedBackendTmpDirs removes only the temp paths that the backend owns
// so unrelated files in the system temp directory remain untouched.
func purgeManagedBackendTmpDirs(baseDir, traceID, phase string) managedBackendTmpPurgeSummary {
	if strings.TrimSpace(baseDir) == "" {
		baseDir = os.TempDir()
	}
	baseDir = filepath.Clean(baseDir)

	summary := managedBackendTmpPurgeSummary{
		TargetCount: len(managedBackendTmpPurgeTargets),
	}
	serverLogStep(traceID, "tmp_purge_start", "server", map[string]any{
		"phase":         phase,
		"base_dir":      baseDir,
		"target_count":  summary.TargetCount,
		"removed_count": summary.RemovedCount,
		"error_count":   summary.ErrorCount,
	})

	for _, target := range managedBackendTmpPurgeTargets {
		removedCount, err := purgeManagedBackendTmpTarget(baseDir, target)
		summary.RemovedCount += removedCount
		if err != nil {
			summary.ErrorCount++
			serverLogStep(traceID, "tmp_purge_error", "server", map[string]any{
				"phase":         phase,
				"base_dir":      baseDir,
				"target":        target.name,
				"pattern":       target.pattern,
				"glob":          target.glob,
				"removed_count": removedCount,
				"error":         err,
			})
		}
	}

	serverLogStep(traceID, "tmp_purge_complete", "server", map[string]any{
		"phase":         phase,
		"base_dir":      baseDir,
		"target_count":  summary.TargetCount,
		"removed_count": summary.RemovedCount,
		"error_count":   summary.ErrorCount,
	})

	return summary
}

// purgeManagedBackendTmpTarget removes a single globbed or fixed temp target
// and reports how many paths were removed.
func purgeManagedBackendTmpTarget(baseDir string, target managedBackendTmpPurgeTarget) (int, error) {
	targetPath := filepath.Join(baseDir, target.pattern)
	if target.glob {
		matches, err := filepath.Glob(targetPath)
		if err != nil {
			return 0, err
		}
		if len(matches) == 0 {
			return 0, nil
		}
		removedCount := 0
		var errs []error
		for _, match := range matches {
			if err := os.RemoveAll(match); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", match, err))
				continue
			}
			removedCount++
		}
		return removedCount, errors.Join(errs...)
	}

	if _, err := os.Stat(targetPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	if err := os.RemoveAll(targetPath); err != nil {
		return 0, err
	}
	return 1, nil
}
