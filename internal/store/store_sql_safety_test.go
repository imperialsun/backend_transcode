package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestRoleLookupUsesDedicatedCatalogQueries(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "sql-safety.sqlite")
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin tx: %v", err)
	}
	defer rollbackTx(tx)

	globalRoleID, err := st.lookupGlobalRoleID(ctx, tx, "super_admin")
	if err != nil {
		t.Fatalf("failed to lookup global role id: %v", err)
	}
	if globalRoleID == "" {
		t.Fatal("expected a global role id for super_admin")
	}

	orgRoleID, err := st.lookupOrganizationRoleID(ctx, tx, "org_admin")
	if err != nil {
		t.Fatalf("failed to lookup organization role id: %v", err)
	}
	if orgRoleID == "" {
		t.Fatal("expected an organization role id for org_admin")
	}

	missingRoleID, err := st.lookupGlobalRoleID(ctx, tx, "missing_role")
	if err != nil {
		t.Fatalf("unexpected error for missing role: %v", err)
	}
	if missingRoleID != "" {
		t.Fatalf("expected missing role lookup to return empty id, got %q", missingRoleID)
	}
}

func TestEnsureColumnExistsRejectsUnknownTableTargets(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "ensure-column.sqlite")
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin tx: %v", err)
	}
	defer rollbackTx(tx)

	err = ensureColumnExists(
		ctx,
		tx,
		"refresh_sessions; DROP TABLE users; --",
		"session_type",
		`ALTER TABLE refresh_sessions ADD COLUMN injected TEXT`,
	)
	if !errors.Is(err, errUnsupportedTableInfoTarget) {
		t.Fatalf("expected unsupported table info target error, got %v", err)
	}
}

func TestHasColumnUsesWhitelistedPragmaTargets(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "has-column.sqlite")
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	ok, err := hasColumn(ctx, st.DB, "refresh_sessions", "session_type")
	if err != nil {
		t.Fatalf("failed to inspect whitelisted table: %v", err)
	}
	if !ok {
		t.Fatal("expected refresh_sessions.session_type to exist after migration")
	}

	_, err = hasColumn(ctx, st.DB, "refresh_sessions; DROP TABLE users; --", "session_type")
	if !errors.Is(err, errUnsupportedTableInfoTarget) {
		t.Fatalf("expected unsupported table info target error, got %v", err)
	}
}

func TestMigrateDropsDemeterAudioLegacyColumn(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "demeter-migration.sqlite")
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	if _, err := st.DB.ExecContext(ctx, `ALTER TABLE demeter_audio_transcription_operations ADD COLUMN partial_text TEXT NOT NULL DEFAULT ''`); err != nil {
		t.Fatalf("failed to add legacy partial_text column: %v", err)
	}

	ok, err := hasColumn(ctx, st.DB, "demeter_audio_transcription_operations", "partial_text")
	if err != nil {
		t.Fatalf("failed to inspect legacy column: %v", err)
	}
	if !ok {
		t.Fatal("expected legacy partial_text column to exist before migration rerun")
	}

	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("failed to rerun migration: %v", err)
	}

	ok, err = hasColumn(ctx, st.DB, "demeter_audio_transcription_operations", "partial_text")
	if err != nil {
		t.Fatalf("failed to inspect demeter transcription table after migration: %v", err)
	}
	if ok {
		t.Fatal("expected partial_text column to be removed by migration")
	}
}

func TestOpenMigratesLegacyDemeterQueueColumns(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "legacy-demeter.sqlite")

	db, err := sql.Open("sqlite", sqliteDSN(dbPath))
	if err != nil {
		t.Fatalf("failed to open raw sqlite database: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE demeter_audio_transcription_operations (
			operation_id TEXT PRIMARY KEY,
			organization_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			status TEXT NOT NULL,
			stage TEXT NOT NULL,
			chunk_index INTEGER NOT NULL DEFAULT 0,
			chunk_count INTEGER NOT NULL DEFAULT 0,
			progress REAL NOT NULL DEFAULT 0,
			response_json TEXT,
			last_error TEXT,
			status_code INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			finished_at DATETIME
		);
	`); err != nil {
		_ = db.Close()
		t.Fatalf("failed to create legacy demeter table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("failed to close raw sqlite database: %v", err)
	}

	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open migrated sqlite store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	hasQueueID, err := hasColumn(ctx, st.DB, "demeter_audio_transcription_operations", "queue_id")
	if err != nil {
		t.Fatalf("failed to inspect queue_id column: %v", err)
	}
	if !hasQueueID {
		t.Fatal("expected queue_id column to be added by migration")
	}

	hasQueuePayload, err := hasColumn(ctx, st.DB, "demeter_audio_transcription_operations", "queue_payload_json")
	if err != nil {
		t.Fatalf("failed to inspect queue_payload_json column: %v", err)
	}
	if !hasQueuePayload {
		t.Fatal("expected queue_payload_json column to be added by migration")
	}

	var indexName string
	err = st.DB.QueryRowContext(ctx, `
		SELECT name
		FROM sqlite_master
		WHERE type = 'index' AND name = 'idx_demeter_audio_transcription_operations_queue'
	`).Scan(&indexName)
	if err != nil {
		t.Fatalf("expected demeter queue index to exist after migration: %v", err)
	}
	if indexName != "idx_demeter_audio_transcription_operations_queue" {
		t.Fatalf("unexpected index name after migration: %q", indexName)
	}
}
