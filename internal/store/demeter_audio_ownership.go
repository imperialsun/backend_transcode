package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// DemeterAudioTranscriptionOperationOwnershipError reports that a request tried
// to access a transcription job owned by another user or organization.
type DemeterAudioTranscriptionOperationOwnershipError struct {
	OperationID           string
	RequestOrganizationID string
	RequestUserID         string
	StoredOrganizationID  string
	StoredUserID          string
	Reason                string
	Source                string
}

// Error returns the canonical ownership violation message.
func (e *DemeterAudioTranscriptionOperationOwnershipError) Error() string {
	return ErrDemeterAudioTranscriptionOperationOwnership.Error()
}

// Is lets callers compare the error against the shared ownership sentinel.
func (e *DemeterAudioTranscriptionOperationOwnershipError) Is(target error) bool {
	return target == ErrDemeterAudioTranscriptionOperationOwnership
}

// WithSource returns a copy that records where the ownership check failed.
func (e *DemeterAudioTranscriptionOperationOwnershipError) WithSource(source string) *DemeterAudioTranscriptionOperationOwnershipError {
	if e == nil {
		return nil
	}
	clone := *e
	clone.Source = strings.TrimSpace(source)
	if clone.Source == "" {
		clone.Source = e.Source
	}
	return &clone
}

// LogFields serializes the ownership error into the shape expected by the
// structured logging pipeline.
func (e *DemeterAudioTranscriptionOperationOwnershipError) LogFields() map[string]any {
	if e == nil {
		return map[string]any{}
	}
	fields := map[string]any{
		"operation_id":    strings.TrimSpace(e.OperationID),
		"request_org_id":  strings.TrimSpace(e.RequestOrganizationID),
		"request_user_id": strings.TrimSpace(e.RequestUserID),
		"reason":          strings.TrimSpace(e.Reason),
		"source":          strings.TrimSpace(e.Source),
	}
	if strings.TrimSpace(e.StoredOrganizationID) != "" {
		fields["stored_org_id"] = strings.TrimSpace(e.StoredOrganizationID)
	}
	if strings.TrimSpace(e.StoredUserID) != "" {
		fields["stored_user_id"] = strings.TrimSpace(e.StoredUserID)
	}
	return fields
}

// newDemeterAudioTranscriptionOperationOwnershipError builds the ownership
// violation with the stored actor and record state for diagnostics.
func newDemeterAudioTranscriptionOperationOwnershipError(
	source string,
	reason string,
	operationID string,
	requestOrganizationID string,
	requestUserID string,
	stored *DemeterAudioTranscriptionOperationRecord,
) *DemeterAudioTranscriptionOperationOwnershipError {
	err := &DemeterAudioTranscriptionOperationOwnershipError{
		OperationID:           strings.TrimSpace(operationID),
		RequestOrganizationID: strings.TrimSpace(requestOrganizationID),
		RequestUserID:         strings.TrimSpace(requestUserID),
		Reason:                strings.TrimSpace(reason),
		Source:                strings.TrimSpace(source),
	}
	if err.Reason == "" {
		err.Reason = "ownership_mismatch"
	}
	if err.Source == "" {
		err.Source = "store"
	}
	if stored != nil {
		err.StoredOrganizationID = strings.TrimSpace(stored.OrganizationID)
		err.StoredUserID = strings.TrimSpace(stored.UserID)
	}
	return err
}

// peekDemeterAudioTranscriptionOperation loads a transcription job without any
// ownership enforcement so callers can produce a better mismatch error.
func (s *Store) peekDemeterAudioTranscriptionOperation(ctx context.Context, operationID string) (*DemeterAudioTranscriptionOperationRecord, error) {
	if s == nil {
		return nil, errors.New("store is not configured")
	}
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return nil, sql.ErrNoRows
	}

	row := s.DB.QueryRowContext(ctx, `
		SELECT operation_id, organization_id, user_id, status, stage, chunk_index, chunk_count, progress, partial_text, response_json, last_error, status_code, created_at, updated_at, finished_at
		FROM demeter_audio_transcription_operations
		WHERE operation_id = ?
	`, operationID)

	return scanDemeterAudioTranscriptionOperationRecord(row)
}
