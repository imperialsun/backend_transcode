package store

import (
	"context"
	"testing"
)

func TestReportTemplatesCRUDAndUserPreferences(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t, "report-templates.sqlite")
	org := createOrg(t, st, "Templates Org", "templates-org", "active")
	user := createUserWithPassword(t, st, org.ID, "templates@example.com", "secret123", "active")

	template, err := st.CreateOrganizationReportTemplate(ctx, org.ID, user.ID, ReportTemplateInput{
		Name:           "CR Comité",
		Description:    "Synthèse comité",
		BaseFormat:     "cro",
		Instructions:   "Mettre les décisions et risques en avant.",
		ExampleOutline: "Décisions\nRisques\nActions",
		OrgEnabled:     true,
	})
	if err != nil {
		t.Fatalf("failed to create report template: %v", err)
	}
	if template.BaseFormat != "CRO" || !template.OrgEnabled {
		t.Fatalf("unexpected normalized template: %+v", template)
	}

	items, err := st.ListOrganizationReportTemplates(ctx, org.ID, false)
	if err != nil {
		t.Fatalf("failed to list templates: %v", err)
	}
	if len(items) != 1 || items[0].ID != template.ID {
		t.Fatalf("expected created template in org list, got %+v", items)
	}

	prefs, err := st.ListUserReportTemplatePreferences(ctx, org.ID, user.ID)
	if err != nil {
		t.Fatalf("failed to list user preferences: %v", err)
	}
	if len(prefs) != 1 || !prefs[0].Enabled {
		t.Fatalf("expected default enabled user preference, got %+v", prefs)
	}

	saved, err := st.SaveUserReportTemplatePreference(ctx, user.ID, template.ID, false)
	if err != nil {
		t.Fatalf("failed to save preference: %v", err)
	}
	if saved.Enabled {
		t.Fatal("expected saved preference to be disabled")
	}
	enabled, err := st.IsUserReportTemplateEnabled(ctx, user.ID, template.ID)
	if err != nil {
		t.Fatalf("failed to resolve preference: %v", err)
	}
	if enabled {
		t.Fatal("expected template to be disabled for user")
	}

	updated, err := st.UpdateOrganizationReportTemplate(ctx, template.ID, user.ID, ReportTemplateInput{
		Name:           "CR Comité v2",
		Description:    "Synthèse comité étendue",
		BaseFormat:     "CRI",
		Instructions:   "Garder le contexte et les arbitrages.",
		ExampleOutline: "",
		OrgEnabled:     false,
	})
	if err != nil {
		t.Fatalf("failed to update template: %v", err)
	}
	if updated.OrgEnabled || updated.Name != "CR Comité v2" {
		t.Fatalf("unexpected updated template: %+v", updated)
	}
	visible, err := st.ListUserReportTemplatePreferences(ctx, org.ID, user.ID)
	if err != nil {
		t.Fatalf("failed to list visible templates: %v", err)
	}
	if len(visible) != 0 {
		t.Fatalf("disabled org template should not be visible, got %+v", visible)
	}
}
