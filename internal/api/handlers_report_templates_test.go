package api

import "testing"

func TestReportTemplateInputAcceptsCustomBaseFormat(t *testing.T) {
	input, err := reportTemplateInputFromRequest(reportTemplateRequest{
		Name:         "CR Libre",
		BaseFormat:   "custom",
		Instructions: "Suivre uniquement les consignes du modèle.",
	})
	if err != nil {
		t.Fatalf("expected custom template input to be accepted: %v", err)
	}
	if input.BaseFormat != "CUSTOM" {
		t.Fatalf("expected CUSTOM base format, got %q", input.BaseFormat)
	}
}

func TestReportTemplateInputRejectsUnknownBaseFormat(t *testing.T) {
	if _, err := reportTemplateInputFromRequest(reportTemplateRequest{
		Name:         "CR Invalide",
		BaseFormat:   "XYZ",
		Instructions: "Consignes",
	}); err == nil {
		t.Fatal("expected unknown template base format to be rejected")
	}
}
