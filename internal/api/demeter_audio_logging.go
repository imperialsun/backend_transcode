package api

import (
	"context"
	"log"
	"strings"

	"demeter-backend/internal/backenderrors"
	"demeter-backend/internal/backendperformance"
	"demeter-backend/internal/observability"
	"demeter-backend/internal/requestmeta"

	"github.com/gofiber/fiber/v2"
)

// demeterAudioLogContext bundles the request context and actor identity used
// by the Demeter audio logging helpers.
type demeterAudioLogContext struct {
	ctx     context.Context
	traceID string
	userID  string
	orgID   string
}

// newDemeterAudioLogContextFromFiber builds the logging context from the active
// Fiber request.
func newDemeterAudioLogContextFromFiber(c *fiber.Ctx) demeterAudioLogContext {
	if c == nil {
		return demeterAudioLogContext{
			ctx:     context.Background(),
			traceID: observability.DefaultTraceID,
			userID:  observability.DefaultTraceID,
			orgID:   observability.DefaultTraceID,
		}
	}
	userID, orgID := requestActorIDs(c)
	return demeterAudioLogContext{
		ctx:     requestContext(c),
		traceID: requestTraceID(c),
		userID:  normalizeDemeterLogID(userID),
		orgID:   normalizeDemeterLogID(orgID),
	}
}

// newDemeterAudioLogContext builds the logging context from a bare context.
func newDemeterAudioLogContext(ctx context.Context) demeterAudioLogContext {
	traceID := observability.TraceIDFromContext(ctx)
	userID, orgID := observability.DefaultTraceID, observability.DefaultTraceID
	if actorUserID, actorOrgID, ok := requestmeta.ActorFromContext(ctx); ok {
		userID = normalizeDemeterLogID(actorUserID)
		orgID = normalizeDemeterLogID(actorOrgID)
	}
	return demeterAudioLogContext{
		ctx:     ctx,
		traceID: normalizeDemeterLogID(traceID),
		userID:  userID,
		orgID:   orgID,
	}
}

// logDemeterAudioStageCtx records a structured Demeter audio event.
func logDemeterAudioStageCtx(logCtx demeterAudioLogContext, route string, seq uint64, stage string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["seq"] = seq
	log.Print(observability.FormatStepLine("demeter", route, stage, logCtx.traceID, logCtx.userID, logCtx.orgID, "demeter_audio_transcription", fields))
	backenderrors.RecordLog(logCtx.ctx, "demeter", route, stage, "demeter_audio_transcription", fields)
}

// logDemeterAudioPerformanceTaskCtx records a Demeter audio performance event.
func logDemeterAudioPerformanceTaskCtx(logCtx demeterAudioLogContext, route string, seq uint64, stage, task string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["seq"] = seq
	log.Print(observability.FormatStepLine("demeter", route, stage, logCtx.traceID, logCtx.userID, logCtx.orgID, task, fields))
	backendperformance.RecordLog(logCtx.ctx, "demeter", route, stage, task, fields)
}

// logDemeterAudioBackendErrorTaskCtx records a backend-error event for the
// Demeter audio path.
func logDemeterAudioBackendErrorTaskCtx(logCtx demeterAudioLogContext, route string, seq uint64, stage string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["seq"] = seq
	log.Print(observability.FormatStepLine("demeter", route, stage, logCtx.traceID, logCtx.userID, logCtx.orgID, "erreur_backend", fields))
	backenderrors.RecordLog(logCtx.ctx, "demeter", route, stage, "erreur_backend", fields)
}

// logDemeterRelayIssueCtx records a top-level relay problem for the current
// request.
func logDemeterRelayIssueCtx(logCtx demeterAudioLogContext, route string, status int, message string) {
	logDemeterAudioStageCtx(logCtx, route, 0, "relay_issue", map[string]any{
		"status":  status,
		"message": message,
	})
}

// logDemeterUpstreamStatusCtx records upstream failures when the proxied
// service returns an error status.
func logDemeterUpstreamStatusCtx(logCtx demeterAudioLogContext, route string, status int) {
	if status < fiber.StatusBadRequest {
		return
	}
	logDemeterAudioStageCtx(logCtx, route, 0, "upstream_error", map[string]any{
		"status": status,
	})
}

// logDemeterOwnershipStageCtx ensures ownership diagnostics always include the
// requesting user and organization.
func logDemeterOwnershipStageCtx(logCtx demeterAudioLogContext, route string, seq uint64, step string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	if _, ok := fields["request_user_id"]; !ok {
		fields["request_user_id"] = logCtx.userID
	}
	if _, ok := fields["request_org_id"]; !ok {
		fields["request_org_id"] = logCtx.orgID
	}
	logDemeterAudioStageCtx(logCtx, route, seq, step, fields)
}

// normalizeDemeterLogID keeps empty actor fields readable in structured logs.
func normalizeDemeterLogID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return observability.DefaultTraceID
	}
	return value
}
