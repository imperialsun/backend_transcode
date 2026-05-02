package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"demeter-backend/internal/backenderrors"
	"demeter-backend/internal/mistral"
	"demeter-backend/internal/reports"
	"demeter-backend/internal/store"
)

func TestDemeterReportQueueCompletesGenerationForCRN(t *testing.T) {
	st := openAPITestStore(t, "demeter-report-queue-backend-error.sqlite")
	backenderrors.RegisterSink(st)
	t.Cleanup(func() {
		backenderrors.RegisterSink(nil)
	})

	org := createTestOrganization(t, st, "Demo Org", "demo-org", "active")
	user := createTestUser(t, st, org.ID, "worker@example.com", "hashed-password", "active")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{
				map[string]any{
					"message": map[string]any{
						"content": `{"title":"Compte rendu CRN","sections":[{"heading":"Résumé","paragraphs":["Test"]}]}`,
					},
				},
			},
		})
	}))
	defer server.Close()

	app := &App{
		Store:         st,
		MistralClient: mistral.NewClient(server.URL, "key", time.Second, time.Second),
	}
	manager := app.EnsureDemeterReportQueueManager()

	now := time.Now().UTC()
	payload := &demeterReportQueueOperationPayload{
		TraceID:      "trace-crn-format-error",
		Route:        "/providers/demeter-sante/report/operations",
		Seq:          7,
		MeetingTitle: "Réunion CRN",
		Participants: []string{"Alice"},
		SourceText:   "source text",
		Format:       reports.ReportFormatCRN,
		DetailLevel:  reports.ReportDetailStandard,
		ModelID:      "mistral-medium-latest",
		Temperature:  0,
		MaxTokens:    1024,
		CreatedAt:    now,
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}

	record := &store.DemeterReportOperationRecord{
		OperationID:      "op-crn-format-error",
		OrganizationID:   org.ID,
		UserID:           user.ID,
		QueueID:          1,
		Status:           store.DemeterReportOperationStatusPending,
		Stage:            "queued",
		FormatIndex:      2,
		FormatCount:      2,
		Progress:         0,
		QueuePayloadJSON: sql.NullString{String: string(rawPayload), Valid: true},
		StatusCode:       http.StatusAccepted,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := st.CreateDemeterReportOperation(context.Background(), record); err != nil {
		t.Fatalf("failed to create report operation: %v", err)
	}

	err = manager.processClaimedOperation(record, payload, 1)
	if err != nil {
		t.Fatalf("expected report generation to succeed: %v", err)
	}

	stored, err := st.GetDemeterReportOperation(context.Background(), record.OperationID, record.OrganizationID, record.UserID)
	if err != nil {
		t.Fatalf("failed to reload report operation: %v", err)
	}
	if stored.Status != store.DemeterReportOperationStatusCompleted {
		t.Fatalf("expected completed status, got %s", stored.Status)
	}
	if stored.Stage != "completed" {
		t.Fatalf("expected completed stage, got %s", stored.Stage)
	}
	if !stored.ResponseJSON.Valid || !strings.Contains(stored.ResponseJSON.String, `"format":"CRN"`) {
		t.Fatalf("expected persisted CRN response, got %#v", stored.ResponseJSON)
	}

	for attempt := 0; attempt < 40; attempt++ {
		result, err := st.ListBackendErrorEvents(context.Background(), store.BackendErrorEventFilters{
			Component: "demeter",
			Query:     payload.TraceID,
			Limit:     10,
		})
		if err != nil {
			t.Fatalf("failed to list backend error events: %v", err)
		}
		if len(result.Items) > 0 {
			t.Fatalf("expected no backend error events, got %#v", result.Items[0])
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestDemeterReportQueueCompletesReportTemplateDraft(t *testing.T) {
	st := openAPITestStore(t, "demeter-report-template-draft.sqlite")
	org := createTestOrganization(t, st, "Draft Org", "draft-org", "active")
	user := createTestUser(t, st, org.ID, "draft-worker@example.com", "hashed-password", "active")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{
				map[string]any{
					"message": map[string]any{
						"content": `{"name":"CR Coordination","description":"Synthèse coordination","baseFormat":"CRI","instructions":"Inclure décisions, risques et prochaines étapes.","exampleOutline":"1. Résumé\n2. Décisions\n3. Actions"}`,
					},
				},
			},
		})
	}))
	defer server.Close()

	app := &App{
		Store:         st,
		MistralClient: mistral.NewClient(server.URL, "key", time.Second, time.Second),
	}
	manager := app.EnsureDemeterReportQueueManager()

	now := time.Now().UTC()
	payload := &demeterReportQueueOperationPayload{
		TraceID:     "trace-template-draft",
		Route:       "/admin/organizations/org/report-templates/draft-operations",
		Seq:         8,
		Kind:        demeterReportQueueKindTemplateDraft,
		ModelID:     "mistral-medium-latest",
		Temperature: 0,
		MaxTokens:   1024,
		DraftBrief:  "Modèle pour réunion de coordination",
		CreatedAt:   now,
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}
	record := &store.DemeterReportOperationRecord{
		OperationID:      "op-template-draft",
		OrganizationID:   org.ID,
		UserID:           user.ID,
		QueueID:          1,
		Status:           store.DemeterReportOperationStatusPending,
		Stage:            "queued",
		FormatCount:      1,
		QueuePayloadJSON: sql.NullString{String: string(rawPayload), Valid: true},
		StatusCode:       http.StatusAccepted,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := st.CreateDemeterReportOperation(context.Background(), record); err != nil {
		t.Fatalf("failed to create report operation: %v", err)
	}

	if err := manager.processClaimedOperation(record, payload, 1); err != nil {
		t.Fatalf("expected template draft generation to succeed: %v", err)
	}

	stored, err := st.GetDemeterReportOperation(context.Background(), record.OperationID, record.OrganizationID, record.UserID)
	if err != nil {
		t.Fatalf("failed to reload report operation: %v", err)
	}
	if stored.Status != store.DemeterReportOperationStatusCompleted {
		t.Fatalf("expected completed status, got %s", stored.Status)
	}
	if !stored.ResponseJSON.Valid || !strings.Contains(stored.ResponseJSON.String, `"kind":"report_template_draft"`) {
		t.Fatalf("expected persisted template draft response, got %#v", stored.ResponseJSON)
	}
}

func TestDemeterReportQueueFailsInvalidReportTemplateDraftJSON(t *testing.T) {
	st := openAPITestStore(t, "demeter-report-template-draft-invalid.sqlite")
	org := createTestOrganization(t, st, "Invalid Draft Org", "invalid-draft-org", "active")
	user := createTestUser(t, st, org.ID, "invalid-draft-worker@example.com", "hashed-password", "active")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{
				map[string]any{
					"message": map[string]any{"content": `{"name": "Broken"`},
				},
			},
		})
	}))
	defer server.Close()

	app := &App{
		Store:         st,
		MistralClient: mistral.NewClient(server.URL, "key", time.Second, time.Second),
	}
	manager := app.EnsureDemeterReportQueueManager()

	now := time.Now().UTC()
	payload := &demeterReportQueueOperationPayload{
		TraceID:     "trace-template-draft-invalid",
		Route:       "/admin/organizations/org/report-templates/draft-operations",
		Seq:         9,
		Kind:        demeterReportQueueKindTemplateDraft,
		ModelID:     "mistral-medium-latest",
		Temperature: 0,
		MaxTokens:   1024,
		DraftBrief:  "Modèle invalide",
		CreatedAt:   now,
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}
	record := &store.DemeterReportOperationRecord{
		OperationID:      "op-template-draft-invalid",
		OrganizationID:   org.ID,
		UserID:           user.ID,
		QueueID:          1,
		Status:           store.DemeterReportOperationStatusPending,
		Stage:            "queued",
		FormatCount:      1,
		QueuePayloadJSON: sql.NullString{String: string(rawPayload), Valid: true},
		StatusCode:       http.StatusAccepted,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := st.CreateDemeterReportOperation(context.Background(), record); err != nil {
		t.Fatalf("failed to create report operation: %v", err)
	}

	if err := manager.processClaimedOperation(record, payload, 1); err == nil {
		t.Fatal("expected template draft generation to fail")
	}

	stored, err := st.GetDemeterReportOperation(context.Background(), record.OperationID, record.OrganizationID, record.UserID)
	if err != nil {
		t.Fatalf("failed to reload report operation: %v", err)
	}
	if stored.Status != store.DemeterReportOperationStatusFailed {
		t.Fatalf("expected failed status, got %s", stored.Status)
	}
	if !stored.LastError.Valid || !strings.Contains(stored.LastError.String, "JSON invalide") {
		t.Fatalf("expected invalid JSON error, got %#v", stored.LastError)
	}
}

func TestParseReportTemplateDraftExtractsJSONEnvelope(t *testing.T) {
	content := "Voici le brouillon:\n```json\n{\"name\":\"CR Suivi\",\"description\":\"Suivi court\",\"baseFormat\":\"CRS\",\"instructions\":\"Inclure les décisions et les échéances.\",\"exampleOutline\":\"1. Evènements\\n2. Décisions\\n3. Prochaines échéances\"}\n```"

	draft, err := parseReportTemplateDraft(content)
	if err != nil {
		t.Fatalf("expected draft to parse from enveloped JSON: %v", err)
	}
	if draft.Name != "CR Suivi" || draft.BaseFormat != "CRS" {
		t.Fatalf("unexpected draft: %#v", draft)
	}
}
