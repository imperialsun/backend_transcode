package api

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const demeterAudioCleanupTask = "suppression_fichiers_transcription"

// cleanupDemeterAudioTempPath removes one temporary directory and records the
// cleanup as a backend performance event.
func cleanupDemeterAudioTempPath(
	logCtx demeterAudioLogContext,
	route string,
	seq uint64,
	cleanupScope string,
	cleanupTarget string,
	path string,
	extraFields map[string]any,
) {
	if strings.TrimSpace(path) == "" {
		return
	}

	startedAt := time.Now()
	removedEntries := countFilesystemEntries(path)
	err := os.RemoveAll(path)

	fields := map[string]any{
		"cleanup_scope":   strings.TrimSpace(cleanupScope),
		"cleanup_target":  strings.TrimSpace(cleanupTarget),
		"removed_entries": removedEntries,
		"duration_ms":     time.Since(startedAt).Milliseconds(),
	}
	for key, value := range extraFields {
		fields[key] = value
	}
	if err != nil {
		fields["message"] = err.Error()
		logDemeterAudioPerformanceTaskCtx(logCtx, route, seq, "cleanup_failed", demeterAudioCleanupTask, fields)
		return
	}

	logDemeterAudioPerformanceTaskCtx(logCtx, route, seq, "cleanup_completed", demeterAudioCleanupTask, fields)
}

// countFilesystemEntries counts the children that will be removed under a
// temporary path so cleanup events can report a useful size without exposing
// the raw filesystem layout.
func countFilesystemEntries(path string) int {
	if strings.TrimSpace(path) == "" {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	if !info.IsDir() {
		return 1
	}

	count := 0
	_ = filepath.WalkDir(path, func(current string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if current == path {
			return nil
		}
		count++
		return nil
	})
	return count
}
