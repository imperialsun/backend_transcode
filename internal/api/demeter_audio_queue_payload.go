package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"demeter-backend/internal/store"
)

// buildDemeterAudioQueuePayload snapshots the prepared upload and chunk plan so
// the queue manager can process the transcription later.
func buildDemeterAudioQueuePayload(
	traceID string,
	route string,
	seq uint64,
	routeMode string,
	audioDurationSec float64,
	audioDurationProvided bool,
	requestBytes int,
	startedAt time.Time,
	upload *demeterBackendAudioUpload,
	chunkPlans []demeterBackendChunkPlan,
) (*demeterAudioQueueOperationPayload, error) {
	if upload == nil {
		return nil, fmt.Errorf("missing upload")
	}

	payloadUpload := *upload
	payloadUpload.cleanup = nil
	payloadChunkPlans := append([]demeterBackendChunkPlan(nil), chunkPlans...)

	return &demeterAudioQueueOperationPayload{
		TraceID:               strings.TrimSpace(traceID),
		Route:                 strings.TrimSpace(route),
		Seq:                   seq,
		RouteMode:             strings.TrimSpace(routeMode),
		AudioDurationSec:      audioDurationSec,
		AudioDurationProvided: audioDurationProvided,
		RequestBytes:          requestBytes,
		StartedAt:             startedAt.UTC(),
		Upload:                payloadUpload,
		ChunkPlans:            payloadChunkPlans,
	}, nil
}

func mustMarshalDemeterQueuePayload(payload *demeterAudioQueueOperationPayload) string {
	if payload == nil {
		return ""
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(raw)
}

// createAndEnqueueDemeterAudioTranscriptionOperation persists a queue payload
// and assigns it to the least loaded lane when one is available.
func (a *App) createAndEnqueueDemeterAudioTranscriptionOperation(
	ctx context.Context,
	record *store.DemeterAudioTranscriptionOperationRecord,
) (*store.DemeterAudioTranscriptionOperationRecord, error) {
	if a == nil || a.Store == nil {
		return nil, fmt.Errorf("store is not configured")
	}
	if record == nil {
		return nil, fmt.Errorf("record is required")
	}

	if err := a.Store.CreateDemeterAudioTranscriptionOperation(ctx, record); err != nil {
		return nil, err
	}

	queueManager := a.ensureDemeterQueueManager()
	if queueManager != nil {
		if _, err := queueManager.EnqueueOperation(ctx, record); err != nil {
			now := time.Now().UTC()
			_, _ = a.updateDemeterAudioTranscriptionOperationStateWithFallback(ctx, &store.DemeterAudioTranscriptionOperationRecord{
				OperationID:    record.OperationID,
				OrganizationID: record.OrganizationID,
				UserID:         record.UserID,
				Status:         store.DemeterAudioTranscriptionOperationStatusFailed,
				Stage:          "failed",
				ChunkIndex:     record.ChunkIndex,
				ChunkCount:     record.ChunkCount,
				Progress:       record.Progress,
				LastError:      sql.NullString{String: strings.TrimSpace(err.Error()), Valid: true},
				StatusCode:     500,
				UpdatedAt:      now,
				FinishedAt:     sql.NullTime{Time: now, Valid: true},
			})
			finalRecord, loadErr := a.Store.GetDemeterAudioTranscriptionOperation(ctx, record.OperationID, record.OrganizationID, record.UserID)
			if loadErr != nil {
				return nil, err
			}
			return finalRecord, err
		}
	}

	finalRecord, err := a.Store.GetDemeterAudioTranscriptionOperation(ctx, record.OperationID, record.OrganizationID, record.UserID)
	if err != nil {
		return record, err
	}
	return finalRecord, nil
}
