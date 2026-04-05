package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"demeter-backend/internal/backenderrors"
	"demeter-backend/internal/mistral"
	"demeter-backend/internal/observability"
	"demeter-backend/internal/requestmeta"
	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
)

func TestDemeterOwnershipStatusAndCancelLogs(t *testing.T) {
	app, token, appCtx := setupDemeterRoutesApp(t, demeterAudioOwnershipTestOverrides(), nil)
	backenderrors.RegisterSink(appCtx.Store)
	t.Cleanup(func() {
		backenderrors.RegisterSink(nil)
	})

	ctx := context.Background()
	user, err := appCtx.Store.FindUserByEmail(ctx, "u@example.com")
	if err != nil {
		t.Fatalf("failed to load seeded user: %v", err)
	}
	otherUser, err := appCtx.Store.CreateUser(ctx, user.OrganizationID, "other-owner@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("failed to create other owner: %v", err)
	}

	statusOpID := "demeter-audio-status-ownership-test"
	cancelOpID := "demeter-audio-cancel-ownership-test"
	now := time.Now().UTC()
	for _, opID := range []string{statusOpID, cancelOpID} {
		record := &store.DemeterAudioTranscriptionOperationRecord{
			OperationID:    opID,
			OrganizationID: user.OrganizationID,
			UserID:         otherUser.ID,
			Status:         store.DemeterAudioTranscriptionOperationStatusRunning,
			Stage:          "queued",
			ChunkIndex:     0,
			ChunkCount:     1,
			Progress:       0,
			StatusCode:     http.StatusAccepted,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		if err := appCtx.Store.CreateDemeterAudioTranscriptionOperation(ctx, record); err != nil {
			t.Fatalf("failed to create record %s: %v", opID, err)
		}
	}

	statusResp := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodGet,
		"/api/v1/providers/demeter-sante/audio/transcriptions/operations/"+statusOpID,
		nil,
		nil,
		map[string]string{
			fiber.HeaderAuthorization: "Bearer " + token,
		},
	)
	if statusResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for ownership mismatch status, got %d", statusResp.StatusCode)
	}
	statusTraceID := statusResp.Header.Get("X-Trace-Id")
	if statusTraceID == "" {
		t.Fatalf("expected trace id on status response")
	}
	statusEvent := waitForBackendErrorEventByTraceAndStep(t, appCtx.Store, statusTraceID, "ownership_status_error")
	if statusEvent == nil {
		t.Fatalf("expected ownership status event to be persisted")
	}
	assertOwnershipPayload(t, statusEvent.PayloadJSON, user.ID, user.OrganizationID, otherUser.ID, user.OrganizationID, "api_status")

	cancelResp := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodDelete,
		"/api/v1/providers/demeter-sante/audio/transcriptions/operations/"+cancelOpID,
		nil,
		nil,
		map[string]string{
			fiber.HeaderAuthorization: "Bearer " + token,
		},
	)
	if cancelResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for ownership mismatch cancel, got %d", cancelResp.StatusCode)
	}
	cancelTraceID := cancelResp.Header.Get("X-Trace-Id")
	if cancelTraceID == "" {
		t.Fatalf("expected trace id on cancel response")
	}
	cancelEvent := waitForBackendErrorEventByTraceAndStep(t, appCtx.Store, cancelTraceID, "ownership_cancel_error")
	if cancelEvent == nil {
		t.Fatalf("expected ownership cancel event to be persisted")
	}
	assertOwnershipPayload(t, cancelEvent.PayloadJSON, user.ID, user.OrganizationID, otherUser.ID, user.OrganizationID, "api_cancel|store_cancel")
}

func TestDemeterTransportSessionOwnershipMismatchLogs(t *testing.T) {
	app, token, appCtx := setupDemeterRoutesApp(t, demeterAudioOwnershipTestOverrides(), nil)
	appCtx.MistralClient = mistral.NewClient("http://127.0.0.1:1", "key", time.Second, time.Second)
	backenderrors.RegisterSink(appCtx.Store)
	t.Cleanup(func() {
		backenderrors.RegisterSink(nil)
	})

	ctx := context.Background()
	user, err := appCtx.Store.FindUserByEmail(ctx, "u@example.com")
	if err != nil {
		t.Fatalf("failed to load seeded user: %v", err)
	}
	otherUser, err := appCtx.Store.CreateUser(ctx, user.OrganizationID, "transport-owner@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("failed to create other owner: %v", err)
	}

	uploadID := "transport-ownership-test"
	demeterAudioTransportSessions.Store(uploadID, &demeterAudioTransportSession{
		uploadID:      uploadID,
		orgID:         otherUser.OrganizationID,
		userID:        otherUser.ID,
		routeMode:     "relay",
		tempDir:       t.TempDir(),
		receivedPaths: map[int]string{},
		receivedSizes: map[int]int64{},
		createdAt:     time.Now().UTC(),
		updatedAt:     time.Now().UTC(),
	})
	t.Cleanup(func() {
		demeterAudioTransportSessions.Delete(uploadID)
	})

	resp := performDemeterTransportSliceRequest(
		t,
		app,
		token,
		uploadID,
		false,
		"segment_0.wav",
		"audio/wav",
		[]byte("slice-bytes"),
	)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for transport session ownership mismatch, got %d", resp.StatusCode)
	}
	traceID := resp.Header.Get("X-Trace-Id")
	if traceID == "" {
		t.Fatalf("expected trace id on transport response")
	}
	event := waitForBackendErrorEventByTraceAndStep(t, appCtx.Store, traceID, "transport_session_ownership_error")
	if event == nil {
		t.Fatalf("expected transport ownership event to be persisted")
	}
	assertTransportOwnershipPayload(t, event.PayloadJSON, uploadID, user.ID, user.OrganizationID, otherUser.ID, otherUser.OrganizationID, "transport_session")
}

func TestDemeterOwnershipFallbackLogs(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "demeter-ownership-fallback.sqlite")
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
		t.Fatalf("failed to create org: %v", err)
	}
	user, err := st.CreateUser(ctx, org.ID, "u@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	otherUser, err := st.CreateUser(ctx, org.ID, "other@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("failed to create other user: %v", err)
	}

	opID := "demeter-audio-fallback-test"
	now := time.Now().UTC()
	if err := st.CreateDemeterAudioTranscriptionOperation(ctx, &store.DemeterAudioTranscriptionOperationRecord{
		OperationID:    opID,
		OrganizationID: org.ID,
		UserID:         otherUser.ID,
		Status:         store.DemeterAudioTranscriptionOperationStatusRunning,
		Stage:          "queued",
		ChunkIndex:     0,
		ChunkCount:     1,
		StatusCode:     http.StatusAccepted,
		CreatedAt:      now,
		UpdatedAt:      now,
	}); err != nil {
		t.Fatalf("failed to create operation: %v", err)
	}

	app := &App{Store: st}
	traceCtx := observability.WithTraceID(context.Background(), "trace-demeter-fallback")
	traceCtx = requestmeta.WithActor(traceCtx, user.ID, org.ID)

	fallbackUsed, err := app.updateDemeterAudioTranscriptionOperationStateWithFallback(traceCtx, &store.DemeterAudioTranscriptionOperationRecord{
		OperationID:    opID,
		OrganizationID: org.ID,
		UserID:         user.ID,
		Status:         store.DemeterAudioTranscriptionOperationStatusCompleted,
		Stage:          "completed",
		ChunkIndex:     1,
		ChunkCount:     1,
		Progress:       1,
		PartialText:    sql.NullString{String: "hello", Valid: true},
		StatusCode:     http.StatusOK,
		UpdatedAt:      time.Now().UTC(),
		FinishedAt:     sql.NullTime{Time: time.Now().UTC(), Valid: true},
	})
	if err != nil {
		t.Fatalf("expected fallback update to succeed, got %v", err)
	}
	if !fallbackUsed {
		t.Fatalf("expected fallback path to be reported")
	}

	updated, err := st.GetDemeterAudioTranscriptionOperation(ctx, opID, org.ID, otherUser.ID)
	if err != nil {
		t.Fatalf("failed to load updated operation: %v", err)
	}
	if updated.Status != store.DemeterAudioTranscriptionOperationStatusCompleted {
		t.Fatalf("unexpected updated status: %+v", updated)
	}

	event := waitForBackendErrorEventByTraceAndStep(t, st, "trace-demeter-fallback", "ownership_fallback_used_error")
	if event == nil {
		t.Fatalf("expected fallback ownership event to be persisted")
	}
	assertOwnershipPayload(t, event.PayloadJSON, user.ID, org.ID, otherUser.ID, org.ID, "store_update_fallback")
}

func performDemeterTransportSliceRequest(t *testing.T, app *fiber.App, token, uploadID string, final bool, fileName, mimeType string, fileBytes []byte) *http.Response {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatalf("failed to create multipart file part: %v", err)
	}
	_ = mimeType
	if _, err := part.Write(fileBytes); err != nil {
		t.Fatalf("failed to write multipart file part: %v", err)
	}
	if err := writer.WriteField("model", defaultDemeterAudioTranscriptionModelID); err != nil {
		t.Fatalf("failed to write model field: %v", err)
	}
	if err := writer.WriteField("diarize", "true"); err != nil {
		t.Fatalf("failed to write diarize field: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/providers/demeter-sante/audio/transcriptions/backend", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set(fiber.HeaderAuthorization, "Bearer "+token)
	req.Header.Set(demeterAudioTransportHeader, demeterAudioTransportModeSliceV1)
	req.Header.Set(demeterAudioTransportUploadIDHeader, uploadID)
	req.Header.Set(demeterAudioTransportUploadIndexHeader, "0")
	req.Header.Set(demeterAudioTransportUploadCountHeader, "1")
	req.Header.Set(demeterAudioTransportUploadFinalHeader, boolToString(final))
	req.Header.Set("X-Cloud-Audio-Duration-Sec", "2")

	resp, err := app.Test(req, 5_000)
	if err != nil {
		t.Fatalf("transport request failed: %v", err)
	}
	t.Cleanup(func() {
		closeHTTPResponse(t, resp)
	})
	return resp
}

func waitForBackendErrorEventByTraceAndStep(t *testing.T, st *store.Store, traceID, step string) *store.BackendErrorEvent {
	t.Helper()

	for i := 0; i < 60; i++ {
		result, err := st.ListBackendErrorEvents(context.Background(), store.BackendErrorEventFilters{
			Query: traceID,
			Limit: 20,
		})
		if err != nil {
			t.Fatalf("failed to list backend errors: %v", err)
		}
		for _, item := range result.Items {
			if item.Step == step {
				return &item
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	return nil
}

func assertOwnershipPayload(t *testing.T, raw string, requestUserID, requestOrgID, storedUserID, storedOrgID, source string) {
	t.Helper()

	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("failed to decode payload: %v", err)
	}
	if payload["request_user_id"] != requestUserID {
		t.Fatalf("unexpected request user id in payload: %#v", payload)
	}
	if payload["request_org_id"] != requestOrgID {
		t.Fatalf("unexpected request org id in payload: %#v", payload)
	}
	if storedUserID != "" && payload["stored_user_id"] != storedUserID {
		t.Fatalf("unexpected stored user id in payload: %#v", payload)
	}
	if storedOrgID != "" && payload["stored_org_id"] != storedOrgID {
		t.Fatalf("unexpected stored org id in payload: %#v", payload)
	}
	if source != "" {
		allowed := strings.Split(source, "|")
		matched := false
		for _, candidate := range allowed {
			if payload["source"] == candidate {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("unexpected source in payload: %#v", payload)
		}
	}
}

func assertTransportOwnershipPayload(t *testing.T, raw string, uploadID, requestUserID, requestOrgID, storedUserID, storedOrgID, source string) {
	t.Helper()

	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("failed to decode payload: %v", err)
	}
	if payload["upload_id"] != uploadID {
		t.Fatalf("unexpected upload id in payload: %#v", payload)
	}
	if payload["request_user_id"] != requestUserID {
		t.Fatalf("unexpected request user id in payload: %#v", payload)
	}
	if payload["request_org_id"] != requestOrgID {
		t.Fatalf("unexpected request org id in payload: %#v", payload)
	}
	if payload["stored_user_id"] != storedUserID {
		t.Fatalf("unexpected stored user id in payload: %#v", payload)
	}
	if payload["stored_org_id"] != storedOrgID {
		t.Fatalf("unexpected stored org id in payload: %#v", payload)
	}
	if payload["source"] != source {
		t.Fatalf("unexpected source in payload: %#v", payload)
	}
}

func boolToString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func demeterAudioOwnershipTestOverrides() []store.UserPermissionOverrideInput {
	return []store.UserPermissionOverrideInput{
		{PermissionCode: "feature.cloudupload", Effect: "allow"},
		{PermissionCode: "provider.cloud.demeter_sante", Effect: "allow"},
	}
}
