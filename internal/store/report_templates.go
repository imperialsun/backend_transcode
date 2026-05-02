package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

func normalizeReportTemplateInput(input ReportTemplateInput) ReportTemplateInput {
	return ReportTemplateInput{
		Name:           strings.TrimSpace(input.Name),
		Description:    strings.TrimSpace(input.Description),
		BaseFormat:     strings.ToUpper(strings.TrimSpace(input.BaseFormat)),
		Instructions:   strings.TrimSpace(input.Instructions),
		ExampleOutline: strings.TrimSpace(input.ExampleOutline),
		OrgEnabled:     input.OrgEnabled,
	}
}

func scanOrganizationReportTemplate(scanner interface {
	Scan(dest ...any) error
}) (*OrganizationReportTemplate, error) {
	var template OrganizationReportTemplate
	var createdBy sql.NullString
	var updatedBy sql.NullString
	var orgEnabled int
	if err := scanner.Scan(
		&template.ID,
		&template.OrganizationID,
		&template.Name,
		&template.Description,
		&template.BaseFormat,
		&template.Instructions,
		&template.ExampleOutline,
		&orgEnabled,
		&createdBy,
		&updatedBy,
		&template.CreatedAt,
		&template.UpdatedAt,
	); err != nil {
		return nil, err
	}
	template.OrgEnabled = orgEnabled != 0
	if createdBy.Valid {
		template.CreatedByUserID = createdBy.String
	}
	if updatedBy.Valid {
		template.UpdatedByUserID = updatedBy.String
	}
	return &template, nil
}

const reportTemplateSelectColumns = `
	id, organization_id, name, description, base_format, instructions, example_outline,
	org_enabled, created_by_user_id, updated_by_user_id, created_at, updated_at
`

func (s *Store) ListOrganizationReportTemplates(ctx context.Context, organizationID string, enabledOnly bool) ([]OrganizationReportTemplate, error) {
	organizationID = strings.TrimSpace(organizationID)
	query := `SELECT ` + reportTemplateSelectColumns + ` FROM organization_report_templates WHERE organization_id = ?`
	args := []any{organizationID}
	if enabledOnly {
		query += ` AND org_enabled = 1`
	}
	query += ` ORDER BY updated_at DESC, name ASC`
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)
	out := make([]OrganizationReportTemplate, 0)
	for rows.Next() {
		template, err := scanOrganizationReportTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *template)
	}
	return out, rows.Err()
}

func (s *Store) GetOrganizationReportTemplate(ctx context.Context, templateID string) (*OrganizationReportTemplate, error) {
	templateID = strings.TrimSpace(templateID)
	template, err := scanOrganizationReportTemplate(s.DB.QueryRowContext(ctx, `
		SELECT `+reportTemplateSelectColumns+`
		FROM organization_report_templates
		WHERE id = ?
	`, templateID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return template, nil
}

func (s *Store) CreateOrganizationReportTemplate(ctx context.Context, organizationID, actorUserID string, input ReportTemplateInput) (*OrganizationReportTemplate, error) {
	input = normalizeReportTemplateInput(input)
	now := time.Now().UTC()
	template := &OrganizationReportTemplate{
		ID:              uuid.NewString(),
		OrganizationID:  strings.TrimSpace(organizationID),
		Name:            input.Name,
		Description:     input.Description,
		BaseFormat:      input.BaseFormat,
		Instructions:    input.Instructions,
		ExampleOutline:  input.ExampleOutline,
		OrgEnabled:      input.OrgEnabled,
		CreatedByUserID: strings.TrimSpace(actorUserID),
		UpdatedByUserID: strings.TrimSpace(actorUserID),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	enabled := 0
	if template.OrgEnabled {
		enabled = 1
	}
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO organization_report_templates(
			id, organization_id, name, description, base_format, instructions, example_outline,
			org_enabled, created_by_user_id, updated_by_user_id, created_at, updated_at
		)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, template.ID, template.OrganizationID, template.Name, template.Description, template.BaseFormat,
		template.Instructions, template.ExampleOutline, enabled, nullString(template.CreatedByUserID),
		nullString(template.UpdatedByUserID), template.CreatedAt, template.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return template, nil
}

func (s *Store) UpdateOrganizationReportTemplate(ctx context.Context, templateID, actorUserID string, input ReportTemplateInput) (*OrganizationReportTemplate, error) {
	current, err := s.GetOrganizationReportTemplate(ctx, templateID)
	if err != nil || current == nil {
		return current, err
	}
	input = normalizeReportTemplateInput(input)
	now := time.Now().UTC()
	enabled := 0
	if input.OrgEnabled {
		enabled = 1
	}
	_, err = s.DB.ExecContext(ctx, `
		UPDATE organization_report_templates
		SET name = ?, description = ?, base_format = ?, instructions = ?, example_outline = ?,
			org_enabled = ?, updated_by_user_id = ?, updated_at = ?
		WHERE id = ?
	`, input.Name, input.Description, input.BaseFormat, input.Instructions, input.ExampleOutline,
		enabled, nullString(strings.TrimSpace(actorUserID)), now, current.ID)
	if err != nil {
		return nil, err
	}
	return s.GetOrganizationReportTemplate(ctx, current.ID)
}

func (s *Store) ListUserReportTemplatePreferences(ctx context.Context, organizationID, userID string) ([]UserReportTemplatePreference, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT t.id, t.organization_id, t.name, t.description, t.base_format, t.instructions, t.example_outline,
			t.org_enabled, t.created_by_user_id, t.updated_by_user_id, t.created_at, t.updated_at,
			COALESCE(p.enabled, 1)
		FROM organization_report_templates t
		LEFT JOIN user_report_template_preferences p
			ON p.template_id = t.id AND p.user_id = ?
		WHERE t.organization_id = ? AND t.org_enabled = 1
		ORDER BY t.updated_at DESC, t.name ASC
	`, strings.TrimSpace(userID), strings.TrimSpace(organizationID))
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)
	out := make([]UserReportTemplatePreference, 0)
	for rows.Next() {
		var enabled int
		template, err := scanOrganizationReportTemplate(rowScanner{rows: rows, extraDest: []any{&enabled}})
		if err != nil {
			return nil, err
		}
		out = append(out, UserReportTemplatePreference{Template: *template, Enabled: enabled != 0})
	}
	return out, rows.Err()
}

func (s *Store) SaveUserReportTemplatePreference(ctx context.Context, userID, templateID string, enabled bool) (*UserReportTemplatePreference, error) {
	template, err := s.GetOrganizationReportTemplate(ctx, templateID)
	if err != nil || template == nil {
		return nil, err
	}
	value := 0
	if enabled {
		value = 1
	}
	_, err = s.DB.ExecContext(ctx, `
		INSERT INTO user_report_template_preferences(user_id, template_id, enabled, updated_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(user_id, template_id) DO UPDATE SET enabled = excluded.enabled, updated_at = excluded.updated_at
	`, strings.TrimSpace(userID), template.ID, value, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return &UserReportTemplatePreference{Template: *template, Enabled: enabled}, nil
}

func (s *Store) IsUserReportTemplateEnabled(ctx context.Context, userID, templateID string) (bool, error) {
	var enabled sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `
		SELECT p.enabled
		FROM organization_report_templates t
		LEFT JOIN user_report_template_preferences p
			ON p.template_id = t.id AND p.user_id = ?
		WHERE t.id = ? AND t.org_enabled = 1
	`, strings.TrimSpace(userID), strings.TrimSpace(templateID)).Scan(&enabled)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return !enabled.Valid || enabled.Int64 != 0, nil
}

func nullString(value string) sql.NullString {
	value = strings.TrimSpace(value)
	return sql.NullString{String: value, Valid: value != ""}
}

type rowScanner struct {
	rows      *sql.Rows
	extraDest []any
}

func (r rowScanner) Scan(dest ...any) error {
	return r.rows.Scan(append(dest, r.extraDest...)...)
}
