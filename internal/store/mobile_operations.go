package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	MobileOperationStatusPending   = "pending"
	MobileOperationStatusRunning   = "running"
	MobileOperationStatusCompleted = "completed"
	MobileOperationStatusFailed    = "failed"
	MobileOperationStatusCancelled = "cancelled"

	mobileOperationRetention = 24 * time.Hour
)

// ErrMobileOperationOwnership reports that the current actor tried to access
// another user's mobile operation.
var ErrMobileOperationOwnership = errors.New("mobile operation owned by another user")

// MobileOperationRecord stores the state exposed by the mobile polling route.
type MobileOperationRecord struct {
	OperationID       string
	OrganizationID    string
	UserID            string
	Kind              string
	Status            string
	StatusCode        int
	Stage             string
	Progress          float64
	ChunkIndex        int
	ChunkCount        int
	Message           sql.NullString
	LastError         sql.NullString
	AudioOperationID  sql.NullString
	ResponseJSON      sql.NullString
	CreatedAt         time.Time
	UpdatedAt         time.Time
	FinishedAt        sql.NullTime
	TerminalExpiresAt sql.NullTime
}

// MobileOperationCreateResult describes whether the current caller created the
// operation or reused an existing idempotent row.
type MobileOperationCreateResult struct {
	Record  *MobileOperationRecord
	Created bool
}

// CreateMobileOperationIfAbsent creates a new operation or returns the current
// owned row when the operation id already exists.
func (s *Store) CreateMobileOperationIfAbsent(ctx context.Context, record *MobileOperationRecord) (*MobileOperationCreateResult, error) {
	if record == nil {
		return nil, fmt.Errorf("record is required")
	}
	normalizeMobileOperationRecord(record)
	if record.OperationID == "" || record.OrganizationID == "" || record.UserID == "" {
		return nil, fmt.Errorf("operation_id, organization_id and user_id are required")
	}
	if record.Kind == "" {
		record.Kind = "mobile"
	}
	if record.Status == "" {
		record.Status = MobileOperationStatusRunning
	}
	if record.Stage == "" {
		record.Stage = "queued"
	}
	if record.StatusCode == 0 {
		record.StatusCode = http.StatusAccepted
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = record.CreatedAt
	}

	logStoreStep(ctx, "mobile_create_start", "mobile_operation", map[string]any{
		"operation_id":    record.OperationID,
		"organization_id": record.OrganizationID,
		"user_id":         record.UserID,
		"kind":            record.Kind,
		"status":          record.Status,
		"stage":           record.Stage,
	})

	res, err := s.DB.ExecContext(ctx, `
		INSERT OR IGNORE INTO mobile_operations(
			operation_id,
			organization_id,
			user_id,
			kind,
			status,
			status_code,
			stage,
			progress,
			chunk_index,
			chunk_count,
			message,
			last_error,
			audio_operation_id,
			response_json,
			created_at,
			updated_at,
			finished_at,
			terminal_expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, record.OperationID, record.OrganizationID, record.UserID, record.Kind, record.Status, record.StatusCode, record.Stage, record.Progress, record.ChunkIndex, record.ChunkCount, nullStringValue(record.Message), nullStringValue(record.LastError), nullStringValue(record.AudioOperationID), nullStringValue(record.ResponseJSON), record.CreatedAt, record.UpdatedAt, nullTimeValue(record.FinishedAt), nullTimeValue(record.TerminalExpiresAt))
	if err != nil {
		logStoreStep(ctx, "mobile_create_error", "mobile_operation", map[string]any{
			"operation_id": record.OperationID,
			"error":        err,
		})
		return nil, err
	}
	rows, _ := res.RowsAffected()

	loaded, err := s.GetMobileOperation(ctx, record.OperationID, record.OrganizationID, record.UserID)
	if err != nil {
		logStoreStep(ctx, "mobile_create_error", "mobile_operation", map[string]any{
			"operation_id": record.OperationID,
			"error":        err,
		})
		return nil, err
	}
	logStoreStep(ctx, "mobile_create_success", "mobile_operation", map[string]any{
		"operation_id": record.OperationID,
		"created":      rows > 0,
		"status":       loaded.Status,
		"stage":        loaded.Stage,
	})
	return &MobileOperationCreateResult{Record: loaded, Created: rows > 0}, nil
}

// GetMobileOperation loads one mobile operation and enforces owner scope.
func (s *Store) GetMobileOperation(ctx context.Context, operationID, organizationID, userID string) (*MobileOperationRecord, error) {
	operationID = strings.TrimSpace(operationID)
	organizationID = strings.TrimSpace(organizationID)
	userID = strings.TrimSpace(userID)
	if operationID == "" || organizationID == "" || userID == "" {
		return nil, fmt.Errorf("operation_id, organization_id and user_id are required")
	}

	logStoreStep(ctx, "mobile_get_start", "mobile_operation", map[string]any{
		"operation_id":    operationID,
		"organization_id": organizationID,
		"user_id":         userID,
	})
	row := s.DB.QueryRowContext(ctx, `
		SELECT operation_id, organization_id, user_id, kind, status, status_code, stage, progress, chunk_index, chunk_count, message, last_error, audio_operation_id, response_json, created_at, updated_at, finished_at, terminal_expires_at
		FROM mobile_operations
		WHERE operation_id = ?
	`, operationID)
	record, err := scanMobileOperationRecord(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			logStoreStep(ctx, "mobile_get_missing", "mobile_operation", map[string]any{"operation_id": operationID})
			return nil, sql.ErrNoRows
		}
		logStoreStep(ctx, "mobile_get_error", "mobile_operation", map[string]any{"operation_id": operationID, "error": err})
		return nil, err
	}
	if record.OrganizationID != organizationID || record.UserID != userID {
		logStoreStep(ctx, "mobile_get_ownership_error", "mobile_operation", map[string]any{
			"operation_id":    operationID,
			"request_org_id":  organizationID,
			"request_user_id": userID,
			"stored_org_id":   record.OrganizationID,
			"stored_user_id":  record.UserID,
		})
		return nil, ErrMobileOperationOwnership
	}
	logStoreStep(ctx, "mobile_get_success", "mobile_operation", map[string]any{
		"operation_id": operationID,
		"status":       record.Status,
		"stage":        record.Stage,
	})
	return record, nil
}

// UpdateMobileOperation writes non-terminal progress for one owned operation.
func (s *Store) UpdateMobileOperation(ctx context.Context, record *MobileOperationRecord) error {
	if record == nil {
		return fmt.Errorf("record is required")
	}
	normalizeMobileOperationRecord(record)
	if record.OperationID == "" || record.OrganizationID == "" || record.UserID == "" {
		return fmt.Errorf("operation_id, organization_id and user_id are required")
	}
	if record.StatusCode == 0 {
		record.StatusCode = http.StatusAccepted
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = time.Now().UTC()
	}
	logStoreStep(ctx, "mobile_update_start", "mobile_operation", map[string]any{
		"operation_id": record.OperationID,
		"status":       record.Status,
		"stage":        record.Stage,
		"progress":     record.Progress,
	})
	res, err := s.DB.ExecContext(ctx, `
		UPDATE mobile_operations
		SET status = ?, status_code = ?, stage = ?, progress = ?, chunk_index = ?, chunk_count = ?, message = ?, last_error = ?, audio_operation_id = ?, response_json = ?, updated_at = ?, finished_at = ?, terminal_expires_at = ?
		WHERE operation_id = ? AND organization_id = ? AND user_id = ?
	`, record.Status, record.StatusCode, record.Stage, record.Progress, record.ChunkIndex, record.ChunkCount, nullStringValue(record.Message), nullStringValue(record.LastError), nullStringValue(record.AudioOperationID), nullStringValue(record.ResponseJSON), record.UpdatedAt, nullTimeValue(record.FinishedAt), nullTimeValue(record.TerminalExpiresAt), record.OperationID, record.OrganizationID, record.UserID)
	if err != nil {
		logStoreStep(ctx, "mobile_update_error", "mobile_operation", map[string]any{"operation_id": record.OperationID, "error": err})
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		logStoreStep(ctx, "mobile_update_error", "mobile_operation", map[string]any{"operation_id": record.OperationID, "error": sql.ErrNoRows})
		return sql.ErrNoRows
	}
	logStoreStep(ctx, "mobile_update_success", "mobile_operation", map[string]any{
		"operation_id": record.OperationID,
		"status":       record.Status,
		"stage":        record.Stage,
	})
	return nil
}

// CompleteMobileOperation stores the successful terminal response.
func (s *Store) CompleteMobileOperation(ctx context.Context, operationID, organizationID, userID string, statusCode int, responseJSON json.RawMessage, now time.Time) (*MobileOperationRecord, error) {
	return s.updateMobileOperationTerminal(ctx, "mobile_complete", operationID, organizationID, userID, statusCode, responseJSON, "", now)
}

// FailMobileOperation stores a terminal failure response.
func (s *Store) FailMobileOperation(ctx context.Context, operationID, organizationID, userID string, statusCode int, responseJSON json.RawMessage, errorMessage string, now time.Time) (*MobileOperationRecord, error) {
	return s.updateMobileOperationTerminal(ctx, "mobile_fail", operationID, organizationID, userID, statusCode, responseJSON, errorMessage, now)
}

// PurgeExpiredMobileOperations fails stale running rows and removes old
// terminal rows.
func (s *Store) PurgeExpiredMobileOperations(ctx context.Context, now time.Time) (int64, error) {
	now = now.UTC()
	logStoreStep(ctx, "mobile_purge_start", "mobile_operation", map[string]any{"retention_hours": mobileOperationRetention.Hours()})

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		logStoreStep(ctx, "mobile_purge_error", "mobile_operation", map[string]any{"error": err})
		return 0, err
	}
	defer rollbackTx(tx)

	timeoutCutoff := now.Add(-mobileOperationRetention)
	failedResponse := json.RawMessage(`{"error":"mobile operation timed out"}`)
	res, err := tx.ExecContext(ctx, `
		UPDATE mobile_operations
		SET status = ?, status_code = ?, response_json = ?, last_error = ?, finished_at = ?, terminal_expires_at = ?, updated_at = ?
		WHERE status IN (?, ?) AND created_at <= ?
	`, MobileOperationStatusFailed, http.StatusGatewayTimeout, string(failedResponse), "mobile operation timed out", now, now.Add(mobileOperationRetention), now, MobileOperationStatusPending, MobileOperationStatusRunning, timeoutCutoff)
	if err != nil {
		logStoreStep(ctx, "mobile_purge_error", "mobile_operation", map[string]any{"error": err})
		return 0, err
	}

	deleteRes, err := tx.ExecContext(ctx, `
		DELETE FROM mobile_operations
		WHERE status IN (?, ?, ?) AND terminal_expires_at IS NOT NULL AND terminal_expires_at <= ?
	`, MobileOperationStatusCompleted, MobileOperationStatusFailed, MobileOperationStatusCancelled, now)
	if err != nil {
		logStoreStep(ctx, "mobile_purge_error", "mobile_operation", map[string]any{"error": err})
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		logStoreStep(ctx, "mobile_purge_error", "mobile_operation", map[string]any{"error": err})
		return 0, err
	}
	updatedRows, _ := res.RowsAffected()
	deletedRows, _ := deleteRes.RowsAffected()
	total := updatedRows + deletedRows
	logStoreStep(ctx, "mobile_purge_success", "mobile_operation", map[string]any{
		"updated_rows": updatedRows,
		"deleted_rows": deletedRows,
		"total":        total,
	})
	return total, nil
}

func (s *Store) updateMobileOperationTerminal(
	ctx context.Context,
	step string,
	operationID, organizationID, userID string,
	statusCode int,
	responseJSON json.RawMessage,
	errorMessage string,
	now time.Time,
) (*MobileOperationRecord, error) {
	operationID = strings.TrimSpace(operationID)
	organizationID = strings.TrimSpace(organizationID)
	userID = strings.TrimSpace(userID)
	now = now.UTC()
	if operationID == "" || organizationID == "" || userID == "" {
		return nil, fmt.Errorf("operation_id, organization_id and user_id are required")
	}
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	response := strings.TrimSpace(string(responseJSON))
	if response == "" {
		response = "{}"
	}
	status := MobileOperationStatusCompleted
	if statusCode < 200 || statusCode >= 300 {
		status = MobileOperationStatusFailed
	}

	logStoreStep(ctx, step+"_start", "mobile_operation", map[string]any{
		"operation_id": operationID,
		"status_code":  statusCode,
	})
	res, err := s.DB.ExecContext(ctx, `
		UPDATE mobile_operations
		SET status = ?, status_code = ?, response_json = ?, last_error = ?, finished_at = ?, terminal_expires_at = ?, updated_at = ?, progress = ?
		WHERE operation_id = ? AND organization_id = ? AND user_id = ?
	`, status, statusCode, response, nullableString(strings.TrimSpace(errorMessage)), now, now.Add(mobileOperationRetention), now, terminalMobileProgress(status), operationID, organizationID, userID)
	if err != nil {
		logStoreStep(ctx, step+"_error", "mobile_operation", map[string]any{"operation_id": operationID, "error": err})
		return nil, err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		logStoreStep(ctx, step+"_error", "mobile_operation", map[string]any{"operation_id": operationID, "error": sql.ErrNoRows})
		return nil, sql.ErrNoRows
	}
	record, err := s.GetMobileOperation(ctx, operationID, organizationID, userID)
	if err != nil {
		logStoreStep(ctx, step+"_error", "mobile_operation", map[string]any{"operation_id": operationID, "error": err})
		return nil, err
	}
	logStoreStep(ctx, step+"_success", "mobile_operation", map[string]any{
		"operation_id": operationID,
		"status":       record.Status,
	})
	return record, nil
}

func terminalMobileProgress(status string) float64 {
	if status == MobileOperationStatusCompleted {
		return 1
	}
	return 0
}

func normalizeMobileOperationRecord(record *MobileOperationRecord) {
	record.OperationID = strings.TrimSpace(record.OperationID)
	record.OrganizationID = strings.TrimSpace(record.OrganizationID)
	record.UserID = strings.TrimSpace(record.UserID)
	record.Kind = strings.TrimSpace(record.Kind)
	record.Status = strings.TrimSpace(record.Status)
	record.Stage = strings.TrimSpace(record.Stage)
	record.Message.String = strings.TrimSpace(record.Message.String)
	record.LastError.String = strings.TrimSpace(record.LastError.String)
	record.AudioOperationID.String = strings.TrimSpace(record.AudioOperationID.String)
	record.ResponseJSON.String = strings.TrimSpace(record.ResponseJSON.String)
	record.CreatedAt = record.CreatedAt.UTC()
	record.UpdatedAt = record.UpdatedAt.UTC()
}

func scanMobileOperationRecord(row *sql.Row) (*MobileOperationRecord, error) {
	var record MobileOperationRecord
	if err := row.Scan(
		&record.OperationID,
		&record.OrganizationID,
		&record.UserID,
		&record.Kind,
		&record.Status,
		&record.StatusCode,
		&record.Stage,
		&record.Progress,
		&record.ChunkIndex,
		&record.ChunkCount,
		&record.Message,
		&record.LastError,
		&record.AudioOperationID,
		&record.ResponseJSON,
		&record.CreatedAt,
		&record.UpdatedAt,
		&record.FinishedAt,
		&record.TerminalExpiresAt,
	); err != nil {
		return nil, err
	}
	return &record, nil
}
