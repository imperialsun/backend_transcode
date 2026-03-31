package backenderrors

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"demeter-backend/internal/observability"
	"demeter-backend/internal/requestmeta"
)

const (
	maxStoredTextLength   = 512
	maxPayloadTextLength  = 4096
	maxSanitizeDepth      = 4
	defaultCaptureTimeout = 2 * time.Second
)

type Event struct {
	TraceID        string          `json:"traceId"`
	UserID         string          `json:"userId"`
	OrganizationID string          `json:"organizationId"`
	Component      string          `json:"component"`
	Route          string          `json:"route"`
	Step           string          `json:"step"`
	Title          string          `json:"title"`
	StatusCode     int             `json:"statusCode"`
	DurationMS     int64           `json:"durationMs"`
	ErrorMessage   string          `json:"errorMessage"`
	PayloadJSON    json.RawMessage `json:"payloadJson"`
	AnnexJSON      json.RawMessage `json:"annexJson,omitempty"`
	RecoveryStatus string          `json:"recoveryStatus,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
}

type Sink interface {
	InsertBackendErrorEvent(context.Context, Event) error
}

var (
	sinkMu sync.RWMutex
	sink   Sink
)

func RegisterSink(next Sink) {
	sinkMu.Lock()
	defer sinkMu.Unlock()
	sink = next
}

func RecordLog(ctx context.Context, component, route, step, title string, fields map[string]any) {
	if !shouldCapture(step, fields) {
		return
	}

	event := buildEvent(ctx, component, route, step, title, fields)

	sinkMu.RLock()
	currentSink := sink
	sinkMu.RUnlock()
	if currentSink == nil {
		return
	}

	go func(event Event, currentSink Sink) {
		defer func() {
			_ = recover()
		}()
		insertCtx, cancel := context.WithTimeout(context.Background(), defaultCaptureTimeout)
		defer cancel()
		_ = currentSink.InsertBackendErrorEvent(insertCtx, event)
	}(event, currentSink)
}

func buildEvent(ctx context.Context, component, route, step, title string, fields map[string]any) Event {
	traceID := observability.TraceIDFromContext(ctx)
	userID, orgID := "", ""
	if actorUserID, actorOrgID, ok := requestmeta.ActorFromContext(ctx); ok {
		userID = actorUserID
		orgID = actorOrgID
	}

	statusCode := extractStatusCode(fields)
	durationMS := extractDurationMS(fields)
	errorMessage := extractErrorMessage(fields)
	if errorMessage == "" && statusCode >= 500 {
		errorMessage = fmt.Sprintf("status %d", statusCode)
	}

	payload := sanitizePayload(fields)
	payloadJSON, err := json.Marshal(payload)
	if err != nil || len(payloadJSON) == 0 {
		payloadJSON = json.RawMessage(`{}`)
	}
	if len(payloadJSON) > maxPayloadTextLength {
		payloadJSON = json.RawMessage(fmt.Sprintf(`{"truncated":true,"size":%d}`, len(payloadJSON)))
	}

	return Event{
		TraceID:        traceID,
		UserID:         strings.TrimSpace(userID),
		OrganizationID: strings.TrimSpace(orgID),
		Component:      normalizeToken(component, "log"),
		Route:          normalizeToken(route, "-"),
		Step:           normalizeToken(step, "unknown"),
		Title:          normalizeText(title),
		StatusCode:     statusCode,
		DurationMS:     durationMS,
		ErrorMessage:   normalizeText(errorMessage),
		PayloadJSON:    payloadJSON,
		AnnexJSON:      nil,
		RecoveryStatus:  "",
		CreatedAt:      time.Now().UTC(),
	}
}

func shouldCapture(step string, fields map[string]any) bool {
	normalizedStep := strings.ToLower(strings.TrimSpace(step))
	if normalizedStep == "" {
		return false
	}

	for _, fragment := range []string{
		"validation_error",
		"request_validation_error",
		"request_parse_error",
		"parse_error",
		"unauthorized",
		"forbidden",
		"missing",
		"skipped",
		"rejected",
		"range_error",
	} {
		if strings.Contains(normalizedStep, fragment) {
			return false
		}
	}

	if strings.Contains(normalizedStep, "error") || strings.Contains(normalizedStep, "failed") || strings.Contains(normalizedStep, "timeout") {
		return true
	}

	return extractStatusCode(fields) >= 500
}

func sanitizePayload(fields map[string]any) map[string]any {
	if len(fields) == 0 {
		return map[string]any{}
	}

	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make(map[string]any, len(fields))
	for _, key := range keys {
		out[key] = sanitizeValue(fields[key], 0)
	}
	return out
}

func sanitizeValue(value any, depth int) any {
	if depth >= maxSanitizeDepth {
		return "<truncated>"
	}

	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return truncateText(typed)
	case []byte:
		return truncateText(string(typed))
	case error:
		return truncateText(typed.Error())
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano)
	case fmt.Stringer:
		return truncateText(typed.String())
	case json.RawMessage:
		return truncateText(string(typed))
	case map[string]any:
		out := make(map[string]any, len(typed))
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			out[key] = sanitizeValue(typed[key], depth+1)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(typed))
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			out[key] = truncateText(typed[key])
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, sanitizeValue(item, depth+1))
		}
		return out
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, truncateText(item))
		}
		return out
	default:
		return truncateText(fmt.Sprint(value))
	}
}

func truncateText(value string) string {
	value = normalizeText(value)
	if len(value) <= maxStoredTextLength {
		return value
	}
	return value[:maxStoredTextLength]
}

func normalizeToken(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return normalizeText(value)
}

func normalizeText(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

func extractStatusCode(fields map[string]any) int {
	for _, key := range []string{"status_code", "status", "upstream_status"} {
		if value, ok := fields[key]; ok {
			if parsed, ok := toInt(value); ok {
				return parsed
			}
		}
	}
	return 0
}

func extractDurationMS(fields map[string]any) int64 {
	for _, key := range []string{"duration_ms", "upstream_duration_ms", "elapsed_ms"} {
		if value, ok := fields[key]; ok {
			if parsed, ok := toInt64(value); ok {
				return parsed
			}
		}
	}
	return 0
}

func extractErrorMessage(fields map[string]any) string {
	for _, key := range []string{"error", "response_error", "summary", "message", "detail"} {
		if value, ok := fields[key]; ok {
			switch typed := value.(type) {
			case error:
				if msg := normalizeText(typed.Error()); msg != "" {
					return msg
				}
			case string:
				if msg := normalizeText(typed); msg != "" {
					return msg
				}
			case fmt.Stringer:
				if msg := normalizeText(typed.String()); msg != "" {
					return msg
				}
			default:
				if msg := normalizeText(fmt.Sprint(typed)); msg != "" {
					return msg
				}
			}
		}
	}
	return ""
}

func toInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case uint:
		return int(typed), true
	case uint8:
		return int(typed), true
	case uint16:
		return int(typed), true
	case uint32:
		return int(typed), true
	case uint64:
		return int(typed), true
	case float32:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func toInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int8:
		return int64(typed), true
	case int16:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case uint:
		return int64(typed), true
	case uint8:
		return int64(typed), true
	case uint16:
		return int64(typed), true
	case uint32:
		return int64(typed), true
	case uint64:
		return int64(typed), true
	case float32:
		return int64(typed), true
	case float64:
		return int64(typed), true
	default:
		return 0, false
	}
}
