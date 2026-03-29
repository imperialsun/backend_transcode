package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
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
	started  chan struct{}
	release  chan struct{}
	mu       sync.Mutex
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
	m.mu.Lock()
	m.sent = append(m.sent, input)
	started := m.started
	release := m.release
	m.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if release != nil {
		<-release
	}
	return nil
}

func (m *fakeMeetingMailer) sentCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

func (m *fakeMeetingMailer) SendUserProvisioningEmail(_ context.Context, _ mailer.UserProvisioningEmail) error {
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

	var draftResp *http.Response
	draftLogs := captureMeetingLogs(t, func() {
		draftResp = performJSONRequestWithHeaders(
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
	})
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

	var finalResp *http.Response
	finalLogs := captureMeetingLogs(t, func() {
		finalResp = performJSONRequestWithHeaders(
			t,
			app,
			http.MethodPost,
			"/api/v1/meetings/finalize",
			map[string]any{
				"meetingTitle":            "Revue qualité",
				"participants":            []string{"Alice", "Bob"},
				"transcriptionSourceMode": "cloud_backend",
				"transcriptionProvider":   "demeter_sante",
				"rawTranscriptText":       "Bonjour tout le monde.",
				"editedTranscriptText":    "Bonjour tout le monde.",
				"selectedFormats":         []string{"CRI", "CRO"},
				"recipientEmails":         []string{" assistant@example.com ", "ops@example.com"},
				"reports":                 draftPayload.Reports,
			},
			nil,
			map[string]string{fiber.HeaderAuthorization: "Bearer " + token},
		)
	})
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
	if finalPayload.TranscriptionSourceMode != meetingReportSourceMode || finalPayload.TranscriptionProvider != meetingReportProvider {
		t.Fatalf("unexpected transcription source metadata: %+v", finalPayload)
	}
	if len(finalPayload.SentToEmails) != 3 {
		t.Fatalf("expected 3 recipient emails, got %v", finalPayload.SentToEmails)
	}
	if len(mailerStub.sent) != 3 {
		t.Fatalf("expected one mail per recipient, got %d", len(mailerStub.sent))
	}
	if got, want := mailerStub.sent[0].ToEmail, "u@example.com"; got != want {
		t.Fatalf("expected primary recipient %q, got %q", want, got)
	}
	if got, want := mailerStub.sent[1].ToEmail, "assistant@example.com"; got != want {
		t.Fatalf("expected second recipient %q, got %q", want, got)
	}
	if got, want := mailerStub.sent[2].ToEmail, "ops@example.com"; got != want {
		t.Fatalf("expected third recipient %q, got %q", want, got)
	}
	for index, sent := range mailerStub.sent {
		if len(sent.Attachments) != 3 {
			t.Fatalf("expected 3 attachments in mail %d, got %d", index, len(sent.Attachments))
		}
	}
	if !containsMeetingFilename(mailerStub.sent[0].Attachments[0].Filename, "transcription-") {
		t.Fatalf("expected raw transcript attachment first, got %+v", mailerStub.sent[0].Attachments)
	}
	assertMeetingLogs(t, draftLogs, meetingDraftsRoute, []string{
		"request_received",
		"request_parsed",
		"request_normalized",
		"generator_configured",
		"generate_start",
		"generate_success",
		"response_ready",
	})
	assertMeetingLogs(t, finalLogs, meetingFinalizeRoute, []string{
		"request_received",
		"request_parsed",
		"request_normalized",
		"documents_start",
		"documents_success",
		"recipients_resolved",
		"mailer_ready",
		"send_start",
		"send_success",
		"send_start",
		"send_success",
		"send_start",
		"send_success",
		"response_ready",
	})
	assertMeetingLogExcludes(t, draftLogs, []string{
		"Bonjour tout le monde.",
		"assistant@example.com",
		"ops@example.com",
	})
	assertMeetingLogExcludes(t, finalLogs, []string{
		"Bonjour tout le monde.",
		"assistant@example.com",
		"ops@example.com",
	})

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

func TestMeetingDraftsLogsGenerateError(t *testing.T) {
	app, token, appCtx := setupDemeterRoutesApp(t, []store.UserPermissionOverrideInput{
		{PermissionCode: "feature.llmapi", Effect: "allow"},
		{PermissionCode: "provider.llm.demeter_sante", Effect: "allow"},
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"upstream down"}`))
	}))
	appCtx.Mailer = &fakeMeetingMailer{}
	appCtx.RegisterMeetingRoutes(app.Group("/api/v1"))

	var resp *http.Response
	logs := captureMeetingLogs(t, func() {
		resp = performJSONRequestWithHeaders(
			t,
			app,
			http.MethodPost,
			"/api/v1/meetings/reports/drafts",
			map[string]any{
				"meetingTitle":      "Réunion qualité",
				"participants":      []string{"Alice", "Bob"},
				"rawTranscriptText": "Bonjour tout le monde.",
				"selectedFormats":   []string{"CRI"},
				"reportModelId":     "mistral-medium-latest",
			},
			nil,
			map[string]string{fiber.HeaderAuthorization: "Bearer " + token},
		)
	})
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 from drafts failure path, got %d", resp.StatusCode)
	}

	assertMeetingLogs(t, logs, meetingDraftsRoute, []string{
		"request_received",
		"request_parsed",
		"request_normalized",
		"generator_configured",
		"generate_start",
		"generate_error",
	})
	assertMeetingLogExcludes(t, logs, []string{"Bonjour tout le monde."})
}

func TestFinalizeLogsSendError(t *testing.T) {
	app, token, appCtx := setupDemeterRoutesApp(t, []store.UserPermissionOverrideInput{
		{PermissionCode: "feature.llmapi", Effect: "allow"},
		{PermissionCode: "provider.llm.demeter_sante", Effect: "allow"},
	}, nil)
	appCtx.Mailer = &fakeMeetingMailer{sendErr: errors.New("smtp down")}
	appCtx.RegisterMeetingRoutes(app.Group("/api/v1"))

	var resp *http.Response
	logs := captureMeetingLogs(t, func() {
		resp = performJSONRequestWithHeaders(
			t,
			app,
			http.MethodPost,
			"/api/v1/meetings/finalize",
			map[string]any{
				"meetingTitle":            "Réunion qualité",
				"participants":            []string{"Alice", "Bob"},
				"transcriptionSourceMode": "cloud_backend",
				"transcriptionProvider":   "demeter_sante",
				"rawTranscriptText":       "Bonjour tout le monde.",
				"editedTranscriptText":    "Bonjour tout le monde.",
				"selectedFormats":         []string{"CRI"},
				"reports": []meetingReportEnvelope{
					minimalMeetingReportEnvelope("CRI"),
				},
			},
			nil,
			map[string]string{fiber.HeaderAuthorization: "Bearer " + token},
		)
	})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 from finalize failure path, got %d", resp.StatusCode)
	}

	assertMeetingLogs(t, logs, meetingFinalizeRoute, []string{
		"request_received",
		"request_parsed",
		"request_normalized",
		"documents_start",
		"documents_success",
		"recipients_resolved",
		"mailer_ready",
		"send_start",
		"send_error",
	})
	assertMeetingLogExcludes(t, logs, []string{
		"Bonjour tout le monde.",
		"assistant@example.com",
		"ops@example.com",
	})
}

func TestFinalizeMeetingRejectsConcurrentDuplicateBeforeBodyParsing(t *testing.T) {
	app, token, appCtx := setupDemeterRoutesApp(t, []store.UserPermissionOverrideInput{
		{PermissionCode: "feature.llmapi", Effect: "allow"},
		{PermissionCode: "provider.llm.demeter_sante", Effect: "allow"},
	}, nil)

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	mailerStub := &fakeMeetingMailer{
		started: started,
		release: release,
	}
	appCtx.Mailer = mailerStub
	appCtx.RegisterMeetingRoutes(app.Group("/api/v1"))

	requestBody := map[string]any{
		"meetingTitle":            "Réunion qualité",
		"participants":            []string{"Alice", "Bob"},
		"transcriptionSourceMode": "cloud_backend",
		"transcriptionProvider":   "demeter_sante",
		"rawTranscriptText":       "Bonjour tout le monde.",
		"editedTranscriptText":    "Bonjour tout le monde.",
		"selectedFormats":         []string{"CRI"},
		"reports":                 []meetingReportEnvelope{minimalMeetingReportEnvelope("CRI")},
	}
	rawBody, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("failed to marshal request body: %v", err)
	}

	resultCh := make(chan testResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := performMeetingRequest(app, http.MethodPost, "/api/v1/meetings/finalize", string(rawBody), map[string]string{
			fiber.HeaderAuthorization: "Bearer " + token,
		})
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- resp
	}()

	select {
	case <-started:
	case err := <-errCh:
		t.Fatalf("first finalize request failed before blocking: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the first finalize request to reach the mailer")
	}

	secondResp, err := performMeetingRequest(app, http.MethodPost, "/api/v1/meetings/finalize", `{"meetingTitle":`, map[string]string{
		fiber.HeaderAuthorization: "Bearer " + token,
		fiber.HeaderContentType:   fiber.MIMEApplicationJSON,
	})
	if err != nil {
		t.Fatalf("second finalize request failed: %v", err)
	}
	if secondResp.StatusCode != fiber.StatusConflict {
		t.Fatalf("expected 409 for duplicate finalize request, got %d with body %q", secondResp.StatusCode, string(secondResp.Body))
	}
	if got := mailerStub.sentCount(); got != 1 {
		t.Fatalf("expected only one mailer send before releasing the first request, got %d", got)
	}

	close(release)

	select {
	case firstResp := <-resultCh:
		if firstResp.StatusCode != http.StatusOK {
			t.Fatalf("expected first finalize request to succeed, got %d with body %q", firstResp.StatusCode, string(firstResp.Body))
		}
		var payload meetingFinalizeResponse
		if err := json.Unmarshal(firstResp.Body, &payload); err != nil {
			t.Fatalf("failed to decode first finalize response: %v", err)
		}
		if len(payload.SentToEmails) != 1 {
			t.Fatalf("expected one recipient in the successful response, got %v", payload.SentToEmails)
		}
	case err := <-errCh:
		t.Fatalf("first finalize request failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the first finalize request to complete")
	}
}

func TestFinalizeMeetingAllowsDifferentUsersToFinalizeConcurrently(t *testing.T) {
	app, token, appCtx := setupDemeterRoutesApp(t, []store.UserPermissionOverrideInput{
		{PermissionCode: "feature.llmapi", Effect: "allow"},
		{PermissionCode: "provider.llm.demeter_sante", Effect: "allow"},
	}, nil)

	claims, err := auth.ParseAccessToken(appCtx.Config.JWTSecret, token)
	if err != nil {
		t.Fatalf("failed to parse access token: %v", err)
	}

	secondUser, err := appCtx.Store.CreateUser(context.Background(), claims.OrgID, "other@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("failed to create second user: %v", err)
	}
	if err := appCtx.Store.SetUserPermissionOverrides(context.Background(), secondUser.ID, []store.UserPermissionOverrideInput{
		{PermissionCode: "feature.llmapi", Effect: "allow"},
		{PermissionCode: "provider.llm.demeter_sante", Effect: "allow"},
	}); err != nil {
		t.Fatalf("failed to set second user permissions: %v", err)
	}
	secondToken := issueAccessToken(t, appCtx.Config.JWTSecret, auth.Claims{
		UserID: secondUser.ID,
		OrgID:  secondUser.OrganizationID,
		Email:  secondUser.Email,
		Permissions: []string{
			"feature.llmapi",
			"provider.llm.demeter_sante",
		},
	})

	started := make(chan struct{}, 2)
	release := make(chan struct{})
	mailerStub := &fakeMeetingMailer{
		started: started,
		release: release,
	}
	appCtx.Mailer = mailerStub
	appCtx.RegisterMeetingRoutes(app.Group("/api/v1"))

	requestBody := map[string]any{
		"meetingTitle":            "Réunion qualité",
		"participants":            []string{"Alice", "Bob"},
		"transcriptionSourceMode": "cloud_backend",
		"transcriptionProvider":   "demeter_sante",
		"rawTranscriptText":       "Bonjour tout le monde.",
		"editedTranscriptText":    "Bonjour tout le monde.",
		"selectedFormats":         []string{"CRI"},
		"reports":                 []meetingReportEnvelope{minimalMeetingReportEnvelope("CRI")},
	}
	rawBody, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("failed to marshal request body: %v", err)
	}

	type requestResult struct {
		resp testResponse
		err  error
	}
	resultCh := make(chan requestResult, 2)
	runRequest := func(token string) {
		resp, err := performMeetingRequest(app, http.MethodPost, "/api/v1/meetings/finalize", string(rawBody), map[string]string{
			fiber.HeaderAuthorization: "Bearer " + token,
		})
		resultCh <- requestResult{resp: resp, err: err}
	}

	go runRequest(token)
	go runRequest(secondToken)

	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case res := <-resultCh:
			if res.err != nil {
				t.Fatalf("unexpected request failure before both sends started: %v", res.err)
			}
			t.Fatalf("expected both finalize requests to reach the mailer, got an early response with status %d", res.resp.StatusCode)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for both finalize requests to reach the mailer")
		}
	}

	close(release)

	for i := 0; i < 2; i++ {
		select {
		case res := <-resultCh:
			if res.err != nil {
				t.Fatalf("finalize request failed: %v", res.err)
			}
			if res.resp.StatusCode != http.StatusOK {
				t.Fatalf("expected 200 for concurrent finalize request, got %d with body %q", res.resp.StatusCode, string(res.resp.Body))
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for concurrent finalize requests to complete")
		}
	}

	if got := mailerStub.sentCount(); got != 2 {
		t.Fatalf("expected one mailer send per user, got %d", got)
	}
}

var (
	meetingLogTracePattern = regexp.MustCompile(`trace_id=([^\s]+)`)
	meetingLogStepPattern  = regexp.MustCompile(`step=([^\s]+)`)
)

func captureMeetingLogs(t *testing.T, fn func()) string {
	t.Helper()

	var buffer bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	previousPrefix := log.Prefix()

	log.SetOutput(&buffer)
	log.SetFlags(0)
	log.SetPrefix("")
	defer log.SetOutput(previousWriter)
	defer log.SetFlags(previousFlags)
	defer log.SetPrefix(previousPrefix)

	fn()
	return buffer.String()
}

func assertMeetingLogs(t *testing.T, logs, route string, wantSteps []string) {
	t.Helper()

	lines := meetingLogLines(logs)
	if len(lines) == 0 {
		t.Fatalf("expected meeting logs for %s, got %q", route, logs)
	}

	actualSteps := make([]string, 0, len(lines))
	traceID := ""
	for _, line := range lines {
		if !strings.Contains(line, "route="+route) {
			t.Fatalf("expected route %s in log line %q", route, line)
		}
		traceMatch := meetingLogTracePattern.FindStringSubmatch(line)
		if len(traceMatch) != 2 {
			t.Fatalf("expected trace_id in log line %q", line)
		}
		if traceID == "" {
			traceID = traceMatch[1]
		} else if traceID != traceMatch[1] {
			t.Fatalf("expected one trace_id per request, got %q and %q", traceID, traceMatch[1])
		}

		stepMatch := meetingLogStepPattern.FindStringSubmatch(line)
		if len(stepMatch) != 2 {
			t.Fatalf("expected step in log line %q", line)
		}
		actualSteps = append(actualSteps, stepMatch[1])
	}

	if traceID == "" {
		t.Fatal("expected a non-empty trace_id")
	}
	if !containsStringSubsequence(actualSteps, wantSteps) {
		t.Fatalf("expected steps %v in order, got %v", wantSteps, actualSteps)
	}
}

func assertMeetingLogExcludes(t *testing.T, logs string, forbidden []string) {
	t.Helper()

	for _, needle := range forbidden {
		if needle == "" {
			continue
		}
		if strings.Contains(logs, needle) {
			t.Fatalf("did not expect %q in meeting logs: %q", needle, logs)
		}
	}
}

func meetingLogLines(logs string) []string {
	rawLines := strings.Split(strings.TrimSpace(logs), "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "[meetings]") {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func containsStringSubsequence(actual, want []string) bool {
	if len(want) == 0 {
		return true
	}
	index := 0
	for _, value := range actual {
		if value == want[index] {
			index++
			if index == len(want) {
				return true
			}
		}
	}
	return false
}

func minimalMeetingReportEnvelope(format string) meetingReportEnvelope {
	return meetingReportEnvelope{
		Format: format,
		Raw: fmt.Sprintf(
			`{"format":%q,"title":"Compte rendu %s","sections":[{"heading":"Contexte","paragraphs":["Point %s"]}]}`,
			format,
			format,
			format,
		),
	}
}

func containsMeetingFilename(filename, prefix string) bool {
	return len(filename) >= len(prefix) && filename[:len(prefix)] == prefix
}

func performMeetingRequest(app *fiber.App, method, path, body string, headers map[string]string) (testResponse, error) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if strings.TrimSpace(body) != "" {
		req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := app.Test(req, 5_000)
	if err != nil {
		return testResponse{}, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return testResponse{}, err
	}
	return testResponse{StatusCode: resp.StatusCode, Body: raw}, nil
}
