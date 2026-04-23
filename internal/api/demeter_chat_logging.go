package api

import (
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"demeter-backend/internal/backenderrors"
	"demeter-backend/internal/backendperformance"
	"demeter-backend/internal/observability"

	"github.com/gofiber/fiber/v2"
)

type demeterChatLogContext = demeterAudioLogContext

var demeterChatCompletionsSequenceCounter uint64

const demeterChatCompletionsMaxAttempts = demeterAudioTranscriptionMaxAttempts

var demeterChatCompletionsRetryDelayForAttempt = func(attempt int) time.Duration {
	return demeterAudioTranscriptionRetryDelayForAttempt(attempt)
}

func nextDemeterChatCompletionsSequenceID() uint64 {
	return atomic.AddUint64(&demeterChatCompletionsSequenceCounter, 1)
}

func newDemeterChatLogContextFromFiber(c *fiber.Ctx) demeterChatLogContext {
	return demeterChatLogContext(newDemeterAudioLogContextFromFiber(c))
}

func logDemeterChatStageCtx(logCtx demeterChatLogContext, route string, seq uint64, stage string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["seq"] = seq
	log.Print(observability.FormatStepLine("demeter", route, stage, logCtx.traceID, logCtx.userID, logCtx.orgID, "chat_completions", fields))
}

func logDemeterChatBackendErrorCtx(logCtx demeterChatLogContext, route string, seq uint64, stage string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["seq"] = seq
	log.Print(observability.FormatStepLine("demeter", route, stage, logCtx.traceID, logCtx.userID, logCtx.orgID, "demeter_report_generation", fields))
	backenderrors.RecordLog(logCtx.ctx, "demeter", route, stage, "demeter_report_generation", stripDemeterChatDurationFields(fields))
}

func logDemeterChatPerformanceTaskCtx(logCtx demeterChatLogContext, route string, seq uint64, stage string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["seq"] = seq
	log.Print(observability.FormatStepLine("demeter", route, stage, logCtx.traceID, logCtx.userID, logCtx.orgID, "demeter_report_generation", fields))
	backendperformance.RecordLog(logCtx.ctx, "demeter", route, stage, "demeter_report_generation", fields)
}

func stripDemeterChatDurationFields(fields map[string]any) map[string]any {
	if len(fields) == 0 {
		return map[string]any{}
	}

	out := make(map[string]any, len(fields))
	for key, value := range fields {
		out[key] = value
	}
	for _, key := range []string{"duration_ms", "total_duration_ms", "upstream_duration_ms", "elapsed_ms"} {
		delete(out, key)
	}
	return out
}

func logDemeterChatRelayIssueCtx(logCtx demeterChatLogContext, route string, status int, message string) {
	logDemeterChatStageCtx(logCtx, route, 0, "relay_issue", map[string]any{
		"status":  status,
		"message": message,
	})
}

func logDemeterChatUpstreamStatusCtx(logCtx demeterChatLogContext, route string, status int) {
	if status < fiber.StatusBadRequest {
		return
	}
	logDemeterChatBackendErrorCtx(logCtx, route, 0, "upstream_error", map[string]any{
		"status": status,
	})
}

func logDemeterChatStage(c *fiber.Ctx, route string, seq uint64, stage string, fields map[string]any) {
	logDemeterChatStageCtx(newDemeterChatLogContextFromFiber(c), route, seq, stage, fields)
}

func logDemeterChatRelayIssue(c *fiber.Ctx, route string, status int, message string) {
	logDemeterChatRelayIssueCtx(newDemeterChatLogContextFromFiber(c), route, status, message)
}

func logDemeterChatUpstreamStatus(c *fiber.Ctx, route string, status int) {
	logDemeterChatUpstreamStatusCtx(newDemeterChatLogContextFromFiber(c), route, status)
}

type demeterChatCompletionsRelayResult struct {
	statusCode         int
	responseBody       []byte
	upstreamDurationMs int64
	attempts           int
}

func shouldRetryDemeterChatCompletionsResponse(status int, responseBody []byte) bool {
	return shouldRetryDemeterAudioTranscriptionResponse(status, responseBody)
}

func (a *App) demeterChatCompletionsWithRetry(logCtx demeterChatLogContext, route string, seq uint64, requestBody []byte) (demeterChatCompletionsRelayResult, error) {
	ctx := logCtx.ctx
	requestBytes := len(requestBody)

	for attempt := 1; attempt <= demeterChatCompletionsMaxAttempts; attempt++ {
		logDemeterChatStageCtx(logCtx, route, seq, "upstream_send_start", map[string]any{
			"upstream":      demeterChatCompletionsUpstreamPath,
			"attempt":       attempt,
			"max_attempts":  demeterChatCompletionsMaxAttempts,
			"request_bytes": requestBytes,
		})

		upstreamStartedAt := time.Now()
		statusCode, responseBody, err := a.MistralClient.DoJSON(ctx, fiber.MethodPost, demeterChatCompletionsUpstreamPath, requestBody)
		upstreamDurationMs := time.Since(upstreamStartedAt).Milliseconds()
		if err != nil {
			return demeterChatCompletionsRelayResult{}, err
		}

		if shouldRetryDemeterChatCompletionsResponse(statusCode, responseBody) {
			delay := demeterChatCompletionsRetryDelayForAttempt(attempt)
			fields := map[string]any{
				"upstream":             demeterChatCompletionsUpstreamPath,
				"attempt":              attempt,
				"max_attempts":         demeterChatCompletionsMaxAttempts,
				"request_bytes":        requestBytes,
				"upstream_status":      statusCode,
				"upstream_duration_ms": upstreamDurationMs,
				"response_bytes":       len(responseBody),
				"retry_delay_ms":       delay.Milliseconds(),
				"reason":               demeterAudioTranscriptionRetryReason(statusCode),
			}
			if demeterAudioTranscriptionResponseIsCapacityExceeded(statusCode, responseBody) {
				fields["reason"] = demeterAudioTranscriptionCapacityErrorReason(statusCode)
				fields["message"] = strings.TrimSpace(string(responseBody))
				if attempt < demeterChatCompletionsMaxAttempts {
					fields["action"] = "retry"
					fields["next_attempt"] = attempt + 1
				} else {
					fields["action"] = "exhausted"
				}
				logDemeterChatBackendErrorCtx(logCtx, route, seq, "upstream_capacity_error", fields)
			} else if attempt < demeterChatCompletionsMaxAttempts {
				fields["next_attempt"] = attempt + 1
				logDemeterChatStageCtx(logCtx, route, seq, "upstream_retry", fields)
			}

			if attempt < demeterChatCompletionsMaxAttempts {
				if err := sleepContext(ctx, delay); err != nil {
					return demeterChatCompletionsRelayResult{}, err
				}
				continue
			}
		}

		logDemeterChatStageCtx(logCtx, route, seq, "upstream_response_received", map[string]any{
			"upstream":             demeterChatCompletionsUpstreamPath,
			"attempt":              attempt,
			"max_attempts":         demeterChatCompletionsMaxAttempts,
			"request_bytes":        requestBytes,
			"upstream_status":      statusCode,
			"upstream_duration_ms": upstreamDurationMs,
			"response_bytes":       len(responseBody),
		})

		return demeterChatCompletionsRelayResult{
			statusCode:         statusCode,
			responseBody:       responseBody,
			upstreamDurationMs: upstreamDurationMs,
			attempts:           attempt,
		}, nil
	}

	return demeterChatCompletionsRelayResult{}, fmt.Errorf("retry loop exhausted unexpectedly")
}
