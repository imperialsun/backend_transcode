package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// DemeterReportOperationOwnershipError reports that a request tried
// to access a transcription job owned by another user or organization.
type DemeterReportOperationOwnershipError struct {
	OperationID           string
	RequestOrganizationID string
	RequestUserID         string
	StoredOrganizationID  string
	StoredUserID          string
	Reason                string
	Source                string
}

// Error returns the canonical ownership violation message.
func (e *DemeterReportOperationOwnershipError) Error() string {
	return ErrDemeterReportOperationOwnership.Error()
}

// Is lets callers compare the error against the shared ownership sentinel.
func (e *DemeterReportOperationOwnershipError) Is(target error) bool {
	return target == ErrDemeterReportOperationOwnership
}

// WithSource returns a copy that records where the ownership check failed.
func (e *DemeterReportOperationOwnershipError) WithSource(source string) *DemeterReportOperationOwnershipError {
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
func (e *DemeterReportOperationOwnershipError) LogFields() map[string]any {
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

// newDemeterReportOperationOwnershipError builds the ownership
// violation with the stored actor and record state for diagnostics.
func newDemeterReportOperationOwnershipError(
	source string,
	reason string,
	operationID string,
	requestOrganizationID string,
	requestUserID string,
	stored *DemeterReportOperationRecord,
) *DemeterReportOperationOwnershipError {
	err := &DemeterReportOperationOwnershipError{
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

// peekDemeterReportOperation loads a transcription job without any
// ownership enforcement so callers can produce a better mismatch error.
func (s *Store) peekDemeterReportOperation(ctx context.Context, operationID string) (*DemeterReportOperationRecord, error) {
	if s == nil {
		return nil, errors.New("store is not configured")
	}
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return nil, sql.ErrNoRows
	}

	row := s.DB.QueryRowContext(ctx, `
		SELECT operation_id, organization_id, user_id, queue_id, queue_payload_json, status, stage, format_index, format_count, progress, response_json, last_error, status_code, created_at, updated_at, finished_at
		FROM demeter_report_operations
		WHERE operation_id = ?
	`, operationID)

	return scanDemeterReportOperationRecord(row)
}
