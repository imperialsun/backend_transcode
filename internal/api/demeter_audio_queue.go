package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"demeter-backend/internal/observability"
	"demeter-backend/internal/requestmeta"
	"demeter-backend/internal/store"
)

const (
	demeterAudioQueueDefaultParallelism = 1
	demeterAudioQueueMaxParallelism     = 8
	demeterAudioQueuePollInterval       = 250 * time.Millisecond
	demeterAudioQueueIdleFallback       = 30 * time.Second
	demeterAudioQueueCooldownDuration   = 5 * time.Second
)

type demeterAudioQueueOperationPayload struct {
	TraceID               string                    `json:"traceId"`
	Route                 string                    `json:"route"`
	Seq                   uint64                    `json:"seq"`
	RouteMode             string                    `json:"routeMode"`
	AudioDurationSec      float64                   `json:"audioDurationSec"`
	AudioDurationProvided bool                      `json:"audioDurationProvided"`
	RequestBytes          int                       `json:"requestBytes"`
	StartedAt             time.Time                 `json:"startedAt"`
	Upload                demeterBackendAudioUpload `json:"upload"`
	ChunkPlans            []demeterBackendChunkPlan `json:"chunkPlans"`
}

type demeterAudioQueueLaneState struct {
	ID                 int
	Open               bool
	Draining           bool
	WorkerRunning      bool
	CooldownUntil      time.Time
	CurrentOperationID string
	CurrentStatus      string
	CurrentStage       string
	CurrentChunkIndex  int
	CurrentChunkCount  int
	CurrentProgress    float64
	LastError          string
}

type demeterAudioQueueSettingsSnapshot struct {
	Parallelism int    `json:"parallelism"`
	UpdatedAt   string `json:"updatedAt"`
}

type demeterAudioQueueSummarySnapshot struct {
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
	RetryPausedChunkIndex  int    `json:"retryPausedChunkIndex,omitempty"`
	RetryPausedSince       string `json:"retryPausedSince,omitempty"`
}

type demeterAudioQueueOperationSnapshot struct {
	OperationID      string  `json:"operationId"`
	OrganizationID   string  `json:"organizationId,omitempty"`
	UserID           string  `json:"userId,omitempty"`
	QueueID          int     `json:"queueId"`
	Status           string  `json:"status"`
	Stage            string  `json:"stage"`
	ChunkIndex       int     `json:"chunkIndex"`
	ChunkCount       int     `json:"chunkCount"`
	Progress         float64 `json:"progress"`
	StatusCode       int     `json:"statusCode"`
	CreatedAt        string  `json:"createdAt"`
	UpdatedAt        string  `json:"updatedAt"`
	FinishedAt       string  `json:"finishedAt,omitempty"`
	QueuePayloadJSON string  `json:"queuePayloadJson,omitempty"`
	ResponseJSON     string  `json:"responseJson,omitempty"`
	LastError        string  `json:"lastError,omitempty"`
}

type demeterAudioQueueLaneSnapshot struct {
	QueueID            int     `json:"queueId"`
	Open               bool    `json:"open"`
	Draining           bool    `json:"draining"`
	WorkerRunning      bool    `json:"workerRunning"`
	CooldownUntil      string  `json:"cooldownUntil,omitempty"`
	CurrentOperationID string  `json:"currentOperationId,omitempty"`
	CurrentStatus      string  `json:"currentStatus,omitempty"`
	CurrentStage       string  `json:"currentStage,omitempty"`
	CurrentChunkIndex  int     `json:"currentChunkIndex,omitempty"`
	CurrentChunkCount  int     `json:"currentChunkCount,omitempty"`
	CurrentProgress    float64 `json:"currentProgress,omitempty"`
	Load               int     `json:"load"`
	PendingCount       int     `json:"pendingCount"`
	RunningCount       int     `json:"runningCount"`
	LastError          string  `json:"lastError,omitempty"`
}

type demeterAudioQueueSnapshot struct {
	Settings      demeterAudioQueueSettingsSnapshot    `json:"settings"`
	Summary       demeterAudioQueueSummarySnapshot     `json:"summary"`
	Workers       []demeterAudioQueueLaneSnapshot      `json:"workers"`
	Operations    []demeterAudioQueueOperationSnapshot `json:"operations"`
	AllOperations []demeterAudioQueueOperationSnapshot `json:"allOperations"`
}

func demeterAudioQueueOperationSnapshotFromRecord(record *store.DemeterAudioTranscriptionOperationRecord) demeterAudioQueueOperationSnapshot {
	item := demeterAudioQueueOperationSnapshot{}
	if record == nil {
		return item
	}

	item.OperationID = strings.TrimSpace(record.OperationID)
	item.OrganizationID = strings.TrimSpace(record.OrganizationID)
	item.UserID = strings.TrimSpace(record.UserID)
	item.QueueID = record.QueueID
	item.Status = strings.TrimSpace(record.Status)
	item.Stage = strings.TrimSpace(record.Stage)
	item.ChunkIndex = record.ChunkIndex
	item.ChunkCount = record.ChunkCount
	item.Progress = record.Progress
	item.StatusCode = record.StatusCode
	item.CreatedAt = record.CreatedAt.UTC().Format(time.RFC3339)
	item.UpdatedAt = record.UpdatedAt.UTC().Format(time.RFC3339)

	if record.FinishedAt.Valid {
		item.FinishedAt = record.FinishedAt.Time.UTC().Format(time.RFC3339)
	}
	if record.QueuePayloadJSON.Valid {
		if trimmed := strings.TrimSpace(record.QueuePayloadJSON.String); trimmed != "" {
			item.QueuePayloadJSON = trimmed
		}
	}
	if record.ResponseJSON.Valid {
		if trimmed := strings.TrimSpace(record.ResponseJSON.String); trimmed != "" {
			item.ResponseJSON = trimmed
		}
	}
	if record.LastError.Valid {
		item.LastError = strings.TrimSpace(record.LastError.String)
	}

	return item
}

// DemeterAudioQueueManager serializes Demeter transcription operations into
// worker lanes and keeps the queue state visible to the admin UI.
type DemeterAudioQueueManager struct {
	app                    *App
	startOnce              sync.Once
	startErr               error
	mu                     sync.Mutex
	ctx                    context.Context
	cancel                 context.CancelFunc
	parallelism            int
	lanes                  map[int]*demeterAudioQueueLaneState
	laneWakeCh             map[int]chan struct{}
	retryPaused            bool
	retryPausedLaneID      int
	retryPausedOperationID string
	retryPausedChunkIndex  int
	retryPausedSince       time.Time
	retryPauseDone         chan struct{}
}

func (a *App) ensureDemeterQueueManager() *DemeterAudioQueueManager {
	if a == nil {
		return nil
	}
	if a.DemeterQueue == nil {
		a.DemeterQueue = &DemeterAudioQueueManager{
			app:        a,
			lanes:      map[int]*demeterAudioQueueLaneState{},
			laneWakeCh: map[int]chan struct{}{},
		}
	}
	return a.DemeterQueue
}

// EnsureDemeterQueueManager exposes the queue manager constructor to callers
// outside the api package.
func (a *App) EnsureDemeterQueueManager() *DemeterAudioQueueManager {
	return a.ensureDemeterQueueManager()
}

func (m *DemeterAudioQueueManager) Start(ctx context.Context) error {
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

func (m *DemeterAudioQueueManager) Stop() {
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

func (m *DemeterAudioQueueManager) bootstrap(ctx context.Context) error {
	settings, err := m.loadSettings(ctx)
	if err != nil {
		return err
	}
	if err := m.reconcileInterruptedOperations(ctx); err != nil {
		return err
	}
	assignedLaneIDs, err := m.loadAssignedLaneIDs(ctx)
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.parallelism = clampInt(settings.Parallelism, 0, demeterAudioQueueMaxParallelism)
	m.syncLaneStatesLocked(assignedLaneIDs)
	laneIDs := m.startableLaneIDsLocked()
	m.mu.Unlock()

	for _, laneID := range laneIDs {
		m.ensureLaneWorker(ctx, laneID)
	}

	if err := m.rebalancePendingOperations(ctx); err != nil {
		return err
	}
	return nil
}

func (m *DemeterAudioQueueManager) loadSettings(ctx context.Context) (*store.DemeterAudioQueueSettingsRecord, error) {
	record, err := m.app.Store.GetDemeterAudioQueueSettings(ctx)
	if err != nil {
		return nil, err
	}
	if record == nil {
		record, err = m.app.Store.SaveDemeterAudioQueueSettings(ctx, demeterAudioQueueDefaultParallelism)
		if err != nil {
			return nil, err
		}
	}
	if record.Parallelism < 0 {
		record.Parallelism = 0
	}
	return record, nil
}

func (m *DemeterAudioQueueManager) loadAssignedLaneIDs(ctx context.Context) (map[int]struct{}, error) {
	records, err := m.app.Store.ListDemeterAudioTranscriptionOperations(ctx, nil, []string{
		store.DemeterAudioTranscriptionOperationStatusPending,
		store.DemeterAudioTranscriptionOperationStatusRunning,
	}, 1000)
	if err != nil {
		return nil, err
	}
	out := map[int]struct{}{}
	for _, record := range records {
		if record != nil && record.QueueID > 0 {
			out[record.QueueID] = struct{}{}
		}
	}
	return out, nil
}

func (m *DemeterAudioQueueManager) syncLaneStatesLocked(assignedLaneIDs map[int]struct{}) {
	if m.lanes == nil {
		m.lanes = map[int]*demeterAudioQueueLaneState{}
	}
	for laneID := 1; laneID <= m.parallelism; laneID++ {
		state := m.ensureLaneStateLocked(laneID)
		state.Open = true
		state.Draining = false
	}
	for laneID := range assignedLaneIDs {
		state := m.ensureLaneStateLocked(laneID)
		if laneID <= m.parallelism {
			state.Open = true
			state.Draining = false
		} else {
			state.Open = false
			state.Draining = true
		}
	}
}

func (m *DemeterAudioQueueManager) startableLaneIDsLocked() []int {
	laneIDs := make([]int, 0, len(m.lanes))
	for laneID, state := range m.lanes {
		if state == nil {
			continue
		}
		if state.Open || state.Draining {
			laneIDs = append(laneIDs, laneID)
		}
	}
	return laneIDs
}

func (m *DemeterAudioQueueManager) ensureLaneStateLocked(laneID int) *demeterAudioQueueLaneState {
	if laneID <= 0 {
		laneID = 1
	}
	if m.lanes == nil {
		m.lanes = map[int]*demeterAudioQueueLaneState{}
	}
	state, ok := m.lanes[laneID]
	if !ok {
		state = &demeterAudioQueueLaneState{ID: laneID}
		m.lanes[laneID] = state
	}
	return state
}

func (m *DemeterAudioQueueManager) mistralRetryPauseStateLocked() (paused bool, laneID int, operationID string, chunkIndex int, since time.Time) {
	if m == nil || !m.retryPaused {
		return false, 0, "", 0, time.Time{}
	}
	return true, m.retryPausedLaneID, m.retryPausedOperationID, m.retryPausedChunkIndex, m.retryPausedSince
}

func (m *DemeterAudioQueueManager) startMistralRetryPause(laneID int, operationID string, chunkIndex int) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.retryPaused {
		return false
	}
	m.retryPaused = true
	m.retryPausedLaneID = laneID
	m.retryPausedOperationID = strings.TrimSpace(operationID)
	m.retryPausedChunkIndex = chunkIndex
	m.retryPausedSince = time.Now().UTC()
	m.retryPauseDone = make(chan struct{})
	return true
}

func (m *DemeterAudioQueueManager) finishMistralRetryPause(laneID int, operationID string, chunkIndex int) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	if !m.retryPaused {
		m.mu.Unlock()
		return false
	}
	if m.retryPausedLaneID != laneID || m.retryPausedOperationID != strings.TrimSpace(operationID) || m.retryPausedChunkIndex != chunkIndex {
		m.mu.Unlock()
		return false
	}
	done := m.retryPauseDone
	m.retryPaused = false
	m.retryPausedLaneID = 0
	m.retryPausedOperationID = ""
	m.retryPausedChunkIndex = 0
	m.retryPausedSince = time.Time{}
	m.retryPauseDone = nil
	m.mu.Unlock()
	if done != nil {
		close(done)
	}
	m.notifyAllLaneWorkAvailable()
	return true
}

func (m *DemeterAudioQueueManager) ensureMistralRetryPauseDoneLocked() chan struct{} {
	if m.retryPauseDone == nil {
		m.retryPauseDone = make(chan struct{})
	}
	return m.retryPauseDone
}

func (m *DemeterAudioQueueManager) ensureLaneWakeChLocked(laneID int) chan struct{} {
	if laneID <= 0 {
		laneID = 1
	}
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

func (m *DemeterAudioQueueManager) laneWakeChannel(laneID int) <-chan struct{} {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	ch := m.ensureLaneWakeChLocked(laneID)
	m.mu.Unlock()
	return ch
}

func (m *DemeterAudioQueueManager) notifyLaneWorkAvailable(laneID int) {
	if m == nil || laneID <= 0 {
		return
	}
	m.mu.Lock()
	ch := m.ensureLaneWakeChLocked(laneID)
	m.mu.Unlock()
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (m *DemeterAudioQueueManager) notifyAllLaneWorkAvailable() {
	if m == nil {
		return
	}
	m.mu.Lock()
	channels := make([]chan struct{}, 0, len(m.lanes))
	for laneID, state := range m.lanes {
		if state == nil || (!state.Open && !state.Draining) {
			continue
		}
		channels = append(channels, m.ensureLaneWakeChLocked(laneID))
	}
	m.mu.Unlock()
	for _, ch := range channels {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (m *DemeterAudioQueueManager) waitForMistralRetryPause(ctx context.Context, laneID int) bool {
	if m == nil {
		return true
	}
	for {
		m.mu.Lock()
		paused, ownerLaneID, _, _, _ := m.mistralRetryPauseStateLocked()
		if !paused || ownerLaneID == laneID {
			m.mu.Unlock()
			return true
		}
		done := m.ensureMistralRetryPauseDoneLocked()
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

func (m *DemeterAudioQueueManager) sleepWithMistralRetryPause(ctx context.Context, laneID int, duration time.Duration) bool {
	if duration <= 0 {
		return true
	}
	if !m.waitForMistralRetryPause(ctx, laneID) {
		return false
	}
	if ctx == nil {
		time.Sleep(duration)
		return true
	}
	if err := sleepContext(ctx, duration); err != nil {
		return false
	}
	return true
}

func (m *DemeterAudioQueueManager) waitForLaneWorkAvailable(ctx context.Context, laneID int, fallback time.Duration) bool {
	if fallback <= 0 {
		return true
	}
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

func (m *DemeterAudioQueueManager) ensureLaneWorker(ctx context.Context, laneID int) bool {
	m.mu.Lock()
	state := m.ensureLaneStateLocked(laneID)
	if state.WorkerRunning || (!state.Open && !state.Draining) {
		m.mu.Unlock()
		return false
	}
	open := state.Open
	draining := state.Draining
	state.WorkerRunning = true
	m.mu.Unlock()

	logDemeterAudioQueuePerformanceTaskCtx(newDemeterAudioLogContext(ctx), "", 0, "worker_created", "demeter_worker_created", map[string]any{
		"queue_id": laneID,
		"open":     open,
		"draining": draining,
	})

	go m.runLaneWorker(laneID)
	return true
}

func (m *DemeterAudioQueueManager) runLaneWorker(laneID int) {
	defer func() {
		state := m.snapshotLaneState(laneID)
		logDemeterAudioQueuePerformanceTaskCtx(newDemeterAudioLogContext(m.ctx), "", 0, "worker_stopped", "demeter_worker_stopped", map[string]any{
			"queue_id": laneID,
			"open":     state != nil && state.Open,
			"draining": state != nil && state.Draining,
		})

		m.mu.Lock()
		if state := m.ensureLaneStateLocked(laneID); state != nil {
			state.WorkerRunning = false
		}
		m.mu.Unlock()
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

		state := m.snapshotLaneState(laneID)
		if state == nil {
			return
		}
		now := time.Now().UTC()
		if !state.CooldownUntil.IsZero() && state.CooldownUntil.After(now) {
			if !m.sleepWithMistralRetryPause(m.ctx, laneID, state.CooldownUntil.Sub(now)) {
				return
			}
			continue
		}

		record, err := m.app.Store.ClaimNextPendingDemeterAudioTranscriptionOperationForQueue(m.ctx, laneID)
		if err != nil {
			if m.isFatalQueueError(err) {
				return
			}
			if !m.sleepWithMistralRetryPause(m.ctx, laneID, demeterAudioQueuePollInterval) {
				return
			}
			continue
		}
		if record == nil {
			if state.Draining && !m.laneHasWork(m.ctx, laneID) {
				return
			}
			if err := m.rebalancePendingOperations(m.ctx); err != nil && !m.isFatalQueueError(err) {
				_ = err
			}
			if !m.waitForLaneWorkAvailable(m.ctx, laneID, demeterAudioQueueIdleFallback) {
				return
			}
			continue
		}

		payload, err := decodeDemeterAudioQueuePayload(record.QueuePayloadJSON)
		if err != nil {
			_ = m.failClaimedOperation(m.ctx, record, err.Error(), httpStatusInternalServerError(), false)
			continue
		}
		if payload == nil {
			_ = m.failClaimedOperation(m.ctx, record, "queue payload missing", httpStatusInternalServerError(), false)
			continue
		}
		if strings.TrimSpace(payload.Upload.SourcePath) == "" {
			_ = m.failClaimedOperation(m.ctx, record, "source path missing", httpStatusInternalServerError(), false)
			continue
		}
		if _, err := os.Stat(payload.Upload.SourcePath); err != nil {
			_ = m.failClaimedOperation(m.ctx, record, fmt.Sprintf("source file unavailable: %v", err), httpStatusInternalServerError(), false)
			continue
		}

		if err := m.processClaimedOperation(record, payload, laneID); err != nil {
			if !m.isFatalQueueError(err) {
				m.setLaneCooldown(laneID, demeterAudioQueueCooldownDuration)
				logDemeterAudioQueuePerformanceTaskCtx(newDemeterAudioLogContext(m.ctx), payload.Route, payload.Seq, "cooldown_started", "demeter_worker_cooldown_started", map[string]any{
					"queue_id":     laneID,
					"operation_id": record.OperationID,
					"cooldown_ms":  demeterAudioQueueCooldownDuration.Milliseconds(),
					"error":        err.Error(),
				})
			}
		}
	}
}

func (m *DemeterAudioQueueManager) processClaimedOperation(record *store.DemeterAudioTranscriptionOperationRecord, payload *demeterAudioQueueOperationPayload, laneID int) error {
	if m == nil || m.app == nil || record == nil || payload == nil {
		return fmt.Errorf("queue manager is not configured")
	}
	jobStartedAt := time.Now().UTC()
	operationCtx := observability.WithTraceID(m.ctx, payload.TraceID)
	operationCtx = requestmeta.WithActor(operationCtx, record.UserID, record.OrganizationID)
	workerCtx, cancel := context.WithCancel(operationCtx)
	logCtx := demeterAudioLogContext{
		ctx:     workerCtx,
		traceID: strings.TrimSpace(payload.TraceID),
		userID:  record.UserID,
		orgID:   record.OrganizationID,
	}
	attachDemeterAudioQueueCleanup(&payload.Upload, logCtx, payload)
	demeterAudioTranscriptionOperationCancels.Store(record.OperationID, cancel)
	defer func() {
		cancel()
		demeterAudioTranscriptionOperationCancels.Delete(record.OperationID)
	}()

	if !m.waitForMistralRetryPause(workerCtx, laneID) {
		return workerCtx.Err()
	}

	m.setLaneCurrentOperation(laneID, record.OperationID, "running", "running", 0, record.ChunkCount, 0, "")
	auditFields := demeterAudioRequestBaseFields(payload.RouteMode, payload.AudioDurationSec, payload.AudioDurationProvided, map[string]any{
		"operation_id":  record.OperationID,
		"queue_id":      laneID,
		"chunk_count":   record.ChunkCount,
		"request_bytes": payload.RequestBytes,
	})
	logDemeterAudioStageCtx(logCtx, payload.Route, payload.Seq, "operation_worker_start", auditFields)
	m.app.runDemeterAudioTranscriptionOperation(
		workerCtx,
		logCtx,
		cancel,
		m,
		laneID,
		record.OperationID,
		payload.Route,
		payload.Seq,
		payload.RouteMode,
		payload.AudioDurationSec,
		payload.AudioDurationProvided,
		payload.RequestBytes,
		&payload.Upload,
		payload.ChunkPlans,
	)

	finalLoadCtx := observability.WithTraceID(context.Background(), payload.TraceID)
	finalLoadCtx = requestmeta.WithActor(finalLoadCtx, record.UserID, record.OrganizationID)
	finalRecord, err := m.app.Store.GetDemeterAudioTranscriptionOperation(finalLoadCtx, record.OperationID, record.OrganizationID, record.UserID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			m.clearLaneCurrentOperation(laneID)
			return nil
		}
		m.clearLaneCurrentOperation(laneID)
		return err
	}
	if finalRecord != nil && finalRecord.Status == store.DemeterAudioTranscriptionOperationStatusFailed {
		lastError := ""
		if finalRecord.LastError.Valid {
			lastError = strings.TrimSpace(finalRecord.LastError.String)
		}
		logDemeterAudioQueuePerformanceTaskCtx(logCtx, payload.Route, payload.Seq, "job_failed", "demeter_worker_job_failed", map[string]any{
			"queue_id":     laneID,
			"operation_id": record.OperationID,
			"status":       finalRecord.Status,
			"status_code":  finalRecord.StatusCode,
			"chunk_count":  finalRecord.ChunkCount,
			"chunk_index":  finalRecord.ChunkIndex,
			"progress":     finalRecord.Progress,
			"duration_ms":  time.Since(jobStartedAt).Milliseconds(),
			"last_error":   lastError,
			"updated_at":   finalRecord.UpdatedAt.UTC().Format(time.RFC3339),
		})
		m.setLaneCooldown(laneID, demeterAudioQueueCooldownDuration)
		logDemeterAudioQueuePerformanceTaskCtx(logCtx, payload.Route, payload.Seq, "cooldown_started", "demeter_worker_cooldown_started", map[string]any{
			"queue_id":     laneID,
			"operation_id": record.OperationID,
			"cooldown_ms":  demeterAudioQueueCooldownDuration.Milliseconds(),
			"status_code":  finalRecord.StatusCode,
			"stage":        finalRecord.Stage,
			"status":       finalRecord.Status,
			"chunk_count":  finalRecord.ChunkCount,
			"chunk_index":  finalRecord.ChunkIndex,
			"updated_at":   finalRecord.UpdatedAt.UTC().Format(time.RFC3339),
			"last_error":   lastError,
		})
	}
	if finalRecord != nil && finalRecord.Status == store.DemeterAudioTranscriptionOperationStatusCompleted {
		logDemeterAudioQueuePerformanceTaskCtx(logCtx, payload.Route, payload.Seq, "job_completed", "demeter_worker_job_completed", map[string]any{
			"queue_id":     laneID,
			"operation_id": record.OperationID,
			"status":       finalRecord.Status,
			"status_code":  finalRecord.StatusCode,
			"chunk_count":  finalRecord.ChunkCount,
			"chunk_index":  finalRecord.ChunkIndex,
			"progress":     finalRecord.Progress,
			"duration_ms":  time.Since(jobStartedAt).Milliseconds(),
			"updated_at":   finalRecord.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	m.clearLaneCurrentOperation(laneID)
	return nil
}

func attachDemeterAudioQueueCleanup(upload *demeterBackendAudioUpload, logCtx demeterAudioLogContext, payload *demeterAudioQueueOperationPayload) {
	if upload == nil {
		return
	}
	sourceDir := strings.TrimSpace(upload.SourceDir)
	if sourceDir == "" {
		upload.cleanup = func() {}
		return
	}
	route := ""
	seq := uint64(0)
	if payload != nil {
		route = strings.TrimSpace(payload.Route)
		seq = payload.Seq
	}
	upload.cleanup = func() {
		cleanupDemeterAudioTempPath(logCtx, route, seq, "source_cleanup", "backend_upload_dir", sourceDir, map[string]any{
			"source_path": sourceDir,
		})
	}
}

func (m *DemeterAudioQueueManager) failClaimedOperation(ctx context.Context, record *store.DemeterAudioTranscriptionOperationRecord, message string, statusCode int, cooldown bool) error {
	if record == nil {
		return nil
	}
	now := time.Now().UTC()
	update := &store.DemeterAudioTranscriptionOperationRecord{
		OperationID:    record.OperationID,
		OrganizationID: record.OrganizationID,
		UserID:         record.UserID,
		Status:         store.DemeterAudioTranscriptionOperationStatusFailed,
		Stage:          "failed",
		ChunkIndex:     record.ChunkIndex,
		ChunkCount:     record.ChunkCount,
		Progress:       record.Progress,
		LastError:      sql.NullString{String: strings.TrimSpace(message), Valid: true},
		StatusCode:     statusCode,
		UpdatedAt:      now,
		FinishedAt:     sql.NullTime{Time: now, Valid: true},
	}
	if err := m.app.Store.UpdateDemeterAudioTranscriptionOperationByID(ctx, update); err != nil {
		return err
	}
	if cooldown {
		m.setLaneCooldown(record.QueueID, demeterAudioQueueCooldownDuration)
	}
	return nil
}

func (m *DemeterAudioQueueManager) reconcileInterruptedOperations(ctx context.Context) error {
	records, err := m.app.Store.ListDemeterAudioTranscriptionOperations(ctx, nil, []string{
		store.DemeterAudioTranscriptionOperationStatusPending,
		store.DemeterAudioTranscriptionOperationStatusRunning,
	}, 1000)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record == nil {
			continue
		}
		payload, err := decodeDemeterAudioQueuePayload(record.QueuePayloadJSON)
		if err != nil {
			if updateErr := m.failClaimedOperation(ctx, record, fmt.Sprintf("queue payload invalid: %v", err), httpStatusInternalServerError(), false); updateErr != nil {
				return updateErr
			}
			continue
		}
		if payload == nil || strings.TrimSpace(payload.Upload.SourcePath) == "" {
			if updateErr := m.failClaimedOperation(ctx, record, "source file unavailable after restart", httpStatusInternalServerError(), false); updateErr != nil {
				return updateErr
			}
			continue
		}
		if _, statErr := os.Stat(payload.Upload.SourcePath); statErr != nil {
			if updateErr := m.failClaimedOperation(ctx, record, fmt.Sprintf("source file unavailable after restart: %v", statErr), httpStatusInternalServerError(), false); updateErr != nil {
				return updateErr
			}
			continue
		}
		if record.Status == store.DemeterAudioTranscriptionOperationStatusRunning {
			reset := &store.DemeterAudioTranscriptionOperationRecord{
				OperationID:      record.OperationID,
				OrganizationID:   record.OrganizationID,
				UserID:           record.UserID,
				Status:           store.DemeterAudioTranscriptionOperationStatusPending,
				Stage:            "queued",
				ChunkIndex:       0,
				ChunkCount:       record.ChunkCount,
				Progress:         0,
				QueueID:          record.QueueID,
				QueuePayloadJSON: record.QueuePayloadJSON,
				StatusCode:       httpStatusAccepted(),
				UpdatedAt:        time.Now().UTC(),
			}
			if err := m.app.Store.UpdateDemeterAudioTranscriptionOperationByID(ctx, reset); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *DemeterAudioQueueManager) EnqueueOperation(ctx context.Context, record *store.DemeterAudioTranscriptionOperationRecord) (int, error) {
	if m == nil || m.app == nil || m.app.Store == nil || record == nil {
		return 0, fmt.Errorf("queue manager is not configured")
	}
	if err := m.Start(context.Background()); err != nil {
		return 0, err
	}
	payload, err := decodeDemeterAudioQueuePayload(record.QueuePayloadJSON)
	if err != nil {
		return 0, err
	}
	if payload == nil {
		return 0, nil
	}
	m.mu.Lock()
	laneID := m.chooseLaneLocked(ctx, payload)
	parallelism := m.parallelism
	m.mu.Unlock()
	logCtx := newDemeterAudioLogContext(ctx)
	fields := map[string]any{
		"operation_id": record.OperationID,
		"queue_id":     laneID,
		"chunk_count":  record.ChunkCount,
		"parallelism":  parallelism,
		"status":       "success",
		"queue_status": record.Status,
	}
	if laneID > 0 {
		if err := m.app.Store.UpdateDemeterAudioTranscriptionOperationQueueByID(ctx, record.OperationID, laneID, time.Now().UTC()); err != nil {
			return 0, err
		}
		logDemeterAudioQueuePerformanceTaskCtx(logCtx, payload.Route, payload.Seq, "enqueue", "demeter_queue_enqueue", fields)
		m.ensureLaneWorker(ctx, laneID)
		m.notifyLaneWorkAvailable(laneID)
	} else {
		logDemeterAudioQueuePerformanceTaskCtx(logCtx, payload.Route, payload.Seq, "enqueue", "demeter_queue_enqueue", fields)
	}
	if err := m.rebalancePendingOperations(ctx); err != nil {
		return laneID, err
	}
	return laneID, nil
}

func (m *DemeterAudioQueueManager) Resize(ctx context.Context, route string, parallelism int) error {
	if m == nil || m.app == nil || m.app.Store == nil {
		return fmt.Errorf("queue manager is not configured")
	}
	if err := m.Start(context.Background()); err != nil {
		return err
	}
	if parallelism < 0 {
		parallelism = 0
	}
	if parallelism > demeterAudioQueueMaxParallelism {
		parallelism = demeterAudioQueueMaxParallelism
	}
	logCtx := newDemeterAudioLogContext(ctx)
	m.mu.Lock()
	currentParallelism := m.parallelism
	openWorkers, drainingWorkers, coolingWorkers := m.queueLifecycleSummaryLocked(time.Now().UTC())
	m.mu.Unlock()
	logDemeterAudioQueuePerformanceTaskCtx(logCtx, route, 0, "queue_resize_requested", "demeter_queue_resize_requested", map[string]any{
		"previous_parallelism":  currentParallelism,
		"requested_parallelism": parallelism,
		"open_workers":          openWorkers,
		"draining_workers":      drainingWorkers,
		"cooling_workers":       coolingWorkers,
	})
	if _, err := m.app.Store.SaveDemeterAudioQueueSettings(ctx, parallelism); err != nil {
		return err
	}
	m.mu.Lock()
	toDrain := make([]int, 0)
	for laneID, state := range m.lanes {
		if state == nil {
			continue
		}
		if laneID > parallelism && state.Open && !state.Draining {
			toDrain = append(toDrain, laneID)
		}
	}
	m.parallelism = parallelism
	assignedLaneIDs, err := m.loadAssignedLaneIDs(ctx)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	m.syncLaneStatesLocked(assignedLaneIDs)
	laneIDs := m.startableLaneIDsLocked()
	m.mu.Unlock()
	for _, laneID := range toDrain {
		logDemeterAudioQueuePerformanceTaskCtx(logCtx, route, 0, "worker_drain_requested", "demeter_worker_drain_requested", map[string]any{
			"queue_id":              laneID,
			"requested_parallelism": parallelism,
		})
	}
	startedWorkers := 0
	for _, laneID := range laneIDs {
		if m.ensureLaneWorker(ctx, laneID) {
			startedWorkers++
		}
	}
	if err := m.rebalancePendingOperations(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	openWorkers, drainingWorkers, coolingWorkers = m.queueLifecycleSummaryLocked(time.Now().UTC())
	m.mu.Unlock()
	logDemeterAudioQueuePerformanceTaskCtx(logCtx, route, 0, "queue_resize_applied", "demeter_queue_resize_applied", map[string]any{
		"previous_parallelism": currentParallelism,
		"parallelism":          parallelism,
		"started_workers":      startedWorkers,
		"drain_requested":      len(toDrain),
		"open_workers":         openWorkers,
		"draining_workers":     drainingWorkers,
		"cooling_workers":      coolingWorkers,
	})
	return nil
}

func (m *DemeterAudioQueueManager) Snapshot(ctx context.Context, limit int) (demeterAudioQueueSnapshot, error) {
	snapshot := demeterAudioQueueSnapshot{
		Workers:       []demeterAudioQueueLaneSnapshot{},
		Operations:    []demeterAudioQueueOperationSnapshot{},
		AllOperations: []demeterAudioQueueOperationSnapshot{},
	}
	if m == nil || m.app == nil || m.app.Store == nil {
		return snapshot, nil
	}
	if limit <= 0 {
		limit = 200
	}
	settings, err := m.app.Store.GetDemeterAudioQueueSettings(ctx)
	if err != nil {
		return snapshot, err
	}
	if settings == nil {
		settings = &store.DemeterAudioQueueSettingsRecord{Parallelism: demeterAudioQueueDefaultParallelism, UpdatedAt: time.Now().UTC()}
	}
	operations, err := m.app.Store.ListDemeterAudioTranscriptionOperations(ctx, nil, []string{
		store.DemeterAudioTranscriptionOperationStatusPending,
		store.DemeterAudioTranscriptionOperationStatusRunning,
	}, limit)
	if err != nil {
		return snapshot, err
	}
	allOperations, err := m.app.Store.ListDemeterAudioTranscriptionOperations(ctx, nil, nil, limit)
	if err != nil {
		return snapshot, err
	}

	m.mu.Lock()
	retryPaused, retryPausedLaneID, retryPausedOperationID, retryPausedChunkIndex, retryPausedSince := m.mistralRetryPauseStateLocked()
	laneIDs := make([]int, 0, len(m.lanes))
	for laneID := range m.lanes {
		laneIDs = append(laneIDs, laneID)
	}
	m.mu.Unlock()

	operationsByLane := map[int][]*store.DemeterAudioTranscriptionOperationRecord{}
	unassignedCount := 0
	pendingCount := 0
	runningCount := 0
	for _, op := range operations {
		if op == nil {
			continue
		}
		if op.Status == store.DemeterAudioTranscriptionOperationStatusPending {
			pendingCount++
		}
		if op.Status == store.DemeterAudioTranscriptionOperationStatusRunning {
			runningCount++
		}
		if op.QueueID <= 0 {
			unassignedCount++
			continue
		}
		operationsByLane[op.QueueID] = append(operationsByLane[op.QueueID], op)
		if !containsInt(laneIDs, op.QueueID) {
			laneIDs = append(laneIDs, op.QueueID)
		}
	}

	m.mu.Lock()
	sortIntSlice(laneIDs)
	workers := make([]demeterAudioQueueLaneSnapshot, 0, len(laneIDs))
	openWorkers := 0
	drainingWorkers := 0
	coolingWorkers := 0
	for _, laneID := range laneIDs {
		state := m.ensureLaneStateLocked(laneID)
		laneOps := operationsByLane[laneID]
		activeOperation := selectDemeterAudioQueueLaneOperation(state, laneOps)
		load := 0
		pending := 0
		running := 0
		for _, op := range laneOps {
			if op == nil {
				continue
			}
			workload := op.ChunkCount - op.ChunkIndex
			if workload < 1 {
				workload = 1
			}
			load += workload
			switch op.Status {
			case store.DemeterAudioTranscriptionOperationStatusPending:
				pending++
			case store.DemeterAudioTranscriptionOperationStatusRunning:
				running++
			}
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
		laneSnapshot := demeterAudioQueueLaneSnapshot{
			QueueID:            laneID,
			Open:               state.Open,
			Draining:           state.Draining,
			WorkerRunning:      state.WorkerRunning,
			Load:               load,
			PendingCount:       pending,
			RunningCount:       running,
			CurrentOperationID: state.CurrentOperationID,
			CurrentStatus:      state.CurrentStatus,
			CurrentStage:       state.CurrentStage,
			CurrentChunkIndex:  state.CurrentChunkIndex,
			CurrentChunkCount:  state.CurrentChunkCount,
			CurrentProgress:    state.CurrentProgress,
			LastError:          state.LastError,
		}
		if activeOperation != nil {
			laneSnapshot.CurrentOperationID = activeOperation.OperationID
			laneSnapshot.CurrentStatus = activeOperation.Status
			laneSnapshot.CurrentStage = activeOperation.Stage
			laneSnapshot.CurrentChunkIndex = activeOperation.ChunkIndex
			laneSnapshot.CurrentChunkCount = activeOperation.ChunkCount
			laneSnapshot.CurrentProgress = activeOperation.Progress
			if activeOperation.LastError.Valid {
				laneSnapshot.LastError = strings.TrimSpace(activeOperation.LastError.String)
			} else {
				laneSnapshot.LastError = ""
			}
		}
		if !state.CooldownUntil.IsZero() && state.CooldownUntil.After(time.Now().UTC()) {
			laneSnapshot.CooldownUntil = state.CooldownUntil.UTC().Format(time.RFC3339)
		}
		workers = append(workers, laneSnapshot)
	}
	m.mu.Unlock()

	opsSnapshot := make([]demeterAudioQueueOperationSnapshot, 0, len(operations))
	for _, op := range operations {
		if op == nil {
			continue
		}
		opsSnapshot = append(opsSnapshot, demeterAudioQueueOperationSnapshotFromRecord(op))
	}

	allOpsSnapshot := make([]demeterAudioQueueOperationSnapshot, 0, len(allOperations))
	for _, op := range allOperations {
		if op == nil {
			continue
		}
		allOpsSnapshot = append(allOpsSnapshot, demeterAudioQueueOperationSnapshotFromRecord(op))
	}

	snapshot.Settings = demeterAudioQueueSettingsSnapshot{
		Parallelism: settings.Parallelism,
		UpdatedAt:   settings.UpdatedAt.UTC().Format(time.RFC3339),
	}
	snapshot.Summary = demeterAudioQueueSummarySnapshot{
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
		RetryPausedChunkIndex:  retryPausedChunkIndex,
	}
	if retryPaused && !retryPausedSince.IsZero() {
		snapshot.Summary.RetryPausedSince = retryPausedSince.UTC().Format(time.RFC3339)
	}
	snapshot.Workers = workers
	snapshot.Operations = opsSnapshot
	snapshot.AllOperations = allOpsSnapshot
	return snapshot, nil
}

func selectDemeterAudioQueueLaneOperation(state *demeterAudioQueueLaneState, laneOps []*store.DemeterAudioTranscriptionOperationRecord) *store.DemeterAudioTranscriptionOperationRecord {
	if len(laneOps) == 0 {
		return nil
	}
	if state != nil {
		trimmed := strings.TrimSpace(state.CurrentOperationID)
		if trimmed != "" {
			for _, op := range laneOps {
				if op != nil && strings.TrimSpace(op.OperationID) == trimmed {
					return op
				}
			}
		}
	}
	for _, op := range laneOps {
		if op != nil && op.Status == store.DemeterAudioTranscriptionOperationStatusRunning {
			return op
		}
	}
	for _, op := range laneOps {
		if op != nil && op.Status == store.DemeterAudioTranscriptionOperationStatusPending {
			return op
		}
	}
	return laneOps[0]
}

func (m *DemeterAudioQueueManager) chooseLaneLocked(ctx context.Context, payload *demeterAudioQueueOperationPayload) int {
	if payload == nil || m == nil {
		return 0
	}
	paused, _, _, _, _ := m.mistralRetryPauseStateLocked()
	if paused {
		return 0
	}
	now := time.Now().UTC()
	candidateIDs := make([]int, 0, m.parallelism)
	for laneID := 1; laneID <= m.parallelism; laneID++ {
		state := m.ensureLaneStateLocked(laneID)
		if !state.Open || state.Draining {
			continue
		}
		if !state.CooldownUntil.IsZero() && state.CooldownUntil.After(now) {
			continue
		}
		candidateIDs = append(candidateIDs, laneID)
	}
	if len(candidateIDs) == 0 {
		return 0
	}

	leastLaneID := 0
	leastLoad := int(^uint(0) >> 1)
	for _, laneID := range candidateIDs {
		load, err := m.laneWorkload(ctx, laneID)
		if err != nil {
			continue
		}
		if load < leastLoad || (load == leastLoad && (leastLaneID == 0 || laneID < leastLaneID)) {
			leastLoad = load
			leastLaneID = laneID
		}
	}
	return leastLaneID
}

func (m *DemeterAudioQueueManager) laneWorkload(ctx context.Context, laneID int) (int, error) {
	records, err := m.app.Store.ListDemeterAudioTranscriptionOperations(ctx, &laneID, []string{
		store.DemeterAudioTranscriptionOperationStatusPending,
		store.DemeterAudioTranscriptionOperationStatusRunning,
	}, 1000)
	if err != nil {
		return 0, err
	}
	load := 0
	for _, record := range records {
		if record == nil {
			continue
		}
		workload := record.ChunkCount - record.ChunkIndex
		if workload < 1 {
			workload = 1
		}
		load += workload
	}
	return load, nil
}

func (m *DemeterAudioQueueManager) rebalancePendingOperations(ctx context.Context) error {
	if m == nil || m.app == nil || m.app.Store == nil {
		return nil
	}
	pending, err := m.app.Store.ListDemeterAudioTranscriptionOperations(ctx, nil, []string{
		store.DemeterAudioTranscriptionOperationStatusPending,
	}, 1000)
	if err != nil {
		return err
	}
	now := time.Now().UTC()

	m.mu.Lock()
	paused, _, _, _, _ := m.mistralRetryPauseStateLocked()
	if paused {
		m.mu.Unlock()
		return nil
	}
	openLaneIDs := make([]int, 0, m.parallelism)
	for laneID := 1; laneID <= m.parallelism; laneID++ {
		state := m.ensureLaneStateLocked(laneID)
		if state.Open && !state.Draining {
			if state.CooldownUntil.IsZero() || !state.CooldownUntil.After(now) {
				openLaneIDs = append(openLaneIDs, laneID)
			}
		}
	}
	m.mu.Unlock()

	if len(openLaneIDs) == 0 {
		return nil
	}

	type laneLoad struct {
		id   int
		load int
	}
	loads := make([]laneLoad, 0, len(openLaneIDs))
	for _, laneID := range openLaneIDs {
		load, err := m.laneWorkload(ctx, laneID)
		if err != nil {
			continue
		}
		loads = append(loads, laneLoad{id: laneID, load: load})
	}
	if len(loads) == 0 {
		return nil
	}

	for _, record := range pending {
		if record == nil || record.QueueID > 0 {
			continue
		}
		bestIndex := -1
		for i, lane := range loads {
			if bestIndex < 0 || lane.load < loads[bestIndex].load || (lane.load == loads[bestIndex].load && lane.id < loads[bestIndex].id) {
				bestIndex = i
			}
		}
		if bestIndex < 0 {
			break
		}
		laneID := loads[bestIndex].id
		if err := m.app.Store.UpdateDemeterAudioTranscriptionOperationQueueByID(ctx, record.OperationID, laneID, now); err != nil {
			return err
		}
		workload := record.ChunkCount - record.ChunkIndex
		if workload < 1 {
			workload = 1
		}
		loads[bestIndex].load += workload
		m.ensureLaneWorker(ctx, laneID)
		m.notifyLaneWorkAvailable(laneID)
	}
	return nil
}

func (m *DemeterAudioQueueManager) setLaneCooldown(laneID int, duration time.Duration) {
	if laneID <= 0 || duration <= 0 || m == nil {
		return
	}
	m.mu.Lock()
	state := m.ensureLaneStateLocked(laneID)
	state.CooldownUntil = time.Now().UTC().Add(duration)
	m.mu.Unlock()
}

func (m *DemeterAudioQueueManager) setLaneCurrentOperation(laneID int, operationID, status, stage string, chunkIndex, chunkCount int, progress float64, lastError string) {
	if laneID <= 0 || m == nil {
		return
	}
	m.mu.Lock()
	state := m.ensureLaneStateLocked(laneID)
	state.CurrentOperationID = strings.TrimSpace(operationID)
	state.CurrentStatus = strings.TrimSpace(status)
	state.CurrentStage = strings.TrimSpace(stage)
	state.CurrentChunkIndex = chunkIndex
	state.CurrentChunkCount = chunkCount
	state.CurrentProgress = progress
	state.LastError = strings.TrimSpace(lastError)
	m.mu.Unlock()
}

func (m *DemeterAudioQueueManager) clearLaneCurrentOperation(laneID int) {
	m.setLaneCurrentOperation(laneID, "", "", "", 0, 0, 0, "")
}

func (m *DemeterAudioQueueManager) queueLifecycleSummaryLocked(now time.Time) (openWorkers, drainingWorkers, coolingWorkers int) {
	if m == nil {
		return 0, 0, 0
	}
	for _, state := range m.lanes {
		if state == nil {
			continue
		}
		if state.Open {
			openWorkers++
		}
		if state.Draining {
			drainingWorkers++
		}
		if !state.CooldownUntil.IsZero() && state.CooldownUntil.After(now) {
			coolingWorkers++
		}
	}
	return openWorkers, drainingWorkers, coolingWorkers
}

func (m *DemeterAudioQueueManager) snapshotLaneState(laneID int) *demeterAudioQueueLaneState {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.lanes[laneID]
	if !ok || state == nil {
		return &demeterAudioQueueLaneState{ID: laneID}
	}
	clone := *state
	return &clone
}

func (m *DemeterAudioQueueManager) laneHasWork(ctx context.Context, laneID int) bool {
	records, err := m.app.Store.ListDemeterAudioTranscriptionOperations(ctx, &laneID, []string{
		store.DemeterAudioTranscriptionOperationStatusPending,
		store.DemeterAudioTranscriptionOperationStatusRunning,
	}, 1)
	return err == nil && len(records) > 0
}

func (m *DemeterAudioQueueManager) sleepContext(duration time.Duration) bool {
	if duration <= 0 {
		return true
	}
	if m == nil || m.ctx == nil {
		time.Sleep(duration)
		return true
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-m.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (m *DemeterAudioQueueManager) isFatalQueueError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, sql.ErrConnDone) || strings.Contains(strings.ToLower(err.Error()), "database is closed")
}

func decodeDemeterAudioQueuePayload(record sql.NullString) (*demeterAudioQueueOperationPayload, error) {
	if !record.Valid || strings.TrimSpace(record.String) == "" {
		return nil, nil
	}
	var payload demeterAudioQueueOperationPayload
	if err := json.Unmarshal([]byte(record.String), &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func httpStatusAccepted() int {
	return 202
}

func httpStatusInternalServerError() int {
	return 500
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func containsInt(values []int, needle int) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func sortIntSlice(values []int) {
	sort.Ints(values)
}
