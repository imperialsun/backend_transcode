package reports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"demeter-backend/internal/backenderrors"
	"demeter-backend/internal/mistral"
	"demeter-backend/internal/observability"
)

const (
	DefaultReportModelID   = "mistral-medium-latest"
	DefaultReportMaxTokens = 32768
	DefaultReportTemp      = 0

	reportGenerationMaxAttempts = 3
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
	backenderrors.RecordLog(ctx, "reports", "generator", step, title, fields)
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
		"max_attempts":          reportGenerationMaxAttempts,
	})

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type formatResult struct {
		format ReportFormat
		report GeneratedReport
		err    error
	}

	resultsCh := make(chan formatResult, len(selectedFormats))
	var wg sync.WaitGroup
	for _, format := range selectedFormats {
		format := format
		wg.Add(1)
		go func() {
			defer wg.Done()

			report, err := g.generateReportForFormat(ctx, modelID, meetingTitle, participants, sourceText, format, maxTokens, temperature, reportGenerationMaxAttempts)
			if err != nil {
				cancel()
				resultsCh <- formatResult{format: format, err: err}
				return
			}
			resultsCh <- formatResult{format: format, report: report}
		}()
	}

	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	results := make(GeneratedReports, len(selectedFormats))
	var firstErr error
	for result := range resultsCh {
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
			}
			continue
		}
		results[result.format] = result.report
	}

	if firstErr != nil {
		logReportStep(ctx, "generate_error", "reports", map[string]any{
			"format_count": len(selectedFormats),
			"completed":    len(results),
			"error":        firstErr,
		})
		return nil, firstErr
	}
	logReportStep(ctx, "generate_success", "reports", map[string]any{
		"format_count": len(results),
	})
	return results, nil
}

func (g *Generator) generateReportForFormat(
	ctx context.Context,
	modelID string,
	meetingTitle string,
	participants []string,
	sourceText string,
	format ReportFormat,
	maxTokens int,
	temperature float64,
	maxAttempts int,
) (GeneratedReport, error) {
	formatName := ReportFormatDisplayName(format)
	var lastErr error
	var lastStatusCode int
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return GeneratedReport{}, err
		}

		logReportStep(ctx, "generate_format_start", formatName, map[string]any{
			"format":       formatName,
			"attempt":      attempt,
			"max_attempts": maxAttempts,
		})

		report, raw, statusCode, err := g.generateOne(ctx, modelID, meetingTitle, participants, sourceText, format, maxTokens, temperature)
		if err == nil {
			logReportStep(ctx, "generate_format_success", formatName, map[string]any{
				"format":       formatName,
				"attempt":      attempt,
				"max_attempts": maxAttempts,
				"status_code":  statusCode,
				"sections":     len(report.Sections),
				"key_points":   len(report.KeyPoints),
				"action_items": len(report.ActionItems),
				"caveats":      len(report.Caveats),
			})
			return GeneratedReport{
				Format: format,
				Report: report,
				Raw:    raw,
			}, nil
		}

		lastErr = err
		lastStatusCode = statusCode
		logFields := map[string]any{
			"format":       formatName,
			"attempt":      attempt,
			"max_attempts": maxAttempts,
			"status_code":  statusCode,
			"error":        err,
		}
		logReportStep(ctx, "generate_format_error", formatName, logFields)

		if attempt < maxAttempts {
			nextAttempt := attempt + 1
			delay := reportGenerationRetryDelay(attempt)
			logReportStep(ctx, "generate_format_retry", formatName, map[string]any{
				"format":       formatName,
				"attempt":      attempt,
				"next_attempt": nextAttempt,
				"max_attempts": maxAttempts,
				"delay_ms":     delay.Milliseconds(),
				"status_code":  statusCode,
				"error":        err,
			})
			if err := sleepContext(ctx, delay); err != nil {
				return GeneratedReport{}, err
			}
		}
	}

	if lastStatusCode > 0 {
		return GeneratedReport{}, fmt.Errorf("failed to generate report for %s after %d attempts (status %d): %w", formatName, maxAttempts, lastStatusCode, lastErr)
	}
	return GeneratedReport{}, fmt.Errorf("failed to generate report for %s after %d attempts: %w", formatName, maxAttempts, lastErr)
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
) (ReportJson, string, int, error) {
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
		return ReportJson{}, "", 0, err
	}

	status, responseBody, err := g.Client.DoJSON(ctx, http.MethodPost, "/v1/chat/completions", rawBody)
	if err != nil {
		return ReportJson{}, "", status, err
	}
	if status < 200 || status >= 300 {
		return ReportJson{}, "", status, fmt.Errorf("mistral api (%d): %s", status, summarizeUpstreamBody(responseBody))
	}

	content := extractChatContent(responseBody)
	if content == "" {
		return ReportJson{}, "", status, errors.New("the model returned an empty response")
	}

	report, err := ParseReportJSON(content, format)
	if err != nil {
		return ReportJson{}, "", status, err
	}
	return report, content, status, nil
}

func reportGenerationRetryDelay(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 250 * time.Millisecond
	case 2:
		return 500 * time.Millisecond
	default:
		return 0
	}
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
