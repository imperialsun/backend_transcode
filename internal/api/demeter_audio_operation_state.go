package api

import (
	"context"
	"errors"

	"demeter-backend/internal/store"
)

// updateDemeterAudioTranscriptionOperationState updates the job state and
// ignores whether the fallback path was needed.
func (a *App) updateDemeterAudioTranscriptionOperationState(ctx context.Context, record *store.DemeterAudioTranscriptionOperationRecord) error {
	_, err := a.updateDemeterAudioTranscriptionOperationStateWithFallback(ctx, record)
	return err
}

// updateDemeterAudioTranscriptionOperationStateWithFallback retries the update
// by primary key when the scoped update reports an ownership mismatch.
func (a *App) updateDemeterAudioTranscriptionOperationStateWithFallback(ctx context.Context, record *store.DemeterAudioTranscriptionOperationRecord) (bool, error) {
	if a == nil || a.Store == nil {
		return false, errors.New("store is not configured")
	}
	if err := a.Store.UpdateDemeterAudioTranscriptionOperation(ctx, record); err != nil {
		var ownershipErr *store.DemeterAudioTranscriptionOperationOwnershipError
		if errors.As(err, &ownershipErr) || errors.Is(err, store.ErrDemeterAudioTranscriptionOperationOwnership) {
			logCtx := newDemeterAudioLogContext(ctx)
			if ownershipErr != nil {
				logDemeterAudioStageCtx(logCtx, "-", 0, "ownership_fallback_used_error", map[string]any{
					"operation_id":    ownershipErr.OperationID,
					"request_org_id":  ownershipErr.RequestOrganizationID,
					"request_user_id": ownershipErr.RequestUserID,
					"stored_org_id":   ownershipErr.StoredOrganizationID,
					"stored_user_id":  ownershipErr.StoredUserID,
					"reason":          ownershipErr.Reason,
					"source":          "store_update_fallback",
					"stage":           record.Stage,
					"status":          record.Status,
					"status_code":     record.StatusCode,
					"chunk_index":     record.ChunkIndex,
					"chunk_count":     record.ChunkCount,
				})
			} else {
				logDemeterAudioStageCtx(logCtx, "-", 0, "ownership_fallback_used_error", map[string]any{
					"operation_id":    record.OperationID,
					"request_org_id":  record.OrganizationID,
					"request_user_id": record.UserID,
					"reason":          "ownership_mismatch",
					"source":          "store_update_fallback",
					"stage":           record.Stage,
					"status":          record.Status,
					"status_code":     record.StatusCode,
					"chunk_index":     record.ChunkIndex,
					"chunk_count":     record.ChunkCount,
				})
			}
			if fallbackErr := a.Store.UpdateDemeterAudioTranscriptionOperationByID(ctx, record); fallbackErr == nil {
				return true, nil
			} else {
				return false, fallbackErr
			}
		}
		return false, err
	}
	return false, nil
}
