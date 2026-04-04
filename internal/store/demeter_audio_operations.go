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

const (
	DemeterAudioTranscriptionOperationStatusPending   = "pending"
	DemeterAudioTranscriptionOperationStatusRunning   = "running"
	DemeterAudioTranscriptionOperationStatusCompleted = "completed"
	DemeterAudioTranscriptionOperationStatusFailed    = "failed"
	DemeterAudioTranscriptionOperationStatusCancelled = "cancelled"
	demeterAudioTranscriptionOperationRetention       = 24 * time.Hour
)

var ErrDemeterAudioTranscriptionOperationOwnership = errors.New("demeter audio transcription operation owned by another user")

type DemeterAudioTranscriptionOperationRecord struct {
	OperationID    string
	OrganizationID string
	UserID         string
	Status         string
	Stage          string
	ChunkIndex     int
	ChunkCount     int
	Progress       float64
	PartialText    sql.NullString
	ResponseJSON   sql.NullString
	LastError      sql.NullString
	StatusCode     int
	CreatedAt      time.Time
	UpdatedAt      time.Time
	FinishedAt     sql.NullTime
}

func (s *Store) CreateDemeterAudioTranscriptionOperation(ctx context.Context, record *DemeterAudioTranscriptionOperationRecord) error {
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
		record.Status = DemeterAudioTranscriptionOperationStatusPending
	}
	if record.Stage == "" {
		record.Stage = "queued"
	}

	logStoreStep(ctx, "demeter_create_start", "demeter_audio_transcription_operation", map[string]any{
		"operation_id":    record.OperationID,
		"organization_id": record.OrganizationID,
		"user_id":         record.UserID,
		"status":          record.Status,
		"stage":           record.Stage,
		"chunk_index":     record.ChunkIndex,
		"chunk_count":     record.ChunkCount,
	})

	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO demeter_audio_transcription_operations(
			operation_id,
			organization_id,
			user_id,
			status,
			stage,
			chunk_index,
			chunk_count,
			progress,
			partial_text,
			response_json,
			last_error,
			status_code,
			created_at,
			updated_at,
			finished_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, record.OperationID, record.OrganizationID, record.UserID, record.Status, record.Stage, record.ChunkIndex, record.ChunkCount, record.Progress, textValue(record.PartialText), nullStringValue(record.ResponseJSON), nullStringValue(record.LastError), record.StatusCode, record.CreatedAt, record.UpdatedAt, nullTimeValue(record.FinishedAt))
	if err != nil {
		logStoreStep(ctx, "demeter_create_error", "demeter_audio_transcription_operation", map[string]any{
			"operation_id": record.OperationID,
			"error":        err,
		})
		return err
	}

	logStoreStep(ctx, "demeter_create_success", "demeter_audio_transcription_operation", map[string]any{
		"operation_id": record.OperationID,
		"status":       record.Status,
		"stage":        record.Stage,
	})
	return nil
}

func (s *Store) UpdateDemeterAudioTranscriptionOperation(ctx context.Context, record *DemeterAudioTranscriptionOperationRecord) error {
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

	logStoreStep(ctx, "demeter_update_start", "demeter_audio_transcription_operation", map[string]any{
		"operation_id": record.OperationID,
		"status":       record.Status,
		"stage":        record.Stage,
		"chunk_index":  record.ChunkIndex,
		"chunk_count":  record.ChunkCount,
	})

	res, err := s.DB.ExecContext(ctx, `
		UPDATE demeter_audio_transcription_operations
		SET status = ?, stage = ?, chunk_index = ?, chunk_count = ?, progress = ?, partial_text = ?, response_json = ?, last_error = ?, status_code = ?, updated_at = ?, finished_at = ?
		WHERE operation_id = ? AND organization_id = ? AND user_id = ?
	`, record.Status, record.Stage, record.ChunkIndex, record.ChunkCount, record.Progress, textValue(record.PartialText), nullStringValue(record.ResponseJSON), nullStringValue(record.LastError), record.StatusCode, record.UpdatedAt, nullTimeValue(record.FinishedAt), record.OperationID, record.OrganizationID, record.UserID)
	if err != nil {
		logStoreStep(ctx, "demeter_update_error", "demeter_audio_transcription_operation", map[string]any{
			"operation_id": record.OperationID,
			"error":        err,
		})
		return err
	}

	rows, _ := res.RowsAffected()
	if rows == 0 {
		stored, peekErr := s.peekDemeterAudioTranscriptionOperation(ctx, record.OperationID)
		if peekErr != nil && !errors.Is(peekErr, sql.ErrNoRows) {
			logStoreStep(ctx, "ownership_update_error", "demeter_audio_transcription_operation", map[string]any{
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
		ownershipErr := newDemeterAudioTranscriptionOperationOwnershipError("store_update", reason, record.OperationID, record.OrganizationID, record.UserID, stored)
		logStoreStep(ctx, "ownership_update_error", "demeter_audio_transcription_operation", ownershipErr.LogFields())
		return ownershipErr
	}

	logStoreStep(ctx, "demeter_update_success", "demeter_audio_transcription_operation", map[string]any{
		"operation_id": record.OperationID,
		"status":       record.Status,
		"stage":        record.Stage,
		"chunk_index":  record.ChunkIndex,
		"chunk_count":  record.ChunkCount,
	})
	return nil
}

func (s *Store) UpdateDemeterAudioTranscriptionOperationByID(ctx context.Context, record *DemeterAudioTranscriptionOperationRecord) error {
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

	logStoreStep(ctx, "demeter_update_start", "demeter_audio_transcription_operation", map[string]any{
		"operation_id": record.OperationID,
		"status":       record.Status,
		"stage":        record.Stage,
		"chunk_index":  record.ChunkIndex,
		"chunk_count":  record.ChunkCount,
	})

	res, err := s.DB.ExecContext(ctx, `
		UPDATE demeter_audio_transcription_operations
		SET status = ?, stage = ?, chunk_index = ?, chunk_count = ?, progress = ?, partial_text = ?, response_json = ?, last_error = ?, status_code = ?, updated_at = ?, finished_at = ?
		WHERE operation_id = ?
	`, record.Status, record.Stage, record.ChunkIndex, record.ChunkCount, record.Progress, textValue(record.PartialText), nullStringValue(record.ResponseJSON), nullStringValue(record.LastError), record.StatusCode, record.UpdatedAt, nullTimeValue(record.FinishedAt), record.OperationID)
	if err != nil {
		logStoreStep(ctx, "demeter_update_error", "demeter_audio_transcription_operation", map[string]any{
			"operation_id": record.OperationID,
			"error":        err,
		})
		return err
	}

	rows, _ := res.RowsAffected()
	if rows == 0 {
		logStoreStep(ctx, "demeter_update_error", "demeter_audio_transcription_operation", map[string]any{
			"operation_id": record.OperationID,
			"error":        sql.ErrNoRows,
		})
		return sql.ErrNoRows
	}

	logStoreStep(ctx, "demeter_update_success", "demeter_audio_transcription_operation", map[string]any{
		"operation_id": record.OperationID,
		"status":       record.Status,
		"stage":        record.Stage,
		"chunk_index":  record.ChunkIndex,
		"chunk_count":  record.ChunkCount,
	})
	return nil
}

func (s *Store) GetDemeterAudioTranscriptionOperation(ctx context.Context, operationID, organizationID, userID string) (*DemeterAudioTranscriptionOperationRecord, error) {
	operationID = strings.TrimSpace(operationID)
	organizationID = strings.TrimSpace(organizationID)
	userID = strings.TrimSpace(userID)
	if operationID == "" || organizationID == "" || userID == "" {
		return nil, fmt.Errorf("operation_id, organization_id and user_id are required")
	}

	logStoreStep(ctx, "demeter_get_start", "demeter_audio_transcription_operation", map[string]any{
		"operation_id":    operationID,
		"organization_id": organizationID,
		"user_id":         userID,
	})

	row := s.DB.QueryRowContext(ctx, `
		SELECT operation_id, organization_id, user_id, status, stage, chunk_index, chunk_count, progress, partial_text, response_json, last_error, status_code, created_at, updated_at, finished_at
		FROM demeter_audio_transcription_operations
		WHERE operation_id = ?
	`, operationID)

	record, err := scanDemeterAudioTranscriptionOperationRecord(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			logStoreStep(ctx, "demeter_get_missing", "demeter_audio_transcription_operation", map[string]any{
				"operation_id": operationID,
			})
			return nil, sql.ErrNoRows
		}
		logStoreStep(ctx, "demeter_get_error", "demeter_audio_transcription_operation", map[string]any{
			"operation_id": operationID,
			"error":        err,
		})
		return nil, err
	}

	if record.OrganizationID != organizationID || record.UserID != userID {
		ownershipErr := newDemeterAudioTranscriptionOperationOwnershipError("store_get", "ownership_mismatch", operationID, organizationID, userID, record)
		logStoreStep(ctx, "ownership_mismatch_error", "demeter_audio_transcription_operation", ownershipErr.LogFields())
		return nil, ownershipErr
	}

	logStoreStep(ctx, "demeter_get_success", "demeter_audio_transcription_operation", map[string]any{
		"operation_id": operationID,
		"status":       record.Status,
		"stage":        record.Stage,
		"chunk_index":  record.ChunkIndex,
		"chunk_count":  record.ChunkCount,
	})
	return record, nil
}

func (s *Store) CancelDemeterAudioTranscriptionOperation(ctx context.Context, operationID, organizationID, userID string, now time.Time) (*DemeterAudioTranscriptionOperationRecord, error) {
	now = now.UTC()
	record := &DemeterAudioTranscriptionOperationRecord{
		OperationID:    strings.TrimSpace(operationID),
		OrganizationID: strings.TrimSpace(organizationID),
		UserID:         strings.TrimSpace(userID),
		Status:         DemeterAudioTranscriptionOperationStatusCancelled,
		Stage:          "cancelled",
		UpdatedAt:      now,
		FinishedAt:     sql.NullTime{Time: now, Valid: true},
	}
	if record.OperationID == "" || record.OrganizationID == "" || record.UserID == "" {
		return nil, fmt.Errorf("operation_id, organization_id and user_id are required")
	}

	existing, err := s.GetDemeterAudioTranscriptionOperation(ctx, record.OperationID, record.OrganizationID, record.UserID)
	if err != nil {
		var ownershipErr *DemeterAudioTranscriptionOperationOwnershipError
		if errors.As(err, &ownershipErr) {
			logStoreStep(ctx, "ownership_cancel_error", "demeter_audio_transcription_operation", ownershipErr.WithSource("store_cancel").LogFields())
		}
		return nil, err
	}
	if existing.Status == DemeterAudioTranscriptionOperationStatusCompleted || existing.Status == DemeterAudioTranscriptionOperationStatusFailed || existing.Status == DemeterAudioTranscriptionOperationStatusCancelled {
		return existing, nil
	}

	record.ChunkIndex = existing.ChunkIndex
	record.ChunkCount = existing.ChunkCount
	record.Progress = existing.Progress
	record.PartialText = existing.PartialText
	record.ResponseJSON = existing.ResponseJSON
	record.LastError = sql.NullString{String: "operation cancelled", Valid: true}
	record.StatusCode = http.StatusRequestTimeout

	if err := s.UpdateDemeterAudioTranscriptionOperation(ctx, record); err != nil {
		var ownershipErr *DemeterAudioTranscriptionOperationOwnershipError
		if errors.As(err, &ownershipErr) {
			logStoreStep(ctx, "ownership_cancel_error", "demeter_audio_transcription_operation", ownershipErr.WithSource("store_cancel").LogFields())
		}
		return nil, err
	}

	return s.GetDemeterAudioTranscriptionOperation(ctx, record.OperationID, record.OrganizationID, record.UserID)
}

func scanDemeterAudioTranscriptionOperationRecord(row *sql.Row) (*DemeterAudioTranscriptionOperationRecord, error) {
	var record DemeterAudioTranscriptionOperationRecord
	if err := row.Scan(
		&record.OperationID,
		&record.OrganizationID,
		&record.UserID,
		&record.Status,
		&record.Stage,
		&record.ChunkIndex,
		&record.ChunkCount,
		&record.Progress,
		&record.PartialText,
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

func nullStringValue(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return strings.TrimSpace(value.String)
}

func textValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return strings.TrimSpace(value.String)
}

func nullTimeValue(value sql.NullTime) any {
	if !value.Valid {
		return nil
	}
	return value.Time.UTC()
}
