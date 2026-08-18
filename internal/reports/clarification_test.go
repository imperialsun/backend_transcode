package reports

import "testing"

func TestParseReportClarificationJSONNormalizesQuestions(t *testing.T) {
	clarification, err := ParseReportClarificationJSON("```json\n{\"needsClarification\":true,\"summary\":\"Contexte incomplet\",\"questions\":[{\"id\":\"participants\",\"question\":\"Qui était présent ?\"},{\"id\":\"participants\",\"question\":\"Doublon\"}]}\n```")
	if err != nil {
		t.Fatalf("expected clarification to parse: %v", err)
	}
	if !clarification.NeedsClarification || clarification.Summary != "Contexte incomplet" {
		t.Fatalf("unexpected clarification: %+v", clarification)
	}
	if len(clarification.Questions) != 1 || clarification.Questions[0].ID != "participants" {
		t.Fatalf("expected one normalized question, got %+v", clarification.Questions)
	}
}

func TestParseReportClarificationJSONRejectsInvalidJSON(t *testing.T) {
	if _, err := ParseReportClarificationJSON(`{"needsClarification":true`); err == nil {
		t.Fatal("expected invalid clarification JSON to fail")
	}
}

func TestBuildClarificationPromptsMentionWordNoteSafety(t *testing.T) {
	prompt := BuildClarificationSystemPrompt(ReportSourceWordNote)
	if prompt == "" || !containsAll(prompt, "prise de note Word très abrégée", "n'invente jamais de fait") {
		t.Fatalf("expected conservative Word note prompt, got %s", prompt)
	}
}

func containsAll(value string, fragments ...string) bool {
	for _, fragment := range fragments {
		if !contains(value, fragment) {
			return false
		}
	}
	return true
}

func contains(value, fragment string) bool {
	for i := 0; i+len(fragment) <= len(value); i++ {
		if value[i:i+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
