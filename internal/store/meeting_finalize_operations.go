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

// MeetingFinalizeOperationStatus* constants track the lifecycle of a finalize
// job inside the store.
const (
	MeetingFinalizeOperationStatusPending   = "pending"
	MeetingFinalizeOperationStatusCompleted = "completed"
	MeetingFinalizeOperationStatusFailed    = "failed"

	meetingFinalizeOperationRetention = 24 * time.Hour
)

// ErrMeetingFinalizeOperationOwnership reports that the current actor tried to
// access someone else's finalize operation.
var ErrMeetingFinalizeOperationOwnership = errors.New("meeting finalize operation owned by another user")

// MeetingFinalizeOperationRecord stores the state of one idempotent meeting
// finalization job.
type MeetingFinalizeOperationRecord struct {
	OperationID       string
	OrganizationID    string
	UserID            string
	Status            string
	StatusCode        int
	ResponseJSON      sql.NullString
	ErrorMessage      sql.NullString
	CreatedAt         time.Time
	UpdatedAt         time.Time
	CompletedAt       sql.NullTime
	TerminalExpiresAt sql.NullTime
}

// MeetingFinalizeOperationClaimResult describes the current state and whether
// the caller claimed a pending operation.
type MeetingFinalizeOperationClaimResult struct {
	Record  *MeetingFinalizeOperationRecord
	Claimed bool
}

// ClaimMeetingFinalizeOperation creates or claims one pending operation inside
// a transaction so only one worker can finish it.
func (s *Store) ClaimMeetingFinalizeOperation(ctx context.Context, operationID, organizationID, userID string, now time.Time) (*MeetingFinalizeOperationClaimResult, error) {
	operationID = strings.TrimSpace(operationID)
	organizationID = strings.TrimSpace(organizationID)
	userID = strings.TrimSpace(userID)
	now = now.UTC()
	if operationID == "" || organizationID == "" || userID == "" {
		return nil, fmt.Errorf("operation_id, organization_id and user_id are required")
	}

	return withSQLiteRetry(ctx, func() (*MeetingFinalizeOperationClaimResult, error) {
		logStoreStep(ctx, "finalize_claim_start", "meeting_finalize_operation", map[string]any{
			"operation_id":    operationID,
			"organization_id": organizationID,
			"user_id":         userID,
		})

		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			logStoreStep(ctx, "finalize_claim_error", "meeting_finalize_operation", map[string]any{
				"operation_id": operationID,
				"error":        err,
			})
			return nil, err
		}
		defer rollbackTx(tx)

		result, err := claimOrGetMeetingFinalizeOperationTx(ctx, tx, operationID, organizationID, userID, now, true)
		if err != nil {
			logStoreStep(ctx, "finalize_claim_error", "meeting_finalize_operation", map[string]any{
				"operation_id": operationID,
				"error":        err,
			})
			return nil, err
		}

		if err := tx.Commit(); err != nil {
			logStoreStep(ctx, "finalize_claim_error", "meeting_finalize_operation", map[string]any{
				"operation_id": operationID,
				"error":        err,
			})
			return nil, err
		}

		logStoreStep(ctx, "finalize_claim_success", "meeting_finalize_operation", map[string]any{
			"operation_id": operationID,
			"status":       result.Record.Status,
			"claimed":      result.Claimed,
		})
		return result, nil
	})
}

// GetMeetingFinalizeOperation loads the current record without changing its
// state unless a stale pending job needs to be failed.
func (s *Store) GetMeetingFinalizeOperation(ctx context.Context, operationID, organizationID, userID string, now time.Time) (*MeetingFinalizeOperationRecord, error) {
	operationID = strings.TrimSpace(operationID)
	organizationID = strings.TrimSpace(organizationID)
	userID = strings.TrimSpace(userID)
	now = now.UTC()
	if operationID == "" || organizationID == "" || userID == "" {
		return nil, fmt.Errorf("operation_id, organization_id and user_id are required")
	}

	return withSQLiteRetry(ctx, func() (*MeetingFinalizeOperationRecord, error) {
		logStoreStep(ctx, "finalize_get_start", "meeting_finalize_operation", map[string]any{
			"operation_id":    operationID,
			"organization_id": organizationID,
			"user_id":         userID,
		})

		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			logStoreStep(ctx, "finalize_get_error", "meeting_finalize_operation", map[string]any{
				"operation_id": operationID,
				"error":        err,
			})
			return nil, err
		}
		defer rollbackTx(tx)

		result, err := claimOrGetMeetingFinalizeOperationTx(ctx, tx, operationID, organizationID, userID, now, false)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				if commitErr := tx.Commit(); commitErr != nil {
					logStoreStep(ctx, "finalize_get_error", "meeting_finalize_operation", map[string]any{
						"operation_id": operationID,
						"error":        commitErr,
					})
					return nil, commitErr
				}
				logStoreStep(ctx, "finalize_get_missing", "meeting_finalize_operation", map[string]any{
					"operation_id": operationID,
				})
				return nil, sql.ErrNoRows
			}
			logStoreStep(ctx, "finalize_get_error", "meeting_finalize_operation", map[string]any{
				"operation_id": operationID,
				"error":        err,
			})
			return nil, err
		}

		if err := tx.Commit(); err != nil {
			logStoreStep(ctx, "finalize_get_error", "meeting_finalize_operation", map[string]any{
				"operation_id": operationID,
				"error":        err,
			})
			return nil, err
		}

		logStoreStep(ctx, "finalize_get_success", "meeting_finalize_operation", map[string]any{
			"operation_id": operationID,
			"status":       result.Record.Status,
		})
		return result.Record, nil
	})
}

// CompleteMeetingFinalizeOperation stores the successful terminal result.
func (s *Store) CompleteMeetingFinalizeOperation(
	ctx context.Context,
	operationID, organizationID, userID string,
	statusCode int,
	responseJSON json.RawMessage,
	now time.Time,
) (*MeetingFinalizeOperationRecord, error) {
	return s.updateMeetingFinalizeOperationTerminal(ctx, "finalize_complete", operationID, organizationID, userID, statusCode, responseJSON, "", now)
}

// FailMeetingFinalizeOperation stores a failure response for the job.
func (s *Store) FailMeetingFinalizeOperation(
	ctx context.Context,
	operationID, organizationID, userID string,
	statusCode int,
	responseJSON json.RawMessage,
	errorMessage string,
	now time.Time,
) (*MeetingFinalizeOperationRecord, error) {
	return s.updateMeetingFinalizeOperationTerminal(ctx, "finalize_fail", operationID, organizationID, userID, statusCode, responseJSON, errorMessage, now)
}

// PurgeExpiredMeetingFinalizeOperations advances timed-out pending jobs to a
// terminal failure and removes old terminal rows.
func (s *Store) PurgeExpiredMeetingFinalizeOperations(ctx context.Context, now time.Time) (int64, error) {
	now = now.UTC()
	logStoreStep(ctx, "finalize_purge_start", "meeting_finalize_operation", map[string]any{
		"retention_hours": meetingFinalizeOperationRetention.Hours(),
	})

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		logStoreStep(ctx, "finalize_purge_error", "meeting_finalize_operation", map[string]any{"error": err})
		return 0, err
	}
	defer rollbackTx(tx)

	timeoutCutoff := now.Add(-meetingFinalizeOperationRetention)
	failedResponse := json.RawMessage(`{"error":"meeting finalization timed out"}`)
	res, err := tx.ExecContext(ctx, `
		UPDATE meeting_finalize_operations
		SET status = ?, status_code = ?, response_json = ?, error_message = ?, completed_at = ?, terminal_expires_at = ?, updated_at = ?
		WHERE status = ? AND created_at <= ?
	`, MeetingFinalizeOperationStatusFailed, http.StatusGatewayTimeout, string(failedResponse), "meeting finalization timed out", now, now.Add(meetingFinalizeOperationRetention), now, MeetingFinalizeOperationStatusPending, timeoutCutoff)
	if err != nil {
		logStoreStep(ctx, "finalize_purge_error", "meeting_finalize_operation", map[string]any{"error": err})
		return 0, err
	}

	deleteRes, err := tx.ExecContext(ctx, `
		DELETE FROM meeting_finalize_operations
		WHERE status IN (?, ?) AND terminal_expires_at IS NOT NULL AND terminal_expires_at <= ?
	`, MeetingFinalizeOperationStatusCompleted, MeetingFinalizeOperationStatusFailed, now)
	if err != nil {
		logStoreStep(ctx, "finalize_purge_error", "meeting_finalize_operation", map[string]any{"error": err})
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		logStoreStep(ctx, "finalize_purge_error", "meeting_finalize_operation", map[string]any{"error": err})
		return 0, err
	}

	updatedRows, _ := res.RowsAffected()
	deletedRows, _ := deleteRes.RowsAffected()
	total := updatedRows + deletedRows
	logStoreStep(ctx, "finalize_purge_success", "meeting_finalize_operation", map[string]any{
		"updated_rows": updatedRows,
		"deleted_rows": deletedRows,
		"total":        total,
	})
	return total, nil
}

// claimOrGetMeetingFinalizeOperationTx centralizes the claim-or-read logic used
// by both the public claim and get methods.
func claimOrGetMeetingFinalizeOperationTx(
	ctx context.Context,
	tx *sql.Tx,
	operationID, organizationID, userID string,
	now time.Time,
	allowClaim bool,
) (*MeetingFinalizeOperationClaimResult, error) {
	record, err := loadMeetingFinalizeOperationRecordTx(ctx, tx, operationID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if !allowClaim {
				return nil, sql.ErrNoRows
			}
			inserted, err := insertMeetingFinalizeOperationPendingTx(ctx, tx, operationID, organizationID, userID, now)
			if err != nil {
				return nil, err
			}
			return &MeetingFinalizeOperationClaimResult{Record: inserted, Claimed: true}, nil
		}
		return nil, err
	}

	if record.OrganizationID != organizationID || record.UserID != userID {
		return nil, ErrMeetingFinalizeOperationOwnership
	}

	if record.Status == MeetingFinalizeOperationStatusPending {
		if isMeetingFinalizeOperationStale(record, now) {
			failed, err := markMeetingFinalizeOperationFailedTx(ctx, tx, record.OperationID, record.OrganizationID, record.UserID, http.StatusGatewayTimeout, json.RawMessage(`{"error":"meeting finalization timed out"}`), "meeting finalization timed out", now)
			if err != nil {
				return nil, err
			}
			return &MeetingFinalizeOperationClaimResult{Record: failed, Claimed: false}, nil
		}
		return &MeetingFinalizeOperationClaimResult{Record: record, Claimed: false}, nil
	}

	if isMeetingFinalizeOperationTerminalExpired(record, now) {
		if _, err := tx.ExecContext(ctx, `DELETE FROM meeting_finalize_operations WHERE operation_id = ?`, record.OperationID); err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}

	return &MeetingFinalizeOperationClaimResult{Record: record, Claimed: false}, nil
}

// loadMeetingFinalizeOperationRecordTx reads one row inside the current
// transaction.
func loadMeetingFinalizeOperationRecordTx(ctx context.Context, tx *sql.Tx, operationID string) (*MeetingFinalizeOperationRecord, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT operation_id, organization_id, user_id, status, status_code, response_json, error_message, created_at, updated_at, completed_at, terminal_expires_at
		FROM meeting_finalize_operations
		WHERE operation_id = ?
	`, strings.TrimSpace(operationID))

	var record MeetingFinalizeOperationRecord
	var responseJSON sql.NullString
	var errorMessage sql.NullString
	if err := row.Scan(
		&record.OperationID,
		&record.OrganizationID,
		&record.UserID,
		&record.Status,
		&record.StatusCode,
		&responseJSON,
		&errorMessage,
		&record.CreatedAt,
		&record.UpdatedAt,
		&record.CompletedAt,
		&record.TerminalExpiresAt,
	); err != nil {
		return nil, err
	}
	record.ResponseJSON = responseJSON
	record.ErrorMessage = errorMessage
	return &record, nil
}

// insertMeetingFinalizeOperationPendingTx creates a brand-new pending record.
func insertMeetingFinalizeOperationPendingTx(ctx context.Context, tx *sql.Tx, operationID, organizationID, userID string, now time.Time) (*MeetingFinalizeOperationRecord, error) {
	record := &MeetingFinalizeOperationRecord{
		OperationID:    strings.TrimSpace(operationID),
		OrganizationID: strings.TrimSpace(organizationID),
		UserID:         strings.TrimSpace(userID),
		Status:         MeetingFinalizeOperationStatusPending,
		StatusCode:     http.StatusAccepted,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO meeting_finalize_operations(
			operation_id,
			organization_id,
			user_id,
			status,
			status_code,
			response_json,
			error_message,
			created_at,
			updated_at,
			completed_at,
			terminal_expires_at
		) VALUES (?, ?, ?, ?, ?, NULL, NULL, ?, ?, NULL, NULL)
	`, record.OperationID, record.OrganizationID, record.UserID, record.Status, record.StatusCode, record.CreatedAt, record.UpdatedAt); err != nil {
		return nil, err
	}
	return record, nil
}

// markMeetingFinalizeOperationFailedTx updates an existing record to a failed
// terminal state.
func markMeetingFinalizeOperationFailedTx(
	ctx context.Context,
	tx *sql.Tx,
	operationID, organizationID, userID string,
	statusCode int,
	responseJSON json.RawMessage,
	errorMessage string,
	now time.Time,
) (*MeetingFinalizeOperationRecord, error) {
	operationID = strings.TrimSpace(operationID)
	organizationID = strings.TrimSpace(organizationID)
	userID = strings.TrimSpace(userID)
	response := strings.TrimSpace(string(responseJSON))
	if response == "" {
		response = `{"error":"meeting finalization failed"}`
	}
	if strings.TrimSpace(errorMessage) == "" {
		errorMessage = "meeting finalization failed"
	}

	_, err := tx.ExecContext(ctx, `
		UPDATE meeting_finalize_operations
		SET status = ?, status_code = ?, response_json = ?, error_message = ?, completed_at = ?, terminal_expires_at = ?, updated_at = ?
		WHERE operation_id = ? AND organization_id = ? AND user_id = ?
	`, MeetingFinalizeOperationStatusFailed, statusCode, response, errorMessage, now, now.Add(meetingFinalizeOperationRetention), now, operationID, organizationID, userID)
	if err != nil {
		return nil, err
	}
	return loadMeetingFinalizeOperationRecordTx(ctx, tx, operationID)
}

// updateMeetingFinalizeOperationTerminal stores the final response and terminal
// expiry for a job.
func (s *Store) updateMeetingFinalizeOperationTerminal(
	ctx context.Context,
	step string,
	operationID, organizationID, userID string,
	statusCode int,
	responseJSON json.RawMessage,
	errorMessage string,
	now time.Time,
) (*MeetingFinalizeOperationRecord, error) {
	operationID = strings.TrimSpace(operationID)
	organizationID = strings.TrimSpace(organizationID)
	userID = strings.TrimSpace(userID)
	now = now.UTC()
	if operationID == "" || organizationID == "" || userID == "" {
		return nil, fmt.Errorf("operation_id, organization_id and user_id are required")
	}

	return withSQLiteRetry(ctx, func() (*MeetingFinalizeOperationRecord, error) {
		logStoreStep(ctx, step+"_start", "meeting_finalize_operation", map[string]any{
			"operation_id":    operationID,
			"organization_id": organizationID,
			"user_id":         userID,
			"status_code":     statusCode,
		})

		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			logStoreStep(ctx, step+"_error", "meeting_finalize_operation", map[string]any{
				"operation_id": operationID,
				"error":        err,
			})
			return nil, err
		}
		defer rollbackTx(tx)

		record, err := loadMeetingFinalizeOperationRecordTx(ctx, tx, operationID)
		if err != nil {
			logStoreStep(ctx, step+"_error", "meeting_finalize_operation", map[string]any{
				"operation_id": operationID,
				"error":        err,
			})
			return nil, err
		}
		if record.OrganizationID != organizationID || record.UserID != userID {
			logStoreStep(ctx, step+"_error", "meeting_finalize_operation", map[string]any{
				"operation_id": operationID,
				"error":        ErrMeetingFinalizeOperationOwnership,
			})
			return nil, ErrMeetingFinalizeOperationOwnership
		}
		if record.Status != MeetingFinalizeOperationStatusPending {
			logStoreStep(ctx, step+"_error", "meeting_finalize_operation", map[string]any{
				"operation_id": operationID,
				"status":       record.Status,
			})
			return nil, fmt.Errorf("meeting finalize operation is not pending")
		}

		response := strings.TrimSpace(string(responseJSON))
		if response == "" {
			response = `{}`
		}
		if errorMessage != "" {
			errorMessage = strings.TrimSpace(errorMessage)
		}
		if statusCode == 0 {
			statusCode = http.StatusOK
		}

		_, err = tx.ExecContext(ctx, `
		UPDATE meeting_finalize_operations
		SET status = ?, status_code = ?, response_json = ?, error_message = ?, completed_at = ?, terminal_expires_at = ?, updated_at = ?
		WHERE operation_id = ? AND organization_id = ? AND user_id = ? AND status = ?
	`, terminalStatusFromStatusCode(statusCode), statusCode, response, nullableString(errorMessage), now, now.Add(meetingFinalizeOperationRetention), now, operationID, organizationID, userID, MeetingFinalizeOperationStatusPending)
		if err != nil {
			logStoreStep(ctx, step+"_error", "meeting_finalize_operation", map[string]any{
				"operation_id": operationID,
				"error":        err,
			})
			return nil, err
		}

		updated, err := loadMeetingFinalizeOperationRecordTx(ctx, tx, operationID)
		if err != nil {
			logStoreStep(ctx, step+"_error", "meeting_finalize_operation", map[string]any{
				"operation_id": operationID,
				"error":        err,
			})
			return nil, err
		}

		if err := tx.Commit(); err != nil {
			logStoreStep(ctx, step+"_error", "meeting_finalize_operation", map[string]any{
				"operation_id": operationID,
				"error":        err,
			})
			return nil, err
		}

		logStoreStep(ctx, step+"_success", "meeting_finalize_operation", map[string]any{
			"operation_id": operationID,
			"status":       updated.Status,
		})
		return updated, nil
	})
}

// isMeetingFinalizeOperationStale reports whether a pending job has outlived
// its processing window.
func isMeetingFinalizeOperationStale(record *MeetingFinalizeOperationRecord, now time.Time) bool {
	return record != nil && record.Status == MeetingFinalizeOperationStatusPending && record.CreatedAt.Add(meetingFinalizeOperationRetention).Before(now)
}

// isMeetingFinalizeOperationTerminalExpired reports whether a terminal row is
// eligible for deletion.
func isMeetingFinalizeOperationTerminalExpired(record *MeetingFinalizeOperationRecord, now time.Time) bool {
	if record == nil {
		return false
	}
	if !record.TerminalExpiresAt.Valid {
		return false
	}
	return !record.TerminalExpiresAt.Time.After(now)
}

// terminalStatusFromStatusCode maps the HTTP result into a terminal state.
func terminalStatusFromStatusCode(statusCode int) string {
	switch {
	case statusCode >= 200 && statusCode < 300:
		return MeetingFinalizeOperationStatusCompleted
	default:
		return MeetingFinalizeOperationStatusFailed
	}
}
