package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var errUnsupportedTableInfoTarget = errors.New("unsupported sqlite table info target")

func resolveTableInfoQuery(tableName string) (string, error) {
	switch strings.TrimSpace(tableName) {
	case "refresh_sessions":
		return `PRAGMA table_info(refresh_sessions)`, nil
	default:
		return "", fmt.Errorf("%w: %s", errUnsupportedTableInfoTarget, strings.TrimSpace(tableName))
	}
}

func ensureColumnExists(ctx context.Context, tx *sql.Tx, tableName string, columnName string, alterStmt string) error {
	query, err := resolveTableInfoQuery(tableName)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer closeRows(rows)

	for rows.Next() {
		var (
			cid       int
			name      string
			valueType string
			notNull   int
			defaultV  sql.NullString
			primaryK  int
		)
		if err := rows.Scan(&cid, &name, &valueType, &notNull, &defaultV, &primaryK); err != nil {
			return err
		}
		if name == columnName {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, alterStmt)
	return err
}

func (s *Store) GetUserPermissionOverrides(ctx context.Context, userID string) ([]UserPermissionOverrideInput, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT p.code, upo.effect
		FROM user_permission_overrides upo
		JOIN permissions p ON p.id = upo.permission_id
		WHERE upo.user_id = ?
		ORDER BY p.code
	`, userID)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)

	out := make([]UserPermissionOverrideInput, 0)
	for rows.Next() {
		var item UserPermissionOverrideInput
		if err := rows.Scan(&item.PermissionCode, &item.Effect); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) CountActiveUsersByGlobalRole(ctx context.Context, roleCode string) (int, error) {
	var count int
	err := s.DB.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT u.id)
		FROM users u
		JOIN user_global_roles ugr ON ugr.user_id = u.id
		JOIN global_roles gr ON gr.id = ugr.global_role_id
		WHERE u.status = 'active' AND gr.code = ?
	`, strings.TrimSpace(roleCode)).Scan(&count)
	return count, err
}

func (s *Store) CountActiveUsersByOrganizationRole(ctx context.Context, organizationID string, roleCode string) (int, error) {
	var count int
	err := s.DB.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT u.id)
		FROM users u
		JOIN user_organization_roles uor ON uor.user_id = u.id
		JOIN organization_roles orr ON orr.id = uor.organization_role_id
		WHERE u.status = 'active' AND u.organization_id = ? AND orr.code = ?
	`, strings.TrimSpace(organizationID), strings.TrimSpace(roleCode)).Scan(&count)
	return count, err
}

func (s *Store) DeleteUser(ctx context.Context, userID string) (bool, error) {
	logStoreStep(ctx, "delete_start", "user", map[string]any{"user_id": strings.TrimSpace(userID)})
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		logStoreStep(ctx, "delete_error", "user", map[string]any{"error": err, "user_id": strings.TrimSpace(userID)})
		return false, err
	}
	defer rollbackTx(tx)

	if _, err := tx.ExecContext(ctx, `
		UPDATE password_reset_tokens
		SET requested_by_user_id = NULL
		WHERE requested_by_user_id = ?
	`, strings.TrimSpace(userID)); err != nil {
		logStoreStep(ctx, "delete_error", "user", map[string]any{"error": err, "user_id": strings.TrimSpace(userID)})
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE audit_logs
		SET actor_user_id = NULL
		WHERE actor_user_id = ?
	`, strings.TrimSpace(userID)); err != nil {
		logStoreStep(ctx, "delete_error", "user", map[string]any{"error": err, "user_id": strings.TrimSpace(userID)})
		return false, err
	}

	result, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, strings.TrimSpace(userID))
	if err != nil {
		logStoreStep(ctx, "delete_error", "user", map[string]any{"error": err, "user_id": strings.TrimSpace(userID)})
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		logStoreStep(ctx, "delete_error", "user", map[string]any{"error": err, "user_id": strings.TrimSpace(userID)})
		return false, err
	}
	if affected == 0 {
		logStoreStep(ctx, "delete_skipped", "user", map[string]any{"user_id": strings.TrimSpace(userID), "reason": "missing"})
		return false, nil
	}

	if err := tx.Commit(); err != nil {
		logStoreStep(ctx, "delete_error", "user", map[string]any{"error": err, "user_id": strings.TrimSpace(userID)})
		return false, err
	}
	logStoreStep(ctx, "delete_success", "user", map[string]any{"user_id": strings.TrimSpace(userID)})
	return true, nil
}

func (s *Store) DeleteUserActivity(ctx context.Context, userID string) (int64, error) {
	logStoreStep(ctx, "delete_start", "activity", map[string]any{"user_id": strings.TrimSpace(userID)})
	result, err := s.DB.ExecContext(ctx, `
		DELETE FROM activity_usage_events
		WHERE user_id = ?
	`, strings.TrimSpace(userID))
	if err != nil {
		logStoreStep(ctx, "delete_error", "activity", map[string]any{"error": err, "user_id": strings.TrimSpace(userID)})
		return 0, err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		logStoreStep(ctx, "delete_error", "activity", map[string]any{"error": err, "user_id": strings.TrimSpace(userID)})
		return 0, err
	}
	logStoreStep(ctx, "delete_success", "activity", map[string]any{"user_id": strings.TrimSpace(userID), "deleted_count": affected})
	return affected, nil
}

func (s *Store) InsertAuditLog(ctx context.Context, input AuditLogInput) error {
	payload := input.PayloadJSON
	if len(strings.TrimSpace(string(payload))) == 0 && input.Payload != nil {
		raw, err := json.Marshal(input.Payload)
		if err != nil {
			return err
		}
		payload = raw
	}
	if len(strings.TrimSpace(string(payload))) == 0 {
		payload = json.RawMessage(`{}`)
	}

	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO audit_logs(id, actor_user_id, organization_id, action, target_type, target_id, payload_json, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
	`, uuid.NewString(), nullableString(input.ActorUserID), nullableString(input.OrganizationID), strings.TrimSpace(input.Action), nullableString(input.TargetType), nullableString(input.TargetID), string(payload), time.Now().UTC())
	return err
}

func (s *Store) GetGlobalActivitySummary(ctx context.Context, fromDay string, toDay string) (*ActivitySummary, error) {
	summary := &ActivitySummary{
		OrganizationID: "",
		Range: ActivityRange{
			From: fromDay,
			To:   toDay,
		},
		Breakdown: ActivityBreakdown{
			TranscriptionsByMode:     map[string]int{},
			TranscriptionsByProvider: map[string]int{},
			ReportsByMode:            map[string]int{},
			ReportsByProvider:        map[string]int{},
		},
		ByDay:  []ActivityByDayItem{},
		ByUser: []ActivityByUserItem{},
	}

	err := s.DB.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN event_kind = 'transcription' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN event_kind = 'report' THEN 1 ELSE 0 END), 0)
		FROM activity_usage_events
		WHERE day BETWEEN ? AND ?
	`, fromDay, toDay).Scan(&summary.Totals.Transcriptions, &summary.Totals.Reports)
	if err != nil {
		return nil, err
	}

	byDayRows, err := s.DB.QueryContext(ctx, `
		SELECT
			day,
			COALESCE(SUM(CASE WHEN event_kind = 'transcription' THEN 1 ELSE 0 END), 0) AS transcriptions,
			COALESCE(SUM(CASE WHEN event_kind = 'report' THEN 1 ELSE 0 END), 0) AS reports
		FROM activity_usage_events
		WHERE day BETWEEN ? AND ?
		GROUP BY day
		ORDER BY day ASC
	`, fromDay, toDay)
	if err != nil {
		return nil, err
	}
	defer closeRows(byDayRows)
	for byDayRows.Next() {
		var item ActivityByDayItem
		if err := byDayRows.Scan(&item.Day, &item.Transcriptions, &item.Reports); err != nil {
			return nil, err
		}
		summary.ByDay = append(summary.ByDay, item)
	}
	if err := byDayRows.Err(); err != nil {
		return nil, err
	}

	byUserRows, err := s.DB.QueryContext(ctx, `
		SELECT
			e.user_id,
			COALESCE(u.email, '') AS email,
			COALESCE(SUM(CASE WHEN e.event_kind = 'transcription' THEN 1 ELSE 0 END), 0) AS transcriptions,
			COALESCE(SUM(CASE WHEN e.event_kind = 'report' THEN 1 ELSE 0 END), 0) AS reports
		FROM activity_usage_events e
		LEFT JOIN users u ON u.id = e.user_id
		WHERE e.day BETWEEN ? AND ?
		GROUP BY e.user_id, u.email
		ORDER BY (transcriptions + reports) DESC, email ASC
	`, fromDay, toDay)
	if err != nil {
		return nil, err
	}
	defer closeRows(byUserRows)
	for byUserRows.Next() {
		var item ActivityByUserItem
		if err := byUserRows.Scan(&item.UserID, &item.Email, &item.Transcriptions, &item.Reports); err != nil {
			return nil, err
		}
		summary.ByUser = append(summary.ByUser, item)
	}
	if err := byUserRows.Err(); err != nil {
		return nil, err
	}

	if err := s.scanActivityBreakdown(ctx, summary.Breakdown.TranscriptionsByMode, `
		SELECT source_mode, COUNT(*)
		FROM activity_usage_events
		WHERE day BETWEEN ? AND ? AND event_kind = 'transcription'
		GROUP BY source_mode
	`, fromDay, toDay); err != nil {
		return nil, err
	}
	if err := s.scanActivityBreakdown(ctx, summary.Breakdown.TranscriptionsByProvider, `
		SELECT provider, COUNT(*)
		FROM activity_usage_events
		WHERE day BETWEEN ? AND ? AND event_kind = 'transcription'
		GROUP BY provider
	`, fromDay, toDay); err != nil {
		return nil, err
	}
	if err := s.scanActivityBreakdown(ctx, summary.Breakdown.ReportsByMode, `
		SELECT source_mode, COUNT(*)
		FROM activity_usage_events
		WHERE day BETWEEN ? AND ? AND event_kind = 'report'
		GROUP BY source_mode
	`, fromDay, toDay); err != nil {
		return nil, err
	}
	if err := s.scanActivityBreakdown(ctx, summary.Breakdown.ReportsByProvider, `
		SELECT provider, COUNT(*)
		FROM activity_usage_events
		WHERE day BETWEEN ? AND ? AND event_kind = 'report'
		GROUP BY provider
	`, fromDay, toDay); err != nil {
		return nil, err
	}

	return summary, nil
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func hasColumn(ctx context.Context, db *sql.DB, tableName string, columnName string) (bool, error) {
	query, err := resolveTableInfoQuery(tableName)
	if err != nil {
		return false, err
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return false, err
	}
	defer closeRows(rows)
	for rows.Next() {
		var (
			cid       int
			name      string
			valueType string
			notNull   int
			defaultV  sql.NullString
			primaryK  int
		)
		if err := rows.Scan(&cid, &name, &valueType, &notNull, &defaultV, &primaryK); err != nil {
			return false, err
		}
		if name == columnName {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Store) RefreshSessionHasTypeColumn(ctx context.Context) (bool, error) {
	return hasColumn(ctx, s.DB, "refresh_sessions", "session_type")
}

func isNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
