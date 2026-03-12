package store

import (
	"context"
	"path/filepath"
	"testing"
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
