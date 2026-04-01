package api

import (
	"context"
	"log"
	"strings"

	"demeter-backend/internal/backenderrors"
	"demeter-backend/internal/observability"
	"demeter-backend/internal/requestmeta"

	"github.com/gofiber/fiber/v2"
)

type demeterAudioLogContext struct {
	ctx     context.Context
	traceID string
	userID  string
	orgID   string
}

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

func logDemeterAudioStageCtx(logCtx demeterAudioLogContext, route string, seq uint64, stage string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["seq"] = seq
	log.Print(observability.FormatStepLine("demeter", route, stage, logCtx.traceID, logCtx.userID, logCtx.orgID, "audio_transcription", fields))
	backenderrors.RecordLog(logCtx.ctx, "demeter", route, stage, "audio_transcription", fields)
}

func logDemeterRelayIssueCtx(logCtx demeterAudioLogContext, route string, status int, message string) {
	logDemeterAudioStageCtx(logCtx, route, 0, "relay_issue", map[string]any{
		"status":  status,
		"message": message,
	})
}

func logDemeterUpstreamStatusCtx(logCtx demeterAudioLogContext, route string, status int) {
	if status < fiber.StatusBadRequest {
		return
	}
	logDemeterAudioStageCtx(logCtx, route, 0, "upstream_error", map[string]any{
		"status": status,
	})
}

func normalizeDemeterLogID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return observability.DefaultTraceID
	}
	return value
}
