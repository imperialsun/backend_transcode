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

func (a *App) RequestTrace() fiber.Handler {
	return func(c *fiber.Ctx) error {
		ensureRequestTraceID(c)
		return c.Next()
	}
}

func ensureRequestTraceID(c *fiber.Ctx) string {
	if c == nil {
		return observability.NewTraceID()
	}

	if traceID, ok := c.Locals(traceIDLocalsKey).(string); ok {
		if normalized := observability.NormalizeTraceID(traceID); normalized != "" {
			traceID = normalized
			if ctx := c.UserContext(); ctx != nil {
				c.SetUserContext(observability.WithTraceID(ctx, traceID))
			} else {
				c.SetUserContext(observability.WithTraceID(context.Background(), traceID))
			}
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

	ctx := c.UserContext()
	if ctx == nil {
		ctx = context.Background()
	}
	c.SetUserContext(observability.WithTraceID(ctx, traceID))
	c.Locals(traceIDLocalsKey, traceID)
	return traceID
}

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
		return traceID
	}

	return ensureRequestTraceID(c)
}

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

func requestActorIDs(c *fiber.Ctx) (string, string) {
	return claimsActorIDs(MustClaims(c))
}

func claimsActorIDs(claims *auth.Claims) (string, string) {
	if claims == nil {
		return observability.DefaultTraceID, observability.DefaultTraceID
	}
	userID := strings.TrimSpace(claims.UserID)
	orgID := strings.TrimSpace(claims.OrgID)
	if userID == "" {
		userID = observability.DefaultTraceID
	}
	if orgID == "" {
		orgID = observability.DefaultTraceID
	}
	return userID, orgID
}

func logAPIStep(c *fiber.Ctx, component, route, step, title string, fields map[string]any) {
	userID, orgID := requestActorIDs(c)
	ctx := requestContext(c)
	log.Print(observability.FormatStepLine(component, route, step, requestTraceID(c), userID, orgID, title, fields))
	backenderrors.RecordLog(ctx, component, route, step, title, fields)
}

func logContextStep(ctx context.Context, component, route, step, title string, fields map[string]any) {
	log.Print(observability.FormatStepLine(component, route, step, observability.TraceIDFromContext(ctx), observability.DefaultTraceID, observability.DefaultTraceID, title, fields))
	backenderrors.RecordLog(ctx, component, route, step, title, fields)
}
