package observability

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

type traceIDKey struct{}

const DefaultTraceID = "-"

const MaxTraceIDLength = 128

func NewTraceID() string {
	return uuid.NewString()
}

func NormalizeTraceID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	var builder strings.Builder
	builder.Grow(len(value))
	for _, r := range value {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_', r == '.', r == ':':
			builder.WriteRune(r)
		case unicode.IsSpace(r):
			builder.WriteByte('-')
		default:
			builder.WriteByte('-')
		}
	}

	normalized := strings.Trim(builder.String(), "-")
	if normalized == "" {
		return ""
	}
	if len(normalized) > MaxTraceIDLength {
		normalized = normalized[:MaxTraceIDLength]
	}
	return normalized
}

func WithTraceID(ctx context.Context, traceID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	normalized := NormalizeTraceID(traceID)
	if normalized == "" {
		normalized = NewTraceID()
	}
	return context.WithValue(ctx, traceIDKey{}, normalized)
}

func TraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return DefaultTraceID
	}

	if traceID, ok := ctx.Value(traceIDKey{}).(string); ok {
		if normalized := NormalizeTraceID(traceID); normalized != "" {
			return normalized
		}
	}
	return DefaultTraceID
}

func FormatStepLine(component, route, step, traceID, userID, orgID, title string, fields map[string]any) string {
	component = strings.TrimSpace(component)
	if component == "" {
		component = "log"
	}
	route = strings.TrimSpace(route)
	step = strings.TrimSpace(step)
	if step == "" {
		step = "unknown"
	}
	traceID = NormalizeTraceID(traceID)
	if traceID == "" {
		traceID = DefaultTraceID
	}
	userID = normalizeToken(userID)
	orgID = normalizeToken(orgID)
	title = sanitizeLogText(title)

	var builder strings.Builder
	builder.WriteByte('[')
	builder.WriteString(component)
	builder.WriteByte(']')
	if route != "" {
		builder.WriteString(" route=")
		builder.WriteString(route)
	}
	builder.WriteString(" step=")
	builder.WriteString(step)
	builder.WriteString(" trace_id=")
	builder.WriteString(traceID)
	builder.WriteString(" user=")
	builder.WriteString(userID)
	builder.WriteString(" org=")
	builder.WriteString(orgID)
	builder.WriteString(" title=")
	builder.WriteString(strconv.Quote(title))

	if len(fields) > 0 {
		keys := make([]string, 0, len(fields))
		for key := range fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			builder.WriteByte(' ')
			builder.WriteString(key)
			builder.WriteByte('=')
			builder.WriteString(formatFieldValue(fields[key]))
		}
	}

	return builder.String()
}

func sanitizeLogText(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

func normalizeToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return DefaultTraceID
	}
	return sanitizeLogText(value)
}

func formatFieldValue(value any) string {
	switch v := value.(type) {
	case string:
		return strconv.Quote(sanitizeLogText(v))
	case error:
		return strconv.Quote(sanitizeLogText(v.Error()))
	case fmt.Stringer:
		return strconv.Quote(sanitizeLogText(v.String()))
	default:
		return fmt.Sprint(v)
	}
}
