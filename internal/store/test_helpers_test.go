package store

import (
	"context"
	"path/filepath"
	"testing"

	"demeter-backend/internal/auth"
)

func openTestStore(t *testing.T, name string) *Store {
	t.Helper()
	st, err := Open(context.Background(), filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("failed to open sqlite store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})
	return st
}

func createOrg(t *testing.T, st *Store, name, code, status string) *Organization {
	t.Helper()
	org, err := st.CreateOrganization(context.Background(), name, code, status)
	if err != nil {
		t.Fatalf("failed to create organization: %v", err)
	}
	return org
}

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
