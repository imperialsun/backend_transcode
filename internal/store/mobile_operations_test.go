package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestMobileOperationLifecycleReplayAndPurge(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t, "mobile-ops.sqlite")
	org := createOrg(t, st, "Mobile Org", "mobile-org", "active")
	user := createUserWithPassword(t, st, org.ID, "mobile@example.com", "Pass1234!", "active")
	now := time.Now().UTC()
	operationID := "mobile-op-1"

	created, err := st.CreateMobileOperationIfAbsent(ctx, &MobileOperationRecord{
		OperationID:    operationID,
		OrganizationID: org.ID,
		UserID:         user.ID,
		Kind:           "report_email",
		Status:         MobileOperationStatusRunning,
		StatusCode:     http.StatusAccepted,
		Stage:          "generation",
		Progress:       0.25,
		CreatedAt:      now,
		UpdatedAt:      now,
	})
	if err != nil {
		t.Fatalf("CreateMobileOperationIfAbsent failed: %v", err)
	}
	if created == nil || !created.Created || created.Record.Status != MobileOperationStatusRunning {
		t.Fatalf("unexpected create result: %#v", created)
	}

	replayed, err := st.CreateMobileOperationIfAbsent(ctx, &MobileOperationRecord{
		OperationID:    operationID,
		OrganizationID: org.ID,
		UserID:         user.ID,
		Kind:           "report_email",
		CreatedAt:      now,
		UpdatedAt:      now,
	})
	if err != nil {
		t.Fatalf("replay CreateMobileOperationIfAbsent failed: %v", err)
	}
	if replayed.Created {
		t.Fatal("expected idempotent replay to reuse the operation")
	}

	responseJSON := json.RawMessage(`{"operationId":"mobile-op-1","files":[{"filename":"transcription.docx","contentType":"application/vnd.openxmlformats-officedocument.wordprocessingml.document","sizeBytes":12}]}`)
	completed, err := st.CompleteMobileOperation(ctx, operationID, org.ID, user.ID, http.StatusOK, responseJSON, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("CompleteMobileOperation failed: %v", err)
	}
	if completed.Status != MobileOperationStatusCompleted || !completed.ResponseJSON.Valid {
		t.Fatalf("unexpected completed record: %#v", completed)
	}

	if purged, err := st.PurgeExpiredMobileOperations(ctx, now.Add(time.Hour)); err != nil || purged != 0 {
		t.Fatalf("fresh purge = %d, %v; want 0, nil", purged, err)
	}
	if purged, err := st.PurgeExpiredMobileOperations(ctx, now.Add(25*time.Hour)); err != nil || purged == 0 {
		t.Fatalf("expired purge = %d, %v; want deleted row", purged, err)
	}
	if _, err := st.GetMobileOperation(ctx, operationID, org.ID, user.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected deleted operation, got %v", err)
	}
}

func TestMobileOperationPurgeMarksStaleRunningAsFailed(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t, "mobile-ops-stale.sqlite")
	org := createOrg(t, st, "Mobile Stale Org", "mobile-stale-org", "active")
	user := createUserWithPassword(t, st, org.ID, "mobile-stale@example.com", "Pass1234!", "active")
	now := time.Now().UTC()
	operationID := "mobile-stale-op"

	if _, err := st.CreateMobileOperationIfAbsent(ctx, &MobileOperationRecord{
		OperationID:    operationID,
		OrganizationID: org.ID,
		UserID:         user.ID,
		Kind:           "audio_report_email",
		Status:         MobileOperationStatusRunning,
		StatusCode:     http.StatusAccepted,
		Stage:          "transcription",
		CreatedAt:      now,
		UpdatedAt:      now,
	}); err != nil {
		t.Fatalf("CreateMobileOperationIfAbsent failed: %v", err)
	}

	if purged, err := st.PurgeExpiredMobileOperations(ctx, now.Add(25*time.Hour)); err != nil || purged == 0 {
		t.Fatalf("stale purge = %d, %v; want updated row", purged, err)
	}
	record, err := st.GetMobileOperation(ctx, operationID, org.ID, user.ID)
	if err != nil {
		t.Fatalf("GetMobileOperation failed: %v", err)
	}
	if record.Status != MobileOperationStatusFailed || record.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("unexpected stale record: %#v", record)
	}
}
