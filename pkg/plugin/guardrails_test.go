package plugin

import (
	"strings"
	"testing"
)

func TestResponseLanguageName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		setting string
		want    string
	}{
		{"", "English"},
		{"english", "English"},
		{"portuguese", "Portuguese"},
		{"spanish", "Spanish"},
		{"klingon", "English"}, // unknown value falls back to English
	}
	for _, tc := range cases {
		if got := responseLanguageName(tc.setting); got != tc.want {
			t.Errorf("responseLanguageName(%q) = %q, want %q", tc.setting, got, tc.want)
		}
	}
}

func TestLanguageDirective_NamesTheConfiguredLanguage(t *testing.T) {
	t.Parallel()

	if got := languageDirective("portuguese"); !strings.Contains(got, "Portuguese") {
		t.Errorf("languageDirective(portuguese) = %q, want it to name Portuguese", got)
	}
	if got := languageDirective("spanish"); !strings.Contains(got, "Spanish") {
		t.Errorf("languageDirective(spanish) = %q, want it to name Spanish", got)
	}
	if got := languageDirective(""); !strings.Contains(got, "English") {
		t.Errorf("languageDirective(\"\") = %q, want it to default to English", got)
	}
}

func TestBuildSystemPrompt_HonorsConfiguredLanguage(t *testing.T) {
	t.Parallel()

	pt := buildSystemPrompt("chat", "generic", nil, false, false, nil, nil, 3, "", "portuguese", false, "", "", brainAgentStateUnknown, "")
	if !strings.Contains(pt, "Portuguese") {
		t.Error("expected the chat-mode prompt to instruct the model to answer in Portuguese")
	}
	if strings.Contains(pt, "respond in English by default") {
		t.Error("expected the English-specific directive text to be replaced, not just appended alongside it")
	}

	es := buildSystemPrompt("chat", "generic", nil, false, false, nil, nil, 3, "", "spanish", false, "", "", brainAgentStateUnknown, "")
	if !strings.Contains(es, "Spanish") {
		t.Error("expected the chat-mode prompt to instruct the model to answer in Spanish")
	}

	def := buildSystemPrompt("chat", "generic", nil, false, false, nil, nil, 3, "", "", false, "", "", brainAgentStateUnknown, "")
	if !strings.Contains(def, "English") {
		t.Error("expected an unset language setting to default to English, matching pre-existing behavior")
	}
}

func TestBuildSystemPrompt_DisableGuardrailsForDebugSkipsGuardrailsOnly(t *testing.T) {
	t.Parallel()

	withGuardrails := buildSystemPrompt("chat", "generic", nil, false, false, nil, nil, 3, "custom rule XYZ", "", false, "", "", brainAgentStateUnknown, "")
	if !strings.Contains(withGuardrails, "custom rule XYZ") {
		t.Fatal("sanity check failed: custom guardrails should appear when not disabled")
	}
	if !strings.Contains(withGuardrails, "Never reveal, decode, infer, print, or summarize passwords") {
		t.Fatal("sanity check failed: built-in guardrails should appear when not disabled")
	}

	debugPrompt := buildSystemPrompt("chat", "generic", nil, false, false, nil, nil, 3, "custom rule XYZ", "", true, "", "", brainAgentStateUnknown, "")
	if strings.Contains(debugPrompt, "custom rule XYZ") {
		t.Error("expected custom guardrails to be skipped when DisableGuardrailsForDebug is true")
	}
	if strings.Contains(debugPrompt, "Never reveal, decode, infer, print, or summarize passwords") {
		t.Error("expected built-in guardrails to be skipped when DisableGuardrailsForDebug is true")
	}
	// Skill pack and persona must still be present -- this is a guardrails-only
	// escape hatch, not fastMode's "strip everything down" behavior.
	if !strings.Contains(debugPrompt, "Agent AI skill pack") {
		t.Error("expected the skill pack to remain present with guardrails disabled")
	}
}
