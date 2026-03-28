package reports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"demeter-backend/internal/mistral"
	"demeter-backend/internal/observability"
)

const (
	DefaultReportModelID   = "mistral-medium-latest"
	DefaultReportMaxTokens = 32768
	DefaultReportTemp      = 0
)

type Generator struct {
	Client      *mistral.Client
	ModelID     string
	MaxTokens   int
	Temperature float64
}

type GeneratedReport struct {
	Format ReportFormat
	Report ReportJson
	Raw    string
}

type GeneratedReports map[ReportFormat]GeneratedReport

func logReportStep(ctx context.Context, step, title string, fields map[string]any) {
	log.Print(observability.FormatStepLine("reports", "generator", step, observability.TraceIDFromContext(ctx), observability.DefaultTraceID, observability.DefaultTraceID, title, fields))
}

func (g *Generator) GenerateReports(ctx context.Context, meetingTitle string, participants []string, sourceText string, formats []ReportFormat) (GeneratedReports, error) {
	if g == nil || g.Client == nil || !g.Client.IsConfigured() {
		return nil, errors.New("mistral client is not configured")
	}
	if strings.TrimSpace(sourceText) == "" {
		return nil, errors.New("source text is empty")
	}

	modelID := strings.TrimSpace(g.ModelID)
	if modelID == "" {
		modelID = DefaultReportModelID
	}
	maxTokens := g.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultReportMaxTokens
	}
	temperature := g.Temperature
	if temperature < 0 || temperature > 2 {
		temperature = DefaultReportTemp
	}

	selectedFormats := formats
	if len(selectedFormats) == 0 {
		selectedFormats = AllReportFormats()
	}

	logReportStep(ctx, "generate_start", "reports", map[string]any{
		"meeting_title_present": strings.TrimSpace(meetingTitle) != "",
		"participants_count":    len(participants),
		"source_bytes":          len(sourceText),
		"format_count":          len(selectedFormats),
		"model_id":              modelID,
		"max_tokens":            maxTokens,
		"temperature":           temperature,
	})

	results := make(GeneratedReports, len(selectedFormats))
	for index, format := range selectedFormats {
		formatName := ReportFormatDisplayName(format)
		logReportStep(ctx, "generate_format_start", formatName, map[string]any{
			"index":  index + 1,
			"format": formatName,
		})

		report, raw, err := g.generateOne(ctx, modelID, meetingTitle, participants, sourceText, format, maxTokens, temperature)
		if err != nil {
			logReportStep(ctx, "generate_format_error", formatName, map[string]any{
				"index":  index + 1,
				"format": formatName,
				"error":  err,
			})
			logReportStep(ctx, "generate_error", "reports", map[string]any{
				"format_count": len(selectedFormats),
				"completed":    len(results),
				"error":        err,
			})
			return nil, err
		}
		results[format] = GeneratedReport{
			Format: format,
			Report: report,
			Raw:    raw,
		}
		logReportStep(ctx, "generate_format_success", formatName, map[string]any{
			"index":        index + 1,
			"format":       formatName,
			"sections":     len(report.Sections),
			"key_points":   len(report.KeyPoints),
			"action_items": len(report.ActionItems),
			"caveats":      len(report.Caveats),
		})
	}
	logReportStep(ctx, "generate_success", "reports", map[string]any{
		"format_count": len(results),
	})
	return results, nil
}

func (g *Generator) generateOne(
	ctx context.Context,
	modelID string,
	meetingTitle string,
	participants []string,
	sourceText string,
	format ReportFormat,
	maxTokens int,
	temperature float64,
) (ReportJson, string, error) {
	body := map[string]any{
		"model": modelID,
		"messages": []map[string]string{
			{"role": "system", "content": BuildReportSystemPrompt()},
			{"role": "user", "content": BuildReportUserPrompt(format, sourceText, meetingTitle, participants)},
		},
		"temperature": temperature,
		"max_tokens":  maxTokens,
		"response_format": map[string]string{
			"type": "json_object",
		},
	}
	rawBody, err := json.Marshal(body)
	if err != nil {
		return ReportJson{}, "", err
	}

	status, responseBody, err := g.Client.DoJSON(ctx, http.MethodPost, "/v1/chat/completions", rawBody)
	if err != nil {
		return ReportJson{}, "", err
	}
	if status < 200 || status >= 300 {
		return ReportJson{}, "", fmt.Errorf("mistral api (%d): %s", status, summarizeUpstreamBody(responseBody))
	}

	content := extractChatContent(responseBody)
	if content == "" {
		return ReportJson{}, "", errors.New("the model returned an empty response")
	}

	report, err := ParseReportJSON(content, format)
	if err != nil {
		return ReportJson{}, "", err
	}
	return report, content, nil
}

func extractChatContent(response []byte) string {
	if len(response) == 0 {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(response, &payload); err != nil {
		return ""
	}
	if text := normalizeTextContent(payload["output_text"]); text != "" {
		return text
	}
	if text := normalizeTextContent(payload["generated_text"]); text != "" {
		return text
	}
	if text := normalizeTextContent(payload["content"]); text != "" {
		return text
	}

	choices, _ := payload["choices"].([]any)
	if len(choices) == 0 {
		return ""
	}
	firstChoice, _ := choices[0].(map[string]any)
	if firstChoice == nil {
		return ""
	}
	if text := normalizeTextContent(firstChoice["text"]); text != "" {
		return text
	}
	message, _ := firstChoice["message"].(map[string]any)
	if message == nil {
		return ""
	}
	return normalizeTextContent(message["content"])
}

func normalizeTextContent(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, part := range typed {
			if text := normalizeTextContent(part); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	case map[string]any:
		if text, ok := typed["text"].(string); ok {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func summarizeUpstreamBody(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return "empty upstream body"
	}

	type upstreamErrorEnvelope struct {
		Error   any `json:"error"`
		Message any `json:"message"`
		Detail  any `json:"detail"`
	}

	var parsed upstreamErrorEnvelope
	if err := json.Unmarshal(body, &parsed); err == nil {
		if msg := firstNonEmptyMessage(parsed.Message, parsed.Error, parsed.Detail); msg != "" {
			return msg
		}
	}

	compacted := strings.Join(strings.Fields(trimmed), " ")
	if len(compacted) > 512 {
		return compacted[:512] + "..."
	}
	return compacted
}

func firstNonEmptyMessage(values ...any) string {
	for _, value := range values {
		if msg := messageFromAny(value); msg != "" {
			return msg
		}
	}
	return ""
}

func messageFromAny(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if msg := messageFromAny(item); msg != "" {
				parts = append(parts, msg)
			}
		}
		return strings.Join(parts, " | ")
	case map[string]any:
		return firstNonEmptyMessage(typed["message"], typed["error"], typed["detail"], typed["msg"])
	default:
		return ""
	}
}
