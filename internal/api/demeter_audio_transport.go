package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"demeter-backend/internal/observability"
	"demeter-backend/internal/requestmeta"
	"demeter-backend/internal/store"

	"github.com/gofiber/fiber/v2"
)

const (
	demeterAudioTransportModeSliceV1           = "slice-v1"
	demeterAudioTransportHeader                = "X-Demeter-Transport"
	demeterAudioTransportUploadIDHeader        = "X-Demeter-Upload-Id"
	demeterAudioTransportUploadIndexHeader     = "X-Demeter-Upload-Index"
	demeterAudioTransportUploadCountHeader     = "X-Demeter-Upload-Count"
	demeterAudioTransportUploadFinalHeader     = "X-Demeter-Upload-Final"
	demeterAudioTransportSessionRetention      = 30 * time.Minute
	demeterAudioTransportSliceFileExt          = ".part"
	demeterAudioTransportFinalizationStage     = "reconstructing"
	demeterAudioTransportReconstructedStage    = "queued"
	demeterAudioTransportUpdateChunkCountStage = "running"
	demeterAudioTransportSessionRootDirName    = "demeter-transport"
)

var demeterAudioTransportSessions sync.Map

type demeterAudioTransportOwnershipError struct {
	UploadID              string
	RequestOrganizationID string
	RequestUserID         string
	StoredOrganizationID  string
	StoredUserID          string
	Reason                string
	Source                string
}

func (e *demeterAudioTransportOwnershipError) Error() string {
	return "upload session owned by another user"
}

func (e *demeterAudioTransportOwnershipError) LogFields() map[string]any {
	if e == nil {
		return map[string]any{}
	}
	fields := map[string]any{
		"upload_id":       strings.TrimSpace(e.UploadID),
		"request_org_id":  strings.TrimSpace(e.RequestOrganizationID),
		"request_user_id": strings.TrimSpace(e.RequestUserID),
		"reason":          strings.TrimSpace(e.Reason),
		"source":          strings.TrimSpace(e.Source),
	}
	if strings.TrimSpace(e.StoredOrganizationID) != "" {
		fields["stored_org_id"] = strings.TrimSpace(e.StoredOrganizationID)
	}
	if strings.TrimSpace(e.StoredUserID) != "" {
		fields["stored_user_id"] = strings.TrimSpace(e.StoredUserID)
	}
	return fields
}

func newDemeterAudioTransportOwnershipError(source, reason, uploadID, requestOrgID, requestUserID string, session *demeterAudioTransportSession) *demeterAudioTransportOwnershipError {
	err := &demeterAudioTransportOwnershipError{
		UploadID:              strings.TrimSpace(uploadID),
		RequestOrganizationID: strings.TrimSpace(requestOrgID),
		RequestUserID:         strings.TrimSpace(requestUserID),
		Reason:                strings.TrimSpace(reason),
		Source:                strings.TrimSpace(source),
	}
	if err.Reason == "" {
		err.Reason = "ownership_mismatch"
	}
	if err.Source == "" {
		err.Source = "transport_session"
	}
	if session != nil {
		err.StoredOrganizationID = strings.TrimSpace(session.orgID)
		err.StoredUserID = strings.TrimSpace(session.userID)
	}
	return err
}

type demeterAudioTransportSliceRequest struct {
	UploadID              string
	SliceIndex            int
	SliceCount            int
	Final                 bool
	FileName              string
	MimeType              string
	Model                 string
	Diarize               bool
	AudioDurationSec      float64
	AudioDurationProvided bool
}

type demeterAudioTransportSession struct {
	mu                    sync.Mutex
	uploadID              string
	orgID                 string
	userID                string
	route                 string
	routeMode             string
	fileName              string
	mimeType              string
	sourceFormat          string
	model                 string
	diarize               bool
	audioDurationSec      float64
	audioDurationProvided bool
	sliceCount            int
	totalBytes            int64
	tempDir               string
	receivedPaths         map[int]string
	receivedSizes         map[int]int64
	createdAt             time.Time
	updatedAt             time.Time
	finalizing            bool
	finalized             bool
	cleanupOnce           sync.Once
}

func (s *demeterAudioTransportSession) cleanup() {
	if s == nil {
		return
	}
	s.cleanupOnce.Do(func() {
		if strings.TrimSpace(s.tempDir) != "" {
			_ = os.RemoveAll(s.tempDir)
		}
	})
}

func demeterAudioTransportRootDir() string {
	return filepath.Join(os.TempDir(), demeterAudioTransportSessionRootDirName)
}

func demeterAudioTransportSessionDir(uploadID string) string {
	return filepath.Join(demeterAudioTransportRootDir(), sanitizeDemeterAudioTransportUploadID(uploadID))
}

func sanitizeDemeterAudioTransportUploadID(uploadID string) string {
	trimmed := strings.TrimSpace(uploadID)
	if trimmed == "" {
		return "unknown"
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':':
			return '_'
		default:
			return r
		}
	}, trimmed)
}

func demeterAudioTransportSliceFileName(sliceIndex int) string {
	return fmt.Sprintf("slice_%06d%s", sliceIndex+1, demeterAudioTransportSliceFileExt)
}

func demeterAudioTransportSlicePath(tempDir string, sliceIndex int) string {
	return filepath.Join(tempDir, demeterAudioTransportSliceFileName(sliceIndex))
}

func demeterAudioTransportTotalBytes(tempDir string, sliceCount int) int64 {
	if sliceCount <= 0 {
		return 0
	}

	var total int64
	for index := 0; index < sliceCount; index++ {
		info, err := os.Stat(demeterAudioTransportSlicePath(tempDir, index))
		if err != nil || info.IsDir() {
			continue
		}
		total += info.Size()
	}
	return total
}

func isDemeterAudioSliceTransport(c *fiber.Ctx) bool {
	return strings.EqualFold(strings.TrimSpace(c.Get(demeterAudioTransportHeader)), demeterAudioTransportModeSliceV1)
}

func cleanupExpiredDemeterAudioTransportSessions(now time.Time) {
	demeterAudioTransportSessions.Range(func(key, value any) bool {
		session, ok := value.(*demeterAudioTransportSession)
		if !ok || session == nil {
			demeterAudioTransportSessions.Delete(key)
			return true
		}

		session.mu.Lock()
		expired := !session.finalized && !session.finalizing && now.Sub(session.updatedAt) > demeterAudioTransportSessionRetention
		session.mu.Unlock()
		if expired {
			session.cleanup()
			demeterAudioTransportSessions.Delete(key)
		}
		return true
	})
}

func parseDemeterAudioTransportSliceRequest(c *fiber.Ctx) (*demeterAudioTransportSliceRequest, error) {
	if c == nil {
		return nil, fmt.Errorf("missing request context")
	}

	uploadID := cloneDemeterRequestString(c.Get(demeterAudioTransportUploadIDHeader))
	if uploadID == "" {
		return nil, fmt.Errorf("missing upload id")
	}
	sliceIndex, err := strconv.Atoi(strings.TrimSpace(c.Get(demeterAudioTransportUploadIndexHeader)))
	if err != nil || sliceIndex < 0 {
		return nil, fmt.Errorf("invalid slice index")
	}
	sliceCount, err := strconv.Atoi(strings.TrimSpace(c.Get(demeterAudioTransportUploadCountHeader)))
	if err != nil || sliceCount <= 0 {
		return nil, fmt.Errorf("invalid slice count")
	}
	if sliceIndex >= sliceCount {
		return nil, fmt.Errorf("slice index out of bounds")
	}
	final := false
	if raw := strings.TrimSpace(c.Get(demeterAudioTransportUploadFinalHeader)); raw != "" {
		if parsed, parseErr := strconv.ParseBool(raw); parseErr == nil {
			final = parsed
		}
	}

	form, err := c.MultipartForm()
	if err != nil {
		return nil, fmt.Errorf("failed to read multipart form: %w", err)
	}
	fileHeaders := form.File["file"]
	if len(fileHeaders) == 0 {
		return nil, &demeterAudioValidationError{
			code:    "invalid_multipart",
			message: "multipart file part is missing",
			file:    demeterAudioFileInfo{},
		}
	}
	fileHeader := fileHeaders[0]

	model := defaultDemeterAudioTranscriptionModelID
	if values := form.Value["model"]; len(values) > 0 && strings.TrimSpace(values[0]) != "" {
		model = strings.TrimSpace(values[0])
	}
	diarize := false
	if values := form.Value["diarize"]; len(values) > 0 {
		if parsed, parseErr := strconv.ParseBool(strings.TrimSpace(values[0])); parseErr == nil {
			diarize = parsed
		}
	}
	if !diarize {
		if values := form.Value["timestamp_granularities"]; len(values) > 0 {
			for _, value := range values {
				if strings.TrimSpace(value) != "" {
					diarize = true
					break
				}
			}
		}
	}

	audioDurationSec, audioDurationProvided := requestDemeterAudioDurationSec(c)

	return &demeterAudioTransportSliceRequest{
		UploadID:              uploadID,
		SliceIndex:            sliceIndex,
		SliceCount:            sliceCount,
		Final:                 final,
		FileName:              cloneDemeterRequestString(normalizeDemeterAudioFileName(fileHeader.Filename)),
		MimeType:              cloneDemeterRequestString(normalizeDemeterAudioMimeType(fileHeader.Header.Get("Content-Type"), fileHeader.Filename)),
		Model:                 cloneDemeterRequestString(model),
		Diarize:               diarize,
		AudioDurationSec:      audioDurationSec,
		AudioDurationProvided: audioDurationProvided,
	}, nil
}

func getOrCreateDemeterAudioTransportSession(
	uploadID string,
	orgID string,
	userID string,
	route string,
	routeMode string,
	req *demeterAudioTransportSliceRequest,
) (*demeterAudioTransportSession, error) {
	now := time.Now().UTC()
	if value, ok := demeterAudioTransportSessions.Load(uploadID); ok {
		session, ok := value.(*demeterAudioTransportSession)
		if !ok || session == nil {
			demeterAudioTransportSessions.Delete(uploadID)
		} else {
			session.mu.Lock()
			defer session.mu.Unlock()
			if session.orgID != orgID || session.userID != userID {
				return nil, newDemeterAudioTransportOwnershipError("transport_session", "ownership_mismatch", uploadID, orgID, userID, session)
			}
			if session.routeMode != "" && session.routeMode != routeMode {
				return nil, fmt.Errorf("upload session route mode mismatch")
			}
			if session.route == "" {
				session.route = route
			}
			session.updatedAt = now
			return session, nil
		}
	}

	tempDir := demeterAudioTransportSessionDir(uploadID)
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create transport temp dir: %w", err)
	}
	session := &demeterAudioTransportSession{
		uploadID:              cloneDemeterRequestString(uploadID),
		orgID:                 cloneDemeterRequestString(orgID),
		userID:                cloneDemeterRequestString(userID),
		route:                 cloneDemeterRequestString(route),
		routeMode:             cloneDemeterRequestString(routeMode),
		fileName:              cloneDemeterRequestString(req.FileName),
		mimeType:              cloneDemeterRequestString(req.MimeType),
		sourceFormat:          cloneDemeterRequestString(resolveDemeterAudioKind(req.FileName, req.MimeType)),
		model:                 cloneDemeterRequestString(req.Model),
		diarize:               req.Diarize,
		audioDurationSec:      req.AudioDurationSec,
		audioDurationProvided: req.AudioDurationProvided,
		sliceCount:            req.SliceCount,
		tempDir:               tempDir,
		receivedPaths:         map[int]string{},
		receivedSizes:         map[int]int64{},
		createdAt:             now,
		updatedAt:             now,
	}
	demeterAudioTransportSessions.Store(uploadID, session)
	return session, nil
}

func storeDemeterAudioTransportSlice(session *demeterAudioTransportSession, req *demeterAudioTransportSliceRequest, fileHeader *multipart.FileHeader) (int64, error) {
	if session == nil {
		return 0, fmt.Errorf("missing transport session")
	}
	if fileHeader == nil {
		return 0, fmt.Errorf("missing file header")
	}

	input, err := fileHeader.Open()
	if err != nil {
		return 0, fmt.Errorf("failed to open uploaded audio slice: %w", err)
	}
	defer func() {
		_ = input.Close()
	}()

	session.mu.Lock()
	defer session.mu.Unlock()

	if session.finalizing || session.finalized {
		return 0, fmt.Errorf("upload already finalized")
	}
	if session.sliceCount != 0 && session.sliceCount != req.SliceCount {
		return 0, fmt.Errorf("slice count mismatch")
	}
	if session.sliceCount == 0 {
		session.sliceCount = req.SliceCount
	}
	if session.fileName == "" {
		session.fileName = req.FileName
	} else if req.FileName != "" && session.fileName != req.FileName {
		return 0, fmt.Errorf("file name mismatch")
	}
	if session.mimeType == "" {
		session.mimeType = req.MimeType
	} else if req.MimeType != "" && session.mimeType != req.MimeType {
		return 0, fmt.Errorf("mime type mismatch")
	}
	if session.model == "" {
		session.model = req.Model
	} else if req.Model != "" && session.model != req.Model {
		return 0, fmt.Errorf("model mismatch")
	}
	if session.audioDurationProvided {
		if req.AudioDurationProvided && math.Abs(session.audioDurationSec-req.AudioDurationSec) > 0.001 {
			return 0, fmt.Errorf("audio duration mismatch")
		}
	} else if req.AudioDurationProvided {
		session.audioDurationSec = req.AudioDurationSec
		session.audioDurationProvided = true
	}
	session.diarize = session.diarize || req.Diarize
	if session.sourceFormat == "" {
		session.sourceFormat = resolveDemeterAudioKind(session.fileName, session.mimeType)
	}

	slicePath := demeterAudioTransportSlicePath(session.tempDir, req.SliceIndex)
	output, err := os.Create(slicePath)
	if err != nil {
		return 0, fmt.Errorf("failed to create transport slice file: %w", err)
	}
	sizeCopied, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		_ = os.Remove(slicePath)
		return 0, fmt.Errorf("failed to persist transport slice: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(slicePath)
		return 0, fmt.Errorf("failed to close transport slice file: %w", closeErr)
	}
	if sizeCopied <= 0 {
		_ = os.Remove(slicePath)
		return 0, &demeterAudioValidationError{
			code:    "empty_audio_file",
			message: "fichier audio vide",
			file: demeterAudioFileInfo{
				FileName:  req.FileName,
				MimeType:  req.MimeType,
				SizeBytes: sizeCopied,
			},
		}
	}

	if previousPath, ok := session.receivedPaths[req.SliceIndex]; ok && previousPath != slicePath {
		_ = os.Remove(previousPath)
	}
	session.receivedPaths[req.SliceIndex] = slicePath
	session.receivedSizes[req.SliceIndex] = sizeCopied
	session.totalBytes = demeterAudioTransportTotalBytes(session.tempDir, session.sliceCount)
	session.updatedAt = time.Now().UTC()
	return sizeCopied, nil
}

func demeterTransportSessionSlicePaths(session *demeterAudioTransportSession) ([]string, error) {
	if session == nil {
		return nil, fmt.Errorf("missing transport session")
	}
	session.mu.Lock()
	defer session.mu.Unlock()

	if session.sliceCount <= 0 {
		return nil, fmt.Errorf("missing slice count")
	}
	missing := make([]int, 0)
	paths := make([]string, 0, session.sliceCount)
	for index := 0; index < session.sliceCount; index++ {
		path := demeterAudioTransportSlicePath(session.tempDir, index)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			missing = append(missing, index)
			continue
		}
		paths = append(paths, path)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing transport slices: %v", missing)
	}
	return paths, nil
}

func demeterAudioSourceFileExt(fileName, mimeType string) string {
	switch resolveDemeterAudioKind(fileName, mimeType) {
	case "mp3":
		return ".mp3"
	case "mp4":
		return ".m4a"
	case "aac":
		return ".aac"
	case "ogg":
		return ".ogg"
	case "webm":
		return ".webm"
	case "wav":
		return ".wav"
	default:
		return demeterAudioChunkFileExt
	}
}

func buildDemeterBackendAudioUploadFromTransportSession(ctx context.Context, session *demeterAudioTransportSession) (*demeterBackendAudioUpload, error) {
	if session == nil {
		return nil, fmt.Errorf("missing transport session")
	}

	paths, err := demeterTransportSessionSlicePaths(session)
	if err != nil {
		return nil, err
	}

	session.mu.Lock()
	fileName := session.fileName
	mimeType := session.mimeType
	model := session.model
	diarize := session.diarize
	sourceFormat := session.sourceFormat
	tempDir := session.tempDir
	totalBytes := demeterAudioTransportTotalBytes(tempDir, session.sliceCount)
	session.mu.Unlock()

	sourceExt := demeterAudioSourceFileExt(fileName, mimeType)
	sourcePath := filepath.Join(tempDir, "source"+sourceExt)
	output, err := os.Create(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create reconstructed audio file: %w", err)
	}
	for _, path := range paths {
		input, openErr := os.Open(path)
		if openErr != nil {
			_ = output.Close()
			return nil, fmt.Errorf("failed to open transport slice: %w", openErr)
		}
		if _, copyErr := io.Copy(output, input); copyErr != nil {
			_ = input.Close()
			_ = output.Close()
			return nil, fmt.Errorf("failed to reassemble transport slices: %w", copyErr)
		}
		_ = input.Close()
	}
	if err := output.Close(); err != nil {
		return nil, fmt.Errorf("failed to finalize reconstructed audio file: %w", err)
	}
	loggedUpload, err := buildDemeterBackendAudioUploadFromSource(ctx, tempDir, sourcePath, fileName, mimeType, model, diarize, sourceFormat)
	if err != nil {
		return nil, err
	}
	if loggedUpload != nil && totalBytes > 0 {
		loggedUpload.SizeBytes = totalBytes
	}
	return loggedUpload, nil
}

func (a *App) startDemeterAudioTransportTranscriptionOperation(
	c *fiber.Ctx,
	logCtx demeterAudioLogContext,
	route string,
	seq uint64,
	routeMode string,
	audioDurationSec float64,
	audioDurationProvided bool,
	requestBytes int,
	session *demeterAudioTransportSession,
	startedAt time.Time,
) error {
	claims := MustClaims(c)
	if claims == nil {
		logDemeterRelayIssueCtx(logCtx, route, fiber.StatusUnauthorized, "missing claims")
		logDemeterAudioStageCtx(logCtx, route, seq, "sequence_end", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"result":            "unauthorized",
			"status_code":       fiber.StatusUnauthorized,
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
		}))
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized", Code: "unauthorized", TraceID: requestTraceID(c), Path: route})
	}

	if session == nil {
		logDemeterRelayIssueCtx(logCtx, route, fiber.StatusBadRequest, "missing transport session")
		logDemeterAudioStageCtx(logCtx, route, seq, "sequence_end", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"result":            "missing_transport_session",
			"status_code":       fiber.StatusBadRequest,
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
		}))
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "missing transport session"})
	}

	now := time.Now().UTC()
	initialResponse := &store.DemeterAudioTranscriptionOperationRecord{
		OperationID:    session.uploadID,
		OrganizationID: claims.OrgID,
		UserID:         claims.UserID,
		Status:         store.DemeterAudioTranscriptionOperationStatusRunning,
		Stage:          demeterAudioTransportFinalizationStage,
		ChunkIndex:     0,
		ChunkCount:     0,
		Progress:       0,
		PartialText:    sql.NullString{String: "", Valid: false},
		StatusCode:     fiber.StatusAccepted,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := a.Store.CreateDemeterAudioTranscriptionOperation(requestContext(c), initialResponse); err != nil {
		if existing, loadErr := a.Store.GetDemeterAudioTranscriptionOperation(requestContext(c), session.uploadID, claims.OrgID, claims.UserID); loadErr == nil {
			return c.Status(existing.StatusCode).JSON(demeterAudioTranscriptionOperationStartResponse{
				demeterAudioTranscriptionOperationResponse: demeterAudioTranscriptionOperationResponseFromRecord(existing),
			})
		}
		logDemeterRelayIssueCtx(logCtx, route, fiber.StatusInternalServerError, err.Error())
		logDemeterAudioStageCtx(logCtx, route, seq, "sequence_end", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"result":            "operation_create_error",
			"status_code":       fiber.StatusInternalServerError,
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
			"message":           err.Error(),
		}))
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to create backend transcription operation"})
	}

	workerBaseCtx := requestmeta.WithActor(observability.WithTraceID(context.Background(), requestTraceID(c)), claims.UserID, claims.OrgID)
	workerCtx, cancel := context.WithCancel(workerBaseCtx)
	demeterAudioTranscriptionOperationCancels.Store(session.uploadID, cancel)

	go a.runDemeterAudioTransportTranscriptionOperation(
		workerCtx,
		logCtx,
		cancel,
		session,
		route,
		seq,
		routeMode,
		audioDurationSec,
		audioDurationProvided,
		requestBytes,
		startedAt,
	)

	logDemeterAudioStageCtx(logCtx, route, seq, "operation_started", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
		"operation_id":      session.uploadID,
		"stage":             demeterAudioTransportFinalizationStage,
		"total_duration_ms": time.Since(startedAt).Milliseconds(),
		"request_bytes":     requestBytes,
	}))
	logDemeterAudioStageCtx(logCtx, route, seq, "sequence_end", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
		"result":            "accepted",
		"status_code":       fiber.StatusAccepted,
		"operation_id":      session.uploadID,
		"stage":             demeterAudioTransportFinalizationStage,
		"total_duration_ms": time.Since(startedAt).Milliseconds(),
		"request_bytes":     requestBytes,
	}))

	return c.Status(fiber.StatusAccepted).JSON(demeterAudioTranscriptionOperationStartResponse{
		demeterAudioTranscriptionOperationResponse: demeterAudioTranscriptionOperationResponse{
			OperationID: session.uploadID,
			Status:      store.DemeterAudioTranscriptionOperationStatusRunning,
			StatusCode:  fiber.StatusAccepted,
			Stage:       demeterAudioTransportFinalizationStage,
			ChunkIndex:  0,
			ChunkCount:  0,
			Progress:    0,
			UpdatedAt:   now.Format(time.RFC3339),
		},
	})
}

func (a *App) runDemeterAudioTransportTranscriptionOperation(
	ctx context.Context,
	baseLogCtx demeterAudioLogContext,
	cancel context.CancelFunc,
	session *demeterAudioTransportSession,
	route string,
	seq uint64,
	routeMode string,
	audioDurationSec float64,
	audioDurationProvided bool,
	requestBytes int,
	startedAt time.Time,
) {
	defer demeterAudioTranscriptionOperationCancels.Delete(session.uploadID)
	defer cancel()
	defer func() {
		if session != nil {
			demeterAudioTransportSessions.Delete(session.uploadID)
			session.cleanup()
		}
	}()

	logCtx := demeterAudioLogContext{
		ctx:     ctx,
		traceID: baseLogCtx.traceID,
		userID:  baseLogCtx.userID,
		orgID:   baseLogCtx.orgID,
	}
	logDemeterAudioStageCtx(logCtx, route, seq, "operation_worker_start", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
		"operation_id":      session.uploadID,
		"stage":             demeterAudioTransportFinalizationStage,
		"request_bytes":     requestBytes,
		"transport_bytes":   session.totalBytes,
		"total_duration_ms": time.Since(startedAt).Milliseconds(),
	}))

	upload, err := buildDemeterBackendAudioUploadFromTransportSession(ctx, session)
	if err != nil {
		statusCode := fiber.StatusBadGateway
		lastError := err.Error()
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			statusCode = fiber.StatusRequestTimeout
			lastError = "operation cancelled"
		}
		fallbackUsed, updateErr := a.updateDemeterAudioTranscriptionOperationStateWithFallback(ctx, &store.DemeterAudioTranscriptionOperationRecord{
			OperationID:    session.uploadID,
			OrganizationID: baseLogCtx.orgID,
			UserID:         baseLogCtx.userID,
			Status:         store.DemeterAudioTranscriptionOperationStatusFailed,
			Stage:          "failed",
			ChunkIndex:     0,
			ChunkCount:     0,
			Progress:       0,
			LastError:      sql.NullString{String: lastError, Valid: true},
			StatusCode:     statusCode,
			UpdatedAt:      time.Now().UTC(),
			FinishedAt:     sql.NullTime{Time: time.Now().UTC(), Valid: true},
		})
		if fallbackUsed {
			logDemeterOwnershipStageCtx(logCtx, route, seq, "ownership_fallback_used", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
				"operation_id": session.uploadID,
				"stage":        "failed",
				"status":       store.DemeterAudioTranscriptionOperationStatusFailed,
				"status_code":  statusCode,
				"chunk_index":  0,
				"chunk_count":  0,
				"source":       "worker_update",
			}))
		}
		_ = updateErr
		logDemeterAudioStageCtx(logCtx, route, seq, "operation_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"operation_id":      session.uploadID,
			"status_code":       statusCode,
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
			"transport_bytes":   session.totalBytes,
			"message":           lastError,
		}))
		return
	}

	if upload == nil {
		fallbackUsed, updateErr := a.updateDemeterAudioTranscriptionOperationStateWithFallback(ctx, &store.DemeterAudioTranscriptionOperationRecord{
			OperationID:    session.uploadID,
			OrganizationID: baseLogCtx.orgID,
			UserID:         baseLogCtx.userID,
			Status:         store.DemeterAudioTranscriptionOperationStatusFailed,
			Stage:          "failed",
			ChunkIndex:     0,
			ChunkCount:     0,
			Progress:       0,
			LastError:      sql.NullString{String: "missing reconstructed upload", Valid: true},
			StatusCode:     fiber.StatusBadGateway,
			UpdatedAt:      time.Now().UTC(),
			FinishedAt:     sql.NullTime{Time: time.Now().UTC(), Valid: true},
		})
		if fallbackUsed {
			logDemeterOwnershipStageCtx(logCtx, route, seq, "ownership_fallback_used", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
				"operation_id": session.uploadID,
				"stage":        "failed",
				"status":       store.DemeterAudioTranscriptionOperationStatusFailed,
				"status_code":  fiber.StatusBadGateway,
				"chunk_index":  0,
				"chunk_count":  0,
				"source":       "worker_update",
			}))
		}
		_ = updateErr
		logDemeterAudioStageCtx(logCtx, route, seq, "operation_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"operation_id":      session.uploadID,
			"status_code":       fiber.StatusBadGateway,
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
			"transport_bytes":   session.totalBytes,
			"message":           "missing reconstructed upload",
		}))
		return
	}

	var chunkPlans []demeterBackendChunkPlan
	if routeMode == "relay" {
		wholeDuration := maxInt(1, int(math.Ceil(upload.DurationSec)))
		chunkIDPrefix := fmt.Sprintf("demeter-relay-%s", sanitizeDemeterAudioTransportUploadID(session.uploadID))
		chunkPlans = buildDemeterBackendChunkPlansWithPrefix(upload.DurationSec, wholeDuration, 0, chunkIDPrefix)
		if len(chunkPlans) == 0 {
			chunkPlans = []demeterBackendChunkPlan{{
				Index:    0,
				StartSec: 0,
				EndSec:   upload.DurationSec,
				Duration: upload.DurationSec,
				ChunkID:  fmt.Sprintf("%s-001", chunkIDPrefix),
				FileName: filepath.Base(upload.FileName),
				MimeType: upload.MimeType,
			}}
		}
	} else {
		if !a.MistralClient.IsConfigured() {
			fallbackUsed, updateErr := a.updateDemeterAudioTranscriptionOperationStateWithFallback(ctx, &store.DemeterAudioTranscriptionOperationRecord{
				OperationID:    session.uploadID,
				OrganizationID: baseLogCtx.orgID,
				UserID:         baseLogCtx.userID,
				Status:         store.DemeterAudioTranscriptionOperationStatusFailed,
				Stage:          "failed",
				ChunkIndex:     0,
				ChunkCount:     0,
				Progress:       0,
				LastError:      sql.NullString{String: "mistral client is not configured", Valid: true},
				StatusCode:     fiber.StatusServiceUnavailable,
				UpdatedAt:      time.Now().UTC(),
				FinishedAt:     sql.NullTime{Time: time.Now().UTC(), Valid: true},
			})
			if fallbackUsed {
				logDemeterOwnershipStageCtx(logCtx, route, seq, "ownership_fallback_used", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
					"operation_id": session.uploadID,
					"stage":        "failed",
					"status":       store.DemeterAudioTranscriptionOperationStatusFailed,
					"status_code":  fiber.StatusServiceUnavailable,
					"chunk_index":  0,
					"chunk_count":  0,
					"source":       "worker_update",
				}))
			}
			_ = updateErr
			logDemeterAudioStageCtx(logCtx, route, seq, "operation_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
				"operation_id":      session.uploadID,
				"status_code":       fiber.StatusServiceUnavailable,
				"total_duration_ms": time.Since(startedAt).Milliseconds(),
				"request_bytes":     requestBytes,
				"transport_bytes":   session.totalBytes,
				"message":           "mistral client is not configured",
			}))
			return
		}
		settings, loadErr := a.loadDemeterBackendAudioChunkSettings(ctx, baseLogCtx.userID)
		if loadErr != nil {
			fallbackUsed, updateErr := a.updateDemeterAudioTranscriptionOperationStateWithFallback(ctx, &store.DemeterAudioTranscriptionOperationRecord{
				OperationID:    session.uploadID,
				OrganizationID: baseLogCtx.orgID,
				UserID:         baseLogCtx.userID,
				Status:         store.DemeterAudioTranscriptionOperationStatusFailed,
				Stage:          "failed",
				ChunkIndex:     0,
				ChunkCount:     0,
				Progress:       0,
				LastError:      sql.NullString{String: loadErr.Error(), Valid: true},
				StatusCode:     fiber.StatusInternalServerError,
				UpdatedAt:      time.Now().UTC(),
				FinishedAt:     sql.NullTime{Time: time.Now().UTC(), Valid: true},
			})
			if fallbackUsed {
				logDemeterOwnershipStageCtx(logCtx, route, seq, "ownership_fallback_used", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
					"operation_id": session.uploadID,
					"stage":        "failed",
					"status":       store.DemeterAudioTranscriptionOperationStatusFailed,
					"status_code":  fiber.StatusBadRequest,
					"chunk_index":  0,
					"chunk_count":  0,
					"source":       "worker_update",
				}))
			}
			_ = updateErr
			logDemeterAudioStageCtx(logCtx, route, seq, "operation_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
				"operation_id":      session.uploadID,
				"status_code":       fiber.StatusInternalServerError,
				"total_duration_ms": time.Since(startedAt).Milliseconds(),
				"request_bytes":     requestBytes,
				"transport_bytes":   session.totalBytes,
				"message":           loadErr.Error(),
			}))
			return
		}
		chunking := resolveDemeterBackendChunkingConfig(settings, upload.Model)
		chunkPlans = buildDemeterBackendChunkPlans(upload.DurationSec, chunking.EffectiveDurationSec, chunking.EffectiveOverlapSec)
		if len(chunkPlans) == 0 {
			fallbackUsed, updateErr := a.updateDemeterAudioTranscriptionOperationStateWithFallback(ctx, &store.DemeterAudioTranscriptionOperationRecord{
				OperationID:    session.uploadID,
				OrganizationID: baseLogCtx.orgID,
				UserID:         baseLogCtx.userID,
				Status:         store.DemeterAudioTranscriptionOperationStatusFailed,
				Stage:          "failed",
				ChunkIndex:     0,
				ChunkCount:     0,
				Progress:       0,
				LastError:      sql.NullString{String: "fichier audio illisible", Valid: true},
				StatusCode:     fiber.StatusBadRequest,
				UpdatedAt:      time.Now().UTC(),
				FinishedAt:     sql.NullTime{Time: time.Now().UTC(), Valid: true},
			})
			if fallbackUsed {
				logDemeterOwnershipStageCtx(logCtx, route, seq, "ownership_fallback_used", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
					"operation_id": session.uploadID,
					"stage":        "failed",
					"status":       store.DemeterAudioTranscriptionOperationStatusFailed,
					"status_code":  fiber.StatusBadRequest,
					"chunk_index":  0,
					"chunk_count":  0,
					"source":       "worker_update",
				}))
			}
			_ = updateErr
			logDemeterAudioStageCtx(logCtx, route, seq, "operation_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
				"operation_id":      session.uploadID,
				"status_code":       fiber.StatusBadRequest,
				"total_duration_ms": time.Since(startedAt).Milliseconds(),
				"request_bytes":     requestBytes,
				"transport_bytes":   session.totalBytes,
				"message":           "fichier audio illisible",
			}))
			return
		}
	}

	fallbackUsed, updateErr := a.updateDemeterAudioTranscriptionOperationStateWithFallback(ctx, &store.DemeterAudioTranscriptionOperationRecord{
		OperationID:    session.uploadID,
		OrganizationID: baseLogCtx.orgID,
		UserID:         baseLogCtx.userID,
		Status:         store.DemeterAudioTranscriptionOperationStatusRunning,
		Stage:          demeterAudioTransportReconstructedStage,
		ChunkIndex:     0,
		ChunkCount:     len(chunkPlans),
		Progress:       0,
		StatusCode:     fiber.StatusAccepted,
		UpdatedAt:      time.Now().UTC(),
	})
	if fallbackUsed {
		logDemeterOwnershipStageCtx(logCtx, route, seq, "ownership_fallback_used", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"operation_id": session.uploadID,
			"stage":        demeterAudioTransportReconstructedStage,
			"status":       store.DemeterAudioTranscriptionOperationStatusRunning,
			"status_code":  fiber.StatusAccepted,
			"chunk_index":  0,
			"chunk_count":  len(chunkPlans),
			"source":       "worker_update",
		}))
	}
	_ = updateErr

	logDemeterAudioStageCtx(logCtx, route, seq, "transport_reconstructed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
		"operation_id":    session.uploadID,
		"file_name":       upload.FileName,
		"file_size_bytes": upload.SizeBytes,
		"mime_type":       upload.MimeType,
		"duration_sec":    upload.DurationSec,
		"chunk_count":     len(chunkPlans),
		"transport_bytes": session.totalBytes,
	}))

	a.runDemeterAudioTranscriptionOperation(
		ctx,
		baseLogCtx,
		cancel,
		session.uploadID,
		route,
		seq,
		routeMode,
		audioDurationSec,
		audioDurationProvided,
		requestBytes,
		upload,
		chunkPlans,
	)
}

func (a *App) demeterAudioTranscriptionsTransportSlice(
	c *fiber.Ctx,
	route string,
	seq uint64,
	startedAt time.Time,
	routeMode string,
	audioDurationSec float64,
	audioDurationProvided bool,
	requestBytes int,
	contentType string,
) error {
	logCtx := newDemeterAudioLogContextFromFiber(c)
	claims := MustClaims(c)
	if claims == nil {
		logDemeterRelayIssueCtx(logCtx, route, fiber.StatusUnauthorized, "missing claims")
		logDemeterAudioStageCtx(logCtx, route, seq, "sequence_end", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"result":            "unauthorized",
			"status_code":       fiber.StatusUnauthorized,
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
		}))
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized", Code: "unauthorized", TraceID: requestTraceID(c), Path: route})
	}
	if !a.MistralClient.IsConfigured() {
		logDemeterRelayIssueCtx(logCtx, route, fiber.StatusServiceUnavailable, "mistral client is not configured")
		logDemeterAudioStageCtx(logCtx, route, seq, "sequence_end", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"result":            "mistral_not_configured",
			"status_code":       fiber.StatusServiceUnavailable,
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
		}))
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "mistral is not configured"})
	}

	req, err := parseDemeterAudioTransportSliceRequest(c)
	if err != nil {
		logDemeterRelayIssueCtx(logCtx, route, fiber.StatusBadRequest, err.Error())
		logDemeterAudioStageCtx(logCtx, route, seq, "request_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"result":            "invalid_transport",
			"status_code":       fiber.StatusBadRequest,
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
			"message":           err.Error(),
		}))
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error:   err.Error(),
			Code:    "invalid_transport",
			TraceID: requestTraceID(c),
			Path:    route,
		})
	}

	cleanupExpiredDemeterAudioTransportSessions(time.Now().UTC())

	if req.Final {
		if existing, loadErr := a.Store.GetDemeterAudioTranscriptionOperation(requestContext(c), req.UploadID, claims.OrgID, claims.UserID); loadErr == nil {
			logDemeterAudioStageCtx(logCtx, route, seq, "transport_final_retry", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
				"operation_id":    req.UploadID,
				"status":          existing.Status,
				"stage":           existing.Stage,
				"chunk_count":     existing.ChunkCount,
				"chunk_index":     existing.ChunkIndex,
				"transport_bytes": requestBytes,
			}))
			return c.Status(existing.StatusCode).JSON(demeterAudioTranscriptionOperationResponseFromRecord(existing))
		} else {
			var ownershipErr *store.DemeterAudioTranscriptionOperationOwnershipError
			if errors.As(loadErr, &ownershipErr) {
				logDemeterOwnershipStageCtx(logCtx, route, seq, "ownership_transport_error", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
					"operation_id":    req.UploadID,
					"upload_id":       req.UploadID,
					"status_code":     fiber.StatusBadRequest,
					"reason":          ownershipErr.Reason,
					"source":          "transport_final_retry",
					"request_user_id": claims.UserID,
					"request_org_id":  claims.OrgID,
					"stored_user_id":  ownershipErr.StoredUserID,
					"stored_org_id":   ownershipErr.StoredOrganizationID,
				}))
			}
		}
	}

	session, err := getOrCreateDemeterAudioTransportSession(req.UploadID, claims.OrgID, claims.UserID, route, routeMode, req)
	if err != nil {
		var ownershipErr *demeterAudioTransportOwnershipError
		if errors.As(err, &ownershipErr) {
			logDemeterOwnershipStageCtx(logCtx, route, seq, "transport_session_ownership_error", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
				"operation_id":    req.UploadID,
				"upload_id":       req.UploadID,
				"status_code":     fiber.StatusBadRequest,
				"reason":          ownershipErr.Reason,
				"source":          "transport_session",
				"request_user_id": claims.UserID,
				"request_org_id":  claims.OrgID,
				"stored_user_id":  ownershipErr.StoredUserID,
				"stored_org_id":   ownershipErr.StoredOrganizationID,
			}))
		}
		logDemeterRelayIssueCtx(logCtx, route, fiber.StatusBadRequest, err.Error())
		logDemeterAudioStageCtx(logCtx, route, seq, "request_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"result":            "transport_session_error",
			"status_code":       fiber.StatusBadRequest,
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
			"operation_id":      req.UploadID,
			"message":           err.Error(),
		}))
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error:   err.Error(),
			Code:    "transport_session_error",
			TraceID: requestTraceID(c),
			Path:    route,
		})
	}

	form, err := c.MultipartForm()
	if err != nil {
		logDemeterRelayIssueCtx(logCtx, route, fiber.StatusBadRequest, err.Error())
		logDemeterAudioStageCtx(logCtx, route, seq, "request_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"result":            "invalid_multipart",
			"status_code":       fiber.StatusBadRequest,
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
			"operation_id":      req.UploadID,
			"message":           err.Error(),
		}))
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error:   "invalid multipart form",
			Code:    "invalid_multipart",
			TraceID: requestTraceID(c),
			Path:    route,
		})
	}
	fileHeaders := form.File["file"]
	if len(fileHeaders) == 0 {
		logDemeterRelayIssueCtx(logCtx, route, fiber.StatusBadRequest, "multipart file part is missing")
		logDemeterAudioStageCtx(logCtx, route, seq, "request_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"result":            "invalid_multipart",
			"status_code":       fiber.StatusBadRequest,
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
			"operation_id":      req.UploadID,
			"message":           "multipart file part is missing",
		}))
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error:   "multipart file part is missing",
			Code:    "invalid_multipart",
			TraceID: requestTraceID(c),
			Path:    route,
		})
	}
	storedBytes, err := storeDemeterAudioTransportSlice(session, req, fileHeaders[0])
	if err != nil {
		var validationErr *demeterAudioValidationError
		if errors.As(err, &validationErr) {
			return a.demeterAudioValidationFailure(c, route, seq, startedAt, requestBytes, contentType, routeMode, audioDurationSec, audioDurationProvided, validationErr)
		}
		logDemeterRelayIssueCtx(logCtx, route, fiber.StatusBadRequest, err.Error())
		logDemeterAudioStageCtx(logCtx, route, seq, "request_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"result":            "transport_store_error",
			"status_code":       fiber.StatusBadRequest,
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
			"operation_id":      req.UploadID,
			"message":           err.Error(),
		}))
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error:   err.Error(),
			Code:    "transport_store_error",
			TraceID: requestTraceID(c),
			Path:    route,
		})
	}

	logDemeterAudioStageCtx(logCtx, route, seq, "transport_slice_received", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
		"operation_id":      req.UploadID,
		"slice_index":       req.SliceIndex,
		"slice_count":       req.SliceCount,
		"slice_final":       req.Final,
		"slice_bytes":       storedBytes,
		"file_name":         req.FileName,
		"mime_type":         req.MimeType,
		"model":             req.Model,
		"diarize":           req.Diarize,
		"total_duration_ms": time.Since(startedAt).Milliseconds(),
		"request_bytes":     requestBytes,
	}))
	logDemeterAudioStageCtx(logCtx, route, seq, "transport_slice_stored", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
		"operation_id":      req.UploadID,
		"slice_index":       req.SliceIndex,
		"slice_count":       req.SliceCount,
		"slice_final":       req.Final,
		"slice_bytes":       storedBytes,
		"transport_bytes":   session.totalBytes,
		"total_duration_ms": time.Since(startedAt).Milliseconds(),
		"request_bytes":     requestBytes,
	}))

	if !req.Final {
		logDemeterAudioStageCtx(logCtx, route, seq, "sequence_end", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"result":            "slice_accepted",
			"status_code":       fiber.StatusNoContent,
			"operation_id":      req.UploadID,
			"slice_index":       req.SliceIndex,
			"slice_count":       req.SliceCount,
			"slice_final":       req.Final,
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
		}))
		return c.SendStatus(fiber.StatusNoContent)
	}

	if session != nil {
		session.mu.Lock()
		session.finalizing = true
		session.mu.Unlock()
	}

	logDemeterAudioStageCtx(logCtx, route, seq, "transport_finalizing", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
		"operation_id":      req.UploadID,
		"slice_index":       req.SliceIndex,
		"slice_count":       req.SliceCount,
		"slice_final":       req.Final,
		"transport_bytes":   session.totalBytes,
		"total_duration_ms": time.Since(startedAt).Milliseconds(),
		"request_bytes":     requestBytes,
	}))

	if err := a.startDemeterAudioTransportTranscriptionOperation(c, logCtx, route, seq, routeMode, audioDurationSec, audioDurationProvided, requestBytes, session, startedAt); err != nil {
		if session != nil {
			session.mu.Lock()
			session.finalizing = false
			session.mu.Unlock()
		}
		session.cleanup()
		demeterAudioTransportSessions.Delete(req.UploadID)
		return err
	}

	if session != nil {
		session.mu.Lock()
		session.finalized = true
		session.mu.Unlock()
	}

	return nil
}
