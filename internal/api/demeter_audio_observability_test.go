package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"demeter-backend/internal/backendperformance"
	"demeter-backend/internal/mistral"
	"demeter-backend/internal/observability"
	"demeter-backend/internal/store"
)

func TestDemeterQueueResizePersistsPerformanceEvents(t *testing.T) {
	st := openAPITestStore(t, "demeter-queue-observability.sqlite")
	backendperformance.RegisterSink(st)
	t.Cleanup(func() {
		backendperformance.RegisterSink(nil)
	})

	app := &App{Store: st}
	manager := app.EnsureDemeterQueueManager()
	t.Cleanup(manager.Stop)

	traceID := "queue-resize-trace"
	ctx := observability.WithTraceID(context.Background(), traceID)
	if err := manager.Resize(ctx, "/admin/providers/demeter-sante/queue/settings", 2); err != nil {
		t.Fatalf("failed to resize queue: %v", err)
	}

	requested := waitForPerformanceEventByTask(t, st, traceID, "demeter_queue_resize_requested")
	if requested == nil {
		t.Fatal("expected queue resize request performance event")
	}
	if requested.Component != "demeter" || requested.Route != "/admin/providers/demeter-sante/queue/settings" {
		t.Fatalf("unexpected resize request event: %#v", requested)
	}

	applied := waitForPerformanceEventByTask(t, st, traceID, "demeter_queue_resize_applied")
	if applied == nil {
		t.Fatal("expected queue resize applied performance event")
	}
	if applied.Component != "demeter" || applied.Route != "/admin/providers/demeter-sante/queue/settings" {
		t.Fatalf("unexpected resize applied event: %#v", applied)
	}

	created := waitForPerformanceEventByTask(t, st, traceID, "demeter_worker_created")
	if created == nil {
		t.Fatal("expected worker created performance event")
	}
	if created.Component != "demeter" {
		t.Fatalf("unexpected worker created event: %#v", created)
	}
	if !strings.Contains(created.MetaJSON, `"queue_id":"2"`) {
		t.Fatalf("expected worker created meta to mention queue_id=2, got %#v", created.MetaJSON)
	}
}

func TestDemeterAudioTranscriptionWithRetryPersistsPerformanceEvent(t *testing.T) {
	withDemeterAudioRetryDelay(t, 10*time.Millisecond)

	st := openAPITestStore(t, "demeter-mistral-retry-observability.sqlite")
	backendperformance.RegisterSink(st)
	t.Cleanup(func() {
		backendperformance.RegisterSink(nil)
	})

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		time.Sleep(5 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		if attempts == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`service tier capacity exceeded`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"text":"ok","segments":[]}`))
	}))
	defer server.Close()

	app := &App{
		MistralClient: mistral.NewClient(server.URL, "key", time.Second, time.Second),
	}

	logCtx := newDemeterAudioLogContext(observability.WithTraceID(context.Background(), "mistral-retry-trace"))
	result, err := app.demeterAudioTranscriptionWithRetry(
		logCtx,
		"/api/v1/providers/demeter-sante/audio/transcriptions/backend",
		7,
		"backend_direct",
		12.5,
		true,
		app.EnsureDemeterQueueManager(),
		3,
		"operation-1",
		4,
		[]byte("test-body"),
		"multipart/form-data; boundary=test",
		9,
	)
	if err != nil {
		t.Fatalf("retry flow failed: %v", err)
	}
	if result.statusCode != http.StatusOK {
		t.Fatalf("expected final success status, got %d", result.statusCode)
	}

	retryEvent := waitForPerformanceEventByTask(t, st, "mistral-retry-trace", "mistral_audio_transcription_retry")
	if retryEvent == nil {
		t.Fatal("expected mistral retry performance event")
	}
	if retryEvent.Component != "demeter" || retryEvent.Route != "/api/v1/providers/demeter-sante/audio/transcriptions/backend" {
		t.Fatalf("unexpected retry event: %#v", retryEvent)
	}
	if retryEvent.DurationMS <= 0 {
		t.Fatalf("expected retry duration to be recorded, got %d", retryEvent.DurationMS)
	}
	if !strings.Contains(retryEvent.MetaJSON, `"queue_id":"3"`) || !strings.Contains(retryEvent.MetaJSON, `"operation_id":"operation-1"`) || !strings.Contains(retryEvent.MetaJSON, `"chunk_index":"4"`) {
		t.Fatalf("expected retry meta to include queue and chunk context, got %#v", retryEvent.MetaJSON)
	}
}

func TestDemeterAudioTranscriptionRetryPausesOtherLanes(t *testing.T) {
	withDemeterAudioRetryDelay(t, 20*time.Millisecond)

	attempts := int32(0)
	firstAttemptSeen := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&attempts, 1)
		w.Header().Set("Content-Type", "application/json")
		if attempt == 1 {
			close(firstAttemptSeen)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`service tier capacity exceeded`))
			return
		}
		time.Sleep(120 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"text":"ok","segments":[]}`))
	}))
	defer server.Close()

	app := &App{
		MistralClient: mistral.NewClient(server.URL, "key", time.Second, time.Second),
	}
	queueManager := &DemeterAudioQueueManager{}

	logCtx := newDemeterAudioLogContext(observability.WithTraceID(context.Background(), "mistral-retry-pause-trace"))
	type retryResult struct {
		result demeterAudioTranscriptionRelayResult
		err    error
	}
	resultCh := make(chan retryResult, 1)
	go func() {
		result, err := app.demeterAudioTranscriptionWithRetry(
			logCtx,
			"/api/v1/providers/demeter-sante/audio/transcriptions/backend",
			8,
			"backend_direct",
			12.5,
			true,
			queueManager,
			2,
			"operation-2",
			5,
			[]byte("test-body"),
			"multipart/form-data; boundary=test",
			9,
		)
		resultCh <- retryResult{result: result, err: err}
	}()

	select {
	case <-firstAttemptSeen:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the first Mistral attempt")
	}
	waitForRetryPauseState(t, queueManager, 2)

	waiterDone := make(chan struct{})
	go func() {
		if !queueManager.waitForMistralRetryPause(context.Background(), 1) {
			t.Error("pause waiter stopped unexpectedly")
			return
		}
		close(waiterDone)
	}()

	select {
	case <-waiterDone:
		t.Fatal("expected lane 1 to remain paused while lane 2 is retrying")
	case <-time.After(50 * time.Millisecond):
	}

	select {
	case res := <-resultCh:
		if res.err != nil {
			t.Fatalf("retry flow failed: %v", res.err)
		}
		if res.result.statusCode != http.StatusOK {
			t.Fatalf("expected final success status, got %d", res.result.statusCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for retry flow to complete")
	}

	select {
	case <-waiterDone:
	case <-time.After(2 * time.Second):
		t.Fatal("lane 1 did not resume after the retry finished")
	}
}

func TestDemeterQueueSnapshotUsesLiveOperationProgressAndRetryPauseState(t *testing.T) {
	st := openAPITestStore(t, "demeter-queue-snapshot-live-state.sqlite")
	app := &App{Store: st}
	manager := app.EnsureDemeterQueueManager()
	t.Cleanup(manager.Stop)

	now := time.Now().UTC()
	org := createTestOrganization(t, st, "Demo Org", "demo-org", "active")
	user := createTestUser(t, st, org.ID, "worker@example.com", "hashed-password", "active")
	record := &store.DemeterAudioTranscriptionOperationRecord{
		OperationID:    "op-live-1",
		OrganizationID: org.ID,
		UserID:         user.ID,
		QueueID:        1,
		Status:         store.DemeterAudioTranscriptionOperationStatusRunning,
		Stage:          "chunk_completed",
		ChunkIndex:     4,
		ChunkCount:     10,
		Progress:       0.4,
		StatusCode:     http.StatusAccepted,
		CreatedAt:      now.Add(-time.Minute),
		UpdatedAt:      now,
	}
	if err := st.CreateDemeterAudioTranscriptionOperation(context.Background(), record); err != nil {
		t.Fatalf("failed to seed operation: %v", err)
	}

	manager.mu.Lock()
	state := manager.ensureLaneStateLocked(1)
	state.Open = true
	state.WorkerRunning = true
	state.CurrentOperationID = "op-live-1"
	state.CurrentStatus = "running"
	state.CurrentStage = "running"
	state.CurrentChunkIndex = 0
	state.CurrentChunkCount = 10
	state.CurrentProgress = 0
	state.LastError = "stale"
	manager.retryPaused = true
	manager.retryPausedLaneID = 1
	manager.retryPausedOperationID = "op-live-1"
	manager.retryPausedChunkIndex = 4
	manager.retryPausedSince = now.Add(-30 * time.Second)
	manager.mu.Unlock()

	snapshot, err := manager.Snapshot(context.Background(), 10)
	if err != nil {
		t.Fatalf("failed to load queue snapshot: %v", err)
	}
	if !snapshot.Summary.RetryPaused {
		t.Fatal("expected retry pause state to be exposed")
	}
	if snapshot.Summary.RetryPausedLaneID != 1 || snapshot.Summary.RetryPausedOperationID != "op-live-1" || snapshot.Summary.RetryPausedChunkIndex != 4 {
		t.Fatalf("unexpected retry pause snapshot: %#v", snapshot.Summary)
	}
	if snapshot.Summary.RetryPausedSince == "" {
		t.Fatal("expected retry pause timestamp to be exposed")
	}
	if len(snapshot.Workers) != 1 {
		t.Fatalf("expected one worker snapshot, got %d", len(snapshot.Workers))
	}
	worker := snapshot.Workers[0]
	if worker.CurrentOperationID != "op-live-1" {
		t.Fatalf("expected live operation id to be preserved, got %#v", worker.CurrentOperationID)
	}
	if worker.CurrentChunkIndex != 4 || worker.CurrentChunkCount != 10 {
		t.Fatalf("expected worker chunk progress to follow the live operation, got %#v", worker)
	}
	if worker.CurrentProgress != 0.4 {
		t.Fatalf("expected worker progress to follow the live operation, got %#v", worker.CurrentProgress)
	}
	if worker.CurrentStage != "chunk_completed" || worker.CurrentStatus != "running" {
		t.Fatalf("expected worker status to follow the live operation, got %#v", worker)
	}
	if worker.LastError != "" {
		t.Fatalf("expected worker last error to be cleared from the live operation, got %#v", worker.LastError)
	}
}

func TestDemeterQueueSnapshotIncludesFullOperationTableRows(t *testing.T) {
	st := openAPITestStore(t, "demeter-queue-snapshot-full-table.sqlite")
	app := &App{Store: st}
	manager := app.EnsureDemeterQueueManager()
	t.Cleanup(manager.Stop)

	now := time.Now().UTC()
	org := createTestOrganization(t, st, "Demo Org", "demo-org-full", "active")
	user := createTestUser(t, st, org.ID, "full@example.com", "hashed-password", "active")

	records := []*store.DemeterAudioTranscriptionOperationRecord{
		{
			OperationID:    "op-pending",
			OrganizationID: org.ID,
			UserID:         user.ID,
			QueueID:        1,
			Status:         store.DemeterAudioTranscriptionOperationStatusPending,
			Stage:          "queued",
			ChunkIndex:     0,
			ChunkCount:     4,
			Progress:       0,
			StatusCode:     http.StatusAccepted,
			CreatedAt:      now.Add(-4 * time.Minute),
			UpdatedAt:      now.Add(-4 * time.Minute),
			QueuePayloadJSON: sql.NullString{
				String: `{"sourceMode":"backend","chunkCount":4}`,
				Valid:  true,
			},
		},
		{
			OperationID:    "op-running",
			OrganizationID: org.ID,
			UserID:         user.ID,
			QueueID:        1,
			Status:         store.DemeterAudioTranscriptionOperationStatusRunning,
			Stage:          "chunk_completed",
			ChunkIndex:     2,
			ChunkCount:     4,
			Progress:       0.5,
			StatusCode:     http.StatusAccepted,
			CreatedAt:      now.Add(-3 * time.Minute),
			UpdatedAt:      now.Add(-2 * time.Minute),
		},
		{
			OperationID:    "op-completed",
			OrganizationID: org.ID,
			UserID:         user.ID,
			QueueID:        2,
			Status:         store.DemeterAudioTranscriptionOperationStatusCompleted,
			Stage:          "completed",
			ChunkIndex:     4,
			ChunkCount:     4,
			Progress:       1,
			StatusCode:     http.StatusOK,
			CreatedAt:      now.Add(-2 * time.Minute),
			UpdatedAt:      now.Add(-time.Minute),
			ResponseJSON: sql.NullString{
				String: `{"text":"done","segments":[]}`,
				Valid:  true,
			},
			FinishedAt: sql.NullTime{Time: now.Add(-time.Minute), Valid: true},
		},
		{
			OperationID:    "op-failed",
			OrganizationID: org.ID,
			UserID:         user.ID,
			QueueID:        0,
			Status:         store.DemeterAudioTranscriptionOperationStatusFailed,
			Stage:          "failed",
			ChunkIndex:     1,
			ChunkCount:     4,
			Progress:       0.25,
			StatusCode:     http.StatusInternalServerError,
			CreatedAt:      now.Add(-90 * time.Second),
			UpdatedAt:      now.Add(-90 * time.Second),
			LastError: sql.NullString{
				String: "upstream unavailable",
				Valid:  true,
			},
			FinishedAt: sql.NullTime{Time: now.Add(-90 * time.Second), Valid: true},
		},
	}
	for _, record := range records {
		if err := st.CreateDemeterAudioTranscriptionOperation(context.Background(), record); err != nil {
			t.Fatalf("failed to create record %s: %v", record.OperationID, err)
		}
	}

	snapshot, err := manager.Snapshot(context.Background(), 10)
	if err != nil {
		t.Fatalf("failed to load queue snapshot: %v", err)
	}
	if len(snapshot.Operations) != 2 {
		t.Fatalf("expected 2 live queue operations, got %d", len(snapshot.Operations))
	}
	if len(snapshot.AllOperations) != 4 {
		t.Fatalf("expected 4 operations in the full table snapshot, got %d", len(snapshot.AllOperations))
	}

	var completed, failed *demeterAudioQueueOperationSnapshot
	for i := range snapshot.AllOperations {
		op := &snapshot.AllOperations[i]
		switch op.OperationID {
		case "op-completed":
			completed = op
		case "op-failed":
			failed = op
		}
	}
	if completed == nil {
		t.Fatal("expected completed operation to be exposed in the full table snapshot")
	}
	if completed.OrganizationID != org.ID || completed.UserID != user.ID {
		t.Fatalf("expected completed operation ownership to be exposed, got %#v", completed)
	}
	if completed.QueuePayloadJSON == "" || completed.ResponseJSON == "" || completed.FinishedAt == "" {
		t.Fatalf("expected completed operation details to include payload, response and finishedAt, got %#v", completed)
	}
	if !strings.Contains(completed.QueuePayloadJSON, `"sourceMode":"backend"`) {
		t.Fatalf("expected completed payload json to be preserved, got %#v", completed.QueuePayloadJSON)
	}
	if !strings.Contains(completed.ResponseJSON, `"text":"done"`) {
		t.Fatalf("expected completed response json to be preserved, got %#v", completed.ResponseJSON)
	}
	if failed == nil {
		t.Fatal("expected failed operation to be exposed in the full table snapshot")
	}
	if failed.LastError != "upstream unavailable" {
		t.Fatalf("expected failed last error to be exposed, got %#v", failed.LastError)
	}
}

func withDemeterAudioRetryDelay(t *testing.T, delay time.Duration) {
	t.Helper()

	original := demeterAudioTranscriptionRetryDelayForAttempt
	demeterAudioTranscriptionRetryDelayForAttempt = func(attempt int) time.Duration {
		return delay
	}
	t.Cleanup(func() {
		demeterAudioTranscriptionRetryDelayForAttempt = original
	})
}

func waitForRetryPauseState(t *testing.T, manager *DemeterAudioQueueManager, laneID int) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		manager.mu.Lock()
		paused, ownerLaneID, _, _, _ := manager.mistralRetryPauseStateLocked()
		manager.mu.Unlock()
		if paused && ownerLaneID == laneID {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for retry pause on lane %d", laneID)
}

func waitForPerformanceEventByTask(t *testing.T, st *store.Store, traceID, task string) *store.PerformanceEvent {
	t.Helper()

	time.Sleep(120 * time.Millisecond)
	for i := 0; i < 30; i++ {
		var (
			event               store.PerformanceEvent
			userID, orgID, meta sql.NullString
		)
		err := st.DB.QueryRowContext(context.Background(), `
			SELECT event_id, trace_id, user_id, organization_id, surface, component, task, status, duration_ms, route, meta_json, occurred_at, day, created_at
			FROM performance_events
			WHERE trace_id = ? AND task = ?
			ORDER BY occurred_at DESC, event_id DESC
			LIMIT 1
		`, traceID, task).Scan(
			&event.EventID,
			&event.TraceID,
			&userID,
			&orgID,
			&event.Surface,
			&event.Component,
			&event.Task,
			&event.Status,
			&event.DurationMS,
			&event.Route,
			&meta,
			&event.OccurredAt,
			&event.Day,
			&event.CreatedAt,
		)
		if err == nil {
			event.UserID = userID.String
			event.OrganizationID = orgID.String
			event.MetaJSON = meta.String
			return &event
		}
		if err != sql.ErrNoRows {
			t.Fatalf("failed to query performance event: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for performance event %q for trace %q", task, traceID)
	return nil
}
