package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestIngestActivityEventsAndSummary(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "activity.sqlite")
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	org, err := st.CreateOrganization(ctx, "Org A", "org-a", "active")
	if err != nil {
		t.Fatalf("failed to create organization: %v", err)
	}
	userA, err := st.CreateUser(ctx, org.ID, "a@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("failed to create user A: %v", err)
	}
	userB, err := st.CreateUser(ctx, org.ID, "b@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("failed to create user B: %v", err)
	}

	now := time.Now().UTC()
	firstBatch := []ActivityEventInput{
		{
			EventID:    "evt-1",
			EventKind:  "transcription",
			SourceMode: "local",
			Provider:   "local_upload",
			Status:     "success",
			OccurredAt: now,
		},
		{
			EventID:    "evt-2",
			EventKind:  "transcription",
			SourceMode: "cloud_backend",
			Provider:   "demeter_sante",
			Status:     "error",
			OccurredAt: now,
		},
		{
			EventID:    "evt-3",
			EventKind:  "report",
			SourceMode: "local",
			Provider:   "local",
			Status:     "success",
			OccurredAt: now,
		},
		{
			EventID:    "evt-4",
			EventKind:  "report",
			SourceMode: "cloud_backend",
			Provider:   "demeter_sante",
			Status:     "error",
			OccurredAt: now,
		},
	}
	firstResult, err := st.IngestActivityEvents(ctx, org.ID, userA.ID, firstBatch)
	if err != nil {
		t.Fatalf("failed to ingest first batch: %v", err)
	}
	if firstResult.Accepted != 4 || firstResult.Duplicates != 0 {
		t.Fatalf("unexpected first ingest result: %+v", firstResult)
	}

	secondBatch := []ActivityEventInput{
		{
			EventID:    "evt-1",
			EventKind:  "transcription",
			SourceMode: "local",
			Provider:   "local_upload",
			Status:     "success",
			OccurredAt: now,
		},
		{
			EventID:    "evt-5",
			EventKind:  "transcription",
			SourceMode: "cloud_direct",
			Provider:   "whisper",
			Status:     "success",
			OccurredAt: now,
		},
		{
			EventID:    "evt-6",
			EventKind:  "report",
			SourceMode: "cloud_direct",
			Provider:   "mistral",
			Status:     "success",
			OccurredAt: now,
		},
	}
	secondResult, err := st.IngestActivityEvents(ctx, org.ID, userB.ID, secondBatch)
	if err != nil {
		t.Fatalf("failed to ingest second batch: %v", err)
	}
	if secondResult.Accepted != 2 || secondResult.Duplicates != 1 {
		t.Fatalf("unexpected second ingest result: %+v", secondResult)
	}

	day := now.Format("2006-01-02")
	summary, err := st.GetOrganizationActivitySummary(ctx, org.ID, day, day)
	if err != nil {
		t.Fatalf("failed to get activity summary: %v", err)
	}
	if summary.OrganizationID != org.ID {
		t.Fatalf("unexpected organization id: %q", summary.OrganizationID)
	}
	if summary.Totals.Transcriptions != 3 {
		t.Fatalf("unexpected transcription total: %d", summary.Totals.Transcriptions)
	}
	if summary.Totals.Reports != 3 {
		t.Fatalf("unexpected report total: %d", summary.Totals.Reports)
	}
	if got := summary.Breakdown.TranscriptionsByMode["local"]; got != 1 {
		t.Fatalf("unexpected local transcription count: %d", got)
	}
	if got := summary.Breakdown.TranscriptionsByMode["cloud_direct"]; got != 1 {
		t.Fatalf("unexpected cloud_direct transcription count: %d", got)
	}
	if got := summary.Breakdown.TranscriptionsByMode["cloud_backend"]; got != 1 {
		t.Fatalf("unexpected cloud_backend transcription count: %d", got)
	}
	if got := summary.Breakdown.ReportsByProvider["local"]; got != 1 {
		t.Fatalf("unexpected local report count: %d", got)
	}
	if got := summary.Breakdown.ReportsByProvider["mistral"]; got != 1 {
		t.Fatalf("unexpected mistral report count: %d", got)
	}
	if got := summary.Breakdown.ReportsByProvider["demeter_sante"]; got != 1 {
		t.Fatalf("unexpected demeter report count: %d", got)
	}
	if len(summary.ByUser) != 2 {
		t.Fatalf("expected 2 users in summary, got %d", len(summary.ByUser))
	}
}

func TestOrganizationActivitySummary_TracksMultipleDaysAndEmptyRanges(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t, "activity-summary.sqlite")

	org := createOrg(t, st, "Summary Org", "summary-org", "active")
	user := createUserWithPassword(t, st, org.ID, "summary@example.com", "ChangeMe123!", "active")

	dayOne := time.Date(2026, time.March, 10, 8, 0, 0, 0, time.UTC)
	dayTwo := time.Date(2026, time.March, 11, 8, 0, 0, 0, time.UTC)
	_, err := st.IngestActivityEvents(ctx, org.ID, user.ID, []ActivityEventInput{
		{EventID: "sum-1", EventKind: "transcription", SourceMode: "local", Provider: "local_upload", Status: "success", OccurredAt: dayOne},
		{EventID: "sum-2", EventKind: "report", SourceMode: "cloud_direct", Provider: "mistral", Status: "success", OccurredAt: dayTwo},
	})
	if err != nil {
		t.Fatalf("IngestActivityEvents failed: %v", err)
	}

	summary, err := st.GetOrganizationActivitySummary(ctx, org.ID, "2026-03-10", "2026-03-11")
	if err != nil {
		t.Fatalf("GetOrganizationActivitySummary failed: %v", err)
	}
	if len(summary.ByDay) != 2 {
		t.Fatalf("expected 2 by-day items, got %+v", summary.ByDay)
	}
	if summary.ByDay[0].Day != "2026-03-10" || summary.ByDay[1].Day != "2026-03-11" {
		t.Fatalf("unexpected day ordering: %+v", summary.ByDay)
	}
	if len(summary.ByUser) != 1 || summary.ByUser[0].UserID != user.ID {
		t.Fatalf("unexpected by-user summary: %+v", summary.ByUser)
	}

	emptySummary, err := st.GetOrganizationActivitySummary(ctx, org.ID, "2026-03-01", "2026-03-02")
	if err != nil {
		t.Fatalf("GetOrganizationActivitySummary for empty range failed: %v", err)
	}
	if emptySummary.Totals.Transcriptions != 0 || emptySummary.Totals.Reports != 0 {
		t.Fatalf("expected empty totals, got %+v", emptySummary.Totals)
	}
	if len(emptySummary.ByDay) != 0 || len(emptySummary.ByUser) != 0 {
		t.Fatalf("expected empty breakdowns, got %+v", emptySummary)
	}
}

func TestOrganizationActivitySummary_ExcludesOtherOrganizationsAndOrdersUsers(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t, "activity-order.sqlite")

	orgA := createOrg(t, st, "Org A", "org-a", "active")
	orgB := createOrg(t, st, "Org B", "org-b", "active")
	alpha := createUserWithPassword(t, st, orgA.ID, "alpha@example.com", "ChangeMe123!", "active")
	beta := createUserWithPassword(t, st, orgA.ID, "beta@example.com", "ChangeMe123!", "active")
	other := createUserWithPassword(t, st, orgB.ID, "other@example.com", "ChangeMe123!", "active")

	day := time.Date(2026, time.March, 12, 9, 0, 0, 0, time.UTC)
	_, err := st.IngestActivityEvents(ctx, orgA.ID, alpha.ID, []ActivityEventInput{
		{EventID: "ord-a-1", EventKind: "transcription", SourceMode: "local", Provider: "local_upload", Status: "success", OccurredAt: day},
		{EventID: "ord-a-2", EventKind: "report", SourceMode: "local", Provider: "local", Status: "success", OccurredAt: day},
	})
	if err != nil {
		t.Fatalf("IngestActivityEvents failed: %v", err)
	}
	_, err = st.IngestActivityEvents(ctx, orgA.ID, beta.ID, []ActivityEventInput{
		{EventID: "ord-b-1", EventKind: "transcription", SourceMode: "cloud_direct", Provider: "whisper", Status: "success", OccurredAt: day},
		{EventID: "ord-b-2", EventKind: "report", SourceMode: "cloud_direct", Provider: "mistral", Status: "success", OccurredAt: day},
	})
	if err != nil {
		t.Fatalf("IngestActivityEvents failed: %v", err)
	}
	_, err = st.IngestActivityEvents(ctx, orgB.ID, other.ID, []ActivityEventInput{{
		EventID: "ord-o-1", EventKind: "report", SourceMode: "cloud_backend", Provider: "demeter_sante", Status: "success", OccurredAt: day,
	}})
	if err != nil {
		t.Fatalf("IngestActivityEvents failed: %v", err)
	}

	summary, err := st.GetOrganizationActivitySummary(ctx, orgA.ID, "2026-03-12", "2026-03-12")
	if err != nil {
		t.Fatalf("GetOrganizationActivitySummary failed: %v", err)
	}
	if summary.Totals.Transcriptions != 2 || summary.Totals.Reports != 2 {
		t.Fatalf("unexpected org summary totals: %+v", summary.Totals)
	}
	if len(summary.ByUser) != 2 {
		t.Fatalf("expected 2 org users in summary, got %+v", summary.ByUser)
	}
	if summary.ByUser[0].Email != "alpha@example.com" || summary.ByUser[1].Email != "beta@example.com" {
		t.Fatalf("expected by-user ordering by email on equal totals, got %+v", summary.ByUser)
	}
}
