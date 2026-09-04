package plugin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	openai "github.com/sashabaranov/go-openai"
)

// Real live case this whole file is about: the user disabled the Brain
// Agent plugin mid-session and asked agent-ai-app to save a memory fact.
// The raw tool result was "Error: mcp server returned status 404" shown
// verbatim -- Grafana's own plugin resource routing 404s identically
// whether brain-agent is disabled or was never installed, so distinguishing
// those two (and giving honest, actionable guidance instead of a raw
// transport error) needs Grafana's plugin registry, not brain-agent's own
// MCP endpoint.

func TestBrainAgentInstallState_NotInstalledOn404(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	te := NewToolExecutor(server.URL, log.DefaultLogger)
	if got, _ := te.brainAgentInstallState(context.Background()); got != brainAgentNotInstalled {
		t.Errorf("brainAgentInstallState() = %v, want brainAgentNotInstalled", got)
	}
}

func TestBrainAgentInstallState_DisabledWhenSettingsReportEnabledFalse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/plugins/shortbobcat2735-brainagent-app/settings" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enabled":false}`))
	}))
	defer server.Close()

	te := NewToolExecutor(server.URL, log.DefaultLogger)
	if got, _ := te.brainAgentInstallState(context.Background()); got != brainAgentDisabled {
		t.Errorf("brainAgentInstallState() = %v, want brainAgentDisabled", got)
	}
}

func TestBrainAgentInstallState_EnabledWhenSettingsReportEnabledTrue(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/plugins/shortbobcat2735-brainagent-app/settings" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enabled":true}`))
	}))
	defer server.Close()

	te := NewToolExecutor(server.URL, log.DefaultLogger)
	if got, _ := te.brainAgentInstallState(context.Background()); got != brainAgentEnabled {
		t.Errorf("brainAgentInstallState() = %v, want brainAgentEnabled", got)
	}
}

func TestBrainAgentInstallState_ReturnsRealVersionWhenEnabledOrDisabled(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/plugins/shortbobcat2735-brainagent-app/settings" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enabled":false,"info":{"version":"1.0.0"}}`))
	}))
	defer server.Close()

	te := NewToolExecutor(server.URL, log.DefaultLogger)
	state, version := te.brainAgentInstallState(context.Background())
	if state != brainAgentDisabled {
		t.Errorf("state = %v, want brainAgentDisabled", state)
	}
	if version != "1.0.0" {
		t.Errorf("version = %q, want %q", version, "1.0.0")
	}
}

func TestBrainAgentInstallState_NoVersionWhenNotInstalled(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	te := NewToolExecutor(server.URL, log.DefaultLogger)
	_, version := te.brainAgentInstallState(context.Background())
	if version != "" {
		t.Errorf("version = %q, want empty (nothing was actually parsed)", version)
	}
}

func TestBrainAgentVersionLine_StatesRealVersionOrEmpty(t *testing.T) {
	t.Parallel()

	if line := brainAgentVersionLine("1.0.0"); !strings.Contains(line, "1.0.0") {
		t.Errorf("brainAgentVersionLine(\"1.0.0\") = %q, want it to state the version", line)
	}
	if line := brainAgentVersionLine(""); line != "" {
		t.Errorf("brainAgentVersionLine(\"\") = %q, want empty", line)
	}
}

func TestBrainAgentStatusLine_ForbidsFabricatingSuccess(t *testing.T) {
	t.Parallel()

	// Real observed live failure, worse than the raw error this whole
	// feature replaced: asked to save something to memory with no memory
	// tool available, the model skipped calling anything and just replied
	// "I've saved ... as requested" -- a confidently fabricated success.
	for _, state := range []brainAgentInstallState{brainAgentNotInstalled, brainAgentDisabled, brainAgentAuthError} {
		line := brainAgentStatusLine(state)
		if !strings.Contains(line, "Never claim to have saved") {
			t.Errorf("brainAgentStatusLine(%v) = %q, want the anti-fabrication clause", state, line)
		}
	}
}

func TestBrainAgentInstallState_AuthErrorWhenGrafanaRejectsToken(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	te := NewToolExecutor(server.URL, log.DefaultLogger)
	if got, _ := te.brainAgentInstallState(context.Background()); got != brainAgentAuthError {
		t.Errorf("brainAgentInstallState() = %v, want brainAgentAuthError", got)
	}
}

func TestBrainAgentUnavailableMessage_AuthErrorPointsAtOwnTokenNotBrainAgent(t *testing.T) {
	t.Parallel()

	msg := brainAgentUnavailableMessage(brainAgentAuthError)
	if !strings.Contains(msg, "grafanaToken") {
		t.Errorf("message = %q, want it to name grafanaToken as the fix", msg)
	}
	if strings.Contains(msg, "not installed") || strings.Contains(msg, "DISABLED") {
		t.Errorf("message = %q, must not blame Brain Agent for this plugin's own token problem", msg)
	}
}

func TestBrainAgentStatusLine_AuthErrorAlsoConditionalOnly(t *testing.T) {
	t.Parallel()

	line := brainAgentStatusLine(brainAgentAuthError)
	if !strings.Contains(line, "don't mention it unprompted") {
		t.Errorf("brainAgentStatusLine(authError) = %q, want conditional-only guidance", line)
	}
	if !strings.Contains(line, "grafanaToken") {
		t.Errorf("brainAgentStatusLine(authError) = %q, want it to point at the real fix", line)
	}
}

func TestBuildSystemPrompt_IncludesBrainAgentCapabilitiesKnowledgeUnconditionally(t *testing.T) {
	t.Parallel()

	// Unlike brainAgentStatusLine (gated on live availability), this
	// factual knowledge about Brain Agent's own UI/settings should be
	// present regardless of brainAgentState -- it's independent of whether
	// THIS assistant's own integration with it is turned on.
	prompt := buildSystemPrompt("chat", "generic", nil, false, false, nil, nil, 3, "", "", false, "", "", brainAgentStateUnknown, "")
	if !strings.Contains(prompt, "Semantic Search") {
		t.Errorf("system prompt missing Brain Agent capabilities knowledge")
	}
	if !strings.Contains(prompt, "1-2 short, plain sentences") {
		t.Errorf("system prompt missing the short-first-answer style instruction")
	}
}

func TestIsMCPUnavailableError_MatchesTransportFailuresOnly(t *testing.T) {
	t.Parallel()

	transportErrors := []error{
		errors.New("mcp server returned status 404"),
		errors.New("execute mcp request: dial tcp: connection refused"),
		errors.New("create mcp request: net/url: invalid control character"),
		errors.New("no grafana token configured"),
	}
	for _, err := range transportErrors {
		if !isMCPUnavailableError(err) {
			t.Errorf("isMCPUnavailableError(%q) = false, want true", err)
		}
	}

	// A real application-level error from brain-agent itself (or a local
	// argument-parsing error) carries actionable information and must NOT
	// be swallowed into the generic unavailability message.
	nonTransportErrors := []error{
		errors.New("mcp error: fact validation failed: value too long"),
		errors.New("parse arguments for store_memory: unexpected end of JSON input"),
		errors.New("mcp server rejected our service account token (status 401) -- grafanaToken is likely invalid"),
	}
	for _, err := range nonTransportErrors {
		if isMCPUnavailableError(err) {
			t.Errorf("isMCPUnavailableError(%q) = true, want false", err)
		}
	}
}

func TestBrainAgentUnavailableMessage_DistinguishesNotInstalledVsDisabled(t *testing.T) {
	t.Parallel()

	notInstalled := brainAgentUnavailableMessage(brainAgentNotInstalled)
	if !strings.Contains(notInstalled, "not installed") {
		t.Errorf("notInstalled message = %q, want it to say the plugin isn't installed", notInstalled)
	}
	if strings.Contains(strings.ToLower(notInstalled), "enable it") {
		t.Errorf("notInstalled message = %q, must not suggest 'enabling' something that isn't installed", notInstalled)
	}

	disabled := brainAgentUnavailableMessage(brainAgentDisabled)
	if !strings.Contains(disabled, "DISABLED") {
		t.Errorf("disabled message = %q, want it to say the plugin is disabled", disabled)
	}
	if !strings.Contains(disabled, "Administration > Plugins") {
		t.Errorf("disabled message = %q, want guidance on where to enable it", disabled)
	}
}

// Regression test for a real live-validation finding: reproduced live with
// qwen2.5:14b-instruct, asked to "remember this fact" with
// EnableBrainAgentTools off -- it confidently replied "I'll store that
// information for future reference", a fabricated success, because the
// state used to stay at brainAgentStateUnknown (not confident enough to
// correct anything). This message must point at THIS plugin's own settings,
// never at brain-agent's own Configuration/Plugins page (which may well
// already show it as installed and enabled -- that's not the issue here).
func TestBrainAgentUnavailableMessage_IntegrationOff_PointsAtOwnSettingsNotBrainAgents(t *testing.T) {
	t.Parallel()

	msg := brainAgentUnavailableMessage(brainAgentIntegrationOff)
	if !strings.Contains(msg, "Enable Brain Agent Tools") {
		t.Errorf("integrationOff message = %q, want it to name this plugin's own setting", msg)
	}
	if strings.Contains(msg, "Administration > Plugins") {
		t.Errorf("integrationOff message = %q, must NOT point at brain-agent's own Grafana plugin page -- the fix is in agent-ai-app's own settings", msg)
	}
}

func TestBrainAgentStatusLine_OnlyNotInstalledAndDisabledProduceGuidance(t *testing.T) {
	t.Parallel()

	if line := brainAgentStatusLine(brainAgentEnabled); line != "" {
		t.Errorf("brainAgentStatusLine(enabled) = %q, want empty (no caveat needed)", line)
	}
	if line := brainAgentStatusLine(brainAgentStateUnknown); line != "" {
		t.Errorf("brainAgentStatusLine(unknown) = %q, want empty (not confident enough to state anything)", line)
	}
	if line := brainAgentStatusLine(brainAgentNotInstalled); !strings.Contains(line, "don't mention it unprompted") {
		t.Errorf("brainAgentStatusLine(notInstalled) = %q, want conditional-only guidance", line)
	}
	if line := brainAgentStatusLine(brainAgentDisabled); !strings.Contains(line, "don't mention it unprompted") {
		t.Errorf("brainAgentStatusLine(disabled) = %q, want conditional-only guidance", line)
	}
	if line := brainAgentStatusLine(brainAgentIntegrationOff); !strings.Contains(line, "don't mention it unprompted") {
		t.Errorf("brainAgentStatusLine(integrationOff) = %q, want conditional-only guidance", line)
	}
	if line := brainAgentStatusLine(brainAgentIntegrationOff); strings.Contains(line, "Administration > Plugins") {
		t.Errorf("brainAgentStatusLine(integrationOff) = %q, must NOT point at brain-agent's own Grafana plugin page", line)
	}
}

func TestToolExecutor_Execute_MCPTransportFailureReturnsHonestMessageNotRawError(t *testing.T) {
	t.Parallel()

	// Simulates exactly the live incident: brain-agent's plugin resource
	// routes 404 (disabled), but its own /settings still reports it as
	// installed with enabled=false.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/plugins/shortbobcat2735-brainagent-app/settings":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"enabled":false}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	te := NewToolExecutor(server.URL, log.DefaultLogger)
	te.mcp = newMCPClient(server.URL, func() string { return "token" }, log.DefaultLogger)
	// Seed the tool cache directly (same package) instead of a real
	// tools/list round trip -- HasTool must return true for a tool this
	// client saw before brain-agent went down, matching the live scenario
	// (the cache doesn't clear the instant brain-agent is disabled).
	te.mcp.tools = []openai.Tool{{
		Type:     openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{Name: "store_memory"},
	}}

	result, err := te.Execute(context.Background(), "store_memory", `{"project":"teste2","value":"2"}`)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (a graceful message, not a hard error)", err)
	}
	if strings.Contains(result, "status 404") {
		t.Errorf("result = %q, must not contain the raw transport error", result)
	}
	if !strings.Contains(result, "DISABLED") {
		t.Errorf("result = %q, want it to say Brain Agent is disabled", result)
	}
}
