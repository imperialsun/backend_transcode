package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"demeter-backend/internal/backenderrors"
	"demeter-backend/internal/observability"
	"demeter-backend/internal/requestmeta"
)

func TestUpdateDemeterAudioTranscriptionOperationByID_IgnoresOwnership(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "demeter.sqlite"))
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	org, err := st.CreateOrganization(ctx, "Org", "org", "active")
	if err != nil {
		t.Fatalf("failed to create org: %v", err)
	}
	user, err := st.CreateUser(ctx, org.ID, "u@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	record := &DemeterAudioTranscriptionOperationRecord{
		OperationID:    "demeter-audio-test-operation",
		OrganizationID: org.ID,
		UserID:         user.ID,
		Status:         DemeterAudioTranscriptionOperationStatusRunning,
		Stage:          "queued",
		ChunkIndex:     0,
		ChunkCount:     2,
		Progress:       0,
		StatusCode:     202,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	if err := st.CreateDemeterAudioTranscriptionOperation(ctx, record); err != nil {
		t.Fatalf("failed to create record: %v", err)
	}

	if err := st.UpdateDemeterAudioTranscriptionOperationByID(ctx, &DemeterAudioTranscriptionOperationRecord{
		OperationID:    record.OperationID,
		OrganizationID: "org-2",
		UserID:         "user-2",
		Status:         DemeterAudioTranscriptionOperationStatusCompleted,
		Stage:          "completed",
		ChunkIndex:     2,
		ChunkCount:     2,
		Progress:       1,
		ResponseJSON:   sql.NullString{String: `{"text":"hello"}`, Valid: true},
		StatusCode:     200,
		UpdatedAt:      time.Now().UTC(),
		FinishedAt:     sql.NullTime{Time: time.Now().UTC(), Valid: true},
	}); err != nil {
		t.Fatalf("failed to update by id: %v", err)
	}

	updated, err := st.GetDemeterAudioTranscriptionOperation(ctx, record.OperationID, record.OrganizationID, record.UserID)
	if err != nil {
		t.Fatalf("failed to load updated record: %v", err)
	}
	if updated.Status != DemeterAudioTranscriptionOperationStatusCompleted {
		t.Fatalf("unexpected status: %+v", updated)
	}
	if updated.Stage != "completed" {
		t.Fatalf("unexpected stage: %+v", updated)
	}
	if !updated.ResponseJSON.Valid || updated.ResponseJSON.String != `{"text":"hello"}` {
		t.Fatalf("unexpected response json: %+v", updated)
	}
	if !updated.FinishedAt.Valid {
		t.Fatalf("expected finished at to be set: %+v", updated)
	}
}

func TestClaimNextPendingDemeterAudioTranscriptionOperationForQueueClaimsOnce(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "demeter-claim.sqlite"))
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	org, err := st.CreateOrganization(ctx, "Org", "org", "active")
	if err != nil {
		t.Fatalf("failed to create org: %v", err)
	}
	user, err := st.CreateUser(ctx, org.ID, "u@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	now := time.Now().UTC()
	record := &DemeterAudioTranscriptionOperationRecord{
		OperationID:      "demeter-audio-claim-once",
		OrganizationID:   org.ID,
		UserID:           user.ID,
		QueueID:          2,
		Status:           DemeterAudioTranscriptionOperationStatusPending,
		Stage:            "queued",
		ChunkCount:       1,
		QueuePayloadJSON: sql.NullString{String: `{"traceId":"trace"}`, Valid: true},
		StatusCode:       http.StatusAccepted,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := st.CreateDemeterAudioTranscriptionOperation(ctx, record); err != nil {
		t.Fatalf("failed to create operation: %v", err)
	}

	wrongLane, err := st.ClaimNextPendingDemeterAudioTranscriptionOperationForQueue(ctx, 1)
	if err != nil {
		t.Fatalf("claim from wrong lane failed: %v", err)
	}
	if wrongLane != nil {
		t.Fatalf("expected no record on lane 1, got %+v", wrongLane)
	}

	claimed, err := st.ClaimNextPendingDemeterAudioTranscriptionOperationForQueue(ctx, 2)
	if err != nil {
		t.Fatalf("claim failed: %v", err)
	}
	if claimed == nil {
		t.Fatal("expected record to be claimed")
	}
	if claimed.OperationID != record.OperationID || claimed.Status != DemeterAudioTranscriptionOperationStatusRunning || claimed.Stage != "running" {
		t.Fatalf("unexpected claimed record: %+v", claimed)
	}

	claimedAgain, err := st.ClaimNextPendingDemeterAudioTranscriptionOperationForQueue(ctx, 2)
	if err != nil {
		t.Fatalf("second claim failed: %v", err)
	}
	if claimedAgain != nil {
		t.Fatalf("expected second claim to return nil, got %+v", claimedAgain)
	}
}

func TestPurgeCompletedDemeterAudioTranscriptionOperationsRemovesOnlyCompletedRows(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "demeter-purge.sqlite"))
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	org, err := st.CreateOrganization(ctx, "Org", "org", "active")
	if err != nil {
		t.Fatalf("failed to create org: %v", err)
	}
	user, err := st.CreateUser(ctx, org.ID, "u@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	now := time.Now().UTC()
	completedID := "demeter-audio-completed-purge"
	runningID := "demeter-audio-running-purge"
	for _, record := range []*DemeterAudioTranscriptionOperationRecord{
		{
			OperationID:    completedID,
			OrganizationID: org.ID,
			UserID:         user.ID,
			Status:         DemeterAudioTranscriptionOperationStatusCompleted,
			Stage:          "completed",
			ChunkIndex:     1,
			ChunkCount:     1,
			Progress:       1,
			StatusCode:     200,
			CreatedAt:      now,
			UpdatedAt:      now,
			FinishedAt:     sql.NullTime{Time: now, Valid: true},
		},
		{
			OperationID:    runningID,
			OrganizationID: org.ID,
			UserID:         user.ID,
			Status:         DemeterAudioTranscriptionOperationStatusRunning,
			Stage:          "queued",
			ChunkIndex:     0,
			ChunkCount:     2,
			Progress:       0.5,
			StatusCode:     202,
			CreatedAt:      now,
			UpdatedAt:      now,
		},
	} {
		if err := st.CreateDemeterAudioTranscriptionOperation(ctx, record); err != nil {
			t.Fatalf("failed to create record %s: %v", record.OperationID, err)
		}
	}

	purged, err := st.PurgeCompletedDemeterAudioTranscriptionOperations(ctx)
	if err != nil {
		t.Fatalf("purge completed operations failed: %v", err)
	}
	if purged != 1 {
		t.Fatalf("unexpected purge count: got %d want 1", purged)
	}

	if _, err := st.GetDemeterAudioTranscriptionOperation(ctx, completedID, org.ID, user.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected completed record to be removed, got %v", err)
	}
	running, err := st.GetDemeterAudioTranscriptionOperation(ctx, runningID, org.ID, user.ID)
	if err != nil {
		t.Fatalf("failed to load running record: %v", err)
	}
	if running.Status != DemeterAudioTranscriptionOperationStatusRunning {
		t.Fatalf("expected running record to remain, got %+v", running)
	}
}

func TestDeleteDemeterAudioTranscriptionOperationRemovesRow(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "demeter-delete.sqlite"))
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	org, err := st.CreateOrganization(ctx, "Org", "org", "active")
	if err != nil {
		t.Fatalf("failed to create org: %v", err)
	}
	user, err := st.CreateUser(ctx, org.ID, "u@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	opID := "demeter-audio-delete-one"
	now := time.Now().UTC()
	if err := st.CreateDemeterAudioTranscriptionOperation(ctx, &DemeterAudioTranscriptionOperationRecord{
		OperationID:    opID,
		OrganizationID: org.ID,
		UserID:         user.ID,
		Status:         DemeterAudioTranscriptionOperationStatusCompleted,
		Stage:          "completed",
		ChunkIndex:     1,
		ChunkCount:     1,
		Progress:       1,
		StatusCode:     200,
		CreatedAt:      now,
		UpdatedAt:      now,
		FinishedAt:     sql.NullTime{Time: now, Valid: true},
	}); err != nil {
		t.Fatalf("failed to create record: %v", err)
	}

	deleted, err := st.DeleteDemeterAudioTranscriptionOperation(ctx, opID)
	if err != nil {
		t.Fatalf("delete operation failed: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("unexpected delete count: got %d want 1", deleted)
	}
	if _, err := st.GetDemeterAudioTranscriptionOperation(ctx, opID, org.ID, user.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected record to be removed, got %v", err)
	}
}

func TestGetDemeterAudioTranscriptionOperation_PersistsOwnershipMismatchEvent(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "demeter-ownership.sqlite")
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})
	backenderrors.RegisterSink(st)
	t.Cleanup(func() {
		backenderrors.RegisterSink(nil)
	})

	org, err := st.CreateOrganization(ctx, "Org", "org", "active")
	if err != nil {
		t.Fatalf("failed to create org: %v", err)
	}
	user1, err := st.CreateUser(ctx, org.ID, "owner@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("failed to create user1: %v", err)
	}
	user2, err := st.CreateUser(ctx, org.ID, "other@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("failed to create user2: %v", err)
	}

	opID := "demeter-audio-ownership-get"
	now := time.Now().UTC()
	if err := st.CreateDemeterAudioTranscriptionOperation(ctx, &DemeterAudioTranscriptionOperationRecord{
		OperationID:    opID,
		OrganizationID: org.ID,
		UserID:         user2.ID,
		Status:         DemeterAudioTranscriptionOperationStatusRunning,
		Stage:          "queued",
		ChunkIndex:     0,
		ChunkCount:     1,
		StatusCode:     202,
		CreatedAt:      now,
		UpdatedAt:      now,
	}); err != nil {
		t.Fatalf("failed to create record: %v", err)
	}

	traceCtx := observability.WithTraceID(context.Background(), "trace-demeter-ownership-get")
	traceCtx = requestmeta.WithActor(traceCtx, user1.ID, org.ID)
	_, err = st.GetDemeterAudioTranscriptionOperation(traceCtx, opID, org.ID, user1.ID)
	var ownershipErr *DemeterAudioTranscriptionOperationOwnershipError
	if !errors.As(err, &ownershipErr) {
		t.Fatalf("expected ownership error, got %v", err)
	}
	if ownershipErr.StoredUserID != user2.ID || ownershipErr.StoredOrganizationID != org.ID {
		t.Fatalf("unexpected stored ownership details: %#v", ownershipErr)
	}

	event := waitForBackendErrorEventByTraceAndStepStore(t, st, "trace-demeter-ownership-get", "ownership_mismatch_error")
	if event == nil {
		t.Fatalf("expected persisted ownership event")
	}
	if event.UserID != user1.ID || event.OrganizationID != org.ID {
		t.Fatalf("unexpected event actor scope: %#v", event)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
		t.Fatalf("failed to decode payload: %v", err)
	}
	if payload["request_user_id"] != user1.ID || payload["request_org_id"] != org.ID {
		t.Fatalf("unexpected request ownership payload: %#v", payload)
	}
	if payload["stored_user_id"] != user2.ID || payload["stored_org_id"] != org.ID {
		t.Fatalf("unexpected stored ownership payload: %#v", payload)
	}
	if payload["source"] != "store_get" {
		t.Fatalf("unexpected ownership source: %#v", payload)
	}
}

func waitForBackendErrorEventByTraceAndStepStore(t *testing.T, st *Store, traceID, step string) *BackendErrorEvent {
	t.Helper()

	for i := 0; i < 60; i++ {
		result, err := st.ListBackendErrorEvents(context.Background(), BackendErrorEventFilters{
			Query: traceID,
			Limit: 20,
		})
		if err != nil {
			t.Fatalf("failed to list backend errors: %v", err)
		}
		for _, item := range result.Items {
			if item.Step == step {
				return &item
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	return nil
}
