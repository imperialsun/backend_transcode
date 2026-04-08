package reports

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
	"time"
)

const docxMimeType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

// ReportDocxMetadata carries optional descriptive fields that are embedded in
// the generated report document.
type ReportDocxMetadata struct {
	Format           ReportFormat
	ModelID          string
	GeneratedAt      string
	SourceMode       string
	SourceTokenCount int
}

// TranscriptDocxMetadata carries optional descriptive fields that are embedded
// in the transcript export.
type TranscriptDocxMetadata struct {
	Title            string
	GeneratedAt      string
	SourceMode       string
	SourceTokenCount int
}

// docxParagraph represents a single paragraph in the minimal DOCX builder.
type docxParagraph struct {
	Text        string
	Bold        bool
	Center      bool
	Right       bool
	SizeHalfPt  int
	SpaceBefore int
	SpaceAfter  int
}

// BuildReportDocx renders a generated report into a standalone DOCX archive.
func BuildReportDocx(report ReportJson, metadata ReportDocxMetadata) ([]byte, error) {
	generatedAt := metadata.GeneratedAt
	if generatedAt == "" {
		generatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	timestamp := generatedAt
	if parsed, err := time.Parse(time.RFC3339, generatedAt); err == nil {
		timestamp = parsed.UTC().Format("2006-01-02 15:04 UTC")
	}

	paragraphs := []docxParagraph{
		{Text: report.Title, Bold: true, SizeHalfPt: 28, SpaceAfter: 120},
	}
	if strings.TrimSpace(report.Subtitle) != "" {
		paragraphs = append(paragraphs, docxParagraph{Text: report.Subtitle, Bold: true, SizeHalfPt: 22, SpaceAfter: 120})
	}
	paragraphs = append(paragraphs,
		docxParagraph{Text: fmt.Sprintf("Format: %s | Généré le: %s", ReportFormatDisplayName(metadata.Format), timestamp), SpaceAfter: 80},
		docxParagraph{Text: fmt.Sprintf("Mode source: %s | Modèle: %s | Tokens source: %d", metadata.SourceMode, metadata.ModelID, metadata.SourceTokenCount), SpaceAfter: 240},
	)

	for _, section := range report.Sections {
		paragraphs = append(paragraphs,
			docxParagraph{Text: section.Heading, Bold: true, SizeHalfPt: 24, SpaceBefore: 120, SpaceAfter: 80},
		)
		for _, paragraph := range section.Paragraphs {
			paragraphs = append(paragraphs, docxParagraph{Text: paragraph, SpaceAfter: 60})
		}
	}

	appendBulletSection := func(title string, entries []string) {
		if len(entries) == 0 {
			return
		}
		paragraphs = append(paragraphs,
			docxParagraph{Text: title, Bold: true, SizeHalfPt: 24, SpaceBefore: 120, SpaceAfter: 80},
		)
		for _, entry := range entries {
			paragraphs = append(paragraphs, docxParagraph{Text: "• " + entry, SpaceAfter: 40})
		}
	}

	appendBulletSection("Points cles", report.KeyPoints)
	appendBulletSection("Actions", report.ActionItems)
	appendBulletSection("Points de vigilance", report.Caveats)

	title := fmt.Sprintf("%s - %s", ReportFormatDisplayName(metadata.Format), report.Title)
	return buildDocx(title, paragraphs, map[string]string{
		"creator":      "Demeter Speech",
		"description":  "Compte rendu genere par Demeter backend",
		"language":     "fr-FR",
		"lastModified": timestamp,
	})
}

// BuildTranscriptDocx renders a transcript export into a standalone DOCX
// archive.
func BuildTranscriptDocx(title string, participants []string, sourceMode string, transcript string, segments []TranscriptSegment, metadata TranscriptDocxMetadata) ([]byte, error) {
	if strings.TrimSpace(title) == "" {
		title = "Réunion"
	}
	generatedAt := metadata.GeneratedAt
	if generatedAt == "" {
		generatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	timestamp := generatedAt
	if parsed, err := time.Parse(time.RFC3339, generatedAt); err == nil {
		timestamp = parsed.UTC().Format("2006-01-02 15:04 UTC")
	}

	paragraphs := []docxParagraph{
		{Text: "Transcription brute", Bold: true, SizeHalfPt: 28, SpaceAfter: 120},
		{Text: title, Bold: true, SizeHalfPt: 22, SpaceAfter: 120},
		{Text: fmt.Sprintf("Mode source: %s | Généré le: %s", sourceMode, timestamp), SpaceAfter: 80},
	}
	if len(participants) > 0 {
		paragraphs = append(paragraphs, docxParagraph{Text: "Participants: " + strings.Join(participants, ", "), SpaceAfter: 180})
	}
	if len(segments) > 0 {
		for _, segment := range segments {
			speaker := strings.TrimSpace(segment.SpeakerName)
			if speaker == "" {
				speaker = strings.TrimSpace(segment.SpeakerID)
			}
			text := strings.TrimSpace(segment.Text)
			if text == "" {
				continue
			}
			if speaker != "" {
				paragraphs = append(paragraphs, docxParagraph{Text: speaker + ": " + text, SpaceAfter: 40})
			} else {
				paragraphs = append(paragraphs, docxParagraph{Text: text, SpaceAfter: 40})
			}
		}
	} else {
		lines := splitTranscriptIntoParagraphs(transcript)
		for _, line := range lines {
			paragraphs = append(paragraphs, docxParagraph{Text: line, SpaceAfter: 40})
		}
	}

	return buildDocx("Transcription brute - "+title, paragraphs, map[string]string{
		"creator":      "Demeter Speech",
		"description":  "Transcription brute generee par Demeter backend",
		"language":     "fr-FR",
		"lastModified": timestamp,
	})
}

// TranscriptDocxFilename builds the canonical filename for transcript exports.
func TranscriptDocxFilename(date time.Time) string {
	return fmt.Sprintf("transcription-brute-%04d-%02d-%02d-%02d%02d.docx", date.Year(), date.Month(), date.Day(), date.Hour(), date.Minute())
}

// ReportDocxFilename builds the canonical filename for report exports.
func ReportDocxFilename(formatKey string, date time.Time) string {
	return fmt.Sprintf("rapport-%s-%04d-%02d-%02d-%02d%02d.docx", strings.ToLower(strings.TrimSpace(formatKey)), date.Year(), date.Month(), date.Day(), date.Hour(), date.Minute())
}

// buildDocx assembles the ZIP container and document XML for the minimal DOCX
// representation.
func buildDocx(title string, paragraphs []docxParagraph, meta map[string]string) ([]byte, error) {
	var buffer bytes.Buffer
	zipWriter := zip.NewWriter(&buffer)
	write := func(name string, data []byte) error {
		entry, err := zipWriter.Create(name)
		if err != nil {
			return err
		}
		_, err = entry.Write(data)
		return err
	}

	creator := meta["creator"]
	if creator == "" {
		creator = "Demeter Speech"
	}
	description := meta["description"]
	if description == "" {
		description = title
	}
	language := meta["language"]
	if language == "" {
		language = "fr-FR"
	}
	lastModified := meta["lastModified"]
	if lastModified == "" {
		lastModified = time.Now().UTC().Format(time.RFC3339)
	}

	if err := write("[Content_Types].xml", []byte(contentTypesXML())); err != nil {
		_ = zipWriter.Close()
		return nil, err
	}
	if err := write("_rels/.rels", []byte(rootRelationshipsXML())); err != nil {
		_ = zipWriter.Close()
		return nil, err
	}
	if err := write("word/_rels/document.xml.rels", []byte(documentRelationshipsXML())); err != nil {
		_ = zipWriter.Close()
		return nil, err
	}
	if err := write("docProps/core.xml", []byte(corePropertiesXML(creator, title, description, language, lastModified))); err != nil {
		_ = zipWriter.Close()
		return nil, err
	}
	if err := write("docProps/app.xml", []byte(appPropertiesXML())); err != nil {
		_ = zipWriter.Close()
		return nil, err
	}
	if err := write("word/document.xml", []byte(buildDocumentXML(paragraphs))); err != nil {
		_ = zipWriter.Close()
		return nil, err
	}
	if err := zipWriter.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// buildDocumentXML converts the paragraph model into the main DOCX document.
func buildDocumentXML(paragraphs []docxParagraph) string {
	var builder strings.Builder
	builder.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	builder.WriteString(`<w:document xmlns:wpc="http://schemas.microsoft.com/office/word/2010/wordprocessingCanvas" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006" xmlns:o="urn:schemas-microsoft-com:office:office" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:m="http://schemas.openxmlformats.org/officeDocument/2006/math" xmlns:v="urn:schemas-microsoft-com:vml" xmlns:wp14="http://schemas.microsoft.com/office/word/2010/wordprocessingDrawing" xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing" xmlns:w10="urn:schemas-microsoft-com:office:word" xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:w14="http://schemas.microsoft.com/office/word/2010/wordml" xmlns:wpg="http://schemas.microsoft.com/office/word/2010/wordprocessingGroup" xmlns:wpi="http://schemas.microsoft.com/office/word/2010/wordprocessingInk" xmlns:wne="http://schemas.microsoft.com/office/word/2006/wordml" xmlns:wps="http://schemas.microsoft.com/office/word/2010/wordprocessingShape" mc:Ignorable="w14 wp14">`)
	builder.WriteString(`<w:body>`)
	for _, paragraph := range paragraphs {
		builder.WriteString(buildParagraphXML(paragraph))
	}
	builder.WriteString(`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440" w:header="708" w:footer="708" w:gutter="0"/></w:sectPr>`)
	builder.WriteString(`</w:body></w:document>`)
	return builder.String()
}

// buildParagraphXML renders one paragraph, preserving bold and heading styles.
func buildParagraphXML(paragraph docxParagraph) string {
	var builder strings.Builder
	builder.WriteString(`<w:p>`)
	builder.WriteString(`<w:pPr>`)
	if paragraph.Center {
		builder.WriteString(`<w:jc w:val="center"/>`)
	} else if paragraph.Right {
		builder.WriteString(`<w:jc w:val="right"/>`)
	}
	if paragraph.SpaceBefore > 0 || paragraph.SpaceAfter > 0 {
		builder.WriteString(`<w:spacing`)
		if paragraph.SpaceBefore > 0 {
			fmt.Fprintf(&builder, ` w:before="%d"`, paragraph.SpaceBefore)
		}
		if paragraph.SpaceAfter > 0 {
			fmt.Fprintf(&builder, ` w:after="%d"`, paragraph.SpaceAfter)
		}
		builder.WriteString(`/>`)
	}
	builder.WriteString(`</w:pPr>`)
	builder.WriteString(`<w:r>`)
	builder.WriteString(`<w:rPr>`)
	if paragraph.Bold {
		builder.WriteString(`<w:b/>`)
	}
	if paragraph.SizeHalfPt > 0 {
		fmt.Fprintf(&builder, `<w:sz w:val="%d"/><w:szCs w:val="%d"/>`, paragraph.SizeHalfPt, paragraph.SizeHalfPt)
	}
	builder.WriteString(`</w:rPr>`)
	builder.WriteString(`<w:t xml:space="preserve">`)
	builder.WriteString(xmlEscape(paragraph.Text))
	builder.WriteString(`</w:t>`)
	builder.WriteString(`</w:r>`)
	builder.WriteString(`</w:p>`)
	return builder.String()
}

// splitTranscriptIntoParagraphs preserves speaker breaks while normalizing raw
// transcript text.
func splitTranscriptIntoParagraphs(transcript string) []string {
	normalized := strings.ReplaceAll(strings.TrimSpace(transcript), "\r\n", "\n")
	if normalized == "" {
		return []string{"Transcription vide."}
	}
	rawLines := strings.Split(normalized, "\n")
	paragraphs := make([]string, 0, len(rawLines))
	for _, rawLine := range rawLines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		paragraphs = append(paragraphs, line)
	}
	if len(paragraphs) == 0 {
		return []string{"Transcription vide."}
	}
	return paragraphs
}

// xmlEscape escapes text nodes for the minimal DOCX XML documents.
func xmlEscape(value string) string {
	var buffer bytes.Buffer
	_ = xml.EscapeText(&buffer, []byte(value))
	return buffer.String()
}

// contentTypesXML returns the static DOCX content types manifest.
func contentTypesXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
  <Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>
  <Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>
</Types>`
}

// rootRelationshipsXML returns the static DOCX root relationships manifest.
func rootRelationshipsXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>
  <Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/>
</Relationships>`
}

// documentRelationshipsXML returns the static DOCX relationships manifest for
// the main document part.
func documentRelationshipsXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"></Relationships>`
}

// corePropertiesXML renders the metadata block stored in the DOCX package.
func corePropertiesXML(creator, title, description, language, lastModified string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:dcmitype="http://purl.org/dc/dcmitype/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <dc:creator>%s</dc:creator>
  <cp:lastModifiedBy>%s</cp:lastModifiedBy>
  <dc:title>%s</dc:title>
  <dc:description>%s</dc:description>
  <dc:language>%s</dc:language>
  <dcterms:modified xsi:type="dcterms:W3CDTF">%s</dcterms:modified>
</cp:coreProperties>`, xmlEscape(creator), xmlEscape(creator), xmlEscape(title), xmlEscape(description), xmlEscape(language), xmlEscape(lastModified))
}

// appPropertiesXML returns the static application-properties block required by
// the DOCX container.
func appPropertiesXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties" xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes">
  <Application>Demeter Speech</Application>
</Properties>`
}
