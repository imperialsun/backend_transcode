package api

import (
	"context"
	"log"
	"strings"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/backenderrors"
	"demeter-backend/internal/observability"

	"github.com/gofiber/fiber/v2"
)

const traceIDLocalsKey = "trace_id"

// RequestTrace ensures every request gets a trace identifier before the rest of
// the middleware chain runs.
func (a *App) RequestTrace() fiber.Handler {
	return func(c *fiber.Ctx) error {
		ensureRequestTraceID(c)
		return c.Next()
	}
}

// ensureRequestTraceID normalizes an incoming trace header or generates a new
// one when the request does not provide a usable value.
func ensureRequestTraceID(c *fiber.Ctx) string {
	if c == nil {
		return observability.NewTraceID()
	}

	if traceID, ok := c.Locals(traceIDLocalsKey).(string); ok {
		if normalized := observability.NormalizeTraceID(traceID); normalized != "" {
			traceID = strings.Clone(normalized)
			if ctx := c.UserContext(); ctx != nil {
				c.SetUserContext(observability.WithTraceID(ctx, traceID))
			} else {
				c.SetUserContext(observability.WithTraceID(context.Background(), traceID))
			}
			c.Set("X-Trace-Id", traceID)
			c.Locals(traceIDLocalsKey, traceID)
			return traceID
		}
	}

	traceID := observability.NormalizeTraceID(strings.TrimSpace(c.Get(fiber.HeaderXRequestID)))
	if traceID == "" {
		traceID = observability.NormalizeTraceID(strings.TrimSpace(c.Get("X-Trace-Id")))
	}
	if traceID == "" {
		traceID = observability.NewTraceID()
	}
	traceID = strings.Clone(traceID)

	ctx := c.UserContext()
	if ctx == nil {
		ctx = context.Background()
	}
	c.SetUserContext(observability.WithTraceID(ctx, traceID))
	c.Set("X-Trace-Id", traceID)
	c.Locals(traceIDLocalsKey, traceID)
	return traceID
}

// requestTraceID returns the current request trace identifier and creates one
// on demand when needed.
func requestTraceID(c *fiber.Ctx) string {
	if c == nil {
		return observability.DefaultTraceID
	}

	if traceID, ok := c.Locals(traceIDLocalsKey).(string); ok {
		if normalized := observability.NormalizeTraceID(traceID); normalized != "" {
			return normalized
		}
	}

	if traceID := observability.TraceIDFromContext(requestContext(c)); traceID != observability.DefaultTraceID {
		return strings.Clone(traceID)
	}

	return ensureRequestTraceID(c)
}

// requestRoutePath returns the registered route path when Fiber knows it and
// falls back to the raw path otherwise.
func requestRoutePath(c *fiber.Ctx) string {
	if c == nil {
		return "-"
	}
	if route := c.Route(); route != nil {
		if path := strings.TrimSpace(route.Path); path != "" && path != "/" && path != "*" {
			return path
		}
	}
	if path := strings.TrimSpace(c.Path()); path != "" {
		return path
	}
	return "-"
}

// requestActorIDs extracts the current user and organization IDs from claims.
func requestActorIDs(c *fiber.Ctx) (string, string) {
	return claimsActorIDs(MustClaims(c))
}

// claimsActorIDs returns safe log-friendly actor identifiers for structured
// logs.
func claimsActorIDs(claims *auth.Claims) (string, string) {
	if claims == nil {
		return observability.DefaultTraceID, observability.DefaultTraceID
	}
	userID := strings.TrimSpace(claims.UserID)
	orgID := strings.TrimSpace(claims.OrgID)
	if userID == "" {
		userID = observability.DefaultTraceID
	} else {
		userID = strings.Clone(userID)
	}
	if orgID == "" {
		orgID = observability.DefaultTraceID
	} else {
		orgID = strings.Clone(orgID)
	}
	return userID, orgID
}

// cloneDemeterRequestString trims a request value and clones it before storing
// it in long-lived state.
func cloneDemeterRequestString(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	return strings.Clone(trimmed)
}

// logAPIStep emits a structured API log line and forwards it to the
// observability sinks.
func logAPIStep(c *fiber.Ctx, component, route, step, title string, fields map[string]any) {
	userID, orgID := requestActorIDs(c)
	ctx := requestContext(c)
	log.Print(observability.FormatStepLine(component, route, step, requestTraceID(c), userID, orgID, title, fields))
	backenderrors.RecordLog(ctx, component, route, step, title, fields)
}

// logContextStep does the same as logAPIStep but for code paths that only have
// a context and not a Fiber request.
func logContextStep(ctx context.Context, component, route, step, title string, fields map[string]any) {
	log.Print(observability.FormatStepLine(component, route, step, observability.TraceIDFromContext(ctx), observability.DefaultTraceID, observability.DefaultTraceID, title, fields))
	backenderrors.RecordLog(ctx, component, route, step, title, fields)
}
