package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"demeter-backend/internal/mistral"
	"demeter-backend/internal/reports"
	"demeter-backend/internal/store"
)

func TestDemeterReportRetryDelayMatchesAudioBackoff(t *testing.T) {
	expected := []time.Duration{
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		32 * time.Second,
		64 * time.Second,
		128 * time.Second,
		256 * time.Second,
		512 * time.Second,
	}
	for attempt, want := range expected {
		got := demeterReportRetryDelayForAttempt(attempt + 1)
		if got != want {
			t.Fatalf("attempt %d: expected %s, got %s", attempt+1, want, got)
		}
	}
}

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

func TestDemeterReportQueueRebalancesPendingOperationsToIdleLane(t *testing.T) {
	ctx := context.Background()
	st := openAPITestStore(t, "demeter-report-rebalance.sqlite")
	org := createTestOrganization(t, st, "Rebalance Org", "rebalance-org", "active")
	user := createTestUser(t, st, org.ID, "rebalance@example.com", "hashed-password", "active")
	now := time.Now().UTC()

	createReportQueueOperation(t, st, org.ID, user.ID, "running-lane-1", 1, store.DemeterReportOperationStatusRunning, now)
	for index := 1; index <= 4; index++ {
		createReportQueueOperation(t, st, org.ID, user.ID, fmt.Sprintf("pending-lane-1-%d", index), 1, store.DemeterReportOperationStatusPending, now.Add(time.Duration(index)*time.Second))
	}

	manager := &DemeterReportQueueManager{
		app: &App{Store: st},
		lanes: map[int]*demeterReportQueueLaneState{
			1: {ID: 1, Open: true, WorkerRunning: true},
			2: {ID: 2, Open: true, WorkerRunning: true},
		},
		laneWakeCh: map[int]chan struct{}{},
	}
	lane2Wake := manager.laneWakeChannel(2)

	changed, err := manager.rebalancePendingOperations(ctx)
	if err != nil {
		t.Fatalf("rebalance failed: %v", err)
	}
	if !changed {
		t.Fatal("expected pending operations to be moved")
	}

	select {
	case <-lane2Wake:
	default:
		t.Fatal("expected lane 2 to be woken after receiving pending work")
	}

	lane2Pending, err := st.ListDemeterReportOperations(ctx, ptrInt(2), []string{store.DemeterReportOperationStatusPending}, 100)
	if err != nil {
		t.Fatalf("failed to list lane 2 pending operations: %v", err)
	}
	if len(lane2Pending) == 0 {
		t.Fatal("expected idle lane 2 to receive pending operations")
	}

	running, err := st.GetDemeterReportOperation(ctx, "running-lane-1", org.ID, user.ID)
	if err != nil {
		t.Fatalf("failed to reload running operation: %v", err)
	}
	if running.QueueID != 1 || running.Status != store.DemeterReportOperationStatusRunning {
		t.Fatalf("running operation must not move, got %+v", running)
	}
}

func TestDemeterReportQueueRebalanceIgnoresDrainingLanes(t *testing.T) {
	ctx := context.Background()
	st := openAPITestStore(t, "demeter-report-rebalance-draining.sqlite")
	org := createTestOrganization(t, st, "Drain Org", "drain-org", "active")
	user := createTestUser(t, st, org.ID, "drain@example.com", "hashed-password", "active")
	now := time.Now().UTC()
	for index := 1; index <= 3; index++ {
		createReportQueueOperation(t, st, org.ID, user.ID, fmt.Sprintf("pending-drain-%d", index), 1, store.DemeterReportOperationStatusPending, now.Add(time.Duration(index)*time.Second))
	}

	manager := &DemeterReportQueueManager{
		app: &App{Store: st},
		lanes: map[int]*demeterReportQueueLaneState{
			1: {ID: 1, Open: true, WorkerRunning: true},
			2: {ID: 2, Open: true, Draining: true, WorkerRunning: true},
		},
		laneWakeCh: map[int]chan struct{}{},
	}

	changed, err := manager.rebalancePendingOperations(ctx)
	if err != nil {
		t.Fatalf("rebalance failed: %v", err)
	}
	if changed {
		t.Fatal("did not expect work to move to a draining lane")
	}
	lane2Pending, err := st.ListDemeterReportOperations(ctx, ptrInt(2), []string{store.DemeterReportOperationStatusPending}, 100)
	if err != nil {
		t.Fatalf("failed to list lane 2 pending operations: %v", err)
	}
	if len(lane2Pending) != 0 {
		t.Fatalf("draining lane should not receive work, got %+v", lane2Pending)
	}
}

func TestDemeterReportQueueRoutesCRNWorkToDedicatedLanes(t *testing.T) {
	ctx := context.Background()
	st := openAPITestStore(t, "demeter-report-crn-lanes.sqlite")
	org := createTestOrganization(t, st, "CRN Lanes Org", "crn-lanes-org", "active")
	user := createTestUser(t, st, org.ID, "crn-lanes@example.com", "hashed-password", "active")
	now := time.Now().UTC()

	createReportQueueOperationWithFormat(t, st, org.ID, user.ID, "report-crs", 0, store.DemeterReportOperationStatusPending, reports.ReportFormatCRS, now)
	createReportQueueOperationWithFormat(t, st, org.ID, user.ID, "report-crn", 0, store.DemeterReportOperationStatusPending, reports.ReportFormatCRN, now.Add(time.Second))

	manager := &DemeterReportQueueManager{
		app:        &App{Store: st},
		lanes:      map[int]*demeterReportQueueLaneState{},
		laneWakeCh: map[int]chan struct{}{},
	}
	manager.mu.Lock()
	manager.parallelism = 1
	manager.crnParallelism = 1
	manager.applyLaneParallelismLocked(1, 1)
	manager.mu.Unlock()

	changed, err := manager.rebalancePendingOperations(ctx)
	if err != nil {
		t.Fatalf("rebalance failed: %v", err)
	}
	if !changed {
		t.Fatal("expected pending operations to be routed")
	}

	standard, err := st.GetDemeterReportOperation(ctx, "report-crs", org.ID, user.ID)
	if err != nil {
		t.Fatalf("failed to reload standard report: %v", err)
	}
	if standard.QueueID != 1 {
		t.Fatalf("expected standard report on lane 1, got %d", standard.QueueID)
	}

	crn, err := st.GetDemeterReportOperation(ctx, "report-crn", org.ID, user.ID)
	if err != nil {
		t.Fatalf("failed to reload CRN report: %v", err)
	}
	if crn.QueueID != reportCRNLaneID(1) {
		t.Fatalf("expected CRN report on dedicated lane %d, got %d", reportCRNLaneID(1), crn.QueueID)
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

func createReportQueueOperation(t *testing.T, st *store.Store, orgID, userID, operationID string, queueID int, status string, createdAt time.Time) {
	t.Helper()
	createReportQueueOperationWithFormat(t, st, orgID, userID, operationID, queueID, status, reports.ReportFormatCRS, createdAt)
}

func createReportQueueOperationWithFormat(t *testing.T, st *store.Store, orgID, userID, operationID string, queueID int, status string, format reports.ReportFormat, createdAt time.Time) {
	t.Helper()
	payload, err := json.Marshal(demeterReportQueueOperationPayload{
		Kind:       demeterReportQueueKindReport,
		SourceText: "source text",
		Format:     format,
		ModelID:    "mistral-medium-latest",
		CreatedAt:  createdAt,
	})
	if err != nil {
		t.Fatalf("failed to marshal queue payload: %v", err)
	}
	if err := st.CreateDemeterReportOperation(context.Background(), &store.DemeterReportOperationRecord{
		OperationID:      operationID,
		OrganizationID:   orgID,
		UserID:           userID,
		QueueID:          queueID,
		QueuePayloadJSON: sql.NullString{String: string(payload), Valid: true},
		Status:           status,
		Stage:            status,
		FormatIndex:      0,
		FormatCount:      1,
		Progress:         0,
		StatusCode:       http.StatusAccepted,
		CreatedAt:        createdAt,
		UpdatedAt:        createdAt,
	}); err != nil {
		t.Fatalf("failed to create report operation %s: %v", operationID, err)
	}
}

func ptrInt(value int) *int {
	return &value
}
