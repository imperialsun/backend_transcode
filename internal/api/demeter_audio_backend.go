package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

const (
	demeterBackendDefaultChunkDurationSec = 900
	demeterBackendDefaultChunkOverlapSec  = 0
	demeterBackendChunkMinDurationSec     = 5
	demeterAudioPipelineSampleRate        = 16000
	demeterAudioPipelineChannels          = 1
	demeterAudioPipelineSampleFormat      = "pcm_s16le"
	demeterAudioChunkFileExt              = ".wav"
	demeterBackendFfmpegBinary            = "ffmpeg"
	demeterBackendFfprobeBinary           = "ffprobe"
)

type demeterBackendAudioChunkSettings struct {
	ChunkDurationSec int
	OverlapSec       int
}

type demeterBackendAudioUpload struct {
	FileName      string
	MimeType      string
	SizeBytes     int64
	Model         string
	Diarize       bool
	SourcePath    string
	SourceDir     string
	SourceFormat  string
	ProbedFormat  string
	DurationSec   float64
	ChunkSettings demeterBackendChunkingConfig
	cleanup       func()
}

type demeterBackendChunkingConfig struct {
	RequestedDurationSec int
	EffectiveDurationSec int
	EffectiveOverlapSec  int
	ModelMaxDurationSec  int
	DurationWasCapped    bool
}

type demeterBackendChunkPlan struct {
	Index    int
	StartSec float64
	EndSec   float64
	Duration float64
	ChunkID  string
	FileName string
	MimeType string
}

type demeterMistralWord struct {
	Word       string   `json:"word"`
	Start      float64  `json:"start"`
	End        float64  `json:"end"`
	Confidence *float64 `json:"confidence,omitempty"`
}

type demeterMistralSegment struct {
	Text       string               `json:"text"`
	Start      *float64             `json:"start,omitempty"`
	End        *float64             `json:"end,omitempty"`
	Confidence *float64             `json:"confidence,omitempty"`
	Speaker    string               `json:"speaker,omitempty"`
	SpeakerID  string               `json:"speaker_id,omitempty"`
	Words      []demeterMistralWord `json:"words,omitempty"`
}

type demeterMistralTranscriptionResponse struct {
	Text     string                  `json:"text,omitempty"`
	Language string                  `json:"language,omitempty"`
	Duration *float64                `json:"duration,omitempty"`
	Segments []demeterMistralSegment `json:"segments,omitempty"`
	Chunks   []demeterMistralSegment `json:"chunks,omitempty"`
	Words    []demeterMistralWord    `json:"words,omitempty"`
}

type demeterBackendTranscriptionSegment struct {
	Index      int                  `json:"index"`
	Start      float64              `json:"start"`
	End        float64              `json:"end"`
	Text       string               `json:"text"`
	Speaker    string               `json:"speaker,omitempty"`
	SpeakerID  string               `json:"speaker_id,omitempty"`
	ChunkID    string               `json:"chunkId,omitempty"`
	Confidence *float64             `json:"confidence,omitempty"`
	Words      []demeterMistralWord `json:"words,omitempty"`
}

type demeterBackendChunkMetadata struct {
	ChunkID          string  `json:"chunkId"`
	Index            int     `json:"index"`
	StartSec         float64 `json:"startSec"`
	EndSec           float64 `json:"endSec"`
	DurationSec      float64 `json:"durationSec"`
	FileName         string  `json:"fileName,omitempty"`
	MimeType         string  `json:"mimeType,omitempty"`
	SourceFormat     string  `json:"sourceFormat,omitempty"`
	NormalizedFormat string  `json:"normalizedFormat,omitempty"`
	SegmentCount     int     `json:"segmentCount,omitempty"`
	Text             string  `json:"text,omitempty"`
}

type demeterBackendTranscriptionResponse struct {
	Text     string                               `json:"text,omitempty"`
	Language string                               `json:"language,omitempty"`
	Duration float64                              `json:"duration,omitempty"`
	Segments []demeterBackendTranscriptionSegment `json:"segments,omitempty"`
	Chunks   []demeterBackendChunkMetadata        `json:"chunks,omitempty"`
	Words    []demeterMistralWord                 `json:"words,omitempty"`
}

func (a *App) demeterAudioTranscriptionsBackendDirect(
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
	if !a.MistralClient.IsConfigured() {
		logDemeterRelayIssue(c, route, fiber.StatusServiceUnavailable, "mistral client is not configured")
		logDemeterAudioStage(c, route, seq, "sequence_end", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"result":            "mistral_not_configured",
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
		}))
		return c.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{Error: "mistral is not configured"})
	}

	claims := MustClaims(c)
	if claims == nil {
		logDemeterRelayIssue(c, route, fiber.StatusUnauthorized, "missing claims")
		logDemeterAudioStage(c, route, seq, "sequence_end", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"result":            "unauthorized",
			"status_code":       fiber.StatusUnauthorized,
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
		}))
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized", Code: "unauthorized", TraceID: requestTraceID(c), Path: route})
	}

	upload, err := a.loadDemeterBackendAudioUpload(c)
	if err != nil {
		var validationErr *demeterAudioValidationError
		if errors.As(err, &validationErr) {
			return a.demeterAudioValidationFailure(c, route, seq, startedAt, requestBytes, contentType, routeMode, audioDurationSec, audioDurationProvided, validationErr)
		}
		logDemeterRelayIssue(c, route, fiber.StatusBadRequest, err.Error())
		logDemeterAudioStage(c, route, seq, "request_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"result":            "invalid_multipart",
			"status_code":       fiber.StatusBadRequest,
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
			"content_type":      contentType,
			"message":           err.Error(),
		}))
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error:   "invalid multipart form",
			Code:    "invalid_multipart",
			TraceID: requestTraceID(c),
			Path:    route,
		})
	}
	defer upload.cleanup()

	logDemeterAudioStage(c, route, seq, "upload_ready", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
		"file_name":          upload.FileName,
		"file_size_bytes":    upload.SizeBytes,
		"mime_type":          upload.MimeType,
		"source_format":      upload.SourceFormat,
		"probed_format":      upload.ProbedFormat,
		"audio_duration_sec": upload.DurationSec,
		"model":              upload.Model,
		"diarize":            upload.Diarize,
	}))

	settings, err := a.loadDemeterBackendAudioChunkSettings(requestContext(c), claims.UserID)
	if err != nil {
		logDemeterAudioStage(c, route, seq, "request_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"result":            "settings_load_error",
			"status_code":       fiber.StatusInternalServerError,
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
			"message":           err.Error(),
		}))
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user settings"})
	}

	chunking := resolveDemeterBackendChunkingConfig(settings, upload.Model)
	chunkPlans := buildDemeterBackendChunkPlans(upload.DurationSec, chunking.EffectiveDurationSec, chunking.EffectiveOverlapSec)
	if len(chunkPlans) == 0 {
		validationErr := &demeterAudioValidationError{
			code:    "invalid_audio_file",
			message: "fichier audio illisible",
			file: demeterAudioFileInfo{
				FileName:  upload.FileName,
				MimeType:  upload.MimeType,
				SizeBytes: upload.SizeBytes,
			},
		}
		return a.demeterAudioValidationFailure(c, route, seq, startedAt, requestBytes, contentType, routeMode, audioDurationSec, audioDurationProvided, validationErr)
	}

	logDemeterAudioStage(c, route, seq, "chunk_plan_ready", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
		"file_name":              upload.FileName,
		"file_size_bytes":        upload.SizeBytes,
		"mime_type":              upload.MimeType,
		"source_format":          upload.SourceFormat,
		"probed_format":          upload.ProbedFormat,
		"duration_sec":           upload.DurationSec,
		"chunk_count":            len(chunkPlans),
		"requested_duration_sec": chunking.RequestedDurationSec,
		"effective_duration_sec": chunking.EffectiveDurationSec,
		"effective_overlap_sec":  chunking.EffectiveOverlapSec,
		"model_max_duration_sec": chunking.ModelMaxDurationSec,
		"duration_was_capped":    chunking.DurationWasCapped,
		"normalized_format":      "audio/wav",
	}))

	response, statusCode, upstreamBody, upstreamDurationMs, err := a.demeterBackendTranscribeChunks(
		c,
		route,
		seq,
		routeMode,
		audioDurationSec,
		audioDurationProvided,
		upload,
		chunkPlans,
	)
	if err != nil {
		var validationErr *demeterAudioValidationError
		if errors.As(err, &validationErr) {
			return a.demeterAudioValidationFailure(c, route, seq, startedAt, requestBytes, contentType, routeMode, audioDurationSec, audioDurationProvided, validationErr)
		}
		if statusCode == 0 {
			statusCode = fiber.StatusBadGateway
		}
		logDemeterRelayIssue(c, route, statusCode, err.Error())
		logDemeterAudioStage(c, route, seq, "sequence_end", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"result":               "upstream_error",
			"upstream":             demeterAudioTranscriptionsUpstreamPath,
			"upstream_status":      statusCode,
			"upstream_duration_ms": upstreamDurationMs,
			"total_duration_ms":    time.Since(startedAt).Milliseconds(),
			"request_bytes":        requestBytes,
			"message":              err.Error(),
		}))
		if len(upstreamBody) > 0 {
			c.Status(statusCode)
			c.Type("json")
			return c.Send(upstreamBody)
		}
		return c.Status(statusCode).JSON(ErrorResponse{Error: "failed to reach mistral"})
	}

	responseBytes, err := json.Marshal(response)
	if err != nil {
		logDemeterRelayIssue(c, route, fiber.StatusInternalServerError, err.Error())
		logDemeterAudioStage(c, route, seq, "sequence_end", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"result":            "response_marshal_error",
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
			"message":           err.Error(),
		}))
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to marshal transcription response"})
	}

	logDemeterAudioStage(c, route, seq, "response_ready", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
		"file_name":              upload.FileName,
		"file_size_bytes":        upload.SizeBytes,
		"mime_type":              upload.MimeType,
		"source_format":          upload.SourceFormat,
		"probed_format":          upload.ProbedFormat,
		"duration_sec":           upload.DurationSec,
		"chunk_count":            len(chunkPlans),
		"segments_count":         len(response.Segments),
		"upstream_duration_ms":   upstreamDurationMs,
		"response_bytes":         len(responseBytes),
		"requested_duration_sec": chunking.RequestedDurationSec,
		"effective_duration_sec": chunking.EffectiveDurationSec,
		"effective_overlap_sec":  chunking.EffectiveOverlapSec,
		"model_max_duration_sec": chunking.ModelMaxDurationSec,
		"duration_was_capped":    chunking.DurationWasCapped,
	}))

	logDemeterAudioStage(c, route, seq, "sequence_end", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
		"result":               "ok",
		"upstream":             demeterAudioTranscriptionsUpstreamPath,
		"upstream_status":      fiber.StatusOK,
		"upstream_duration_ms": upstreamDurationMs,
		"total_duration_ms":    time.Since(startedAt).Milliseconds(),
		"request_bytes":        requestBytes,
		"response_bytes":       len(responseBytes),
		"chunk_count":          len(chunkPlans),
		"segments_count":       len(response.Segments),
	}))

	c.Status(fiber.StatusOK)
	c.Type("json")
	return c.Send(responseBytes)
}

func (a *App) demeterBackendTranscribeChunks(
	c *fiber.Ctx,
	route string,
	seq uint64,
	routeMode string,
	audioDurationSec float64,
	audioDurationProvided bool,
	upload *demeterBackendAudioUpload,
	chunkPlans []demeterBackendChunkPlan,
) (*demeterBackendTranscriptionResponse, int, []byte, int64, error) {
	ctx := requestContext(c)
	finalResponse := &demeterBackendTranscriptionResponse{}
	nextIndex := 0
	var totalUpstreamDurationMs int64
	flatSegments := make([]demeterBackendTranscriptionSegment, 0)
	chunkMetadata := make([]demeterBackendChunkMetadata, 0, len(chunkPlans))
	var combinedTextParts []string

	for _, plan := range chunkPlans {
		if plan.Duration <= 0 {
			continue
		}

		chunkResult, err := a.transcribeDemeterBackendChunk(ctx, c, route, seq, routeMode, audioDurationSec, audioDurationProvided, upload, plan)
		totalUpstreamDurationMs += chunkResult.upstreamDurationMs
		if err != nil {
			return nil, chunkResult.statusCode, chunkResult.responseBody, totalUpstreamDurationMs, err
		}

		parsed, chunkText := flattenDemeterChunkResponse(chunkResult.response, plan, nextIndex)
		nextIndex += len(parsed)
		flatSegments = append(flatSegments, parsed...)
		chunkMetadata = append(chunkMetadata, demeterBackendChunkMetadata{
			ChunkID:          plan.ChunkID,
			Index:            plan.Index,
			StartSec:         plan.StartSec,
			EndSec:           plan.EndSec,
			DurationSec:      plan.Duration,
			FileName:         plan.FileName,
			MimeType:         plan.MimeType,
			SourceFormat:     upload.SourceFormat,
			NormalizedFormat: "audio/wav",
			SegmentCount:     len(parsed),
			Text:             chunkText,
		})
		if chunkText != "" {
			combinedTextParts = append(combinedTextParts, chunkText)
		}
	}

	finalResponse.Text = strings.TrimSpace(strings.Join(combinedTextParts, "\n"))
	finalResponse.Duration = upload.DurationSec
	finalResponse.Language = ""
	finalResponse.Segments = flatSegments
	finalResponse.Chunks = chunkMetadata
	return finalResponse, fiber.StatusOK, nil, totalUpstreamDurationMs, nil
}

type demeterChunkTranscriptionResult struct {
	statusCode         int
	responseBody       []byte
	upstreamDurationMs int64
	response           demeterMistralTranscriptionResponse
}

func (a *App) transcribeDemeterBackendChunk(
	ctx context.Context,
	c *fiber.Ctx,
	route string,
	seq uint64,
	routeMode string,
	audioDurationSec float64,
	audioDurationProvided bool,
	upload *demeterBackendAudioUpload,
	plan demeterBackendChunkPlan,
) (demeterChunkTranscriptionResult, error) {
	chunkDir, err := os.MkdirTemp("", "demeter-chunk-*")
	if err != nil {
		return demeterChunkTranscriptionResult{statusCode: fiber.StatusInternalServerError}, err
	}
	defer func() {
		_ = os.RemoveAll(chunkDir)
	}()

	chunkPath := filepath.Join(chunkDir, fmt.Sprintf("chunk_%03d%s", plan.Index+1, demeterAudioChunkFileExt))
	if err := transcodeDemeterAudioChunk(ctx, upload.SourcePath, chunkPath, plan.StartSec, plan.Duration); err != nil {
		if errors.Is(err, errDemeterAudioChunkEmpty) {
			return demeterChunkTranscriptionResult{statusCode: fiber.StatusBadRequest}, &demeterAudioValidationError{
				code:    "invalid_audio_file",
				message: "fichier audio illisible",
				file: demeterAudioFileInfo{
					FileName:  upload.FileName,
					MimeType:  upload.MimeType,
					SizeBytes: upload.SizeBytes,
				},
			}
		}
		var validationErr *demeterAudioValidationError
		if errors.As(err, &validationErr) {
			validationErr.file = demeterAudioFileInfo{
				FileName:  upload.FileName,
				MimeType:  upload.MimeType,
				SizeBytes: upload.SizeBytes,
			}
			return demeterChunkTranscriptionResult{statusCode: fiber.StatusServiceUnavailable}, validationErr
		}
		logDemeterAudioStage(c, route, seq, "chunk_transcode_error", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"chunk_index":     plan.Index,
			"chunk_id":        plan.ChunkID,
			"start_sec":       plan.StartSec,
			"end_sec":         plan.EndSec,
			"duration_sec":    plan.Duration,
			"file_name":       upload.FileName,
			"mime_type":       upload.MimeType,
			"source_format":   upload.SourceFormat,
			"normalized_file": chunkPath,
			"message":         err.Error(),
		}))
		return demeterChunkTranscriptionResult{statusCode: fiber.StatusBadGateway}, err
	}

	chunkBytes, err := os.ReadFile(chunkPath)
	if err != nil {
		return demeterChunkTranscriptionResult{statusCode: fiber.StatusBadGateway}, err
	}
	if len(chunkBytes) == 0 {
		return demeterChunkTranscriptionResult{statusCode: fiber.StatusBadRequest}, &demeterAudioValidationError{
			code:    "invalid_audio_file",
			message: "fichier audio illisible",
			file: demeterAudioFileInfo{
				FileName:  upload.FileName,
				MimeType:  upload.MimeType,
				SizeBytes: upload.SizeBytes,
			},
		}
	}

	logDemeterAudioStage(c, route, seq, "upstream_send_start", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
		"chunk_index":        plan.Index,
		"chunk_id":           plan.ChunkID,
		"chunk_start_sec":    plan.StartSec,
		"chunk_end_sec":      plan.EndSec,
		"chunk_duration_sec": plan.Duration,
		"file_name":          upload.FileName,
		"chunk_bytes":        len(chunkBytes),
		"mime_type":          "audio/wav",
		"model":              upload.Model,
		"diarize":            upload.Diarize,
		"normalized_format":  "audio/wav",
	}))

	body, contentType, err := buildDemeterAudioMultipart(chunkBytes, fmt.Sprintf("chunk_%03d%s", plan.Index+1, demeterAudioChunkFileExt), upload.Model, upload.Diarize)
	if err != nil {
		return demeterChunkTranscriptionResult{statusCode: fiber.StatusInternalServerError}, err
	}

	result, err := a.demeterAudioTranscriptionWithRetry(c, route, seq, routeMode, audioDurationSec, audioDurationProvided, body, contentType, len(body))
	if err != nil {
		return demeterChunkTranscriptionResult{statusCode: fiber.StatusBadGateway}, err
	}
	if result.statusCode == fiber.StatusUnprocessableEntity && upload.Diarize {
		logDemeterAudioStage(c, route, seq, "upstream_retry", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"chunk_index":     plan.Index,
			"chunk_id":        plan.ChunkID,
			"reason":          "diarization_validation",
			"attempt":         1,
			"next_attempt":    2,
			"max_attempts":    demeterAudioTranscriptionMaxAttempts,
			"upstream_status": fiber.StatusUnprocessableEntity,
		}))
		body, contentType, err = buildDemeterAudioMultipart(chunkBytes, fmt.Sprintf("chunk_%03d%s", plan.Index+1, demeterAudioChunkFileExt), upload.Model, false)
		if err != nil {
			return demeterChunkTranscriptionResult{statusCode: fiber.StatusInternalServerError}, err
		}
		result, err = a.demeterAudioTranscriptionWithRetry(c, route, seq, routeMode, audioDurationSec, audioDurationProvided, body, contentType, len(body))
		if err != nil {
			return demeterChunkTranscriptionResult{statusCode: fiber.StatusBadGateway}, err
		}
	}

	if result.statusCode >= fiber.StatusBadRequest {
		return demeterChunkTranscriptionResult{
			statusCode:         result.statusCode,
			responseBody:       result.responseBody,
			upstreamDurationMs: result.upstreamDurationMs,
		}, fmt.Errorf("upstream returned status %d", result.statusCode)
	}

	var parsed demeterMistralTranscriptionResponse
	if err := json.Unmarshal(result.responseBody, &parsed); err != nil {
		return demeterChunkTranscriptionResult{
			statusCode:         fiber.StatusBadGateway,
			responseBody:       result.responseBody,
			upstreamDurationMs: result.upstreamDurationMs,
		}, fmt.Errorf("failed to parse mistral chunk response: %w", err)
	}

	logDemeterAudioStage(c, route, seq, "upstream_response_received", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
		"chunk_index":          plan.Index,
		"chunk_id":             plan.ChunkID,
		"chunk_start_sec":      plan.StartSec,
		"chunk_end_sec":        plan.EndSec,
		"chunk_duration_sec":   plan.Duration,
		"upstream_status":      result.statusCode,
		"upstream_duration_ms": result.upstreamDurationMs,
		"request_bytes":        len(body),
		"response_bytes":       len(result.responseBody),
		"attempts":             result.attempts,
	}))

	return demeterChunkTranscriptionResult{
		statusCode:         result.statusCode,
		responseBody:       result.responseBody,
		upstreamDurationMs: result.upstreamDurationMs,
		response:           parsed,
	}, nil
}

func buildDemeterAudioMultipart(fileBytes []byte, fileName, model string, diarize bool) ([]byte, string, error) {
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)

	if err := writer.WriteField("model", strings.TrimSpace(model)); err != nil {
		return nil, "", err
	}
	if err := writer.WriteField("diarize", strconv.FormatBool(diarize)); err != nil {
		return nil, "", err
	}
	if diarize {
		if err := writer.WriteField("timestamp_granularities", "segment"); err != nil {
			return nil, "", err
		}
	}
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(fileBytes); err != nil {
		return nil, "", err
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return buffer.Bytes(), writer.FormDataContentType(), nil
}

func flattenDemeterChunkResponse(resp demeterMistralTranscriptionResponse, plan demeterBackendChunkPlan, startIndex int) ([]demeterBackendTranscriptionSegment, string) {
	rawSegments := resp.Segments
	if len(rawSegments) == 0 {
		rawSegments = resp.Chunks
	}
	segments := make([]demeterBackendTranscriptionSegment, 0, len(rawSegments))
	index := startIndex
	for _, item := range rawSegments {
		text := strings.TrimSpace(item.Text)
		if text == "" {
			continue
		}
		rawStart := 0.0
		rawEnd := math.Max(0, plan.Duration)
		if item.Start != nil && !math.IsNaN(*item.Start) && !math.IsInf(*item.Start, 0) {
			rawStart = math.Max(0, *item.Start)
		}
		if item.End != nil && !math.IsNaN(*item.End) && !math.IsInf(*item.End, 0) {
			rawEnd = math.Max(rawStart, *item.End)
		}
		segment := demeterBackendTranscriptionSegment{
			Index:      index,
			Start:      plan.StartSec + rawStart,
			End:        plan.StartSec + math.Max(rawStart, rawEnd),
			Text:       text,
			Speaker:    strings.TrimSpace(item.Speaker),
			SpeakerID:  strings.TrimSpace(item.SpeakerID),
			ChunkID:    plan.ChunkID,
			Confidence: item.Confidence,
			Words:      offsetDemeterWords(item.Words, plan.StartSec),
		}
		if segment.Speaker == "" && segment.SpeakerID != "" {
			segment.Speaker = segment.SpeakerID
		}
		segments = append(segments, segment)
		index++
	}

	if len(segments) == 0 {
		fallbackText := strings.TrimSpace(resp.Text)
		if fallbackText != "" {
			segments = append(segments, demeterBackendTranscriptionSegment{
				Index:   index,
				Start:   plan.StartSec,
				End:     plan.EndSec,
				Text:    fallbackText,
				ChunkID: plan.ChunkID,
			})
		}
	}

	chunkText := strings.TrimSpace(resp.Text)
	if chunkText == "" && len(segments) > 0 {
		textParts := make([]string, 0, len(segments))
		for _, segment := range segments {
			if text := strings.TrimSpace(segment.Text); text != "" {
				textParts = append(textParts, text)
			}
		}
		chunkText = strings.TrimSpace(strings.Join(textParts, "\n"))
	}

	return segments, chunkText
}

func offsetDemeterWords(words []demeterMistralWord, offsetSec float64) []demeterMistralWord {
	if len(words) == 0 {
		return nil
	}
	out := make([]demeterMistralWord, 0, len(words))
	for _, word := range words {
		if strings.TrimSpace(word.Word) == "" {
			continue
		}
		next := word
		if !math.IsNaN(next.Start) && !math.IsInf(next.Start, 0) {
			next.Start = math.Max(0, offsetSec+next.Start)
		}
		if !math.IsNaN(next.End) && !math.IsInf(next.End, 0) {
			next.End = math.Max(0, offsetSec+next.End)
		}
		out = append(out, next)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (a *App) loadDemeterBackendAudioUpload(c *fiber.Ctx) (*demeterBackendAudioUpload, error) {
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
	fileName := normalizeDemeterAudioFileName(fileHeader.Filename)
	mimeType := normalizeDemeterAudioMimeType(fileHeader.Header.Get("Content-Type"), fileHeader.Filename)
	sizeBytes := fileHeader.Size
	if sizeBytes <= 0 {
		return nil, &demeterAudioValidationError{
			code:    "empty_audio_file",
			message: "fichier audio vide",
			file: demeterAudioFileInfo{
				FileName:  fileName,
				MimeType:  mimeType,
				SizeBytes: sizeBytes,
			},
		}
	}

	tempDir, err := os.MkdirTemp("", "demeter-audio-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create audio temp dir: %w", err)
	}

	kind := resolveDemeterAudioKind(fileName, mimeType)
	sourceFormat := kind
	if sourceFormat == "" {
		sourceFormat = "unknown"
	}
	sourceExt := demeterAudioChunkFileExt
	switch kind {
	case "mp3":
		sourceExt = ".mp3"
	case "mp4":
		sourceExt = ".m4a"
	case "aac":
		sourceExt = ".aac"
	case "ogg":
		sourceExt = ".ogg"
	case "webm":
		sourceExt = ".webm"
	case "wav":
		sourceExt = ".wav"
	}
	sourcePath := filepath.Join(tempDir, "source"+sourceExt)

	input, err := fileHeader.Open()
	if err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, fmt.Errorf("failed to open uploaded audio: %w", err)
	}
	defer func() {
		_ = input.Close()
	}()

	sourceFile, err := os.Create(sourcePath)
	if err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, fmt.Errorf("failed to create uploaded audio temp file: %w", err)
	}

	sizeCopied, copyErr := io.Copy(sourceFile, input)
	closeErr := sourceFile.Close()
	if copyErr != nil {
		_ = os.RemoveAll(tempDir)
		return nil, fmt.Errorf("failed to persist uploaded audio: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.RemoveAll(tempDir)
		return nil, fmt.Errorf("failed to close uploaded audio temp file: %w", closeErr)
	}
	if sizeCopied <= 0 {
		_ = os.RemoveAll(tempDir)
		return nil, &demeterAudioValidationError{
			code:    "empty_audio_file",
			message: "fichier audio vide",
			file: demeterAudioFileInfo{
				FileName:  fileName,
				MimeType:  mimeType,
				SizeBytes: sizeCopied,
			},
		}
	}

	model := defaultDemeterAudioTranscriptionModelID
	if values := form.Value["model"]; len(values) > 0 && strings.TrimSpace(values[0]) != "" {
		model = strings.TrimSpace(values[0])
	}
	diarize := false
	if values := form.Value["diarize"]; len(values) > 0 {
		if parsed, err := strconv.ParseBool(strings.TrimSpace(values[0])); err == nil {
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

	probe, err := probeDemeterAudioFile(c, sourcePath)
	if err != nil {
		var validationErr *demeterAudioValidationError
		if errors.As(err, &validationErr) {
			validationErr.file = demeterAudioFileInfo{
				FileName:  fileName,
				MimeType:  mimeType,
				SizeBytes: sizeCopied,
			}
			_ = os.RemoveAll(tempDir)
			return nil, validationErr
		}
		_ = os.RemoveAll(tempDir)
		return nil, err
	}

	return &demeterBackendAudioUpload{
		FileName:     fileName,
		MimeType:     mimeType,
		SizeBytes:    sizeCopied,
		Model:        model,
		Diarize:      diarize,
		SourcePath:   sourcePath,
		SourceDir:    tempDir,
		SourceFormat: sourceFormat,
		ProbedFormat: func() string {
			if strings.TrimSpace(probe.FormatName) == "" {
				return "unknown"
			}
			return strings.TrimSpace(probe.FormatName)
		}(),
		DurationSec:   probe.DurationSec,
		ChunkSettings: demeterBackendChunkingConfig{},
		cleanup: func() {
			_ = os.RemoveAll(tempDir)
		},
	}, nil
}

type demeterAudioProbeResult struct {
	DurationSec float64
	FormatName  string
	CodecName   string
	SampleRate  int
	Channels    int
}

func probeDemeterAudioFile(c *fiber.Ctx, inputPath string) (*demeterAudioProbeResult, error) {
	ffprobePath, err := exec.LookPath(demeterBackendFfprobeBinary)
	if err != nil {
		return nil, &demeterAudioValidationError{
			code:    "audio_pipeline_unavailable",
			message: "ffprobe is not installed",
			file:    demeterAudioFileInfo{},
		}
	}

	args := []string{
		"-v", "error",
		"-show_entries", "format=duration,format_name:stream=index,codec_type,codec_name,sample_rate,channels,duration",
		"-of", "json",
		inputPath,
	}
	cmd := exec.CommandContext(requestContext(c), ffprobePath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		return nil, &demeterAudioValidationError{
			code:    "invalid_audio_file",
			message: "fichier audio illisible",
			file:    demeterAudioFileInfo{},
		}
	}

	type probeStream struct {
		Index      int    `json:"index"`
		CodecType  string `json:"codec_type"`
		CodecName  string `json:"codec_name"`
		SampleRate string `json:"sample_rate"`
		Channels   int    `json:"channels"`
		Duration   string `json:"duration"`
	}
	type probeFormat struct {
		Duration   string `json:"duration"`
		FormatName string `json:"format_name"`
	}
	type probePayload struct {
		Format  probeFormat   `json:"format"`
		Streams []probeStream `json:"streams"`
	}

	var payload probePayload
	if err := json.Unmarshal(stdout, &payload); err != nil {
		return nil, &demeterAudioValidationError{
			code:    "invalid_audio_file",
			message: "fichier audio illisible",
			file:    demeterAudioFileInfo{},
		}
	}

	durationSec := 0.0
	if parsed, err := strconv.ParseFloat(strings.TrimSpace(payload.Format.Duration), 64); err == nil && parsed > 0 && !math.IsNaN(parsed) && !math.IsInf(parsed, 0) {
		durationSec = parsed
	}
	if durationSec <= 0 {
		for _, stream := range payload.Streams {
			if strings.TrimSpace(stream.CodecType) != "audio" {
				continue
			}
			if parsed, err := strconv.ParseFloat(strings.TrimSpace(stream.Duration), 64); err == nil && parsed > 0 && !math.IsNaN(parsed) && !math.IsInf(parsed, 0) {
				durationSec = parsed
				break
			}
		}
	}

	audioStreamFound := false
	for _, stream := range payload.Streams {
		if strings.TrimSpace(stream.CodecType) == "audio" {
			audioStreamFound = true
			break
		}
	}
	if !audioStreamFound || durationSec <= 0 {
		return nil, &demeterAudioValidationError{
			code:    "invalid_audio_file",
			message: "fichier audio illisible",
			file:    demeterAudioFileInfo{},
		}
	}

	probe := &demeterAudioProbeResult{
		DurationSec: durationSec,
		FormatName:  strings.TrimSpace(payload.Format.FormatName),
	}
	for _, stream := range payload.Streams {
		if strings.TrimSpace(stream.CodecType) != "audio" {
			continue
		}
		probe.CodecName = strings.TrimSpace(stream.CodecName)
		if parsed, err := strconv.Atoi(strings.TrimSpace(stream.SampleRate)); err == nil {
			probe.SampleRate = parsed
		}
		probe.Channels = stream.Channels
		break
	}
	return probe, nil
}

func resolveDemeterBackendChunkingConfig(settings demeterBackendAudioChunkSettings, model string) demeterBackendChunkingConfig {
	modelMax := resolveDemeterBackendModelMaxDurationSec(model)
	requestedDuration := settings.ChunkDurationSec
	if requestedDuration == 0 {
		requestedDuration = modelMax
	}
	requestedDuration = int(math.Round(float64(requestedDuration)))
	if requestedDuration < demeterBackendChunkMinDurationSec {
		requestedDuration = demeterBackendChunkMinDurationSec
	}
	effectiveDuration := requestedDuration
	if effectiveDuration > modelMax {
		effectiveDuration = modelMax
	}
	requestedOverlap := int(math.Round(float64(settings.OverlapSec)))
	if requestedOverlap < 0 {
		requestedOverlap = 0
	}
	effectiveOverlap := requestedOverlap
	if effectiveOverlap > effectiveDuration-1 {
		effectiveOverlap = maxInt(0, effectiveDuration-1)
	}
	return demeterBackendChunkingConfig{
		RequestedDurationSec: requestedDuration,
		EffectiveDurationSec: effectiveDuration,
		EffectiveOverlapSec:  effectiveOverlap,
		ModelMaxDurationSec:  modelMax,
		DurationWasCapped:    effectiveDuration < requestedDuration,
	}
}

func resolveDemeterBackendModelMaxDurationSec(model string) int {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return demeterBackendDefaultChunkDurationSec
	}
	if normalized == "voxtral-mini-latest" || strings.HasPrefix(normalized, "voxtral-mini-") {
		return demeterBackendDefaultChunkDurationSec
	}
	return 30
}

func (a *App) loadDemeterBackendAudioChunkSettings(ctx context.Context, userID string) (demeterBackendAudioChunkSettings, error) {
	settings := demeterBackendAudioChunkSettings{
		ChunkDurationSec: demeterBackendDefaultChunkDurationSec,
		OverlapSec:       demeterBackendDefaultChunkOverlapSec,
	}

	if a == nil || a.Store == nil {
		return settings, nil
	}

	record, err := a.Store.GetUserSettings(ctx, strings.TrimSpace(userID))
	if err != nil {
		return settings, err
	}
	if record == nil || len(record.Settings) == 0 {
		return settings, nil
	}

	var payload map[string]any
	if err := json.Unmarshal(record.Settings, &payload); err != nil {
		return settings, nil
	}

	if value, ok := payload["cloudMistralChunkDurationSec"]; ok {
		if parsed, ok := readDemeterBackendIntSetting(value); ok {
			settings.ChunkDurationSec = parsed
		}
	}
	if value, ok := payload["cloudMistralOverlapSec"]; ok {
		if parsed, ok := readDemeterBackendIntSetting(value); ok {
			settings.OverlapSec = parsed
		}
	}
	return settings, nil
}

func readDemeterBackendIntSetting(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0, false
		}
		return int(math.Round(typed)), true
	case float32:
		converted := float64(typed)
		if math.IsNaN(converted) || math.IsInf(converted, 0) {
			return 0, false
		}
		return int(math.Round(converted)), true
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed), true
		}
		if parsed, err := typed.Float64(); err == nil {
			return int(math.Round(parsed)), true
		}
	case string:
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0) {
			return int(math.Round(parsed)), true
		}
	}
	return 0, false
}

func buildDemeterBackendChunkPlans(durationSec float64, chunkDurationSec, overlapSec int) []demeterBackendChunkPlan {
	if !(durationSec > 0) {
		return nil
	}
	segmentDuration := math.Max(1, float64(chunkDurationSec))
	step := math.Max(1, segmentDuration-float64(overlapSec))
	plans := make([]demeterBackendChunkPlan, 0)
	index := 0
	for start := 0.0; start < durationSec; start += step {
		end := math.Min(start+segmentDuration, durationSec)
		if end <= start {
			break
		}
		plans = append(plans, demeterBackendChunkPlan{
			Index:    index,
			StartSec: start,
			EndSec:   end,
			Duration: end - start,
			ChunkID:  fmt.Sprintf("demeter-backend-%03d", index+1),
			FileName: fmt.Sprintf("chunk_%03d%s", index+1, demeterAudioChunkFileExt),
			MimeType: "audio/wav",
		})
		index++
		if end >= durationSec {
			break
		}
	}
	return plans
}

func transcodeDemeterAudioChunk(ctx context.Context, inputPath, outputPath string, startSec, durationSec float64) error {
	ffmpegPath, err := exec.LookPath(demeterBackendFfmpegBinary)
	if err != nil {
		return &demeterAudioValidationError{
			code:    "audio_pipeline_unavailable",
			message: "ffmpeg is not installed",
			file:    demeterAudioFileInfo{},
		}
	}

	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-nostdin",
		"-y",
		"-ss", formatDemeterFloat(startSec),
		"-t", formatDemeterFloat(durationSec),
		"-i", inputPath,
		"-vn",
		"-ac", strconv.Itoa(demeterAudioPipelineChannels),
		"-ar", strconv.Itoa(demeterAudioPipelineSampleRate),
		"-c:a", demeterAudioPipelineSampleFormat,
		outputPath,
	}

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", errDemeterAudioTranscodeFailed, strings.TrimSpace(stderr.String()))
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		return fmt.Errorf("%w: %s", errDemeterAudioTranscodeFailed, err.Error())
	}
	if info.Size() <= 0 {
		return errDemeterAudioChunkEmpty
	}
	return nil
}

var errDemeterAudioTranscodeFailed = errors.New("audio transcode failed")
var errDemeterAudioChunkEmpty = errors.New("audio chunk empty")

func formatDemeterFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', 3, 64)
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
