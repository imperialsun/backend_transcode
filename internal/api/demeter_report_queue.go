package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"demeter-backend/internal/observability"
	"demeter-backend/internal/reports"
	"demeter-backend/internal/requestmeta"
	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

const (
	demeterReportQueueDefaultParallelism = 1
	demeterReportQueueMaxParallelism     = 8
	demeterReportQueuePollInterval       = 250 * time.Millisecond
	demeterReportQueueIdleFallback       = 30 * time.Second
	demeterReportQueueCooldownDuration   = 5 * time.Second
	demeterReportGenerationMaxAttempts   = 10
	demeterReportGenerationBaseDelay     = 2 * time.Second
	demeterReportQueueKindReport         = "report"
	demeterReportQueueKindTemplateDraft  = "report_template_draft"
	demeterReportRepairResponseMaxChars  = 20000
)

var errInvalidReportTemplateDraft = errors.New("invalid report template draft")

type demeterReportQueueOperationPayload struct {
	TraceID          string                    `json:"traceId"`
	Route            string                    `json:"route"`
	Seq              uint64                    `json:"seq"`
	Kind             string                    `json:"kind,omitempty"`
	MeetingTitle     string                    `json:"meetingTitle,omitempty"`
	Participants     []string                  `json:"participants,omitempty"`
	SourceText       string                    `json:"sourceText"`
	Format           reports.ReportFormat      `json:"format"`
	DetailLevel      reports.ReportDetailLevel `json:"detailLevel"`
	TemplateID       string                    `json:"templateId,omitempty"`
	TemplateName     string                    `json:"templateName,omitempty"`
	Instructions     string                    `json:"instructions,omitempty"`
	ExampleOutline   string                    `json:"exampleOutline,omitempty"`
	ModelID          string                    `json:"modelId"`
	Temperature      float64                   `json:"temperature"`
	MaxTokens        int                       `json:"maxTokens"`
	DraftBrief       string                    `json:"draftBrief,omitempty"`
	BaseFormatHint   string                    `json:"baseFormatHint,omitempty"`
	Tone             string                    `json:"tone,omitempty"`
	RequiredSections []string                  `json:"requiredSections,omitempty"`
	CreatedAt        time.Time                 `json:"createdAt"`
}

type demeterReportRequest struct {
	OperationID  string   `json:"operationId,omitempty"`
	MeetingTitle string   `json:"meetingTitle,omitempty"`
	Participants []string `json:"participants,omitempty"`
	SourceText   string   `json:"sourceText"`
	Format       string   `json:"format"`
	TemplateID   string   `json:"templateId,omitempty"`
	DetailLevel  string   `json:"detailLevel,omitempty"`
	ModelID      string   `json:"modelId,omitempty"`
	Temperature  float64  `json:"temperature,omitempty"`
	MaxTokens    int      `json:"maxTokens,omitempty"`
}

type demeterReportResult struct {
	Format       string             `json:"format"`
	TemplateID   string             `json:"templateId,omitempty"`
	TemplateName string             `json:"templateName,omitempty"`
	Report       reports.ReportJson `json:"report"`
	Raw          string             `json:"raw,omitempty"`
	ModelID      string             `json:"modelId,omitempty"`
	GeneratedAt  string             `json:"generatedAt,omitempty"`
	DetailLevel  string             `json:"detailLevel,omitempty"`
}

type demeterReportTemplateDraft struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	BaseFormat     string `json:"baseFormat"`
	Instructions   string `json:"instructions"`
	ExampleOutline string `json:"exampleOutline"`
}

type demeterReportTemplateDraftResult struct {
	Kind        string                     `json:"kind"`
	Draft       demeterReportTemplateDraft `json:"draft"`
	Raw         string                     `json:"raw,omitempty"`
	ModelID     string                     `json:"modelId,omitempty"`
	GeneratedAt string                     `json:"generatedAt,omitempty"`
}

type demeterReportOperationResponse struct {
	OperationID string  `json:"operationId"`
	Status      string  `json:"status"`
	StatusCode  int     `json:"statusCode"`
	Stage       string  `json:"stage"`
	FormatIndex int     `json:"formatIndex"`
	FormatCount int     `json:"formatCount"`
	Progress    float64 `json:"progress"`
	LastError   string  `json:"lastError,omitempty"`
	UpdatedAt   string  `json:"updatedAt,omitempty"`
	FinishedAt  string  `json:"finishedAt,omitempty"`
	Response    any     `json:"response,omitempty"`
}

type demeterReportQueueLaneState struct {
	ID                 int
	Open               bool
	Draining           bool
	WorkerRunning      bool
	CooldownUntil      time.Time
	CurrentOperationID string
	CurrentStatus      string
	CurrentStage       string
	CurrentFormatIndex int
	CurrentFormatCount int
	CurrentProgress    float64
	LastError          string
}

type demeterReportQueueSettingsSnapshot struct {
	Parallelism int    `json:"parallelism"`
	UpdatedAt   string `json:"updatedAt"`
}

type demeterReportQueueSummarySnapshot struct {
	Parallelism            int    `json:"parallelism"`
	OpenWorkers            int    `json:"openWorkers"`
	DrainingWorkers        int    `json:"drainingWorkers"`
	CoolingWorkers         int    `json:"coolingWorkers"`
	PendingOperations      int    `json:"pendingOperations"`
	RunningOperations      int    `json:"runningOperations"`
	UnassignedOperations   int    `json:"unassignedOperations"`
	RetryPaused            bool   `json:"retryPaused"`
	RetryPausedLaneID      int    `json:"retryPausedLaneId,omitempty"`
	RetryPausedOperationID string `json:"retryPausedOperationId,omitempty"`
	RetryPausedFormatIndex int    `json:"retryPausedFormatIndex,omitempty"`
	RetryPausedSince       string `json:"retryPausedSince,omitempty"`
}

type demeterReportQueueOperationSnapshot struct {
	OperationID      string  `json:"operationId"`
	OrganizationID   string  `json:"organizationId,omitempty"`
	UserID           string  `json:"userId,omitempty"`
	QueueID          int     `json:"queueId"`
	Status           string  `json:"status"`
	Stage            string  `json:"stage"`
	FormatIndex      int     `json:"formatIndex"`
	FormatCount      int     `json:"formatCount"`
	Progress         float64 `json:"progress"`
	StatusCode       int     `json:"statusCode"`
	CreatedAt        string  `json:"createdAt"`
	UpdatedAt        string  `json:"updatedAt"`
	FinishedAt       string  `json:"finishedAt,omitempty"`
	QueuePayloadJSON string  `json:"queuePayloadJson,omitempty"`
	ResponseJSON     string  `json:"responseJson,omitempty"`
	LastError        string  `json:"lastError,omitempty"`
}

type demeterReportQueueLaneSnapshot struct {
	QueueID            int     `json:"queueId"`
	Open               bool    `json:"open"`
	Draining           bool    `json:"draining"`
	WorkerRunning      bool    `json:"workerRunning"`
	CooldownUntil      string  `json:"cooldownUntil,omitempty"`
	CurrentOperationID string  `json:"currentOperationId,omitempty"`
	CurrentStatus      string  `json:"currentStatus,omitempty"`
	CurrentStage       string  `json:"currentStage,omitempty"`
	CurrentFormatIndex int     `json:"currentFormatIndex,omitempty"`
	CurrentFormatCount int     `json:"currentFormatCount,omitempty"`
	CurrentProgress    float64 `json:"currentProgress,omitempty"`
	Load               int     `json:"load"`
	PendingCount       int     `json:"pendingCount"`
	RunningCount       int     `json:"runningCount"`
	LastError          string  `json:"lastError,omitempty"`
}

type demeterReportQueueSnapshot struct {
	Settings      demeterReportQueueSettingsSnapshot    `json:"settings"`
	Summary       demeterReportQueueSummarySnapshot     `json:"summary"`
	Workers       []demeterReportQueueLaneSnapshot      `json:"workers"`
	Operations    []demeterReportQueueOperationSnapshot `json:"operations"`
	AllOperations []demeterReportQueueOperationSnapshot `json:"allOperations"`
}

type DemeterReportQueueManager struct {
	app                    *App
	startOnce              sync.Once
	startErr               error
	mu                     sync.Mutex
	ctx                    context.Context
	cancel                 context.CancelFunc
	parallelism            int
	lanes                  map[int]*demeterReportQueueLaneState
	laneWakeCh             map[int]chan struct{}
	retryPaused            bool
	retryPausedLaneID      int
	retryPausedOperationID string
	retryPausedFormatIndex int
	retryPausedSince       time.Time
	retryPauseDone         chan struct{}
	snapshotSubscribers    map[chan struct{}]struct{}
}

func (a *App) ensureDemeterReportQueueManager() *DemeterReportQueueManager {
	if a == nil {
		return nil
	}
	if a.DemeterReportQueue == nil {
		a.DemeterReportQueue = &DemeterReportQueueManager{
			app:                 a,
			lanes:               map[int]*demeterReportQueueLaneState{},
			laneWakeCh:          map[int]chan struct{}{},
			snapshotSubscribers: map[chan struct{}]struct{}{},
		}
	}
	return a.DemeterReportQueue
}

func (a *App) EnsureDemeterReportQueueManager() *DemeterReportQueueManager {
	return a.ensureDemeterReportQueueManager()
}

func (m *DemeterReportQueueManager) Start(ctx context.Context) error {
	if m == nil || m.app == nil || m.app.Store == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.startOnce.Do(func() {
		m.ctx, m.cancel = context.WithCancel(ctx)
		m.startErr = m.bootstrap(m.ctx)
	})
	return m.startErr
}

func (m *DemeterReportQueueManager) Stop() {
	if m == nil {
		return
	}
	m.mu.Lock()
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *DemeterReportQueueManager) bootstrap(ctx context.Context) error {
	settings, err := m.app.Store.GetDemeterReportQueueSettings(ctx)
	if err != nil {
		return err
	}
	if settings == nil {
		settings, err = m.app.Store.SaveDemeterReportQueueSettings(ctx, demeterReportQueueDefaultParallelism)
		if err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.parallelism = clampInt(settings.Parallelism, 0, demeterReportQueueMaxParallelism)
	for laneID := 1; laneID <= m.parallelism; laneID++ {
		state := m.ensureLaneStateLocked(laneID)
		state.Open = true
		state.Draining = false
	}
	laneIDs := make([]int, 0, len(m.lanes))
	for laneID := range m.lanes {
		laneIDs = append(laneIDs, laneID)
	}
	m.mu.Unlock()
	for _, laneID := range laneIDs {
		m.ensureLaneWorker(laneID)
	}
	_, err = m.rebalancePendingOperations(ctx)
	return err
}

func (m *DemeterReportQueueManager) ensureLaneStateLocked(laneID int) *demeterReportQueueLaneState {
	if laneID <= 0 {
		laneID = 1
	}
	if m.lanes == nil {
		m.lanes = map[int]*demeterReportQueueLaneState{}
	}
	state, ok := m.lanes[laneID]
	if !ok {
		state = &demeterReportQueueLaneState{ID: laneID}
		m.lanes[laneID] = state
	}
	return state
}

func (m *DemeterReportQueueManager) subscribeSnapshotChanges() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	if m == nil {
		return ch, func() {}
	}
	m.mu.Lock()
	if m.snapshotSubscribers == nil {
		m.snapshotSubscribers = map[chan struct{}]struct{}{}
	}
	m.snapshotSubscribers[ch] = struct{}{}
	m.mu.Unlock()
	return ch, func() {
		m.mu.Lock()
		delete(m.snapshotSubscribers, ch)
		m.mu.Unlock()
	}
}

func (m *DemeterReportQueueManager) notifySnapshotChanged() {
	if m == nil {
		return
	}
	m.mu.Lock()
	subscribers := make([]chan struct{}, 0, len(m.snapshotSubscribers))
	for ch := range m.snapshotSubscribers {
		subscribers = append(subscribers, ch)
	}
	m.mu.Unlock()
	for _, ch := range subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (a *App) deleteAdminDemeterReportQueueOperations(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "admin", route, "request_received", "purge_demeter_report_queue", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "admin", route, "request_unauthorized", "purge_demeter_report_queue", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	if !isSuperAdmin(claims) {
		logAPIStep(c, "admin", route, "request_forbidden", "purge_demeter_report_queue", nil)
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden"})
	}

	scope, err := parseDemeterQueuePurgeScope(c.Query("scope"))
	if err != nil {
		logAPIStep(c, "admin", route, "request_validation_error", "purge_demeter_report_queue", map[string]any{
			"reason": "invalid_scope",
			"scope":  strings.TrimSpace(c.Query("scope")),
		})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid scope"})
	}

	ctx := requestContext(c)
	manager := a.EnsureDemeterReportQueueManager()
	logFields := map[string]any{"scope": string(scope)}

	switch scope {
	case demeterQueuePurgeScopeAll:
		logAPIStep(c, "admin", route, "delete_start", "purge_demeter_report_queue", logFields)
		deletedCount, err := a.Store.PurgeAllDemeterReportOperations(ctx)
		if err != nil {
			logAPIStep(c, "admin", route, "delete_error", "purge_demeter_report_queue", map[string]any{"error": err, "scope": string(scope)})
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to purge demeter report queue"})
		}
		a.writeAdminAudit(ctx, claims, "admin.demeter_report_queue.purge", "demeter_report_queue", "", fiber.Map{
			"scope":        string(scope),
			"deletedCount": deletedCount,
		})
		manager.notifySnapshotChanged()
		logAPIStep(c, "admin", route, "response_ready", "purge_demeter_report_queue", map[string]any{
			"scope":         string(scope),
			"deleted_count": deletedCount,
		})
		return c.SendStatus(fiber.StatusNoContent)
	default:
		logAPIStep(c, "admin", route, "delete_start", "purge_demeter_report_queue", logFields)
		deletedCount, err := a.Store.PurgeCompletedDemeterReportOperations(ctx)
		if err != nil {
			logAPIStep(c, "admin", route, "delete_error", "purge_demeter_report_queue", map[string]any{"error": err, "scope": string(scope)})
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to purge demeter report queue"})
		}
		a.writeAdminAudit(ctx, claims, "admin.demeter_report_queue.purge", "demeter_report_queue", "", fiber.Map{
			"scope":        string(scope),
			"deletedCount": deletedCount,
		})
		manager.notifySnapshotChanged()
		logAPIStep(c, "admin", route, "response_ready", "purge_demeter_report_queue", map[string]any{
			"scope":         string(scope),
			"deleted_count": deletedCount,
		})
		return c.SendStatus(fiber.StatusNoContent)
	}
}

func (m *DemeterReportQueueManager) laneWakeChannel(laneID int) chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.laneWakeCh == nil {
		m.laneWakeCh = map[int]chan struct{}{}
	}
	ch, ok := m.laneWakeCh[laneID]
	if !ok {
		ch = make(chan struct{}, 1)
		m.laneWakeCh[laneID] = ch
	}
	return ch
}

func (m *DemeterReportQueueManager) notifyLaneWorkAvailable(laneID int) {
	if laneID <= 0 {
		return
	}
	ch := m.laneWakeChannel(laneID)
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (m *DemeterReportQueueManager) notifyAllLaneWorkAvailable() {
	m.mu.Lock()
	laneIDs := make([]int, 0, len(m.lanes))
	for laneID, state := range m.lanes {
		if state == nil || (!state.Open && !state.Draining) {
			continue
		}
		laneIDs = append(laneIDs, laneID)
	}
	m.mu.Unlock()
	for _, laneID := range laneIDs {
		ch := m.laneWakeChannel(laneID)
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (m *DemeterReportQueueManager) ensureLaneWorker(laneID int) {
	m.mu.Lock()
	state := m.ensureLaneStateLocked(laneID)
	if state.WorkerRunning || (!state.Open && !state.Draining) {
		m.mu.Unlock()
		return
	}
	state.WorkerRunning = true
	m.mu.Unlock()
	m.notifySnapshotChanged()
	go m.runLaneWorker(laneID)
}

func (m *DemeterReportQueueManager) runLaneWorker(laneID int) {
	defer func() {
		m.mu.Lock()
		state := m.ensureLaneStateLocked(laneID)
		state.WorkerRunning = false
		m.mu.Unlock()
		m.notifySnapshotChanged()
	}()

	for {
		if m.ctx == nil {
			return
		}
		select {
		case <-m.ctx.Done():
			return
		default:
		}
		if !m.waitForMistralRetryPause(m.ctx, laneID) {
			return
		}

		// Claims are lane-specific so resizing parallelism can add or drain
		// workers without two goroutines processing the same report operation.
		record, err := m.app.Store.ClaimNextPendingDemeterReportOperationForQueue(m.ctx, laneID)
		if err != nil {
			if !m.sleepWithMistralRetryPause(m.ctx, laneID, demeterReportQueuePollInterval) {
				return
			}
			continue
		}
		if record == nil {
			changed, err := m.rebalancePendingOperations(m.ctx)
			if err != nil {
				if !m.sleepWithMistralRetryPause(m.ctx, laneID, demeterReportQueuePollInterval) {
					return
				}
				continue
			}
			if changed {
				continue
			}
			if !m.waitForLaneWorkAvailable(m.ctx, laneID, demeterReportQueueIdleFallback) {
				return
			}
			continue
		}
		payload, err := decodeDemeterReportQueuePayload(record.QueuePayloadJSON)
		if err != nil || payload == nil {
			_ = m.failClaimedOperation(m.ctx, record, "queue payload invalid", fiber.StatusInternalServerError)
			continue
		}
		if err := m.processClaimedOperation(record, payload, laneID); err != nil {
			m.setLaneCooldown(laneID, demeterReportQueueCooldownDuration)
		}
	}
}

func (m *DemeterReportQueueManager) processClaimedOperation(record *store.DemeterReportOperationRecord, payload *demeterReportQueueOperationPayload, laneID int) error {
	if record == nil || payload == nil {
		return fmt.Errorf("invalid operation")
	}
	opCtx := observability.WithTraceID(m.ctx, payload.TraceID)
	opCtx = requestmeta.WithActor(opCtx, record.UserID, record.OrganizationID)

	m.setLaneCurrentOperation(laneID, record.OperationID, "running", "running", 0, 1, 0, "")
	if demeterReportPayloadKind(payload) == demeterReportQueueKindTemplateDraft {
		return m.processClaimedTemplateDraftOperation(opCtx, record, payload, laneID)
	}

	result, statusCode, err := m.generateReportWithRetry(opCtx, laneID, record.OperationID, payload)
	if err != nil {
		route := strings.TrimSpace(payload.Route)
		if route == "" {
			route = "/providers/demeter-sante/report/operations"
		}
		logDemeterReportBackendErrorCtx(newDemeterAudioLogContext(opCtx), route, payload.Seq, "generate_format_error", map[string]any{
			"operation_id": record.OperationID,
			"format":       string(payload.Format),
			"detail_level": string(payload.DetailLevel),
			"format_index": record.FormatIndex,
			"format_count": record.FormatCount,
			"status_code":  statusCode,
			"error":        err,
			"model_id":     strings.TrimSpace(payload.ModelID),
		})
		_ = m.failClaimedOperation(opCtx, record, err.Error(), statusCode)
		m.clearLaneCurrentOperation(laneID)
		return err
	}

	raw, _ := json.Marshal(result)
	now := time.Now().UTC()
	update := &store.DemeterReportOperationRecord{
		OperationID:    record.OperationID,
		OrganizationID: record.OrganizationID,
		UserID:         record.UserID,
		Status:         store.DemeterReportOperationStatusCompleted,
		Stage:          "completed",
		FormatIndex:    1,
		FormatCount:    1,
		Progress:       1,
		ResponseJSON:   sql.NullString{String: string(raw), Valid: true},
		StatusCode:     fiber.StatusOK,
		UpdatedAt:      now,
		FinishedAt:     sql.NullTime{Time: now, Valid: true},
	}
	if err := m.app.Store.UpdateDemeterReportOperationByID(opCtx, update); err != nil {
		m.clearLaneCurrentOperation(laneID)
		return err
	}
	m.clearLaneCurrentOperation(laneID)
	_, _ = m.rebalancePendingOperations(opCtx)
	return nil
}

func (m *DemeterReportQueueManager) processClaimedTemplateDraftOperation(ctx context.Context, record *store.DemeterReportOperationRecord, payload *demeterReportQueueOperationPayload, laneID int) error {
	result, statusCode, err := m.generateReportTemplateDraftWithRetry(ctx, laneID, record.OperationID, payload)
	if err != nil {
		_ = m.failClaimedOperation(ctx, record, err.Error(), statusCode)
		m.clearLaneCurrentOperation(laneID)
		return err
	}
	raw, _ := json.Marshal(result)
	now := time.Now().UTC()
	update := &store.DemeterReportOperationRecord{
		OperationID:    record.OperationID,
		OrganizationID: record.OrganizationID,
		UserID:         record.UserID,
		Status:         store.DemeterReportOperationStatusCompleted,
		Stage:          "completed",
		FormatIndex:    1,
		FormatCount:    1,
		Progress:       1,
		ResponseJSON:   sql.NullString{String: string(raw), Valid: true},
		StatusCode:     fiber.StatusOK,
		UpdatedAt:      now,
		FinishedAt:     sql.NullTime{Time: now, Valid: true},
	}
	if err := m.app.Store.UpdateDemeterReportOperationByID(ctx, update); err != nil {
		m.clearLaneCurrentOperation(laneID)
		return err
	}
	m.clearLaneCurrentOperation(laneID)
	_, _ = m.rebalancePendingOperations(ctx)
	return nil
}

func (m *DemeterReportQueueManager) failClaimedOperation(ctx context.Context, record *store.DemeterReportOperationRecord, message string, statusCode int) error {
	now := time.Now().UTC()
	update := &store.DemeterReportOperationRecord{
		OperationID:    record.OperationID,
		OrganizationID: record.OrganizationID,
		UserID:         record.UserID,
		Status:         store.DemeterReportOperationStatusFailed,
		Stage:          "failed",
		FormatIndex:    record.FormatIndex,
		FormatCount:    record.FormatCount,
		Progress:       record.Progress,
		LastError:      sql.NullString{String: strings.TrimSpace(message), Valid: true},
		StatusCode:     statusCode,
		UpdatedAt:      now,
		FinishedAt:     sql.NullTime{Time: now, Valid: true},
	}
	if err := m.app.Store.UpdateDemeterReportOperationByID(ctx, update); err != nil {
		return err
	}
	_, _ = m.rebalancePendingOperations(ctx)
	m.notifySnapshotChanged()
	return nil
}

func (m *DemeterReportQueueManager) generateReportWithRetry(ctx context.Context, laneID int, operationID string, payload *demeterReportQueueOperationPayload) (*demeterReportResult, int, error) {
	if payload == nil {
		return nil, fiber.StatusInternalServerError, fmt.Errorf("missing payload")
	}
	lastStatus := fiber.StatusBadGateway
	var lastErr error
	for attempt := 1; attempt <= demeterReportGenerationMaxAttempts; attempt++ {
		if !m.waitForMistralRetryPause(ctx, laneID) {
			return nil, fiber.StatusRequestTimeout, context.Canceled
		}
		result, status, err := m.generateReportOnce(ctx, payload)
		if err == nil {
			_ = m.finishMistralRetryPause(laneID, operationID, 0)
			return result, fiber.StatusOK, nil
		}
		lastStatus = status
		lastErr = err
		if !shouldRetryDemeterReportResponse(status, err) || attempt >= demeterReportGenerationMaxAttempts {
			break
		}
		// Capacity responses pause the whole lane so subsequent operations do
		// not immediately hit the same upstream throttling window.
		if responseIsDemeterReportCapacityExceeded(status, err) {
			m.startMistralRetryPause(laneID, operationID, 0)
		}
		delay := demeterReportRetryDelayForAttempt(attempt)
		if !m.sleepWithMistralRetryPause(ctx, laneID, delay) {
			return nil, fiber.StatusRequestTimeout, context.Canceled
		}
	}
	m.finishMistralRetryPause(laneID, operationID, 0)
	if lastErr == nil {
		lastErr = fmt.Errorf("report generation failed")
	}
	return nil, lastStatus, lastErr
}

func (m *DemeterReportQueueManager) generateReportTemplateDraftWithRetry(ctx context.Context, laneID int, operationID string, payload *demeterReportQueueOperationPayload) (*demeterReportTemplateDraftResult, int, error) {
	if payload == nil {
		return nil, fiber.StatusInternalServerError, fmt.Errorf("missing payload")
	}
	lastStatus := fiber.StatusBadGateway
	var lastErr error
	for attempt := 1; attempt <= demeterReportGenerationMaxAttempts; attempt++ {
		if !m.waitForMistralRetryPause(ctx, laneID) {
			return nil, fiber.StatusRequestTimeout, context.Canceled
		}
		result, status, err := m.generateReportTemplateDraftOnce(ctx, payload)
		if err == nil {
			_ = m.finishMistralRetryPause(laneID, operationID, 0)
			return result, fiber.StatusOK, nil
		}
		lastStatus = status
		lastErr = err
		if !shouldRetryDemeterReportResponse(status, err) || attempt >= demeterReportGenerationMaxAttempts {
			break
		}
		if responseIsDemeterReportCapacityExceeded(status, err) {
			m.startMistralRetryPause(laneID, operationID, 0)
		}
		if !m.sleepWithMistralRetryPause(ctx, laneID, demeterReportRetryDelayForAttempt(attempt)) {
			return nil, fiber.StatusRequestTimeout, context.Canceled
		}
	}
	m.finishMistralRetryPause(laneID, operationID, 0)
	if lastErr == nil {
		lastErr = fmt.Errorf("report template draft generation failed")
	}
	return nil, lastStatus, lastErr
}

func (m *DemeterReportQueueManager) generateReportTemplateDraftOnce(ctx context.Context, payload *demeterReportQueueOperationPayload) (*demeterReportTemplateDraftResult, int, error) {
	body := map[string]any{
		"model": strings.TrimSpace(payload.ModelID),
		"messages": []map[string]string{
			{"role": "system", "content": buildReportTemplateDraftSystemPrompt()},
			{"role": "user", "content": buildReportTemplateDraftUserPrompt(payload)},
		},
		"temperature":     payload.Temperature,
		"max_tokens":      payload.MaxTokens,
		"response_format": map[string]string{"type": "json_object"},
	}
	rawBody, _ := json.Marshal(body)
	status, responseBody, err := m.app.MistralClient.DoJSON(ctx, http.MethodPost, demeterReportGenerationUpstreamPath, rawBody)
	if err != nil {
		return nil, status, err
	}
	if status < 200 || status >= 300 {
		return nil, status, fmt.Errorf("mistral api (%d): %s", status, strings.TrimSpace(string(responseBody)))
	}
	content := extractDemeterReportChatContent(responseBody)
	if strings.TrimSpace(content) == "" {
		return nil, fiber.StatusBadGateway, fmt.Errorf("empty model response")
	}
	draft, err := parseReportTemplateDraft(content)
	if err != nil {
		return nil, fiber.StatusBadGateway, err
	}
	return &demeterReportTemplateDraftResult{
		Kind:        demeterReportQueueKindTemplateDraft,
		Draft:       *draft,
		Raw:         content,
		ModelID:     strings.TrimSpace(payload.ModelID),
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}, status, nil
}

func (m *DemeterReportQueueManager) generateReportOnce(ctx context.Context, payload *demeterReportQueueOperationPayload) (*demeterReportResult, int, error) {
	userPrompt := reports.BuildReportUserPromptWithDetail(payload.Format, payload.DetailLevel, payload.SourceText, payload.MeetingTitle, payload.Participants)
	if strings.TrimSpace(payload.TemplateID) != "" {
		userPrompt = reports.BuildCustomReportUserPromptWithDetail(payload.Format, payload.DetailLevel, payload.SourceText, payload.MeetingTitle, payload.Participants, payload.TemplateName, payload.Instructions, payload.ExampleOutline)
	}
	body := map[string]any{
		"model": strings.TrimSpace(payload.ModelID),
		"messages": []map[string]string{
			{"role": "system", "content": reports.BuildReportSystemPromptWithDetail(payload.DetailLevel)},
			{"role": "user", "content": userPrompt},
		},
		"temperature":     payload.Temperature,
		"max_tokens":      payload.MaxTokens,
		"response_format": map[string]string{"type": "json_object"},
	}
	rawBody, _ := json.Marshal(body)
	status, responseBody, err := m.app.MistralClient.DoJSON(ctx, http.MethodPost, demeterReportGenerationUpstreamPath, rawBody)
	if err != nil {
		return nil, status, err
	}
	if status < 200 || status >= 300 {
		return nil, status, fmt.Errorf("mistral api (%d): %s", status, strings.TrimSpace(string(responseBody)))
	}
	content := extractDemeterReportChatContent(responseBody)
	if strings.TrimSpace(content) == "" {
		return nil, fiber.StatusBadGateway, fmt.Errorf("empty model response")
	}
	report, err := reports.ParseReportJSON(content, payload.Format)
	if err != nil {
		repaired, repairStatus, repairErr := m.repairReportJSONOnce(ctx, payload, content, err)
		if repairErr != nil {
			if !errors.Is(repairErr, reports.ErrInvalidReport) {
				return nil, repairStatus, repairErr
			}
			return nil, repairStatus, err
		}
		return repaired, status, nil
	}
	return &demeterReportResult{
		Format:       string(payload.Format),
		TemplateID:   strings.TrimSpace(payload.TemplateID),
		TemplateName: strings.TrimSpace(payload.TemplateName),
		Report:       report,
		Raw:          content,
		ModelID:      payload.ModelID,
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		DetailLevel:  string(payload.DetailLevel),
	}, status, nil
}

func (m *DemeterReportQueueManager) repairReportJSONOnce(ctx context.Context, payload *demeterReportQueueOperationPayload, rawContent string, parseErr error) (*demeterReportResult, int, error) {
	body := map[string]any{
		"model": strings.TrimSpace(payload.ModelID),
		"messages": []map[string]string{
			{"role": "system", "content": buildReportJSONRepairSystemPrompt()},
			{"role": "user", "content": buildReportJSONRepairUserPrompt(payload, rawContent, parseErr)},
		},
		"temperature":     0,
		"max_tokens":      payload.MaxTokens,
		"response_format": map[string]string{"type": "json_object"},
	}
	rawBody, _ := json.Marshal(body)
	status, responseBody, err := m.app.MistralClient.DoJSON(ctx, http.MethodPost, demeterReportGenerationUpstreamPath, rawBody)
	if err != nil {
		return nil, status, err
	}
	if status < 200 || status >= 300 {
		return nil, status, fmt.Errorf("mistral api (%d): %s", status, strings.TrimSpace(string(responseBody)))
	}
	content := extractDemeterReportChatContent(responseBody)
	if strings.TrimSpace(content) == "" {
		return nil, fiber.StatusBadGateway, fmt.Errorf("empty model repair response")
	}
	report, err := reports.ParseReportJSON(content, payload.Format)
	if err != nil {
		return nil, fiber.StatusBadGateway, err
	}
	return &demeterReportResult{
		Format:       string(payload.Format),
		TemplateID:   strings.TrimSpace(payload.TemplateID),
		TemplateName: strings.TrimSpace(payload.TemplateName),
		Report:       report,
		Raw:          content,
		ModelID:      payload.ModelID,
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		DetailLevel:  string(payload.DetailLevel),
	}, status, nil
}

func buildReportJSONRepairSystemPrompt() string {
	return strings.TrimSpace(`
Tu répares une réponse de génération de compte rendu médical qui n'a pas respecté le contrat JSON.
Retourne uniquement un objet JSON valide, sans Markdown ni texte autour.
Tu ne dois pas ajouter de faits absents de la réponse initiale.
Le JSON final doit respecter exactement ce schéma:
{
  "format": "CRI|CRO|CRS|CRN|CUSTOM",
  "title": "Titre court",
  "sections": [{"heading": "Titre de section", "paragraphs": ["Paragraphe factuel"]}],
  "key_points": ["optionnel"],
  "action_items": ["optionnel"],
  "caveats": ["optionnel"]
}
Si la réponse initiale contient peu d'informations utilisables, crée une section courte qui l'indique clairement sans inventer.
`)
}

func buildReportJSONRepairUserPrompt(payload *demeterReportQueueOperationPayload, rawContent string, parseErr error) string {
	content := strings.TrimSpace(rawContent)
	if len(content) > demeterReportRepairResponseMaxChars {
		content = content[:demeterReportRepairResponseMaxChars] + "\n[réponse tronquée]"
	}
	return strings.TrimSpace(fmt.Sprintf(`
Format attendu: %s.
Niveau de détail attendu: %s.
Erreur de parsing rencontrée: %v.

Réponse initiale à convertir en JSON structuré:
%s
`, payload.Format, payload.DetailLevel, parseErr, content))
}

func extractDemeterReportChatContent(response []byte) string {
	if len(response) == 0 {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(response, &payload); err != nil {
		return ""
	}
	if text := demeterNormalizeTextContent(payload["output_text"]); text != "" {
		return text
	}
	if text := demeterNormalizeTextContent(payload["generated_text"]); text != "" {
		return text
	}
	if text := demeterNormalizeTextContent(payload["content"]); text != "" {
		return text
	}
	choices, _ := payload["choices"].([]any)
	if len(choices) == 0 {
		return ""
	}
	firstChoice, _ := choices[0].(map[string]any)
	if firstChoice == nil {
		return ""
	}
	if text := demeterNormalizeTextContent(firstChoice["text"]); text != "" {
		return text
	}
	message, _ := firstChoice["message"].(map[string]any)
	if message == nil {
		return ""
	}
	return demeterNormalizeTextContent(message["content"])
}

func buildReportTemplateDraftSystemPrompt() string {
	return strings.Join([]string{
		"Tu es un assistant de conception de modèles de compte rendu médical et administratif.",
		"Tu dois produire uniquement un objet JSON valide, sans markdown ni texte autour.",
		"N'utilise pas de bloc ```json, pas d'explication, pas de commentaire.",
		"Toutes les chaînes JSON multilignes doivent utiliser des échappements \\n, jamais de retour ligne brut dans une chaîne.",
		"Le JSON doit respecter exactement cette forme:",
		`{"name":"...","description":"...","baseFormat":"CUSTOM|CRI|CRO|CRS|CRN","instructions":"...","exampleOutline":"..."}`,
		"Utilise baseFormat CUSTOM pour un modèle libre qui ne doit pas se baser sur CRI, CRO, CRS ou CRN.",
		"Les instructions doivent être directement utilisables comme consignes de génération du CR final.",
	}, "\n")
}

func buildReportTemplateDraftUserPrompt(payload *demeterReportQueueOperationPayload) string {
	if payload == nil {
		return ""
	}
	sections := strings.TrimSpace(strings.Join(payload.RequiredSections, "\n- "))
	if sections != "" {
		sections = "- " + sections
	}
	baseFormat := strings.TrimSpace(payload.BaseFormatHint)
	if baseFormat == "" {
		baseFormat = "CUSTOM"
	}
	return strings.Join([]string{
		"Crée un brouillon de modèle de compte rendu personnalisé pour Tradmin.",
		"",
		"Brief métier:",
		strings.TrimSpace(payload.DraftBrief),
		"",
		"Format de base souhaité:",
		baseFormat,
		"",
		"Ton / style souhaité:",
		strings.TrimSpace(payload.Tone),
		"",
		"Sections souhaitées:",
		sections,
		"",
		"Contraintes:",
		"- name: nom court, clair, exploitable dans une liste de modèles.",
		"- description: une phrase courte pour les utilisateurs Front User.",
		"- baseFormat: CUSTOM pour un modèle libre; sinon exactement CRI, CRO, CRS ou CRN si une base explicite est demandée.",
		"- instructions: inclure objectif, sections attendues, règles de style, contraintes métier et éléments à éviter.",
		"- exampleOutline: structure lisible du compte rendu attendu, avec titres de sections.",
	}, "\n")
}

func parseReportTemplateDraft(content string) (*demeterReportTemplateDraft, error) {
	var draft demeterReportTemplateDraft
	jsonText, ok := extractFirstJSONObject(strings.TrimSpace(content))
	if !ok {
		jsonText = strings.TrimSpace(content)
	}
	if err := json.Unmarshal([]byte(jsonText), &draft); err != nil {
		return nil, fmt.Errorf("%w: JSON invalide", errInvalidReportTemplateDraft)
	}
	draft.Name = strings.TrimSpace(draft.Name)
	draft.Description = strings.TrimSpace(draft.Description)
	draft.BaseFormat = strings.TrimSpace(strings.ToUpper(draft.BaseFormat))
	draft.Instructions = strings.TrimSpace(draft.Instructions)
	draft.ExampleOutline = strings.TrimSpace(draft.ExampleOutline)
	if draft.Name == "" || draft.Instructions == "" {
		return nil, fmt.Errorf("%w: name and instructions are required", errInvalidReportTemplateDraft)
	}
	if _, ok := reports.ParseReportTemplateFormat(draft.BaseFormat); !ok {
		return nil, fmt.Errorf("%w: baseFormat must be CUSTOM, CRI, CRO, CRS or CRN", errInvalidReportTemplateDraft)
	}
	return &draft, nil
}

func extractFirstJSONObject(content string) (string, bool) {
	content = strings.TrimSpace(content)
	if content == "" {
		return "", false
	}
	start := strings.IndexByte(content, '{')
	if start < 0 {
		return "", false
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(content); i++ {
		ch := content[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return content[start : i+1], true
			}
		}
	}
	return "", false
}

func demeterNormalizeTextContent(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			rec, _ := item.(map[string]any)
			if rec == nil {
				continue
			}
			text, _ := rec["text"].(string)
			if strings.TrimSpace(text) != "" {
				parts = append(parts, strings.TrimSpace(text))
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	default:
		return ""
	}
}

func shouldRetryDemeterReportResponse(status int, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, reports.ErrInvalidReport) {
		return false
	}
	if errors.Is(err, errInvalidReportTemplateDraft) {
		return false
	}
	if status == http.StatusTooManyRequests || status >= 500 {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "capacity exceeded") || strings.Contains(msg, "timeout")
}

func responseIsDemeterReportCapacityExceeded(status int, err error) bool {
	if err == nil {
		return false
	}
	switch status {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return true
	default:
		return false
	}
}

func demeterReportRetryDelayForAttempt(attempt int) time.Duration {
	delay := demeterReportGenerationBaseDelay
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay > 32*time.Second {
			return 32 * time.Second
		}
	}
	return delay
}

func (m *DemeterReportQueueManager) startMistralRetryPause(laneID int, operationID string, formatIndex int) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	if m.retryPaused {
		m.mu.Unlock()
		return false
	}
	m.retryPaused = true
	m.retryPausedLaneID = laneID
	m.retryPausedOperationID = strings.TrimSpace(operationID)
	m.retryPausedFormatIndex = formatIndex
	m.retryPausedSince = time.Now().UTC()
	m.retryPauseDone = make(chan struct{})
	m.mu.Unlock()
	m.notifySnapshotChanged()
	return true
}

func (m *DemeterReportQueueManager) finishMistralRetryPause(laneID int, operationID string, formatIndex int) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	if !m.retryPaused {
		m.mu.Unlock()
		return false
	}
	if m.retryPausedLaneID != laneID || m.retryPausedOperationID != strings.TrimSpace(operationID) || m.retryPausedFormatIndex != formatIndex {
		m.mu.Unlock()
		return false
	}
	done := m.retryPauseDone
	m.retryPaused = false
	m.retryPausedLaneID = 0
	m.retryPausedOperationID = ""
	m.retryPausedFormatIndex = 0
	m.retryPausedSince = time.Time{}
	m.retryPauseDone = nil
	m.mu.Unlock()
	if done != nil {
		close(done)
	}
	m.notifyAllLaneWorkAvailable()
	m.notifySnapshotChanged()
	return true
}

func (m *DemeterReportQueueManager) waitForMistralRetryPause(ctx context.Context, laneID int) bool {
	for {
		m.mu.Lock()
		if !m.retryPaused || m.retryPausedLaneID == laneID {
			m.mu.Unlock()
			return true
		}
		done := m.retryPauseDone
		if done == nil {
			done = make(chan struct{})
			m.retryPauseDone = done
		}
		m.mu.Unlock()
		if ctx == nil {
			<-done
			continue
		}
		select {
		case <-ctx.Done():
			return false
		case <-done:
		}
	}
}

func (m *DemeterReportQueueManager) sleepWithMistralRetryPause(ctx context.Context, laneID int, delay time.Duration) bool {
	if !m.waitForMistralRetryPause(ctx, laneID) {
		return false
	}
	if ctx == nil {
		time.Sleep(delay)
		return true
	}
	return sleepContext(ctx, delay) == nil
}

func (m *DemeterReportQueueManager) waitForLaneWorkAvailable(ctx context.Context, laneID int, fallback time.Duration) bool {
	ch := m.laneWakeChannel(laneID)
	timer := time.NewTimer(fallback)
	defer timer.Stop()
	if ctx == nil {
		select {
		case <-ch:
			return true
		case <-timer.C:
			return true
		}
	}
	select {
	case <-ctx.Done():
		return false
	case <-ch:
		return true
	case <-timer.C:
		return true
	}
}

func (m *DemeterReportQueueManager) setLaneCurrentOperation(laneID int, operationID, status, stage string, formatIndex, formatCount int, progress float64, lastError string) {
	m.mu.Lock()
	state := m.ensureLaneStateLocked(laneID)
	state.CurrentOperationID = strings.TrimSpace(operationID)
	state.CurrentStatus = strings.TrimSpace(status)
	state.CurrentStage = strings.TrimSpace(stage)
	state.CurrentFormatIndex = formatIndex
	state.CurrentFormatCount = formatCount
	state.CurrentProgress = progress
	state.LastError = strings.TrimSpace(lastError)
	m.mu.Unlock()
	m.notifySnapshotChanged()
}

func (m *DemeterReportQueueManager) clearLaneCurrentOperation(laneID int) {
	m.setLaneCurrentOperation(laneID, "", "", "", 0, 0, 0, "")
}

func (m *DemeterReportQueueManager) setLaneCooldown(laneID int, duration time.Duration) {
	if duration <= 0 {
		return
	}
	m.mu.Lock()
	state := m.ensureLaneStateLocked(laneID)
	state.CooldownUntil = time.Now().UTC().Add(duration)
	m.mu.Unlock()
	m.notifySnapshotChanged()
}

func (m *DemeterReportQueueManager) chooseLane(ctx context.Context) int {
	m.mu.Lock()
	parallelism := m.parallelism
	m.mu.Unlock()
	if parallelism <= 0 {
		return 0
	}
	bestID := 0
	bestLoad := int(^uint(0) >> 1)
	for laneID := 1; laneID <= parallelism; laneID++ {
		count, err := m.app.Store.ListDemeterReportOperations(ctx, &laneID, []string{store.DemeterReportOperationStatusPending, store.DemeterReportOperationStatusRunning}, 1000)
		if err != nil {
			continue
		}
		load := len(count)
		if load < bestLoad || (load == bestLoad && (bestID == 0 || laneID < bestID)) {
			bestID = laneID
			bestLoad = load
		}
	}
	return bestID
}

func (m *DemeterReportQueueManager) openReportLaneIDs() []int {
	m.mu.Lock()
	defer m.mu.Unlock()
	laneIDs := make([]int, 0, len(m.lanes))
	for laneID, state := range m.lanes {
		if state == nil || !state.Open || state.Draining {
			continue
		}
		laneIDs = append(laneIDs, laneID)
	}
	sort.Ints(laneIDs)
	return laneIDs
}

func chooseLeastLoadedReportLane(loadByLane map[int]int, laneIDs []int) int {
	bestID := 0
	bestLoad := int(^uint(0) >> 1)
	for _, laneID := range laneIDs {
		load := loadByLane[laneID]
		if load < bestLoad || (load == bestLoad && (bestID == 0 || laneID < bestID)) {
			bestID = laneID
			bestLoad = load
		}
	}
	return bestID
}

func (m *DemeterReportQueueManager) rebalancePendingOperations(ctx context.Context) (bool, error) {
	laneIDs := m.openReportLaneIDs()
	if len(laneIDs) == 0 {
		return false, nil
	}
	operations, err := m.app.Store.ListDemeterReportOperations(ctx, nil, []string{
		store.DemeterReportOperationStatusPending,
		store.DemeterReportOperationStatusRunning,
	}, 0)
	if err != nil {
		return false, err
	}

	openLaneSet := make(map[int]struct{}, len(laneIDs))
	loadByLane := make(map[int]int, len(laneIDs))
	for _, laneID := range laneIDs {
		openLaneSet[laneID] = struct{}{}
		loadByLane[laneID] = 0
	}

	pending := make([]*store.DemeterReportOperationRecord, 0)
	for _, record := range operations {
		if record == nil {
			continue
		}
		switch record.Status {
		case store.DemeterReportOperationStatusRunning:
			if _, ok := openLaneSet[record.QueueID]; ok {
				loadByLane[record.QueueID]++
			}
		case store.DemeterReportOperationStatusPending:
			pending = append(pending, record)
		}
	}

	changed := false
	for _, record := range pending {
		if record == nil {
			continue
		}
		laneID := chooseLeastLoadedReportLane(loadByLane, laneIDs)
		if laneID <= 0 {
			continue
		}
		loadByLane[laneID]++
		if record.QueueID == laneID {
			continue
		}
		moved, err := m.app.Store.UpdatePendingDemeterReportOperationQueueByID(ctx, record.OperationID, laneID, time.Now().UTC())
		if err != nil {
			return changed, err
		}
		if !moved {
			continue
		}
		m.ensureLaneWorker(laneID)
		m.notifyLaneWorkAvailable(laneID)
		changed = true
	}
	if changed {
		m.notifySnapshotChanged()
	}
	return changed, nil
}

func (m *DemeterReportQueueManager) EnqueueOperation(ctx context.Context, record *store.DemeterReportOperationRecord) (int, error) {
	if err := m.Start(context.Background()); err != nil {
		return 0, err
	}
	laneID := m.chooseLane(ctx)
	if laneID > 0 {
		if err := m.app.Store.UpdateDemeterReportOperationQueueByID(ctx, record.OperationID, laneID, time.Now().UTC()); err != nil {
			return 0, err
		}
		m.ensureLaneWorker(laneID)
		m.notifyLaneWorkAvailable(laneID)
		m.notifySnapshotChanged()
	}
	if _, err := m.rebalancePendingOperations(ctx); err != nil {
		return laneID, err
	}
	m.notifySnapshotChanged()
	return laneID, nil
}

func (m *DemeterReportQueueManager) Resize(ctx context.Context, parallelism int) error {
	if err := m.Start(context.Background()); err != nil {
		return err
	}
	if parallelism < 0 {
		parallelism = 0
	}
	if parallelism > demeterReportQueueMaxParallelism {
		parallelism = demeterReportQueueMaxParallelism
	}
	if _, err := m.app.Store.SaveDemeterReportQueueSettings(ctx, parallelism); err != nil {
		return err
	}
	m.mu.Lock()
	m.parallelism = parallelism
	for laneID, state := range m.lanes {
		if laneID <= parallelism {
			state.Open = true
			state.Draining = false
		} else {
			state.Open = false
			state.Draining = true
		}
	}
	for laneID := 1; laneID <= parallelism; laneID++ {
		state := m.ensureLaneStateLocked(laneID)
		state.Open = true
		state.Draining = false
	}
	laneIDs := make([]int, 0, len(m.lanes))
	for laneID := range m.lanes {
		laneIDs = append(laneIDs, laneID)
	}
	m.mu.Unlock()
	for _, laneID := range laneIDs {
		m.ensureLaneWorker(laneID)
	}
	m.notifySnapshotChanged()
	_, err := m.rebalancePendingOperations(ctx)
	return err
}

func (m *DemeterReportQueueManager) Snapshot(ctx context.Context, limit int) (demeterReportQueueSnapshot, error) {
	snapshot := demeterReportQueueSnapshot{Workers: []demeterReportQueueLaneSnapshot{}, Operations: []demeterReportQueueOperationSnapshot{}, AllOperations: []demeterReportQueueOperationSnapshot{}}
	if limit <= 0 {
		limit = 200
	}
	settings, err := m.app.Store.GetDemeterReportQueueSettings(ctx)
	if err != nil {
		return snapshot, err
	}
	if settings == nil {
		settings = &store.DemeterReportQueueSettingsRecord{Parallelism: demeterReportQueueDefaultParallelism, UpdatedAt: time.Now().UTC()}
	}
	operations, err := m.app.Store.ListDemeterReportOperations(ctx, nil, []string{store.DemeterReportOperationStatusPending, store.DemeterReportOperationStatusRunning}, limit)
	if err != nil {
		return snapshot, err
	}
	allOperations, err := m.app.Store.ListDemeterReportOperations(ctx, nil, nil, limit)
	if err != nil {
		return snapshot, err
	}

	opsByLane := map[int][]*store.DemeterReportOperationRecord{}
	pendingCount := 0
	runningCount := 0
	unassignedCount := 0
	for _, op := range operations {
		if op == nil {
			continue
		}
		if op.Status == store.DemeterReportOperationStatusPending {
			pendingCount++
		}
		if op.Status == store.DemeterReportOperationStatusRunning {
			runningCount++
		}
		if op.QueueID <= 0 {
			unassignedCount++
			continue
		}
		opsByLane[op.QueueID] = append(opsByLane[op.QueueID], op)
	}

	m.mu.Lock()
	laneIDs := make([]int, 0, len(m.lanes))
	for laneID := range m.lanes {
		laneIDs = append(laneIDs, laneID)
	}
	sort.Ints(laneIDs)
	openWorkers, drainingWorkers, coolingWorkers := 0, 0, 0
	workers := make([]demeterReportQueueLaneSnapshot, 0, len(laneIDs))
	for _, laneID := range laneIDs {
		state := m.lanes[laneID]
		if state == nil {
			continue
		}
		if state.Open {
			openWorkers++
		}
		if state.Draining {
			drainingWorkers++
		}
		if !state.CooldownUntil.IsZero() && state.CooldownUntil.After(time.Now().UTC()) {
			coolingWorkers++
		}
		laneOps := opsByLane[laneID]
		pending := 0
		running := 0
		for _, op := range laneOps {
			if op.Status == store.DemeterReportOperationStatusPending {
				pending++
			}
			if op.Status == store.DemeterReportOperationStatusRunning {
				running++
			}
		}
		item := demeterReportQueueLaneSnapshot{
			QueueID:            laneID,
			Open:               state.Open,
			Draining:           state.Draining,
			WorkerRunning:      state.WorkerRunning,
			Load:               len(laneOps),
			PendingCount:       pending,
			RunningCount:       running,
			CurrentOperationID: state.CurrentOperationID,
			CurrentStatus:      state.CurrentStatus,
			CurrentStage:       state.CurrentStage,
			CurrentFormatIndex: state.CurrentFormatIndex,
			CurrentFormatCount: state.CurrentFormatCount,
			CurrentProgress:    state.CurrentProgress,
			LastError:          state.LastError,
		}
		if !state.CooldownUntil.IsZero() && state.CooldownUntil.After(time.Now().UTC()) {
			item.CooldownUntil = state.CooldownUntil.UTC().Format(time.RFC3339)
		}
		workers = append(workers, item)
	}
	retryPaused := m.retryPaused
	retryPausedLaneID := m.retryPausedLaneID
	retryPausedOperationID := m.retryPausedOperationID
	retryPausedFormatIndex := m.retryPausedFormatIndex
	retryPausedSince := m.retryPausedSince
	m.mu.Unlock()

	opSnapshots := make([]demeterReportQueueOperationSnapshot, 0, len(operations))
	for _, op := range operations {
		if op == nil {
			continue
		}
		opSnapshots = append(opSnapshots, demeterReportQueueOperationSnapshotFromRecord(op))
	}
	allSnapshots := make([]demeterReportQueueOperationSnapshot, 0, len(allOperations))
	for _, op := range allOperations {
		if op == nil {
			continue
		}
		allSnapshots = append(allSnapshots, demeterReportQueueOperationSnapshotFromRecord(op))
	}

	snapshot.Settings = demeterReportQueueSettingsSnapshot{Parallelism: settings.Parallelism, UpdatedAt: settings.UpdatedAt.UTC().Format(time.RFC3339)}
	snapshot.Summary = demeterReportQueueSummarySnapshot{
		Parallelism:            settings.Parallelism,
		OpenWorkers:            openWorkers,
		DrainingWorkers:        drainingWorkers,
		CoolingWorkers:         coolingWorkers,
		PendingOperations:      pendingCount,
		RunningOperations:      runningCount,
		UnassignedOperations:   unassignedCount,
		RetryPaused:            retryPaused,
		RetryPausedLaneID:      retryPausedLaneID,
		RetryPausedOperationID: retryPausedOperationID,
		RetryPausedFormatIndex: retryPausedFormatIndex,
	}
	if retryPaused && !retryPausedSince.IsZero() {
		snapshot.Summary.RetryPausedSince = retryPausedSince.UTC().Format(time.RFC3339)
	}
	snapshot.Workers = workers
	snapshot.Operations = opSnapshots
	snapshot.AllOperations = allSnapshots
	return snapshot, nil
}

func demeterReportQueueOperationSnapshotFromRecord(record *store.DemeterReportOperationRecord) demeterReportQueueOperationSnapshot {
	out := demeterReportQueueOperationSnapshot{}
	if record == nil {
		return out
	}
	out.OperationID = strings.TrimSpace(record.OperationID)
	out.OrganizationID = strings.TrimSpace(record.OrganizationID)
	out.UserID = strings.TrimSpace(record.UserID)
	out.QueueID = record.QueueID
	out.Status = strings.TrimSpace(record.Status)
	out.Stage = strings.TrimSpace(record.Stage)
	out.FormatIndex = record.FormatIndex
	out.FormatCount = record.FormatCount
	out.Progress = record.Progress
	out.StatusCode = record.StatusCode
	out.CreatedAt = record.CreatedAt.UTC().Format(time.RFC3339)
	out.UpdatedAt = record.UpdatedAt.UTC().Format(time.RFC3339)
	if record.FinishedAt.Valid {
		out.FinishedAt = record.FinishedAt.Time.UTC().Format(time.RFC3339)
	}
	if record.QueuePayloadJSON.Valid {
		out.QueuePayloadJSON = strings.TrimSpace(record.QueuePayloadJSON.String)
	}
	if record.ResponseJSON.Valid {
		out.ResponseJSON = strings.TrimSpace(record.ResponseJSON.String)
	}
	if record.LastError.Valid {
		out.LastError = strings.TrimSpace(record.LastError.String)
	}
	return out
}

func decodeDemeterReportQueuePayload(record sql.NullString) (*demeterReportQueueOperationPayload, error) {
	if !record.Valid || strings.TrimSpace(record.String) == "" {
		return nil, nil
	}
	var payload demeterReportQueueOperationPayload
	if err := json.Unmarshal([]byte(record.String), &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func demeterReportPayloadKind(payload *demeterReportQueueOperationPayload) string {
	if payload == nil {
		return demeterReportQueueKindReport
	}
	kind := strings.TrimSpace(payload.Kind)
	if kind == "" {
		return demeterReportQueueKindReport
	}
	return kind
}

func (a *App) createAndEnqueueDemeterReportOperation(ctx context.Context, record *store.DemeterReportOperationRecord) (*store.DemeterReportOperationRecord, error) {
	if err := a.Store.CreateDemeterReportOperation(ctx, record); err != nil {
		return nil, err
	}
	queueManager := a.ensureDemeterReportQueueManager()
	if queueManager != nil {
		if _, err := queueManager.EnqueueOperation(ctx, record); err != nil {
			// The operation is already visible to clients, so mark it terminal
			// instead of deleting it and leaving pollers with a missing record.
			now := time.Now().UTC()
			_ = a.Store.UpdateDemeterReportOperationByID(ctx, &store.DemeterReportOperationRecord{
				OperationID:    record.OperationID,
				OrganizationID: record.OrganizationID,
				UserID:         record.UserID,
				Status:         store.DemeterReportOperationStatusFailed,
				Stage:          "failed",
				FormatIndex:    record.FormatIndex,
				FormatCount:    record.FormatCount,
				Progress:       record.Progress,
				LastError:      sql.NullString{String: strings.TrimSpace(err.Error()), Valid: true},
				StatusCode:     fiber.StatusInternalServerError,
				UpdatedAt:      now,
				FinishedAt:     sql.NullTime{Time: now, Valid: true},
			})
			finalRecord, loadErr := a.Store.GetDemeterReportOperation(ctx, record.OperationID, record.OrganizationID, record.UserID)
			if loadErr != nil {
				return nil, err
			}
			return finalRecord, err
		}
	}
	return a.Store.GetDemeterReportOperation(ctx, record.OperationID, record.OrganizationID, record.UserID)
}

func demeterReportOperationResponseFromRecord(record *store.DemeterReportOperationRecord) demeterReportOperationResponse {
	resp := demeterReportOperationResponse{}
	if record == nil {
		return resp
	}
	resp.OperationID = record.OperationID
	resp.Status = record.Status
	resp.StatusCode = record.StatusCode
	resp.Stage = record.Stage
	resp.FormatIndex = record.FormatIndex
	resp.FormatCount = record.FormatCount
	resp.Progress = record.Progress
	resp.UpdatedAt = record.UpdatedAt.UTC().Format(time.RFC3339)
	if record.LastError.Valid {
		resp.LastError = strings.TrimSpace(record.LastError.String)
	}
	if record.FinishedAt.Valid {
		resp.FinishedAt = record.FinishedAt.Time.UTC().Format(time.RFC3339)
	}
	if record.ResponseJSON.Valid && strings.TrimSpace(record.ResponseJSON.String) != "" {
		raw := []byte(record.ResponseJSON.String)
		var envelope struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(raw, &envelope); err == nil && strings.TrimSpace(envelope.Kind) == demeterReportQueueKindTemplateDraft {
			var result demeterReportTemplateDraftResult
			if err := json.Unmarshal(raw, &result); err == nil {
				resp.Response = &result
			}
		} else {
			var result demeterReportResult
			if err := json.Unmarshal(raw, &result); err == nil {
				resp.Response = &result
			}
		}
	}
	return resp
}

func (a *App) submitDemeterReportOperation(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	if a.MistralClient == nil || !a.MistralClient.IsConfigured() {
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "mistral is not configured"})
	}
	var req demeterReportRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	var template *store.OrganizationReportTemplate
	templateID := strings.TrimSpace(req.TemplateID)
	format, ok := reports.ParseReportFormat(req.Format)
	if !ok && templateID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid format"})
	}
	if templateID != "" {
		loaded, err := a.Store.GetOrganizationReportTemplate(requestContext(c), templateID)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load report template"})
		}
		if loaded == nil || loaded.OrganizationID != claims.OrgID || !loaded.OrgEnabled {
			return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "report template is not available"})
		}
		preferenceEnabled, err := a.Store.IsUserReportTemplateEnabled(requestContext(c), claims.UserID, loaded.ID)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load report template preference"})
		}
		if !preferenceEnabled {
			return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "report template is disabled for user"})
		}
		templateFormat, ok := reports.ParseReportFormat(loaded.BaseFormat)
		if !ok {
			templateFormat, ok = reports.ParseReportTemplateFormat(loaded.BaseFormat)
		}
		if !ok {
			return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid report template base format"})
		}
		format = templateFormat
		template = loaded
	}
	detailLevel, ok := reports.ParseReportDetailLevel(req.DetailLevel)
	if !ok {
		detailLevel = reports.ReportDetailStandard
	}
	sourceText := strings.TrimSpace(req.SourceText)
	if sourceText == "" {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "sourceText is required"})
	}
	operationID := strings.TrimSpace(req.OperationID)
	if operationID == "" {
		operationID = "demeter-report-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	if existing, err := a.Store.GetDemeterReportOperation(requestContext(c), operationID, claims.OrgID, claims.UserID); err == nil {
		response := demeterReportOperationResponseFromRecord(existing)
		defer a.purgeDemeterReportOperationAfterResponse(existing.OperationID, response.Status)
		return c.Status(existing.StatusCode).JSON(response)
	} else if !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, store.ErrDemeterReportOperationOwnership) {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load operation"})
	}
	modelID := strings.TrimSpace(req.ModelID)
	if modelID == "" {
		modelID = reports.DefaultReportModelID
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = reports.DefaultReportMaxTokens
	}
	temperature := req.Temperature
	if temperature < 0 || temperature > 2 {
		temperature = reports.DefaultReportTemp
	}
	payload := demeterReportQueueOperationPayload{
		TraceID:      requestTraceID(c),
		Route:        requestRoutePath(c),
		Seq:          nextDemeterReportOperationSequenceID(),
		Kind:         demeterReportQueueKindReport,
		MeetingTitle: strings.TrimSpace(req.MeetingTitle),
		Participants: append([]string(nil), req.Participants...),
		SourceText:   sourceText,
		Format:       format,
		DetailLevel:  detailLevel,
		TemplateID:   templateID,
		ModelID:      modelID,
		Temperature:  temperature,
		MaxTokens:    maxTokens,
		CreatedAt:    time.Now().UTC(),
	}
	if template != nil {
		payload.TemplateName = template.Name
		payload.Instructions = template.Instructions
		payload.ExampleOutline = template.ExampleOutline
	}
	rawPayload, _ := json.Marshal(payload)
	now := time.Now().UTC()
	record := &store.DemeterReportOperationRecord{
		OperationID:      operationID,
		OrganizationID:   claims.OrgID,
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
			response := demeterReportOperationResponseFromRecord(finalRecord)
			defer a.purgeDemeterReportOperationAfterResponse(finalRecord.OperationID, response.Status)
			return c.Status(finalRecord.StatusCode).JSON(response)
		}
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to create report operation"})
	}
	response := demeterReportOperationResponseFromRecord(finalRecord)
	defer a.purgeDemeterReportOperationAfterResponse(finalRecord.OperationID, response.Status)
	return c.Status(finalRecord.StatusCode).JSON(response)
}

func (a *App) getDemeterReportOperationStatus(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	operationID := strings.TrimSpace(c.Params("operationId"))
	if operationID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "missing operation id"})
	}
	record, err := a.Store.GetDemeterReportOperation(requestContext(c), operationID, claims.OrgID, claims.UserID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, store.ErrDemeterReportOperationOwnership) {
			return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "operation not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load operation"})
	}
	response := demeterReportOperationResponseFromRecord(record)
	defer a.purgeDemeterReportOperationAfterResponse(record.OperationID, response.Status)
	return c.Status(fiber.StatusOK).JSON(response)
}

func (a *App) cancelDemeterReportOperation(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	operationID := strings.TrimSpace(c.Params("operationId"))
	if operationID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "missing operation id"})
	}
	record, err := a.Store.CancelDemeterReportOperation(requestContext(c), operationID, claims.OrgID, claims.UserID, time.Now().UTC())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, store.ErrDemeterReportOperationOwnership) {
			return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "operation not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to cancel operation"})
	}
	response := demeterReportOperationResponseFromRecord(record)
	defer a.purgeDemeterReportOperationAfterResponse(record.OperationID, response.Status)
	return c.Status(fiber.StatusOK).JSON(response)
}

func (a *App) purgeDemeterReportOperationAfterResponse(operationID, status string) {
	if a == nil || a.Store == nil {
		return
	}
	switch strings.TrimSpace(status) {
	case store.DemeterReportOperationStatusCompleted, store.DemeterReportOperationStatusFailed, store.DemeterReportOperationStatusCancelled:
	default:
		return
	}
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return
	}
	go func(store *store.Store, reportOperationID string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = store.DeleteDemeterReportOperation(ctx, reportOperationID)
	}(a.Store, operationID)
}

func (a *App) getAdminDemeterReportQueue(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	if !isSuperAdmin(claims) {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden"})
	}
	limit := 200
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			if parsed > 500 {
				parsed = 500
			}
			limit = parsed
		} else {
			return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid limit"})
		}
	}
	manager := a.EnsureDemeterReportQueueManager()
	snapshot, err := manager.Snapshot(requestContext(c), limit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load demeter report queue"})
	}
	return c.JSON(snapshot)
}

func (a *App) registerAdminDemeterReportQueueRoutes(group fiber.Router) {
	group.Get("/providers/demeter-sante/report-queue", RequireSuperAdminScope(), a.getAdminDemeterReportQueue)
	group.Put("/providers/demeter-sante/report-queue/settings", RequireSuperAdminScope(), a.putAdminDemeterReportQueueSettings)
	group.Delete("/providers/demeter-sante/report-queue", RequireSuperAdminScope(), a.deleteAdminDemeterReportQueueOperations)
}

func (a *App) putAdminDemeterReportQueueSettings(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	if !isSuperAdmin(claims) {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden"})
	}
	var req struct {
		Parallelism *int `json:"parallelism"`
		Settings    *struct {
			Parallelism *int `json:"parallelism"`
		} `json:"settings"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	parallelism := 0
	switch {
	case req.Parallelism != nil:
		parallelism = *req.Parallelism
	case req.Settings != nil && req.Settings.Parallelism != nil:
		parallelism = *req.Settings.Parallelism
	default:
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "parallelism is required"})
	}
	manager := a.EnsureDemeterReportQueueManager()
	if err := manager.Resize(requestContext(c), parallelism); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to update demeter report queue settings"})
	}
	snapshot, err := manager.Snapshot(requestContext(c), 200)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load demeter report queue"})
	}
	return c.JSON(snapshot)
}
