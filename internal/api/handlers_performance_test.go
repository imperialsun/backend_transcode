package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"demeter-backend/internal/backendperformance"
	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
)

func TestAdminPerformanceSummary_RespectsTaskFilterAndTaskOptions(t *testing.T) {
	fixture := setupAdminDeleteRoutesTest(t)
	if err := fixture.store.SetUserGlobalRoles(context.Background(), fixture.superAdminUser.ID, []string{"super_admin"}); err != nil {
		t.Fatalf("failed to set super admin roles: %v", err)
	}

	now := time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC)
	if _, err := fixture.store.IngestPerformanceEvents(context.Background(), fixture.org.ID, fixture.actor.ID, []backendperformance.Event{
		{
			EventID:        "perf-1",
			TraceID:        "trace-1",
			UserID:         fixture.actor.ID,
			OrganizationID: fixture.org.ID,
			Surface:        "backend",
			Component:      "http",
			Task:           "request",
			Status:         "success",
			DurationMS:     125,
			Route:          "/api/v1/ping",
			MetaJSON:       json.RawMessage(`{"status":200}`),
			OccurredAt:     now,
		},
		{
			EventID:        "perf-2",
			TraceID:        "trace-2",
			UserID:         fixture.actor.ID,
			OrganizationID: fixture.org.ID,
			Surface:        "backend",
			Component:      "mistral",
			Task:           "response_received",
			Status:         "success",
			DurationMS:     375,
			Route:          "/v1/models",
			MetaJSON:       json.RawMessage(`{"status":200}`),
			OccurredAt:     now.Add(-time.Hour),
		},
		{
			EventID:        "perf-3",
			TraceID:        "trace-3",
			UserID:         fixture.actor.ID,
			OrganizationID: fixture.org.ID,
			Surface:        "frontend",
			Component:      "cloud",
			Task:           "cloud_total",
			Status:         "success",
			DurationMS:     1200,
			Route:          "/cloudupload",
			MetaJSON:       json.RawMessage(`{"provider":"whisper"}`),
			OccurredAt:     now.Add(-2 * time.Hour),
		},
	}); err != nil {
		t.Fatalf("failed to seed performance events: %v", err)
	}

	resp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodGet,
		"/api/v1/admin/performance/summary?organizationId="+fixture.org.ID+"&from=2026-04-03&to=2026-04-04&task=request",
		nil,
		nil,
		adminHeaders(t, fixture.superAdminUser, fixture.appCtx.Config.JWTSecret),
	)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for performance summary, got %d", resp.StatusCode)
	}

	var summary store.PerformanceSummary
	if err := json.NewDecoder(resp.Body).Decode(&summary); err != nil {
		t.Fatalf("failed to decode summary: %v", err)
	}
	if summary.OrganizationID != fixture.org.ID {
		t.Fatalf("unexpected organization id: %s", summary.OrganizationID)
	}
	if summary.Totals.Events != 1 || summary.Totals.TotalDurationMS != 125 {
		t.Fatalf("unexpected filtered totals: %#v", summary.Totals)
	}
	if len(summary.TaskOptions) != 3 {
		t.Fatalf("expected 3 task options, got %#v", summary.TaskOptions)
	}
	if len(summary.TopTasks) != 1 || summary.TopTasks[0].Task != "request" || summary.TopTasks[0].Route != "/api/v1/ping" {
		t.Fatalf("unexpected filtered top tasks: %#v", summary.TopTasks)
	}
	if len(summary.RecentEvents) != 1 || summary.RecentEvents[0].TraceID != "trace-1" {
		t.Fatalf("unexpected filtered recent events: %#v", summary.RecentEvents)
	}
}

func TestDeletePerformanceEvents_RespectsFiltersAndWritesAudit(t *testing.T) {
	fixture := setupAdminDeleteRoutesTest(t)
	if err := fixture.store.SetUserGlobalRoles(context.Background(), fixture.superAdminUser.ID, []string{"super_admin"}); err != nil {
		t.Fatalf("failed to set super admin roles: %v", err)
	}

	now := time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC)
	if _, err := fixture.store.IngestPerformanceEvents(context.Background(), fixture.org.ID, fixture.actor.ID, []backendperformance.Event{
		{
			EventID:        "perf-keep",
			TraceID:        "trace-keep",
			UserID:         fixture.actor.ID,
			OrganizationID: fixture.org.ID,
			Surface:        "backend",
			Component:      "http",
			Task:           "request",
			Status:         "success",
			DurationMS:     125,
			Route:          "/api/v1/ping",
			MetaJSON:       json.RawMessage(`{"status":200}`),
			OccurredAt:     now,
		},
		{
			EventID:        "perf-delete",
			TraceID:        "trace-delete",
			UserID:         fixture.actor.ID,
			OrganizationID: fixture.org.ID,
			Surface:        "backend",
			Component:      "mistral",
			Task:           "response_received",
			Status:         "success",
			DurationMS:     375,
			Route:          "/v1/models",
			MetaJSON:       json.RawMessage(`{"status":200}`),
			OccurredAt:     now.Add(-time.Hour),
		},
	}); err != nil {
		t.Fatalf("failed to seed performance events: %v", err)
	}

	resp := performJSONRequestWithHeaders(
		t,
		fixture.app,
		http.MethodDelete,
		"/api/v1/admin/performance?organizationId="+fixture.org.ID+"&from=2026-04-03&to=2026-04-04&task=response_received",
		nil,
		nil,
		adminHeaders(t, fixture.superAdminUser, fixture.appCtx.Config.JWTSecret),
	)
	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for performance purge, got %d", resp.StatusCode)
	}
	closeHTTPResponse(t, resp)

	summary, err := fixture.store.GetPerformanceSummary(context.Background(), store.PerformanceSummaryFilters{
		OrganizationID: fixture.org.ID,
		From:           "2026-04-03",
		To:             "2026-04-04",
		TopLimit:       10,
		RecentLimit:    10,
	})
	if err != nil {
		t.Fatalf("failed to reload performance summary: %v", err)
	}
	if summary.Totals.Events != 1 || len(summary.RecentEvents) != 1 || summary.RecentEvents[0].TraceID != "trace-keep" {
		t.Fatalf("unexpected remaining performance data: %#v", summary)
	}

	var auditCount int
	if err := fixture.store.DB.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM audit_logs WHERE action = 'admin.performance.purge'
	`).Scan(&auditCount); err != nil {
		t.Fatalf("failed to count performance purge audits: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("expected one performance purge audit, got %d", auditCount)
	}
}
