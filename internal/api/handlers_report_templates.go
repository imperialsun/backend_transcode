package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"demeter-backend/internal/reports"
	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type reportTemplateRequest struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	BaseFormat     string `json:"baseFormat"`
	Instructions   string `json:"instructions"`
	ExampleOutline string `json:"exampleOutline"`
	OrgEnabled     *bool  `json:"orgEnabled,omitempty"`
}

type reportTemplatePreferenceRequest struct {
	Enabled bool `json:"enabled"`
}

type reportTemplateDraftOperationRequest struct {
	DraftBrief       string   `json:"draftBrief"`
	BaseFormatHint   string   `json:"baseFormatHint,omitempty"`
	Tone             string   `json:"tone,omitempty"`
	RequiredSections []string `json:"requiredSections,omitempty"`
	ModelID          string   `json:"modelId,omitempty"`
	Temperature      float64  `json:"temperature,omitempty"`
	MaxTokens        int      `json:"maxTokens,omitempty"`
}

func (a *App) registerAdminReportTemplateRoutes(group fiber.Router) {
	group.Get("/organizations/:id/report-templates", a.adminListReportTemplates)
	group.Post("/organizations/:id/report-templates", a.adminCreateReportTemplate)
	group.Patch("/organizations/:id/report-templates/:templateId", a.adminPatchReportTemplate)
	group.Post("/organizations/:id/report-templates/draft-operations", a.adminSubmitReportTemplateDraftOperation)
	group.Get("/report-template-draft-operations/:operationId", a.adminGetReportTemplateDraftOperation)
}

// RegisterReportTemplateRoutes installs app-facing custom report template routes.
func (a *App) RegisterReportTemplateRoutes(router fiber.Router) {
	group := router.Group("/report-templates", a.AppAuthRequired(), RequirePermissions("feature.llmapi"))
	group.Get("/", a.listUserReportTemplates)
	group.Put("/:templateId/preference", a.putUserReportTemplatePreference)
}

func reportTemplateInputFromRequest(req reportTemplateRequest) (store.ReportTemplateInput, error) {
	baseFormat, ok := reports.ParseReportFormat(req.BaseFormat)
	if !ok {
		return store.ReportTemplateInput{}, fiber.NewError(fiber.StatusBadRequest, "invalid base format")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return store.ReportTemplateInput{}, fiber.NewError(fiber.StatusBadRequest, "name is required")
	}
	instructions := strings.TrimSpace(req.Instructions)
	if instructions == "" {
		return store.ReportTemplateInput{}, fiber.NewError(fiber.StatusBadRequest, "instructions are required")
	}
	orgEnabled := true
	if req.OrgEnabled != nil {
		orgEnabled = *req.OrgEnabled
	}
	return store.ReportTemplateInput{
		Name:           name,
		Description:    strings.TrimSpace(req.Description),
		BaseFormat:     string(baseFormat),
		Instructions:   instructions,
		ExampleOutline: strings.TrimSpace(req.ExampleOutline),
		OrgEnabled:     orgEnabled,
	}, nil
}

func (a *App) canManageReportTemplateOrganization(c *fiber.Ctx, organizationID string) bool {
	claims := MustClaims(c)
	return claims != nil && (isSuperAdmin(claims) || claims.OrgID == strings.TrimSpace(organizationID))
}

func (a *App) adminListReportTemplates(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	organizationID := strings.TrimSpace(c.Params("id"))
	if organizationID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "organization id is required"})
	}
	if !a.canManageReportTemplateOrganization(c, organizationID) {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}
	items, err := a.Store.ListOrganizationReportTemplates(requestContext(c), organizationID, false)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to list report templates"})
	}
	return c.JSON(items)
}

func (a *App) adminCreateReportTemplate(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	organizationID := strings.TrimSpace(c.Params("id"))
	if organizationID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "organization id is required"})
	}
	if !a.canManageReportTemplateOrganization(c, organizationID) {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}
	var req reportTemplateRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	input, err := reportTemplateInputFromRequest(req)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: err.Error()})
	}
	template, err := a.Store.CreateOrganizationReportTemplate(requestContext(c), organizationID, claims.UserID, input)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to create report template"})
	}
	a.writeAdminAudit(requestContext(c), claims, "admin.report_template.create", "report_template", template.ID, fiber.Map{
		"organizationId": organizationID,
		"name":           template.Name,
		"baseFormat":     template.BaseFormat,
		"orgEnabled":     template.OrgEnabled,
	})
	return c.Status(fiber.StatusCreated).JSON(template)
}

func (a *App) adminPatchReportTemplate(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	organizationID := strings.TrimSpace(c.Params("id"))
	templateID := strings.TrimSpace(c.Params("templateId"))
	if organizationID == "" || templateID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "organization id and template id are required"})
	}
	if !a.canManageReportTemplateOrganization(c, organizationID) {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}
	current, err := a.Store.GetOrganizationReportTemplate(requestContext(c), templateID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load report template"})
	}
	if current == nil || current.OrganizationID != organizationID {
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "report template not found"})
	}
	var req reportTemplateRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	input, err := reportTemplateInputFromRequest(req)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: err.Error()})
	}
	template, err := a.Store.UpdateOrganizationReportTemplate(requestContext(c), templateID, claims.UserID, input)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to update report template"})
	}
	a.writeAdminAudit(requestContext(c), claims, "admin.report_template.update", "report_template", template.ID, fiber.Map{
		"organizationId": organizationID,
		"name":           template.Name,
		"baseFormat":     template.BaseFormat,
		"orgEnabled":     template.OrgEnabled,
	})
	return c.JSON(template)
}

func (a *App) adminSubmitReportTemplateDraftOperation(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	if a.MistralClient == nil || !a.MistralClient.IsConfigured() {
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "mistral is not configured"})
	}
	organizationID := strings.TrimSpace(c.Params("id"))
	if organizationID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "organization id is required"})
	}
	if !a.canManageReportTemplateOrganization(c, organizationID) {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden organization scope"})
	}
	var req reportTemplateDraftOperationRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	draftBrief := strings.TrimSpace(req.DraftBrief)
	if draftBrief == "" {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "draftBrief is required"})
	}
	baseFormatHint := strings.TrimSpace(strings.ToUpper(req.BaseFormatHint))
	if baseFormatHint != "" {
		if _, ok := reports.ParseReportFormat(baseFormatHint); !ok {
			return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid base format hint"})
		}
	}
	modelID := strings.TrimSpace(req.ModelID)
	if modelID == "" {
		modelID = reports.DefaultReportModelID
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 3000
	}
	temperature := req.Temperature
	if temperature < 0 || temperature > 2 {
		temperature = reports.DefaultReportTemp
	}
	sections := make([]string, 0, len(req.RequiredSections))
	for _, section := range req.RequiredSections {
		if trimmed := strings.TrimSpace(section); trimmed != "" {
			sections = append(sections, trimmed)
		}
	}
	now := time.Now().UTC()
	payload := demeterReportQueueOperationPayload{
		TraceID:          requestTraceID(c),
		Route:            requestRoutePath(c),
		Seq:              nextDemeterReportOperationSequenceID(),
		Kind:             demeterReportQueueKindTemplateDraft,
		ModelID:          modelID,
		Temperature:      temperature,
		MaxTokens:        maxTokens,
		DraftBrief:       draftBrief,
		BaseFormatHint:   baseFormatHint,
		Tone:             strings.TrimSpace(req.Tone),
		RequiredSections: sections,
		CreatedAt:        now,
	}
	rawPayload, _ := json.Marshal(payload)
	record := &store.DemeterReportOperationRecord{
		OperationID:      "demeter-report-template-draft-" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		OrganizationID:   organizationID,
		UserID:           claims.UserID,
		Status:           store.DemeterReportOperationStatusPending,
		Stage:            "queued",
		FormatIndex:      0,
		FormatCount:      1,
		Progress:         0,
		QueuePayloadJSON: sql.NullString{String: string(rawPayload), Valid: true},
		StatusCode:       fiber.StatusAccepted,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	finalRecord, err := a.createAndEnqueueDemeterReportOperation(requestContext(c), record)
	if err != nil {
		if finalRecord != nil {
			return c.Status(finalRecord.StatusCode).JSON(demeterReportOperationResponseFromRecord(finalRecord))
		}
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to create report template draft operation"})
	}
	a.writeAdminAudit(requestContext(c), claims, "admin.report_template.draft.create", "report_template_draft", finalRecord.OperationID, fiber.Map{
		"organizationId": organizationID,
		"baseFormatHint": baseFormatHint,
	})
	return c.Status(finalRecord.StatusCode).JSON(demeterReportOperationResponseFromRecord(finalRecord))
}

func (a *App) adminGetReportTemplateDraftOperation(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	operationID := strings.TrimSpace(c.Params("operationId"))
	if operationID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "missing operation id"})
	}
	record, err := a.Store.GetDemeterReportOperationByID(requestContext(c), operationID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "operation not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load operation"})
	}
	if record.UserID != claims.UserID || !a.canManageReportTemplateOrganization(c, record.OrganizationID) {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden operation scope"})
	}
	payload, err := decodeDemeterReportQueuePayload(record.QueuePayloadJSON)
	if err != nil || demeterReportPayloadKind(payload) != demeterReportQueueKindTemplateDraft {
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "operation not found"})
	}
	return c.Status(fiber.StatusOK).JSON(demeterReportOperationResponseFromRecord(record))
}

func (a *App) listUserReportTemplates(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	items, err := a.Store.ListUserReportTemplatePreferences(requestContext(c), claims.OrgID, claims.UserID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to list report templates"})
	}
	return c.JSON(items)
}

func (a *App) putUserReportTemplatePreference(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	templateID := strings.TrimSpace(c.Params("templateId"))
	if templateID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "template id is required"})
	}
	template, err := a.Store.GetOrganizationReportTemplate(requestContext(c), templateID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load report template"})
	}
	if template == nil || template.OrganizationID != claims.OrgID || !template.OrgEnabled {
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "report template not found"})
	}
	var req reportTemplatePreferenceRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	item, err := a.Store.SaveUserReportTemplatePreference(requestContext(c), claims.UserID, templateID, req.Enabled)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to save report template preference"})
	}
	return c.JSON(item)
}
