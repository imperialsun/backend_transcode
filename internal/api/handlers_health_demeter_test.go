package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
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
	app := fiber.New(fiber.Config{
		BodyLimit: 16 * 1024 * 1024,
	})
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
	resp, err := app.Test(req, 15_000)
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

func TestDemeterAudioTranscriptions_BackendRouteChunksServerSideAndLogsDurationAndMode(t *testing.T) {
	var buffer bytes.Buffer
	var upstreamAttempts int32
	originalWriter := log.Writer()
	originalFlags := log.Flags()
	log.SetOutput(&buffer)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(originalWriter)
		log.SetFlags(originalFlags)
	})

	app, token, appCtx := setupDemeterRoutesApp(t, []store.UserPermissionOverrideInput{
		{PermissionCode: "feature.settings", Effect: "allow"},
		{PermissionCode: "feature.cloudupload", Effect: "allow"},
		{PermissionCode: "provider.cloud.demeter_sante", Effect: "allow"},
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read upstream body: %v", err)
		}
		receivedContentType := r.Header.Get(fiber.HeaderContentType)
		if receivedContentType == "" {
			t.Fatalf("expected multipart content type on upstream request")
		}
		_, params, err := mime.ParseMediaType(receivedContentType)
		if err != nil {
			t.Fatalf("failed to parse upstream content type: %v", err)
		}
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
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
		if got := string(parts["diarize"]); got != "false" {
			t.Fatalf("expected diarize=false, got %q", got)
		}
		if got := string(parts["model"]); got != defaultDemeterAudioTranscriptionModelID {
			t.Fatalf("expected default model %q, got %q", defaultDemeterAudioTranscriptionModelID, got)
		}
		if len(parts["file"]) < 12 || !bytes.HasPrefix(parts["file"], []byte("RIFF")) || !bytes.Equal(parts["file"][8:12], []byte("WAVE")) {
			t.Fatalf("expected upstream chunk to be wav, got %x", parts["file"][:min(12, len(parts["file"]))])
		}
		attempt := int(atomic.AddInt32(&upstreamAttempts, 1))
		w.Header().Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
		segmentText := fmt.Sprintf("chunk-%d", attempt)
		response := fmt.Sprintf(`{"text":"%s","segments":[{"text":"%s","start":0,"end":1,"speaker":"SPEAKER_%02d"}]}`, segmentText, segmentText, attempt)
		w.Header().Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(response))
	}))

	claims, err := auth.ParseAccessToken(appCtx.Config.JWTSecret, token)
	if err != nil {
		t.Fatalf("failed to parse access token: %v", err)
	}
	if _, err := appCtx.Store.SaveUserSettings(
		context.Background(),
		claims.UserID,
		claims.OrgID,
		json.RawMessage(`{"cloudDemeterChunkDurationSec":600,"cloudDemeterOverlapSec":1}`),
		1,
	); err != nil {
		t.Fatalf("failed to save user settings: %v", err)
	}

	wavBytes := buildSilentWAVBytes(t, 660, 4_000)
	totalSlices := (len(wavBytes) + demeterBackendTransportSliceSizeBytes - 1) / demeterBackendTransportSliceSizeBytes
	if totalSlices != 2 {
		t.Fatalf("expected test audio to produce 2 transport slices, got %d", totalSlices)
	}
	uploadID := fmt.Sprintf("backend-direct-%d", time.Now().UnixNano())

	var startPayload struct {
		OperationID string  `json:"operationId"`
		Status      string  `json:"status"`
		StatusCode  int     `json:"statusCode"`
		Stage       string  `json:"stage"`
		ChunkIndex  int     `json:"chunkIndex"`
		ChunkCount  int     `json:"chunkCount"`
		Progress    float64 `json:"progress"`
	}
	for sliceIndex := 0; sliceIndex < totalSlices; sliceIndex++ {
		start := sliceIndex * demeterBackendTransportSliceSizeBytes
		end := start + demeterBackendTransportSliceSizeBytes
		if end > len(wavBytes) {
			end = len(wavBytes)
		}
		resp := sendDemeterAudioSliceRequest(
			t,
			app,
			token,
			uploadID,
			sliceIndex,
			totalSlices,
			sliceIndex == totalSlices-1,
			"sample.wav",
			wavBytes[start:end],
			660,
			defaultDemeterAudioTranscriptionModelID,
			false,
		)
		if sliceIndex < totalSlices-1 {
			if resp.StatusCode != fiber.StatusNoContent {
				t.Fatalf("expected 204 for transport slice %d, got %d", sliceIndex, resp.StatusCode)
			}
			closeHTTPResponse(t, resp)
			continue
		}
		if resp.StatusCode != fiber.StatusAccepted {
			t.Fatalf("expected 202 for backend direct transcription start, got %d", resp.StatusCode)
		}
		if err := json.NewDecoder(resp.Body).Decode(&startPayload); err != nil {
			t.Fatalf("failed to decode backend direct start response: %v", err)
		}
		closeHTTPResponse(t, resp)
	}
	if startPayload.OperationID != uploadID {
		t.Fatalf("expected backend operation id %q, got %+v", uploadID, startPayload)
	}
	if startPayload.Stage != demeterAudioTransportFinalizationStage {
		t.Fatalf("expected reconstructing stage in start payload, got %+v", startPayload)
	}
	if startPayload.ChunkCount != 0 {
		t.Fatalf("expected zero chunks at transport start, got %+v", startPayload)
	}
	if startPayload.ChunkIndex != 0 {
		t.Fatalf("expected zero chunk index at transport start, got %+v", startPayload)
	}
	if startPayload.Status != store.DemeterAudioTranscriptionOperationStatusRunning {
		t.Fatalf("expected running status at transport start, got %+v", startPayload)
	}
	if startPayload.StatusCode != fiber.StatusAccepted {
		t.Fatalf("expected accepted status code in start payload, got %+v", startPayload)
	}

	var finalPayload struct {
		OperationID string  `json:"operationId"`
		Status      string  `json:"status"`
		StatusCode  int     `json:"statusCode"`
		Stage       string  `json:"stage"`
		ChunkIndex  int     `json:"chunkIndex"`
		ChunkCount  int     `json:"chunkCount"`
		Progress    float64 `json:"progress"`
		PartialText string  `json:"partialText"`
		Response    struct {
			Text     string  `json:"text"`
			Duration float64 `json:"duration"`
			Chunks   []struct {
				ChunkID      string  `json:"chunkId"`
				Index        int     `json:"index"`
				StartSec     float64 `json:"startSec"`
				EndSec       float64 `json:"endSec"`
				DurationSec  float64 `json:"durationSec"`
				SegmentCount int     `json:"segmentCount"`
				Text         string  `json:"text"`
				Segments     []struct {
					Index   int     `json:"index"`
					Start   float64 `json:"start"`
					End     float64 `json:"end"`
					Text    string  `json:"text"`
					ChunkID string  `json:"chunkId"`
				} `json:"segments"`
			} `json:"chunks"`
		} `json:"response"`
	}

	deadline := time.Now().Add(20 * time.Second)
	for {
		statusReq := httptest.NewRequest(http.MethodGet, "/api/v1/providers/demeter-sante/audio/transcriptions/operations/"+startPayload.OperationID, nil)
		statusReq.Header.Set(fiber.HeaderAuthorization, "Bearer "+token)
		statusResp, statusErr := app.Test(statusReq, 5_000)
		if statusErr != nil {
			t.Fatalf("status request failed: %v", statusErr)
		}
		if err := json.NewDecoder(statusResp.Body).Decode(&finalPayload); err != nil {
			_ = statusResp.Body.Close()
			t.Fatalf("failed to decode backend operation status: %v", err)
		}
		_ = statusResp.Body.Close()
		if finalPayload.Status == store.DemeterAudioTranscriptionOperationStatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for backend operation completion, last payload: %+v", finalPayload)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if finalPayload.ChunkIndex != 2 {
		t.Fatalf("expected 2 completed chunks, got %+v", finalPayload)
	}
	if finalPayload.ChunkCount != 2 {
		t.Fatalf("expected 2 total chunks, got %+v", finalPayload)
	}
	if finalPayload.Progress != 1 {
		t.Fatalf("expected final progress 1, got %+v", finalPayload)
	}
	if finalPayload.Response.Text != "chunk-1\nchunk-2" {
		t.Fatalf("unexpected aggregated text: %+v", finalPayload)
	}
	if len(finalPayload.Response.Chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %+v", finalPayload)
	}
	if len(finalPayload.Response.Chunks[0].Segments) != 1 || len(finalPayload.Response.Chunks[1].Segments) != 1 {
		t.Fatalf("expected one nested segment per chunk, got %+v", finalPayload.Response.Chunks)
	}
	if finalPayload.Response.Chunks[0].ChunkID == finalPayload.Response.Chunks[1].ChunkID {
		t.Fatalf("expected distinct chunk ids across backend chunks, got %+v", finalPayload.Response.Chunks)
	}
	if finalPayload.Response.Chunks[0].Segments[0].ChunkID != finalPayload.Response.Chunks[0].ChunkID {
		t.Fatalf("expected nested segment chunk id to match parent chunk, got %+v", finalPayload.Response.Chunks[0])
	}
	if finalPayload.Response.Chunks[1].Segments[0].ChunkID != finalPayload.Response.Chunks[1].ChunkID {
		t.Fatalf("expected nested segment chunk id to match parent chunk, got %+v", finalPayload.Response.Chunks[1])
	}

	logged := buffer.String()
	for _, needle := range []string{
		"route=/api/v1/providers/demeter-sante/audio/transcriptions/backend",
		"route_mode=\"backend_direct\"",
		"audio_duration_sec=660",
		"chunk_count=2",
		"chunk_start_sec=599",
		"chunk_duration_sec=61",
		"normalized_format=\"audio/wav\"",
		"file_name=\"sample.wav\"",
		"title=\"audio_transcription\"",
	} {
		if !strings.Contains(logged, needle) {
			t.Fatalf("expected %q in backend direct logs, got %q", needle, logged)
		}
	}
}

func TestDemeterAudioTranscriptions_RelayRouteUsesSourceAudioWithoutBackendTranscode(t *testing.T) {
	var buffer bytes.Buffer
	var upstreamAttempts int32
	originalWriter := log.Writer()
	originalFlags := log.Flags()
	log.SetOutput(&buffer)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(originalWriter)
		log.SetFlags(originalFlags)
	})

	expectedAudio := buildSilentWAVBytes(t, 2, 4_000)
	app, token, _ := setupDemeterRoutesApp(t, []store.UserPermissionOverrideInput{
		{PermissionCode: "feature.cloudupload", Effect: "allow"},
		{PermissionCode: "provider.cloud.demeter_sante", Effect: "allow"},
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read upstream body: %v", err)
		}
		receivedContentType := r.Header.Get(fiber.HeaderContentType)
		if receivedContentType == "" {
			t.Fatalf("expected multipart content type on upstream request")
		}
		_, params, err := mime.ParseMediaType(receivedContentType)
		if err != nil {
			t.Fatalf("failed to parse upstream content type: %v", err)
		}
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
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
		if got := string(parts["diarize"]); got != "false" {
			t.Fatalf("expected diarize=false, got %q", got)
		}
		if got := string(parts["model"]); got != defaultDemeterAudioTranscriptionModelID {
			t.Fatalf("expected default model %q, got %q", defaultDemeterAudioTranscriptionModelID, got)
		}
		if !bytes.Equal(parts["file"], expectedAudio) {
			t.Fatalf("expected relay route to forward the original audio bytes unchanged")
		}
		attempt := int(atomic.AddInt32(&upstreamAttempts, 1))
		w.Header().Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
		segmentText := fmt.Sprintf("relay-%d", attempt)
		response := fmt.Sprintf(`{"text":"%s","segments":[{"text":"%s","start":0,"end":1,"speaker":"SPEAKER_%02d"}]}`, segmentText, segmentText, attempt)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(response))
	}))

	resp := sendDemeterAudioSliceRequestToPath(
		t,
		app,
		token,
		"/api/v1/providers/demeter-sante/audio/transcriptions",
		"relay-short-1",
		0,
		1,
		true,
		"sample.wav",
		expectedAudio,
		2,
		defaultDemeterAudioTranscriptionModelID,
		false,
	)
	if resp.StatusCode != fiber.StatusAccepted {
		t.Fatalf("expected 202 for relay transcription start, got %d", resp.StatusCode)
	}

	var startPayload struct {
		OperationID string `json:"operationId"`
		Status      string `json:"status"`
		StatusCode  int    `json:"statusCode"`
		Stage       string `json:"stage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&startPayload); err != nil {
		_ = resp.Body.Close()
		t.Fatalf("failed to decode relay start payload: %v", err)
	}
	closeHTTPResponse(t, resp)

	finalPayload := waitForDemeterAudioTranscriptionOperationResponse(t, app, token, startPayload.OperationID)
	if finalPayload.Status != store.DemeterAudioTranscriptionOperationStatusCompleted {
		t.Fatalf("expected completed relay operation, got %+v", finalPayload)
	}
	if finalPayload.Response.Text != "relay-1" {
		t.Fatalf("unexpected relay response text: %+v", finalPayload.Response)
	}
	if len(finalPayload.Response.Chunks) != 1 {
		t.Fatalf("expected one relay chunk, got %+v", finalPayload.Response.Chunks)
	}
	if !strings.HasPrefix(finalPayload.Response.Chunks[0].ChunkID, "demeter-relay-relay-short-1") {
		t.Fatalf("expected relay chunk id prefix, got %+v", finalPayload.Response.Chunks[0].ChunkID)
	}

	logged := buffer.String()
	for _, needle := range []string{
		"route=/api/v1/providers/demeter-sante/audio/transcriptions",
		"route_mode=\"relay\"",
		"chunk_count=1",
		"normalized_format=\"application/octet-stream\"",
		"title=\"audio_transcription\"",
	} {
		if !strings.Contains(logged, needle) {
			t.Fatalf("expected %q in relay logs, got %q", needle, logged)
		}
	}
}

func TestDemeterAudioTranscriptions_RelayRouteUsesDistinctChunkIDsPerTransportOperation(t *testing.T) {
	var upstreamAttempts int32
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
		segmentText := fmt.Sprintf("relay-chunk-%d", attempt)
		response := fmt.Sprintf(`{"text":"%s","segments":[{"text":"%s","start":0,"end":1,"speaker":"SPEAKER_%02d"}]}`, segmentText, segmentText, attempt)
		w.Header().Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(response))
	}))

	var chunkIDs []string
	for i := 0; i < 2; i++ {
		uploadID := fmt.Sprintf("relay-op-%d", i+1)
		resp := sendDemeterAudioSliceRequestToPath(
			t,
			app,
			token,
			"/api/v1/providers/demeter-sante/audio/transcriptions/backend",
			uploadID,
			0,
			1,
			true,
			"sample.wav",
			buildSilentWAVBytes(t, 2, 4_000),
			2,
			defaultDemeterAudioTranscriptionModelID,
			false,
		)
		if resp.StatusCode != fiber.StatusAccepted {
			t.Fatalf("expected 202 for relay transcription start, got %d", resp.StatusCode)
		}

		var startPayload struct {
			OperationID string `json:"operationId"`
			Status      string `json:"status"`
			StatusCode  int    `json:"statusCode"`
			Stage       string `json:"stage"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&startPayload); err != nil {
			_ = resp.Body.Close()
			t.Fatalf("failed to decode relay start payload: %v", err)
		}
		closeHTTPResponse(t, resp)
		if startPayload.OperationID != uploadID {
			t.Fatalf("expected operation id %q, got %+v", uploadID, startPayload)
		}

		finalPayload := waitForDemeterAudioTranscriptionOperationResponse(t, app, token, startPayload.OperationID)
		if finalPayload.Status != store.DemeterAudioTranscriptionOperationStatusCompleted {
			t.Fatalf("expected completed relay operation, got %+v", finalPayload)
		}
		if finalPayload.Response.Text != fmt.Sprintf("relay-chunk-%d", i+1) {
			t.Fatalf("unexpected relay response text: %+v", finalPayload.Response)
		}
		if len(finalPayload.Response.Chunks) != 1 {
			t.Fatalf("expected one relay chunk, got %+v", finalPayload.Response.Chunks)
		}

		chunk := finalPayload.Response.Chunks[0]
		expectedPrefix := fmt.Sprintf("demeter-backend-%s", uploadID)
		if !strings.HasPrefix(chunk.ChunkID, expectedPrefix) {
			t.Fatalf("expected relay chunk id to start with %q, got %q", expectedPrefix, chunk.ChunkID)
		}
		if len(chunk.Segments) != 1 {
			t.Fatalf("expected one nested relay segment, got %+v", chunk)
		}
		if chunk.Segments[0].ChunkID != chunk.ChunkID {
			t.Fatalf("expected nested relay segment chunk id to match parent, got %+v", chunk)
		}
		chunkIDs = append(chunkIDs, chunk.ChunkID)
	}

	if chunkIDs[0] == chunkIDs[1] {
		t.Fatalf("expected distinct relay chunk ids across transport operations, got %+v", chunkIDs)
	}
}

func buildSilentWAVBytes(t *testing.T, durationSeconds int, sampleRate int) []byte {
	t.Helper()
	if durationSeconds <= 0 {
		t.Fatalf("duration must be positive")
	}
	if sampleRate <= 0 {
		t.Fatalf("sample rate must be positive")
	}

	numSamples := durationSeconds * sampleRate
	dataSize := numSamples * 2
	byteRate := sampleRate * 2
	blockAlign := 2
	chunkSize := 36 + dataSize
	buf := bytes.NewBuffer(make([]byte, 0, 44+dataSize))

	writeLE := func(value any) {
		if err := binary.Write(buf, binary.LittleEndian, value); err != nil {
			t.Fatalf("failed to write wav bytes: %v", err)
		}
	}

	_, _ = buf.WriteString("RIFF")
	writeLE(uint32(chunkSize))
	_, _ = buf.WriteString("WAVE")
	_, _ = buf.WriteString("fmt ")
	writeLE(uint32(16))
	writeLE(uint16(1))
	writeLE(uint16(1))
	writeLE(uint32(sampleRate))
	writeLE(uint32(byteRate))
	writeLE(uint16(blockAlign))
	writeLE(uint16(16))
	_, _ = buf.WriteString("data")
	writeLE(uint32(dataSize))
	if _, err := buf.Write(make([]byte, dataSize)); err != nil {
		t.Fatalf("failed to write wav silence data: %v", err)
	}
	return buf.Bytes()
}

const demeterBackendTransportSliceSizeBytes = 5 * 1024 * 1024

func buildDemeterAudioSliceMultipartBody(t *testing.T, fileName string, sliceBytes []byte, model string, diarize bool) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	filePart, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatalf("failed to create multipart file part: %v", err)
	}
	if _, err := filePart.Write(sliceBytes); err != nil {
		t.Fatalf("failed to write multipart file part: %v", err)
	}
	if diarize {
		if err := writer.WriteField("diarize", "true"); err != nil {
			t.Fatalf("failed to write multipart diarize field: %v", err)
		}
		if err := writer.WriteField("timestamp_granularities", "segment"); err != nil {
			t.Fatalf("failed to write multipart timestamp granularities field: %v", err)
		}
	} else {
		if err := writer.WriteField("diarize", "false"); err != nil {
			t.Fatalf("failed to write multipart diarize field: %v", err)
		}
	}
	if model != "" {
		if err := writer.WriteField("model", model); err != nil {
			t.Fatalf("failed to write multipart model field: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}
	return body.Bytes(), writer.FormDataContentType()
}

func sendDemeterAudioSliceRequest(t *testing.T, app *fiber.App, token string, uploadID string, sliceIndex int, sliceCount int, final bool, fileName string, sliceBytes []byte, durationSec int, model string, diarize bool) *http.Response {
	return sendDemeterAudioSliceRequestToPath(t, app, token, "/api/v1/providers/demeter-sante/audio/transcriptions/backend", uploadID, sliceIndex, sliceCount, final, fileName, sliceBytes, durationSec, model, diarize)
}

func sendDemeterAudioSliceRequestToPath(t *testing.T, app *fiber.App, token string, path string, uploadID string, sliceIndex int, sliceCount int, final bool, fileName string, sliceBytes []byte, durationSec int, model string, diarize bool) *http.Response {
	t.Helper()
	body, contentType := buildDemeterAudioSliceMultipartBody(t, fileName, sliceBytes, model, diarize)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set(fiber.HeaderAuthorization, "Bearer "+token)
	req.Header.Set(fiber.HeaderContentType, contentType)
	req.Header.Set(demeterAudioTransportHeader, demeterAudioTransportModeSliceV1)
	req.Header.Set(demeterAudioTransportUploadIDHeader, uploadID)
	req.Header.Set(demeterAudioTransportUploadIndexHeader, fmt.Sprintf("%d", sliceIndex))
	req.Header.Set(demeterAudioTransportUploadCountHeader, fmt.Sprintf("%d", sliceCount))
	if final {
		req.Header.Set(demeterAudioTransportUploadFinalHeader, "true")
	} else {
		req.Header.Set(demeterAudioTransportUploadFinalHeader, "false")
	}
	req.Header.Set("X-Cloud-Audio-Duration-Sec", fmt.Sprintf("%d", durationSec))
	resp, err := app.Test(req, 15_000)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}

type demeterAudioTranscriptionOperationStatusTestResponse struct {
	OperationID string  `json:"operationId"`
	Status      string  `json:"status"`
	StatusCode  int     `json:"statusCode"`
	Stage       string  `json:"stage"`
	ChunkIndex  int     `json:"chunkIndex"`
	ChunkCount  int     `json:"chunkCount"`
	Progress    float64 `json:"progress"`
	PartialText string  `json:"partialText"`
	LastError   string  `json:"lastError"`
	Response    struct {
		Text     string  `json:"text"`
		Duration float64 `json:"duration"`
		Chunks   []struct {
			ChunkID      string  `json:"chunkId"`
			Index        int     `json:"index"`
			StartSec     float64 `json:"startSec"`
			EndSec       float64 `json:"endSec"`
			DurationSec  float64 `json:"durationSec"`
			SegmentCount int     `json:"segmentCount"`
			Text         string  `json:"text"`
			Segments     []struct {
				Index   int     `json:"index"`
				Start   float64 `json:"start"`
				End     float64 `json:"end"`
				Text    string  `json:"text"`
				ChunkID string  `json:"chunkId"`
			} `json:"segments"`
		} `json:"chunks"`
	} `json:"response"`
}

func waitForDemeterAudioTranscriptionOperationResponse(
	t *testing.T,
	app *fiber.App,
	token string,
	operationID string,
) demeterAudioTranscriptionOperationStatusTestResponse {
	t.Helper()

	var finalPayload demeterAudioTranscriptionOperationStatusTestResponse
	deadline := time.Now().Add(20 * time.Second)
	for {
		statusReq := httptest.NewRequest(http.MethodGet, "/api/v1/providers/demeter-sante/audio/transcriptions/operations/"+operationID, nil)
		statusReq.Header.Set(fiber.HeaderAuthorization, "Bearer "+token)
		statusResp, err := app.Test(statusReq, 5_000)
		if err != nil {
			t.Fatalf("status request failed: %v", err)
		}
		if err := json.NewDecoder(statusResp.Body).Decode(&finalPayload); err != nil {
			_ = statusResp.Body.Close()
			t.Fatalf("failed to decode backend operation status: %v", err)
		}
		_ = statusResp.Body.Close()
		if finalPayload.Status == store.DemeterAudioTranscriptionOperationStatusCompleted {
			return finalPayload
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for backend operation completion, last payload: %+v", finalPayload)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestDemeterAudioTranscriptions_RejectsEmptyAudioFile(t *testing.T) {
	app, token, _ := setupDemeterRoutesApp(t, []store.UserPermissionOverrideInput{
		{PermissionCode: "feature.cloudupload", Effect: "allow"},
		{PermissionCode: "provider.cloud.demeter_sante", Effect: "allow"},
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("upstream should not be reached for empty audio")
	}))

	resp := sendDemeterAudioSliceRequest(t, app, token, fmt.Sprintf("backend-empty-%d", time.Now().UnixNano()), 0, 1, true, "empty.wav", nil, 0, defaultDemeterAudioTranscriptionModelID, false)
	defer closeHTTPResponse(t, resp)
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("expected 400 for empty audio file, got %d", resp.StatusCode)
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode empty audio response: %v", err)
	}
	if payload["code"] != "empty_audio_file" {
		t.Fatalf("expected empty_audio_file code, got %+v", payload)
	}
}

func TestDemeterAudioTranscriptions_BackendRouteRejectsNonSliceTransport(t *testing.T) {
	app, token, _ := setupDemeterRoutesApp(t, []store.UserPermissionOverrideInput{
		{PermissionCode: "feature.cloudupload", Effect: "allow"},
		{PermissionCode: "provider.cloud.demeter_sante", Effect: "allow"},
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("upstream should not be reached for non-slice backend transport")
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

	for _, path := range []string{
		"/api/v1/providers/demeter-sante/audio/transcriptions",
		"/api/v1/providers/demeter-sante/audio/transcriptions/backend",
	} {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body.Bytes()))
		req.Header.Set(fiber.HeaderAuthorization, "Bearer "+token)
		req.Header.Set(fiber.HeaderContentType, writer.FormDataContentType())

		resp, err := app.Test(req, 5_000)
		if err != nil {
			t.Fatalf("request failed for %s: %v", path, err)
		}
		func() {
			defer closeHTTPResponse(t, resp)
			if resp.StatusCode != fiber.StatusBadRequest {
				t.Fatalf("expected 400 for non-slice transport on %s, got %d", path, resp.StatusCode)
			}

			var payload map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
				t.Fatalf("failed to decode non-slice transport response for %s: %v", path, err)
			}
			if payload["code"] != "invalid_transport" {
				t.Fatalf("expected invalid_transport code for %s, got %+v", path, payload)
			}
		}()
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

	req := httptest.NewRequest(http.MethodPost, "/api/v1/providers/demeter-sante/audio/transcriptions/backend", bytes.NewBufferString(`{"bad":true}`))
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

func TestDemeterAudioTranscriptions_BackendRouteRejectsOversizedSliceImmediately(t *testing.T) {
	app, token, _ := setupDemeterRoutesApp(t, []store.UserPermissionOverrideInput{
		{PermissionCode: "feature.cloudupload", Effect: "allow"},
		{PermissionCode: "provider.cloud.demeter_sante", Effect: "allow"},
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("upstream should not be reached for oversized slice uploads")
	}))

	sliceBytes := bytes.Repeat([]byte("a"), demeterAudioTransportMaxRequestBytes+1024)
	body, contentType := buildDemeterAudioSliceMultipartBody(t, "sample.wav", sliceBytes, defaultDemeterAudioTranscriptionModelID, false)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/providers/demeter-sante/audio/transcriptions/backend", bytes.NewReader(body))
	req.Header.Set(fiber.HeaderAuthorization, "Bearer "+token)
	req.Header.Set(fiber.HeaderContentType, contentType)
	req.Header.Set(demeterAudioTransportHeader, demeterAudioTransportModeSliceV1)
	req.Header.Set(demeterAudioTransportUploadIDHeader, fmt.Sprintf("oversized-%d", time.Now().UnixNano()))
	req.Header.Set(demeterAudioTransportUploadIndexHeader, "0")
	req.Header.Set(demeterAudioTransportUploadCountHeader, "1")
	req.Header.Set(demeterAudioTransportUploadFinalHeader, "true")

	resp, err := app.Test(req, 5_000)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer closeHTTPResponse(t, resp)
	if resp.StatusCode != fiber.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversized slice upload, got %d", resp.StatusCode)
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode oversized slice response: %v", err)
	}
	if payload["code"] != "payload_too_large" {
		t.Fatalf("expected payload_too_large code, got %+v", payload)
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
	app := fiber.New(fiber.Config{
		BodyLimit: 16 * 1024 * 1024,
	})
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
