package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
		PartialText:    sql.NullString{String: "hello", Valid: true},
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
	if updated.PartialText.String != "hello" {
		t.Fatalf("unexpected partial text: %+v", updated)
	}
	if !updated.FinishedAt.Valid {
		t.Fatalf("expected finished at to be set: %+v", updated)
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
