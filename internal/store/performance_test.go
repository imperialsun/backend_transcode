package store_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/backendperformance"
	"demeter-backend/internal/store"
)

func TestPerformanceSummaryIncludesBackendAndFrontendEvents(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "performance.sqlite")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
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
	peer, err := st.CreateUser(ctx, org.ID, "peer@example.com", passwordHash, "active")
	if err != nil {
		t.Fatalf("failed to create peer user: %v", err)
	}

	now := time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC)
	events := []backendperformance.Event{
		{
			EventID:        "perf-1",
			TraceID:        "trace-1",
			UserID:         user.ID,
			OrganizationID: org.ID,
			Surface:        "frontend",
			Component:      "cloud",
			Task:           "frontend_cloud_total",
			Status:         "success",
			DurationMS:     4_200,
			Route:          "/cloudupload",
			MetaJSON:       json.RawMessage(`{"provider":"whisper"}`),
			OccurredAt:     now.Add(-24 * time.Hour),
		},
		{
			EventID:        "perf-2",
			TraceID:        "trace-2",
			UserID:         user.ID,
			OrganizationID: org.ID,
			Surface:        "backend",
			Component:      "http",
			Task:           "http_request",
			Status:         "error",
			DurationMS:     500,
			Route:          "/api/v1/performance/events",
			MetaJSON:       json.RawMessage(`{"status":500}`),
			OccurredAt:     now,
		},
		{
			EventID:        "perf-3",
			TraceID:        "trace-3",
			UserID:         user.ID,
			OrganizationID: org.ID,
			Surface:        "backend",
			Component:      "mistral",
			Task:           "mistral_models",
			Status:         "success",
			DurationMS:     900,
			Route:          "/v1/models",
			MetaJSON:       json.RawMessage(`{"status":200}`),
			OccurredAt:     now.Add(-2 * time.Hour),
		},
		{
			EventID:        "perf-4",
			TraceID:        "trace-4",
			UserID:         peer.ID,
			OrganizationID: org.ID,
			Surface:        "frontend",
			Component:      "cloud",
			Task:           "frontend_cloud_total",
			Status:         "success",
			DurationMS:     1_100,
			Route:          "/cloudupload",
			MetaJSON:       json.RawMessage(`{"provider":"whisper"}`),
			OccurredAt:     now.Add(-3 * time.Hour),
		},
	}

	result, err := st.IngestPerformanceEvents(ctx, org.ID, user.ID, events)
	if err != nil {
		t.Fatalf("failed to ingest performance events: %v", err)
	}
	if result.Accepted != 4 || result.Duplicates != 0 {
		t.Fatalf("unexpected ingest result: %#v", result)
	}

	summary, err := st.GetPerformanceSummary(ctx, store.PerformanceSummaryFilters{
		OrganizationID: org.ID,
		From:           now.Add(-24 * time.Hour),
		To:             now,
		TopLimit:       10,
		RecentLimit:    10,
	})
	if err != nil {
		t.Fatalf("failed to load performance summary: %v", err)
	}

	if summary.OrganizationID != org.ID {
		t.Fatalf("unexpected organization id: %s", summary.OrganizationID)
	}
	if summary.Totals.Events != 4 || summary.Totals.Successes != 3 || summary.Totals.Failures != 1 {
		t.Fatalf("unexpected totals: %#v", summary.Totals)
	}
	if summary.Totals.TotalDurationMS != 6_700 || summary.Totals.AverageDurationMS != 1_675 || summary.Totals.MaxDurationMS != 4_200 {
		t.Fatalf("unexpected duration totals: %#v", summary.Totals)
	}
	if len(summary.TaskOptions) != 3 || summary.TaskOptions[0] != "frontend_cloud_total" || summary.TaskOptions[1] != "http_request" || summary.TaskOptions[2] != "mistral_models" {
		t.Fatalf("unexpected task options: %#v", summary.TaskOptions)
	}
	if len(summary.TopTasks) != 3 {
		t.Fatalf("expected 3 grouped tasks, got %d", len(summary.TopTasks))
	}
	if summary.TopTasks[0].Task != "frontend_cloud_total" || summary.TopTasks[0].Surface != "frontend" {
		t.Fatalf("unexpected top task ordering: %#v", summary.TopTasks[0])
	}
	if len(summary.RecentEvents) != 4 {
		t.Fatalf("expected 4 recent events, got %d", len(summary.RecentEvents))
	}
	if summary.RecentEvents[0].TraceID != "trace-2" {
		t.Fatalf("expected most recent event first, got %#v", summary.RecentEvents[0])
	}

	filtered, err := st.GetPerformanceSummary(ctx, store.PerformanceSummaryFilters{
		OrganizationID: org.ID,
		UserID:         user.ID,
		From:           now.Add(-24 * time.Hour),
		To:             now,
		Task:           "http_request",
		TopLimit:       10,
		RecentLimit:    10,
	})
	if err != nil {
		t.Fatalf("failed to load filtered performance summary: %v", err)
	}
	if filtered.UserID != user.ID {
		t.Fatalf("unexpected filtered user id: %#v", filtered.UserID)
	}
	if filtered.Totals.Events != 1 || filtered.Totals.TotalDurationMS != 500 {
		t.Fatalf("unexpected filtered totals: %#v", filtered.Totals)
	}
	if len(filtered.TopTasks) != 1 || filtered.TopTasks[0].Task != "http_request" {
		t.Fatalf("unexpected filtered top tasks: %#v", filtered.TopTasks)
	}
	if len(filtered.RecentEvents) != 1 || filtered.RecentEvents[0].Task != "http_request" {
		t.Fatalf("unexpected filtered recent events: %#v", filtered.RecentEvents)
	}
	if len(filtered.TaskOptions) != 3 {
		t.Fatalf("expected task options to stay scoped, got %#v", filtered.TaskOptions)
	}
}

func TestPurgeExpiredPerformanceEventsRemovesOldRows(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "performance-purge.sqlite")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
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
	peer, err := st.CreateUser(ctx, org.ID, "peer@example.com", passwordHash, "active")
	if err != nil {
		t.Fatalf("failed to create peer user: %v", err)
	}

	now := time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC)
	_, err = st.IngestPerformanceEvents(ctx, org.ID, user.ID, []backendperformance.Event{
		{
			EventID:        "perf-old",
			TraceID:        "trace-old",
			UserID:         user.ID,
			OrganizationID: org.ID,
			Surface:        "frontend",
			Component:      "cloud",
			Task:           "frontend_cloud_total",
			Status:         "success",
			DurationMS:     1_100,
			Route:          "/cloudupload",
			MetaJSON:       json.RawMessage(`{}`),
			OccurredAt:     now.AddDate(0, 0, -31),
		},
		{
			EventID:        "perf-user",
			TraceID:        "trace-user",
			UserID:         user.ID,
			OrganizationID: org.ID,
			Surface:        "frontend",
			Component:      "cloud",
			Task:           "frontend_cloud_total",
			Status:         "success",
			DurationMS:     1_300,
			Route:          "/cloudupload",
			MetaJSON:       json.RawMessage(`{}`),
			OccurredAt:     now,
		},
		{
			EventID:        "perf-peer",
			TraceID:        "trace-peer",
			UserID:         peer.ID,
			OrganizationID: org.ID,
			Surface:        "frontend",
			Component:      "cloud",
			Task:           "frontend_cloud_total",
			Status:         "success",
			DurationMS:     1_500,
			Route:          "/cloudupload",
			MetaJSON:       json.RawMessage(`{}`),
			OccurredAt:     now,
		},
	})
	if err != nil {
		t.Fatalf("failed to ingest performance events: %v", err)
	}

	deleted, err := st.PurgeExpiredPerformanceEvents(ctx, now)
	if err != nil {
		t.Fatalf("failed to purge performance events: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected one deleted row, got %d", deleted)
	}

	summary, err := st.GetPerformanceSummary(ctx, store.PerformanceSummaryFilters{
		OrganizationID: org.ID,
		UserID:         user.ID,
		From:           now.Add(-40 * 24 * time.Hour),
		To:             now,
		TopLimit:       10,
		RecentLimit:    10,
	})
	if err != nil {
		t.Fatalf("failed to load performance summary after purge: %v", err)
	}
	if summary.Totals.Events != 1 {
		t.Fatalf("expected one remaining event, got %d", summary.Totals.Events)
	}
	if summary.UserID != user.ID {
		t.Fatalf("unexpected summary user id: %#v", summary.UserID)
	}
	if len(summary.RecentEvents) != 1 || summary.RecentEvents[0].TraceID != "trace-user" {
		t.Fatalf("unexpected remaining events: %#v", summary.RecentEvents)
	}
}

func TestDeletePerformanceEventsRespectsTaskAndOrganizationFilters(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "performance-delete.sqlite")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
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
	peer, err := st.CreateUser(ctx, org.ID, "peer@example.com", passwordHash, "active")
	if err != nil {
		t.Fatalf("failed to create peer user: %v", err)
	}

	now := time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC)
	_, err = st.IngestPerformanceEvents(ctx, org.ID, user.ID, []backendperformance.Event{
		{
			EventID:        "perf-request",
			TraceID:        "trace-request",
			UserID:         user.ID,
			OrganizationID: org.ID,
			Surface:        "backend",
			Component:      "http",
			Task:           "http_request",
			Status:         "success",
			DurationMS:     500,
			Route:          "/api/v1/ping",
			MetaJSON:       json.RawMessage(`{}`),
			OccurredAt:     now,
		},
		{
			EventID:        "perf-peer",
			TraceID:        "trace-peer",
			UserID:         peer.ID,
			OrganizationID: org.ID,
			Surface:        "frontend",
			Component:      "cloud",
			Task:           "frontend_cloud_total",
			Status:         "success",
			DurationMS:     1_100,
			Route:          "/cloudupload",
			MetaJSON:       json.RawMessage(`{}`),
			OccurredAt:     now.Add(-2 * time.Hour),
		},
	})
	if err != nil {
		t.Fatalf("failed to ingest performance events: %v", err)
	}

	deleted, err := st.DeletePerformanceEvents(ctx, store.PerformanceSummaryFilters{
		OrganizationID: org.ID,
		UserID:         user.ID,
		From:           now.Add(-24 * time.Hour),
		To:             now,
		Task:           "http_request",
	})
	if err != nil {
		t.Fatalf("failed to delete performance events: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected one deleted row, got %d", deleted)
	}

	summary, err := st.GetPerformanceSummary(ctx, store.PerformanceSummaryFilters{
		OrganizationID: org.ID,
		UserID:         user.ID,
		From:           now.Add(-24 * time.Hour),
		To:             now,
		TopLimit:       10,
		RecentLimit:    10,
	})
	if err != nil {
		t.Fatalf("failed to reload performance summary: %v", err)
	}
	if summary.UserID != user.ID {
		t.Fatalf("unexpected summary user id: %#v", summary.UserID)
	}
	if summary.Totals.Events != 0 || len(summary.RecentEvents) != 0 || len(summary.TopTasks) != 0 {
		t.Fatalf("unexpected remaining summary: %#v", summary)
	}
}
