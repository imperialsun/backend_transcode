package reports

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ReportFormat identifies one of the supported structured report outputs.
type ReportFormat string

const (
	ReportFormatCRI ReportFormat = "CRI"
	ReportFormatCRO ReportFormat = "CRO"
	ReportFormatCRS ReportFormat = "CRS"
	ReportFormatCRN ReportFormat = "CRN"
	// ReportFormatCUSTOM is reserved for organization-authored free-form
	// templates. It is intentionally not accepted by ParseReportFormat so the
	// standard report endpoints keep their built-in format contract.
	ReportFormatCUSTOM ReportFormat = "CUSTOM"
)

// ReportDetailLevel controls how much source material the model should keep in
// the generated report.
type ReportDetailLevel string

const (
	ReportDetailStandard   ReportDetailLevel = "standard"
	ReportDetailVerbose    ReportDetailLevel = "verbose"
	ReportDetailExhaustive ReportDetailLevel = "exhaustive"
)

var ErrInvalidReport = errors.New("invalid report payload")

// ReportSection is one section of a generated report draft.
type ReportSection struct {
	Heading    string   `json:"heading"`
	Paragraphs []string `json:"paragraphs"`
}

// ReportJson is the normalized report shape returned by the model and stored
// by the backend.
type ReportJson struct {
	Format      ReportFormat    `json:"format"`
	Title       string          `json:"title"`
	Subtitle    string          `json:"subtitle,omitempty"`
	Sections    []ReportSection `json:"sections"`
	KeyPoints   []string        `json:"key_points,omitempty"`
	ActionItems []string        `json:"action_items,omitempty"`
	Caveats     []string        `json:"caveats,omitempty"`
}

// ReportDraft ties a parsed report to the raw model output that produced it.
type ReportDraft struct {
	Format ReportFormat `json:"format"`
	Report ReportJson   `json:"report"`
	Raw    string       `json:"raw,omitempty"`
}

// TranscriptSegment stores the speaker-attributed parts of a meeting transcript.
type TranscriptSegment struct {
	SpeakerID   string  `json:"speakerId,omitempty"`
	SpeakerName string  `json:"speakerName,omitempty"`
	Text        string  `json:"text"`
	StartMS     float64 `json:"startMs,omitempty"`
	EndMS       float64 `json:"endMs,omitempty"`
}

// SpeakerAssignment associates a speaker label with the human-readable name
// used in report generation.
type SpeakerAssignment struct {
	SpeakerID string `json:"speakerId"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
}

// ParseReportJSON extracts the structured report payload from the model output
// and validates the expected report format.
func ParseReportJSON(rawOutput string, expectedFormat ReportFormat) (ReportJson, error) {
	parsed, err := parseReportCandidate(rawOutput)
	if err != nil {
		return ReportJson{}, err
	}
	return normalizeReport(parsed, expectedFormat)
}

// ParseReportFormat normalizes a user-provided report format string.
func ParseReportFormat(value string) (ReportFormat, bool) {
	normalized := ReportFormat(strings.ToUpper(strings.TrimSpace(value)))
	switch normalized {
	case ReportFormatCRI, ReportFormatCRO, ReportFormatCRS, ReportFormatCRN:
		return normalized, true
	default:
		return "", false
	}
}

// ParseReportTemplateFormat normalizes a report-template format. Templates may
// be based on a built-in format or be fully organization-authored.
func ParseReportTemplateFormat(value string) (ReportFormat, bool) {
	normalized := ReportFormat(strings.ToUpper(strings.TrimSpace(value)))
	if normalized == ReportFormatCUSTOM {
		return normalized, true
	}
	return ParseReportFormat(string(normalized))
}

// ParseReportOutputFormat normalizes the format embedded in a generated report
// JSON payload.
func ParseReportOutputFormat(value string) (ReportFormat, bool) {
	return ParseReportTemplateFormat(value)
}

// ParseReportDetailLevel normalizes a user-provided detail level.
func ParseReportDetailLevel(value string) (ReportDetailLevel, bool) {
	switch ReportDetailLevel(strings.ToLower(strings.TrimSpace(value))) {
	case ReportDetailStandard:
		return ReportDetailStandard, true
	case ReportDetailVerbose:
		return ReportDetailVerbose, true
	case ReportDetailExhaustive:
		return ReportDetailExhaustive, true
	default:
		return "", false
	}
}

// NormalizeReportDetailLevels returns a complete detail-level map, defaulting
// every missing or invalid format to the standard level.
func NormalizeReportDetailLevels(values map[ReportFormat]ReportDetailLevel) map[ReportFormat]ReportDetailLevel {
	out := map[ReportFormat]ReportDetailLevel{}
	for _, format := range AllReportFormats() {
		level := ReportDetailStandard
		if candidate, ok := values[format]; ok {
			if parsed, valid := ParseReportDetailLevel(string(candidate)); valid {
				level = parsed
			}
		}
		out[format] = level
	}
	return out
}

// NormalizeReportFormats filters invalid formats and preserves order.
func NormalizeReportFormats(values []string) []ReportFormat {
	seen := make(map[ReportFormat]struct{}, len(values))
	formats := make([]ReportFormat, 0, len(values))
	for _, raw := range values {
		format, ok := ParseReportFormat(raw)
		if !ok {
			continue
		}
		if _, exists := seen[format]; exists {
			continue
		}
		seen[format] = struct{}{}
		formats = append(formats, format)
	}
	return formats
}

// AllReportFormats returns the full set of supported report formats.
func AllReportFormats() []ReportFormat {
	return []ReportFormat{ReportFormatCRI, ReportFormatCRO, ReportFormatCRS, ReportFormatCRN}
}

// ReportFormatKey returns the stable storage key for a report format.
func ReportFormatKey(format ReportFormat) string {
	return strings.ToLower(strings.TrimSpace(string(format)))
}

// ReportFormatDisplayName returns the human-readable name used in logs and UI.
func ReportFormatDisplayName(format ReportFormat) string {
	switch format {
	case ReportFormatCRI:
		return "CRI"
	case ReportFormatCRO:
		return "CRO"
	case ReportFormatCRS:
		return "CRS"
	case ReportFormatCRN:
		return "CRN"
	case ReportFormatCUSTOM:
		return "CUSTOM"
	default:
		return string(format)
	}
}

func parseReportCandidate(rawOutput string) (any, error) {
	trimmed := strings.TrimSpace(rawOutput)
	if trimmed == "" {
		return nil, fmt.Errorf("%w: empty model response", ErrInvalidReport)
	}

	if fenced := extractFencedJSON(trimmed); fenced != "" {
		trimmed = fenced
	}

	for _, candidate := range buildJSONCandidates(trimmed) {
		var value any
		if err := json.Unmarshal([]byte(candidate), &value); err == nil {
			return value, nil
		}
	}

	return nil, fmt.Errorf("%w: invalid JSON response", ErrInvalidReport)
}

func buildJSONCandidates(primary string) []string {
	trimmed := strings.TrimSpace(primary)
	candidates := []string{trimmed}
	if balanced := extractFirstBalancedObject(trimmed); balanced != "" {
		candidates = append(candidates, balanced)
	}
	if first := strings.Index(trimmed, "{"); first >= 0 {
		if last := strings.LastIndex(trimmed, "}"); last > first {
			candidates = append(candidates, trimmed[first:last+1])
		}
	}

	seen := map[string]struct{}{}
	unique := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		normalized := strings.TrimSpace(candidate)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		unique = append(unique, normalized)
	}
	return unique
}

func extractFencedJSON(value string) string {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "```") {
		return ""
	}
	end := strings.LastIndex(value, "```")
	if end <= 3 {
		return ""
	}
	inner := strings.TrimSpace(value[3:end])
	if strings.HasPrefix(strings.ToLower(inner), "json") {
		inner = strings.TrimSpace(inner[4:])
	}
	return inner
}

func extractFirstBalancedObject(value string) string {
	start := strings.Index(value, "{")
	if start < 0 {
		return ""
	}

	depth := 0
	inString := false
	escaped := false
	for index := start; index < len(value); index++ {
		char := value[index]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if char == '\\' {
				escaped = true
				continue
			}
			if char == '"' {
				inString = false
			}
			continue
		}

		switch char {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return value[start : index+1]
			}
		}
	}

	return ""
}

func normalizeReport(value any, expectedFormat ReportFormat) (ReportJson, error) {
	record, ok := value.(map[string]any)
	if !ok {
		return ReportJson{}, fmt.Errorf("%w: response root must be an object", ErrInvalidReport)
	}

	formatCandidate, _ := trimString(record["format"])
	format := expectedFormat
	if _, ok := ParseReportOutputFormat(string(expectedFormat)); !ok && formatCandidate != "" {
		if parsed, ok := ParseReportOutputFormat(formatCandidate); ok {
			format = parsed
		}
	}
	if _, ok := ParseReportOutputFormat(string(format)); !ok {
		return ReportJson{}, fmt.Errorf("%w: invalid report format", ErrInvalidReport)
	}

	title, _ := trimString(record["title"])
	if title == "" {
		title = fmt.Sprintf("Compte rendu %s", ReportFormatDisplayName(format))
	}

	sectionsRaw, _ := record["sections"].([]any)
	sections := make([]ReportSection, 0, len(sectionsRaw))
	for _, item := range sectionsRaw {
		sectionMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		heading, _ := trimString(sectionMap["heading"])
		if heading == "" {
			continue
		}
		paragraphs := normalizeStringSlice(sectionMap["paragraphs"])
		if len(paragraphs) == 0 {
			continue
		}
		sections = append(sections, ReportSection{Heading: heading, Paragraphs: paragraphs})
	}
	if len(sections) == 0 {
		return ReportJson{}, fmt.Errorf("%w: no usable sections returned", ErrInvalidReport)
	}

	report := ReportJson{
		Format:   format,
		Title:    title,
		Sections: sections,
	}
	if subtitle, ok := trimString(record["subtitle"]); ok {
		report.Subtitle = subtitle
	}
	if keyPoints := normalizeStringSlice(record["key_points"]); len(keyPoints) > 0 {
		report.KeyPoints = keyPoints
	}
	if actionItems := normalizeStringSlice(record["action_items"]); len(actionItems) > 0 {
		report.ActionItems = actionItems
	}
	if caveats := normalizeStringSlice(record["caveats"]); len(caveats) > 0 {
		report.Caveats = caveats
	}
	return report, nil
}

func normalizeStringSlice(value any) []string {
	raw, ok := value.([]any)
	if !ok {
		if typed, ok := value.([]string); ok {
			out := make([]string, 0, len(typed))
			for _, entry := range typed {
				if normalized := strings.TrimSpace(entry); normalized != "" {
					out = append(out, normalized)
				}
			}
			return out
		}
		return nil
	}

	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		if normalized, ok := trimString(entry); ok {
			out = append(out, normalized)
		}
	}
	return out
}

func trimString(value any) (string, bool) {
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	normalized := strings.TrimSpace(text)
	if normalized == "" {
		return "", false
	}
	return normalized, true
}
