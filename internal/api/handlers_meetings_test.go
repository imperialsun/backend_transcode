package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/mailer"
	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
)

type fakeMeetingMailer struct {
	readyErr error
	sendErr  error
	sent     []mailer.MeetingSummaryEmail
}

func (m *fakeMeetingMailer) Ready() error {
	return m.readyErr
}

func (m *fakeMeetingMailer) SendPasswordResetEmail(_ context.Context, _ mailer.PasswordResetEmail) error {
	return nil
}

func (m *fakeMeetingMailer) SendMeetingSummaryEmail(_ context.Context, input mailer.MeetingSummaryEmail) error {
	if m.sendErr != nil {
		return m.sendErr
	}
	m.sent = append(m.sent, input)
	return nil
}

func TestMeetingDraftsAndFinalizeSendsMailAndActivity(t *testing.T) {
	app, token, appCtx := setupDemeterRoutesApp(t, []store.UserPermissionOverrideInput{
		{PermissionCode: "feature.llmapi", Effect: "allow"},
		{PermissionCode: "provider.llm.demeter_sante", Effect: "allow"},
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode upstream payload: %v", err)
		}
		messages, _ := payload["messages"].([]any)
		content := ""
		if len(messages) > 1 {
			if user, ok := messages[1].(map[string]any); ok {
				content, _ = user["content"].(string)
			}
		}
		format := "CRI"
		switch {
		case strings.Contains(content, "CRO"):
			format = "CRO"
		case strings.Contains(content, "CRS"):
			format = "CRS"
		}

		reportJSON := fmt.Sprintf(`{"format":"%s","title":"%s","sections":[{"heading":"Contexte","paragraphs":["Paragraphe %s"]}],"key_points":["Point %s"],"action_items":["Action %s"],"caveats":["Caveat %s"]}`, format, format, format, format, format, format)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{
				map[string]any{
					"message": map[string]any{
						"content": reportJSON,
					},
				},
			},
		})
	}))

	mailerStub := &fakeMeetingMailer{}
	appCtx.Mailer = mailerStub
	appCtx.RegisterMeetingRoutes(app.Group("/api/v1"))

	draftResp := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodPost,
		"/api/v1/meetings/reports/drafts",
		map[string]any{
			"meetingTitle":      "Revue qualité",
			"participants":      []string{"Alice", "Bob"},
			"rawTranscriptText": "Bonjour tout le monde.",
			"selectedFormats":   []string{"CRI", "CRO"},
				"reportModelId":     "mistral-medium-latest",
			"reportTemperature": 0,
			"reportMaxTokens":   2048,
		},
		nil,
		map[string]string{fiber.HeaderAuthorization: "Bearer " + token},
	)
	if draftResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from drafts endpoint, got %d", draftResp.StatusCode)
	}

	var draftPayload meetingDraftResponse
	if err := json.NewDecoder(draftResp.Body).Decode(&draftPayload); err != nil {
		t.Fatalf("failed to decode draft response: %v", err)
	}
	if len(draftPayload.Reports) != 2 {
		t.Fatalf("expected 2 draft reports, got %d", len(draftPayload.Reports))
	}
	if draftPayload.ReportSourceMode != meetingReportSourceMode || draftPayload.ReportProvider != meetingReportProvider {
		t.Fatalf("unexpected report source metadata: %+v", draftPayload)
	}

	finalResp := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodPost,
		"/api/v1/meetings/finalize",
		map[string]any{
			"meetingTitle":            "Revue qualité",
			"participants":            []string{"Alice", "Bob"},
			"transcriptionSourceMode": "local",
			"transcriptionProvider":   "mic",
			"rawTranscriptText":       "Bonjour tout le monde.",
			"editedTranscriptText":    "Bonjour tout le monde.",
			"selectedFormats":         []string{"CRI", "CRO"},
			"reports":                 draftPayload.Reports,
		},
		nil,
		map[string]string{fiber.HeaderAuthorization: "Bearer " + token},
	)
	if finalResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from finalize endpoint, got %d", finalResp.StatusCode)
	}

	var finalPayload meetingFinalizeResponse
	if err := json.NewDecoder(finalResp.Body).Decode(&finalPayload); err != nil {
		t.Fatalf("failed to decode finalize response: %v", err)
	}
	if len(finalPayload.ReportDocxFilenames) != 2 {
		t.Fatalf("expected 2 report docx filenames, got %d", len(finalPayload.ReportDocxFilenames))
	}
	if finalPayload.TranscriptDocxFilename == "" || finalPayload.Attachments == nil || len(finalPayload.Attachments) != 3 {
		t.Fatalf("unexpected final payload: %+v", finalPayload)
	}
	if len(mailerStub.sent) != 1 {
		t.Fatalf("expected one meeting email, got %d", len(mailerStub.sent))
	}
	if len(mailerStub.sent[0].Attachments) != 3 {
		t.Fatalf("expected 3 attachments in mail, got %d", len(mailerStub.sent[0].Attachments))
	}
	if !containsMeetingFilename(mailerStub.sent[0].Attachments[0].Filename, "transcription-") {
		t.Fatalf("expected raw transcript attachment first, got %+v", mailerStub.sent[0].Attachments)
	}

	claims, err := auth.ParseAccessToken("test-secret", token)
	if err != nil {
		t.Fatalf("failed to parse access token: %v", err)
	}
	today := time.Now().UTC().Format(activityDayLayout)
	summary, err := appCtx.Store.GetOrganizationActivitySummary(context.Background(), claims.OrgID, today, today)
	if err != nil {
		t.Fatalf("failed to load activity summary: %v", err)
	}
	if summary.Totals.Transcriptions != 1 || summary.Totals.Reports != 1 {
		t.Fatalf("unexpected activity summary: %+v", summary)
	}
}

func containsMeetingFilename(filename, prefix string) bool {
	return len(filename) >= len(prefix) && filename[:len(prefix)] == prefix
}
