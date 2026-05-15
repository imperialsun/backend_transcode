package reports

import (
	"fmt"
	"strings"
)

var commonPromptRules = []string{
	"N'invente jamais d'informations absentes de la source.",
	"Si une information est manquante, ambiguë ou incertaine, indique-le explicitement dans `caveats`.",
	"Ne fais aucune interprétation diagnostique supplémentaire.",
	"Le texte source provient d'une transcription ASR et peut contenir des erreurs (reconnaissance, ponctuation, traduction).",
	"Corrige uniquement les erreurs manifestes quand le sens est clair; en cas de doute, conserve l'intention d'origine et signale l'incertitude dans `caveats`.",
	"Respecte strictement le format JSON demandé, sans texte avant/après.",
	"Conserve la langue française.",
}

var formatGuidelines = map[ReportFormat]string{
	ReportFormatCRI: "CRI = restitution narrative fidèle, très détaillée, avec une rédaction textuelle longue et complète.",
	ReportFormatCRO: "CRO = compte rendu opérationnel, axé décisions, actions, priorités et points à exécuter.",
	ReportFormatCRS: "CRS = synthèse ultra concise, uniquement l'essentiel critique en format très court.",
	ReportFormatCRN: "CRN = compte rendu narratif chronologique, proche d'un procès-verbal, avec ordre du jour, interventions attribuées, échanges, décisions et suites.",
}

var formatStyleRules = map[ReportFormat][]string{
	ReportFormatCRI: {
		"style narratif et textuel: développe les informations utiles dans des paragraphes complets.",
		"tu peux produire un document long (plusieurs pages) si la source contient assez de matière.",
		"niveau de détail très élevé: préserve le contexte, la chronologie, les nuances et les formulations importantes.",
		"privilégie la prose continue; n'utilise des listes que si elles sont nécessaires à la clarté.",
		"chaque section doit contenir des paragraphes substantiels (pas de phrases télégraphiques).",
		"reformulation minimale: reste très proche des mots et du sens de la transcription.",
	},
	ReportFormatCRO: {
		"style opérationnel: privilégie ce qui est actionnable et directement exploitable.",
		"structure les informations pour exécution: décisions, actions, responsables, délais (si présents).",
		"priorise les éléments décisifs en début de section.",
		"supprime le secondaire non utile à la prise de décision.",
	},
	ReportFormatCRS: {
		"style ultra synthétique: phrases courtes, sans développement narratif.",
		"ne conserve que les points critiques; élimine tout détail secondaire.",
		"vise un format très court et immédiatement lisible (résumé flash).",
		"limite le résultat à 2-3 sections courtes maximum.",
		"dans chaque section, 1 paragraphe bref (1-2 phrases) suffit.",
		"key_points et action_items doivent rester très courts (3 items max chacun).",
	},
	ReportFormatCRN: {
		"style procès-verbal narratif: raconte le déroulé de la réunion dans l'ordre chronologique.",
		"considère la source comme une transcription potentiellement fragmentée en chunks: ne déduis jamais un début ou une fin de réunion à partir d'une coupure.",
		"n'utilise pas de formules génériques d'ouverture ou de reprise du type 'La réunion a débuté...' sauf si la source les formule explicitement.",
		"enchaîne les idées avec des transitions naturelles et fusionne les répétitions entre chunks au lieu de les réintroduire.",
		"si un ordre du jour, des points numérotés ou des titres de sujets sont détectables, conserve cette structure et ses numéros.",
		"attribue les interventions aux personnes ou groupes mentionnés quand la source le permet: Mme X indique, M. Y précise, un représentant demande.",
		"reformule en prose continue les échanges, positions, objections, réponses et décisions, sans transformer le compte rendu en liste de synthèse.",
		"consigne les décisions, arbitrages, demandes de vérification et suites à donner dans le fil narratif du sujet concerné.",
		"préserve les nuances, désaccords, incertitudes et formulations importantes; n'ajoute aucune position absente de la transcription.",
		"utilise des paragraphes substantiels et évite les puces sauf pour restituer une liste explicitement énoncée dans la source.",
		"vise un document long et exploitable comme compte rendu formel de réunion.",
	},
}

var detailLevelGuidelines = map[ReportDetailLevel]string{
	ReportDetailStandard:   "standard = complet mais compact, sans superflu inutile.",
	ReportDetailVerbose:    "verbeux = sensiblement plus developpe, avec davantage de contexte et de precisions.",
	ReportDetailExhaustive: "exhaustif = le plus long et le plus detaille, avec expansion claire du contexte, des interlocuteurs nommes, des opinions et des points de vigilance.",
}

var detailLevelLabels = map[ReportDetailLevel]string{
	ReportDetailStandard:   "Standard",
	ReportDetailVerbose:    "Verbeux",
	ReportDetailExhaustive: "Exhaustif",
}

var detailMinimumSourceRatios = map[ReportFormat]map[ReportDetailLevel]float64{
	ReportFormatCRI: {
		ReportDetailStandard:   0.05,
		ReportDetailVerbose:    0.10,
		ReportDetailExhaustive: 0.15,
	},
	ReportFormatCRO: {
		ReportDetailStandard:   0.025,
		ReportDetailVerbose:    0.05,
		ReportDetailExhaustive: 0.075,
	},
	ReportFormatCRS: {
		ReportDetailStandard:   0.0125,
		ReportDetailVerbose:    0.025,
		ReportDetailExhaustive: 0.0375,
	},
	ReportFormatCRN: {
		ReportDetailStandard:   0.4,
		ReportDetailVerbose:    0.5,
		ReportDetailExhaustive: 0.6,
	},
}

func detailMinimumWords(format ReportFormat, detailLevel ReportDetailLevel, sourceText string) int {
	minimumRatio := detailMinimumSourceRatios[format][detailLevel]
	if sourceWordCount := len(strings.Fields(strings.TrimSpace(sourceText))); sourceWordCount > 0 && minimumRatio > 0 {
		return int(float64(sourceWordCount)*minimumRatio + 0.5)
	}
	return 0
}

// BuildReportSystemPrompt returns the system prompt that constrains report
// generation to the backend's structured format.
func BuildReportSystemPrompt() string {
	return BuildReportSystemPromptWithDetail("")
}

// BuildReportSystemPromptWithDetail returns the system prompt with the same
// detail-priority rules used by the frontend when a level is selected.
func BuildReportSystemPromptWithDetail(detailLevel ReportDetailLevel) string {
	lines := []string{
		"Tu es un rédacteur expert des comptes rendus professionnels.",
		"Ta mission: transformer une transcription brute en compte rendu structuré selon le format demandé.",
	}
	lines = append(lines, commonPromptRules...)
	if parsed, ok := ParseReportDetailLevel(string(detailLevel)); ok {
		lines = append(lines,
			fmt.Sprintf("Niveau de detail actif: %s.", detailLevelLabels[parsed]),
			"La contrainte de longueur associee est prioritaire: considere-la comme une base minimale, un minimum obligatoire, pas comme une moyenne ni un plafond.",
			"Respecte cette contrainte avant toute recherche de concision.",
		)
	}
	return strings.Join(lines, "\n")
}

// BuildReportUserPrompt assembles the user prompt that gives the model the
// transcript context and the requested report format.
func BuildReportUserPrompt(format ReportFormat, sourceText string, title string, participants []string) string {
	return BuildReportUserPromptWithDetail(format, ReportDetailStandard, sourceText, title, participants)
}

// BuildReportUserPromptWithDetail assembles the user prompt with the requested
// detail level, mirroring the frontend's standard/verbose/exhaustive behavior.
func BuildReportUserPromptWithDetail(format ReportFormat, detailLevel ReportDetailLevel, sourceText string, title string, participants []string) string {
	participantLine := "Aucun participant fourni."
	if len(participants) > 0 {
		participantLine = strings.Join(participants, ", ")
	}
	if parsed, ok := ParseReportDetailLevel(string(detailLevel)); ok {
		detailLevel = parsed
	} else {
		detailLevel = ReportDetailStandard
	}
	minimumWords := detailMinimumWords(format, detailLevel, sourceText)

	lines := []string{
		fmt.Sprintf("Format cible: %s.", ReportFormatDisplayName(format)),
		formatGuidelines[format],
		detailLevelGuidelines[detailLevel],
		"",
		"Titre de la réunion:",
		title,
		"",
		"Participants:",
		participantLine,
		"",
		"Retourne uniquement un JSON valide avec cette structure:",
		fmt.Sprintf(`{
  "format": "%s",
  "title": "...",
  "subtitle": "... (optionnel)",
  "sections": [
    { "heading": "...", "paragraphs": ["...", "..."] }
  ],
  "key_points": ["..."],
  "action_items": ["..."],
  "caveats": ["..."]
}`, ReportFormatDisplayName(format)),
		"",
		"Contraintes de contenu:",
		"- sections: ordre logique, titres clairs.",
		"- key_points: points saillants utiles à la lecture rapide.",
		"- action_items: suites concrètes si explicites dans la source.",
		"- caveats: zones d'incertitude / informations absentes.",
	}
	if minimumWords > 0 {
		wordLabel := "mots"
		if minimumWords == 1 {
			wordLabel = "mot"
		}
		lines = append(lines,
			"",
			"Consigne prioritaire de longueur:",
			fmt.Sprintf("- longueur minimale obligatoire (%s): vise au moins %d %s en prenant la quantite demandee comme base minimale sur la transcription source.", detailLevelLabels[detailLevel], minimumWords, wordLabel),
			"- cette limite est un minimum, pas un plafond.",
			"- tu peux depasser cette longueur sans probleme si cela ameliore la fidelite, le contexte, les noms cites ou les nuances; ne compresse pas le texte pour rester court.",
			"- progression attendue: "+detailLevelGuidelines[detailLevel],
			"- si des interlocuteurs sont nommes, cite leurs noms et leur avis ou position lorsqu'elle est exprimee.",
		)
	}
	for _, rule := range formatStyleRules[format] {
		lines = append(lines, "- "+rule)
	}
	lines = append(lines,
		"",
		"SOURCE:",
		sourceText,
	)
	return strings.Join(lines, "\n")
}

// BuildCustomReportUserPromptWithDetail assembles the same structured report
// contract as built-in formats while injecting organization-authored guidance.
func BuildCustomReportUserPromptWithDetail(format ReportFormat, detailLevel ReportDetailLevel, sourceText string, title string, participants []string, templateName string, instructions string, exampleOutline string) string {
	if format == ReportFormatCUSTOM {
		return BuildFreeCustomReportUserPromptWithDetail(detailLevel, sourceText, title, participants, templateName, instructions, exampleOutline)
	}
	basePrompt := BuildReportUserPromptWithDetail(format, detailLevel, sourceText, title, participants)
	customLines := []string{
		"",
		"MODELE PERSONNALISE ORGANISATION:",
		fmt.Sprintf("Nom du modele: %s.", strings.TrimSpace(templateName)),
		"Consignes specifiques prioritaires:",
		strings.TrimSpace(instructions),
	}
	if strings.TrimSpace(exampleOutline) != "" {
		customLines = append(customLines,
			"",
			"Structure ou exemple attendu par l'organisation:",
			strings.TrimSpace(exampleOutline),
		)
	}
	customLines = append(customLines,
		"",
		"Respecte ces consignes personnalisees tant qu'elles ne contredisent pas les regles de securite, de fidelite a la source et le schema JSON impose.",
	)
	return strings.Replace(basePrompt, "\nSOURCE:\n"+sourceText, strings.Join(customLines, "\n")+"\n\nSOURCE:\n"+sourceText, 1)
}

// BuildFreeCustomReportUserPromptWithDetail assembles a structured report
// prompt for organization-authored templates that are not based on CRI/CRO/CRS
// or CRN.
func BuildFreeCustomReportUserPromptWithDetail(detailLevel ReportDetailLevel, sourceText string, title string, participants []string, templateName string, instructions string, exampleOutline string) string {
	participantLine := "Aucun participant fourni."
	if len(participants) > 0 {
		participantLine = strings.Join(participants, ", ")
	}
	if parsed, ok := ParseReportDetailLevel(string(detailLevel)); ok {
		detailLevel = parsed
	} else {
		detailLevel = ReportDetailStandard
	}
	lines := []string{
		"Format cible: CUSTOM.",
		"Ce modèle est libre: ne t'appuie sur aucune structure CRI, CRO, CRS ou CRN.",
		detailLevelGuidelines[detailLevel],
		"",
		"Titre de la réunion:",
		title,
		"",
		"Participants:",
		participantLine,
		"",
		"MODELE PERSONNALISE ORGANISATION:",
		fmt.Sprintf("Nom du modele: %s.", strings.TrimSpace(templateName)),
		"Consignes specifiques prioritaires:",
		strings.TrimSpace(instructions),
	}
	if strings.TrimSpace(exampleOutline) != "" {
		lines = append(lines,
			"",
			"Structure ou exemple attendu par l'organisation:",
			strings.TrimSpace(exampleOutline),
		)
	}
	lines = append(lines,
		"",
		"Retourne uniquement un JSON valide avec cette structure:",
		`{
  "format": "CUSTOM",
  "title": "...",
  "subtitle": "... (optionnel)",
  "sections": [
    { "heading": "...", "paragraphs": ["...", "..."] }
  ],
  "key_points": ["..."],
  "action_items": ["..."],
  "caveats": ["..."]
}`,
		"",
		"Contraintes de contenu:",
		"- applique les consignes du modèle comme structure principale du compte rendu.",
		"- sections: titres clairs et ordre conforme au modèle si une structure est fournie.",
		"- key_points: points saillants utiles à la lecture rapide.",
		"- action_items: suites concrètes si explicites dans la source.",
		"- caveats: zones d'incertitude / informations absentes.",
		"- respecte ces consignes personnalisees tant qu'elles ne contredisent pas les regles de securite, de fidelite a la source et le schema JSON impose.",
		"",
		"SOURCE:",
		sourceText,
	)
	return strings.Join(lines, "\n")
}

// BuildReportTitle returns the display title used for the exported document.
func BuildReportTitle(format ReportFormat, meetingTitle string) string {
	meetingTitle = strings.TrimSpace(meetingTitle)
	if meetingTitle == "" {
		meetingTitle = "Réunion"
	}
	return fmt.Sprintf("%s - %s", ReportFormatDisplayName(format), meetingTitle)
}
