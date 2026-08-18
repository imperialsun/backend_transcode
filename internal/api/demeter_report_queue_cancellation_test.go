package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"demeter-backend/internal/mistral"
	"demeter-backend/internal/reports"
	"demeter-backend/internal/store"
)

type blockingReportRoundTripper struct {
	started   chan struct{}
	cancelled chan struct{}
}

func (t *blockingReportRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	close(t.started)
	<-req.Context().Done()
	close(t.cancelled)
	return nil, req.Context().Err()
}

func TestDemeterReportQueueCancellationStopsRunningWorker(t *testing.T) {
	st := openAPITestStore(t, "demeter-report-queue-cancellation.sqlite")
	org := createTestOrganization(t, st, "Cancel Org", "cancel-org", "active")
	user := createTestUser(t, st, org.ID, "cancel-worker@example.com", "hashed-password", "active")
	transport := &blockingReportRoundTripper{
		started:   make(chan struct{}),
		cancelled: make(chan struct{}),
	}

	app := &App{
		Store:         st,
		MistralClient: mistral.NewClient("http://mistral.test", "key", time.Minute, time.Minute),
	}
	app.MistralClient.HTTP = &http.Client{Transport: transport}
	manager := app.EnsureDemeterReportQueueManager()
	now := time.Now().UTC()
	payload := &demeterReportQueueOperationPayload{
		TraceID:     "trace-cancel-running",
		Route:       "/providers/demeter-sante/report/operations",
		Seq:         1,
		Kind:        demeterReportQueueKindClarification,
		SourceKind:  reports.ReportSourceWordNote,
		SourceText:  "note abrégée",
		Format:      reports.ReportFormatCRS,
		ModelID:     "mistral-medium-latest",
		Temperature: 0,
		MaxTokens:   512,
		CreatedAt:   now,
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}
	operation := &store.DemeterReportOperationRecord{
		OperationID:      "op-cancel-running",
		OrganizationID:   org.ID,
		UserID:           user.ID,
		QueueID:          1,
		Status:           store.DemeterReportOperationStatusRunning,
		Stage:            "running",
		FormatCount:      1,
		QueuePayloadJSON: sql.NullString{String: string(rawPayload), Valid: true},
		StatusCode:       http.StatusAccepted,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := st.CreateDemeterReportOperation(context.Background(), operation); err != nil {
		t.Fatalf("failed to create operation: %v", err)
	}

	workerDone := make(chan error, 1)
	go func() {
		workerDone <- manager.processClaimedOperation(operation, payload, 1)
	}()
	select {
	case <-transport.started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not reach upstream request")
	}

	if _, err := st.CancelDemeterReportOperation(context.Background(), operation.OperationID, org.ID, user.ID, time.Now().UTC()); err != nil {
		t.Fatalf("failed to cancel running operation: %v", err)
	}
	cancelDemeterReportOperationContext(operation.OperationID)

	select {
	case <-transport.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not reach upstream request")
	}
	select {
	case err := <-workerDone:
		if err != nil {
			t.Fatalf("cancelled worker returned an error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled worker did not finish")
	}

	final, err := st.GetDemeterReportOperation(context.Background(), operation.OperationID, org.ID, user.ID)
	if err != nil {
		t.Fatalf("failed to reload operation: %v", err)
	}
	if final.Status != store.DemeterReportOperationStatusCancelled {
		t.Fatalf("expected cancellation to remain terminal, got %+v", final)
	}
}
