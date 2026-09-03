package plugin

import (
	"context"
	"net/http"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

// llmProvider is one fully-configured, ready-to-use LLM endpoint -- its own
// client (bounded timeout, for tool-call check rounds) and stream client
// (long timeout, for the final streamed answer). endpointURL is kept alongside
// so diagnostics can identify whichever provider actually ends up serving a
// request, not just the primary.
type llmProvider struct {
	endpointURL  string
	model        string
	client       *openai.Client
	streamClient *openai.Client
}

func newLLMProvider(endpointURL, apiKey, model string, timeoutSeconds int) llmProvider {
	config := openai.DefaultConfig(apiKey)
	config.BaseURL = strings.TrimSuffix(endpointURL, "/")

	// Both clients share the same Retry-After-capturing transport (see
	// retry_after.go) -- it's a passive observer keyed off the request's own
	// context, so wrapping it here has no effect unless a caller actually
	// asks for capture via withRetryAfterCapture.
	requestConfig := config
	requestConfig.HTTPClient = &http.Client{
		Timeout:   time.Duration(timeoutSeconds) * time.Second,
		Transport: &retryAfterTransport{base: &geminiThoughtRewriteTransport{base: &reasoningKeyRewriteTransport{base: http.DefaultTransport}}},
	}

	streamConfig := config
	streamConfig.HTTPClient = &http.Client{
		Timeout:   streamHTTPTimeout,
		Transport: &retryAfterTransport{base: &geminiThoughtRewriteTransport{base: &reasoningKeyRewriteTransport{base: http.DefaultTransport}}},
	}

	return llmProvider{
		endpointURL:  endpointURL,
		model:        model,
		client:       openai.NewClientWithConfig(requestConfig),
		streamClient: openai.NewClientWithConfig(streamConfig),
	}
}

// buildProviders resolves the ordered list of usable LLM providers: the
// primary configuration first, then any complete fallback slots (endpoint
// and model set), in order, then grafana-llm-app last if it's installed and
// configured on this Grafana instance. A fallback slot missing either of
// those two is skipped rather than failing plugin startup -- it's optional.
// The API key is NOT required to build a provider -- confirmed live: a
// self-hosted, no-auth endpoint (e.g. a local Ollama) reports CheckHealth
// Ok (which never required a key either, see health.go) while this used to
// still refuse to build a provider at all, so chat failed with a bare "no
// LLM provider configured" -- a misleading, hard-to-debug "healthy but
// completely non-functional" state. An endpoint that genuinely does need a
// key and wasn't given one now fails with a clear 401 from that provider at
// request time instead, which is more actionable than a silent skip.
// grafanaURL is passed in already resolved/defaulted (see NewApp), not read
// again from settings.GrafanaURL, which may be empty.
func buildProviders(ctx context.Context, settings Settings, grafanaURL string) []llmProvider {
	var providers []llmProvider
	if settings.EndpointURL != "" && settings.Model != "" {
		providers = append(providers, newLLMProvider(settings.EndpointURL, settings.APIKey, settings.Model, settings.TimeoutSeconds))
	}
	for i, fp := range settings.FallbackProviders {
		if fp.EndpointURL == "" || fp.Model == "" {
			continue
		}
		var key string
		if i < len(settings.FallbackAPIKeys) {
			key = settings.FallbackAPIKeys[i]
		}
		providers = append(providers, newLLMProvider(fp.EndpointURL, key, fp.Model, settings.TimeoutSeconds))
	}

	// grafana-llm-app, when installed and actually configured with a
	// working LLM provider, is always appended last -- an extra resilience
	// option when a provider is already configured above, or the ONLY
	// provider (zero-config) when nothing else has been set up yet. This is
	// the one part of the provider list that depends on a live check rather
	// than pure settings, so it's the one place buildProviders can be
	// slow (bounded by llmAppDetectTimeout) or silently find nothing.
	llmAppEnabled := settings.EnableLLMAppIntegration == nil || *settings.EnableLLMAppIntegration
	if token := resolveGrafanaToken(settings); llmAppEnabled && token != "" && detectLLMApp(ctx, grafanaURL, token) {
		providers = append(providers, newLLMAppProvider(grafanaURL, token, settings.TimeoutSeconds))
	}

	return providers
}

// firstProviderResponse tries each configured provider in order for the
// very first LLM call of a request -- the one point where a failure is
// still invisible to the user, since nothing has been shown yet. buildReq
// builds the request for a given provider (so per-provider quirks, like
// Gemini's tools being disabled, are applied correctly). onSwitch is called
// (if non-nil) right before trying the next provider, e.g. to surface a
// brief "trying another provider" status the same way rate-limit retries
// already do -- never a raw error, so the user never sees a failed attempt.
func (a *App) firstProviderResponse(
	ctx context.Context,
	buildReq func(p llmProvider) openai.ChatCompletionRequest,
	onRetry func(wait time.Duration),
	onSwitch func(),
) (openai.ChatCompletionResponse, llmProvider, error) {
	var lastErr error
	for i, p := range a.providers {
		resp, err := createChatCompletionWithRetry(ctx, p.client, buildReq(p), rateLimitMaxRetries(a.settings), onRetry)
		if err == nil {
			return resp, p, nil
		}
		lastErr = err
		a.logger.Warn("provider failed on first call, trying next configured provider", "providerIndex", i, "error", err)
		if i < len(a.providers)-1 && onSwitch != nil {
			onSwitch()
		}
	}
	return openai.ChatCompletionResponse{}, llmProvider{}, lastErr
}
