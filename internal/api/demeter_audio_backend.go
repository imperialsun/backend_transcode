package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	demeterBackendDefaultChunkDurationSec = 25 * 60
	demeterBackendDefaultChunkOverlapSec  = 0
	demeterBackendChunkMinDurationSec     = 10 * 60
	demeterBackendChunkMaxDurationSec     = 28 * 60
	demeterAudioPipelineSampleRate        = 16000
	demeterAudioPipelineChannels          = 1
	demeterAudioPipelineSampleFormat      = "pcm_s16le"
	demeterAudioChunkFileExt              = ".wav"
	demeterBackendFfmpegBinary            = "ffmpeg"
	demeterBackendFfprobeBinary           = "ffprobe"
)

// demeterBackendAudioChunkSettings carries the user-configured chunking policy
// loaded from backend settings.
type demeterBackendAudioChunkSettings struct {
	ChunkDurationSec int
	OverlapSec       int
}

// demeterBackendAudioUpload describes the reconstructed audio source that will
// be chunked and sent upstream.
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

// demeterBackendChunkingConfig stores the effective chunking policy after
// clamping the user configuration and the model limits.
type demeterBackendChunkingConfig struct {
	RequestedDurationSec int
	EffectiveDurationSec int
	EffectiveOverlapSec  int
	ModelMaxDurationSec  int
	DurationWasCapped    bool
}

// demeterBackendChunkPlan describes one time window in the reconstructed audio.
type demeterBackendChunkPlan struct {
	Index    int
	StartSec float64
	EndSec   float64
	Duration float64
	ChunkID  string
	FileName string
	MimeType string
}

// demeterMistralWord mirrors the word-level upstream payload.
type demeterMistralWord struct {
	Word       string   `json:"word"`
	Start      float64  `json:"start"`
	End        float64  `json:"end"`
	Confidence *float64 `json:"confidence,omitempty"`
}

// demeterMistralSegment mirrors one upstream transcription segment.
type demeterMistralSegment struct {
	Text       string               `json:"text"`
	Start      *float64             `json:"start,omitempty"`
	End        *float64             `json:"end,omitempty"`
	Confidence *float64             `json:"confidence,omitempty"`
	Speaker    string               `json:"speaker,omitempty"`
	SpeakerID  string               `json:"speaker_id,omitempty"`
	Words      []demeterMistralWord `json:"words,omitempty"`
}

// demeterMistralTranscriptionResponse is the raw JSON shape returned by the
// upstream provider.
type demeterMistralTranscriptionResponse struct {
	Text     string                  `json:"text,omitempty"`
	Language string                  `json:"language,omitempty"`
	Duration *float64                `json:"duration,omitempty"`
	Segments []demeterMistralSegment `json:"segments,omitempty"`
	Chunks   []demeterMistralSegment `json:"chunks,omitempty"`
	Words    []demeterMistralWord    `json:"words,omitempty"`
}

// demeterBackendTranscriptionSegment is the normalized segment shape returned
// by the backend pipeline.
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

// demeterBackendChunkMetadata summarizes one processed chunk.
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

// demeterBackendTranscriptionChunk combines metadata and normalized segments
// for one chunk.
type demeterBackendTranscriptionChunk struct {
	ChunkID          string                               `json:"chunkId"`
	Index            int                                  `json:"index"`
	StartSec         float64                              `json:"startSec"`
	EndSec           float64                              `json:"endSec"`
	DurationSec      float64                              `json:"durationSec"`
	FileName         string                               `json:"fileName,omitempty"`
	MimeType         string                               `json:"mimeType,omitempty"`
	SourceFormat     string                               `json:"sourceFormat,omitempty"`
	NormalizedFormat string                               `json:"normalizedFormat,omitempty"`
	SegmentCount     int                                  `json:"segmentCount,omitempty"`
	Text             string                               `json:"text,omitempty"`
	Segments         []demeterBackendTranscriptionSegment `json:"segments,omitempty"`
}

// demeterBackendTranscriptionResponse is the final response returned by the
// backend audio path.
type demeterBackendTranscriptionResponse struct {
	Text     string                             `json:"text,omitempty"`
	Language string                             `json:"language,omitempty"`
	Duration float64                            `json:"duration,omitempty"`
	Chunks   []demeterBackendTranscriptionChunk `json:"chunks,omitempty"`
	Words    []demeterMistralWord               `json:"words,omitempty"`
}

// demeterAudioTranscriptionsBackendDirect handles the backend route family that
// already carries the reconstructed audio request.
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

	if !isDemeterAudioSliceTransport(c) {
		message := "X-Demeter-Transport: slice-v1 is required for /audio/transcriptions/backend"
		logDemeterRelayIssue(c, route, fiber.StatusBadRequest, message)
		logDemeterAudioStage(c, route, seq, "request_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"result":            "invalid_transport",
			"status_code":       fiber.StatusBadRequest,
			"total_duration_ms": time.Since(startedAt).Milliseconds(),
			"request_bytes":     requestBytes,
			"content_type":      contentType,
			"message":           message,
		}))
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error:   message,
			Code:    "invalid_transport",
			TraceID: requestTraceID(c),
			Path:    route,
		})
	}

	return a.demeterAudioTranscriptionsTransportSlice(c, route, seq, startedAt, routeMode, audioDurationSec, audioDurationProvided, requestBytes, contentType)
}

// demeterBackendTranscribeChunks sends each chunk to the upstream provider and
// merges the responses into a single transcript.
func (a *App) demeterBackendTranscribeChunks(
	logCtx demeterAudioLogContext,
	route string,
	seq uint64,
	routeMode string,
	audioDurationSec float64,
	audioDurationProvided bool,
	upload *demeterBackendAudioUpload,
	chunkPlans []demeterBackendChunkPlan,
	onChunkComplete func(completedChunks int, chunkCount int, response *demeterBackendTranscriptionResponse),
) (*demeterBackendTranscriptionResponse, int, []byte, int64, error) {
	ctx := logCtx.ctx
	finalResponse := &demeterBackendTranscriptionResponse{}
	nextIndex := 0
	var totalUpstreamDurationMs int64
	chunkResponses := make([]demeterBackendTranscriptionChunk, 0, len(chunkPlans))
	var combinedTextParts []string
	completedChunks := 0

	for _, plan := range chunkPlans {
		if plan.Duration <= 0 {
			continue
		}
		if len(chunkResponses) > 0 {
			if err := sleepContext(ctx, demeterAudioTranscriptionBaseDelay); err != nil {
				return nil, fiber.StatusBadGateway, nil, totalUpstreamDurationMs, err
			}
		}

		chunkResult, err := a.transcribeDemeterBackendChunk(logCtx, route, seq, routeMode, audioDurationSec, audioDurationProvided, upload, plan)
		totalUpstreamDurationMs += chunkResult.upstreamDurationMs
		if err != nil {
			return nil, chunkResult.statusCode, chunkResult.responseBody, totalUpstreamDurationMs, err
		}

		chunkResponse, chunkText := buildDemeterBackendTranscriptionChunk(chunkResult.response, plan, upload, nextIndex)
		nextIndex += len(chunkResponse.Segments)
		chunkResponses = append(chunkResponses, chunkResponse)
		if chunkText != "" {
			combinedTextParts = append(combinedTextParts, chunkText)
		}
		completedChunks++
		finalResponse.Text = strings.TrimSpace(strings.Join(combinedTextParts, "\n"))
		finalResponse.Duration = upload.DurationSec
		finalResponse.Language = ""
		finalResponse.Chunks = chunkResponses
		if onChunkComplete != nil {
			snapshot := *finalResponse
			onChunkComplete(completedChunks, len(chunkPlans), &snapshot)
		}
	}

	finalResponse.Text = strings.TrimSpace(strings.Join(combinedTextParts, "\n"))
	finalResponse.Duration = upload.DurationSec
	finalResponse.Language = ""
	finalResponse.Chunks = chunkResponses
	return finalResponse, fiber.StatusOK, nil, totalUpstreamDurationMs, nil
}

// demeterChunkTranscriptionResult carries the raw upstream response for one
// chunk together with timing metadata.
type demeterChunkTranscriptionResult struct {
	statusCode         int
	responseBody       []byte
	upstreamDurationMs int64
	response           demeterMistralTranscriptionResponse
}

// transcribeDemeterBackendChunk prepares one chunk, transcodes it if needed,
// and relays it to the upstream provider.
func (a *App) transcribeDemeterBackendChunk(
	logCtx demeterAudioLogContext,
	route string,
	seq uint64,
	routeMode string,
	audioDurationSec float64,
	audioDurationProvided bool,
	upload *demeterBackendAudioUpload,
	plan demeterBackendChunkPlan,
) (demeterChunkTranscriptionResult, error) {
	ctx := logCtx.ctx
	chunkProcessingStartedAt := time.Now()
	var (
		chunkBytes     []byte
		chunkFileName  string
		normalizedMime string
	)
	if routeMode == "relay" {
		var readErr error
		chunkBytes, readErr = os.ReadFile(upload.SourcePath)
		if readErr != nil {
			logDemeterAudioBackendErrorTaskCtx(logCtx, route, seq, "chunk_input_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
				"chunk_index":     plan.Index,
				"chunk_id":        plan.ChunkID,
				"chunk_start_sec": plan.StartSec,
				"chunk_end_sec":   plan.EndSec,
				"duration_ms":     time.Since(chunkProcessingStartedAt).Milliseconds(),
				"message":         readErr.Error(),
				"status_code":     fiber.StatusBadGateway,
			}))
			return demeterChunkTranscriptionResult{statusCode: fiber.StatusBadGateway}, readErr
		}
		if len(chunkBytes) == 0 {
			logDemeterAudioBackendErrorTaskCtx(logCtx, route, seq, "chunk_input_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
				"chunk_index":     plan.Index,
				"chunk_id":        plan.ChunkID,
				"chunk_start_sec": plan.StartSec,
				"chunk_end_sec":   plan.EndSec,
				"duration_ms":     time.Since(chunkProcessingStartedAt).Milliseconds(),
				"message":         "fichier audio illisible",
				"status_code":     fiber.StatusBadRequest,
			}))
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
		chunkFileName = filepath.Base(upload.FileName)
		normalizedMime = strings.TrimSpace(upload.MimeType)
		if normalizedMime == "" {
			normalizedMime = "audio/wav"
		}
	} else {
		chunkDir, err := os.MkdirTemp("", "demeter-chunk-*")
		if err != nil {
			logDemeterAudioBackendErrorTaskCtx(logCtx, route, seq, "chunk_preparation_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
				"chunk_index":     plan.Index,
				"chunk_id":        plan.ChunkID,
				"chunk_start_sec": plan.StartSec,
				"chunk_end_sec":   plan.EndSec,
				"duration_ms":     time.Since(chunkProcessingStartedAt).Milliseconds(),
				"message":         err.Error(),
				"status_code":     fiber.StatusInternalServerError,
			}))
			return demeterChunkTranscriptionResult{statusCode: fiber.StatusInternalServerError}, err
		}
		defer func() {
			_ = os.RemoveAll(chunkDir)
		}()

		chunkPath := filepath.Join(chunkDir, fmt.Sprintf("chunk_%03d%s", plan.Index+1, demeterAudioChunkFileExt))
		transcodeStartedAt := time.Now()
		if err := transcodeDemeterAudioChunk(ctx, upload.SourcePath, chunkPath, plan.StartSec, plan.Duration); err != nil {
			transcodeDurationMs := time.Since(transcodeStartedAt).Milliseconds()
			transcodeFields := map[string]any{
				"chunk_index":        plan.Index,
				"chunk_id":           plan.ChunkID,
				"chunk_start_sec":    plan.StartSec,
				"chunk_end_sec":      plan.EndSec,
				"chunk_duration_sec": plan.Duration,
				"duration_ms":        transcodeDurationMs,
			}
			if errors.Is(err, errDemeterAudioChunkEmpty) {
				transcodeFields["message"] = "fichier audio illisible"
				transcodeFields["status_code"] = fiber.StatusBadRequest
				logDemeterAudioPerformanceTaskCtx(logCtx, route, seq, "ffmpeg_transcode_failed", "transcodage_ffmpeg", transcodeFields)
				logDemeterAudioBackendErrorTaskCtx(logCtx, route, seq, "chunk_transcode_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
					"chunk_index":     plan.Index,
					"chunk_id":        plan.ChunkID,
					"chunk_start_sec": plan.StartSec,
					"chunk_end_sec":   plan.EndSec,
					"duration_ms":     transcodeDurationMs,
					"message":         "fichier audio illisible",
					"status_code":     fiber.StatusBadRequest,
				}))
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
				transcodeFields["message"] = validationErr.Error()
				transcodeFields["status_code"] = demeterAudioValidationStatusCode(validationErr.code)
				logDemeterAudioPerformanceTaskCtx(logCtx, route, seq, "ffmpeg_transcode_failed", "transcodage_ffmpeg", transcodeFields)
				logDemeterAudioBackendErrorTaskCtx(logCtx, route, seq, "chunk_transcode_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
					"chunk_index":     plan.Index,
					"chunk_id":        plan.ChunkID,
					"chunk_start_sec": plan.StartSec,
					"chunk_end_sec":   plan.EndSec,
					"duration_ms":     transcodeDurationMs,
					"message":         validationErr.Error(),
					"status_code":     fiber.StatusServiceUnavailable,
				}))
				return demeterChunkTranscriptionResult{statusCode: fiber.StatusServiceUnavailable}, validationErr
			}
			transcodeFields["message"] = err.Error()
			transcodeFields["status_code"] = fiber.StatusBadGateway
			logDemeterAudioPerformanceTaskCtx(logCtx, route, seq, "ffmpeg_transcode_failed", "transcodage_ffmpeg", transcodeFields)
			logDemeterAudioStageCtx(logCtx, route, seq, "chunk_transcode_error", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
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
			logDemeterAudioBackendErrorTaskCtx(logCtx, route, seq, "chunk_transcode_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
				"chunk_index":     plan.Index,
				"chunk_id":        plan.ChunkID,
				"chunk_start_sec": plan.StartSec,
				"chunk_end_sec":   plan.EndSec,
				"duration_ms":     transcodeDurationMs,
				"message":         err.Error(),
				"status_code":     fiber.StatusBadGateway,
			}))
			return demeterChunkTranscriptionResult{statusCode: fiber.StatusBadGateway}, err
		}
		logDemeterAudioPerformanceTaskCtx(logCtx, route, seq, "ffmpeg_transcode_completed", "transcodage_ffmpeg", map[string]any{
			"chunk_index":        plan.Index,
			"chunk_id":           plan.ChunkID,
			"chunk_start_sec":    plan.StartSec,
			"chunk_end_sec":      plan.EndSec,
			"chunk_duration_sec": plan.Duration,
			"duration_ms":        time.Since(transcodeStartedAt).Milliseconds(),
		})

		var readErr error
		chunkBytes, readErr = os.ReadFile(chunkPath)
		if readErr != nil {
			logDemeterAudioBackendErrorTaskCtx(logCtx, route, seq, "chunk_preparation_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
				"chunk_index":     plan.Index,
				"chunk_id":        plan.ChunkID,
				"chunk_start_sec": plan.StartSec,
				"chunk_end_sec":   plan.EndSec,
				"duration_ms":     time.Since(chunkProcessingStartedAt).Milliseconds(),
				"message":         readErr.Error(),
				"status_code":     fiber.StatusBadGateway,
			}))
			return demeterChunkTranscriptionResult{statusCode: fiber.StatusBadGateway}, readErr
		}
		if len(chunkBytes) == 0 {
			logDemeterAudioBackendErrorTaskCtx(logCtx, route, seq, "chunk_preparation_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
				"chunk_index":     plan.Index,
				"chunk_id":        plan.ChunkID,
				"chunk_start_sec": plan.StartSec,
				"chunk_end_sec":   plan.EndSec,
				"duration_ms":     time.Since(chunkProcessingStartedAt).Milliseconds(),
				"message":         "fichier audio illisible",
				"status_code":     fiber.StatusBadRequest,
			}))
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
		chunkFileName = fmt.Sprintf("chunk_%03d%s", plan.Index+1, demeterAudioChunkFileExt)
		normalizedMime = "audio/wav"
	}

	logDemeterAudioStageCtx(logCtx, route, seq, "upstream_send_start", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
		"chunk_index":        plan.Index,
		"chunk_id":           plan.ChunkID,
		"chunk_start_sec":    plan.StartSec,
		"chunk_end_sec":      plan.EndSec,
		"chunk_duration_sec": plan.Duration,
		"file_name":          upload.FileName,
		"chunk_bytes":        len(chunkBytes),
		"mime_type":          normalizedMime,
		"model":              upload.Model,
		"diarize":            upload.Diarize,
		"normalized_format":  normalizedMime,
	}))

	body, contentType, err := buildDemeterAudioMultipart(chunkBytes, chunkFileName, upload.Model, upload.Diarize)
	if err != nil {
		logDemeterAudioBackendErrorTaskCtx(logCtx, route, seq, "chunk_preparation_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"chunk_index":     plan.Index,
			"chunk_id":        plan.ChunkID,
			"chunk_start_sec": plan.StartSec,
			"chunk_end_sec":   plan.EndSec,
			"duration_ms":     time.Since(chunkProcessingStartedAt).Milliseconds(),
			"message":         err.Error(),
			"status_code":     fiber.StatusInternalServerError,
		}))
		return demeterChunkTranscriptionResult{statusCode: fiber.StatusInternalServerError}, err
	}

	result, err := a.demeterAudioTranscriptionWithRetry(logCtx, route, seq, routeMode, audioDurationSec, audioDurationProvided, body, contentType, len(body))
	if err != nil {
		logDemeterAudioBackendErrorTaskCtx(logCtx, route, seq, "chunk_transcription_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"chunk_index":     plan.Index,
			"chunk_id":        plan.ChunkID,
			"chunk_start_sec": plan.StartSec,
			"chunk_end_sec":   plan.EndSec,
			"duration_ms":     time.Since(chunkProcessingStartedAt).Milliseconds(),
			"request_bytes":   len(body),
			"message":         err.Error(),
			"status_code":     fiber.StatusBadGateway,
		}))
		return demeterChunkTranscriptionResult{statusCode: fiber.StatusBadGateway}, err
	}
	if result.statusCode == fiber.StatusUnprocessableEntity && upload.Diarize {
		logDemeterAudioStageCtx(logCtx, route, seq, "upstream_retry", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
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
		result, err = a.demeterAudioTranscriptionWithRetry(logCtx, route, seq, routeMode, audioDurationSec, audioDurationProvided, body, contentType, len(body))
		if err != nil {
			return demeterChunkTranscriptionResult{statusCode: fiber.StatusBadGateway}, err
		}
	}

	if result.statusCode >= fiber.StatusBadRequest {
		logDemeterAudioBackendErrorTaskCtx(logCtx, route, seq, "chunk_transcription_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"chunk_index":     plan.Index,
			"chunk_id":        plan.ChunkID,
			"chunk_start_sec": plan.StartSec,
			"chunk_end_sec":   plan.EndSec,
			"duration_ms":     time.Since(chunkProcessingStartedAt).Milliseconds(),
			"request_bytes":   len(body),
			"upstream_status": result.statusCode,
			"response_bytes":  len(result.responseBody),
			"attempts":        result.attempts,
			"status_code":     result.statusCode,
		}))
		return demeterChunkTranscriptionResult{
			statusCode:         result.statusCode,
			responseBody:       result.responseBody,
			upstreamDurationMs: result.upstreamDurationMs,
		}, fmt.Errorf("upstream returned status %d", result.statusCode)
	}

	var parsed demeterMistralTranscriptionResponse
	if err := json.Unmarshal(result.responseBody, &parsed); err != nil {
		logDemeterAudioBackendErrorTaskCtx(logCtx, route, seq, "chunk_transcription_failed", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
			"chunk_index":     plan.Index,
			"chunk_id":        plan.ChunkID,
			"chunk_start_sec": plan.StartSec,
			"chunk_end_sec":   plan.EndSec,
			"duration_ms":     time.Since(chunkProcessingStartedAt).Milliseconds(),
			"request_bytes":   len(body),
			"response_bytes":  len(result.responseBody),
			"message":         err.Error(),
			"status_code":     fiber.StatusBadGateway,
		}))
		return demeterChunkTranscriptionResult{
			statusCode:         fiber.StatusBadGateway,
			responseBody:       result.responseBody,
			upstreamDurationMs: result.upstreamDurationMs,
		}, fmt.Errorf("failed to parse mistral chunk response: %w", err)
	}

	logDemeterAudioStageCtx(logCtx, route, seq, "upstream_response_received", demeterAudioRequestBaseFields(routeMode, audioDurationSec, audioDurationProvided, map[string]any{
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

// buildDemeterAudioMultipart creates the multipart body used for upstream
// transcription requests.
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

// buildDemeterBackendTranscriptionChunk converts one upstream response into the
// normalized backend chunk shape and corresponding merged text.
func buildDemeterBackendTranscriptionChunk(
	resp demeterMistralTranscriptionResponse,
	plan demeterBackendChunkPlan,
	upload *demeterBackendAudioUpload,
	startIndex int,
) (demeterBackendTranscriptionChunk, string) {
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

	sourceFormat := ""
	if upload != nil {
		sourceFormat = strings.TrimSpace(upload.SourceFormat)
	}

	return demeterBackendTranscriptionChunk{
		ChunkID:          plan.ChunkID,
		Index:            plan.Index,
		StartSec:         plan.StartSec,
		EndSec:           plan.EndSec,
		DurationSec:      plan.Duration,
		FileName:         plan.FileName,
		MimeType:         plan.MimeType,
		SourceFormat:     sourceFormat,
		NormalizedFormat: "audio/wav",
		SegmentCount:     len(segments),
		Text:             chunkText,
		Segments:         segments,
	}, chunkText
}

// offsetDemeterWords shifts word timings so each chunk can be stitched back
// into the full transcript.
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

// buildDemeterBackendAudioUploadFromSource probes a local audio file and
// prepares the reconstruction metadata used by the backend path.
func buildDemeterBackendAudioUploadFromSource(
	ctx context.Context,
	logCtx demeterAudioLogContext,
	route string,
	seq uint64,
	tempDir string,
	sourcePath string,
	fileName string,
	mimeType string,
	model string,
	diarize bool,
	sourceFormat string,
) (*demeterBackendAudioUpload, error) {
	probeStartedAt := time.Now()
	probe, err := probeDemeterAudioFile(ctx, sourcePath)
	probeDurationMs := time.Since(probeStartedAt).Milliseconds()
	probeFields := map[string]any{
		"file_name":       fileName,
		"mime_type":       mimeType,
		"source_format":   sourceFormat,
		"file_size_bytes": fileSizeBytes(sourcePath),
		"duration_ms":     probeDurationMs,
	}
	if err != nil {
		var validationErr *demeterAudioValidationError
		if errors.As(err, &validationErr) {
			validationErr.file = demeterAudioFileInfo{
				FileName:  fileName,
				MimeType:  mimeType,
				SizeBytes: fileSizeBytes(sourcePath),
			}
			probeFields["message"] = validationErr.Error()
			probeFields["status_code"] = demeterAudioValidationStatusCode(validationErr.code)
			logDemeterAudioPerformanceTaskCtx(logCtx, route, seq, "ffprobe_validation_failed", "validation_ffprobe", probeFields)
			_ = os.RemoveAll(tempDir)
			return nil, validationErr
		}
		probeFields["message"] = err.Error()
		probeFields["status_code"] = fiber.StatusInternalServerError
		logDemeterAudioPerformanceTaskCtx(logCtx, route, seq, "ffprobe_validation_failed", "validation_ffprobe", probeFields)
		_ = os.RemoveAll(tempDir)
		return nil, err
	}

	probedFormat := "unknown"
	if normalized := strings.TrimSpace(probe.FormatName); normalized != "" {
		probedFormat = strings.Clone(normalized)
	}
	probeFields["probed_format"] = probedFormat
	probeFields["duration_sec"] = probe.DurationSec
	logDemeterAudioPerformanceTaskCtx(logCtx, route, seq, "ffprobe_validation_completed", "validation_ffprobe", probeFields)

	return &demeterBackendAudioUpload{
		FileName:      cloneDemeterRequestString(fileName),
		MimeType:      cloneDemeterRequestString(mimeType),
		SizeBytes:     fileSizeBytes(sourcePath),
		Model:         cloneDemeterRequestString(model),
		Diarize:       diarize,
		SourcePath:    sourcePath,
		SourceDir:     tempDir,
		SourceFormat:  cloneDemeterRequestString(sourceFormat),
		ProbedFormat:  probedFormat,
		DurationSec:   probe.DurationSec,
		ChunkSettings: demeterBackendChunkingConfig{},
		cleanup: func() {
			_ = os.RemoveAll(tempDir)
		},
	}, nil
}

// fileSizeBytes returns zero when the file is missing and the actual byte size
// otherwise.
func fileSizeBytes(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// demeterAudioProbeResult stores the ffprobe-derived metadata used during
// validation.
type demeterAudioProbeResult struct {
	DurationSec float64
	FormatName  string
	CodecName   string
	SampleRate  int
	Channels    int
}

// probeDemeterAudioFile inspects the source audio file with ffprobe and
// converts it into the validation metadata used by the backend route.
func probeDemeterAudioFile(ctx context.Context, inputPath string) (*demeterAudioProbeResult, error) {
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
	cmd := exec.CommandContext(ctx, ffprobePath, args...)
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

// resolveDemeterBackendChunkingConfig merges settings and model limits into one
// effective chunking policy.
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
	if requestedDuration > demeterBackendChunkMaxDurationSec {
		requestedDuration = demeterBackendChunkMaxDurationSec
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

// resolveDemeterBackendModelMaxDurationSec returns the maximum supported chunk
// duration for the selected model.
func resolveDemeterBackendModelMaxDurationSec(model string) int {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return demeterBackendChunkMaxDurationSec
	}
	if normalized == "voxtral-mini-latest" || strings.HasPrefix(normalized, "voxtral-mini-") {
		return demeterBackendChunkMaxDurationSec
	}
	return demeterBackendChunkMaxDurationSec
}

// loadDemeterBackendAudioChunkSettings reads the chunking policy from the
// caller's settings document.
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

	if parsed, ok := readDemeterBackendIntSettingFromKeys(payload, "cloudDemeterChunkDurationSec", "cloudMistralChunkDurationSec"); ok {
		settings.ChunkDurationSec = parsed
	}
	if parsed, ok := readDemeterBackendIntSettingFromKeys(payload, "cloudDemeterOverlapSec", "cloudMistralOverlapSec"); ok {
		settings.OverlapSec = parsed
	}
	return settings, nil
}

// readDemeterBackendIntSettingFromKeys extracts the first matching numeric
// setting from the payload.
func readDemeterBackendIntSettingFromKeys(payload map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok {
			continue
		}
		if parsed, ok := readDemeterBackendIntSetting(value); ok {
			return parsed, true
		}
	}
	return 0, false
}

// readDemeterBackendIntSetting converts supported JSON number shapes into a
// Go int.
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

// buildDemeterBackendChunkPlans splits the audio duration into the default
// sequence of chunk windows.
func buildDemeterBackendChunkPlans(durationSec float64, chunkDurationSec, overlapSec int) []demeterBackendChunkPlan {
	return buildDemeterBackendChunkPlansWithPrefix(durationSec, chunkDurationSec, overlapSec, "demeter-backend")
}

// buildDemeterBackendChunkPlansWithPrefix does the same split while allowing a
// deterministic chunk identifier prefix.
func buildDemeterBackendChunkPlansWithPrefix(durationSec float64, chunkDurationSec, overlapSec int, chunkIDPrefix string) []demeterBackendChunkPlan {
	if !(durationSec > 0) {
		return nil
	}
	prefix := strings.TrimSpace(chunkIDPrefix)
	if prefix == "" {
		prefix = "demeter-backend"
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
			ChunkID:  fmt.Sprintf("%s-%03d", prefix, index+1),
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

// transcodeDemeterAudioChunk converts the source audio into the normalized WAV
// format expected by the upstream provider.
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

// errDemeterAudioTranscodeFailed marks ffmpeg failures after stderr has been
// attached to the error.
var errDemeterAudioTranscodeFailed = errors.New("audio transcode failed")

// errDemeterAudioChunkEmpty reports that ffmpeg produced an empty output file.
var errDemeterAudioChunkEmpty = errors.New("audio chunk empty")

// formatDemeterFloat renders float values in a stable compact representation
// for ffmpeg arguments and logs.
func formatDemeterFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', 3, 64)
}

// maxInt returns the larger of two integers.
func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
