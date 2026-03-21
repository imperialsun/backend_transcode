package reports

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"demeter-backend/internal/mistral"
)

func TestParseReportJSON(t *testing.T) {
	raw := "```json\n{\"format\":\"CRI\",\"title\":\"Test\",\"sections\":[{\"heading\":\"Contexte\",\"paragraphs\":[\"Un paragraphe\"]}],\"key_points\":[\"P1\"],\"action_items\":[\"A1\"],\"caveats\":[\"C1\"]}\n```"
	report, err := ParseReportJSON(raw, ReportFormatCRI)
	if err != nil {
		t.Fatalf("ParseReportJSON failed: %v", err)
	}
	if report.Format != ReportFormatCRI {
		t.Fatalf("unexpected format: %s", report.Format)
	}
	if len(report.Sections) != 1 {
		t.Fatalf("unexpected section count: %d", len(report.Sections))
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
	reports, err := gen.GenerateReports(context.Background(), "Réunion", []string{"Alice", "Bob"}, "source text", []ReportFormat{ReportFormatCRI, ReportFormatCRO})
	if err != nil {
		t.Fatalf("GenerateReports failed: %v", err)
	}
	if len(reports) != 2 {
		t.Fatalf("unexpected report count: %d", len(reports))
	}
}
