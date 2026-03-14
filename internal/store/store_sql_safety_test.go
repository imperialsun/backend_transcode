package store

import (
	"context"
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
