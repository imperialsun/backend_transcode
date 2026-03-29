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

func TestMeetingFinalizeOperationLifecycleReplayAndPurge(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t, "meeting-finalize-ops.sqlite")

	org := createOrg(t, st, "Ops Org", "ops-org", "active")
	user := createUserWithPassword(t, st, org.ID, "ops@example.com", "ChangeMe123!", "active")

	now := time.Date(2026, 3, 29, 10, 0, 0, 0, time.UTC)
	operationID := "meeting-finalize-op-1"

	claim, err := st.ClaimMeetingFinalizeOperation(ctx, operationID, org.ID, user.ID, now)
	if err != nil {
		t.Fatalf("ClaimMeetingFinalizeOperation failed: %v", err)
	}
	if !claim.Claimed || claim.Record == nil || claim.Record.Status != MeetingFinalizeOperationStatusPending {
		t.Fatalf("unexpected claim result: %+v", claim)
	}

	responseJSON := json.RawMessage(`{"operationId":"meeting-finalize-op-1","meetingTitle":"Réunion qualité","sentTo":"u@example.com","sentToEmails":["u@example.com"]}`)
	completed, err := st.CompleteMeetingFinalizeOperation(ctx, operationID, org.ID, user.ID, 200, responseJSON, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("CompleteMeetingFinalizeOperation failed: %v", err)
	}
	if completed == nil || completed.Status != MeetingFinalizeOperationStatusCompleted {
		t.Fatalf("unexpected completed record: %+v", completed)
	}

	record, err := st.GetMeetingFinalizeOperation(ctx, operationID, org.ID, user.ID, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("GetMeetingFinalizeOperation failed: %v", err)
	}
	if record.Status != MeetingFinalizeOperationStatusCompleted || record.StatusCode != 200 {
		t.Fatalf("unexpected record after completion: %+v", record)
	}
	if !record.ResponseJSON.Valid || record.ResponseJSON.String == "" {
		t.Fatalf("expected stored response payload, got %+v", record)
	}

	replay, err := st.ClaimMeetingFinalizeOperation(ctx, operationID, org.ID, user.ID, now.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("replay claim failed: %v", err)
	}
	if replay.Claimed {
		t.Fatalf("expected replay claim to reuse stored result, got %+v", replay)
	}
	if replay.Record == nil || replay.Record.Status != MeetingFinalizeOperationStatusCompleted {
		t.Fatalf("unexpected replay record: %+v", replay)
	}

	purged, err := st.PurgeExpiredMeetingFinalizeOperations(ctx, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("PurgeExpiredMeetingFinalizeOperations fresh window failed: %v", err)
	}
	if purged != 0 {
		t.Fatalf("expected no purge while operation is still fresh, got %d", purged)
	}

	record, err = st.GetMeetingFinalizeOperation(ctx, operationID, org.ID, user.ID, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("expected completed record to remain available before retention expiry: %v", err)
	}
	if record.Status != MeetingFinalizeOperationStatusCompleted {
		t.Fatalf("unexpected record before purge expiry: %+v", record)
	}

	purged, err = st.PurgeExpiredMeetingFinalizeOperations(ctx, now.Add(meetingFinalizeOperationRetention+time.Minute))
	if err != nil {
		t.Fatalf("PurgeExpiredMeetingFinalizeOperations expired window failed: %v", err)
	}
	if purged == 0 {
		t.Fatal("expected expired operation to be purged")
	}

	if _, err := st.GetMeetingFinalizeOperation(ctx, operationID, org.ID, user.ID, now.Add(meetingFinalizeOperationRetention+time.Minute)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected purged operation to be missing, got %v", err)
	}
}

func TestMeetingFinalizeOperationPurgeMarksStalePendingAsFailed(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t, "meeting-finalize-pending.sqlite")

	org := createOrg(t, st, "Pending Org", "pending-org", "active")
	user := createUserWithPassword(t, st, org.ID, "pending@example.com", "ChangeMe123!", "active")

	now := time.Date(2026, 3, 29, 10, 0, 0, 0, time.UTC)
	freshOperationID := "meeting-finalize-fresh"
	staleOperationID := "meeting-finalize-stale"

	if _, err := st.ClaimMeetingFinalizeOperation(ctx, freshOperationID, org.ID, user.ID, now); err != nil {
		t.Fatalf("ClaimMeetingFinalizeOperation fresh failed: %v", err)
	}
	if purged, err := st.PurgeExpiredMeetingFinalizeOperations(ctx, now.Add(time.Hour)); err != nil {
		t.Fatalf("PurgeExpiredMeetingFinalizeOperations fresh failed: %v", err)
	} else if purged != 0 {
		t.Fatalf("expected fresh pending operation to survive purge, got %d", purged)
	}
	if record, err := st.GetMeetingFinalizeOperation(ctx, freshOperationID, org.ID, user.ID, now.Add(time.Hour)); err != nil {
		t.Fatalf("expected fresh pending operation to remain readable: %v", err)
	} else if record.Status != MeetingFinalizeOperationStatusPending {
		t.Fatalf("unexpected fresh pending record: %+v", record)
	}

	if _, err := st.ClaimMeetingFinalizeOperation(ctx, staleOperationID, org.ID, user.ID, now); err != nil {
		t.Fatalf("ClaimMeetingFinalizeOperation stale failed: %v", err)
	}
	purged, err := st.PurgeExpiredMeetingFinalizeOperations(ctx, now.Add(meetingFinalizeOperationRetention+time.Minute))
	if err != nil {
		t.Fatalf("PurgeExpiredMeetingFinalizeOperations stale failed: %v", err)
	}
	if purged == 0 {
		t.Fatal("expected stale pending operation to be transitioned to failed")
	}

	record, err := st.GetMeetingFinalizeOperation(ctx, staleOperationID, org.ID, user.ID, now.Add(meetingFinalizeOperationRetention+time.Minute))
	if err != nil {
		t.Fatalf("expected stale pending operation to remain readable as failed: %v", err)
	}
	if record.Status != MeetingFinalizeOperationStatusFailed || record.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("unexpected stale record after purge: %+v", record)
	}
	if !record.ResponseJSON.Valid || record.ResponseJSON.String == "" {
		t.Fatalf("expected timeout payload to be stored, got %+v", record)
	}
}
