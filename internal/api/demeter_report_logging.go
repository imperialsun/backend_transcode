package api

import (
	"log"
	"sync/atomic"

	"demeter-backend/internal/backenderrors"
	"demeter-backend/internal/backendperformance"
	"demeter-backend/internal/observability"
)

const demeterReportGenerationUpstreamPath = "/v1/chat/completions"

var demeterReportOperationSequenceCounter uint64

func nextDemeterReportOperationSequenceID() uint64 {
	return atomic.AddUint64(&demeterReportOperationSequenceCounter, 1)
}

func logDemeterReportBackendErrorCtx(logCtx demeterAudioLogContext, route string, seq uint64, stage string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["seq"] = seq
	log.Print(observability.FormatStepLine("demeter", route, stage, logCtx.traceID, logCtx.userID, logCtx.orgID, "demeter_report_generation", fields))
	backenderrors.RecordLog(logCtx.ctx, "demeter", route, stage, "demeter_report_generation", stripDemeterReportDurationFields(fields))
}

func logDemeterReportPerformanceTaskCtx(logCtx demeterAudioLogContext, route string, seq uint64, stage string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["seq"] = seq
	log.Print(observability.FormatStepLine("demeter", route, stage, logCtx.traceID, logCtx.userID, logCtx.orgID, "demeter_report_generation", fields))
	backendperformance.RecordLog(logCtx.ctx, "demeter", route, stage, "demeter_report_generation", fields)
}

func stripDemeterReportDurationFields(fields map[string]any) map[string]any {
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
