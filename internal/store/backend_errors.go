package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"demeter-backend/internal/backenderrors"
	"github.com/google/uuid"
)

type BackendErrorEvent struct {
	ID             string    `json:"id"`
	TraceID        string    `json:"traceId"`
	UserID         string    `json:"userId,omitempty"`
	OrganizationID string    `json:"organizationId,omitempty"`
	Component      string    `json:"component"`
	Route          string    `json:"route"`
	Step           string    `json:"step"`
	Title          string    `json:"title"`
	StatusCode     int       `json:"statusCode,omitempty"`
	DurationMS     int64     `json:"durationMs,omitempty"`
	ErrorMessage   string    `json:"errorMessage,omitempty"`
	PayloadJSON    string    `json:"payloadJson"`
	AnnexJSON      string    `json:"annexJson,omitempty"`
	RecoveryStatus string    `json:"recoveryStatus,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
}

type BackendErrorEventFilters struct {
	OrganizationID string
	UserID         string
	Component      string
	Route          string
	Query          string
	From           time.Time
	To             time.Time
	Limit          int
	Offset         int
}

type BackendErrorEventListResult struct {
	Items []BackendErrorEvent
	Total int
}

func (s *Store) InsertBackendErrorEvent(ctx context.Context, input backenderrors.Event) error {
	payload := strings.TrimSpace(string(input.PayloadJSON))
	if payload == "" {
		payload = "{}"
	}
	annex := strings.TrimSpace(string(input.AnnexJSON))
	if annex == "" {
		annex = "{}"
	}

	createdAt := input.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}

	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO backend_error_events(
			id,
			trace_id,
			user_id,
			organization_id,
			component,
			route,
			step,
			title,
			status_code,
			duration_ms,
			error_message,
			payload_json,
			annex_json,
			recovery_status,
			created_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, uuid.NewString(),
		normalizeNullableString(input.TraceID),
		normalizeNullableString(input.UserID),
		normalizeNullableString(input.OrganizationID),
		normalizeRequiredString(input.Component, "log"),
		normalizeRequiredString(input.Route, "-"),
		normalizeRequiredString(input.Step, "unknown"),
		normalizeRequiredString(input.Title, normalizeRequiredString(input.Step, "unknown")),
		nullableInt(input.StatusCode),
		nullableInt64(input.DurationMS),
		normalizeNullableString(input.ErrorMessage),
		payload,
		annex,
		normalizeNullableString(input.RecoveryStatus),
		createdAt,
	)
	return err
}

func (s *Store) AttachBackendErrorAnnex(ctx context.Context, traceID string, annex json.RawMessage, recoveryStatus string) (int64, error) {
	normalizedTraceID := strings.TrimSpace(traceID)
	if normalizedTraceID == "" {
		return 0, nil
	}

	payload := strings.TrimSpace(string(annex))
	if payload == "" {
		payload = "{}"
	}

	result, err := s.DB.ExecContext(ctx, `
		UPDATE backend_error_events
		SET annex_json = ?, recovery_status = ?
		WHERE id IN (
			SELECT id
			FROM backend_error_events
			WHERE trace_id = ?
			ORDER BY created_at DESC, id DESC
			LIMIT 1
		)
	`, payload, normalizeNullableString(recoveryStatus), normalizedTraceID)
	if err != nil {
		return 0, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return rowsAffected, nil
}

func (s *Store) ListBackendErrorEvents(ctx context.Context, filters BackendErrorEventFilters) (BackendErrorEventListResult, error) {
	whereSQL, args := buildBackendErrorWhereClause(filters)
	limit := clampPositiveInt(filters.Limit, 50, 100)
	offset := clampNonNegativeInt(filters.Offset)

	var total int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM backend_error_events WHERE `+whereSQL, args...).Scan(&total); err != nil {
		return BackendErrorEventListResult{}, err
	}

	queryArgs := append(append([]any{}, args...), limit, offset)
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, trace_id, user_id, organization_id, component, route, step, title, status_code, duration_ms, error_message, payload_json, annex_json, recovery_status, created_at
		FROM backend_error_events
		WHERE `+whereSQL+`
		ORDER BY created_at DESC, id DESC
		LIMIT ? OFFSET ?
	`, queryArgs...)
	if err != nil {
		return BackendErrorEventListResult{}, err
	}
	defer closeRows(rows)

	items := make([]BackendErrorEvent, 0)
	for rows.Next() {
		var (
			item   BackendErrorEvent
			userID sql.NullString
			orgID  sql.NullString
			status sql.NullInt64
			dur    sql.NullInt64
			errMsg sql.NullString
			annex  sql.NullString
			recov  sql.NullString
		)
		if err := rows.Scan(
			&item.ID,
			&item.TraceID,
			&userID,
			&orgID,
			&item.Component,
			&item.Route,
			&item.Step,
			&item.Title,
			&status,
			&dur,
			&errMsg,
			&item.PayloadJSON,
			&annex,
			&recov,
			&item.CreatedAt,
		); err != nil {
			return BackendErrorEventListResult{}, err
		}
		item.UserID = strings.TrimSpace(userID.String)
		item.OrganizationID = strings.TrimSpace(orgID.String)
		if status.Valid {
			item.StatusCode = int(status.Int64)
		}
		if dur.Valid {
			item.DurationMS = dur.Int64
		}
		item.ErrorMessage = strings.TrimSpace(errMsg.String)
		if strings.TrimSpace(item.PayloadJSON) == "" {
			item.PayloadJSON = "{}"
		}
		item.AnnexJSON = strings.TrimSpace(annex.String)
		if strings.TrimSpace(item.AnnexJSON) == "" {
			item.AnnexJSON = "{}"
		}
		item.RecoveryStatus = strings.TrimSpace(recov.String)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return BackendErrorEventListResult{}, err
	}

	return BackendErrorEventListResult{
		Items: items,
		Total: total,
	}, nil
}

func (s *Store) DeleteBackendErrorEvents(ctx context.Context, filters BackendErrorEventFilters) (int64, error) {
	whereSQL, args := buildBackendErrorWhereClause(filters)
	result, err := s.DB.ExecContext(ctx, `DELETE FROM backend_error_events WHERE `+whereSQL, args...)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) PurgeExpiredBackendErrorEvents(ctx context.Context, now time.Time) (int64, error) {
	cutoff := now.UTC().AddDate(0, 0, -30)
	result, err := s.DB.ExecContext(ctx, `DELETE FROM backend_error_events WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return count, nil
}

func buildBackendErrorWhereClause(filters BackendErrorEventFilters) (string, []any) {
	clauses := []string{"1=1"}
	args := make([]any, 0, 8)

	if organizationID := strings.TrimSpace(filters.OrganizationID); organizationID != "" {
		clauses = append(clauses, "organization_id = ?")
		args = append(args, organizationID)
	}
	if userID := strings.TrimSpace(filters.UserID); userID != "" {
		clauses = append(clauses, "user_id = ?")
		args = append(args, userID)
	}
	if !filters.From.IsZero() {
		clauses = append(clauses, "created_at >= ?")
		args = append(args, filters.From.UTC())
	}
	if !filters.To.IsZero() {
		clauses = append(clauses, "created_at <= ?")
		args = append(args, filters.To.UTC())
	}
	if component := strings.TrimSpace(filters.Component); component != "" {
		clauses = append(clauses, "instr(lower(component), lower(?)) > 0")
		args = append(args, component)
	}
	if route := strings.TrimSpace(filters.Route); route != "" {
		clauses = append(clauses, "instr(lower(route), lower(?)) > 0")
		args = append(args, route)
	}
	if query := strings.TrimSpace(filters.Query); query != "" {
		normalized := strings.ToLower(query)
		clauses = append(clauses, `(
			instr(lower(component), ?) > 0 OR
			instr(lower(route), ?) > 0 OR
			instr(lower(step), ?) > 0 OR
			instr(lower(title), ?) > 0 OR
			instr(lower(COALESCE(error_message, '')), ?) > 0 OR
			instr(lower(trace_id), ?) > 0 OR
			instr(lower(COALESCE(payload_json, '')), ?) > 0 OR
			instr(lower(COALESCE(annex_json, '')), ?) > 0 OR
			instr(lower(COALESCE(recovery_status, '')), ?) > 0
		)`)
		args = append(args, normalized, normalized, normalized, normalized, normalized, normalized, normalized, normalized, normalized)
	}
	return strings.Join(clauses, " AND "), args
}

func clampPositiveInt(value, fallback, maximum int) int {
	if value <= 0 {
		return fallback
	}
	if value > maximum {
		return maximum
	}
	return value
}

func clampNonNegativeInt(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func normalizeNullableString(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func normalizeRequiredString(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func nullableInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableInt64(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}
