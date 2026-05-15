package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestPurgeCompletedDemeterReportOperationsRemovesOnlyCompletedRows(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "demeter-report-purge.sqlite"))
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
	completedID := "demeter-report-completed-purge"
	runningID := "demeter-report-running-purge"
	for _, record := range []*DemeterReportOperationRecord{
		{
			OperationID:    completedID,
			OrganizationID: org.ID,
			UserID:         user.ID,
			Status:         DemeterReportOperationStatusCompleted,
			Stage:          "completed",
			FormatIndex:    1,
			FormatCount:    1,
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
			Status:         DemeterReportOperationStatusRunning,
			Stage:          "running",
			FormatIndex:    0,
			FormatCount:    2,
			Progress:       0.5,
			StatusCode:     202,
			CreatedAt:      now,
			UpdatedAt:      now,
		},
	} {
		if err := st.CreateDemeterReportOperation(ctx, record); err != nil {
			t.Fatalf("failed to create report record %s: %v", record.OperationID, err)
		}
	}

	purged, err := st.PurgeCompletedDemeterReportOperations(ctx)
	if err != nil {
		t.Fatalf("purge completed report operations failed: %v", err)
	}
	if purged != 1 {
		t.Fatalf("unexpected purge count: got %d want 1", purged)
	}

	if _, err := st.GetDemeterReportOperation(ctx, completedID, org.ID, user.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected completed record to be removed, got %v", err)
	}
	running, err := st.GetDemeterReportOperation(ctx, runningID, org.ID, user.ID)
	if err != nil {
		t.Fatalf("failed to load running record: %v", err)
	}
	if running.Status != DemeterReportOperationStatusRunning {
		t.Fatalf("expected running record to remain, got %+v", running)
	}
}

func TestPurgeAllDemeterReportOperationsRemovesEveryRow(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "demeter-report-purge-all.sqlite"))
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
	for _, record := range []*DemeterReportOperationRecord{
		{
			OperationID:    "demeter-report-completed-all-purge",
			OrganizationID: org.ID,
			UserID:         user.ID,
			Status:         DemeterReportOperationStatusCompleted,
			Stage:          "completed",
			FormatIndex:    1,
			FormatCount:    1,
			Progress:       1,
			StatusCode:     200,
			CreatedAt:      now,
			UpdatedAt:      now,
			FinishedAt:     sql.NullTime{Time: now, Valid: true},
		},
		{
			OperationID:    "demeter-report-running-all-purge",
			OrganizationID: org.ID,
			UserID:         user.ID,
			Status:         DemeterReportOperationStatusRunning,
			Stage:          "running",
			FormatIndex:    0,
			FormatCount:    2,
			Progress:       0.5,
			StatusCode:     202,
			CreatedAt:      now,
			UpdatedAt:      now,
		},
	} {
		if err := st.CreateDemeterReportOperation(ctx, record); err != nil {
			t.Fatalf("failed to create report record %s: %v", record.OperationID, err)
		}
	}

	deleted, err := st.PurgeAllDemeterReportOperations(ctx)
	if err != nil {
		t.Fatalf("purge all report operations failed: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("unexpected purge count: got %d want 2", deleted)
	}

	for _, opID := range []string{"demeter-report-completed-all-purge", "demeter-report-running-all-purge"} {
		if _, err := st.GetDemeterReportOperation(ctx, opID, org.ID, user.ID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("expected record %s to be removed, got %v", opID, err)
		}
	}
}

func TestUpdatePendingDemeterReportOperationQueueByIDMovesOnlyPendingRows(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "demeter-report-pending-queue.sqlite"))
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	org, err := st.CreateOrganization(ctx, "Org", "pending-org", "active")
	if err != nil {
		t.Fatalf("failed to create org: %v", err)
	}
	user, err := st.CreateUser(ctx, org.ID, "pending@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	now := time.Now().UTC()
	for _, record := range []*DemeterReportOperationRecord{
		{
			OperationID:    "demeter-report-pending-move",
			OrganizationID: org.ID,
			UserID:         user.ID,
			QueueID:        1,
			Status:         DemeterReportOperationStatusPending,
			Stage:          "queued",
			FormatCount:    1,
			StatusCode:     202,
			CreatedAt:      now,
			UpdatedAt:      now,
		},
		{
			OperationID:    "demeter-report-running-stays",
			OrganizationID: org.ID,
			UserID:         user.ID,
			QueueID:        1,
			Status:         DemeterReportOperationStatusRunning,
			Stage:          "running",
			FormatCount:    1,
			StatusCode:     202,
			CreatedAt:      now,
			UpdatedAt:      now,
		},
	} {
		if err := st.CreateDemeterReportOperation(ctx, record); err != nil {
			t.Fatalf("failed to create report record %s: %v", record.OperationID, err)
		}
	}

	moved, err := st.UpdatePendingDemeterReportOperationQueueByID(ctx, "demeter-report-pending-move", 2, now.Add(time.Second))
	if err != nil {
		t.Fatalf("failed to move pending row: %v", err)
	}
	if !moved {
		t.Fatal("expected pending row to move")
	}
	skipped, err := st.UpdatePendingDemeterReportOperationQueueByID(ctx, "demeter-report-running-stays", 2, now.Add(time.Second))
	if err != nil {
		t.Fatalf("failed to skip running row: %v", err)
	}
	if skipped {
		t.Fatal("running row must not be moved by pending-only update")
	}

	pending, err := st.GetDemeterReportOperation(ctx, "demeter-report-pending-move", org.ID, user.ID)
	if err != nil {
		t.Fatalf("failed to reload pending row: %v", err)
	}
	if pending.QueueID != 2 {
		t.Fatalf("expected pending row on queue 2, got %+v", pending)
	}
	running, err := st.GetDemeterReportOperation(ctx, "demeter-report-running-stays", org.ID, user.ID)
	if err != nil {
		t.Fatalf("failed to reload running row: %v", err)
	}
	if running.QueueID != 1 {
		t.Fatalf("running row should stay on queue 1, got %+v", running)
	}
}
