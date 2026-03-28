package api

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/mail"
	"strings"
	"time"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/mailer"
	"demeter-backend/internal/observability"
	meetingreports "demeter-backend/internal/reports"
	"demeter-backend/internal/store"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

const (
	meetingReportSourceMode = "cloud_backend"
	meetingReportProvider   = "demeter_sante"
	meetingDraftsRoute      = "/api/v1/meetings/reports/drafts"
	meetingFinalizeRoute    = "/api/v1/meetings/finalize"
)

type meetingRequest struct {
	MeetingTitle            string                             `json:"meetingTitle"`
	Participants            []string                           `json:"participants"`
	TranscriptionSourceMode string                             `json:"transcriptionSourceMode,omitempty"`
	TranscriptionProvider   string                             `json:"transcriptionProvider,omitempty"`
	RecipientEmails         []string                           `json:"recipientEmails,omitempty"`
	RawTranscriptText       string                             `json:"rawTranscriptText,omitempty"`
	EditedTranscriptText    string                             `json:"editedTranscriptText,omitempty"`
	SpeakerAssignments      []meetingreports.SpeakerAssignment `json:"speakerAssignments,omitempty"`
	SelectedFormats         []string                           `json:"selectedFormats,omitempty"`
	ReportModelID           string                             `json:"reportModelId,omitempty"`
	ReportMaxTokens         int                                `json:"reportMaxTokens,omitempty"`
	ReportTemperature       float64                            `json:"reportTemperature,omitempty"`
	Reports                 []meetingReportEnvelope            `json:"reports,omitempty"`
}

type meetingReportEnvelope struct {
	Format           string                    `json:"format"`
	Report           meetingreports.ReportJson `json:"report"`
	Raw              string                    `json:"raw,omitempty"`
	ModelID          string                    `json:"modelId,omitempty"`
	GeneratedAt      string                    `json:"generatedAt,omitempty"`
	SourceMode       string                    `json:"sourceMode,omitempty"`
	Provider         string                    `json:"provider,omitempty"`
	SourceTokenCount int                       `json:"sourceTokenCount,omitempty"`
}

type meetingDraftResponse struct {
	MeetingTitle     string                  `json:"meetingTitle"`
	Participants     []string                `json:"participants"`
	SelectedFormats  []string                `json:"selectedFormats"`
	ReportSourceMode string                  `json:"reportSourceMode"`
	ReportProvider   string                  `json:"reportProvider"`
	ModelID          string                  `json:"modelId"`
	GeneratedAt      string                  `json:"generatedAt"`
	SourceTokenCount int                     `json:"sourceTokenCount"`
	Reports          []meetingReportEnvelope `json:"reports"`
}

type meetingAttachmentResponse struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	SizeBytes   int    `json:"sizeBytes"`
}

type meetingFinalizeResponse struct {
	MeetingTitle            string                      `json:"meetingTitle"`
	Participants            []string                    `json:"participants"`
	TranscriptionSourceMode string                      `json:"transcriptionSourceMode"`
	TranscriptionProvider   string                      `json:"transcriptionProvider"`
	ReportSourceMode        string                      `json:"reportSourceMode"`
	ReportProvider          string                      `json:"reportProvider"`
	SelectedFormats         []string                    `json:"selectedFormats"`
	SentTo                  string                      `json:"sentTo"`
	SentToEmails            []string                    `json:"sentToEmails,omitempty"`
	GeneratedAt             string                      `json:"generatedAt"`
	TranscriptDocxFilename  string                      `json:"transcriptDocxFilename"`
	ReportDocxFilenames     []string                    `json:"reportDocxFilenames"`
	Attachments             []meetingAttachmentResponse `json:"attachments"`
}

func (a *App) RegisterMeetingRoutes(router fiber.Router) {
	group := router.Group("/meetings", a.AppAuthRequired(), RequirePermissions("feature.llmapi", "provider.llm.demeter_sante"))
	group.Post("/reports/drafts", a.postMeetingReportDrafts)
	group.Post("/finalize", a.finalizeMeeting)
}

func (a *App) postMeetingReportDrafts(c *fiber.Ctx) error {
	traceID := requestTraceID(c)
	logMeetingStage(c, meetingDraftsRoute, traceID, "request_received", "", nil)

	claims := MustClaims(c)
	if claims == nil {
		logMeetingStage(c, meetingDraftsRoute, traceID, "request_unauthorized", "", map[string]any{
			"error": "unauthorized",
		})
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}

	var req meetingRequest
	if err := c.BodyParser(&req); err != nil {
		logMeetingStage(c, meetingDraftsRoute, traceID, "request_parse_error", "", map[string]any{
			"error": err.Error(),
		})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}

	title := normalizeMeetingTitle(req.MeetingTitle)
	participants := normalizeStringList(req.Participants)
	sourceText := resolveMeetingSourceText(req.EditedTranscriptText, req.RawTranscriptText)
	logMeetingStage(c, meetingDraftsRoute, traceID, "request_parsed", title, map[string]any{
		"participant_count":      len(participants),
		"selected_format_count":  len(req.SelectedFormats),
		"source_text_present":    sourceText != "",
		"requested_report_model": strings.TrimSpace(req.ReportModelID),
	})
	if sourceText == "" {
		logMeetingStage(c, meetingDraftsRoute, traceID, "validation_error", title, map[string]any{
			"error": "transcript text is required",
		})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "transcript text is required"})
	}

	selectedFormats := normalizeMeetingFormats(req.SelectedFormats)
	logMeetingStage(c, meetingDraftsRoute, traceID, "request_normalized", title, map[string]any{
		"participant_count":     len(participants),
		"selected_format_count": len(selectedFormats),
		"selected_formats":      strings.Join(selectedFormatsToStrings(selectedFormats), ","),
	})
	reportModelID := strings.TrimSpace(req.ReportModelID)
	reportMaxTokens := meetingReportMaxTokens(req.ReportMaxTokens)
	reportTemperature := meetingReportTemperature(req.ReportTemperature)
	logMeetingStage(c, meetingDraftsRoute, traceID, "generator_configured", title, map[string]any{
		"model_id":    reportModelID,
		"max_tokens":  reportMaxTokens,
		"temperature": reportTemperature,
	})
	generator, err := a.newMeetingReportGenerator(reportModelID, reportMaxTokens, reportTemperature)
	if err != nil {
		logMeetingStage(c, meetingDraftsRoute, traceID, "generator_error", title, map[string]any{
			"error": err.Error(),
		})
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: err.Error()})
	}

	generatedAt := time.Now().UTC().Format(time.RFC3339)
	sourceTokenCount := approximateTokenCount(sourceText)
	logMeetingStage(c, meetingDraftsRoute, traceID, "generate_start", title, map[string]any{
		"selected_format_count": len(selectedFormats),
		"source_token_count":    sourceTokenCount,
	})
	draftsByFormat, err := a.generateMeetingReportDrafts(requestContext(c), generator, title, participants, sourceText, selectedFormats, generatedAt, sourceTokenCount)
	if err != nil {
		logMeetingStage(c, meetingDraftsRoute, traceID, "generate_error", title, map[string]any{
			"error": err.Error(),
		})
		return a.meetingDraftErrorResponse(c, err)
	}
	logMeetingStage(c, meetingDraftsRoute, traceID, "generate_success", title, map[string]any{
		"draft_count": len(draftsByFormat),
	})

	response := meetingDraftResponse{
		MeetingTitle:     title,
		Participants:     participants,
		SelectedFormats:  selectedFormatsToStrings(selectedFormats),
		ReportSourceMode: meetingReportSourceMode,
		ReportProvider:   meetingReportProvider,
		ModelID:          generatorModelID(generator),
		GeneratedAt:      generatedAt,
		SourceTokenCount: sourceTokenCount,
		Reports:          buildMeetingReportEnvelopeList(selectedFormats, draftsByFormat),
	}
	logMeetingStage(c, meetingDraftsRoute, traceID, "response_ready", title, map[string]any{
		"draft_count": len(response.Reports),
		"model_id":    response.ModelID,
	})
	return c.JSON(response)
}

func (a *App) finalizeMeeting(c *fiber.Ctx) error {
	traceID := requestTraceID(c)
	logMeetingStage(c, meetingFinalizeRoute, traceID, "request_received", "", nil)

	claims := MustClaims(c)
	if claims == nil {
		logMeetingStage(c, meetingFinalizeRoute, traceID, "request_unauthorized", "", map[string]any{
			"error": "unauthorized",
		})
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}

	var req meetingRequest
	if err := c.BodyParser(&req); err != nil {
		logMeetingStage(c, meetingFinalizeRoute, traceID, "request_parse_error", "", map[string]any{
			"error": err.Error(),
		})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}

	title := normalizeMeetingTitle(req.MeetingTitle)
	participants := normalizeStringList(req.Participants)
	rawTranscript := resolveMeetingTranscriptForMail(req.RawTranscriptText, req.EditedTranscriptText)
	reportSourceText := buildMeetingReportSourceText(req.EditedTranscriptText, req.RawTranscriptText, req.SpeakerAssignments)
	logMeetingStage(c, meetingFinalizeRoute, traceID, "request_parsed", title, map[string]any{
		"participant_count":         len(participants),
		"speaker_assignment_count":  len(req.SpeakerAssignments),
		"selected_format_count":     len(req.SelectedFormats),
		"recipient_count_requested": len(req.RecipientEmails),
		"source_text_present":       rawTranscript != "",
		"report_source_present":     reportSourceText != "",
	})
	if rawTranscript == "" || reportSourceText == "" {
		logMeetingStage(c, meetingFinalizeRoute, traceID, "validation_error", title, map[string]any{
			"error": "transcript text is required",
		})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "transcript text is required"})
	}

	transcriptionSourceMode, transcriptionProvider, err := normalizeMeetingTranscriptionSource(req.TranscriptionSourceMode, req.TranscriptionProvider)
	if err != nil {
		logMeetingStage(c, meetingFinalizeRoute, traceID, "source_normalization_error", title, map[string]any{
			"error": err.Error(),
		})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: err.Error()})
	}

	selectedFormats := normalizeMeetingFormats(req.SelectedFormats)
	if len(selectedFormats) == 0 && len(req.Reports) > 0 {
		selectedFormats = extractMeetingReportFormats(req.Reports)
	}
	if len(selectedFormats) == 0 {
		selectedFormats = meetingreports.AllReportFormats()
	}
	logMeetingStage(c, meetingFinalizeRoute, traceID, "request_normalized", title, map[string]any{
		"participant_count":         len(participants),
		"selected_format_count":     len(selectedFormats),
		"selected_formats":          strings.Join(selectedFormatsToStrings(selectedFormats), ","),
		"transcription_source_mode": transcriptionSourceMode,
		"transcription_provider":    transcriptionProvider,
	})

	if err := a.recordMeetingActivityEvent(claims, "transcription", transcriptionSourceMode, transcriptionProvider, "success", map[string]any{
		"title":                  title,
		"participantCount":       len(participants),
		"speakerCount":           len(req.SpeakerAssignments),
		"rawTranscriptTokens":    approximateTokenCount(rawTranscript),
		"editedTranscriptTokens": approximateTokenCount(reportSourceText),
		"selectedFormats":        selectedFormatsToStrings(selectedFormats),
	}); err != nil {
		logMeetingStage(c, meetingFinalizeRoute, traceID, "activity_ingest_error", title, map[string]any{
			"error": err.Error(),
		})
	}

	logMeetingStage(c, meetingFinalizeRoute, traceID, "documents_start", title, map[string]any{
		"report_model_id":       req.ReportModelID,
		"selected_format_count": len(selectedFormats),
		"participant_count":     len(participants),
	})
	attachments, reportEnvelopes, err := a.buildMeetingDocuments(
		requestContext(c),
		req.ReportModelID,
		meetingReportMaxTokens(req.ReportMaxTokens),
		meetingReportTemperature(req.ReportTemperature),
		title,
		participants,
		transcriptionSourceMode,
		rawTranscript,
		reportSourceText,
		selectedFormats,
		req.Reports,
	)
	if err != nil {
		logMeetingStage(c, meetingFinalizeRoute, traceID, "documents_error", title, map[string]any{
			"error": err.Error(),
		})
		_ = a.recordMeetingActivityEvent(claims, "report", meetingReportSourceMode, meetingReportProvider, "error", map[string]any{
			"title":           title,
			"selectedFormats": selectedFormatsToStrings(selectedFormats),
			"message":         err.Error(),
		})
		return a.meetingFinalizeErrorResponse(c, err)
	}
	logMeetingStage(c, meetingFinalizeRoute, traceID, "documents_success", title, map[string]any{
		"attachment_count": len(attachments),
		"report_count":     len(reportEnvelopes),
	})

	toEmail := strings.TrimSpace(claims.Email)
	if toEmail == "" {
		user, userErr := a.Store.GetUserByID(requestContext(c), claims.UserID)
		if userErr != nil || user == nil || strings.TrimSpace(user.Email) == "" {
			logMeetingStage(c, meetingFinalizeRoute, traceID, "recipient_resolution_error", title, map[string]any{
				"error": "recipient email unavailable",
			})
			_ = a.recordMeetingActivityEvent(claims, "report", meetingReportSourceMode, meetingReportProvider, "error", map[string]any{
				"title":           title,
				"selectedFormats": selectedFormatsToStrings(selectedFormats),
				"message":         "recipient email unavailable",
			})
			return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "recipient email unavailable"})
		}
		toEmail = strings.TrimSpace(user.Email)
	}

	recipients, err := normalizeRecipientEmails(append([]string{toEmail}, req.RecipientEmails...))
	if err != nil {
		logMeetingStage(c, meetingFinalizeRoute, traceID, "recipient_normalization_error", title, map[string]any{
			"error": "invalid recipient email",
		})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: err.Error()})
	}
	if len(recipients) == 0 {
		logMeetingStage(c, meetingFinalizeRoute, traceID, "recipient_resolution_error", title, map[string]any{
			"error": "recipient email unavailable",
		})
		_ = a.recordMeetingActivityEvent(claims, "report", meetingReportSourceMode, meetingReportProvider, "error", map[string]any{
			"title":           title,
			"selectedFormats": selectedFormatsToStrings(selectedFormats),
			"message":         "recipient email unavailable",
		})
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "recipient email unavailable"})
	}
	logMeetingStage(c, meetingFinalizeRoute, traceID, "recipients_resolved", title, map[string]any{
		"recipient_count": len(recipients),
	})

	if a.Mailer == nil {
		logMeetingStage(c, meetingFinalizeRoute, traceID, "mailer_unavailable", title, map[string]any{
			"error": "mailer unavailable",
		})
		_ = a.recordMeetingActivityEvent(claims, "report", meetingReportSourceMode, meetingReportProvider, "error", map[string]any{
			"title":           title,
			"selectedFormats": selectedFormatsToStrings(selectedFormats),
			"message":         "mailer unavailable",
		})
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "mailer unavailable"})
	}
	if err := a.Mailer.Ready(); err != nil {
		logMeetingStage(c, meetingFinalizeRoute, traceID, "mailer_ready_error", title, map[string]any{
			"error": err.Error(),
		})
		_ = a.recordMeetingActivityEvent(claims, "report", meetingReportSourceMode, meetingReportProvider, "error", map[string]any{
			"title":           title,
			"selectedFormats": selectedFormatsToStrings(selectedFormats),
			"message":         err.Error(),
		})
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "mailer unavailable"})
	}
	logMeetingStage(c, meetingFinalizeRoute, traceID, "mailer_ready", title, map[string]any{
		"recipient_count": len(recipients),
	})

	subject := buildMeetingSubject(title)
	textBody, htmlBody := buildMeetingEmailBodies(title, participants, transcriptionSourceMode, reportEnvelopes)
	for index, recipient := range recipients {
		logMeetingStage(c, meetingFinalizeRoute, traceID, "send_start", title, map[string]any{
			"recipient_index": index + 1,
			"recipient_count": len(recipients),
		})
		if err := a.Mailer.SendMeetingSummaryEmail(requestContext(c), mailer.MeetingSummaryEmail{
			ToEmail:     recipient,
			Subject:     subject,
			TextBody:    textBody,
			HTMLBody:    htmlBody,
			Attachments: attachments,
		}); err != nil {
			logMeetingStage(c, meetingFinalizeRoute, traceID, "send_error", title, map[string]any{
				"recipient_index": index + 1,
				"recipient_count": len(recipients),
				"error":           err.Error(),
			})
			_ = a.recordMeetingActivityEvent(claims, "report", meetingReportSourceMode, meetingReportProvider, "error", map[string]any{
				"title":           title,
				"selectedFormats": selectedFormatsToStrings(selectedFormats),
				"recipientCount":  len(recipients),
				"message":         err.Error(),
			})
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to send meeting email"})
		}
		logMeetingStage(c, meetingFinalizeRoute, traceID, "send_success", title, map[string]any{
			"recipient_index": index + 1,
			"recipient_count": len(recipients),
		})
	}

	_ = a.recordMeetingActivityEvent(claims, "report", meetingReportSourceMode, meetingReportProvider, "success", map[string]any{
		"title":           title,
		"selectedFormats": selectedFormatsToStrings(selectedFormats),
		"attachmentCount": len(attachments),
		"recipientCount":  len(recipients),
	})

	response := meetingFinalizeResponse{
		MeetingTitle:            title,
		Participants:            participants,
		TranscriptionSourceMode: transcriptionSourceMode,
		TranscriptionProvider:   transcriptionProvider,
		ReportSourceMode:        meetingReportSourceMode,
		ReportProvider:          meetingReportProvider,
		SelectedFormats:         selectedFormatsToStrings(selectedFormats),
		SentTo:                  strings.Join(recipients, ", "),
		SentToEmails:            recipients,
		GeneratedAt:             time.Now().UTC().Format(time.RFC3339),
		TranscriptDocxFilename:  extractTranscriptFilename(attachments),
		ReportDocxFilenames:     extractReportFilenames(attachments),
		Attachments:             summarizeAttachments(attachments),
	}
	logMeetingStage(c, meetingFinalizeRoute, traceID, "response_ready", title, map[string]any{
		"attachment_count": len(attachments),
		"recipient_count":  len(recipients),
	})
	return c.JSON(response)
}

func (a *App) newMeetingReportGenerator(modelID string, maxTokens int, temperature float64) (*meetingreports.Generator, error) {
	if a.MistralClient == nil || !a.MistralClient.IsConfigured() {
		return nil, fmt.Errorf("mistral client is not configured")
	}
	return &meetingreports.Generator{
		Client:      a.MistralClient,
		ModelID:     modelID,
		MaxTokens:   maxTokens,
		Temperature: temperature,
	}, nil
}

func (a *App) generateMeetingReportDrafts(
	ctx context.Context,
	generator *meetingreports.Generator,
	meetingTitle string,
	participants []string,
	sourceText string,
	formats []meetingreports.ReportFormat,
	generatedAt string,
	sourceTokenCount int,
) (map[meetingreports.ReportFormat]meetingReportEnvelope, error) {
	if generator == nil {
		return nil, fmt.Errorf("mistral client is not configured")
	}
	results, err := generator.GenerateReports(ctx, meetingTitle, participants, sourceText, formats)
	if err != nil {
		return nil, err
	}
	out := make(map[meetingreports.ReportFormat]meetingReportEnvelope, len(results))
	for format, generated := range results {
		out[format] = meetingReportEnvelope{
			Format:           string(format),
			Report:           generated.Report,
			Raw:              generated.Raw,
			ModelID:          generatorModelID(generator),
			GeneratedAt:      generatedAt,
			SourceMode:       meetingReportSourceMode,
			Provider:         meetingReportProvider,
			SourceTokenCount: sourceTokenCount,
		}
	}
	return out, nil
}

func (a *App) buildMeetingDocuments(
	ctx context.Context,
	reportModelID string,
	reportMaxTokens int,
	reportTemperature float64,
	meetingTitle string,
	participants []string,
	transcriptionSourceMode string,
	rawTranscript string,
	reportSourceText string,
	selectedFormats []meetingreports.ReportFormat,
	providedReports []meetingReportEnvelope,
) ([]mailer.MailAttachment, map[meetingreports.ReportFormat]meetingReportEnvelope, error) {
	now := time.Now().UTC()
	generatedAt := now.Format(time.RFC3339)
	sourceTokenCount := approximateTokenCount(reportSourceText)

	reportEnvelopes, err := normalizeProvidedMeetingReports(providedReports)
	if err != nil {
		return nil, nil, err
	}

	missingFormats := make([]meetingreports.ReportFormat, 0)
	for _, format := range selectedFormats {
		if _, ok := reportEnvelopes[format]; ok {
			continue
		}
		missingFormats = append(missingFormats, format)
	}

	if len(missingFormats) > 0 {
		generator, err := a.newMeetingReportGenerator(reportModelID, reportMaxTokens, reportTemperature)
		if err != nil {
			return nil, nil, err
		}
		generatedReports, err := generator.GenerateReports(ctx, meetingTitle, participants, reportSourceText, missingFormats)
		if err != nil {
			return nil, nil, err
		}
		for format, generated := range generatedReports {
			reportEnvelopes[format] = meetingReportEnvelope{
				Format:           string(format),
				Report:           generated.Report,
				Raw:              generated.Raw,
				ModelID:          generatorModelID(generator),
				GeneratedAt:      generatedAt,
				SourceMode:       meetingReportSourceMode,
				Provider:         meetingReportProvider,
				SourceTokenCount: sourceTokenCount,
			}
		}
	}

	attachments := make([]mailer.MailAttachment, 0, len(selectedFormats)+1)
	rawTranscriptDocx, err := meetingreports.BuildTranscriptDocx(
		meetingTitle,
		participants,
		transcriptionSourceMode,
		rawTranscript,
		nil,
		meetingreports.TranscriptDocxMetadata{
			Title:            meetingTitle,
			GeneratedAt:      generatedAt,
			SourceMode:       transcriptionSourceMode,
			SourceTokenCount: approximateTokenCount(rawTranscript),
		},
	)
	if err != nil {
		return nil, nil, err
	}
	attachments = append(attachments, mailer.MailAttachment{
		Filename:    meetingreports.TranscriptDocxFilename(now),
		ContentType: mailer.DocxContentType,
		Data:        rawTranscriptDocx,
	})

	for _, format := range selectedFormats {
		envelope, ok := reportEnvelopes[format]
		if !ok {
			return nil, nil, fmt.Errorf("missing report for format %s", meetingreports.ReportFormatDisplayName(format))
		}
		docx, err := meetingreports.BuildReportDocx(envelope.Report, meetingreports.ReportDocxMetadata{
			Format:           format,
			ModelID:          envelope.ModelID,
			GeneratedAt:      envelope.GeneratedAt,
			SourceMode:       envelope.SourceMode,
			SourceTokenCount: envelope.SourceTokenCount,
		})
		if err != nil {
			return nil, nil, err
		}
		attachments = append(attachments, mailer.MailAttachment{
			Filename:    meetingreports.ReportDocxFilename(meetingreports.ReportFormatKey(format), now),
			ContentType: mailer.DocxContentType,
			Data:        docx,
		})
	}

	return attachments, reportEnvelopes, nil
}

func normalizeMeetingTitle(value string) string {
	title := strings.TrimSpace(value)
	if title == "" {
		return "Réunion"
	}
	return title
}

func normalizeStringList(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func resolveMeetingSourceText(editedText, rawText string) string {
	if trimmed := strings.TrimSpace(editedText); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(rawText)
}

func resolveMeetingTranscriptForMail(rawText, editedText string) string {
	if trimmed := strings.TrimSpace(rawText); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(editedText)
}

func buildMeetingReportSourceText(editedText, rawText string, speakerAssignments []meetingreports.SpeakerAssignment) string {
	transcript := resolveMeetingSourceText(editedText, rawText)
	parts := make([]string, 0, 2)
	if len(speakerAssignments) > 0 {
		lines := make([]string, 0, len(speakerAssignments)+1)
		lines = append(lines, "Assignation des speakers:")
		for _, assignment := range speakerAssignments {
			speakerID := strings.TrimSpace(assignment.SpeakerID)
			label := joinNameParts(assignment.FirstName, assignment.LastName)
			if label == "" {
				label = speakerID
			}
			if speakerID != "" && label != "" {
				lines = append(lines, fmt.Sprintf("- %s: %s", speakerID, label))
			} else if label != "" {
				lines = append(lines, "- "+label)
			}
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}
	if transcript != "" {
		parts = append(parts, "Transcription:\n"+transcript)
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func joinNameParts(parts ...string) string {
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			filtered = append(filtered, trimmed)
		}
	}
	return strings.Join(filtered, " ")
}

func normalizeMeetingFormats(values []string) []meetingreports.ReportFormat {
	formats := meetingreports.NormalizeReportFormats(values)
	if len(formats) == 0 {
		return meetingreports.AllReportFormats()
	}
	return formats
}

func selectedFormatsToStrings(formats []meetingreports.ReportFormat) []string {
	out := make([]string, 0, len(formats))
	for _, format := range formats {
		out = append(out, string(format))
	}
	return out
}

func normalizeRecipientEmails(values []string) ([]string, error) {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, raw := range values {
		candidate := strings.TrimSpace(raw)
		if candidate == "" {
			continue
		}
		addr, err := mail.ParseAddress(candidate)
		if err != nil {
			return nil, fmt.Errorf("invalid recipient email: %s", candidate)
		}
		email := strings.ToLower(strings.TrimSpace(addr.Address))
		if email == "" {
			continue
		}
		if _, exists := seen[email]; exists {
			continue
		}
		seen[email] = struct{}{}
		out = append(out, email)
	}
	return out, nil
}

func extractMeetingReportFormats(reports []meetingReportEnvelope) []meetingreports.ReportFormat {
	formats := make([]meetingreports.ReportFormat, 0, len(reports))
	seen := map[meetingreports.ReportFormat]struct{}{}
	for _, report := range reports {
		format, ok := meetingreports.ParseReportFormat(report.Format)
		if !ok {
			continue
		}
		if _, exists := seen[format]; exists {
			continue
		}
		seen[format] = struct{}{}
		formats = append(formats, format)
	}
	if len(formats) == 0 {
		return meetingreports.AllReportFormats()
	}
	return formats
}

func buildMeetingSubject(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Réunion"
	}
	return "Compte rendu de réunion - " + title
}

func buildMeetingEmailBodies(title string, participants []string, transcriptionSourceMode string, reports map[meetingreports.ReportFormat]meetingReportEnvelope) (string, string) {
	summaryBullets := collectMeetingHighlights(reports)
	reportFormats := extractMeetingReportFormatsFromMap(reports)
	participantsLine := "Aucun participant fourni."
	if len(participants) > 0 {
		participantsLine = strings.Join(participants, ", ")
	}
	sourceLabel := transcriptionSourceModeLabel(transcriptionSourceMode)

	textLines := []string{
		"Bonjour,",
		"",
		fmt.Sprintf("La reunion \"%s\" est terminee.", title),
		fmt.Sprintf("Participants: %s", participantsLine),
		fmt.Sprintf("Source de transcription: %s.", sourceLabel),
		fmt.Sprintf("Rapports joints: %s.", strings.Join(reportFormats, ", ")),
		"",
		"Resume:",
	}
	if len(summaryBullets) == 0 {
		textLines = append(textLines, "- Aucun point saillant supplementaire n a ete extrait.")
	} else {
		for _, bullet := range summaryBullets {
			textLines = append(textLines, "- "+bullet)
		}
	}
	textLines = append(textLines,
		"",
		"La transcription brute est jointe en DOCX.",
		"Cordialement,",
		"Demeter Speech",
	)

	htmlBullets := make([]string, 0, len(summaryBullets))
	for _, bullet := range summaryBullets {
		htmlBullets = append(htmlBullets, "<li>"+html.EscapeString(bullet)+"</li>")
	}
	if len(htmlBullets) == 0 {
		htmlBullets = append(htmlBullets, "<li>Aucun point saillant supplementaire n a ete extrait.</li>")
	}
	htmlBody := "<html><body style=\"font-family:Arial,sans-serif;color:#1f2937;line-height:1.5\">" +
		"<p>Bonjour,</p>" +
		"<p>La reunion <strong>" + html.EscapeString(title) + "</strong> est terminee.</p>" +
		"<p><strong>Participants :</strong> " + html.EscapeString(participantsLine) + "</p>" +
		"<p><strong>Source de transcription :</strong> " + html.EscapeString(sourceLabel) + "</p>" +
		"<p><strong>Rapports joints :</strong> " + html.EscapeString(strings.Join(reportFormats, ", ")) + "</p>" +
		"<p><strong>Resume :</strong></p><ul>" + strings.Join(htmlBullets, "") + "</ul>" +
		"<p>La transcription brute est jointe en DOCX.</p>" +
		"<p>Cordialement,<br/>Demeter Speech</p>" +
		"</body></html>"

	return strings.Join(textLines, "\n"), htmlBody
}

func collectMeetingHighlights(reports map[meetingreports.ReportFormat]meetingReportEnvelope) []string {
	orderedFormats := meetingreports.AllReportFormats()
	highlights := make([]string, 0, 3)
	seen := map[string]struct{}{}
	for _, format := range orderedFormats {
		envelope, ok := reports[format]
		if !ok {
			continue
		}
		for _, value := range append(append([]string{}, envelope.Report.KeyPoints...), envelope.Report.ActionItems...) {
			trimmed := strings.TrimSpace(value)
			if trimmed == "" {
				continue
			}
			key := strings.ToLower(trimmed)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			highlights = append(highlights, trimmed)
			if len(highlights) >= 3 {
				return highlights
			}
		}
		for _, section := range envelope.Report.Sections {
			for _, paragraph := range section.Paragraphs {
				trimmed := strings.TrimSpace(paragraph)
				if trimmed == "" {
					continue
				}
				key := strings.ToLower(trimmed)
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
				highlights = append(highlights, trimmed)
				if len(highlights) >= 3 {
					return highlights
				}
			}
		}
	}
	return highlights
}

func extractMeetingReportFormatsFromMap(reports map[meetingreports.ReportFormat]meetingReportEnvelope) []string {
	formats := meetingreports.AllReportFormats()
	out := make([]string, 0, len(formats))
	for _, format := range formats {
		if _, ok := reports[format]; ok {
			out = append(out, string(format))
		}
	}
	if len(out) == 0 {
		for format := range reports {
			out = append(out, string(format))
		}
	}
	return out
}

func transcriptionSourceModeLabel(sourceMode string) string {
	switch strings.ToLower(strings.TrimSpace(sourceMode)) {
	case "local":
		return "local"
	case meetingReportSourceMode:
		return "Demeter Santé"
	default:
		return strings.TrimSpace(sourceMode)
	}
}

func normalizeMeetingTranscriptionSource(mode, provider string) (string, string, error) {
	normalizedMode := strings.ToLower(strings.TrimSpace(mode))
	normalizedProvider := strings.ToLower(strings.TrimSpace(provider))

	switch normalizedMode {
	case "", "local":
		if normalizedProvider == "" {
			normalizedProvider = "mic"
		}
		if normalizedProvider != "mic" && normalizedProvider != "local_upload" {
			return "", "", fmt.Errorf("invalid transcription provider for local source")
		}
		return "local", normalizedProvider, nil
	case meetingReportSourceMode, "demeter_sante", "backend", "cloud-backend":
		if normalizedProvider == "" {
			normalizedProvider = meetingReportProvider
		}
		if normalizedProvider != meetingReportProvider {
			return "", "", fmt.Errorf("invalid transcription provider for backend source")
		}
		return meetingReportSourceMode, meetingReportProvider, nil
	default:
		return "", "", fmt.Errorf("invalid transcription source mode")
	}
}

func normalizeProvidedMeetingReports(inputs []meetingReportEnvelope) (map[meetingreports.ReportFormat]meetingReportEnvelope, error) {
	if len(inputs) == 0 {
		return map[meetingreports.ReportFormat]meetingReportEnvelope{}, nil
	}
	out := make(map[meetingreports.ReportFormat]meetingReportEnvelope, len(inputs))
	for _, input := range inputs {
		parsed, err := normalizeProvidedMeetingReport(input)
		if err != nil {
			return nil, err
		}
		format, ok := meetingreports.ParseReportFormat(parsed.Format)
		if !ok {
			return nil, fmt.Errorf("invalid report format")
		}
		out[format] = parsed
	}
	return out, nil
}

func normalizeProvidedMeetingReport(input meetingReportEnvelope) (meetingReportEnvelope, error) {
	formatCandidate := strings.TrimSpace(input.Format)
	if formatCandidate == "" && input.Report.Format != "" {
		formatCandidate = string(input.Report.Format)
	}
	format, ok := meetingreports.ParseReportFormat(formatCandidate)
	if !ok {
		return meetingReportEnvelope{}, fmt.Errorf("invalid report format")
	}

	raw := strings.TrimSpace(input.Raw)
	if raw == "" {
		marshaled, err := json.Marshal(input.Report)
		if err != nil {
			return meetingReportEnvelope{}, err
		}
		raw = string(marshaled)
	}
	normalizedReport, err := meetingreports.ParseReportJSON(raw, format)
	if err != nil {
		return meetingReportEnvelope{}, err
	}

	return meetingReportEnvelope{
		Format:           string(format),
		Report:           normalizedReport,
		Raw:              raw,
		ModelID:          strings.TrimSpace(input.ModelID),
		GeneratedAt:      strings.TrimSpace(input.GeneratedAt),
		SourceMode:       strings.TrimSpace(input.SourceMode),
		Provider:         strings.TrimSpace(input.Provider),
		SourceTokenCount: input.SourceTokenCount,
	}, nil
}

func buildMeetingReportEnvelopeList(selectedFormats []meetingreports.ReportFormat, reports map[meetingreports.ReportFormat]meetingReportEnvelope) []meetingReportEnvelope {
	out := make([]meetingReportEnvelope, 0, len(selectedFormats))
	for _, format := range selectedFormats {
		envelope, ok := reports[format]
		if !ok {
			continue
		}
		out = append(out, envelope)
	}
	return out
}

func approximateTokenCount(text string) int {
	return len(strings.Fields(strings.TrimSpace(text)))
}

func meetingReportMaxTokens(value int) int {
	if value > 0 {
		return value
	}
	return meetingreports.DefaultReportMaxTokens
}

func meetingReportTemperature(value float64) float64 {
	if value < 0 || value > 2 {
		return meetingreports.DefaultReportTemp
	}
	return value
}

func generatorModelID(generator *meetingreports.Generator) string {
	if generator == nil {
		return meetingreports.DefaultReportModelID
	}
	if strings.TrimSpace(generator.ModelID) == "" {
		return meetingreports.DefaultReportModelID
	}
	return strings.TrimSpace(generator.ModelID)
}

func extractTranscriptFilename(attachments []mailer.MailAttachment) string {
	for _, attachment := range attachments {
		if strings.HasPrefix(strings.ToLower(attachment.Filename), "transcription-") {
			return attachment.Filename
		}
	}
	if len(attachments) > 0 {
		return attachments[0].Filename
	}
	return ""
}

func extractReportFilenames(attachments []mailer.MailAttachment) []string {
	out := make([]string, 0, len(attachments))
	for _, attachment := range attachments {
		if strings.HasPrefix(strings.ToLower(attachment.Filename), "transcription-") {
			continue
		}
		out = append(out, attachment.Filename)
	}
	return out
}

func summarizeAttachments(attachments []mailer.MailAttachment) []meetingAttachmentResponse {
	out := make([]meetingAttachmentResponse, 0, len(attachments))
	for _, attachment := range attachments {
		out = append(out, meetingAttachmentResponse{
			Filename:    attachment.Filename,
			ContentType: attachment.ContentType,
			SizeBytes:   len(attachment.Data),
		})
	}
	return out
}

func logMeetingStage(c *fiber.Ctx, route, traceID, step, title string, fields map[string]any) {
	userID, orgID := demeterActorIDs(c)
	log.Print(observability.FormatStepLine("meetings", route, step, traceID, userID, orgID, title, fields))
}

func (a *App) recordMeetingActivityEvent(
	claims *auth.Claims,
	eventKind string,
	sourceMode string,
	provider string,
	status string,
	meta map[string]any,
) error {
	if a.Store == nil || claims == nil {
		return fmt.Errorf("activity store unavailable")
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	_, err = a.Store.IngestActivityEvents(context.Background(), claims.OrgID, claims.UserID, []store.ActivityEventInput{
		{
			EventID:    uuid.NewString(),
			EventKind:  strings.ToLower(strings.TrimSpace(eventKind)),
			SourceMode: strings.ToLower(strings.TrimSpace(sourceMode)),
			Provider:   strings.ToLower(strings.TrimSpace(provider)),
			Status:     strings.ToLower(strings.TrimSpace(status)),
			OccurredAt: time.Now().UTC(),
			MetaJSON:   metaJSON,
		},
	})
	return err
}

func (a *App) meetingDraftErrorResponse(c *fiber.Ctx, err error) error {
	if err == nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to generate report drafts"})
	}
	if strings.Contains(strings.ToLower(err.Error()), "not configured") {
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: err.Error()})
	}
	return c.Status(fiber.StatusBadGateway).JSON(ErrorResponse{Error: err.Error()})
}

func (a *App) meetingFinalizeErrorResponse(c *fiber.Ctx, err error) error {
	if err == nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to finalize meeting"})
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "mailer unavailable"):
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "mailer unavailable"})
	case strings.Contains(msg, "not configured"):
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: err.Error()})
	case strings.Contains(msg, "invalid report format"), strings.Contains(msg, "transcript text is required"), strings.Contains(msg, "invalid transcription"):
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: err.Error()})
	default:
		return c.Status(fiber.StatusBadGateway).JSON(ErrorResponse{Error: err.Error()})
	}
}
