package store

import (
	"context"
	"database/sql"
	"testing"
)

func TestResolveTableInfoQuery(t *testing.T) {
	q, err := resolveTableInfoQuery("refresh_sessions")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q == "" {
		t.Fatal("expected a query string")
	}

	_, err = resolveTableInfoQuery("unknown")
	if err == nil {
		t.Fatal("expected error for unsupported table")
	}
}

func TestEnsureColumnExistsAddsMissingColumn(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/test.sqlite"
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin tx: %v", err)
	}
	defer rollbackTx(tx)

	// ensure a made-up nullable column can be added
	if err := ensureColumnExists(ctx, tx, "refresh_sessions", "test_column", "ALTER TABLE refresh_sessions ADD COLUMN test_column TEXT"); err != nil {
		t.Fatalf("ensureColumnExists returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("failed to commit tx: %v", err)
	}

	// verify the column exists
	rows, err := st.DB.QueryContext(ctx, `PRAGMA table_info(refresh_sessions)`)
	if err != nil {
		t.Fatalf("failed to query table info: %v", err)
	}
	defer closeRows(rows)
	found := false
	for rows.Next() {
		var cid int
		var name, valueType string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &valueType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("failed to scan row: %v", err)
		}
		if name == "test_column" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected test_column to exist")
	}
}

func TestEnsureColumnExistsDoesNotFailWhenAlreadyPresent(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/test2.sqlite"
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin tx: %v", err)
	}
	defer rollbackTx(tx)

	// Should not error when column already exists
	if err := ensureColumnExists(ctx, tx, "refresh_sessions", "session_type", "ALTER TABLE refresh_sessions ADD COLUMN session_type TEXT"); err != nil {
		t.Fatalf("expected no error when column already exists, got %v", err)
	}
}
