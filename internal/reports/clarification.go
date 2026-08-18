package reports

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ReportSourceKind describes the provenance and writing style of the source
// passed to a report or clarification request.
type ReportSourceKind string

const (
	ReportSourceTranscription ReportSourceKind = "transcription"
	ReportSourceWordNote      ReportSourceKind = "word_note"
	ReportSourceTextNote      ReportSourceKind = "text_note"
)

// ReportClarificationQuestion is one question that can be answered by the
// person who supplied the source note.
type ReportClarificationQuestion struct {
	ID        string `json:"id"`
	Question  string `json:"question"`
	Rationale string `json:"rationale,omitempty"`
}

// ReportClarification is the normalized response of a source analysis pass.
type ReportClarification struct {
	NeedsClarification bool                          `json:"needsClarification"`
	Summary            string                        `json:"summary,omitempty"`
	Questions          []ReportClarificationQuestion `json:"questions,omitempty"`
}

var ErrInvalidReportClarification = errors.New("invalid report clarification payload")

// NormalizeReportSourceKind returns a supported source kind and falls back to
// the historical transcription behavior for omitted or unknown values.
func NormalizeReportSourceKind(value string) ReportSourceKind {
	switch ReportSourceKind(strings.ToLower(strings.TrimSpace(value))) {
	case ReportSourceWordNote:
		return ReportSourceWordNote
	case ReportSourceTextNote:
		return ReportSourceTextNote
	default:
		return ReportSourceTranscription
	}
}

// BuildClarificationSystemPrompt builds the JSON-only system instruction used
// by the report queue before a report is generated.
func BuildClarificationSystemPrompt(sourceKind ReportSourceKind) string {
	sourceKind = NormalizeReportSourceKind(string(sourceKind))
	sourceDescription := "une transcription ASR"
	if sourceKind == ReportSourceWordNote {
		sourceDescription = "une prise de note Word très abrégée et potentiellement fragmentaire"
	} else if sourceKind == ReportSourceTextNote {
		sourceDescription = "une note texte potentiellement fragmentaire"
	}

	return strings.Join([]string{
		"Tu analyses une source avant la rédaction d'un compte rendu professionnel.",
		"La source est " + sourceDescription + ".",
		"Identifie uniquement les informations manquantes qui empêcheraient une rédaction fidèle et exploitable.",
		"Ne demande jamais une information déjà présente, même sous forme abrégée.",
		"Ne complète jamais une abréviation ambiguë et n'invente jamais de fait.",
		"Pose au maximum cinq questions concrètes, courtes et directement répondables par l'utilisateur.",
		"Retourne uniquement un objet JSON valide, sans Markdown ni commentaire autour.",
	}, "\n")
}

// BuildClarificationUserPrompt builds the user prompt for the clarification
// pass. The source itself is intentionally not logged by callers.
func BuildClarificationUserPrompt(sourceText string, sourceKind ReportSourceKind) string {
	sourceKind = NormalizeReportSourceKind(string(sourceKind))
	return strings.Join([]string{
		"Retourne exactement cette structure:",
		`{"needsClarification":true,"summary":"...","questions":[{"id":"...","question":"...","rationale":"..."}]}`,
		"Si aucune information importante ne manque, retourne needsClarification=false et questions=[].",
		"Les questions doivent couvrir en priorité le contexte, les personnes, les décisions, les actions, les échéances ou les objectifs lorsqu'ils sont réellement absents.",
		"Type de source: " + string(sourceKind),
		"SOURCE:",
		sourceText,
	}, "\n")
}

// ParseReportClarificationJSON extracts and validates the model response.
func ParseReportClarificationJSON(rawOutput string) (ReportClarification, error) {
	candidate := strings.TrimSpace(rawOutput)
	if strings.HasPrefix(candidate, "```") {
		candidate = strings.TrimPrefix(candidate, "```")
		if newline := strings.IndexByte(candidate, '\n'); newline >= 0 {
			candidate = candidate[newline+1:]
		}
		candidate = strings.TrimSuffix(strings.TrimSpace(candidate), "```")
	}

	var parsed ReportClarification
	if err := json.Unmarshal([]byte(candidate), &parsed); err != nil {
		return ReportClarification{}, fmt.Errorf("%w: %v", ErrInvalidReportClarification, err)
	}

	questions := make([]ReportClarificationQuestion, 0, len(parsed.Questions))
	seenIDs := make(map[string]struct{}, len(parsed.Questions))
	for _, question := range parsed.Questions {
		id := strings.TrimSpace(question.ID)
		text := strings.TrimSpace(question.Question)
		if id == "" || text == "" || len(questions) >= 5 {
			continue
		}
		if _, exists := seenIDs[id]; exists {
			continue
		}
		seenIDs[id] = struct{}{}
		questions = append(questions, ReportClarificationQuestion{
			ID:        id,
			Question:  text,
			Rationale: strings.TrimSpace(question.Rationale),
		})
	}

	return ReportClarification{
		NeedsClarification: len(questions) > 0,
		Summary:            strings.TrimSpace(parsed.Summary),
		Questions:          questions,
	}, nil
}
