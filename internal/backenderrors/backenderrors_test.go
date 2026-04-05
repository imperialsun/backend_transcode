package backenderrors_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/backenderrors"
	"demeter-backend/internal/observability"
	"demeter-backend/internal/requestmeta"
	"demeter-backend/internal/store"
)

func TestRecordLogPersistsCapturedBackendErrors(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "backenderrors.sqlite")
	st, err := store.Open(ctx, dbPath)
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
		t.Fatalf("failed to create organization: %v", err)
	}
	passwordHash, err := auth.HashPassword("ChangeMe123!")
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}
	user, err := st.CreateUser(ctx, org.ID, "user@example.com", passwordHash, "active")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	traceCtx := observability.WithTraceID(context.Background(), "trace-backend-error")
	traceCtx = requestmeta.WithActor(traceCtx, user.ID, org.ID)

	backenderrors.RecordLog(traceCtx, "http", "/admin/backend-errors", "request_completed", "http_request", map[string]any{
		"status_code": 200,
		"duration_ms": 12,
	})
	backenderrors.RecordLog(traceCtx, "http", "/admin/backend-errors", "request_failed", "http_request", map[string]any{
		"status_code":      500,
		"duration_ms":      31,
		"error":            errors.New("boom"),
		"response_preview": "Internal server error",
	})

	event := waitForBackendErrorEvent(t, st, "trace-backend-error")
	if event == nil {
		t.Fatalf("expected one persisted backend error event")
	}
	if event.TraceID != "trace-backend-error" {
		t.Fatalf("unexpected trace id: %s", event.TraceID)
	}
	if event.Component != "http" || event.Step != "request_failed" {
		t.Fatalf("unexpected persisted event: %#v", event)
	}
	if event.StatusCode != 500 {
		t.Fatalf("expected status 500, got %d", event.StatusCode)
	}
	if event.UserID != user.ID || event.OrganizationID != org.ID {
		t.Fatalf("unexpected actor scope: %#v", event)
	}
	if event.ErrorMessage == "" {
		t.Fatalf("expected error message to be stored")
	}
}

func TestRecordLogSkipsSuccessLifecycleSteps(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "backenderrors-success.sqlite")
	st, err := store.Open(ctx, dbPath)
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

	traceCtx := observability.WithTraceID(context.Background(), "trace-backend-error-cleanup-success")
	backenderrors.RecordLog(traceCtx, "server", "lifecycle", "backend_error_cleanup_success", "server", map[string]any{
		"purged": "0",
	})

	for i := 0; i < 40; i++ {
		result, err := st.ListBackendErrorEvents(context.Background(), store.BackendErrorEventFilters{
			Query: "trace-backend-error-cleanup-success",
			Limit: 10,
		})
		if err != nil {
			t.Fatalf("failed to list backend errors: %v", err)
		}
		if len(result.Items) > 0 {
			t.Fatalf("expected success lifecycle step to be skipped, got %#v", result.Items[0])
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestRecordLogCapturesErrorLifecycleSteps(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "backenderrors-error.sqlite")
	st, err := store.Open(ctx, dbPath)
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

	traceCtx := observability.WithTraceID(context.Background(), "trace-backend-error-cleanup-error")
	backenderrors.RecordLog(traceCtx, "server", "lifecycle", "backend_error_cleanup_error", "server", map[string]any{
		"error":  context.DeadlineExceeded,
		"purged": "0",
	})

	event := waitForBackendErrorEvent(t, st, "trace-backend-error-cleanup-error")
	if event == nil {
		t.Fatalf("expected error lifecycle step to be persisted")
	}
	if event.Step != "backend_error_cleanup_error" {
		t.Fatalf("unexpected persisted step: %s", event.Step)
	}
}

func waitForBackendErrorEvent(t *testing.T, st *store.Store, traceID string) *store.BackendErrorEvent {
	t.Helper()

	for i := 0; i < 40; i++ {
		result, err := st.ListBackendErrorEvents(context.Background(), store.BackendErrorEventFilters{
			Query: traceID,
			Limit: 10,
		})
		if err != nil {
			t.Fatalf("failed to list backend errors: %v", err)
		}
		if len(result.Items) > 0 {
			return &result.Items[0]
		}
		time.Sleep(25 * time.Millisecond)
	}
	return nil
}
