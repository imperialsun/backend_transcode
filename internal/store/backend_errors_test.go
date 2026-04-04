package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"demeter-backend/internal/backenderrors"
)

func TestBackendErrorEvents_ListAndDelete(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "backend-errors.sqlite")
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	org1, err := st.CreateOrganization(ctx, "Org 1", "org-1", "active")
	if err != nil {
		t.Fatalf("failed to create org1: %v", err)
	}
	org2, err := st.CreateOrganization(ctx, "Org 2", "org-2", "active")
	if err != nil {
		t.Fatalf("failed to create org2: %v", err)
	}
	user1, err := st.CreateUser(ctx, org1.ID, "user1@example.com", "active", "password1")
	if err != nil {
		t.Fatalf("failed to create user1: %v", err)
	}
	user2, err := st.CreateUser(ctx, org1.ID, "user2@example.com", "active", "password2")
	if err != nil {
		t.Fatalf("failed to create user2: %v", err)
	}
	user3, err := st.CreateUser(ctx, org2.ID, "user3@example.com", "active", "password3")
	if err != nil {
		t.Fatalf("failed to create user3: %v", err)
	}

	now := time.Date(2026, 3, 30, 15, 45, 23, 0, time.UTC)
	mustInsertBackendErrorEvent(t, st, backenderrors.Event{
		TraceID:        "trace-1",
		UserID:         user1.ID,
		OrganizationID: org1.ID,
		Component:      "admin",
		Route:          "/admin/backend-errors",
		Step:           "load_error",
		Title:          "list_backend_errors",
		StatusCode:     500,
		DurationMS:     42,
		ErrorMessage:   "boom alpha",
		PayloadJSON:    json.RawMessage(`{"error":"boom alpha"}`),
		CreatedAt:      now.Add(-2 * time.Hour),
	})
	mustInsertBackendErrorEvent(t, st, backenderrors.Event{
		TraceID:        "trace-2",
		UserID:         user2.ID,
		OrganizationID: org1.ID,
		Component:      "store",
		Route:          "sqlite",
		Step:           "update_error",
		Title:          "store_backend_error",
		StatusCode:     500,
		DurationMS:     17,
		ErrorMessage:   "boom beta",
		PayloadJSON:    json.RawMessage(`{"error":"boom beta"}`),
		CreatedAt:      now.Add(-time.Hour),
	})
	mustInsertBackendErrorEvent(t, st, backenderrors.Event{
		TraceID:        "trace-3",
		UserID:         user3.ID,
		OrganizationID: org2.ID,
		Component:      "admin",
		Route:          "/admin/activity",
		Step:           "load_error",
		Title:          "list_backend_errors",
		StatusCode:     500,
		DurationMS:     9,
		ErrorMessage:   "boom gamma",
		PayloadJSON:    json.RawMessage(`{"error":"boom gamma"}`),
		CreatedAt:      now,
	})

	result, err := st.ListBackendErrorEvents(ctx, BackendErrorEventFilters{
		OrganizationID: org1.ID,
		UserID:         user1.ID,
		Component:      "admin",
		Route:          "/admin/backend-errors",
		Query:          "alpha",
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("failed to list backend errors: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("expected one matching error, got %d", result.Total)
	}
	if len(result.Items) != 1 || result.Items[0].TraceID != "trace-1" {
		t.Fatalf("unexpected list result: %+v", result.Items)
	}

	deleted, err := st.DeleteBackendErrorEvents(ctx, BackendErrorEventFilters{
		OrganizationID: org1.ID,
		UserID:         user2.ID,
		Query:          "beta",
	})
	if err != nil {
		t.Fatalf("failed to delete backend errors: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected one deleted event, got %d", deleted)
	}

	remaining, err := st.ListBackendErrorEvents(ctx, BackendErrorEventFilters{
		OrganizationID: org1.ID,
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("failed to list remaining backend errors: %v", err)
	}
	if remaining.Total != 1 {
		t.Fatalf("expected one remaining event in org1, got %d", remaining.Total)
	}
	if remaining.Items[0].TraceID != "trace-1" {
		t.Fatalf("unexpected remaining item: %+v", remaining.Items[0])
	}
}

func TestPurgeExpiredBackendErrorEventsRemovesOldRows(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "backend-errors-purge.sqlite")
	st, err := Open(ctx, dbPath)
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

	now := time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC)
	mustInsertBackendErrorEvent(t, st, backenderrors.Event{
		TraceID:        "trace-old",
		OrganizationID: org.ID,
		Component:      "admin",
		Route:          "/admin/backend-errors",
		Step:           "load_error",
		Title:          "old_backend_error",
		StatusCode:     500,
		DurationMS:     11,
		ErrorMessage:   "old boom",
		PayloadJSON:    json.RawMessage(`{"error":"old boom"}`),
		CreatedAt:      now.AddDate(0, 0, -31),
	})
	mustInsertBackendErrorEvent(t, st, backenderrors.Event{
		TraceID:        "trace-new",
		OrganizationID: org.ID,
		Component:      "admin",
		Route:          "/admin/backend-errors",
		Step:           "load_error",
		Title:          "new_backend_error",
		StatusCode:     500,
		DurationMS:     13,
		ErrorMessage:   "new boom",
		PayloadJSON:    json.RawMessage(`{"error":"new boom"}`),
		CreatedAt:      now,
	})

	deleted, err := st.PurgeExpiredBackendErrorEvents(ctx, now)
	if err != nil {
		t.Fatalf("failed to purge backend errors: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected one deleted event, got %d", deleted)
	}

	result, err := st.ListBackendErrorEvents(ctx, BackendErrorEventFilters{
		OrganizationID: org.ID,
		From:           now.AddDate(0, 0, -40),
		To:             now,
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("failed to list backend errors after purge: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("expected one remaining backend error, got %d", result.Total)
	}
	if len(result.Items) != 1 || result.Items[0].TraceID != "trace-new" {
		t.Fatalf("unexpected remaining backend errors: %+v", result.Items)
	}
}

func mustInsertBackendErrorEvent(t *testing.T, st *Store, event backenderrors.Event) {
	t.Helper()
	if err := st.InsertBackendErrorEvent(context.Background(), event); err != nil {
		t.Fatalf("failed to insert backend error event: %v", err)
	}
}
