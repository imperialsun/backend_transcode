package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// DemeterReportOperationStatus* constants track the lifecycle of a
// backend report job.
const (
	DemeterReportOperationStatusPending   = "pending"
	DemeterReportOperationStatusRunning   = "running"
	DemeterReportOperationStatusCompleted = "completed"
	DemeterReportOperationStatusFailed    = "failed"
	DemeterReportOperationStatusCancelled = "cancelled"
	demeterReportOperationRetention       = 24 * time.Hour
)

// ErrDemeterReportOperationOwnership reports that the caller tried
// to access someone else's upload session.
var ErrDemeterReportOperationOwnership = errors.New("demeter report report operation owned by another user")

// DemeterReportOperationRecord stores the state of one backend
// report report job.
type DemeterReportOperationRecord struct {
	OperationID      string
	OrganizationID   string
	UserID           string
	QueueID          int
	Status           string
	Stage            string
	FormatIndex      int
	FormatCount      int
	Progress         float64
	QueuePayloadJSON sql.NullString
	ResponseJSON     sql.NullString
	LastError        sql.NullString
	StatusCode       int
	CreatedAt        time.Time
	UpdatedAt        time.Time
	FinishedAt       sql.NullTime
}

// CreateDemeterReportOperation inserts a new job record.
func (s *Store) CreateDemeterReportOperation(ctx context.Context, record *DemeterReportOperationRecord) error {
	if record == nil {
		return fmt.Errorf("record is required")
	}
	record.OperationID = strings.TrimSpace(record.OperationID)
	record.OrganizationID = strings.TrimSpace(record.OrganizationID)
	record.UserID = strings.TrimSpace(record.UserID)
	record.Status = strings.TrimSpace(record.Status)
	record.Stage = strings.TrimSpace(record.Stage)
	record.CreatedAt = record.CreatedAt.UTC()
	record.UpdatedAt = record.UpdatedAt.UTC()
	if record.OperationID == "" || record.OrganizationID == "" || record.UserID == "" {
		return fmt.Errorf("operation_id, organization_id and user_id are required")
	}
	if record.Status == "" {
		record.Status = DemeterReportOperationStatusPending
	}
	if record.Stage == "" {
		record.Stage = "queued"
	}

	logStoreStep(ctx, "demeter_create_start", "demeter_report_report_operation", map[string]any{
		"operation_id":    record.OperationID,
		"organization_id": record.OrganizationID,
		"user_id":         record.UserID,
		"status":          record.Status,
		"stage":           record.Stage,
		"format_index":    record.FormatIndex,
		"format_count":    record.FormatCount,
	})

	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO demeter_report_operations(
			operation_id,
			organization_id,
			user_id,
			queue_id,
			queue_payload_json,
			status,
			stage,
			format_index,
			format_count,
			progress,
			response_json,
			last_error,
			status_code,
			created_at,
			updated_at,
			finished_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, record.OperationID, record.OrganizationID, record.UserID, record.QueueID, nullStringValue(record.QueuePayloadJSON), record.Status, record.Stage, record.FormatIndex, record.FormatCount, record.Progress, nullStringValue(record.ResponseJSON), nullStringValue(record.LastError), record.StatusCode, record.CreatedAt, record.UpdatedAt, nullTimeValue(record.FinishedAt))
	if err != nil {
		logStoreStep(ctx, "demeter_create_error", "demeter_report_report_operation", map[string]any{
			"operation_id": record.OperationID,
			"error":        err,
		})
		return err
	}

	logStoreStep(ctx, "demeter_create_success", "demeter_report_report_operation", map[string]any{
		"operation_id": record.OperationID,
		"status":       record.Status,
		"stage":        record.Stage,
	})
	return nil
}

// UpdateDemeterReportOperation updates a record that is scoped to a
// specific organization and user.
func (s *Store) UpdateDemeterReportOperation(ctx context.Context, record *DemeterReportOperationRecord) error {
	if record == nil {
		return fmt.Errorf("record is required")
	}
	record.OperationID = strings.TrimSpace(record.OperationID)
	record.OrganizationID = strings.TrimSpace(record.OrganizationID)
	record.UserID = strings.TrimSpace(record.UserID)
	record.Status = strings.TrimSpace(record.Status)
	record.Stage = strings.TrimSpace(record.Stage)
	record.UpdatedAt = record.UpdatedAt.UTC()
	if record.OperationID == "" || record.OrganizationID == "" || record.UserID == "" {
		return fmt.Errorf("operation_id, organization_id and user_id are required")
	}

	logStoreStep(ctx, "demeter_update_start", "demeter_report_report_operation", map[string]any{
		"operation_id": record.OperationID,
		"status":       record.Status,
		"stage":        record.Stage,
		"queue_id":     record.QueueID,
		"format_index": record.FormatIndex,
		"format_count": record.FormatCount,
	})

	res, err := s.DB.ExecContext(ctx, `
		UPDATE demeter_report_operations
		SET status = ?, stage = ?, format_index = ?, format_count = ?, progress = ?, response_json = ?, last_error = ?, status_code = ?, updated_at = ?, finished_at = ?
		WHERE operation_id = ? AND organization_id = ? AND user_id = ?
	`, record.Status, record.Stage, record.FormatIndex, record.FormatCount, record.Progress, nullStringValue(record.ResponseJSON), nullStringValue(record.LastError), record.StatusCode, record.UpdatedAt, nullTimeValue(record.FinishedAt), record.OperationID, record.OrganizationID, record.UserID)
	if err != nil {
		logStoreStep(ctx, "demeter_update_error", "demeter_report_report_operation", map[string]any{
			"operation_id": record.OperationID,
			"error":        err,
		})
		return err
	}

	rows, _ := res.RowsAffected()
	if rows == 0 {
		stored, peekErr := s.peekDemeterReportOperation(ctx, record.OperationID)
		if peekErr != nil && !errors.Is(peekErr, sql.ErrNoRows) {
			logStoreStep(ctx, "ownership_update_error", "demeter_report_report_operation", map[string]any{
				"operation_id":    record.OperationID,
				"request_org_id":  record.OrganizationID,
				"request_user_id": record.UserID,
				"source":          "store_update",
				"reason":          "peek_error",
				"error":           peekErr,
			})
			return peekErr
		}

		reason := "ownership_mismatch"
		if stored == nil {
			reason = "record_not_found"
		}
		ownershipErr := newDemeterReportOperationOwnershipError("store_update", reason, record.OperationID, record.OrganizationID, record.UserID, stored)
		logStoreStep(ctx, "ownership_update_error", "demeter_report_report_operation", ownershipErr.LogFields())
		return ownershipErr
	}

	logStoreStep(ctx, "demeter_update_success", "demeter_report_report_operation", map[string]any{
		"operation_id": record.OperationID,
		"status":       record.Status,
		"stage":        record.Stage,
		"format_index": record.FormatIndex,
		"format_count": record.FormatCount,
	})
	return nil
}

// UpdateDemeterReportOperationByID updates a record by primary key
// only and is used as a fallback when ownership has already been checked.
func (s *Store) UpdateDemeterReportOperationByID(ctx context.Context, record *DemeterReportOperationRecord) error {
	if record == nil {
		return fmt.Errorf("record is required")
	}
	record.OperationID = strings.TrimSpace(record.OperationID)
	record.Status = strings.TrimSpace(record.Status)
	record.Stage = strings.TrimSpace(record.Stage)
	record.UpdatedAt = record.UpdatedAt.UTC()
	if record.OperationID == "" {
		return fmt.Errorf("operation_id is required")
	}

	logStoreStep(ctx, "demeter_update_start", "demeter_report_report_operation", map[string]any{
		"operation_id": record.OperationID,
		"status":       record.Status,
		"stage":        record.Stage,
		"queue_id":     record.QueueID,
		"format_index": record.FormatIndex,
		"format_count": record.FormatCount,
	})

	res, err := s.DB.ExecContext(ctx, `
		UPDATE demeter_report_operations
		SET status = ?, stage = ?, format_index = ?, format_count = ?, progress = ?, response_json = ?, last_error = ?, status_code = ?, updated_at = ?, finished_at = ?
		WHERE operation_id = ?
	`, record.Status, record.Stage, record.FormatIndex, record.FormatCount, record.Progress, nullStringValue(record.ResponseJSON), nullStringValue(record.LastError), record.StatusCode, record.UpdatedAt, nullTimeValue(record.FinishedAt), record.OperationID)
	if err != nil {
		logStoreStep(ctx, "demeter_update_error", "demeter_report_report_operation", map[string]any{
			"operation_id": record.OperationID,
			"error":        err,
		})
		return err
	}

	rows, _ := res.RowsAffected()
	if rows == 0 {
		logStoreStep(ctx, "demeter_update_error", "demeter_report_report_operation", map[string]any{
			"operation_id": record.OperationID,
			"error":        sql.ErrNoRows,
		})
		return sql.ErrNoRows
	}

	logStoreStep(ctx, "demeter_update_success", "demeter_report_report_operation", map[string]any{
		"operation_id": record.OperationID,
		"status":       record.Status,
		"stage":        record.Stage,
		"queue_id":     record.QueueID,
		"format_index": record.FormatIndex,
		"format_count": record.FormatCount,
	})
	return nil
}

// GetDemeterReportOperation loads the current job record and
// validates ownership before returning it.
func (s *Store) GetDemeterReportOperation(ctx context.Context, operationID, organizationID, userID string) (*DemeterReportOperationRecord, error) {
	operationID = strings.TrimSpace(operationID)
	organizationID = strings.TrimSpace(organizationID)
	userID = strings.TrimSpace(userID)
	if operationID == "" || organizationID == "" || userID == "" {
		return nil, fmt.Errorf("operation_id, organization_id and user_id are required")
	}

	logStoreStep(ctx, "demeter_get_start", "demeter_report_report_operation", map[string]any{
		"operation_id":    operationID,
		"organization_id": organizationID,
		"user_id":         userID,
	})

	row := s.DB.QueryRowContext(ctx, `
		SELECT operation_id, organization_id, user_id, queue_id, queue_payload_json, status, stage, format_index, format_count, progress, response_json, last_error, status_code, created_at, updated_at, finished_at
		FROM demeter_report_operations
		WHERE operation_id = ?
	`, operationID)

	record, err := scanDemeterReportOperationRecord(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			logStoreStep(ctx, "demeter_get_missing", "demeter_report_report_operation", map[string]any{
				"operation_id": operationID,
			})
			return nil, sql.ErrNoRows
		}
		logStoreStep(ctx, "demeter_get_error", "demeter_report_report_operation", map[string]any{
			"operation_id": operationID,
			"error":        err,
		})
		return nil, err
	}

	if record.OrganizationID != organizationID || record.UserID != userID {
		ownershipErr := newDemeterReportOperationOwnershipError("store_get", "ownership_mismatch", operationID, organizationID, userID, record)
		logStoreStep(ctx, "ownership_mismatch_error", "demeter_report_report_operation", ownershipErr.LogFields())
		return nil, ownershipErr
	}

	logStoreStep(ctx, "demeter_get_success", "demeter_report_report_operation", map[string]any{
		"operation_id": operationID,
		"status":       record.Status,
		"stage":        record.Stage,
		"format_index": record.FormatIndex,
		"format_count": record.FormatCount,
	})
	return record, nil
}

// CancelDemeterReportOperation marks a running job as cancelled.
func (s *Store) CancelDemeterReportOperation(ctx context.Context, operationID, organizationID, userID string, now time.Time) (*DemeterReportOperationRecord, error) {
	now = now.UTC()
	record := &DemeterReportOperationRecord{
		OperationID:    strings.TrimSpace(operationID),
		OrganizationID: strings.TrimSpace(organizationID),
		UserID:         strings.TrimSpace(userID),
		Status:         DemeterReportOperationStatusCancelled,
		Stage:          "cancelled",
		UpdatedAt:      now,
		FinishedAt:     sql.NullTime{Time: now, Valid: true},
	}
	if record.OperationID == "" || record.OrganizationID == "" || record.UserID == "" {
		return nil, fmt.Errorf("operation_id, organization_id and user_id are required")
	}

	existing, err := s.GetDemeterReportOperation(ctx, record.OperationID, record.OrganizationID, record.UserID)
	if err != nil {
		var ownershipErr *DemeterReportOperationOwnershipError
		if errors.As(err, &ownershipErr) {
			logStoreStep(ctx, "ownership_cancel_error", "demeter_report_report_operation", ownershipErr.WithSource("store_cancel").LogFields())
		}
		return nil, err
	}
	if existing.Status == DemeterReportOperationStatusCompleted || existing.Status == DemeterReportOperationStatusFailed || existing.Status == DemeterReportOperationStatusCancelled {
		return existing, nil
	}

	record.FormatIndex = existing.FormatIndex
	record.FormatCount = existing.FormatCount
	record.Progress = existing.Progress
	record.ResponseJSON = existing.ResponseJSON
	record.LastError = sql.NullString{String: "operation cancelled", Valid: true}
	record.StatusCode = http.StatusRequestTimeout

	if err := s.UpdateDemeterReportOperation(ctx, record); err != nil {
		var ownershipErr *DemeterReportOperationOwnershipError
		if errors.As(err, &ownershipErr) {
			logStoreStep(ctx, "ownership_cancel_error", "demeter_report_report_operation", ownershipErr.WithSource("store_cancel").LogFields())
		}
		return nil, err
	}

	return s.GetDemeterReportOperation(ctx, record.OperationID, record.OrganizationID, record.UserID)
}

// UpdateDemeterReportOperationQueueByID updates the queue
// assignment for one report by primary key only.
func (s *Store) UpdateDemeterReportOperationQueueByID(ctx context.Context, operationID string, queueID int, updatedAt time.Time) error {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return fmt.Errorf("operation_id is required")
	}
	updatedAt = updatedAt.UTC()

	logStoreStep(ctx, "demeter_update_start", "demeter_report_report_operation", map[string]any{
		"operation_id": operationID,
		"queue_id":     queueID,
	})

	res, err := s.DB.ExecContext(ctx, `
		UPDATE demeter_report_operations
		SET queue_id = ?, updated_at = ?
		WHERE operation_id = ?
	`, queueID, updatedAt, operationID)
	if err != nil {
		logStoreStep(ctx, "demeter_update_error", "demeter_report_report_operation", map[string]any{
			"operation_id": operationID,
			"queue_id":     queueID,
			"error":        err,
		})
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		logStoreStep(ctx, "demeter_update_error", "demeter_report_report_operation", map[string]any{
			"operation_id": operationID,
			"queue_id":     queueID,
			"error":        sql.ErrNoRows,
		})
		return sql.ErrNoRows
	}

	logStoreStep(ctx, "demeter_update_success", "demeter_report_report_operation", map[string]any{
		"operation_id": operationID,
		"queue_id":     queueID,
	})
	return nil
}

// UpdateDemeterReportOperationQueuePayloadByID updates the stored
// queue payload for one report by primary key only.
func (s *Store) UpdateDemeterReportOperationQueuePayloadByID(ctx context.Context, operationID string, queuePayloadJSON string, updatedAt time.Time) error {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return fmt.Errorf("operation_id is required")
	}
	updatedAt = updatedAt.UTC()
	queuePayloadJSON = strings.TrimSpace(queuePayloadJSON)
	if queuePayloadJSON == "" {
		queuePayloadJSON = "{}"
	}

	logStoreStep(ctx, "demeter_update_start", "demeter_report_report_operation", map[string]any{
		"operation_id": operationID,
	})

	res, err := s.DB.ExecContext(ctx, `
		UPDATE demeter_report_operations
		SET queue_payload_json = ?, updated_at = ?
		WHERE operation_id = ?
	`, queuePayloadJSON, updatedAt, operationID)
	if err != nil {
		logStoreStep(ctx, "demeter_update_error", "demeter_report_report_operation", map[string]any{
			"operation_id": operationID,
			"error":        err,
		})
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		logStoreStep(ctx, "demeter_update_error", "demeter_report_report_operation", map[string]any{
			"operation_id": operationID,
			"error":        sql.ErrNoRows,
		})
		return sql.ErrNoRows
	}

	logStoreStep(ctx, "demeter_update_success", "demeter_report_report_operation", map[string]any{
		"operation_id": operationID,
	})
	return nil
}

// DeleteDemeterReportOperation removes one report job by
// its primary key.
func (s *Store) DeleteDemeterReportOperation(ctx context.Context, operationID string) (int64, error) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return 0, fmt.Errorf("operation_id is required")
	}

	logStoreStep(ctx, "demeter_delete_start", "demeter_report_report_operation", map[string]any{
		"operation_id": operationID,
	})

	result, err := s.DB.ExecContext(ctx, `
		DELETE FROM demeter_report_operations
		WHERE operation_id = ?
	`, operationID)
	if err != nil {
		logStoreStep(ctx, "demeter_delete_error", "demeter_report_report_operation", map[string]any{
			"operation_id": operationID,
			"error":        err,
		})
		return 0, err
	}

	deleted, err := result.RowsAffected()
	if err != nil {
		logStoreStep(ctx, "demeter_delete_error", "demeter_report_report_operation", map[string]any{
			"operation_id": operationID,
			"error":        err,
		})
		return 0, err
	}

	logStoreStep(ctx, "demeter_delete_success", "demeter_report_report_operation", map[string]any{
		"operation_id":  operationID,
		"deleted_count": deleted,
	})
	return deleted, nil
}

// PurgeCompletedDemeterReportOperations removes every completed
// report job left in the database.
func (s *Store) PurgeCompletedDemeterReportOperations(ctx context.Context) (int64, error) {
	logStoreStep(ctx, "demeter_purge_start", "demeter_report_report_operation", map[string]any{
		"status": DemeterReportOperationStatusCompleted,
	})

	result, err := s.DB.ExecContext(ctx, `
		DELETE FROM demeter_report_operations
		WHERE status = ?
	`, DemeterReportOperationStatusCompleted)
	if err != nil {
		logStoreStep(ctx, "demeter_purge_error", "demeter_report_report_operation", map[string]any{
			"status": DemeterReportOperationStatusCompleted,
			"error":  err,
		})
		return 0, err
	}

	deleted, err := result.RowsAffected()
	if err != nil {
		logStoreStep(ctx, "demeter_purge_error", "demeter_report_report_operation", map[string]any{
			"status": DemeterReportOperationStatusCompleted,
			"error":  err,
		})
		return 0, err
	}

	logStoreStep(ctx, "demeter_purge_success", "demeter_report_report_operation", map[string]any{
		"status":        DemeterReportOperationStatusCompleted,
		"deleted_count": deleted,
	})
	return deleted, nil
}

// PurgeAllDemeterReportOperations removes every report job left in the
// database.
func (s *Store) PurgeAllDemeterReportOperations(ctx context.Context) (int64, error) {
	logStoreStep(ctx, "demeter_purge_start", "demeter_report_report_operation", map[string]any{
		"scope": "all",
	})

	result, err := s.DB.ExecContext(ctx, `
		DELETE FROM demeter_report_operations
	`)
	if err != nil {
		logStoreStep(ctx, "demeter_purge_error", "demeter_report_report_operation", map[string]any{
			"scope": "all",
			"error": err,
		})
		return 0, err
	}

	deleted, err := result.RowsAffected()
	if err != nil {
		logStoreStep(ctx, "demeter_purge_error", "demeter_report_report_operation", map[string]any{
			"scope": "all",
			"error": err,
		})
		return 0, err
	}

	logStoreStep(ctx, "demeter_purge_success", "demeter_report_report_operation", map[string]any{
		"scope":         "all",
		"deleted_count": deleted,
	})
	return deleted, nil
}

// scanDemeterReportOperationRecord converts one SQL row into the
// strongly typed record used by the API layer.
func scanDemeterReportOperationRecord(row *sql.Row) (*DemeterReportOperationRecord, error) {
	var record DemeterReportOperationRecord
	if err := row.Scan(
		&record.OperationID,
		&record.OrganizationID,
		&record.UserID,
		&record.QueueID,
		&record.QueuePayloadJSON,
		&record.Status,
		&record.Stage,
		&record.FormatIndex,
		&record.FormatCount,
		&record.Progress,
		&record.ResponseJSON,
		&record.LastError,
		&record.StatusCode,
		&record.CreatedAt,
		&record.UpdatedAt,
		&record.FinishedAt,
	); err != nil {
		return nil, err
	}
	return &record, nil
}
