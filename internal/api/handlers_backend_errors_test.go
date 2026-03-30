package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"demeter-backend/internal/backenderrors"
	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
)

func TestListBackendErrorEvents_RespectsScopeAndFilters(t *testing.T) {
	fixture := setupBackendErrorRoutesTest(t)
	if err := fixture.store.SetUserGlobalRoles(context.Background(), fixture.superAdminUser.ID, []string{"super_admin"}); err != nil {
		t.Fatalf("failed to set super admin roles: %v", err)
	}

	now := time.Date(2026, 3, 30, 15, 45, 23, 0, time.UTC)
	mustInsertBackendErrorEvent(t, fixture.store, backenderrors.Event{
		TraceID:        "trace-org-1-a",
		UserID:         fixture.orgAdminUser.ID,
		OrganizationID: fixture.org.ID,
		Component:      "admin",
		Route:          "/admin/backend-errors",
		Step:           "load_error",
		Title:          "list_backend_errors",
		StatusCode:     500,
		DurationMS:     31,
		ErrorMessage:   "boom alpha",
		PayloadJSON:    json.RawMessage(`{"error":"boom alpha"}`),
		CreatedAt:      now.Add(-2 * time.Hour),
	})
	mustInsertBackendErrorEvent(t, fixture.store, backenderrors.Event{
		TraceID:        "trace-org-1-b",
		UserID:         fixture.actor.ID,
		OrganizationID: fixture.org.ID,
		Component:      "store",
		Route:          "sqlite",
		Step:           "update_error",
		Title:          "store_backend_error",
		StatusCode:     500,
		DurationMS:     12,
		ErrorMessage:   "boom beta",
		PayloadJSON:    json.RawMessage(`{"error":"boom beta"}`),
		CreatedAt:      now.Add(-90 * time.Minute),
	})
	mustInsertBackendErrorEvent(t, fixture.store, backenderrors.Event{
		TraceID:        "trace-org-2",
		UserID:         fixture.otherOrgMember.ID,
		OrganizationID: fixture.otherOrg.ID,
		Component:      "admin",
		Route:          "/admin/activity",
		Step:           "load_error",
		Title:          "list_backend_errors",
		StatusCode:     500,
		DurationMS:     18,
		ErrorMessage:   "boom gamma",
		PayloadJSON:    json.RawMessage(`{"error":"boom gamma"}`),
		CreatedAt:      now.Add(-30 * time.Minute),
	})

	orgScopeResp := performPasswordResetRequest(
		t,
		fixture.app,
		http.MethodGet,
		"/api/v1/admin/backend-errors?organizationId="+fixture.org.ID+"&page=1&pageSize=10",
		nil,
		nil,
		adminHeaders(t, fixture.superAdminUser, fixture.appCtx.Config.JWTSecret),
	)
	if orgScopeResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for org-scoped list, got %d", orgScopeResp.StatusCode)
	}

	var orgPayload backendErrorEventsResponse
	if err := json.NewDecoder(orgScopeResp.Body).Decode(&orgPayload); err != nil {
		t.Fatalf("failed to decode list payload: %v", err)
	}
	if orgPayload.Total != 2 {
		t.Fatalf("expected 2 org-scoped events, got %d", orgPayload.Total)
	}
	if len(orgPayload.Items) != 2 {
		t.Fatalf("expected 2 org-scoped items, got %d", len(orgPayload.Items))
	}
	for _, item := range orgPayload.Items {
		if item.OrganizationID != fixture.org.ID {
			t.Fatalf("expected org scope to stay on %s, got %#v", fixture.org.ID, item)
		}
	}

	superResp := performPasswordResetRequest(
		t,
		fixture.app,
		http.MethodGet,
		"/api/v1/admin/backend-errors?q=gamma&page=1&pageSize=10",
		nil,
		nil,
		adminHeaders(t, fixture.superAdminUser, fixture.appCtx.Config.JWTSecret),
	)
	if superResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 for super admin list, got %d", superResp.StatusCode)
	}

	var superPayload backendErrorEventsResponse
	if err := json.NewDecoder(superResp.Body).Decode(&superPayload); err != nil {
		t.Fatalf("failed to decode super payload: %v", err)
	}
	if superPayload.Total != 1 {
		t.Fatalf("expected 1 filtered event, got %d", superPayload.Total)
	}
	if len(superPayload.Items) != 1 || superPayload.Items[0].TraceID != "trace-org-2" {
		t.Fatalf("unexpected filtered payload: %+v", superPayload.Items)
	}
}

func TestListBackendErrorEvents_RejectsNonSuperAdmin(t *testing.T) {
	fixture := setupBackendErrorRoutesTest(t)
	if err := fixture.store.SetUserOrganizationRoles(context.Background(), fixture.orgAdminUser.ID, []string{"org_admin"}); err != nil {
		t.Fatalf("failed to set org admin roles: %v", err)
	}

	resp := performPasswordResetRequest(
		t,
		fixture.app,
		http.MethodGet,
		"/api/v1/admin/backend-errors?page=1&pageSize=10",
		nil,
		nil,
		adminHeaders(t, fixture.orgAdminUser, fixture.appCtx.Config.JWTSecret),
	)
	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 for non super admin list, got %d", resp.StatusCode)
	}
}

func TestDeleteBackendErrorEvents_PurgesFilteredHistoryAndWritesAudit(t *testing.T) {
	fixture := setupBackendErrorRoutesTest(t)
	if err := fixture.store.SetUserGlobalRoles(context.Background(), fixture.superAdminUser.ID, []string{"super_admin"}); err != nil {
		t.Fatalf("failed to set super admin roles: %v", err)
	}

	now := time.Date(2026, 3, 30, 15, 45, 23, 0, time.UTC)
	mustInsertBackendErrorEvent(t, fixture.store, backenderrors.Event{
		TraceID:        "trace-delete-1",
		UserID:         fixture.orgAdminUser.ID,
		OrganizationID: fixture.org.ID,
		Component:      "admin",
		Route:          "/admin/backend-errors",
		Step:           "load_error",
		Title:          "list_backend_errors",
		StatusCode:     500,
		DurationMS:     31,
		ErrorMessage:   "purge me",
		PayloadJSON:    json.RawMessage(`{"error":"purge me"}`),
		CreatedAt:      now,
	})
	mustInsertBackendErrorEvent(t, fixture.store, backenderrors.Event{
		TraceID:        "trace-delete-2",
		UserID:         fixture.actor.ID,
		OrganizationID: fixture.org.ID,
		Component:      "store",
		Route:          "sqlite",
		Step:           "update_error",
		Title:          "store_backend_error",
		StatusCode:     500,
		DurationMS:     12,
		ErrorMessage:   "keep me",
		PayloadJSON:    json.RawMessage(`{"error":"keep me"}`),
		CreatedAt:      now.Add(1 * time.Minute),
	})

	resp := performPasswordResetRequest(
		t,
		fixture.app,
		http.MethodDelete,
		"/api/v1/admin/backend-errors?component=admin&q=purge",
		nil,
		nil,
		adminHeaders(t, fixture.superAdminUser, fixture.appCtx.Config.JWTSecret),
	)
	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 for purge, got %d", resp.StatusCode)
	}
	closeHTTPResponse(t, resp)

	var remaining int
	if err := fixture.store.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM backend_error_events`).Scan(&remaining); err != nil {
		t.Fatalf("failed to count backend errors: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("expected 1 remaining backend error, got %d", remaining)
	}

	var auditCount int
	if err := fixture.store.DB.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM audit_logs WHERE action = 'admin.backend_error.purge'
	`).Scan(&auditCount); err != nil {
		t.Fatalf("failed to count purge audits: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("expected one purge audit, got %d", auditCount)
	}
}

func TestDeleteBackendErrorEvents_RejectsNonSuperAdmin(t *testing.T) {
	fixture := setupBackendErrorRoutesTest(t)
	if err := fixture.store.SetUserOrganizationRoles(context.Background(), fixture.orgAdminUser.ID, []string{"org_admin"}); err != nil {
		t.Fatalf("failed to set org admin roles: %v", err)
	}

	resp := performPasswordResetRequest(
		t,
		fixture.app,
		http.MethodDelete,
		"/api/v1/admin/backend-errors?component=admin&q=purge",
		nil,
		nil,
		adminHeaders(t, fixture.orgAdminUser, fixture.appCtx.Config.JWTSecret),
	)
	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 for non super admin purge, got %d", resp.StatusCode)
	}
}

func setupBackendErrorRoutesTest(t *testing.T) *adminDeleteFixture {
	t.Helper()
	fixture := setupAdminDeleteRoutesTest(t)
	if err := fixture.store.SetUserGlobalRoles(context.Background(), fixture.superAdminUser.ID, []string{"super_admin"}); err != nil {
		t.Fatalf("failed to set super admin roles: %v", err)
	}
	return fixture
}

func mustInsertBackendErrorEvent(t *testing.T, st *store.Store, event backenderrors.Event) {
	t.Helper()
	if err := st.InsertBackendErrorEvent(context.Background(), event); err != nil {
		t.Fatalf("failed to insert backend error event: %v", err)
	}
}
