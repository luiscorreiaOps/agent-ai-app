package plugin

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/instancemgmt"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	"github.com/prometheus/client_golang/prometheus"
	openai "github.com/sashabaranov/go-openai"
	"golang.org/x/time/rate"
)

// App is the main plugin instance.
type App struct {
	httpHandler backend.CallResourceHandler
	settings    Settings
	logger      log.Logger
	metrics     *metrics
	// metricsRegistry is the concrete registry metrics are registered
	// against, kept alongside metrics so registerRoutes can expose it over
	// GET /metrics (promhttp) -- prometheus.Registerer (metrics' own
	// constructor argument) doesn't expose enough to build a handler from.
	metricsRegistry *prometheus.Registry
	toolExecutor    *ToolExecutor
	limiters        sync.Map
	limiterCalls    atomic.Uint64
	// llmClient is used for bounded, single request/response calls (the
	// per-round tool-call check, the non-streaming chat path) -- its
	// underlying HTTP client honors settings.TimeoutSeconds.
	llmClient *openai.Client
	// llmStreamClient is used only for CreateChatCompletionStream, with a
	// much longer HTTP timeout (see streamHTTPTimeout) so a slow-but-healthy
	// stream isn't cut off mid-answer by the same bound meant for a single
	// request/response.
	llmStreamClient *openai.Client
	// providers is the ordered, fully-resolved list of usable LLM providers:
	// the primary configuration first (providers[0], mirrored onto llmClient/
	// llmStreamClient above for callers that only ever need the primary),
	// then any complete fallback slots. See FallbackProviders' doc comment.
	providers []llmProvider

	// chatSemaphore caps how many chat requests (streaming or not) run at
	// once across every user -- see MaxConcurrentChats and
	// tryAcquireChatSlot. Buffered channel used purely as a counting
	// semaphore (values are never read for their content).
	chatSemaphore chan struct{}

	// chatQueueWaiting counts callers currently WAITING for a chat slot
	// (not yet holding one) -- bounds queue depth independently of the
	// wait-timeout, see tryAcquireChatSlotQueued.
	chatQueueWaiting int64

	// healthCache* memoize handleHealth's result for healthCacheTTL -- the
	// UI's mount-time hook and every open tab hitting /resources/health at
	// once would otherwise each trigger their own real LLM-endpoint round
	// trip. CheckHealth itself (Grafana core's own health-check entry point,
	// and the admin config page's "Test connection" button) is NOT cached --
	// only the HTTP route the chat UI polls.
	healthCacheMu     sync.Mutex
	healthCacheResult *backend.CheckHealthResult
	healthCacheTime   time.Time
}

// healthCacheTTL bounds how long handleHealth reuses a previous CheckHealth
// result -- long enough that several tabs mounting at once share one real
// check, short enough that a genuine outage/recovery still shows up quickly.
const healthCacheTTL = 15 * time.Second

// limiterEntry pairs a per-user limiter with when it was last touched, so
// idle entries can be evicted -- with a distinct real user per caller (the
// identity now comes from Grafana's own authenticated login, see
// requestUser), a.limiters would otherwise grow one entry per user for the
// life of the process with no upper bound.
type limiterEntry struct {
	limiter  *rate.Limiter
	lastUsed int64 // unix seconds, updated/read via atomic
}

// limiterIdleEvictAfter/limiterSweepEvery bound how long a user's limiter
// sticks around unused and how often the sweep runs (as a fraction of
// getLimiter calls, so this never needs its own goroutine/ticker).
const (
	limiterIdleEvictAfter = 30 * time.Minute
	limiterSweepEvery     = 500
)

// getLimiter returns a per-user rate limiter, defaultChatRateLimitPerMinute
// requests per minute unless overridden by Settings.ChatRateLimitPerMinute.
func (a *App) getLimiter(user string) *rate.Limiter {
	now := time.Now().Unix()
	if v, ok := a.limiters.Load(user); ok {
		entry := v.(*limiterEntry)
		atomic.StoreInt64(&entry.lastUsed, now)
		a.maybeSweepIdleLimiters(now)
		return entry.limiter
	}
	perMinute := defaultChatRateLimitPerMinute
	if a.settings.ChatRateLimitPerMinute != nil {
		perMinute = *a.settings.ChatRateLimitPerMinute
	}
	entry := &limiterEntry{limiter: rate.NewLimiter(rate.Limit(float64(perMinute)/60.0), perMinute), lastUsed: now}
	actual, _ := a.limiters.LoadOrStore(user, entry)
	a.maybeSweepIdleLimiters(now)
	return actual.(*limiterEntry).limiter
}

// tryAcquireChatSlot reserves one of MaxConcurrentChats global concurrent
// chat slots. Complements getLimiter's per-user cap: without this, enough
// distinct users each staying within their own 10/min limit could still
// collectively saturate this instance's outbound connections or the LLM
// provider's own concurrency limits. Returns ok=false immediately (never
// blocks/queues) when already at capacity -- the caller should reject with
// 429 rather than wait, so a saturated instance fails fast instead of
// piling up latency. release is always safe to call (a no-op on failure).
func (a *App) tryAcquireChatSlot() (release func(), ok bool) {
	if a.chatSemaphore == nil {
		// Never initialized (e.g. an App built directly in a test rather
		// than via NewApp) -- behave as unlimited rather than a nil channel
		// send that select's default branch would always treat as "full".
		return func() {}, true
	}
	select {
	case a.chatSemaphore <- struct{}{}:
		return func() { <-a.chatSemaphore }, true
	default:
		return func() {}, false
	}
}

// tryAcquireChatSlotQueued is tryAcquireChatSlot's queueing variant: when no
// slot is immediately free, it waits up to Settings.ChatQueueWaitSeconds for
// one to open (or the request's own context to cancel/timeout, whichever
// comes first) instead of rejecting instantly. Live-measured motivation: a
// single real GPU backend with no load balancer can genuinely run very few
// real generations well at once (a 14B model on one T4 degraded severely
// past 1-2 truly concurrent generations) -- instant rejection turns "the
// backend is momentarily busy" into a hard failure the caller must retry
// itself, when a short wait would have gotten a real answer instead.
//
// Purely additive, never a new bottleneck: the fast path below (a slot
// already free) is byte-for-byte the same check as tryAcquireChatSlot,
// hit unconditionally first -- an install with real spare capacity (a
// beefier GPU, or several instances behind a load balancer, either way
// reflected by a correspondingly higher MaxConcurrentChats) never enters
// the queueing path at all, so this adds no latency for them. Setting
// ChatQueueWaitSeconds=0 (or ChatQueueDepth=0) restores the exact old
// fail-fast behavior for anyone who prefers that.
//
// ChatQueueDepth bounds how many callers may be WAITING at once, separate
// from the wait-timeout -- a genuinely overwhelmed instance still fails
// immediately for callers beyond that depth, rather than growing an
// unbounded backlog of blocked goroutines that could themselves exhaust
// memory/connections.
func (a *App) tryAcquireChatSlotQueued(ctx context.Context) (release func(), ok bool) {
	if a.chatSemaphore == nil {
		return func() {}, true
	}

	// Fast path: identical to tryAcquireChatSlot -- never touches the
	// queue at all when a slot is already free.
	select {
	case a.chatSemaphore <- struct{}{}:
		return func() { <-a.chatSemaphore }, true
	default:
	}

	waitSeconds := defaultChatQueueWaitSeconds
	if a.settings.ChatQueueWaitSeconds != nil {
		waitSeconds = *a.settings.ChatQueueWaitSeconds
	}
	if waitSeconds <= 0 {
		return func() {}, false
	}

	depth := defaultChatQueueDepth
	if a.settings.ChatQueueDepth != nil {
		depth = *a.settings.ChatQueueDepth
	}
	if depth <= 0 {
		return func() {}, false
	}
	if atomic.AddInt64(&a.chatQueueWaiting, 1) > int64(depth) {
		atomic.AddInt64(&a.chatQueueWaiting, -1)
		return func() {}, false
	}
	defer atomic.AddInt64(&a.chatQueueWaiting, -1)

	timer := time.NewTimer(time.Duration(waitSeconds) * time.Second)
	defer timer.Stop()

	select {
	case a.chatSemaphore <- struct{}{}:
		return func() { <-a.chatSemaphore }, true
	case <-timer.C:
		return func() {}, false
	case <-ctx.Done():
		return func() {}, false
	}
}

// maybeSweepIdleLimiters removes limiters unused for limiterIdleEvictAfter,
// running roughly every limiterSweepEvery calls rather than on every call.
func (a *App) maybeSweepIdleLimiters(now int64) {
	if a.limiterCalls.Add(1)%limiterSweepEvery != 0 {
		return
	}
	cutoff := now - int64(limiterIdleEvictAfter.Seconds())
	a.limiters.Range(func(key, value any) bool {
		entry := value.(*limiterEntry)
		if atomic.LoadInt64(&entry.lastUsed) < cutoff {
			a.limiters.Delete(key)
		}
		return true
	})
}

// NewApp creates a new plugin instance from the given settings.
func NewApp(ctx context.Context, appSettings backend.AppInstanceSettings) (instancemgmt.Instance, error) {
	logger := log.DefaultLogger
	logger.Info("Creating new plugin instance", "updated", appSettings.Updated)

	settings, err := LoadSettings(appSettings)
	if err != nil {
		return nil, err
	}
	grafanaURL := settings.GrafanaURL

	te := NewToolExecutor(grafanaURL, logger)
	if len(settings.AllowedDatasourceUIDs) > 0 {
		allowed := make(map[string]bool, len(settings.AllowedDatasourceUIDs))
		for _, uid := range settings.AllowedDatasourceUIDs {
			if uid = strings.TrimSpace(uid); uid != "" {
				allowed[uid] = true
			}
		}
		te.allowedDatasourceUIDs = allowed
		logger.Info("Tool executor restricted to datasources", "count", len(allowed))
	}
	// Grafana strips auth headers from plugin backend requests, so a service
	// account token is needed for the tool executor to call the Grafana API.
	// When forwarded headers are present they take precedence (future-proofing).
	if settings.GrafanaTokenPath != "" {
		te.tokenPath = settings.GrafanaTokenPath
		logger.Info("Tool executor configured with token file", "path", settings.GrafanaTokenPath)
	} else if settings.GrafanaToken != "" {
		te.defaultHeaders = map[string]string{
			"Authorization": "Bearer " + settings.GrafanaToken,
		}
		logger.Info("Tool executor configured with service account token")
	} else {
		logger.Warn("No Grafana service account token configured; tool calls that need the Grafana API will fail")
	}

	// Wire up brain-agent's MCP server as an additional, optional tool source
	// (see mcp.go) -- opt-in (see EnableBrainAgentTools' doc comment for why
	// this one defaults OFF, unlike EnableLLMAppIntegration). Doesn't require
	// a working LLM key anywhere -- only the same Grafana token already
	// required above; a missing or unreachable brain-agent is handled
	// gracefully at call time.
	if settings.EnableBrainAgentTools != nil && *settings.EnableBrainAgentTools && resolveGrafanaToken(settings) != "" {
		te.mcp = newMCPClient(grafanaURL, func() string { return resolveGrafanaToken(settings) }, logger)
	}

	// Internet Tools: only ever constructed, health-primed, or given a
	// chance to make a DNS/socket call when the admin has explicitly turned
	// on EnableInternetTools (invariant #16 -- see PLANO-PESQUISA-ONLINE's
	// security invariants). te.internetToolsEnabled is checked again,
	// independently, inside ToolExecutor.Execute -- defense in depth against
	// a stale/forced call reaching this dispatcher some other way.
	internetToolsEnabled := settings.EnableInternetTools != nil && *settings.EnableInternetTools
	te.internetToolsEnabled = internetToolsEnabled
	if internetToolsEnabled {
		onlineSearch, err := NewOnlineSearchClient(
			OnlineSearchBackend(settings.OnlineSearchBackend),
			settings.SearchGatewayURL,
			settings.SearchGatewayToken,
			settings.SearxngURL,
			settings.OnlineSearchMaxResults,
			settings.OnlineSearchTimeoutSeconds,
			logger,
		)
		if err != nil {
			logger.Warn("online search client not configured", "backend", settings.OnlineSearchBackend, "error", err)
		} else {
			te.onlineSearch = onlineSearch
		}
		// Prime the health cache asynchronously (HealthyCached triggers its
		// own background refresh and returns immediately) -- only reachable
		// here because internetToolsEnabled is already true.
		if te.onlineSearch != nil {
			_ = te.onlineSearch.HealthyCached()
		}
	}

	metricsRegistry := prometheus.NewRegistry()
	app := &App{
		settings:        settings,
		logger:          logger,
		metrics:         newMetrics(metricsRegistry),
		metricsRegistry: metricsRegistry,
		toolExecutor:    te,
		chatSemaphore:   make(chan struct{}, settings.MaxConcurrentChats),
	}

	// Resolve the full ordered provider list (primary + any complete fallback
	// slots) -- two OpenAI clients each, so the user-configured Timeout
	// (meant for a single bounded request/response) is never applied to a
	// streaming call, which can legitimately take longer to fully arrive.
	// providers[0] (the primary) is mirrored onto llmClient/llmStreamClient
	// for the handful of callers (health check, compaction) that only ever
	// need it.
	app.providers = buildProviders(ctx, settings, grafanaURL)
	if len(app.providers) > 0 {
		app.llmClient = app.providers[0].client
		app.llmStreamClient = app.providers[0].streamClient
	}

	app.registerRoutes()

	return app, nil
}

// Dispose cleans up resources on plugin shutdown.
func (a *App) Dispose() {}

// internetToolState reports this turn's internet-tools state for the system
// prompt (internetToolsPromptAddition) and for allTools' gate -- both must
// agree, so both call OnlineSearchClient.AdvertisedAvailable(), never a
// synchronous health check -- see the "no lentidao no chat" / "health check
// condicionado ao gate" invariants: the normal chat path must never add
// DNS/socket latency, on top of never running at all while the admin gate
// is off.
func (a *App) internetToolState(ctx context.Context) InternetToolState {
	if a.settings.EnableInternetTools == nil || !*a.settings.EnableInternetTools {
		return InternetToolsDisabled
	}
	if a.toolExecutor == nil || a.toolExecutor.onlineSearch == nil {
		return InternetToolsEnabledNoConfiguredTools
	}
	if !a.toolExecutor.onlineSearch.AdvertisedAvailable() {
		return InternetToolsEnabledButUnavailable
	}
	return InternetToolsEnabledWithSearch
}
