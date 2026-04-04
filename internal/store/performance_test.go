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

	now := time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC)
	events := []backendperformance.Event{
		{
			EventID:        "perf-1",
			TraceID:        "trace-1",
			UserID:         user.ID,
			OrganizationID: org.ID,
			Surface:        "frontend",
			Component:      "cloud",
			Task:           "cloud_total",
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
			Task:           "request",
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
			Task:           "response_received",
			Status:         "success",
			DurationMS:     900,
			Route:          "/v1/models",
			MetaJSON:       json.RawMessage(`{"status":200}`),
			OccurredAt:     now.Add(-2 * time.Hour),
		},
	}

	result, err := st.IngestPerformanceEvents(ctx, org.ID, user.ID, events)
	if err != nil {
		t.Fatalf("failed to ingest performance events: %v", err)
	}
	if result.Accepted != 3 || result.Duplicates != 0 {
		t.Fatalf("unexpected ingest result: %#v", result)
	}

	summary, err := st.GetPerformanceSummary(ctx, store.PerformanceSummaryFilters{
		OrganizationID: org.ID,
		From:           now.AddDate(0, 0, -1).Format(time.DateOnly),
		To:             now.Format(time.DateOnly),
		TopLimit:       10,
		RecentLimit:    10,
	})
	if err != nil {
		t.Fatalf("failed to load performance summary: %v", err)
	}

	if summary.OrganizationID != org.ID {
		t.Fatalf("unexpected organization id: %s", summary.OrganizationID)
	}
	if summary.Totals.Events != 3 || summary.Totals.Successes != 2 || summary.Totals.Failures != 1 {
		t.Fatalf("unexpected totals: %#v", summary.Totals)
	}
	if summary.Totals.TotalDurationMS != 5_600 || summary.Totals.AverageDurationMS != 1_867 || summary.Totals.MaxDurationMS != 4_200 {
		t.Fatalf("unexpected duration totals: %#v", summary.Totals)
	}
	if len(summary.TaskOptions) != 3 || summary.TaskOptions[0] != "cloud_total" || summary.TaskOptions[1] != "request" || summary.TaskOptions[2] != "response_received" {
		t.Fatalf("unexpected task options: %#v", summary.TaskOptions)
	}
	if len(summary.ByDay) != 2 {
		t.Fatalf("expected 2 day buckets, got %d", len(summary.ByDay))
	}
	if len(summary.TopTasks) != 3 {
		t.Fatalf("expected 3 grouped tasks, got %d", len(summary.TopTasks))
	}
	if summary.TopTasks[0].Task != "cloud_total" || summary.TopTasks[0].Surface != "frontend" {
		t.Fatalf("unexpected top task ordering: %#v", summary.TopTasks[0])
	}
	if len(summary.RecentEvents) != 3 {
		t.Fatalf("expected 3 recent events, got %d", len(summary.RecentEvents))
	}
	if summary.RecentEvents[0].TraceID != "trace-2" {
		t.Fatalf("expected most recent event first, got %#v", summary.RecentEvents[0])
	}

	filtered, err := st.GetPerformanceSummary(ctx, store.PerformanceSummaryFilters{
		OrganizationID: org.ID,
		From:           now.AddDate(0, 0, -1).Format(time.DateOnly),
		To:             now.Format(time.DateOnly),
		Task:           "request",
		TopLimit:       10,
		RecentLimit:    10,
	})
	if err != nil {
		t.Fatalf("failed to load filtered performance summary: %v", err)
	}
	if filtered.Totals.Events != 1 || filtered.Totals.TotalDurationMS != 500 {
		t.Fatalf("unexpected filtered totals: %#v", filtered.Totals)
	}
	if len(filtered.TopTasks) != 1 || filtered.TopTasks[0].Task != "request" {
		t.Fatalf("unexpected filtered top tasks: %#v", filtered.TopTasks)
	}
	if len(filtered.RecentEvents) != 1 || filtered.RecentEvents[0].Task != "request" {
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

	now := time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC)
	_, err = st.IngestPerformanceEvents(ctx, org.ID, user.ID, []backendperformance.Event{
		{
			EventID:        "perf-old",
			TraceID:        "trace-old",
			UserID:         user.ID,
			OrganizationID: org.ID,
			Surface:        "frontend",
			Component:      "cloud",
			Task:           "cloud_total",
			Status:         "success",
			DurationMS:     1_100,
			Route:          "/cloudupload",
			MetaJSON:       json.RawMessage(`{}`),
			OccurredAt:     now.AddDate(0, 0, -31),
		},
		{
			EventID:        "perf-new",
			TraceID:        "trace-new",
			UserID:         user.ID,
			OrganizationID: org.ID,
			Surface:        "frontend",
			Component:      "cloud",
			Task:           "cloud_total",
			Status:         "success",
			DurationMS:     1_300,
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
		From:           now.AddDate(0, 0, -40).Format(time.DateOnly),
		To:             now.Format(time.DateOnly),
		TopLimit:       10,
		RecentLimit:    10,
	})
	if err != nil {
		t.Fatalf("failed to load performance summary after purge: %v", err)
	}
	if summary.Totals.Events != 1 {
		t.Fatalf("expected one remaining event, got %d", summary.Totals.Events)
	}
	if len(summary.RecentEvents) != 1 || summary.RecentEvents[0].TraceID != "trace-new" {
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

	now := time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC)
	_, err = st.IngestPerformanceEvents(ctx, org.ID, user.ID, []backendperformance.Event{
		{
			EventID:        "perf-request",
			TraceID:        "trace-request",
			UserID:         user.ID,
			OrganizationID: org.ID,
			Surface:        "backend",
			Component:      "http",
			Task:           "request",
			Status:         "success",
			DurationMS:     500,
			Route:          "/api/v1/ping",
			MetaJSON:       json.RawMessage(`{}`),
			OccurredAt:     now,
		},
		{
			EventID:        "perf-response",
			TraceID:        "trace-response",
			UserID:         user.ID,
			OrganizationID: org.ID,
			Surface:        "backend",
			Component:      "mistral",
			Task:           "response_received",
			Status:         "success",
			DurationMS:     900,
			Route:          "/v1/models",
			MetaJSON:       json.RawMessage(`{}`),
			OccurredAt:     now.Add(-time.Hour),
		},
	})
	if err != nil {
		t.Fatalf("failed to ingest performance events: %v", err)
	}

	deleted, err := st.DeletePerformanceEvents(ctx, store.PerformanceSummaryFilters{
		OrganizationID: org.ID,
		From:           now.AddDate(0, 0, -1).Format(time.DateOnly),
		To:             now.Format(time.DateOnly),
		Task:           "response_received",
	})
	if err != nil {
		t.Fatalf("failed to delete performance events: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected one deleted row, got %d", deleted)
	}

	summary, err := st.GetPerformanceSummary(ctx, store.PerformanceSummaryFilters{
		OrganizationID: org.ID,
		From:           now.AddDate(0, 0, -1).Format(time.DateOnly),
		To:             now.Format(time.DateOnly),
		TopLimit:       10,
		RecentLimit:    10,
	})
	if err != nil {
		t.Fatalf("failed to reload performance summary: %v", err)
	}
	if summary.Totals.Events != 1 || len(summary.RecentEvents) != 1 || summary.RecentEvents[0].Task != "request" {
		t.Fatalf("unexpected remaining summary: %#v", summary)
	}
}
