package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"demeter-backend/internal/mistral"
	"demeter-backend/internal/reports"
)

func TestDemeterReportQueueSnapshotChangesAreBroadcast(t *testing.T) {
	manager := &DemeterReportQueueManager{}
	changes, unsubscribe := manager.subscribeSnapshotChanges()
	defer unsubscribe()

	manager.notifySnapshotChanged()

	select {
	case <-changes:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected snapshot change notification")
	}

	unsubscribe()
	manager.notifySnapshotChanged()

	select {
	case <-changes:
		t.Fatal("did not expect notification after unsubscribe")
	default:
	}
}

func TestDemeterReportQueueRetryPauseWaiterResumesWhenPauseFinishes(t *testing.T) {
	manager := &DemeterReportQueueManager{}

	if !manager.startMistralRetryPause(1, "operation-1", 0) {
		t.Fatal("failed to start retry pause")
	}

	waiterDone := make(chan bool, 1)
	go func() {
		waiterDone <- manager.waitForMistralRetryPause(context.Background(), 2)
	}()

	select {
	case <-waiterDone:
		t.Fatal("expected non-owner lane to block while retry pause is active")
	case <-time.After(20 * time.Millisecond):
	}

	if !manager.finishMistralRetryPause(1, "operation-1", 0) {
		t.Fatal("failed to finish retry pause")
	}

	select {
	case ok := <-waiterDone:
		if !ok {
			t.Fatal("expected retry pause waiter to resume successfully")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("retry pause waiter did not resume after finish signal")
	}
}

func TestDemeterReportQueueRetriesLaneAndPausesOtherLanesOn429(t *testing.T) {
	var upstreamCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&upstreamCalls, 1)
		if attempt == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("rate limited"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{
				map[string]any{
					"message": map[string]any{
						"content": `{"title":"Compte rendu CRN","sections":[{"heading":"Résumé","paragraphs":["Test"]}]}`,
					},
				},
			},
		})
	}))
	defer server.Close()

	app := &App{
		MistralClient: mistral.NewClient(server.URL, "test-key", 5*time.Second, 5*time.Second),
	}
	manager := app.EnsureDemeterReportQueueManager()

	payload := &demeterReportQueueOperationPayload{
		TraceID:      "trace-crn-429",
		Route:        "/providers/demeter-sante/report/operations",
		Seq:          7,
		MeetingTitle: "Réunion CRN",
		Participants: []string{"Alice"},
		SourceText:   "source text",
		Format:       reports.ReportFormatCRN,
		DetailLevel:  reports.ReportDetailStandard,
		ModelID:      "mistral-medium-latest",
		Temperature:  0,
		MaxTokens:    1024,
		CreatedAt:    time.Now().UTC(),
	}

	type result struct {
		resp   *demeterReportResult
		status int
		err    error
	}
	lane1Done := make(chan result, 1)
	go func() {
		resp, status, err := manager.generateReportWithRetry(context.Background(), 1, "op-lane-1", payload)
		lane1Done <- result{resp: resp, status: status, err: err}
	}()

	waitForDemeterReportRetryPause(t, manager, 1, "op-lane-1", 2*time.Second)

	lane2Done := make(chan result, 1)
	go func() {
		resp, status, err := manager.generateReportWithRetry(context.Background(), 2, "op-lane-2", payload)
		lane2Done <- result{resp: resp, status: status, err: err}
	}()

	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&upstreamCalls); got != 1 {
		t.Fatalf("expected lane 2 to remain blocked while retry pause is active, got %d upstream calls", got)
	}
	select {
	case <-lane2Done:
		t.Fatal("expected lane 2 to block until lane 1 succeeds")
	default:
	}

	select {
	case lane1Result := <-lane1Done:
		if lane1Result.err != nil {
			t.Fatalf("expected lane 1 retry to succeed, got error: %v", lane1Result.err)
		}
		if lane1Result.status != http.StatusOK || lane1Result.resp == nil {
			t.Fatalf("expected lane 1 success, got status=%d resp=%#v", lane1Result.status, lane1Result.resp)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for lane 1 retry to succeed")
	}

	select {
	case lane2Result := <-lane2Done:
		if lane2Result.err != nil {
			t.Fatalf("expected lane 2 to succeed after pause clears, got error: %v", lane2Result.err)
		}
		if lane2Result.status != http.StatusOK || lane2Result.resp == nil {
			t.Fatalf("expected lane 2 success after pause clears, got status=%d resp=%#v", lane2Result.status, lane2Result.resp)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for lane 2 to resume after lane 1 success")
	}

	if got := atomic.LoadInt32(&upstreamCalls); got != 3 {
		t.Fatalf("expected exactly three upstream calls (lane 1 retry + lane 2 after pause), got %d", got)
	}

	manager.mu.Lock()
	paused := manager.retryPaused
	manager.mu.Unlock()
	if paused {
		t.Fatal("expected retry pause to be cleared after successful retry")
	}
}

func TestDemeterReportQueueFinishRetryPauseWakesOpenLanes(t *testing.T) {
	manager := &DemeterReportQueueManager{
		lanes: map[int]*demeterReportQueueLaneState{
			1: {ID: 1, Open: true},
			2: {ID: 2, Open: true},
			3: {ID: 3},
		},
	}

	if !manager.startMistralRetryPause(1, "operation-1", 0) {
		t.Fatal("failed to start retry pause")
	}
	if !manager.finishMistralRetryPause(1, "operation-1", 0) {
		t.Fatal("failed to finish retry pause")
	}

	for _, laneID := range []int{1, 2} {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		if !manager.waitForLaneWorkAvailable(ctx, laneID, time.Hour) {
			cancel()
			t.Fatalf("expected lane %d to wake after retry pause", laneID)
		}
		cancel()
	}

	closedLaneCtx, closedLaneCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer closedLaneCancel()
	if manager.waitForLaneWorkAvailable(closedLaneCtx, 3, time.Hour) {
		t.Fatal("expected closed lane to remain asleep after retry pause")
	}
}

func waitForDemeterReportRetryPause(t *testing.T, manager *DemeterReportQueueManager, laneID int, operationID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		manager.mu.Lock()
		paused := manager.retryPaused && manager.retryPausedLaneID == laneID && manager.retryPausedOperationID == operationID
		manager.mu.Unlock()
		if paused {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for retry pause on lane %d", laneID)
}
