package reports

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	defaultCRNBatchLineCount    = 10
	defaultCRNBatchOverlapLines = 0
)

// CrnTranscriptBatch represents one frontend-aligned CRN batch slice.
type CrnTranscriptBatch struct {
	BatchIndex int
	BatchCount int
	StartLine  int
	EndLine    int
	Lines      []string
	Text       string
}

// SplitTranscriptTextIntoLines mirrors the frontend CRN line normalization.
func SplitTranscriptTextIntoLines(sourceText string) []string {
	if strings.TrimSpace(sourceText) == "" {
		return nil
	}
	parts := strings.FieldsFunc(sourceText, func(r rune) bool {
		return r == '\n' || r == '\r'
	})
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		if normalized := normalizeCRNLineText(part); normalized != "" {
			lines = append(lines, normalized)
		}
	}
	return lines
}

// BuildCrnTranscriptBatches splits a transcript into the same CRN batches used
// by the frontend pipeline.
func BuildCrnTranscriptBatches(sourceText string, linesPerBatch, overlapLines int) []CrnTranscriptBatch {
	lines := SplitTranscriptTextIntoLines(sourceText)
	if len(lines) == 0 {
		return nil
	}

	chunkSize := normalizePositiveInt(linesPerBatch, defaultCRNBatchLineCount)
	safeOverlap := normalizePositiveInt(overlapLines, defaultCRNBatchOverlapLines)
	if safeOverlap >= chunkSize {
		safeOverlap = chunkSize - 1
	}
	step := chunkSize - safeOverlap
	if step < 1 {
		step = 1
	}

	batches := make([]CrnTranscriptBatch, 0, (len(lines)+step-1)/step)
	for startLine := 0; startLine < len(lines); startLine += step {
		endLine := startLine + chunkSize
		if endLine > len(lines) {
			endLine = len(lines)
		}
		batchLines := append([]string(nil), lines[startLine:endLine]...)
		batches = append(batches, CrnTranscriptBatch{
			BatchIndex: len(batches) + 1,
			StartLine:  startLine,
			EndLine:    endLine,
			Lines:      batchLines,
			Text:       strings.Join(batchLines, "\n"),
		})
		if endLine >= len(lines) {
			break
		}
	}

	batchCount := len(batches)
	for index := range batches {
		batches[index].BatchCount = batchCount
	}
	return batches
}

// MergeCrnReportResults combines batch outputs into one CRN report, mirroring
// the frontend merge behavior.
func MergeCrnReportResults(reports []ReportJson) (ReportJson, error) {
	if len(reports) == 0 {
		return ReportJson{}, fmt.Errorf("impossible de fusionner un CRN vide")
	}

	merged, err := cloneReportJSON(reports[0])
	if err != nil {
		return ReportJson{}, err
	}
	merged.Format = ReportFormatCRN

	for _, report := range reports[1:] {
		mergeCRNReportTitle(&merged, report)
		mergeCRNReportSubtitle(&merged, report)
		mergeCRNReportSections(&merged, report)
		merged.KeyPoints = mergeCRNUniqueStringLists(merged.KeyPoints, report.KeyPoints)
		merged.ActionItems = mergeCRNUniqueStringLists(merged.ActionItems, report.ActionItems)
		merged.Caveats = mergeCRNUniqueStringLists(merged.Caveats, report.Caveats)
	}

	return merged, nil
}

func cloneReportJSON(value ReportJson) (ReportJson, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return ReportJson{}, err
	}
	var clone ReportJson
	if err := json.Unmarshal(raw, &clone); err != nil {
		return ReportJson{}, err
	}
	return clone, nil
}

func mergeCRNReportTitle(target *ReportJson, source ReportJson) {
	if strings.TrimSpace(target.Title) == "" && strings.TrimSpace(source.Title) != "" {
		target.Title = source.Title
	}
}

func mergeCRNReportSubtitle(target *ReportJson, source ReportJson) {
	if strings.TrimSpace(target.Subtitle) != "" || strings.TrimSpace(source.Subtitle) == "" {
		return
	}
	target.Subtitle = source.Subtitle
}

func mergeCRNReportSections(target *ReportJson, source ReportJson) {
	sectionIndexByHeading := make(map[string]int, len(target.Sections))
	for index, section := range target.Sections {
		sectionIndexByHeading[normalizeCRNSectionKey(section.Heading)] = index
	}

	for _, section := range source.Sections {
		normalizedHeading := normalizeCRNSectionKey(section.Heading)
		if existingIndex, ok := sectionIndexByHeading[normalizedHeading]; ok {
			existingSection := &target.Sections[existingIndex]
			existingSection.Paragraphs = mergeCRNUniqueStringLists(existingSection.Paragraphs, section.Paragraphs)
			continue
		}

		target.Sections = append(target.Sections, ReportSection{
			Heading:    section.Heading,
			Paragraphs: mergeCRNUniqueStringLists(nil, section.Paragraphs),
		})
		sectionIndexByHeading[normalizedHeading] = len(target.Sections) - 1
	}
}

func mergeCRNUniqueStringLists(left []string, right []string) []string {
	capacity := len(left)
	if len(right) > capacity {
		capacity = len(right)
	}
	merged := make([]string, 0, capacity)
	seen := make(map[string]struct{}, capacity)

	appendUnique := func(values []string) {
		for _, value := range values {
			normalized := normalizeCRNLineText(value)
			if normalized == "" {
				continue
			}
			if _, exists := seen[normalized]; exists {
				continue
			}
			seen[normalized] = struct{}{}
			merged = append(merged, strings.TrimSpace(value))
		}
	}

	appendUnique(left)
	appendUnique(right)

	return merged
}

func normalizeCRNSectionKey(value string) string {
	return strings.ToLower(normalizeCRNLineText(value))
}

func normalizeCRNLineText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func normalizePositiveInt(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}
