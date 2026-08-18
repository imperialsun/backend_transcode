package reports

import "testing"

func TestParseReportJSON(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		format ReportFormat
	}{
		{
			name:   "cri",
			raw:    "```json\n{\"format\":\"CRI\",\"title\":\"Test\",\"sections\":[{\"heading\":\"Contexte\",\"paragraphs\":[\"Un paragraphe\"]}],\"key_points\":[\"P1\"],\"action_items\":[\"A1\"],\"caveats\":[\"C1\"]}\n```",
			format: ReportFormatCRI,
		},
		{
			name:   "crn",
			raw:    "```json\n{\"format\":\"CRN\",\"title\":\"Test\",\"sections\":[{\"heading\":\"Contexte\",\"paragraphs\":[\"Un paragraphe\"]}],\"key_points\":[\"P1\"],\"action_items\":[\"A1\"],\"caveats\":[\"C1\"]}\n```",
			format: ReportFormatCRN,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report, err := ParseReportJSON(tt.raw, tt.format)
			if err != nil {
				t.Fatalf("ParseReportJSON failed: %v", err)
			}
			if report.Format != tt.format {
				t.Fatalf("unexpected format: %s", report.Format)
			}
			if len(report.Sections) != 1 {
				t.Fatalf("unexpected section count: %d", len(report.Sections))
			}
		})
	}
}
