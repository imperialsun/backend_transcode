package reports

import (
	"strings"
	"testing"
)

func TestBuildCrnTranscriptBatchesSplitsIntoFrontendSizedBatches(t *testing.T) {
	source := strings.Join([]string{
		"  Ligne 1   ",
		"Ligne 2",
		"",
		"Ligne 3",
		"Ligne 4",
		"Ligne 5",
		"Ligne 6",
		"Ligne 7",
		"Ligne 8",
		"Ligne 9",
		"Ligne 10",
		"Ligne 11",
		"Ligne 12",
	}, "\n")

	batches := BuildCrnTranscriptBatches(source, 10, 0)
	if len(batches) != 2 {
		t.Fatalf("expected 2 batches, got %d", len(batches))
	}
	if batches[0].BatchIndex != 1 || batches[0].BatchCount != 2 {
		t.Fatalf("unexpected first batch metadata: %+v", batches[0])
	}
	if batches[0].StartLine != 0 || batches[0].EndLine != 10 {
		t.Fatalf("unexpected first batch range: %+v", batches[0])
	}
	if got := len(batches[0].Lines); got != 10 {
		t.Fatalf("expected first batch to contain 10 lines, got %d", got)
	}
	if batches[0].Lines[0] != "Ligne 1" || batches[0].Lines[1] != "Ligne 2" {
		t.Fatalf("unexpected normalization in first batch: %+v", batches[0].Lines[:2])
	}
	if batches[1].BatchIndex != 2 || batches[1].BatchCount != 2 {
		t.Fatalf("unexpected second batch metadata: %+v", batches[1])
	}
	if got := len(batches[1].Lines); got != 2 {
		t.Fatalf("expected second batch to contain 2 lines, got %d", got)
	}
	if batches[1].Text != "Ligne 11\nLigne 12" {
		t.Fatalf("unexpected second batch text: %q", batches[1].Text)
	}
}

func TestMergeCrnReportResultsMergesDuplicateSectionsAndLists(t *testing.T) {
	merged, err := MergeCrnReportResults([]ReportJson{
		{
			Format: ReportFormatCRN,
			Title:  "Compte rendu",
			Sections: []ReportSection{{
				Heading:    "Points clés",
				Paragraphs: []string{"Premier paragraphe", "Doublon"},
			}},
			KeyPoints:   []string{"Point A", "Point B"},
			ActionItems: []string{"Action 1"},
			Caveats:     []string{"Aucune"},
		},
		{
			Format:   ReportFormatCRN,
			Subtitle: "Sous-titre",
			Sections: []ReportSection{{
				Heading:    "points clés",
				Paragraphs: []string{"Doublon", "Second paragraphe"},
			}, {
				Heading:    "Chronologie",
				Paragraphs: []string{"Lot 2"},
			}},
			KeyPoints:   []string{"Point B", "Point C"},
			ActionItems: []string{"Action 1", "Action 2"},
			Caveats:     []string{"Aucune", "Lot 2"},
		},
	})
	if err != nil {
		t.Fatalf("MergeCrnReportResults returned error: %v", err)
	}
	if merged.Format != ReportFormatCRN {
		t.Fatalf("expected merged format CRN, got %s", merged.Format)
	}
	if merged.Title != "Compte rendu" || merged.Subtitle != "Sous-titre" {
		t.Fatalf("unexpected merged title/subtitle: %+v", merged)
	}
	if len(merged.Sections) != 2 {
		t.Fatalf("expected 2 merged sections, got %d", len(merged.Sections))
	}
	if merged.Sections[0].Heading != "Points clés" {
		t.Fatalf("expected first section to keep original heading, got %q", merged.Sections[0].Heading)
	}
	if len(merged.Sections[0].Paragraphs) != 3 {
		t.Fatalf("expected merged first section to dedupe paragraphs, got %+v", merged.Sections[0].Paragraphs)
	}
	if len(merged.KeyPoints) != 3 || merged.KeyPoints[2] != "Point C" {
		t.Fatalf("unexpected merged key points: %+v", merged.KeyPoints)
	}
	if len(merged.ActionItems) != 2 || merged.ActionItems[1] != "Action 2" {
		t.Fatalf("unexpected merged action items: %+v", merged.ActionItems)
	}
	if len(merged.Caveats) != 2 || merged.Caveats[1] != "Lot 2" {
		t.Fatalf("unexpected merged caveats: %+v", merged.Caveats)
	}
}
