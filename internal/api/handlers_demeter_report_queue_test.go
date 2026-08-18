package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"demeter-backend/internal/store"
)

func TestDemeterReportOperationHandlerQueuesClarification(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	app, token, appCtx := setupDemeterRoutesApp(t, []store.UserPermissionOverrideInput{
		{PermissionCode: "feature.llmapi", Effect: "allow"},
		{PermissionCode: "provider.llm.demeter_sante", Effect: "allow"},
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"needsClarification\":false,\"questions\":[]}"}}]}`))
	}))
	defer close(release)

	response := performJSONRequest(t, app, http.MethodPost, "/api/v1/providers/demeter-sante/report/operations", token, `{
		"operationType":"clarification",
		"sourceText":"CR équipe / budget",
		"sourceKind":"word_note",
		"modelId":"mistral-medium-latest",
		"temperature":0,
		"maxTokens":512
	}`)
	if response.StatusCode != http.StatusAccepted && response.StatusCode != http.StatusOK {
		t.Fatalf("expected queued or completed response, got %d: %s", response.StatusCode, response.Body)
	}

	var operation struct {
		OperationID string `json:"operationId"`
	}
	if err := json.Unmarshal(response.Body, &operation); err != nil {
		t.Fatalf("failed to decode operation response: %v", err)
	}
	if strings.TrimSpace(operation.OperationID) == "" {
		t.Fatalf("expected operation id, got %s", response.Body)
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("expected clarification worker to reach Mistral")
	}

	record, err := appCtx.Store.GetDemeterReportOperationByID(t.Context(), operation.OperationID)
	if err != nil {
		t.Fatalf("failed to load queued operation: %v", err)
	}
	var payload demeterReportQueueOperationPayload
	if err := json.Unmarshal([]byte(record.QueuePayloadJSON.String), &payload); err != nil {
		t.Fatalf("failed to decode queue payload: %v", err)
	}
	if payload.Kind != demeterReportQueueKindClarification {
		t.Fatalf("expected clarification queue kind, got %q", payload.Kind)
	}
	if payload.SourceKind != "word_note" {
		t.Fatalf("expected word_note source kind, got %q", payload.SourceKind)
	}
}
