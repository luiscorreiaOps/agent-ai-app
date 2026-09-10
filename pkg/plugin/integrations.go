package plugin

import "context"

// Integration status values shown on the Configuration page's "Grafana
// Integrations" panel -- a green/yellow indicator for each optional "plus"
// integration this plugin actually does something with: grafana-llm-app
// (see providers.go's use of llmAppStatus/detectLLMApp) and brain-agent (see
// mcp.go's brainAgentStatus). These are purely informational for the admin;
// buildProviders/EnableBrainAgentTools each make their own pass/fail
// decision independently.
const (
	// IntegrationStatusOK: installed and confirmed working.
	IntegrationStatusOK = "ok"
	// IntegrationStatusDegraded: installed, but not usable right now (not
	// configured, misconfigured, or responding in an unexpected shape).
	IntegrationStatusDegraded = "degraded"
	// IntegrationStatusAbsent: not installed, or couldn't be reached at all.
	IntegrationStatusAbsent = "absent"
)

// IntegrationStatus describes one optional integration's live state for
// display -- id/name are stable identifiers for the frontend, enabled
// reflects the admin's own opt-in/opt-out toggle (independent of status:
// an admin can leave it disabled even when it's detected as OK).
type IntegrationStatus struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Enabled bool   `json:"enabled"`
	// Detail is an optional, specific reason for a non-OK status -- e.g.
	// distinguishing "your grafanaToken is invalid/expired" from the
	// generic "not configured" degraded case. Empty when there's nothing
	// more specific to say than the status itself.
	Detail string `json:"detail,omitempty"`
}

// integrationsStatus computes the live status of every optional "plus"
// integration this plugin actually uses, for the Configuration page:
// grafana-llm-app (see providers.go) and brain-agent (see mcp.go). OnCall,
// Incident, and SLO detection code was deliberately removed after
// implementing it without ever wiring it into anything the LLM can call:
// a status dot with no function behind it isn't worth carrying (see
// ROADMAP-INTEGRACOES-PLUS.md).
//
// Only integrations that are actually installed are returned --
// IntegrationStatusAbsent is filtered out here rather than shown as a
// "not installed" indicator, since a plugin the admin doesn't have isn't
// actionable information.
func (a *App) integrationsStatus(ctx context.Context) []IntegrationStatus {
	token := resolveGrafanaToken(a.settings)
	grafanaURL := a.toolExecutor.grafanaURL

	result := []IntegrationStatus{}

	llmAppEnabled := a.settings.EnableLLMAppIntegration == nil || *a.settings.EnableLLMAppIntegration
	if status, detail := llmAppStatus(ctx, grafanaURL, token); status != IntegrationStatusAbsent {
		result = append(result, IntegrationStatus{
			ID:      "grafana-llm-app",
			Name:    "Grafana LLM",
			Status:  status,
			Enabled: llmAppEnabled,
			Detail:  detail,
		})
	}

	brainAgentEnabled := a.settings.EnableBrainAgentTools != nil && *a.settings.EnableBrainAgentTools
	if status, detail := brainAgentStatus(ctx, grafanaURL, token); status != IntegrationStatusAbsent {
		result = append(result, IntegrationStatus{
			ID:      "shortbobcat2735-brainagent-app",
			Name:    "Brain Agent",
			Status:  status,
			Enabled: brainAgentEnabled,
			Detail:  detail,
		})
	}

	return result
}
