package store

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
)

const demeterAudioQueueSettingsID = 1

// DemeterAudioQueueSettingsRecord stores the global worker parallelism used by
// the Demeter queue manager.
type DemeterAudioQueueSettingsRecord struct {
	Parallelism int
	UpdatedAt   time.Time
}

// GetDemeterAudioQueueSettings loads the singleton queue settings row. Missing
// rows are reported as nil so the caller can seed the default value.
func (s *Store) GetDemeterAudioQueueSettings(ctx context.Context) (*DemeterAudioQueueSettingsRecord, error) {
	var record DemeterAudioQueueSettingsRecord
	if err := s.DB.QueryRowContext(ctx, `
		SELECT parallelism, updated_at
		FROM demeter_audio_queue_settings
		WHERE id = ?
	`, demeterAudioQueueSettingsID).Scan(&record.Parallelism, &record.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &record, nil
}

// SaveDemeterAudioQueueSettings persists the singleton queue settings row.
func (s *Store) SaveDemeterAudioQueueSettings(ctx context.Context, parallelism int) (*DemeterAudioQueueSettingsRecord, error) {
	if parallelism < 0 {
		parallelism = 0
	}
	now := time.Now().UTC()
	logStoreStep(ctx, "save_start", "demeter_audio_queue_settings", map[string]any{
		"parallelism": parallelism,
	})
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO demeter_audio_queue_settings(id, parallelism, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			parallelism = excluded.parallelism,
			updated_at = excluded.updated_at
	`, demeterAudioQueueSettingsID, parallelism, now)
	if err != nil {
		logStoreStep(ctx, "save_error", "demeter_audio_queue_settings", map[string]any{"error": err, "parallelism": parallelism})
		return nil, err
	}
	record, err := s.GetDemeterAudioQueueSettings(ctx)
	if err != nil {
		logStoreStep(ctx, "save_error", "demeter_audio_queue_settings", map[string]any{"error": err, "parallelism": parallelism})
		return nil, err
	}
	if record == nil {
		record = &DemeterAudioQueueSettingsRecord{Parallelism: parallelism, UpdatedAt: now}
	}
	logStoreStep(ctx, "save_success", "demeter_audio_queue_settings", map[string]any{
		"parallelism": record.Parallelism,
	})
	return record, nil
}

// ClaimNextPendingDemeterAudioTranscriptionOperationForQueue loads the oldest
// pending transcription for one lane and atomically marks it as running.
func (s *Store) ClaimNextPendingDemeterAudioTranscriptionOperationForQueue(ctx context.Context, queueID int) (*DemeterAudioTranscriptionOperationRecord, error) {
	if queueID < 0 {
		queueID = 0
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollbackTx(tx)

	row := tx.QueryRowContext(ctx, `
		SELECT operation_id, organization_id, user_id, queue_id, queue_payload_json, status, stage, chunk_index, chunk_count, progress, response_json, last_error, status_code, created_at, updated_at, finished_at
		FROM demeter_audio_transcription_operations
		WHERE queue_id = ? AND status = ?
		ORDER BY created_at ASC, operation_id ASC
		LIMIT 1
	`, queueID, DemeterAudioTranscriptionOperationStatusPending)

	record, err := scanDemeterAudioTranscriptionOperationRecord(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `
		UPDATE demeter_audio_transcription_operations
		SET status = ?, stage = ?, status_code = ?, updated_at = ?
		WHERE operation_id = ? AND status = ?
	`, DemeterAudioTranscriptionOperationStatusRunning, "running", http.StatusAccepted, now, record.OperationID, DemeterAudioTranscriptionOperationStatusPending)
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
	record.Status = DemeterAudioTranscriptionOperationStatusRunning
	record.Stage = "running"
	record.StatusCode = http.StatusAccepted
	record.UpdatedAt = now
	return record, nil
}

// ListDemeterAudioTranscriptionOperations returns transcription rows filtered
// by queue and/or status for queue visualisation and reconciliation.
func (s *Store) ListDemeterAudioTranscriptionOperations(ctx context.Context, queueID *int, statuses []string, limit int) ([]*DemeterAudioTranscriptionOperationRecord, error) {
	query := strings.Builder{}
	args := make([]any, 0, len(statuses)+2)
	query.WriteString(`
		SELECT operation_id, organization_id, user_id, queue_id, queue_payload_json, status, stage, chunk_index, chunk_count, progress, response_json, last_error, status_code, created_at, updated_at, finished_at
		FROM demeter_audio_transcription_operations
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

	out := make([]*DemeterAudioTranscriptionOperationRecord, 0)
	for rows.Next() {
		var record DemeterAudioTranscriptionOperationRecord
		if err := rows.Scan(
			&record.OperationID,
			&record.OrganizationID,
			&record.UserID,
			&record.QueueID,
			&record.QueuePayloadJSON,
			&record.Status,
			&record.Stage,
			&record.ChunkIndex,
			&record.ChunkCount,
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
