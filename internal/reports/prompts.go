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
}

func BuildReportSystemPrompt() string {
	lines := []string{
		"Tu es un rédacteur expert des comptes rendus professionnels.",
		"Ta mission: transformer une transcription brute en compte rendu structuré selon le format demandé.",
	}
	lines = append(lines, commonPromptRules...)
	return strings.Join(lines, "\n")
}

func BuildReportUserPrompt(format ReportFormat, sourceText string, title string, participants []string) string {
	participantLine := "Aucun participant fourni."
	if len(participants) > 0 {
		participantLine = strings.Join(participants, ", ")
	}

	lines := []string{
		fmt.Sprintf("Format cible: %s.", ReportFormatDisplayName(format)),
		formatGuidelines[format],
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

func BuildReportTitle(format ReportFormat, meetingTitle string) string {
	meetingTitle = strings.TrimSpace(meetingTitle)
	if meetingTitle == "" {
		meetingTitle = "Réunion"
	}
	return fmt.Sprintf("%s - %s", ReportFormatDisplayName(format), meetingTitle)
}
