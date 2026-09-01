package plugin

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

const (
	// defaultTimeoutSeconds bounds a single non-streaming round (the initial
	// tool-decision call and each subsequent tool-check round). This is tuned
	// for local/GPU-backed models that can spend a long time before the first
	// token when the tool-heavy system prompt is large.
	defaultTimeoutSeconds   = 180
	defaultMaxTokens        = 4096
	defaultMaxContextTokens = 120000

	maxTimeoutSeconds          = 300 // 5 minutes -- generous for a single bounded request/response check.
	maxMaxTokens               = 32768
	maxRateLimitMaxRetries     = 20
	defaultRateLimitMaxRetries = 3

	defaultChatRateLimitPerMinute = 10
	maxChatRateLimitPerMinute     = 120

	// These defaults are just a known-working example for fresh/provisioned
	// installs; real local setups normally override them in Grafana settings.
	defaultEndpointURL = "https://api.groq.com/openai/v1"
	defaultModel       = "llama-3.3-70b-versatile"

	// Streaming gets a separate, longer HTTP client timeout so slow-but-healthy
	// streamed answers are not cut off by the bounded tool-check timeout.
	streamHTTPTimeout = 10 * time.Minute

	defaultMaxConcurrentChats = 25
	maxMaxConcurrentChats     = 500

	// Queueing avoids turning a briefly saturated single-GPU backend into an
	// immediate hard failure, while still bounding waiting goroutines.
	defaultChatQueueWaitSeconds = 30
	maxChatQueueWaitSeconds     = 300
	defaultChatQueueDepth       = 50
	maxChatQueueDepth           = 2000
)

// Settings holds the plugin configuration parsed from Grafana's jsonData and secureJsonData.
type Settings struct {
	EndpointURL      string            `json:"endpointURL"`
	Model            string            `json:"model"`
	TimeoutSeconds   int               `json:"timeoutSeconds"`
	MaxTokens        int               `json:"maxTokens"`
	MaxContextTokens int               `json:"maxContextTokens"`
	CustomHeaders    map[string]string `json:"customHeaders,omitempty"`
	GrafanaURL       string            `json:"grafanaURL,omitempty"`
	// AgentContexts holds user-edited specialization text per custom agent
	// slot (agent-1, agent-2, agent-3), set from the app's own Agents tab.
	AgentContexts                      map[string]string  `json:"agentContexts,omitempty"`
	AgentLabels                        map[string]string  `json:"agentLabels,omitempty"`
	AgentTemperatures                  map[string]float64 `json:"agentTemperatures,omitempty"`
	AgentContextTokens                 map[string]int     `json:"agentContextTokens,omitempty"`
	AgentActiveCount                   *int               `json:"agentActiveCount,omitempty"`
	RestrictSpecialistAgentsForViewers *bool              `json:"restrictSpecialistAgentsForViewers,omitempty"`
	RateLimitMaxRetries                *int               `json:"rateLimitMaxRetries,omitempty"`
	MaxConcurrentChats                 int                `json:"maxConcurrentChats,omitempty"`
	ChatRateLimitPerMinute             *int               `json:"chatRateLimitPerMinute,omitempty"`
	ChatQueueWaitSeconds               *int               `json:"chatQueueWaitSeconds,omitempty"`
	ChatQueueDepth                     *int               `json:"chatQueueDepth,omitempty"`
	EnabledTools                       []string           `json:"enabledTools,omitempty"`
	AllowedDatasourceUIDs              []string           `json:"allowedDatasourceUIDs,omitempty"`
	AttachmentMaxBytes                 int                `json:"attachmentMaxBytes,omitempty"`
	EnableStandaloneChat               *bool              `json:"enableStandaloneChat,omitempty"`
	EnableDashboardIntegration         *bool              `json:"enableDashboardIntegration,omitempty"`
	MaintenanceMode                    bool               `json:"maintenanceMode,omitempty"`
	FastMode                           bool               `json:"fastMode,omitempty"`
	CustomGuardrails                   string             `json:"customGuardrails,omitempty"`
	ResponseLanguage                   string             `json:"responseLanguage,omitempty"`
	DisableGuardrailsForDebug          bool               `json:"disableGuardrailsForDebug,omitempty"`
	FallbackProviders                  []FallbackProvider `json:"fallbackProviders,omitempty"`
	FallbackAPIKeys                    []string           `json:"-"`
	AuditLogFullContent                bool               `json:"auditLogFullContent,omitempty"`
	EnableLLMAppIntegration            *bool              `json:"enableLLMAppIntegration,omitempty"`
	EnableBrainAgentTools              *bool              `json:"enableBrainAgentTools,omitempty"`
	EnableMemoryPrefetch               *bool              `json:"enableMemoryPrefetch,omitempty"`
	EnableInternetTools                *bool              `json:"enableInternetTools,omitempty"`
	OnlineSearchBackend                string             `json:"onlineSearchBackend,omitempty"`
	SearchGatewayURL                   string             `json:"searchGatewayURL,omitempty"`
	SearxngURL                         string             `json:"searxngURL,omitempty"`
	OnlineSearchMaxResults             int                `json:"onlineSearchMaxResults,omitempty"`
	OnlineSearchTimeoutSeconds         int                `json:"onlineSearchTimeoutSeconds,omitempty"`
	SearchGatewayToken                 string             `json:"-"`
	// GrafanaTokenPath is intentionally json:"-" and extracted manually from
	// raw jsonData, so it can be used without being serialized back out.
	GrafanaTokenPath string `json:"-"`
	APIKey           string `json:"-"`
	GrafanaToken     string `json:"-"`
}

// FallbackProvider is one additional endpoint/model pair configurable from
// the Configuration page as a fallback if the primary provider fails.
type FallbackProvider struct {
	EndpointURL string `json:"endpointURL"`
	Model       string `json:"model"`
}

const maxFallbackProviders = 2

// truncateRunes caps s at max runes, cutting on a rune boundary. Settings
// fields like CustomGuardrails/AgentContexts are free-text admin input
// reflected into the LLM system prompt, so this must be UTF-8 safe.
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == max {
			return s[:i]
		}
		count++
	}
	return s
}

// LoadSettings parses and validates plugin settings from Grafana.
func LoadSettings(appSettings backend.AppInstanceSettings) (Settings, error) {
	var settings Settings
	if err := json.Unmarshal(appSettings.JSONData, &settings); err != nil {
		return settings, fmt.Errorf("unmarshal settings: %w", err)
	}

	// Trim accidental whitespace from copy-pasted values before applying
	// defaults or validating URLs.
	settings.EndpointURL = strings.TrimSpace(settings.EndpointURL)
	settings.Model = strings.TrimSpace(settings.Model)

	if settings.EndpointURL == "" {
		settings.EndpointURL = defaultEndpointURL
	}
	if settings.Model == "" {
		settings.Model = defaultModel
	}
	settings.GrafanaURL = strings.TrimSpace(settings.GrafanaURL)

	var rawSettings struct {
		GrafanaTokenPath string `json:"grafanaTokenPath,omitempty"`
	}
	if err := json.Unmarshal(appSettings.JSONData, &rawSettings); err == nil {
		settings.GrafanaTokenPath = rawSettings.GrafanaTokenPath
	}

	if settings.TimeoutSeconds <= 0 {
		settings.TimeoutSeconds = defaultTimeoutSeconds
	}
	if settings.TimeoutSeconds > maxTimeoutSeconds {
		settings.TimeoutSeconds = maxTimeoutSeconds
	}

	if settings.MaxTokens <= 0 {
		settings.MaxTokens = defaultMaxTokens
	}
	if settings.MaxTokens > maxMaxTokens {
		settings.MaxTokens = maxMaxTokens
	}

	if settings.MaxContextTokens <= 0 {
		settings.MaxContextTokens = defaultMaxContextTokens
	}

	if settings.AgentActiveCount == nil {
		defaultCount := defaultAgentActiveCount
		settings.AgentActiveCount = &defaultCount
	}
	if *settings.AgentActiveCount < 0 {
		*settings.AgentActiveCount = 0
	}
	if *settings.AgentActiveCount > maxCustomAgents {
		*settings.AgentActiveCount = maxCustomAgents
	}

	if settings.RateLimitMaxRetries == nil {
		defaultRetries := defaultRateLimitMaxRetries
		settings.RateLimitMaxRetries = &defaultRetries
	}
	if *settings.RateLimitMaxRetries < 0 {
		*settings.RateLimitMaxRetries = 0
	}
	if *settings.RateLimitMaxRetries > maxRateLimitMaxRetries {
		*settings.RateLimitMaxRetries = maxRateLimitMaxRetries
	}

	if settings.ChatRateLimitPerMinute == nil {
		defaultRate := defaultChatRateLimitPerMinute
		settings.ChatRateLimitPerMinute = &defaultRate
	}
	if *settings.ChatRateLimitPerMinute < 1 {
		*settings.ChatRateLimitPerMinute = 1
	}
	if *settings.ChatRateLimitPerMinute > maxChatRateLimitPerMinute {
		*settings.ChatRateLimitPerMinute = maxChatRateLimitPerMinute
	}

	if settings.AttachmentMaxBytes > maxAttachmentMaxBytes {
		settings.AttachmentMaxBytes = maxAttachmentMaxBytes
	}

	if settings.MaxConcurrentChats <= 0 {
		settings.MaxConcurrentChats = defaultMaxConcurrentChats
	}
	if settings.MaxConcurrentChats > maxMaxConcurrentChats {
		settings.MaxConcurrentChats = maxMaxConcurrentChats
	}

	if settings.ChatQueueWaitSeconds == nil {
		defaultWait := defaultChatQueueWaitSeconds
		settings.ChatQueueWaitSeconds = &defaultWait
	}
	if *settings.ChatQueueWaitSeconds < 0 {
		*settings.ChatQueueWaitSeconds = 0
	}
	if *settings.ChatQueueWaitSeconds > maxChatQueueWaitSeconds {
		*settings.ChatQueueWaitSeconds = maxChatQueueWaitSeconds
	}

	if settings.ChatQueueDepth == nil {
		defaultDepth := defaultChatQueueDepth
		settings.ChatQueueDepth = &defaultDepth
	}
	if *settings.ChatQueueDepth < 0 {
		*settings.ChatQueueDepth = 0
	}
	if *settings.ChatQueueDepth > maxChatQueueDepth {
		*settings.ChatQueueDepth = maxChatQueueDepth
	}

	if settings.EnableInternetTools == nil {
		enabled := false
		settings.EnableInternetTools = &enabled
	}
	if settings.OnlineSearchBackend == "" {
		settings.OnlineSearchBackend = string(OnlineSearchBackendDuckDuckGo)
	}
	settings.OnlineSearchBackend = string(normalizeOnlineSearchBackend(OnlineSearchBackend(settings.OnlineSearchBackend)))
	if settings.OnlineSearchMaxResults <= 0 {
		settings.OnlineSearchMaxResults = defaultOnlineSearchMaxResults
	}
	if settings.OnlineSearchMaxResults > maxOnlineSearchMaxResults {
		settings.OnlineSearchMaxResults = maxOnlineSearchMaxResults
	}
	if settings.OnlineSearchTimeoutSeconds <= 0 {
		settings.OnlineSearchTimeoutSeconds = defaultOnlineSearchTimeoutSeconds
	}
	if settings.OnlineSearchTimeoutSeconds > maxOnlineSearchTimeoutSeconds {
		settings.OnlineSearchTimeoutSeconds = maxOnlineSearchTimeoutSeconds
	}

	settings.CustomGuardrails = strings.TrimSpace(settings.CustomGuardrails)
	settings.CustomGuardrails = truncateRunes(settings.CustomGuardrails, maxCustomGuardrailsChars)

	for key, ctx := range settings.AgentContexts {
		settings.AgentContexts[key] = truncateRunes(ctx, maxAgentContextChars)
	}

	if settings.EnableStandaloneChat == nil {
		enabled := true
		settings.EnableStandaloneChat = &enabled
	}
	if settings.EnableDashboardIntegration == nil {
		enabled := true
		settings.EnableDashboardIntegration = &enabled
	}
	if settings.EnableLLMAppIntegration == nil {
		enabled := true
		settings.EnableLLMAppIntegration = &enabled
	}

	if apiKey, ok := appSettings.DecryptedSecureJSONData["apiKey"]; ok {
		settings.APIKey = strings.TrimSpace(apiKey)
	}
	if grafanaToken, ok := appSettings.DecryptedSecureJSONData["grafanaToken"]; ok {
		settings.GrafanaToken = strings.TrimSpace(grafanaToken)
	}
	if token, ok := appSettings.DecryptedSecureJSONData["searchGatewayToken"]; ok {
		settings.SearchGatewayToken = strings.TrimSpace(token)
	}

	if len(settings.FallbackProviders) > maxFallbackProviders {
		settings.FallbackProviders = settings.FallbackProviders[:maxFallbackProviders]
	}
	settings.FallbackAPIKeys = make([]string, len(settings.FallbackProviders))
	for i := range settings.FallbackProviders {
		settings.FallbackProviders[i].EndpointURL = strings.TrimSpace(settings.FallbackProviders[i].EndpointURL)
		settings.FallbackProviders[i].Model = strings.TrimSpace(settings.FallbackProviders[i].Model)
		if key, ok := appSettings.DecryptedSecureJSONData[fmt.Sprintf("fallbackApiKey%d", i+1)]; ok {
			settings.FallbackAPIKeys[i] = strings.TrimSpace(key)
		}
	}

	if settings.EndpointURL != "" {
		if err := validateURL(settings.EndpointURL); err != nil {
			return settings, fmt.Errorf("invalid endpointURL: %w", err)
		}
	}
	for i, fp := range settings.FallbackProviders {
		if fp.EndpointURL == "" {
			continue
		}
		if err := validateURL(fp.EndpointURL); err != nil {
			return settings, fmt.Errorf("invalid fallbackProviders[%d].endpointURL: %w", i, err)
		}
	}

	if settings.GrafanaURL == "" {
		settings.GrafanaURL = "http://localhost:3000"
	}
	if err := validateURL(settings.GrafanaURL); err != nil {
		return settings, fmt.Errorf("invalid grafanaURL: %w", err)
	}

	return settings, nil
}
