package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

func TestRedactSecrets(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		input  string
		secret string // the substring that must NOT survive redaction
	}{
		{"grafana service account token", "token is glsa_FAKEPLACEHOLDERTOKENFORTESTSONLY_00000000 now", "glsa_FAKEPLACEHOLDERTOKENFORTESTSONLY_00000000"},
		{"aws access key", "AKIAIOSFODNN7EXAMPLE was used", "AKIAIOSFODNN7EXAMPLE"},
		{"github token", "auth with ghp_1234567890abcdefghij1234567890abcdef", "ghp_1234567890abcdefghij1234567890abcdef"},
		{"openai-style key", "using sk-abcdefghijklmnopqrstuvwxyz123456", "sk-abcdefghijklmnopqrstuvwxyz123456"},
		{"slack token", "webhook uses xoxb-1234567890-abcdefghij", "xoxb-1234567890-abcdefghij"},
		{"bearer token", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.abcdefghijklmnop", "eyJhbGciOiJIUzI1NiJ9.abcdefghijklmnop"},
		{"pem private key", "-----BEGIN RSA PRIVATE KEY-----\nMIIB...\n-----END RSA PRIVATE KEY-----", "MIIB"},
		{"jwt", "cookie=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dQw4w9WgXcQ", "eyJzdWIiOiIxMjM0NTY3ODkwIn0"},
		{"generic api_key kv", `config: api_key="sk-liveSomeRealLookingSecretValue123"`, "sk-liveSomeRealLookingSecretValue123"},
		{"generic password kv", "db connection failed: password=Sup3rSecretPass!", "Sup3rSecretPass"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactSecrets(tc.input)
			if strings.Contains(got, tc.secret) {
				t.Errorf("redactSecrets(%q) = %q, still contains the secret %q", tc.input, got, tc.secret)
			}
			if !strings.Contains(got, redactionPlaceholder) {
				t.Errorf("redactSecrets(%q) = %q, expected it to contain %q", tc.input, got, redactionPlaceholder)
			}
		})
	}
}

func TestRedactSecrets_LeavesOrdinaryTextAlone(t *testing.T) {
	t.Parallel()

	cases := []string{
		"CPU usage is at 85% on pod api-server-7d9f",
		"error: invalid password provided by user", // mentions "password" but no value follows
		"the request timed out after 30 seconds",
		"namespace=production service=checkout",
	}

	for _, text := range cases {
		if got := redactSecrets(text); got != text {
			t.Errorf("redactSecrets(%q) = %q, want it unchanged", text, got)
		}
	}
}

// Security-audit finding H-02: a tool result that happens to contain a real
// credential (e.g. leaked into a log line or error message somewhere in
// Grafana) must never reach the LLM provider -- unlike a prompt-injection
// attempt (TestExecuteToolCalls_SuspiciousResultStillReachesModel), a
// leaked secret is worth altering the content over.
func TestExecuteToolCalls_RedactsSecretsInToolResults(t *testing.T) {
	t.Parallel()

	leaked := "glsa_FAKEPLACEHOLDERTOKENFORTESTSONLY_00000000"
	app, closeFn := newAppWithMockGrafana(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := json.Marshal(map[string]any{"error": "auth failed, token was " + leaked})
		_, _ = w.Write(body)
	})
	defer closeFn()

	calls := []openai.ToolCall{{
		ID:       "call_1",
		Type:     openai.ToolTypeFunction,
		Function: openai.FunctionCall{Name: "list_datasources", Arguments: `{}`},
	}}

	messages, err := app.executeToolCalls(context.Background(), calls, llmProvider{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("executeToolCalls failed: %v", err)
	}
	if strings.Contains(messages[0].Content, leaked) {
		t.Errorf("tool result reaching the model still contains the leaked token: %q", messages[0].Content)
	}
	if !strings.Contains(messages[0].Content, redactionPlaceholder) {
		t.Errorf("expected the redaction placeholder in the tool result, got: %q", messages[0].Content)
	}
}
