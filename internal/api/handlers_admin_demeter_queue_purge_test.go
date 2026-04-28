package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"testing"
	"time"

	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
)

func TestDeleteAdminDemeterQueue_PurgesCompletedAndWritesAudit(t *testing.T) {
	fixture := setupAdminDeleteRoutesTest(t)
	ctx := context.Background()
	if err := fixture.store.SetUserGlobalRoles(ctx, fixture.superAdminUser.ID, []string{"super_admin"}); err != nil {
		t.Fatalf("failed to set super admin roles: %v", err)
	}

	seedDemeterAudioQueueJobs(t, fixture.store, fixture.org.ID, fixture.actor.ID)

	resp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodDelete,
		"/api/v1/admin/providers/demeter-sante/queue",
		nil,
		nil,
		adminHeaders(t, fixture.superAdminUser, fixture.appCtx.Config.JWTSecret),
	)
	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for completed purge, got %d", resp.StatusCode)
	}
	closeHTTPResponse(t, resp)

	if _, err := fixture.store.GetDemeterAudioTranscriptionOperation(ctx, "audio-completed-purge", fixture.org.ID, fixture.actor.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected completed job to be removed, got %v", err)
	}
	if _, err := fixture.store.GetDemeterAudioTranscriptionOperation(ctx, "audio-running-purge", fixture.org.ID, fixture.actor.ID); err != nil {
		t.Fatalf("running job should remain after completed purge, got %v", err)
	}

	var auditCount int
	if err := fixture.store.DB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM audit_logs WHERE action = 'admin.demeter_audio_queue.purge'
	`).Scan(&auditCount); err != nil {
		t.Fatalf("failed to count audio purge audits: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("expected one audio purge audit, got %d", auditCount)
	}
}

func TestDeleteAdminDemeterQueue_PurgeAllRemovesActiveQueue(t *testing.T) {
	fixture := setupAdminDeleteRoutesTest(t)
	ctx := context.Background()
	if err := fixture.store.SetUserGlobalRoles(ctx, fixture.superAdminUser.ID, []string{"super_admin"}); err != nil {
		t.Fatalf("failed to set super admin roles: %v", err)
	}

	seedDemeterAudioQueueJobs(t, fixture.store, fixture.org.ID, fixture.actor.ID)

	resp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodDelete,
		"/api/v1/admin/providers/demeter-sante/queue?scope=all",
		nil,
		nil,
		adminHeaders(t, fixture.superAdminUser, fixture.appCtx.Config.JWTSecret),
	)
	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for active queue full purge, got %d", resp.StatusCode)
	}
	closeHTTPResponse(t, resp)

	if _, err := fixture.store.GetDemeterAudioTranscriptionOperation(ctx, "audio-completed-purge", fixture.org.ID, fixture.actor.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected completed job to be removed, got %v", err)
	}
	if _, err := fixture.store.GetDemeterAudioTranscriptionOperation(ctx, "audio-running-purge", fixture.org.ID, fixture.actor.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected running job to be removed, got %v", err)
	}
}

func TestDeleteAdminDemeterQueue_PurgeAllRemovesIdleQueue(t *testing.T) {
	fixture := setupAdminDeleteRoutesTest(t)
	ctx := context.Background()
	if err := fixture.store.SetUserGlobalRoles(ctx, fixture.superAdminUser.ID, []string{"super_admin"}); err != nil {
		t.Fatalf("failed to set super admin roles: %v", err)
	}

	seedDemeterAudioQueueCompletedOnly(t, fixture.store, fixture.org.ID, fixture.actor.ID)

	resp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodDelete,
		"/api/v1/admin/providers/demeter-sante/queue?scope=all",
		nil,
		nil,
		adminHeaders(t, fixture.superAdminUser, fixture.appCtx.Config.JWTSecret),
	)
	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for full purge, got %d", resp.StatusCode)
	}
	closeHTTPResponse(t, resp)

	if _, err := fixture.store.GetDemeterAudioTranscriptionOperation(ctx, "audio-completed-only-purge", fixture.org.ID, fixture.actor.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected all audio jobs to be removed, got %v", err)
	}

	var auditCount int
	if err := fixture.store.DB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM audit_logs WHERE action = 'admin.demeter_audio_queue.purge'
	`).Scan(&auditCount); err != nil {
		t.Fatalf("failed to count audio full purge audits: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("expected one audio full purge audit, got %d", auditCount)
	}
}

func TestDeleteAdminDemeterReportQueue_PurgesCompletedAndRejectsForbidden(t *testing.T) {
	fixture := setupAdminDeleteRoutesTest(t)
	ctx := context.Background()
	if err := fixture.store.SetUserGlobalRoles(ctx, fixture.superAdminUser.ID, []string{"super_admin"}); err != nil {
		t.Fatalf("failed to set super admin roles: %v", err)
	}

	seedDemeterReportQueueCompletedOnly(t, fixture.store, fixture.org.ID, fixture.actor.ID)

	resp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodDelete,
		"/api/v1/admin/providers/demeter-sante/report-queue",
		nil,
		nil,
		adminHeaders(t, fixture.superAdminUser, fixture.appCtx.Config.JWTSecret),
	)
	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for report completed purge, got %d", resp.StatusCode)
	}
	closeHTTPResponse(t, resp)

	if _, err := fixture.store.GetDemeterReportOperation(ctx, "report-completed-only-purge", fixture.org.ID, fixture.actor.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected completed report job to be removed, got %v", err)
	}

	if err := fixture.store.SetUserGlobalRoles(ctx, fixture.superAdminUser.ID, []string{"user"}); err != nil {
		t.Fatalf("failed to downgrade super admin: %v", err)
	}
	forbidden := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodDelete,
		"/api/v1/admin/providers/demeter-sante/report-queue",
		nil,
		nil,
		adminHeaders(t, fixture.superAdminUser, fixture.appCtx.Config.JWTSecret),
	)
	if forbidden.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 for non super admin report purge, got %d", forbidden.StatusCode)
	}
}

func TestDeleteAdminDemeterReportQueue_PurgeAllRemovesActiveQueue(t *testing.T) {
	fixture := setupAdminDeleteRoutesTest(t)
	ctx := context.Background()
	if err := fixture.store.SetUserGlobalRoles(ctx, fixture.superAdminUser.ID, []string{"super_admin"}); err != nil {
		t.Fatalf("failed to set super admin roles: %v", err)
	}

	seedDemeterReportQueueJobs(t, fixture.store, fixture.org.ID, fixture.actor.ID)

	resp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodDelete,
		"/api/v1/admin/providers/demeter-sante/report-queue?scope=all",
		nil,
		nil,
		adminHeaders(t, fixture.superAdminUser, fixture.appCtx.Config.JWTSecret),
	)
	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for active report queue full purge, got %d", resp.StatusCode)
	}
	closeHTTPResponse(t, resp)

	if _, err := fixture.store.GetDemeterReportOperation(ctx, "report-completed-only-purge", fixture.org.ID, fixture.actor.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected completed report job to be removed, got %v", err)
	}
	if _, err := fixture.store.GetDemeterReportOperation(ctx, "report-running-purge", fixture.org.ID, fixture.actor.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected running report job to be removed, got %v", err)
	}
}

func TestDeleteAdminDemeterReportQueue_PurgeAllRemovesIdleQueue(t *testing.T) {
	fixture := setupAdminDeleteRoutesTest(t)
	ctx := context.Background()
	if err := fixture.store.SetUserGlobalRoles(ctx, fixture.superAdminUser.ID, []string{"super_admin"}); err != nil {
		t.Fatalf("failed to set super admin roles: %v", err)
	}

	seedDemeterReportQueueCompletedOnly(t, fixture.store, fixture.org.ID, fixture.actor.ID)

	resp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodDelete,
		"/api/v1/admin/providers/demeter-sante/report-queue?scope=all",
		nil,
		nil,
		adminHeaders(t, fixture.superAdminUser, fixture.appCtx.Config.JWTSecret),
	)
	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for report full purge, got %d", resp.StatusCode)
	}
	closeHTTPResponse(t, resp)

	if _, err := fixture.store.GetDemeterReportOperation(ctx, "report-completed-only-purge", fixture.org.ID, fixture.actor.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected report jobs to be removed, got %v", err)
	}

	var auditCount int
	if err := fixture.store.DB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM audit_logs WHERE action = 'admin.demeter_report_queue.purge'
	`).Scan(&auditCount); err != nil {
		t.Fatalf("failed to count report full purge audits: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("expected one report full purge audit, got %d", auditCount)
	}
}

func seedDemeterAudioQueueJobs(t *testing.T, st *store.Store, orgID, userID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, record := range []*store.DemeterAudioTranscriptionOperationRecord{
		{
			OperationID:    "audio-completed-purge",
			OrganizationID: orgID,
			UserID:         userID,
			QueueID:        1,
			Status:         store.DemeterAudioTranscriptionOperationStatusCompleted,
			Stage:          "completed",
			ChunkIndex:     1,
			ChunkCount:     1,
			Progress:       1,
			StatusCode:     http.StatusOK,
			CreatedAt:      now,
			UpdatedAt:      now,
			FinishedAt:     sql.NullTime{Time: now, Valid: true},
		},
		{
			OperationID:    "audio-running-purge",
			OrganizationID: orgID,
			UserID:         userID,
			QueueID:        1,
			Status:         store.DemeterAudioTranscriptionOperationStatusRunning,
			Stage:          "running",
			ChunkIndex:     0,
			ChunkCount:     2,
			Progress:       0.5,
			StatusCode:     http.StatusAccepted,
			CreatedAt:      now,
			UpdatedAt:      now,
		},
	} {
		if err := st.CreateDemeterAudioTranscriptionOperation(ctx, record); err != nil {
			t.Fatalf("failed to seed audio record %s: %v", record.OperationID, err)
		}
	}
}

func seedDemeterAudioQueueCompletedOnly(t *testing.T, st *store.Store, orgID, userID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := st.CreateDemeterAudioTranscriptionOperation(ctx, &store.DemeterAudioTranscriptionOperationRecord{
		OperationID:    "audio-completed-only-purge",
		OrganizationID: orgID,
		UserID:         userID,
		QueueID:        1,
		Status:         store.DemeterAudioTranscriptionOperationStatusCompleted,
		Stage:          "completed",
		ChunkIndex:     1,
		ChunkCount:     1,
		Progress:       1,
		StatusCode:     http.StatusOK,
		CreatedAt:      now,
		UpdatedAt:      now,
		FinishedAt:     sql.NullTime{Time: now, Valid: true},
	}); err != nil {
		t.Fatalf("failed to seed completed audio record: %v", err)
	}
}

func seedDemeterReportQueueJobs(t *testing.T, st *store.Store, orgID, userID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, record := range []*store.DemeterReportOperationRecord{
		{
			OperationID:    "report-completed-only-purge",
			OrganizationID: orgID,
			UserID:         userID,
			QueueID:        1,
			Status:         store.DemeterReportOperationStatusCompleted,
			Stage:          "completed",
			FormatIndex:    1,
			FormatCount:    1,
			Progress:       1,
			StatusCode:     http.StatusOK,
			CreatedAt:      now,
			UpdatedAt:      now,
			FinishedAt:     sql.NullTime{Time: now, Valid: true},
		},
		{
			OperationID:    "report-running-purge",
			OrganizationID: orgID,
			UserID:         userID,
			QueueID:        1,
			Status:         store.DemeterReportOperationStatusRunning,
			Stage:          "running",
			FormatIndex:    0,
			FormatCount:    2,
			Progress:       0.5,
			StatusCode:     http.StatusAccepted,
			CreatedAt:      now,
			UpdatedAt:      now,
		},
	} {
		if err := st.CreateDemeterReportOperation(ctx, record); err != nil {
			t.Fatalf("failed to seed report record %s: %v", record.OperationID, err)
		}
	}
}

func seedDemeterReportQueueCompletedOnly(t *testing.T, st *store.Store, orgID, userID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := st.CreateDemeterReportOperation(ctx, &store.DemeterReportOperationRecord{
		OperationID:    "report-completed-only-purge",
		OrganizationID: orgID,
		UserID:         userID,
		QueueID:        1,
		Status:         store.DemeterReportOperationStatusCompleted,
		Stage:          "completed",
		FormatIndex:    1,
		FormatCount:    1,
		Progress:       1,
		StatusCode:     http.StatusOK,
		CreatedAt:      now,
		UpdatedAt:      now,
		FinishedAt:     sql.NullTime{Time: now, Valid: true},
	}); err != nil {
		t.Fatalf("failed to seed completed report record: %v", err)
	}
}
