package store

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
)

const demeterReportQueueSettingsID = 1

// DemeterReportQueueSettingsRecord stores the global worker parallelism used by
// the Demeter queue manager.
type DemeterReportQueueSettingsRecord struct {
	Parallelism int
	UpdatedAt   time.Time
}

// GetDemeterReportQueueSettings loads the singleton queue settings row. Missing
// rows are reported as nil so the caller can seed the default value.
func (s *Store) GetDemeterReportQueueSettings(ctx context.Context) (*DemeterReportQueueSettingsRecord, error) {
	var record DemeterReportQueueSettingsRecord
	if err := s.DB.QueryRowContext(ctx, `
		SELECT parallelism, updated_at
		FROM demeter_report_queue_settings
		WHERE id = ?
	`, demeterReportQueueSettingsID).Scan(&record.Parallelism, &record.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &record, nil
}

// SaveDemeterReportQueueSettings persists the singleton queue settings row.
func (s *Store) SaveDemeterReportQueueSettings(ctx context.Context, parallelism int) (*DemeterReportQueueSettingsRecord, error) {
	if parallelism < 0 {
		parallelism = 0
	}
	now := time.Now().UTC()
	logStoreStep(ctx, "save_start", "demeter_report_queue_settings", map[string]any{
		"parallelism": parallelism,
	})
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO demeter_report_queue_settings(id, parallelism, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			parallelism = excluded.parallelism,
			updated_at = excluded.updated_at
	`, demeterReportQueueSettingsID, parallelism, now)
	if err != nil {
		logStoreStep(ctx, "save_error", "demeter_report_queue_settings", map[string]any{"error": err, "parallelism": parallelism})
		return nil, err
	}
	record, err := s.GetDemeterReportQueueSettings(ctx)
	if err != nil {
		logStoreStep(ctx, "save_error", "demeter_report_queue_settings", map[string]any{"error": err, "parallelism": parallelism})
		return nil, err
	}
	if record == nil {
		record = &DemeterReportQueueSettingsRecord{Parallelism: parallelism, UpdatedAt: now}
	}
	logStoreStep(ctx, "save_success", "demeter_report_queue_settings", map[string]any{
		"parallelism": record.Parallelism,
	})
	return record, nil
}

// ClaimNextPendingDemeterReportOperationForQueue loads the oldest
// pending report for one lane and atomically marks it as running.
func (s *Store) ClaimNextPendingDemeterReportOperationForQueue(ctx context.Context, queueID int) (*DemeterReportOperationRecord, error) {
	if queueID < 0 {
		queueID = 0
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollbackTx(tx)

	row := tx.QueryRowContext(ctx, `
		SELECT operation_id, organization_id, user_id, queue_id, queue_payload_json, status, stage, format_index, format_count, progress, response_json, last_error, status_code, created_at, updated_at, finished_at
		FROM demeter_report_operations
		WHERE queue_id = ? AND status = ?
		ORDER BY created_at ASC, operation_id ASC
		LIMIT 1
	`, queueID, DemeterReportOperationStatusPending)

	record, err := scanDemeterReportOperationRecord(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `
		UPDATE demeter_report_operations
		SET status = ?, stage = ?, status_code = ?, updated_at = ?
		WHERE operation_id = ? AND status = ?
	`, DemeterReportOperationStatusRunning, "running", http.StatusAccepted, now, record.OperationID, DemeterReportOperationStatusPending)
	if err != nil {
		return nil, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if rows == 0 {
		return nil, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	record.Status = DemeterReportOperationStatusRunning
	record.Stage = "running"
	record.StatusCode = http.StatusAccepted
	record.UpdatedAt = now
	return record, nil
}

// ListDemeterReportOperations returns report rows filtered
// by queue and/or status for queue visualisation and reconciliation.
func (s *Store) ListDemeterReportOperations(ctx context.Context, queueID *int, statuses []string, limit int) ([]*DemeterReportOperationRecord, error) {
	query := strings.Builder{}
	args := make([]any, 0, len(statuses)+2)
	query.WriteString(`
		SELECT operation_id, organization_id, user_id, queue_id, queue_payload_json, status, stage, format_index, format_count, progress, response_json, last_error, status_code, created_at, updated_at, finished_at
		FROM demeter_report_operations
		WHERE 1 = 1
	`)
	if queueID != nil {
		query.WriteString(" AND queue_id = ?")
		args = append(args, *queueID)
	}
	if len(statuses) > 0 {
		query.WriteString(" AND status IN (")
		for i, status := range statuses {
			if i > 0 {
				query.WriteString(",")
			}
			query.WriteString("?")
			args = append(args, strings.TrimSpace(status))
		}
		query.WriteString(")")
	}
	query.WriteString(" ORDER BY created_at ASC, operation_id ASC")
	if limit > 0 {
		query.WriteString(" LIMIT ?")
		args = append(args, limit)
	}

	rows, err := s.DB.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)

	out := make([]*DemeterReportOperationRecord, 0)
	for rows.Next() {
		var record DemeterReportOperationRecord
		if err := rows.Scan(
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
		out = append(out, &record)
	}
	return out, rows.Err()
}
