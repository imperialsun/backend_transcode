package reports

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"demeter-backend/internal/mistral"
	"demeter-backend/internal/observability"
)

func TestGeneratorLogsSuccessWithoutTranscriptPayload(t *testing.T) {
	var buf bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	previousPrefix := log.Prefix()
	log.SetOutput(&buf)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
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
		w.Header().Set("Content-Type", "application/json")
		reportJSON := fmt.Sprintf(`{"format":"%s","title":"%s","sections":[{"heading":"A","paragraphs":["B"]}],"key_points":["P"],"action_items":["A"],"caveats":["C"]}`, format, format)
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
	defer server.Close()

	client := mistral.NewClient(server.URL, "key", time.Second, time.Second)
	gen := &Generator{Client: client, ModelID: "mistral-medium-latest", MaxTokens: 1024, Temperature: 0}
	ctx := observability.WithTraceID(context.Background(), "reports-trace")

	reports, err := gen.GenerateReports(ctx, "Réunion sensible", []string{"Alice", "Bob"}, "transcript payload", []ReportFormat{ReportFormatCRI, ReportFormatCRO})
	if err != nil {
		t.Fatalf("GenerateReports failed: %v", err)
	}
	if len(reports) != 2 {
		t.Fatalf("unexpected report count: %d", len(reports))
	}

	logged := buf.String()
	for _, needle := range []string{
		"step=generate_start",
		"step=generate_format_start",
		"step=generate_format_success",
		"step=generate_success",
		"trace_id=reports-trace",
	} {
		if !strings.Contains(logged, needle) {
			t.Fatalf("expected %q in logs, got %q", needle, logged)
		}
	}
	if strings.Contains(logged, "transcript payload") {
		t.Fatalf("did not expect raw transcript content in logs, got %q", logged)
	}
	if strings.Contains(logged, "Alice") || strings.Contains(logged, "Bob") {
		t.Fatalf("did not expect participant names in logs, got %q", logged)
	}
}

func TestGeneratorLogsErrorForUpstreamFailure(t *testing.T) {
	stubReportGenerationSleep(t)

	var buf bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	previousPrefix := log.Prefix()
	log.SetOutput(&buf)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"upstream down"}`))
	}))
	defer server.Close()

	client := mistral.NewClient(server.URL, "key", time.Second, time.Second)
	gen := &Generator{Client: client, ModelID: "mistral-medium-latest", MaxTokens: 1024, Temperature: 0}
	ctx := observability.WithTraceID(context.Background(), "reports-error-trace")

	_, err := gen.GenerateReports(ctx, "Réunion", []string{"Alice"}, "transcript payload", []ReportFormat{ReportFormatCRI})
	if err == nil {
		t.Fatal("expected GenerateReports to fail")
	}

	logged := buf.String()
	for _, needle := range []string{
		"step=generate_format_error",
		"step=generate_error",
		"trace_id=reports-error-trace",
	} {
		if !strings.Contains(logged, needle) {
			t.Fatalf("expected %q in logs, got %q", needle, logged)
		}
	}
	if strings.Contains(logged, "transcript payload") {
		t.Fatalf("did not expect raw transcript content in error logs, got %q", logged)
	}
}

func TestGeneratorLogsRetriesAndSuccess(t *testing.T) {
	stubReportGenerationSleep(t)

	var attempts int32
	var buf bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	previousPrefix := log.Prefix()
	log.SetOutput(&buf)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&attempts, 1)
		if attempt < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"upstream down"}`))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		reportJSON := `{"format":"CRI","title":"CRI","sections":[{"heading":"A","paragraphs":["B"]}]}`
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
	defer server.Close()

	client := mistral.NewClient(server.URL, "key", time.Second, time.Second)
	gen := &Generator{Client: client, ModelID: "mistral-medium-latest", MaxTokens: 1024, Temperature: 0}
	ctx := observability.WithTraceID(context.Background(), "reports-retry-trace")

	reports, err := gen.GenerateReports(ctx, "Réunion", []string{"Alice"}, "transcript payload", []ReportFormat{ReportFormatCRI})
	if err != nil {
		t.Fatalf("GenerateReports failed: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("unexpected report count: %d", len(reports))
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("expected 3 attempts, got %d", got)
	}

	logged := buf.String()
	for _, needle := range []string{
		"step=generate_format_start",
		"step=generate_format_error",
		"step=generate_format_retry",
		"step=generate_format_success",
		"attempt=1",
		"attempt=2",
		"attempt=3",
		"max_attempts=10",
		"status_code=503",
		"status_code=200",
		"trace_id=reports-retry-trace",
	} {
		if !strings.Contains(logged, needle) {
			t.Fatalf("expected %q in logs, got %q", needle, logged)
		}
	}
	if strings.Contains(logged, "transcript payload") {
		t.Fatalf("did not expect raw transcript content in retry logs, got %q", logged)
	}
	if strings.Contains(logged, "Alice") {
		t.Fatalf("did not expect participant names in retry logs, got %q", logged)
	}
}
