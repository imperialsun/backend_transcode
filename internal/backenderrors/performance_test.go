package backenderrors_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/backenderrors"
	"demeter-backend/internal/backendperformance"
	"demeter-backend/internal/observability"
	"demeter-backend/internal/requestmeta"
	"demeter-backend/internal/store"
)

func TestRecordLogPersistsBackendPerformanceEvents(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "backend-performance.sqlite")
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
	backendperformance.RegisterSink(st)
	t.Cleanup(func() {
		backendperformance.RegisterSink(nil)
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

	traceCtx := observability.WithTraceID(context.Background(), "trace-performance-log")
	traceCtx = requestmeta.WithActor(traceCtx, user.ID, org.ID)

	backenderrors.RecordLog(traceCtx, "http", "/ping", "request_completed", "request", map[string]any{
		"duration_ms": 12,
		"status":      200,
		"method":      "GET",
	})

	event := waitForPerformanceEvent(t, st, org.ID, "trace-performance-log")
	if event == nil {
		t.Fatal("expected performance event to be persisted")
	}
	if event.Surface != "backend" || event.Component != "http" || event.Task != "request" {
		t.Fatalf("unexpected performance event: %#v", event)
	}
	if event.Status != "success" {
		t.Fatalf("expected success status, got %s", event.Status)
	}
	if event.UserID != user.ID || event.OrganizationID != org.ID {
		t.Fatalf("unexpected actor scope: %#v", event)
	}
	if event.DurationMS != 12 {
		t.Fatalf("unexpected duration: %d", event.DurationMS)
	}
}

func TestRecordLogSkipsRefreshRoutes(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "backend-performance-refresh-skip.sqlite")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	backendperformance.RegisterSink(st)
	t.Cleanup(func() {
		backendperformance.RegisterSink(nil)
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

	refreshTraceCtx := observability.WithTraceID(context.Background(), "trace-refresh-skip")
	refreshTraceCtx = requestmeta.WithActor(refreshTraceCtx, user.ID, org.ID)
	backendperformance.RecordLog(refreshTraceCtx, "http", "/api/v1/auth/refresh", "request_completed", "request", map[string]any{
		"duration_ms": 12,
		"status":      200,
	})

	if event := waitForPerformanceEvent(t, st, org.ID, "trace-refresh-skip"); event != nil {
		t.Fatalf("expected refresh route to be skipped, got %#v", event)
	}

	normalTraceCtx := observability.WithTraceID(context.Background(), "trace-normal-skip-check")
	normalTraceCtx = requestmeta.WithActor(normalTraceCtx, user.ID, org.ID)
	backendperformance.RecordLog(normalTraceCtx, "http", "/api/v1/ping", "request_completed", "request", map[string]any{
		"duration_ms": 18,
		"status":      200,
	})

	event := waitForPerformanceEvent(t, st, org.ID, "trace-normal-skip-check")
	if event == nil {
		t.Fatal("expected non-refresh route to persist a performance event")
	}
	if event.Route != "/api/v1/ping" || event.Task != "request" {
		t.Fatalf("unexpected persisted performance event: %#v", event)
	}
}

func waitForPerformanceEvent(t *testing.T, st *store.Store, organizationID, traceID string) *store.PerformanceEvent {
	t.Helper()

	for i := 0; i < 40; i++ {
		summary, err := st.GetPerformanceSummary(context.Background(), store.PerformanceSummaryFilters{
			OrganizationID: organizationID,
			RecentLimit:    10,
			TopLimit:       10,
			From:           time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly),
			To:             time.Now().UTC().Format(time.DateOnly),
		})
		if err != nil {
			t.Fatalf("failed to load performance summary: %v", err)
		}
		for _, event := range summary.RecentEvents {
			if event.TraceID == traceID {
				item := event
				return &item
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	return nil
}
