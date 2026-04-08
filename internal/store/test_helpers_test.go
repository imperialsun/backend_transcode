package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"demeter-backend/internal/auth"
)

// openTestStore creates a fully initialized SQLite store for store tests.
func openTestStore(t *testing.T, name string) *Store {
	t.Helper()
	st, err := Open(context.Background(), filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("failed to open sqlite store: %v", err)
	}
	t.Cleanup(func() {
		closeTestStore(t, st)
	})
	return st
}

// closeTestStore closes the store and reports any teardown failure.
func closeTestStore(t *testing.T, st *Store) {
	t.Helper()
	if st == nil {
		return
	}
	if err := st.Close(); err != nil {
		t.Errorf("close store: %v", err)
	}
}

// closeTestDB closes a raw SQL database handle during test cleanup.
func closeTestDB(t *testing.T, db *sql.DB) {
	t.Helper()
	if db == nil {
		return
	}
	if err := db.Close(); err != nil {
		t.Errorf("close db: %v", err)
	}
}

// createOrg creates an organization row for tests.
func createOrg(t *testing.T, st *Store, name, code, status string) *Organization {
	t.Helper()
	org, err := st.CreateOrganization(context.Background(), name, code, status)
	if err != nil {
		t.Fatalf("failed to create organization: %v", err)
	}
	return org
}

// createUserWithPassword hashes the password and inserts a test user.
func createUserWithPassword(t *testing.T, st *Store, orgID, email, password, status string) *User {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}
	user, err := st.CreateUser(context.Background(), orgID, email, hash, status)
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	return user
}
