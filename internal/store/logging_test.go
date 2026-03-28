package store

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreLogsLifecycleAndMutations(t *testing.T) {
	var buf bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	previousPrefix := log.Prefix()
	log.SetOutput(&buf)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	})

	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "store-logs.sqlite"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer closeTestStore(t, st)

	org := createOrg(t, st, "Logs Org", "logs-org", "active")
	user := createUserWithPassword(t, st, org.ID, "logs@example.com", "ChangeMe123!", "active")

	if _, err := st.SaveUserSettings(ctx, user.ID, org.ID, json.RawMessage(`{"secret":"value"}`), 2); err != nil {
		t.Fatalf("SaveUserSettings failed: %v", err)
	}
	if _, err := st.IngestActivityEvents(ctx, org.ID, user.ID, []ActivityEventInput{
		{EventID: "evt-1", EventKind: "report", SourceMode: "local", Provider: "local", Status: "success", OccurredAt: time.Now().UTC()},
	}); err != nil {
		t.Fatalf("IngestActivityEvents failed: %v", err)
	}

	logged := buf.String()
	for _, needle := range []string{
		"step=open_success",
		"step=migrate_success",
		"step=seed_success",
		"step=create_success",
		"step=save_success",
		"step=ingest_success",
	} {
		if !strings.Contains(logged, needle) {
			t.Fatalf("expected %q in logs, got %q", needle, logged)
		}
	}
	if strings.Contains(logged, `"secret":"value"`) {
		t.Fatalf("did not expect raw settings payload in logs, got %q", logged)
	}
	if strings.Contains(logged, "password_hash") {
		t.Fatalf("did not expect password hash in logs, got %q", logged)
	}
}

func TestStoreLogsOpenError(t *testing.T) {
	var buf bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	previousPrefix := log.Prefix()
	log.SetOutput(&buf)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	})

	ctx := context.Background()
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("file"), 0o644); err != nil {
		t.Fatalf("failed to create blocking file: %v", err)
	}
	_, err := Open(ctx, filepath.Join(parent, "db.sqlite"))
	if err == nil {
		t.Fatal("expected Open to fail when parent path is a file")
	}

	logged := buf.String()
	if !strings.Contains(logged, "step=open_error") {
		t.Fatalf("expected open_error log, got %q", logged)
	}
	if !strings.Contains(logged, "[store] route=sqlite") {
		t.Fatalf("expected store trace line, got %q", logged)
	}
}
