package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/config"
	"demeter-backend/internal/mistral"
	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
)

func TestHealthRoutes_ReadyRequiresMistralConfigured(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "health.sqlite")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()

	appCtx := &App{Config: config.Config{}, Store: st, MistralClient: &mistral.Client{}}
	app := fiber.New()
	api := app.Group("/api/v1")
	appCtx.RegisterHealthRoutes(api)

	health, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/healthz", nil), 5_000)
	if err != nil {
		t.Fatalf("health request failed: %v", err)
	}
	if health.StatusCode != fiber.StatusOK {
		t.Fatalf("expected health 200, got %d", health.StatusCode)
	}

	ready, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/readyz", nil), 5_000)
	if err != nil {
		t.Fatalf("ready request failed: %v", err)
	}
	if ready.StatusCode != fiber.StatusServiceUnavailable {
		t.Fatalf("expected ready 503 when mistral not configured, got %d", ready.StatusCode)
	}
}

func TestHealthRoutes_ReadyHandlesDatabaseFailureAndSuccess(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "health-ready.sqlite")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}

	mistralClient := mistral.NewClient("https://mistral.example.test", "key", time.Second, time.Second)
	appCtx := &App{Config: config.Config{}, Store: st, MistralClient: mistralClient}
	app := fiber.New()
	api := app.Group("/api/v1")
	appCtx.RegisterHealthRoutes(api)

	ready, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/readyz", nil), 5_000)
	if err != nil {
		t.Fatalf("ready request failed: %v", err)
	}
	if ready.StatusCode != fiber.StatusOK {
		t.Fatalf("expected ready 200 with configured dependencies, got %d", ready.StatusCode)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("failed to close store: %v", err)
	}

	readyAfterClose, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/readyz", nil), 5_000)
	if err != nil {
		t.Fatalf("ready request after close failed: %v", err)
	}
	if readyAfterClose.StatusCode != fiber.StatusServiceUnavailable {
		t.Fatalf("expected ready 503 when database is closed, got %d", readyAfterClose.StatusCode)
	}
}

func TestDemeterModels_RequiresPermissionAndMistral(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "demeter.sqlite")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()

	org, err := st.CreateOrganization(ctx, "Org", "org", "active")
	if err != nil {
		t.Fatalf("failed to create org: %v", err)
	}
	user, err := st.CreateUser(ctx, org.ID, "u@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	if err := st.SetUserPermissionOverrides(ctx, user.ID, []store.UserPermissionOverrideInput{{PermissionCode: "provider.cloud.demeter_sante", Effect: "allow"}}); err != nil {
		t.Fatalf("failed to set permission override: %v", err)
	}

	appCtx := &App{
		Config:        config.Config{JWTSecret: "test-secret"},
		Store:         st,
		MistralClient: &mistral.Client{},
	}
	app := fiber.New()
	api := app.Group("/api/v1")
	appCtx.RegisterDemeterRoutes(api)

	// Create a token for the user
	token, _, err := auth.NewAccessToken("test-secret", time.Hour, auth.Claims{
		UserID:      user.ID,
		OrgID:       user.OrganizationID,
		Email:       user.Email,
		Permissions: []string{"provider.cloud.demeter_sante"},
	})
	if err != nil {
		t.Fatalf("failed to create token: %v", err)
	}

	// When mistral is not configured, should return 503
	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers/demeter-sante/models", nil)
	req.Header.Set(fiber.HeaderAuthorization, "Bearer "+token)
	resp, err := app.Test(req, 5_000)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != fiber.StatusServiceUnavailable {
		t.Fatalf("expected 503 when mistral not configured, got %d", resp.StatusCode)
	}

	// Configure a dummy mistral server to respond successfully
	dummy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer dummy.Close()
	appCtx.MistralClient = mistral.NewClient(dummy.URL, "key", time.Second, time.Second)

	// Re-register routes with configured client
	app = fiber.New()
	api = app.Group("/api/v1")
	appCtx.RegisterDemeterRoutes(api)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/providers/demeter-sante/models", nil)
	req.Header.Set(fiber.HeaderAuthorization, "Bearer "+token)
	resp, err = app.Test(req, 5_000)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 when mistral responds, got %d", resp.StatusCode)
	}
}

func TestDemeterChatCompletions_ProxiesJSONBody(t *testing.T) {
	var buffer bytes.Buffer
	originalWriter := log.Writer()
	originalFlags := log.Flags()
	log.SetOutput(&buffer)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(originalWriter)
		log.SetFlags(originalFlags)
	})

	app, token, _ := setupDemeterRoutesApp(t, []store.UserPermissionOverrideInput{
		{PermissionCode: "feature.llmapi", Effect: "allow"},
		{PermissionCode: "provider.llm.demeter_sante", Effect: "allow"},
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected upstream method: %s", r.Method)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode upstream payload: %v", err)
		}
		if payload["model"] != "test-model" {
			t.Fatalf("unexpected upstream payload: %+v", payload)
		}
		w.Header().Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"chat-1"}`))
	}))

	resp := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodPost,
		"/api/v1/providers/demeter-sante/chat/completions",
		map[string]any{"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "hello"}}},
		nil,
		map[string]string{fiber.HeaderAuthorization: "Bearer " + token},
	)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 for proxied chat completion, got %d", resp.StatusCode)
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode chat response: %v", err)
	}
	if payload["id"] != "chat-1" {
		t.Fatalf("unexpected chat response: %+v", payload)
	}

	logged := buffer.String()
	for _, needle := range []string{
		"[demeter]",
		"route=/api/v1/providers/demeter-sante/chat/completions",
		"step=request_received",
		"step=upstream_start",
		"step=response_ready",
		"trace_id=",
		"user=",
		"org=",
		"title=\"chat_completions\"",
		"request_bytes=",
		"response_bytes=",
	} {
		if !strings.Contains(logged, needle) {
			t.Fatalf("expected %q in chat completion logs, got %q", needle, logged)
		}
	}
}

func TestDemeterModelsAndChatCompletions_ReturnBadGatewayOnUpstreamError(t *testing.T) {
	app, token, appCtx := setupDemeterRoutesApp(t, []store.UserPermissionOverrideInput{
		{PermissionCode: "provider.cloud.demeter_sante", Effect: "allow"},
		{PermissionCode: "feature.llmapi", Effect: "allow"},
		{PermissionCode: "provider.llm.demeter_sante", Effect: "allow"},
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	appCtx.MistralClient.BaseURL = "http://127.0.0.1:1"

	modelsResp := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodGet,
		"/api/v1/providers/demeter-sante/models",
		nil,
		nil,
		map[string]string{fiber.HeaderAuthorization: "Bearer " + token},
	)
	if modelsResp.StatusCode != fiber.StatusBadGateway {
		t.Fatalf("expected 502 for models upstream transport error, got %d", modelsResp.StatusCode)
	}

	chatResp := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodPost,
		"/api/v1/providers/demeter-sante/chat/completions",
		map[string]any{"model": "test-model"},
		nil,
		map[string]string{fiber.HeaderAuthorization: "Bearer " + token},
	)
	if chatResp.StatusCode != fiber.StatusBadGateway {
		t.Fatalf("expected 502 for chat upstream transport error, got %d", chatResp.StatusCode)
	}
}

func TestDemeterChatCompletions_RequiresConfiguredClient(t *testing.T) {
	app, token, _ := setupDemeterRoutesApp(t, []store.UserPermissionOverrideInput{
		{PermissionCode: "feature.llmapi", Effect: "allow"},
		{PermissionCode: "provider.llm.demeter_sante", Effect: "allow"},
	}, nil)

	resp := performJSONRequestWithHeaders(
		t,
		app,
		http.MethodPost,
		"/api/v1/providers/demeter-sante/chat/completions",
		map[string]any{"model": "test-model"},
		nil,
		map[string]string{fiber.HeaderAuthorization: "Bearer " + token},
	)
	if resp.StatusCode != fiber.StatusServiceUnavailable {
		t.Fatalf("expected 503 for unconfigured chat client, got %d", resp.StatusCode)
	}
}

func TestDemeterAudioTranscriptions_ReturnsBadGatewayOnUpstreamError(t *testing.T) {
	app, token, appCtx := setupDemeterRoutesApp(t, []store.UserPermissionOverrideInput{
		{PermissionCode: "feature.cloudupload", Effect: "allow"},
		{PermissionCode: "provider.cloud.demeter_sante", Effect: "allow"},
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	appCtx.MistralClient.BaseURL = "http://127.0.0.1:1"

	boundary := "demeter-boundary"
	body := "--" + boundary + "\r\n" +
		"Content-Disposition: form-data; name=\"file\"; filename=\"sample.wav\"\r\n" +
		"Content-Type: audio/wav\r\n\r\n" +
		"wave-data\r\n" +
		"--" + boundary + "--\r\n"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/providers/demeter-sante/audio/transcriptions", bytes.NewBufferString(body))
	req.Header.Set(fiber.HeaderAuthorization, "Bearer "+token)
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEMultipartForm+"; boundary="+boundary)

	resp, err := app.Test(req, 5_000)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer closeHTTPResponse(t, resp)
	if resp.StatusCode != fiber.StatusBadGateway {
		t.Fatalf("expected 502 for transcription upstream transport error, got %d", resp.StatusCode)
	}
}

func TestDemeterAudioTranscriptions_RetriesOnceOnUpstream500(t *testing.T) {
	var upstreamAttempts int32
	var buffer bytes.Buffer
	originalWriter := log.Writer()
	originalFlags := log.Flags()
	log.SetOutput(&buffer)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(originalWriter)
		log.SetFlags(originalFlags)
	})

	app, token, _ := setupDemeterRoutesApp(t, []store.UserPermissionOverrideInput{
		{PermissionCode: "feature.cloudupload", Effect: "allow"},
		{PermissionCode: "provider.cloud.demeter_sante", Effect: "allow"},
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected upstream method: %s", r.Method)
		}
		if _, err := io.ReadAll(r.Body); err != nil {
			t.Fatalf("failed to read upstream body: %v", err)
		}

		attempt := atomic.AddInt32(&upstreamAttempts, 1)
		w.Header().Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
		if attempt == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"detail":"Internal server error"}`))
			return
		}
		if attempt == 2 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"text":"transcribed"}`))
			return
		}
		t.Fatalf("unexpected upstream attempt %d", attempt)
	}))

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	filePart, err := writer.CreateFormFile("file", "sample.wav")
	if err != nil {
		t.Fatalf("failed to create multipart file part: %v", err)
	}
	if _, err := filePart.Write([]byte("wave-data")); err != nil {
		t.Fatalf("failed to write multipart file part: %v", err)
	}
	if err := writer.WriteField("model", defaultDemeterAudioTranscriptionModelID); err != nil {
		t.Fatalf("failed to write multipart model field: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/providers/demeter-sante/audio/transcriptions", bytes.NewReader(body.Bytes()))
	req.Header.Set(fiber.HeaderAuthorization, "Bearer "+token)
	req.Header.Set(fiber.HeaderContentType, writer.FormDataContentType())

	resp, err := app.Test(req, 5_000)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer closeHTTPResponse(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after retry, got %d", resp.StatusCode)
	}

	if got := atomic.LoadInt32(&upstreamAttempts); got != 2 {
		t.Fatalf("expected 2 upstream attempts, got %d", got)
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode transcription response: %v", err)
	}
	if payload["text"] != "transcribed" {
		t.Fatalf("unexpected transcription response: %+v", payload)
	}

	logged := buffer.String()
	retryLine := ""
	for _, line := range strings.Split(logged, "\n") {
		if strings.Contains(line, "step=upstream_retry") {
			retryLine = line
			break
		}
	}
	if retryLine == "" {
		t.Fatalf("expected upstream retry log, got %q", logged)
	}
	for _, needle := range []string{
		"[demeter]",
		"route=/api/v1/providers/demeter-sante/audio/transcriptions",
		"trace_id=",
		"user=",
		"org=",
		"title=\"audio_transcription\"",
		"attempt=1",
		"next_attempt=2",
		"max_attempts=2",
		"upstream_status=500",
		"request_bytes=",
		"response_bytes=",
	} {
		if !strings.Contains(retryLine, needle) {
			t.Fatalf("expected %q in retry log line, got %q", needle, retryLine)
		}
	}
}

func TestDemeterHelperFunctions_LogOnlyErrorsAndExposeActorIDs(t *testing.T) {
	var buffer bytes.Buffer
	originalWriter := log.Writer()
	originalFlags := log.Flags()
	log.SetOutput(&buffer)
	log.SetFlags(0)
	defer log.SetOutput(originalWriter)
	defer log.SetFlags(originalFlags)

	app := fiber.New()
	app.Get("/helpers", func(c *fiber.Ctx) error {
		userID, orgID := demeterActorIDs(c)
		if userID != "-" || orgID != "-" {
			t.Fatalf("expected missing actor ids to default to dashes, got %q %q", userID, orgID)
		}

		logDemeterUpstreamStatus(c, "/helpers", fiber.StatusOK)
		if buffer.Len() != 0 {
			t.Fatalf("expected no log for successful upstream status, got %q", buffer.String())
		}

		c.Locals(claimsContextKey, &auth.Claims{UserID: "user-1", OrgID: "org-1"})
		userID, orgID = demeterActorIDs(c)
		if userID != "user-1" || orgID != "org-1" {
			t.Fatalf("unexpected actor ids: %q %q", userID, orgID)
		}

		logDemeterUpstreamStatus(c, "/helpers", fiber.StatusBadGateway)
		if !strings.Contains(buffer.String(), "step=upstream_error") {
			t.Fatalf("expected upstream error log, got %q", buffer.String())
		}
		return c.SendStatus(fiber.StatusNoContent)
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/helpers", nil), 5_000)
	if err != nil {
		t.Fatalf("helper route request failed: %v", err)
	}
	defer closeHTTPResponse(t, resp)
	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204 from helper route, got %d", resp.StatusCode)
	}

	start := nextDemeterAudioSequenceID()
	next := nextDemeterAudioSequenceID()
	if next <= start {
		t.Fatalf("expected demeter audio sequence ids to increase, got %d then %d", start, next)
	}
}

func TestDemeterAudioTranscriptions_RejectsInvalidContentType(t *testing.T) {
	app, token, _ := setupDemeterRoutesApp(t, []store.UserPermissionOverrideInput{
		{PermissionCode: "feature.cloudupload", Effect: "allow"},
		{PermissionCode: "provider.cloud.demeter_sante", Effect: "allow"},
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/providers/demeter-sante/audio/transcriptions", bytes.NewBufferString(`{"bad":true}`))
	req.Header.Set(fiber.HeaderAuthorization, "Bearer "+token)
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)

	resp, err := app.Test(req, 5_000)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer closeHTTPResponse(t, resp)
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("expected 400 for invalid content type, got %d", resp.StatusCode)
	}
}

func TestDemeterAudioTranscriptions_ProxiesMultipartBody(t *testing.T) {
	var receivedContentType string
	var receivedBody []byte
	app, token, _ := setupDemeterRoutesApp(t, []store.UserPermissionOverrideInput{
		{PermissionCode: "feature.cloudupload", Effect: "allow"},
		{PermissionCode: "provider.cloud.demeter_sante", Effect: "allow"},
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected upstream method: %s", r.Method)
		}
		receivedContentType = r.Header.Get(fiber.HeaderContentType)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read upstream body: %v", err)
		}
		receivedBody = body
		w.Header().Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"text":"transcribed"}`))
	}))

	boundary := "demeter-boundary"
	body := "--" + boundary + "\r\n" +
		"Content-Disposition: form-data; name=\"file\"; filename=\"sample.wav\"\r\n" +
		"Content-Type: audio/wav\r\n\r\n" +
		"wave-data\r\n" +
		"--" + boundary + "--\r\n"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/providers/demeter-sante/audio/transcriptions", bytes.NewBufferString(body))
	req.Header.Set(fiber.HeaderAuthorization, "Bearer "+token)
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEMultipartForm+"; boundary="+boundary)

	resp, err := app.Test(req, 5_000)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer closeHTTPResponse(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for proxied transcription, got %d", resp.StatusCode)
	}
	if receivedContentType == "" || !bytes.Contains(receivedBody, []byte("wave-data")) {
		t.Fatalf("unexpected upstream multipart request: contentType=%q body=%q", receivedContentType, string(receivedBody))
	}
	_, params, err := mime.ParseMediaType(receivedContentType)
	if err != nil {
		t.Fatalf("failed to parse upstream multipart content type: %v", err)
	}
	reader := multipart.NewReader(bytes.NewReader(receivedBody), params["boundary"])
	parts := map[string][]byte{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("failed to read upstream multipart: %v", err)
		}
		data, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("failed to read upstream multipart part: %v", err)
		}
		parts[part.FormName()] = data
	}
	if got := string(parts["model"]); got != defaultDemeterAudioTranscriptionModelID {
		t.Fatalf("expected injected default model %q, got %q", defaultDemeterAudioTranscriptionModelID, got)
	}
	if got := string(parts["file"]); !strings.Contains(got, "wave-data") {
		t.Fatalf("expected file part to be preserved, got %q", got)
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode transcription response: %v", err)
	}
	if payload["text"] != "transcribed" {
		t.Fatalf("unexpected transcription response: %+v", payload)
	}
}

func setupDemeterRoutesApp(t *testing.T, overrides []store.UserPermissionOverrideInput, handler http.Handler) (*fiber.App, string, *App) {
	t.Helper()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "demeter-routes.sqlite")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	org, err := st.CreateOrganization(ctx, "Org", "org", "active")
	if err != nil {
		t.Fatalf("failed to create org: %v", err)
	}
	user, err := st.CreateUser(ctx, org.ID, "u@example.com", "hash", "active")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	if err := st.SetUserPermissionOverrides(ctx, user.ID, overrides); err != nil {
		t.Fatalf("failed to set permission overrides: %v", err)
	}

	client := &mistral.Client{}
	if handler != nil {
		upstream := httptest.NewServer(handler)
		t.Cleanup(upstream.Close)
		client = mistral.NewClient(upstream.URL, "key", time.Second, time.Second)
	}

	appCtx := &App{
		Config:        config.Config{JWTSecret: "test-secret"},
		Store:         st,
		MistralClient: client,
	}
	app := fiber.New()
	api := app.Group("/api/v1")
	appCtx.RegisterDemeterRoutes(api)

	token, _, err := auth.NewAccessToken("test-secret", time.Hour, auth.Claims{
		UserID: user.ID,
		OrgID:  user.OrganizationID,
		Email:  user.Email,
		Permissions: func() []string {
			codes := make([]string, 0, len(overrides))
			for _, override := range overrides {
				codes = append(codes, override.PermissionCode)
			}
			return codes
		}(),
	})
	if err != nil {
		t.Fatalf("failed to create token: %v", err)
	}

	return app, token, appCtx
}
