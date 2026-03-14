package store

import (
	"context"
	"path/filepath"
	"testing"

	"demeter-backend/internal/auth"
)

func TestSeedBaseCatalog_DoesNotExposeGradioPermission(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "catalog.sqlite")
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	if err := st.SeedBaseCatalog(ctx); err != nil {
		t.Fatalf("failed to seed base catalog: %v", err)
	}

	permissions, err := st.ListPermissionsCatalog(ctx)
	if err != nil {
		t.Fatalf("failed to list permissions catalog: %v", err)
	}

	for _, permission := range permissions {
		if permission["code"] == "provider.cloud.gradio" {
			t.Fatalf("unexpected gradio permission in catalog: %v", permission)
		}
	}
}

func TestSeedBaseCatalog_IsIdempotentAndPublishesExpectedRoles(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t, "catalog-idempotent.sqlite")

	if err := st.SeedBaseCatalog(ctx); err != nil {
		t.Fatalf("first SeedBaseCatalog failed: %v", err)
	}
	if err := st.SeedBaseCatalog(ctx); err != nil {
		t.Fatalf("second SeedBaseCatalog failed: %v", err)
	}

	globalRoles, err := st.ListGlobalRolesCatalog(ctx)
	if err != nil {
		t.Fatalf("ListGlobalRolesCatalog failed: %v", err)
	}
	if len(globalRoles) < 2 {
		t.Fatalf("expected seeded global roles, got %+v", globalRoles)
	}

	orgRoles, err := st.ListOrganizationRolesCatalog(ctx)
	if err != nil {
		t.Fatalf("ListOrganizationRolesCatalog failed: %v", err)
	}
	if len(orgRoles) < 2 {
		t.Fatalf("expected seeded organization roles, got %+v", orgRoles)
	}
}

func TestEnsureBootstrap_CreatesAdminOrganizationAndRoles(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t, "bootstrap.sqlite")

	hash, err := auth.HashPassword("ChangeMe123!")
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}
	if err := st.EnsureBootstrap(ctx, "Admin@Example.com", hash, "Bootstrap Org"); err != nil {
		t.Fatalf("EnsureBootstrap failed: %v", err)
	}

	orgs, err := st.ListOrganizations(ctx)
	if err != nil {
		t.Fatalf("ListOrganizations failed: %v", err)
	}
	if len(orgs) != 1 {
		t.Fatalf("expected 1 bootstrap organization, got %+v", orgs)
	}

	user, err := st.FindUserByEmail(ctx, "admin@example.com")
	if err != nil {
		t.Fatalf("FindUserByEmail failed: %v", err)
	}
	if user == nil {
		t.Fatal("expected bootstrap admin user")
	}

	globalRoles, err := st.GetGlobalRoleCodesByUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetGlobalRoleCodesByUser failed: %v", err)
	}
	if len(globalRoles) != 2 {
		t.Fatalf("expected bootstrap global roles, got %+v", globalRoles)
	}

	orgRoles, err := st.GetOrganizationRoleCodesByUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetOrganizationRoleCodesByUser failed: %v", err)
	}
	if len(orgRoles) != 2 {
		t.Fatalf("expected bootstrap organization roles, got %+v", orgRoles)
	}
}
