package reports

import (
	"strings"
	"testing"
)

func TestBuildReportUserPromptWithDetailUsesFrontendLevels(t *testing.T) {
	source := strings.Repeat("mot ", 1000)
	prompt := BuildReportUserPromptWithDetail(ReportFormatCRI, ReportDetailExhaustive, source, "Reunion mobile", []string{"A", "B"})

	for _, needle := range []string{
		"longueur minimale obligatoire (Exhaustif)",
		"au moins 150 mots",
		"Format cible: CRI.",
		"Participants:",
	} {
		if !strings.Contains(prompt, needle) {
			t.Fatalf("expected prompt to contain %q, got %q", needle, prompt)
		}
	}
}

func TestNormalizeReportDetailLevelsDefaultsInvalidValues(t *testing.T) {
	levels := NormalizeReportDetailLevels(map[ReportFormat]ReportDetailLevel{
		ReportFormatCRI: ReportDetailVerbose,
		ReportFormatCRO: ReportDetailLevel("invalid"),
	})

	if levels[ReportFormatCRI] != ReportDetailVerbose {
		t.Fatalf("expected CRI verbose, got %q", levels[ReportFormatCRI])
	}
	if levels[ReportFormatCRO] != ReportDetailStandard {
		t.Fatalf("expected CRO default standard, got %q", levels[ReportFormatCRO])
	}
	if levels[ReportFormatCRS] != ReportDetailStandard {
		t.Fatalf("expected CRS default standard, got %q", levels[ReportFormatCRS])
	}
}
