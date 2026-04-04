package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"demeter-backend/internal/backendperformance"
	"github.com/google/uuid"
)

var performanceSuccessStatuses = []string{"success", "ok", "done", "completed", "complete"}

type PerformanceIngestResult struct {
	Accepted   int `json:"accepted"`
	Duplicates int `json:"duplicates"`
}

type PerformanceEvent struct {
	EventID        string    `json:"eventId"`
	TraceID        string    `json:"traceId"`
	UserID         string    `json:"userId,omitempty"`
	OrganizationID string    `json:"organizationId,omitempty"`
	Surface        string    `json:"surface"`
	Component      string    `json:"component"`
	Task           string    `json:"task"`
	Status         string    `json:"status"`
	DurationMS     int64     `json:"durationMs"`
	Route          string    `json:"route"`
	MetaJSON       string    `json:"metaJson"`
	OccurredAt     time.Time `json:"occurredAt"`
	Day            string    `json:"day"`
	CreatedAt      time.Time `json:"createdAt"`
}

type PerformanceRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type PerformanceTotals struct {
	Events            int   `json:"events"`
	Successes         int   `json:"successes"`
	Failures          int   `json:"failures"`
	TotalDurationMS   int64 `json:"totalDurationMs"`
	AverageDurationMS int64 `json:"averageDurationMs"`
	MaxDurationMS     int64 `json:"maxDurationMs"`
}

type PerformanceByDayItem struct {
	Day               string `json:"day"`
	Events            int    `json:"events"`
	Successes         int    `json:"successes"`
	Failures          int    `json:"failures"`
	TotalDurationMS   int64  `json:"totalDurationMs"`
	AverageDurationMS int64  `json:"averageDurationMs"`
	MaxDurationMS     int64  `json:"maxDurationMs"`
}

type PerformanceTaskItem struct {
	Surface           string    `json:"surface"`
	Component         string    `json:"component"`
	Task              string    `json:"task"`
	Route             string    `json:"route"`
	Events            int       `json:"events"`
	Successes         int       `json:"successes"`
	Failures          int       `json:"failures"`
	TotalDurationMS   int64     `json:"totalDurationMs"`
	AverageDurationMS int64     `json:"averageDurationMs"`
	MaxDurationMS     int64     `json:"maxDurationMs"`
	LastOccurredAt    time.Time `json:"lastOccurredAt"`
}

type PerformanceSummary struct {
	OrganizationID string                 `json:"organizationId"`
	Range          PerformanceRange       `json:"range"`
	Totals         PerformanceTotals      `json:"totals"`
	TaskOptions    []string               `json:"taskOptions"`
	ByDay          []PerformanceByDayItem `json:"byDay"`
	TopTasks       []PerformanceTaskItem  `json:"topTasks"`
	RecentEvents   []PerformanceEvent     `json:"recentEvents"`
}

type PerformanceSummaryFilters struct {
	OrganizationID string
	From           string
	To             string
	Task           string
	TopLimit       int
	RecentLimit    int
}

func (s *Store) InsertPerformanceEvent(ctx context.Context, input backendperformance.Event) error {
	metaJSON := strings.TrimSpace(string(input.MetaJSON))
	if metaJSON == "" {
		metaJSON = "{}"
	}

	occurredAt := input.OccurredAt.UTC()
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}

	eventID := strings.TrimSpace(input.EventID)
	if eventID == "" {
		eventID = uuid.NewString()
	}
	traceID := strings.TrimSpace(input.TraceID)
	if traceID == "" {
		traceID = eventID
	}

	day := occurredAt.Format(time.DateOnly)
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO performance_events(
			event_id,
			trace_id,
			user_id,
			organization_id,
			surface,
			component,
			task,
			status,
			duration_ms,
			route,
			meta_json,
			occurred_at,
			day,
			created_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, eventID,
		traceID,
		normalizeNullableString(input.UserID),
		normalizeNullableString(input.OrganizationID),
		normalizeRequiredString(input.Surface, "backend"),
		normalizeRequiredString(input.Component, "log"),
		normalizeRequiredString(input.Task, "unknown"),
		normalizeRequiredString(input.Status, "success"),
		input.DurationMS,
		normalizeRequiredString(input.Route, "-"),
		metaJSON,
		occurredAt,
		day,
		time.Now().UTC(),
	)
	return err
}

func (s *Store) IngestPerformanceEvents(
	ctx context.Context,
	organizationID string,
	userID string,
	events []backendperformance.Event,
) (PerformanceIngestResult, error) {
	result := PerformanceIngestResult{}
	logStoreStep(ctx, "ingest_start", "performance", map[string]any{
		"organization_id": strings.TrimSpace(organizationID),
		"user_id":         strings.TrimSpace(userID),
		"event_count":     len(events),
	})
	if len(events) == 0 {
		logStoreStep(ctx, "ingest_success", "performance", map[string]any{
			"organization_id": strings.TrimSpace(organizationID),
			"user_id":         strings.TrimSpace(userID),
			"accepted":        0,
			"duplicates":      0,
		})
		return result, nil
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		logStoreStep(ctx, "ingest_error", "performance", map[string]any{
			"error":           err,
			"organization_id": strings.TrimSpace(organizationID),
			"user_id":         strings.TrimSpace(userID),
		})
		return result, err
	}
	defer rollbackTx(tx)

	now := time.Now().UTC()
	for _, event := range events {
		normalized := normalizePerformanceEventInput(event, organizationID, userID)
		occurredAt := normalized.OccurredAt.UTC()
		if occurredAt.IsZero() {
			occurredAt = now
		}
		metaJSON := strings.TrimSpace(string(normalized.MetaJSON))
		if metaJSON == "" {
			metaJSON = "{}"
		}
		day := occurredAt.Format(time.DateOnly)
		eventID := strings.TrimSpace(normalized.EventID)
		if eventID == "" {
			eventID = uuid.NewString()
		}

		_, err := tx.ExecContext(ctx, `
			INSERT INTO performance_events(
				event_id,
				trace_id,
				user_id,
				organization_id,
				surface,
				component,
				task,
				status,
				duration_ms,
				route,
				meta_json,
				occurred_at,
				day,
				created_at
			) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, eventID,
			normalizeNullableString(normalized.TraceID),
			normalizeNullableString(normalized.UserID),
			normalizeNullableString(normalized.OrganizationID),
			normalizeRequiredString(normalized.Surface, "frontend"),
			normalizeRequiredString(normalized.Component, "frontend"),
			normalizeRequiredString(normalized.Task, "unknown"),
			normalizeRequiredString(normalized.Status, "success"),
			normalized.DurationMS,
			normalizeRequiredString(normalized.Route, "-"),
			metaJSON,
			occurredAt,
			day,
			now,
		)
		if err != nil {
			if isPerformanceEventDuplicateErr(err) {
				result.Duplicates++
				continue
			}
			logStoreStep(ctx, "ingest_error", "performance", map[string]any{
				"error":           err,
				"organization_id": strings.TrimSpace(organizationID),
				"user_id":         strings.TrimSpace(userID),
				"accepted":        result.Accepted,
				"duplicates":      result.Duplicates,
			})
			return result, err
		}
		result.Accepted++
	}

	if err := tx.Commit(); err != nil {
		logStoreStep(ctx, "ingest_error", "performance", map[string]any{
			"error":           err,
			"organization_id": strings.TrimSpace(organizationID),
			"user_id":         strings.TrimSpace(userID),
			"accepted":        result.Accepted,
			"duplicates":      result.Duplicates,
		})
		return result, err
	}

	logStoreStep(ctx, "ingest_success", "performance", map[string]any{
		"organization_id": strings.TrimSpace(organizationID),
		"user_id":         strings.TrimSpace(userID),
		"accepted":        result.Accepted,
		"duplicates":      result.Duplicates,
	})
	return result, nil
}

func (s *Store) GetPerformanceSummary(ctx context.Context, filters PerformanceSummaryFilters) (*PerformanceSummary, error) {
	fromDay, toDay := normalizePerformanceRange(filters.From, filters.To)
	whereSQL, args := buildPerformanceWhereClause(filters.OrganizationID, fromDay, toDay, filters.Task)
	scopeWhereSQL, scopeArgs := buildPerformanceScopeWhereClause(filters.OrganizationID, fromDay, toDay)
	topLimit := clampPositiveInt(filters.TopLimit, 10, 50)
	recentLimit := clampPositiveInt(filters.RecentLimit, 20, 100)

	summary := &PerformanceSummary{
		OrganizationID: strings.TrimSpace(filters.OrganizationID),
		Range: PerformanceRange{
			From: fromDay,
			To:   toDay,
		},
		TaskOptions:  []string{},
		ByDay:        []PerformanceByDayItem{},
		TopTasks:     []PerformanceTaskItem{},
		RecentEvents: []PerformanceEvent{},
	}

	if err := s.DB.QueryRowContext(ctx, `
		SELECT
			COALESCE(COUNT(*), 0),
			COALESCE(SUM(CASE WHEN `+performanceSuccessCaseExpr(`status`)+` THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN `+performanceSuccessCaseExpr(`status`)+` THEN 0 ELSE 1 END), 0),
			COALESCE(SUM(duration_ms), 0),
			COALESCE(CAST(ROUND(AVG(duration_ms)) AS INTEGER), 0),
			COALESCE(MAX(duration_ms), 0)
		FROM performance_events
		WHERE `+whereSQL,
		args...,
	).Scan(&summary.Totals.Events, &summary.Totals.Successes, &summary.Totals.Failures, &summary.Totals.TotalDurationMS, &summary.Totals.AverageDurationMS, &summary.Totals.MaxDurationMS); err != nil {
		return nil, err
	}

	taskRows, err := s.DB.QueryContext(ctx, `
		SELECT task
		FROM performance_events
		WHERE `+scopeWhereSQL+`
		GROUP BY task
		ORDER BY task ASC
	`, scopeArgs...)
	if err != nil {
		return nil, err
	}
	defer closeRows(taskRows)
	for taskRows.Next() {
		var task string
		if err := taskRows.Scan(&task); err != nil {
			return nil, err
		}
		if trimmed := strings.TrimSpace(task); trimmed != "" {
			summary.TaskOptions = append(summary.TaskOptions, trimmed)
		}
	}
	if err := taskRows.Err(); err != nil {
		return nil, err
	}

	dayRows, err := s.DB.QueryContext(ctx, `
		SELECT
			day,
			COALESCE(COUNT(*), 0) AS events,
			COALESCE(SUM(CASE WHEN `+performanceSuccessCaseExpr(`status`)+` THEN 1 ELSE 0 END), 0) AS successes,
			COALESCE(SUM(CASE WHEN `+performanceSuccessCaseExpr(`status`)+` THEN 0 ELSE 1 END), 0) AS failures,
			COALESCE(SUM(duration_ms), 0) AS total_duration_ms,
			COALESCE(CAST(ROUND(AVG(duration_ms)) AS INTEGER), 0) AS average_duration_ms,
			COALESCE(MAX(duration_ms), 0) AS max_duration_ms
		FROM performance_events
		WHERE `+whereSQL+`
		GROUP BY day
		ORDER BY day ASC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer closeRows(dayRows)
	for dayRows.Next() {
		var item PerformanceByDayItem
		if err := dayRows.Scan(
			&item.Day,
			&item.Events,
			&item.Successes,
			&item.Failures,
			&item.TotalDurationMS,
			&item.AverageDurationMS,
			&item.MaxDurationMS,
		); err != nil {
			return nil, err
		}
		summary.ByDay = append(summary.ByDay, item)
	}
	if err := dayRows.Err(); err != nil {
		return nil, err
	}

	topRows, err := s.DB.QueryContext(ctx, `
		SELECT
			surface,
			component,
			task,
			route,
			COALESCE(COUNT(*), 0) AS events,
			COALESCE(SUM(CASE WHEN `+performanceSuccessCaseExpr(`status`)+` THEN 1 ELSE 0 END), 0) AS successes,
			COALESCE(SUM(CASE WHEN `+performanceSuccessCaseExpr(`status`)+` THEN 0 ELSE 1 END), 0) AS failures,
			COALESCE(SUM(duration_ms), 0) AS total_duration_ms,
			COALESCE(CAST(ROUND(AVG(duration_ms)) AS INTEGER), 0) AS average_duration_ms,
			COALESCE(MAX(duration_ms), 0) AS max_duration_ms,
			MAX(occurred_at) AS last_occurred_at
		FROM performance_events
		WHERE `+whereSQL+`
		GROUP BY surface, component, task, route
		ORDER BY average_duration_ms DESC, events DESC, last_occurred_at DESC
		LIMIT ?
	`, append(args, topLimit)...)
	if err != nil {
		return nil, err
	}
	defer closeRows(topRows)
	for topRows.Next() {
		var item PerformanceTaskItem
		var lastOccurredAt sql.NullString
		if err := topRows.Scan(
			&item.Surface,
			&item.Component,
			&item.Task,
			&item.Route,
			&item.Events,
			&item.Successes,
			&item.Failures,
			&item.TotalDurationMS,
			&item.AverageDurationMS,
			&item.MaxDurationMS,
			&lastOccurredAt,
		); err != nil {
			return nil, err
		}
		if lastOccurredAt.Valid {
			parsed, err := parsePerformanceTimestamp(lastOccurredAt.String)
			if err != nil {
				return nil, err
			}
			item.LastOccurredAt = parsed
		}
		summary.TopTasks = append(summary.TopTasks, item)
	}
	if err := topRows.Err(); err != nil {
		return nil, err
	}

	recentRows, err := s.DB.QueryContext(ctx, `
		SELECT
			event_id,
			trace_id,
			user_id,
			organization_id,
			surface,
			component,
			task,
			status,
			duration_ms,
			route,
			meta_json,
			occurred_at,
			day,
			created_at
		FROM performance_events
		WHERE `+whereSQL+`
		ORDER BY occurred_at DESC, event_id DESC
		LIMIT ?
	`, append(args, recentLimit)...)
	if err != nil {
		return nil, err
	}
	defer closeRows(recentRows)
	for recentRows.Next() {
		var (
			item   PerformanceEvent
			userID sql.NullString
			orgID  sql.NullString
			meta   sql.NullString
		)
		if err := recentRows.Scan(
			&item.EventID,
			&item.TraceID,
			&userID,
			&orgID,
			&item.Surface,
			&item.Component,
			&item.Task,
			&item.Status,
			&item.DurationMS,
			&item.Route,
			&meta,
			&item.OccurredAt,
			&item.Day,
			&item.CreatedAt,
		); err != nil {
			return nil, err
		}
		item.UserID = strings.TrimSpace(userID.String)
		item.OrganizationID = strings.TrimSpace(orgID.String)
		item.MetaJSON = strings.TrimSpace(meta.String)
		if item.MetaJSON == "" {
			item.MetaJSON = "{}"
		}
		summary.RecentEvents = append(summary.RecentEvents, item)
	}
	if err := recentRows.Err(); err != nil {
		return nil, err
	}

	return summary, nil
}

func (s *Store) PurgeExpiredPerformanceEvents(ctx context.Context, now time.Time) (int64, error) {
	cutoff := now.UTC().AddDate(0, 0, -30).Format(time.DateOnly)
	logStoreStep(ctx, "purge_start", "performance", map[string]any{"cutoff_day": cutoff})
	result, err := s.DB.ExecContext(ctx, `DELETE FROM performance_events WHERE day < ?`, cutoff)
	if err != nil {
		logStoreStep(ctx, "purge_error", "performance", map[string]any{"error": err, "cutoff_day": cutoff})
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		logStoreStep(ctx, "purge_error", "performance", map[string]any{"error": err, "cutoff_day": cutoff})
		return 0, err
	}
	logStoreStep(ctx, "purge_success", "performance", map[string]any{"cutoff_day": cutoff, "deleted_count": count})
	return count, nil
}

func (s *Store) DeletePerformanceEvents(ctx context.Context, filters PerformanceSummaryFilters) (int64, error) {
	fromDay, toDay := normalizePerformanceRange(filters.From, filters.To)
	whereSQL, args := buildPerformanceWhereClause(filters.OrganizationID, fromDay, toDay, filters.Task)
	logStoreStep(ctx, "delete_start", "performance", map[string]any{
		"organization_id": strings.TrimSpace(filters.OrganizationID),
		"from":            fromDay,
		"to":              toDay,
		"task":            strings.TrimSpace(filters.Task),
	})
	result, err := s.DB.ExecContext(ctx, `DELETE FROM performance_events WHERE `+whereSQL, args...)
	if err != nil {
		logStoreStep(ctx, "delete_error", "performance", map[string]any{
			"error":           err,
			"organization_id": strings.TrimSpace(filters.OrganizationID),
			"from":            fromDay,
			"to":              toDay,
			"task":            strings.TrimSpace(filters.Task),
		})
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		logStoreStep(ctx, "delete_error", "performance", map[string]any{
			"error":           err,
			"organization_id": strings.TrimSpace(filters.OrganizationID),
			"from":            fromDay,
			"to":              toDay,
			"task":            strings.TrimSpace(filters.Task),
		})
		return 0, err
	}
	logStoreStep(ctx, "delete_success", "performance", map[string]any{
		"organization_id": strings.TrimSpace(filters.OrganizationID),
		"from":            fromDay,
		"to":              toDay,
		"task":            strings.TrimSpace(filters.Task),
		"deleted_count":   count,
	})
	return count, nil
}

func normalizePerformanceEventInput(input backendperformance.Event, organizationID, userID string) backendperformance.Event {
	if strings.TrimSpace(input.OrganizationID) == "" {
		input.OrganizationID = strings.TrimSpace(organizationID)
	}
	if strings.TrimSpace(input.UserID) == "" {
		input.UserID = strings.TrimSpace(userID)
	}
	if strings.TrimSpace(input.Surface) == "" {
		input.Surface = "frontend"
	}
	if strings.TrimSpace(input.Component) == "" {
		input.Component = "frontend"
	}
	if strings.TrimSpace(input.Task) == "" {
		input.Task = "unknown"
	}
	if strings.TrimSpace(input.Status) == "" {
		input.Status = "success"
	}
	if strings.TrimSpace(input.Route) == "" {
		input.Route = "-"
	}
	if strings.TrimSpace(input.EventID) == "" {
		input.EventID = uuid.NewString()
	}
	if strings.TrimSpace(input.TraceID) == "" {
		input.TraceID = input.EventID
	}
	return input
}

func buildPerformanceWhereClause(organizationID, fromDay, toDay, task string) (string, []any) {
	clauses := []string{"1=1"}
	args := make([]any, 0, 4)

	if organizationID = strings.TrimSpace(organizationID); organizationID != "" {
		clauses = append(clauses, "organization_id = ?")
		args = append(args, organizationID)
	}
	if fromDay = strings.TrimSpace(fromDay); fromDay != "" {
		clauses = append(clauses, "day >= ?")
		args = append(args, fromDay)
	}
	if toDay = strings.TrimSpace(toDay); toDay != "" {
		clauses = append(clauses, "day <= ?")
		args = append(args, toDay)
	}
	if task = strings.TrimSpace(task); task != "" {
		clauses = append(clauses, "task = ?")
		args = append(args, task)
	}

	return strings.Join(clauses, " AND "), args
}

func buildPerformanceScopeWhereClause(organizationID, fromDay, toDay string) (string, []any) {
	clauses := []string{"1=1"}
	args := make([]any, 0, 4)

	if organizationID = strings.TrimSpace(organizationID); organizationID != "" {
		clauses = append(clauses, "organization_id = ?")
		args = append(args, organizationID)
	}
	if fromDay = strings.TrimSpace(fromDay); fromDay != "" {
		clauses = append(clauses, "day >= ?")
		args = append(args, fromDay)
	}
	if toDay = strings.TrimSpace(toDay); toDay != "" {
		clauses = append(clauses, "day <= ?")
		args = append(args, toDay)
	}

	return strings.Join(clauses, " AND "), args
}

func normalizePerformanceRange(fromRaw, toRaw string) (string, string) {
	fromRaw = strings.TrimSpace(fromRaw)
	toRaw = strings.TrimSpace(toRaw)
	if fromRaw == "" && toRaw == "" {
		toDay := time.Now().UTC().Format(time.DateOnly)
		fromDay := time.Now().UTC().AddDate(0, 0, -29).Format(time.DateOnly)
		return fromDay, toDay
	}

	now := time.Now().UTC()
	fromDay := fromRaw
	toDay := toRaw
	if fromDay == "" {
		fromDay = now.AddDate(0, 0, -29).Format(time.DateOnly)
	}
	if toDay == "" {
		toDay = now.Format(time.DateOnly)
	}
	if toDay < fromDay {
		fromDay, toDay = toDay, fromDay
	}
	return fromDay, toDay
}

func performanceSuccessCaseExpr(column string) string {
	values := make([]string, 0, len(performanceSuccessStatuses))
	for _, status := range performanceSuccessStatuses {
		values = append(values, "'"+status+"'")
	}
	return "lower(COALESCE(" + column + ", '')) IN (" + strings.Join(values, ", ") + ")"
}

func isPerformanceEventDuplicateErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint failed") && strings.Contains(msg, "performance_events.event_id")
}

func parsePerformanceTimestamp(value string) (time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}, nil
	}

	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05Z07:00",
		time.DateTime,
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			return parsed.UTC(), nil
		}
	}

	return time.Time{}, fmt.Errorf("unsupported performance timestamp: %q", trimmed)
}
