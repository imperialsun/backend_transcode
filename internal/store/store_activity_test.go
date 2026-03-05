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
