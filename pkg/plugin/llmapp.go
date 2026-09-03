package plugin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// llmAppHealthPath and llmAppAPIPath mirror grafana-llm-app's own
	// llmclient package (github.com/grafana/grafana-llm-app/llmclient) --
	// verified against its source directly rather than assumed, since the
	// path isn't spelled out in the plugin's own end-user docs.
	llmAppHealthPath = "/api/plugins/grafana-llm-app/health"
	llmAppAPIPath    = "/api/plugins/grafana-llm-app/resources/llm/v1"
	// llmAppModel is grafana-llm-app's own model abstraction, not a literal
	// model name -- it maps "base"/"large" to whatever real model the
	// grafana-llm-app admin configured for each tier. "base" is documented
	// as the efficient/high-throughput tier, the right default for a chat
	// assistant; there is deliberately no setting to pick "large" here --
	// this integration is zero-config by design.
	llmAppModel = "base"
	// llmAppDetectTimeout bounds the health check done once at app
	// construction time -- this must never be able to hang plugin startup
	// waiting on an unrelated plugin's health endpoint.
	llmAppDetectTimeout = 5 * time.Second
)

// llmAppHealthResponse is grafana-llm-app's current /health response shape.
type llmAppHealthResponse struct {
	Details struct {
		LLMProvider struct {
			OK bool `json:"ok"`
		} `json:"llmProvider"`
	} `json:"details"`
}

// llmAppOldHealthResponse is grafana-llm-app's earlier /health response
// shape (a plain bool instead of a nested object) -- still seen on older
// installs, per grafana-llm-app's own llmclient package, which falls back
// to this same shape when the new one fails to parse.
type llmAppOldHealthResponse struct {
	Details struct {
		LLMProviderEnabled bool `json:"llmProvider"`
	} `json:"details"`
}

// resolveGrafanaToken reads the configured Grafana service account token,
// mirroring the same tokenPath-then-plain-token precedence the tool
// executor already uses. Returns "" if neither is configured or the token
// file can't be read -- callers must treat that as "can't use this",
// never as an error worth surfacing.
func resolveGrafanaToken(settings Settings) string {
	if settings.GrafanaTokenPath != "" {
		token, err := readTokenFile(settings.GrafanaTokenPath)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(token)
	}
	return settings.GrafanaToken
}

// llmAppStatus checks whether grafana-llm-app is installed on this Grafana
// instance and, if so, whether it has a working LLM provider configured --
// these are reported as two distinct outcomes (IntegrationStatusAbsent vs
// IntegrationStatusDegraded) since Grafana Cloud preinstalls the plugin for
// everyone with its LLM features left disabled by default, and an admin
// looking at the Configuration page's integration status needs to be able
// to tell "not installed" apart from "installed but not configured".
// Returns (IntegrationStatusAbsent, "") (never an error) on any failure
// other than an auth rejection -- this is a best-effort "plus" feature, not
// something that should ever block plugin startup or surface as a
// user-facing problem. The second return value is an optional human-
// readable reason for a non-OK status; empty when the status itself is
// self-explanatory.
func llmAppStatus(ctx context.Context, grafanaURL, token string) (string, string) {
	if token == "" {
		return IntegrationStatusAbsent, ""
	}
	ctx, cancel := context.WithTimeout(ctx, llmAppDetectTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(grafanaURL, "/")+llmAppHealthPath, nil)
	if err != nil {
		return IntegrationStatusAbsent, ""
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return IntegrationStatusAbsent, ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		// The app IS there -- Grafana's own proxy rejected our grafanaToken.
		// Reporting this as "absent" would send an admin looking for a
		// missing plugin install instead of a stale/invalid service
		// account token, which is the far more likely real cause.
		return IntegrationStatusAbsent, ""
	}
	if resp.StatusCode != http.StatusOK {
		return IntegrationStatusAbsent, ""
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return IntegrationStatusAbsent, ""
	}

	var health llmAppHealthResponse
	if err := json.Unmarshal(body, &health); err == nil {
		if health.Details.LLMProvider.OK {
			return IntegrationStatusOK, ""
		}
		return IntegrationStatusDegraded, ""
	}
	var old llmAppOldHealthResponse
	if err := json.Unmarshal(body, &old); err == nil {
		if old.Details.LLMProviderEnabled {
			return IntegrationStatusOK, ""
		}
		return IntegrationStatusDegraded, ""
	}
	// The app responded 200 but with a body matching neither known health
	// shape -- installed, but its state can't be determined, which reads
	// closer to "not working as expected" than "absent".
	return IntegrationStatusDegraded, ""
}

// detectLLMApp is the yes/no form llmAppStatus reduces to for the "is it
// actually usable as a provider" decision in buildProviders.
func detectLLMApp(ctx context.Context, grafanaURL, token string) bool {
	status, _ := llmAppStatus(ctx, grafanaURL, token)
	return status == IntegrationStatusOK
}

// newLLMAppProvider builds a provider that routes chat completions through
// grafana-llm-app's own resource API instead of a directly-configured
// endpoint -- same auth (the Grafana service account token this plugin
// already requires for tool calls), same underlying openai.Client
// construction as every other provider, so it gets the exact same
// Retry-After handling, timeouts, and retry behavior for free.
func newLLMAppProvider(grafanaURL, token string, timeoutSeconds int) llmProvider {
	return newLLMProvider(strings.TrimRight(grafanaURL, "/")+llmAppAPIPath, token, llmAppModel, timeoutSeconds)
}
