package backendperformance

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
	"github.com/google/uuid"
)

const (
	maxStoredTextLength   = 512
	maxPayloadTextLength  = 4096
	maxSanitizeDepth      = 4
	defaultCaptureTimeout = 2 * time.Second
)

type Event struct {
	EventID        string          `json:"eventId"`
	TraceID        string          `json:"traceId"`
	UserID         string          `json:"userId,omitempty"`
	OrganizationID string          `json:"organizationId,omitempty"`
	Surface        string          `json:"surface"`
	Component      string          `json:"component"`
	Task           string          `json:"task"`
	Status         string          `json:"status"`
	DurationMS     int64           `json:"durationMs"`
	Route          string          `json:"route"`
	MetaJSON       json.RawMessage `json:"metaJson"`
	OccurredAt     time.Time       `json:"occurredAt"`
}

type Sink interface {
	InsertPerformanceEvent(context.Context, Event) error
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
	if observability.ShouldSkipObservabilityCaptureRoute(route) {
		return
	}
	event, ok := buildEvent(ctx, component, route, step, title, fields)
	if !ok {
		return
	}

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
		_ = currentSink.InsertPerformanceEvent(insertCtx, event)
	}(event, currentSink)
}

func buildEvent(ctx context.Context, component, route, step, title string, fields map[string]any) (Event, bool) {
	durationMS, ok := extractDurationMS(fields)
	if !ok {
		return Event{}, false
	}

	traceID := observability.TraceIDFromContext(ctx)
	userID, orgID := "", ""
	if actorUserID, actorOrgID, ok := requestmeta.ActorFromContext(ctx); ok {
		userID = actorUserID
		orgID = actorOrgID
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
		EventID:        uuid.NewString(),
		TraceID:        strings.TrimSpace(traceID),
		UserID:         strings.TrimSpace(userID),
		OrganizationID: strings.TrimSpace(orgID),
		Surface:        "backend",
		Component:      normalizeToken(component, "log"),
		Task:           normalizeToken(title, normalizeToken(step, "unknown")),
		Status:         extractStatus(fields, step),
		DurationMS:     durationMS,
		Route:          normalizeToken(route, "-"),
		MetaJSON:       payloadJSON,
		OccurredAt:     time.Now().UTC(),
	}, true
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

func extractDurationMS(fields map[string]any) (int64, bool) {
	if len(fields) == 0 {
		return 0, false
	}

	for _, key := range []string{"duration_ms", "total_duration_ms", "upstream_duration_ms", "elapsed_ms"} {
		value, ok := fields[key]
		if !ok {
			continue
		}
		if parsed, ok := toInt64(value); ok {
			return parsed, true
		}
	}
	return 0, false
}

func extractStatus(fields map[string]any, step string) string {
	if len(fields) == 0 {
		return statusFromStep(step)
	}

	for _, key := range []string{"status", "status_code", "upstream_status"} {
		value, ok := fields[key]
		if !ok {
			continue
		}

		switch typed := value.(type) {
		case string:
			if msg := strings.ToLower(normalizeText(typed)); msg != "" {
				return msg
			}
		default:
			if code, ok := toInt(typed); ok {
				return statusFromCode(code, step)
			}
		}
	}

	return statusFromStep(step)
}

func statusFromCode(code int, step string) string {
	if code >= 400 {
		return "error"
	}
	if code > 0 {
		return "success"
	}
	return statusFromStep(step)
}

func statusFromStep(step string) string {
	normalized := strings.ToLower(strings.TrimSpace(step))
	if normalized == "" {
		return "success"
	}
	if strings.Contains(normalized, "error") || strings.Contains(normalized, "failed") || strings.Contains(normalized, "timeout") {
		return "error"
	}
	return "success"
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
