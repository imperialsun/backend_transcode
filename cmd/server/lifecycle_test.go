package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"demeter-backend/internal/backendperformance"
	"demeter-backend/internal/store"
)

func captureTestLogOutput(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	previousPrefix := log.Prefix()
	log.SetOutput(&buf)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	})

	return &buf
}

// This test exercises the shutdown path with a fake listener so the log trace
// order can be verified without opening a real socket.
func TestRunServerLifecycle_LogsTraceSteps(t *testing.T) {
	var buf bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	previousPrefix := log.Prefix()
	log.SetOutput(&buf)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	})

	listenStarted := make(chan struct{})
	shutdownCalled := make(chan struct{})
	signalCh := make(chan os.Signal, 1)

	runtime := serverRuntime{
		listen: func() error {
			close(listenStarted)
			<-shutdownCalled
			return nil
		},
		shutdown: func(context.Context) error {
			close(shutdownCalled)
			return nil
		},
		signalCh: signalCh,
	}

	go func() {
		<-listenStarted
		signalCh <- syscall.SIGTERM
	}()

	if err := runServerLifecycle(context.Background(), "server-trace", ":8080", runtime); err != nil {
		t.Fatalf("runServerLifecycle failed: %v", err)
	}

	logged := buf.String()
	for _, needle := range []string{
		"step=listen_start",
		"step=signal_received",
		"step=shutdown_start",
		"step=listen_stopped",
		"step=shutdown_success",
		"trace_id=server-trace",
	} {
		if !strings.Contains(logged, needle) {
			t.Fatalf("expected %q in logs, got %q", needle, logged)
		}
	}
}

type fakeMeetingFinalizeOperationPurger struct {
	purged int64
	err    error
}

// fakeMeetingFinalizeOperationPurger lets the cleanup loop be tested without a
// database by returning a controlled purge result.
func (p fakeMeetingFinalizeOperationPurger) PurgeExpiredMeetingFinalizeOperations(context.Context, time.Time) (int64, error) {
	return p.purged, p.err
}

type fakeBackendErrorEventPurger struct {
	purged int64
	err    error
	calls  int64
}

// fakeBackendErrorEventPurger tracks how many times the periodic cleanup loop
// invokes the purge hook.
func (p *fakeBackendErrorEventPurger) PurgeExpiredBackendErrorEvents(context.Context, time.Time) (int64, error) {
	atomic.AddInt64(&p.calls, 1)
	return p.purged, p.err
}

type fakeDemeterTranscriptionOperationPurger struct {
	purged int64
	err    error
}

// fakeDemeterTranscriptionOperationPurger lets the boot cleanup test exercise
// the new completed-job purge path without a real database.
func (p fakeDemeterTranscriptionOperationPurger) PurgeCompletedDemeterAudioTranscriptionOperations(context.Context) (int64, error) {
	return p.purged, p.err
}

// These tests assert that the lifecycle cleanup helpers emit the expected
// structured log entries for both successful and partial-failure cases.
func TestRunMeetingFinalizeOperationCleanupOnce_LogsTraceSteps(t *testing.T) {
	buf := captureTestLogOutput(t)

	runMeetingFinalizeOperationCleanupOnce(context.Background(), "server-trace", fakeMeetingFinalizeOperationPurger{purged: 3})

	logged := buf.String()
	for _, needle := range []string{
		"[server]",
		"route=lifecycle",
		"step=meeting_finalize_cleanup_success",
		"trace_id=server-trace",
		"purged=3",
	} {
		if !strings.Contains(logged, needle) {
			t.Fatalf("expected %q in logs, got %q", needle, logged)
		}
	}
}

func TestRunBackendErrorCleanupOnce_LogsTraceSteps(t *testing.T) {
	buf := captureTestLogOutput(t)

	runBackendErrorCleanupOnce(context.Background(), "server-trace", &fakeBackendErrorEventPurger{purged: 5})

	logged := buf.String()
	for _, needle := range []string{
		"[server]",
		"route=lifecycle",
		"step=backend_error_cleanup_success",
		"trace_id=server-trace",
		"purged=5",
	} {
		if !strings.Contains(logged, needle) {
			t.Fatalf("expected %q in logs, got %q", needle, logged)
		}
	}
}

func TestRunDemeterAudioTranscriptionOperationCleanupOnce_LogsPerformanceEvent(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "lifecycle-performance.sqlite")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		backendperformance.RegisterSink(nil)
		_ = st.Close()
	})
	backendperformance.RegisterSink(st)

	runDemeterAudioTranscriptionOperationCleanupOnce(ctx, "server-trace", fakeDemeterTranscriptionOperationPurger{purged: 4})

	deadline := time.Now().Add(250 * time.Millisecond)
	var metaJSON string
	for {
		if err := st.DB.QueryRowContext(ctx, `
			SELECT meta_json
			FROM performance_events
			WHERE trace_id = ? AND task = ?
			ORDER BY occurred_at DESC
			LIMIT 1
		`, "server-trace", "purge_completed_transcription_operations").Scan(&metaJSON); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expected completed transcription cleanup performance event")
		}
		time.Sleep(10 * time.Millisecond)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(metaJSON), &payload); err != nil {
		t.Fatalf("failed to decode performance payload: %v", err)
	}
	if payload["cleanup_scope"] != "boot" {
		t.Fatalf("unexpected cleanup scope: %#v", payload)
	}
	switch got := payload["purged_count"].(type) {
	case float64:
		if int(got) != 4 {
			t.Fatalf("unexpected purged count: %#v", payload)
		}
	case string:
		if got != "4" {
			t.Fatalf("unexpected purged count: %#v", payload)
		}
	default:
		t.Fatalf("unexpected purged count: %#v", payload)
	}
	if payload["status"] != "success" {
		t.Fatalf("unexpected cleanup status: %#v", payload)
	}
}

func TestRunBackendErrorCleanupLoop_CallsPurgerOnTick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	purger := &fakeBackendErrorEventPurger{purged: 2}
	done := make(chan struct{})
	go func() {
		runBackendErrorCleanupLoop(ctx, "server-trace", purger, 10*time.Millisecond)
		close(done)
	}()

	deadline := time.Now().Add(250 * time.Millisecond)
	for atomic.LoadInt64(&purger.calls) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("expected backend error cleanup loop to call the purger")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()

	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected backend error cleanup loop to stop after cancellation")
	}
}

func TestPurgeManagedBackendTmpDirs_RemovesManagedTargets(t *testing.T) {
	buf := captureTestLogOutput(t)

	baseDir := filepath.Join(t.TempDir(), "managed")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatalf("failed to create base dir: %v", err)
	}

	managedPaths := []string{
		filepath.Join(baseDir, "demeter-audio-001"),
		filepath.Join(baseDir, "demeter-audio-002"),
		filepath.Join(baseDir, "demeter-chunk-001"),
		filepath.Join(baseDir, "demeter-chunk-002"),
		filepath.Join(baseDir, managedBackendTmpTransportFolder),
	}
	for _, path := range managedPaths {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("failed to create managed path %q: %v", path, err)
		}
	}
	if err := os.WriteFile(filepath.Join(managedPaths[len(managedPaths)-1], "nested.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("failed to create transport payload: %v", err)
	}

	keepDir := filepath.Join(baseDir, "keep-me")
	if err := os.MkdirAll(keepDir, 0o755); err != nil {
		t.Fatalf("failed to create keep dir: %v", err)
	}

	summary := purgeManagedBackendTmpDirs(baseDir, "server-trace", "boot")
	if summary.TargetCount != len(managedBackendTmpPurgeTargets) {
		t.Fatalf("unexpected target count: got %d want %d", summary.TargetCount, len(managedBackendTmpPurgeTargets))
	}
	if summary.RemovedCount != len(managedPaths)-1 {
		t.Fatalf("unexpected removed count: got %d want %d", summary.RemovedCount, len(managedPaths)-1)
	}
	if summary.ErrorCount != 0 {
		t.Fatalf("unexpected error count: got %d want 0", summary.ErrorCount)
	}

	for _, path := range managedPaths[:len(managedPaths)-1] {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected managed path to be removed: %q err=%v", path, err)
		}
	}
	if _, err := os.Stat(managedPaths[len(managedPaths)-1]); err != nil {
		t.Fatalf("expected transport path to remain on boot: %v", err)
	}
	if _, err := os.Stat(keepDir); err != nil {
		t.Fatalf("expected keep dir to remain: %v", err)
	}

	logged := buf.String()
	for _, needle := range []string{
		"step=tmp_purge_start",
		"step=tmp_purge_complete",
		"phase=\"boot\"",
		"removed_count=4",
	} {
		if !strings.Contains(logged, needle) {
			t.Fatalf("expected %q in logs, got %q", needle, logged)
		}
	}
}

func TestPurgeManagedBackendTmpDirs_LogsPerformanceEvent(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "tmp-purge-performance.sqlite")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		backendperformance.RegisterSink(nil)
		_ = st.Close()
	})
	backendperformance.RegisterSink(st)

	baseDir := filepath.Join(t.TempDir(), "managed-performance")
	if err := os.MkdirAll(filepath.Join(baseDir, "demeter-audio-001"), 0o755); err != nil {
		t.Fatalf("failed to create managed path: %v", err)
	}

	purgeManagedBackendTmpDirs(baseDir, "server-trace", "boot")

	deadline := time.Now().Add(250 * time.Millisecond)
	var metaJSON string
	for {
		if err := st.DB.QueryRowContext(ctx, `
			SELECT meta_json
			FROM performance_events
			WHERE trace_id = ? AND task = ?
			ORDER BY occurred_at DESC
			LIMIT 1
		`, "server-trace", "purge_managed_tmp_dirs").Scan(&metaJSON); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expected tmp purge performance event")
		}
		time.Sleep(10 * time.Millisecond)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(metaJSON), &payload); err != nil {
		t.Fatalf("failed to decode performance payload: %v", err)
	}
	if payload["cleanup_scope"] != "boot" {
		t.Fatalf("unexpected cleanup scope: %#v", payload)
	}
	if payload["status"] != "success" {
		t.Fatalf("unexpected cleanup status: %#v", payload)
	}
}

func TestPurgeManagedBackendTmpDirs_ContinuesOnGlobError(t *testing.T) {
	buf := captureTestLogOutput(t)

	baseDir := filepath.Join(t.TempDir(), "purge[base")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatalf("failed to create base dir: %v", err)
	}

	transportDir := filepath.Join(baseDir, managedBackendTmpTransportFolder)
	if err := os.MkdirAll(transportDir, 0o755); err != nil {
		t.Fatalf("failed to create transport dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(transportDir, "nested.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("failed to create transport payload: %v", err)
	}

	keepDir := filepath.Join(baseDir, "keep-me")
	if err := os.MkdirAll(keepDir, 0o755); err != nil {
		t.Fatalf("failed to create keep dir: %v", err)
	}

	summary := purgeManagedBackendTmpDirs(baseDir, "server-trace", "shutdown")
	if summary.TargetCount != len(managedBackendTmpPurgeTargets) {
		t.Fatalf("unexpected target count: got %d want %d", summary.TargetCount, len(managedBackendTmpPurgeTargets))
	}
	if summary.RemovedCount != 1 {
		t.Fatalf("unexpected removed count: got %d want 1", summary.RemovedCount)
	}
	if summary.ErrorCount != 2 {
		t.Fatalf("unexpected error count: got %d want 2", summary.ErrorCount)
	}

	if _, err := os.Stat(transportDir); !os.IsNotExist(err) {
		t.Fatalf("expected transport dir to be removed: err=%v", err)
	}
	if _, err := os.Stat(keepDir); err != nil {
		t.Fatalf("expected keep dir to remain: %v", err)
	}

	logged := buf.String()
	for _, needle := range []string{
		"step=tmp_purge_error",
		"phase=\"shutdown\"",
		"step=tmp_purge_complete",
		"error_count=2",
	} {
		if !strings.Contains(logged, needle) {
			t.Fatalf("expected %q in logs, got %q", needle, logged)
		}
	}
}

func TestRunServerLifecycleWithManagedTmpCleanup_CallsPurgeOnBootAndShutdown(t *testing.T) {
	var phases []string
	var baseDirs []string
	previousPurge := purgeManagedBackendTmpDirsFn
	purgeManagedBackendTmpDirsFn = func(baseDir, traceID, phase string) managedBackendTmpPurgeSummary {
		phases = append(phases, phase)
		baseDirs = append(baseDirs, baseDir)
		return managedBackendTmpPurgeSummary{}
	}
	t.Cleanup(func() {
		purgeManagedBackendTmpDirsFn = previousPurge
	})

	listenStarted := make(chan struct{})
	shutdownCalled := make(chan struct{})
	signalCh := make(chan os.Signal, 1)

	runtime := serverRuntime{
		listen: func() error {
			close(listenStarted)
			<-shutdownCalled
			return nil
		},
		shutdown: func(context.Context) error {
			close(shutdownCalled)
			return nil
		},
		signalCh: signalCh,
	}

	go func() {
		<-listenStarted
		signalCh <- syscall.SIGTERM
	}()

	if err := runServerLifecycleWithManagedTmpCleanup(context.Background(), "server-trace", ":8080", runtime); err != nil {
		t.Fatalf("runServerLifecycleWithManagedTmpCleanup failed: %v", err)
	}

	if len(phases) != 2 {
		t.Fatalf("unexpected purge call count: got %d want 2", len(phases))
	}
	if phases[0] != "boot" || phases[1] != "shutdown" {
		t.Fatalf("unexpected purge phases: %v", phases)
	}

	expectedTmpDir := filepath.Clean(os.TempDir())
	if len(baseDirs) != 2 {
		t.Fatalf("unexpected base dir call count: got %d want 2", len(baseDirs))
	}
	for _, baseDir := range baseDirs {
		if filepath.Clean(baseDir) != expectedTmpDir {
			t.Fatalf("unexpected purge base dir: got %q want %q", baseDir, expectedTmpDir)
		}
	}
}
