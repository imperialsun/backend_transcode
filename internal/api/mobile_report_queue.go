package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	meetingreports "demeter-backend/internal/reports"
	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
)

const (
	mobileReportQueuePollInterval = 250 * time.Millisecond
	mobileReportQueueWaitTimeout  = 90 * time.Minute
)

type mobileQueuedReportBatchResult struct {
	batchIndex int
	report     meetingreports.ReportJson
	err        error
}

// normalizeMobileSelectedReportFormats validates the selected report formats
// and preserves the caller order.
func normalizeMobileSelectedReportFormats(values []string) ([]meetingreports.ReportFormat, error) {
	seen := map[meetingreports.ReportFormat]struct{}{}
	formats := make([]meetingreports.ReportFormat, 0, len(values))
	for _, raw := range values {
		format, ok := meetingreports.ParseReportFormat(raw)
		if !ok {
			trimmed := strings.TrimSpace(raw)
			if trimmed == "" {
				continue
			}
			return nil, fmt.Errorf("invalid selected format: %s", trimmed)
		}
		if _, exists := seen[format]; exists {
			continue
		}
		seen[format] = struct{}{}
		formats = append(formats, format)
	}
	if len(formats) == 0 {
		return nil, fmt.Errorf("selected formats are required")
	}
	return formats, nil
}

func mobileReportQueueOperationID(operationID string, format meetingreports.ReportFormat, formatIndex, formatCount, batchIndex, batchCount int) string {
	parts := []string{strings.TrimSpace(operationID), strings.ToLower(string(format))}
	if formatIndex > 0 {
		parts = append(parts, fmt.Sprintf("f%02d", formatIndex))
	}
	if formatCount > 0 {
		parts = append(parts, fmt.Sprintf("of%02d", formatCount))
	}
	if batchIndex > 0 {
		parts = append(parts, fmt.Sprintf("b%02d", batchIndex))
	}
	if batchCount > 0 {
		parts = append(parts, fmt.Sprintf("of%02d", batchCount))
	}
	return strings.Join(parts, "-")
}

func (a *App) generateMobileReportEnvelopesWithQueue(
	ctx context.Context,
	traceID string,
	route string,
	operationID string,
	organizationID string,
	userID string,
	settings mobileReportSettings,
	title string,
	participants []string,
	sourceText string,
	formats []meetingreports.ReportFormat,
) (map[meetingreports.ReportFormat]mobileReportEnvelope, error) {
	if len(formats) == 0 {
		return nil, fmt.Errorf("selected formats are required")
	}

	type result struct {
		format   meetingreports.ReportFormat
		envelope mobileReportEnvelope
		err      error
	}

	results := make(chan result, len(formats))
	var wg sync.WaitGroup
	for index, format := range formats {
		index := index
		format := format
		wg.Add(1)
		go func() {
			defer wg.Done()
			envelope, err := a.generateMobileReportEnvelopeWithQueue(ctx, traceID, route, operationID, organizationID, userID, settings, title, participants, sourceText, format, index+1, len(formats))
			results <- result{format: format, envelope: envelope, err: err}
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	envelopes := make(map[meetingreports.ReportFormat]mobileReportEnvelope, len(formats))
	var firstErr error
	for item := range results {
		if item.err != nil {
			if firstErr == nil {
				firstErr = item.err
			}
			continue
		}
		envelopes[item.format] = item.envelope
	}

	if firstErr != nil {
		return nil, firstErr
	}
	return envelopes, nil
}

func (a *App) generateMobileReportEnvelopeWithQueue(
	ctx context.Context,
	traceID string,
	route string,
	operationID string,
	organizationID string,
	userID string,
	settings mobileReportSettings,
	title string,
	participants []string,
	sourceText string,
	format meetingreports.ReportFormat,
	formatIndex int,
	formatCount int,
) (mobileReportEnvelope, error) {
	detailLevel := settings.DetailLevels[format]
	if format == meetingreports.ReportFormatCRN {
		report, raw, generatedAt, err := a.generateMobileCRNReportWithQueue(ctx, traceID, route, operationID, organizationID, userID, settings, title, participants, sourceText, formatIndex, formatCount)
		if err != nil {
			return mobileReportEnvelope{}, err
		}
		return mobileReportEnvelope{
			Format:           string(format),
			Report:           report,
			Raw:              raw,
			ModelID:          settings.ModelID,
			GeneratedAt:      generatedAt,
			SourceMode:       mobileReportSourceMode,
			Provider:         mobileReportProvider,
			SourceTokenCount: approximateTokenCount(sourceText),
			DetailLevel:      string(detailLevel),
		}, nil
	}

	result, err := a.generateMobileQueuedReportResult(ctx, traceID, route, operationID, organizationID, userID, settings, title, participants, sourceText, format, detailLevel, formatIndex, formatCount, 0, 0)
	if err != nil {
		return mobileReportEnvelope{}, err
	}
	return mobileReportEnvelope{
		Format:           string(format),
		Report:           result.Report,
		Raw:              result.Raw,
		ModelID:          result.ModelID,
		GeneratedAt:      result.GeneratedAt,
		SourceMode:       mobileReportSourceMode,
		Provider:         mobileReportProvider,
		SourceTokenCount: approximateTokenCount(sourceText),
		DetailLevel:      string(detailLevel),
	}, nil
}

func (a *App) generateMobileCRNReportWithQueue(
	ctx context.Context,
	traceID string,
	route string,
	operationID string,
	organizationID string,
	userID string,
	settings mobileReportSettings,
	title string,
	participants []string,
	sourceText string,
	formatIndex int,
	formatCount int,
) (meetingreports.ReportJson, string, string, error) {
	batches := meetingreports.BuildCrnTranscriptBatches(sourceText, 10, 0)
	if len(batches) == 0 {
		return meetingreports.ReportJson{}, "", "", fmt.Errorf("source text is required")
	}

	type batchResult struct {
		index  int
		report meetingreports.ReportJson
		err    error
	}

	results := make(chan batchResult, len(batches))
	var wg sync.WaitGroup
	for _, batch := range batches {
		batch := batch
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := a.generateMobileQueuedReportResult(ctx, traceID, route, operationID, organizationID, userID, settings, title, participants, batch.Text, meetingreports.ReportFormatCRN, settings.DetailLevels[meetingreports.ReportFormatCRN], formatIndex, formatCount, batch.BatchIndex, batch.BatchCount)
			if err != nil {
				results <- batchResult{index: batch.BatchIndex, err: err}
				return
			}
			results <- batchResult{index: batch.BatchIndex, report: result.Report}
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	mergedReports := make([]meetingreports.ReportJson, len(batches))
	var firstErr error
	for item := range results {
		if item.err != nil {
			if firstErr == nil {
				firstErr = item.err
			}
			continue
		}
		if item.index <= 0 || item.index > len(mergedReports) {
			if firstErr == nil {
				firstErr = fmt.Errorf("invalid CRN batch index")
			}
			continue
		}
		mergedReports[item.index-1] = item.report
	}

	if firstErr != nil {
		return meetingreports.ReportJson{}, "", "", firstErr
	}

	mergedReport, err := meetingreports.MergeCrnReportResults(mergedReports)
	if err != nil {
		return meetingreports.ReportJson{}, "", "", err
	}
	raw, err := json.Marshal(mergedReport)
	if err != nil {
		return meetingreports.ReportJson{}, "", "", err
	}
	return mergedReport, string(raw), time.Now().UTC().Format(time.RFC3339), nil
}

func (a *App) generateMobileQueuedReportResult(
	ctx context.Context,
	traceID string,
	route string,
	operationID string,
	organizationID string,
	userID string,
	settings mobileReportSettings,
	title string,
	participants []string,
	sourceText string,
	format meetingreports.ReportFormat,
	detailLevel meetingreports.ReportDetailLevel,
	formatIndex int,
	formatCount int,
	batchIndex int,
	batchCount int,
) (*demeterReportResult, error) {
	if a.MistralClient == nil || !a.MistralClient.IsConfigured() {
		return nil, fmt.Errorf("mistral client is not configured")
	}
	if strings.TrimSpace(sourceText) == "" {
		return nil, fmt.Errorf("source text is required")
	}

	// Mobile parent operations fan out into regular Demeter report operations.
	// The derived ID keeps retries idempotent per format/batch while letting
	// the existing report queue own generation, polling, and cleanup.
	queueOperationID := mobileReportQueueOperationID(operationID, format, formatIndex, formatCount, batchIndex, batchCount)
	payload := &demeterReportQueueOperationPayload{
		TraceID:      traceID,
		Route:        route,
		Seq:          nextDemeterReportOperationSequenceID(),
		MeetingTitle: strings.TrimSpace(title),
		Participants: append([]string(nil), participants...),
		SourceText:   sourceText,
		Format:       format,
		DetailLevel:  detailLevel,
		ModelID:      settings.ModelID,
		Temperature:  settings.Temperature,
		MaxTokens:    settings.MaxTokens,
		CreatedAt:    time.Now().UTC(),
	}
	rawPayload, _ := json.Marshal(payload)
	now := time.Now().UTC()
	record := &store.DemeterReportOperationRecord{
		OperationID:      queueOperationID,
		OrganizationID:   organizationID,
		UserID:           userID,
		Status:           store.DemeterReportOperationStatusPending,
		Stage:            "queued",
		FormatIndex:      formatIndex,
		FormatCount:      formatCount,
		Progress:         0,
		QueuePayloadJSON: sql.NullString{String: string(rawPayload), Valid: true},
		StatusCode:       fiber.StatusAccepted,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	createdRecord, err := a.createAndEnqueueDemeterReportOperation(ctx, record)
	if err != nil {
		return nil, err
	}
	if createdRecord == nil {
		return nil, fmt.Errorf("report operation creation failed")
	}

	finalRecord, err := a.waitForMobileDemeterReportOperation(ctx, createdRecord.OperationID, createdRecord.OrganizationID, createdRecord.UserID)
	if finalRecord != nil {
		defer a.purgeDemeterReportOperationAfterResponse(finalRecord.OperationID, finalRecord.Status)
	}
	if err != nil {
		if finalRecord != nil {
			return nil, fmt.Errorf("%s", mobileReportOperationLastError(finalRecord))
		}
		return nil, err
	}

	response := demeterReportOperationResponseFromRecord(finalRecord)
	if response.Response == nil {
		return nil, fmt.Errorf("report operation completed without a result")
	}
	return response.Response, nil
}

func (a *App) waitForMobileDemeterReportOperation(ctx context.Context, operationID, organizationID, userID string) (*store.DemeterReportOperationRecord, error) {
	waitCtx, cancel := context.WithTimeout(ctx, mobileReportQueueWaitTimeout)
	defer cancel()
	if strings.TrimSpace(operationID) == "" {
		return nil, fmt.Errorf("missing report operation id")
	}

	ticker := time.NewTicker(mobileReportQueuePollInterval)
	defer ticker.Stop()
	for {
		record, err := a.Store.GetDemeterReportOperation(waitCtx, operationID, organizationID, userID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) || errors.Is(err, store.ErrDemeterReportOperationOwnership) {
				return nil, fmt.Errorf("report operation not found")
			}
			return nil, err
		}
		if isMobileDemeterReportTerminalStatus(record.Status) {
			if record.Status == store.DemeterReportOperationStatusCompleted {
				return record, nil
			}
			return record, fmt.Errorf("%s", mobileReportOperationLastError(record))
		}

		select {
		case <-waitCtx.Done():
			return record, waitCtx.Err()
		case <-ticker.C:
		}
	}
}

func isMobileDemeterReportTerminalStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case store.DemeterReportOperationStatusCompleted, store.DemeterReportOperationStatusFailed, store.DemeterReportOperationStatusCancelled:
		return true
	default:
		return false
	}
}

func mobileReportOperationLastError(record *store.DemeterReportOperationRecord) string {
	if record == nil {
		return "report operation failed"
	}
	if record.LastError.Valid && strings.TrimSpace(record.LastError.String) != "" {
		return strings.TrimSpace(record.LastError.String)
	}
	switch strings.ToLower(strings.TrimSpace(record.Status)) {
	case store.DemeterReportOperationStatusCancelled:
		return "report operation cancelled"
	case store.DemeterReportOperationStatusFailed:
		return "report operation failed"
	default:
		return "report operation failed"
	}
}
