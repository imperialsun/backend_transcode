package reports

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"demeter-backend/internal/mistral"
)

// These tests cover JSON parsing, concurrent format generation, and retry
// cancellation behavior for the report generator.
func TestParseReportJSON(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		format ReportFormat
	}{
		{
			name:   "cri",
			raw:    "```json\n{\"format\":\"CRI\",\"title\":\"Test\",\"sections\":[{\"heading\":\"Contexte\",\"paragraphs\":[\"Un paragraphe\"]}],\"key_points\":[\"P1\"],\"action_items\":[\"A1\"],\"caveats\":[\"C1\"]}\n```",
			format: ReportFormatCRI,
		},
		{
			name:   "crn",
			raw:    "```json\n{\"format\":\"CRN\",\"title\":\"Test\",\"sections\":[{\"heading\":\"Contexte\",\"paragraphs\":[\"Un paragraphe\"]}],\"key_points\":[\"P1\"],\"action_items\":[\"A1\"],\"caveats\":[\"C1\"]}\n```",
			format: ReportFormatCRN,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report, err := ParseReportJSON(tt.raw, tt.format)
			if err != nil {
				t.Fatalf("ParseReportJSON failed: %v", err)
			}
			if report.Format != tt.format {
				t.Fatalf("unexpected format: %s", report.Format)
			}
			if len(report.Sections) != 1 {
				t.Fatalf("unexpected section count: %d", len(report.Sections))
			}
		})
	}
}

func TestReportGenerationRetryDelay(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 1, want: 2 * time.Second},
		{attempt: 2, want: 4 * time.Second},
		{attempt: 3, want: 8 * time.Second},
		{attempt: 5, want: 32 * time.Second},
		{attempt: 9, want: 32 * time.Second},
	}

	for _, tt := range tests {
		if got := reportGenerationRetryDelay(tt.attempt); got != tt.want {
			t.Fatalf("attempt %d: expected %s, got %s", tt.attempt, tt.want, got)
		}
	}
}

func TestGeneratorGenerateReports(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
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
		case strings.Contains(content, "CRN"):
			format = "CRN"
		}
		w.Header().Set("Content-Type", "application/json")
		reportJSON := fmt.Sprintf(`{"format":"%s","title":"%s","sections":[{"heading":"A","paragraphs":["B"]}]}`, format, format)
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
	reports, err := gen.GenerateReports(context.Background(), "Réunion", []string{"Alice", "Bob"}, "source text", []ReportFormat{ReportFormatCRI, ReportFormatCRO, ReportFormatCRN})
	if err != nil {
		t.Fatalf("GenerateReports failed: %v", err)
	}
	if len(reports) != 3 {
		t.Fatalf("unexpected report count: %d", len(reports))
	}
}

func TestGeneratorGenerateReportsRunsFormatsInParallel(t *testing.T) {
	var active int32
	var maxActive int32
	started := make(chan ReportFormat, 3)
	release := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		format := reportFormatFromRequest(t, r)
		current := atomic.AddInt32(&active, 1)
		updateMaxInt32(&maxActive, current)
		defer atomic.AddInt32(&active, -1)

		started <- format
		<-release

		writeMockReportResponse(t, w, format)
	}))
	defer server.Close()

	client := mistral.NewClient(server.URL, "key", time.Second, time.Second)
	gen := &Generator{Client: client, ModelID: "mistral-medium-latest", MaxTokens: 1024, Temperature: 0}

	type result struct {
		reports GeneratedReports
		err     error
	}
	resultCh := make(chan result, 1)
	go func() {
		reports, err := gen.GenerateReports(context.Background(), "Réunion", []string{"Alice", "Bob"}, "source text", []ReportFormat{ReportFormatCRI, ReportFormatCRO, ReportFormatCRS})
		resultCh <- result{reports: reports, err: err}
	}()

	seen := map[ReportFormat]struct{}{}
	for len(seen) < 3 {
		select {
		case format := <-started:
			seen[format] = struct{}{}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for concurrent report requests, seen=%v", seen)
		}
	}

	close(release)

	var res result
	select {
	case res = <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for GenerateReports to return")
	}
	if res.err != nil {
		t.Fatalf("GenerateReports failed: %v", res.err)
	}
	if len(res.reports) != 3 {
		t.Fatalf("unexpected report count: %d", len(res.reports))
	}
	if got := atomic.LoadInt32(&maxActive); got < 3 {
		t.Fatalf("expected at least 3 concurrent upstream requests, got %d", got)
	}
	if _, ok := res.reports[ReportFormatCRI]; !ok {
		t.Fatal("expected CRI report to be present")
	}
	if _, ok := res.reports[ReportFormatCRO]; !ok {
		t.Fatal("expected CRO report to be present")
	}
	if _, ok := res.reports[ReportFormatCRS]; !ok {
		t.Fatal("expected CRS report to be present")
	}
}

func TestGeneratorGenerateReportsCancelsRemainingFormatsAfterTerminalFailure(t *testing.T) {
	stubReportGenerationSleep(t)

	var mu sync.Mutex
	attempts := map[ReportFormat]int{}
	started := make(chan ReportFormat, 3)
	allowFailure := make(chan struct{})
	canceled := make(chan ReportFormat, 2)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		format := reportFormatFromRequest(t, r)

		mu.Lock()
		attempts[format]++
		attempt := attempts[format]
		mu.Unlock()

		if attempt == 1 {
			started <- format
		}

		if format == ReportFormatCRI {
			if attempt == 1 {
				select {
				case <-allowFailure:
				case <-time.After(5 * time.Second):
					t.Fatal("timed out waiting to release failing report format")
				}
			}
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"upstream down"}`))
			return
		}

		select {
		case <-r.Context().Done():
			canceled <- format
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for cancellation of %s", format)
		}
	}))
	defer server.Close()

	client := mistral.NewClient(server.URL, "key", time.Second, time.Second)
	gen := &Generator{Client: client, ModelID: "mistral-medium-latest", MaxTokens: 1024, Temperature: 0}

	type result struct {
		reports GeneratedReports
		err     error
	}
	resultCh := make(chan result, 1)
	go func() {
		reports, err := gen.GenerateReports(context.Background(), "Réunion", []string{"Alice", "Bob"}, "source text", []ReportFormat{ReportFormatCRI, ReportFormatCRO, ReportFormatCRS})
		resultCh <- result{reports: reports, err: err}
	}()

	seen := map[ReportFormat]struct{}{}
	for len(seen) < 3 {
		select {
		case format := <-started:
			seen[format] = struct{}{}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for all report formats to start, seen=%v", seen)
		}
	}

	close(allowFailure)

	var res result
	select {
	case res = <-resultCh:
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for GenerateReports to fail")
	}
	if res.err == nil {
		t.Fatal("expected GenerateReports to fail")
	}
	if res.reports != nil {
		t.Fatalf("expected no partial reports on failure, got %+v", res.reports)
	}

	canceledSeen := map[ReportFormat]struct{}{}
	for len(canceledSeen) < 2 {
		select {
		case format := <-canceled:
			canceledSeen[format] = struct{}{}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for cancellation of remaining formats, got %v", canceledSeen)
		}
	}

	if _, ok := canceledSeen[ReportFormatCRO]; !ok {
		t.Fatal("expected CRO generation to be canceled")
	}
	if _, ok := canceledSeen[ReportFormatCRS]; !ok {
		t.Fatal("expected CRS generation to be canceled")
	}
}

func reportFormatFromRequest(t *testing.T, r *http.Request) ReportFormat {
	t.Helper()

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

	switch {
	case strings.Contains(content, "Format cible: CRO."):
		return ReportFormatCRO
	case strings.Contains(content, "Format cible: CRS."):
		return ReportFormatCRS
	case strings.Contains(content, "Format cible: CRI."):
		return ReportFormatCRI
	case strings.Contains(content, "CRO"):
		return ReportFormatCRO
	case strings.Contains(content, "CRS"):
		return ReportFormatCRS
	case strings.Contains(content, "CRI"):
		return ReportFormatCRI
	default:
		t.Fatalf("unable to identify report format from content: %q", content)
		return ""
	}
}

// writeMockReportResponse emits the minimal upstream response used by the
// generator tests.
func writeMockReportResponse(t *testing.T, w http.ResponseWriter, format ReportFormat) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	reportJSON := fmt.Sprintf(`{"format":"%s","title":"%s","sections":[{"heading":"A","paragraphs":["B"]}]}`, format, format)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{
			map[string]any{
				"message": map[string]any{
					"content": reportJSON,
				},
			},
		},
	})
}

// updateMaxInt32 tracks the highest observed concurrency level in a
// thread-safe way.
func updateMaxInt32(max *int32, value int32) {
	for {
		current := atomic.LoadInt32(max)
		if value <= current {
			return
		}
		if atomic.CompareAndSwapInt32(max, current, value) {
			return
		}
	}
}

func stubReportGenerationSleep(t *testing.T) {
	t.Helper()

	previous := reportGenerationSleep
	reportGenerationSleep = func(ctx context.Context, _ time.Duration) error {
		return ctx.Err()
	}
	t.Cleanup(func() {
		reportGenerationSleep = previous
	})
}
