package api

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDemeterTransportSessionSlicePathsReadsFromDisk(t *testing.T) {
	tempDir := t.TempDir()
	session := &demeterAudioTransportSession{
		tempDir:       tempDir,
		sliceCount:    3,
		receivedPaths: map[int]string{},
		receivedSizes: map[int]int64{},
	}

	contents := [][]byte{
		[]byte("slice-0"),
		[]byte("slice-1"),
		[]byte("slice-2"),
	}
	for index, content := range contents {
		path := demeterAudioTransportSlicePath(tempDir, index)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatalf("failed to write slice %d: %v", index, err)
		}
	}

	paths, err := demeterTransportSessionSlicePaths(session)
	if err != nil {
		t.Fatalf("expected slices to be resolved from disk, got error: %v", err)
	}
	if len(paths) != len(contents) {
		t.Fatalf("unexpected path count: got %d want %d", len(paths), len(contents))
	}

	for index, path := range paths {
		expectedPath := demeterAudioTransportSlicePath(tempDir, index)
		if path != expectedPath {
			t.Fatalf("unexpected path order at %d: got %q want %q", index, path, expectedPath)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read slice %d: %v", index, err)
		}
		if string(data) != string(contents[index]) {
			t.Fatalf("unexpected content at %d: got %q want %q", index, data, contents[index])
		}
	}

	if total := demeterAudioTransportTotalBytes(tempDir, session.sliceCount); total != int64(len("slice-0")+len("slice-1")+len("slice-2")) {
		t.Fatalf("unexpected total bytes: got %d", total)
	}
}

func TestDemeterAudioTransportSessionDirDeterministic(t *testing.T) {
	first := demeterAudioTransportSessionDir("fc32884f-4b71-4eff-8aa6-3ba7a30b405b")
	second := demeterAudioTransportSessionDir("fc32884f-4b71-4eff-8aa6-3ba7a30b405b")
	third := demeterAudioTransportSessionDir("different-upload-id")

	if first != second {
		t.Fatalf("expected deterministic session dir, got %q and %q", first, second)
	}
	if first == third {
		t.Fatalf("expected different upload ids to map to different session dirs, got %q", first)
	}
	if filepath.Base(first) != "fc32884f-4b71-4eff-8aa6-3ba7a30b405b" {
		t.Fatalf("unexpected sanitized base dir: %q", filepath.Base(first))
	}
}
