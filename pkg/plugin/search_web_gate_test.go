package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	openai "github.com/sashabaranov/go-openai"
)

func boolPtrForTest(v bool) *bool { return &v }

// -- allTools gating ----------------------------------------------------------

func TestAllTools_InternetToolsNilGateExcludesSearchTool(t *testing.T) {
	t.Parallel()
	a := &App{settings: Settings{}, toolExecutor: NewToolExecutor("http://localhost:3000", log.DefaultLogger)}
	tools := a.allTools(context.Background(), "generic")
	for _, tool := range tools {
		if tool.Function != nil && tool.Function.Name == onlineSearchToolName {
			t.Fatal("search_web must not appear when EnableInternetTools was never configured (nil)")
		}
	}
}

func TestAllTools_InternetToolsDisabledExcludesSearchToolEvenIfClientExists(t *testing.T) {
	t.Parallel()
	te := NewToolExecutor("http://localhost:3000", log.DefaultLogger)
	client, err := NewOnlineSearchClient(OnlineSearchBackendSearxng, "", "", "https://searxng.example.internal", 5, 6, log.DefaultLogger)
	if err != nil {
		t.Fatalf("NewOnlineSearchClient: %v", err)
	}
	client.healthOK = true
	client.healthCheckedAt = time.Now()
	te.onlineSearch = client
	te.internetToolsEnabled = false // admin gate off, even though a healthy client exists

	a := &App{settings: Settings{EnableInternetTools: boolPtrForTest(false)}, toolExecutor: te}
	tools := a.allTools(context.Background(), "generic")
	for _, tool := range tools {
		if tool.Function != nil && tool.Function.Name == onlineSearchToolName {
			t.Fatal("search_web must not appear when EnableInternetTools is false, regardless of client health")
		}
	}
}

func TestAllTools_InternetToolsEnabledButUnhealthyExcludesSearchTool(t *testing.T) {
	t.Parallel()
	te := NewToolExecutor("http://localhost:3000", log.DefaultLogger)
	client, err := NewOnlineSearchClient(OnlineSearchBackendSearxng, "", "", "https://searxng.example.internal", 5, 6, log.DefaultLogger)
	if err != nil {
		t.Fatalf("NewOnlineSearchClient: %v", err)
	}
	// Cache never primed (healthCheckedAt zero) -- HealthyCached must report
	// false synchronously (a background refresh may fire, but the return
	// value here is decided before it completes).
	te.onlineSearch = client
	te.internetToolsEnabled = true

	a := &App{settings: Settings{EnableInternetTools: boolPtrForTest(true)}, toolExecutor: te}
	tools := a.allTools(context.Background(), "generic")
	for _, tool := range tools {
		if tool.Function != nil && tool.Function.Name == onlineSearchToolName {
			t.Fatal("search_web must not appear while the health cache is empty/unhealthy")
		}
	}
}

func TestAllTools_InternetToolsEnabledAndHealthyIncludesSearchTool(t *testing.T) {
	t.Parallel()
	te := NewToolExecutor("http://localhost:3000", log.DefaultLogger)
	client, err := NewOnlineSearchClient(OnlineSearchBackendSearxng, "", "", "https://searxng.example.internal", 5, 6, log.DefaultLogger)
	if err != nil {
		t.Fatalf("NewOnlineSearchClient: %v", err)
	}
	client.healthOK = true
	client.healthCheckedAt = time.Now()
	te.onlineSearch = client
	te.internetToolsEnabled = true

	a := &App{settings: Settings{EnableInternetTools: boolPtrForTest(true)}, toolExecutor: te}
	tools := a.allTools(context.Background(), "generic")
	found := false
	for _, tool := range tools {
		if tool.Function != nil && tool.Function.Name == onlineSearchToolName {
			found = true
		}
	}
	if !found {
		t.Fatal("expected search_web to be exposed when internet tools are enabled and the client is healthy")
	}
}

// -- ToolExecutor.Execute gating (order: admin gate -> client exists -> health) -

func TestExecute_SearchWeb_AdminDisabledNeverTouchesNetwork(t *testing.T) {
	t.Parallel()
	var hits int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := NewOnlineSearchClient(OnlineSearchBackendGateway, server.URL, "tok", "", 5, 6, log.DefaultLogger)
	if err != nil {
		t.Fatalf("NewOnlineSearchClient: %v", err)
	}
	client.httpClient = server.Client()
	client.healthOK = true
	client.healthCheckedAt = time.Now()

	te := NewToolExecutor("http://localhost:3000", log.DefaultLogger)
	te.onlineSearch = client
	te.internetToolsEnabled = false // the gate under test

	result, err := te.Execute(context.Background(), onlineSearchToolName, `{"query":"grafana dashboard docs","reason":"user requested"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(result, "disabled by admin") {
		t.Errorf("expected a disabled-by-admin message, got: %s", result)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Errorf("expected zero network requests when internetToolsEnabled=false, got %d", hits)
	}
}

func TestExecute_SearchWeb_NotConfiguredMessage(t *testing.T) {
	t.Parallel()
	te := NewToolExecutor("http://localhost:3000", log.DefaultLogger)
	te.internetToolsEnabled = true
	te.onlineSearch = nil

	result, err := te.Execute(context.Background(), onlineSearchToolName, `{"query":"grafana dashboard docs","reason":"user requested"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(result, "not configured") {
		t.Errorf("expected a not-configured message, got: %s", result)
	}
}

func TestExecute_SearchWeb_UnhealthySkipsSearchCall(t *testing.T) {
	t.Parallel()
	var healthHits, searchHits int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/v1/search") {
			atomic.AddInt32(&searchHits, 1)
			w.WriteHeader(http.StatusOK)
			return
		}
		atomic.AddInt32(&healthHits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client, err := NewOnlineSearchClient(OnlineSearchBackendGateway, server.URL, "tok", "", 5, 6, log.DefaultLogger)
	if err != nil {
		t.Fatalf("NewOnlineSearchClient: %v", err)
	}
	client.httpClient = server.Client()

	te := NewToolExecutor("http://localhost:3000", log.DefaultLogger)
	te.onlineSearch = client
	te.internetToolsEnabled = true

	result, err := te.Execute(context.Background(), onlineSearchToolName, `{"query":"grafana dashboard docs","reason":"user requested"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(result, "unavailable") && !strings.Contains(result, "failed health check") {
		t.Errorf("expected an unavailable/health-check-failed message, got: %s", result)
	}
	if atomic.LoadInt32(&searchHits) != 0 {
		t.Errorf("expected /v1/search to never be called when health check fails, got %d hits", searchHits)
	}
	if atomic.LoadInt32(&healthHits) == 0 {
		t.Error("expected a real health check attempt")
	}
}

// -- newToolCallInfo ------------------------------------------------------------

func TestNewToolCallInfo_SearchWebHidesRawArgumentsAndUsesInternetLabels(t *testing.T) {
	t.Parallel()
	info := newToolCallInfo("call_1", onlineSearchToolName, `{"query":"real user query","reason":"real reason"}`)
	if info.Arguments != "{}" {
		t.Errorf("Arguments = %q, want {} (raw query must not reach the frontend before sanitization)", info.Arguments)
	}
	if info.Kind != "internet_search" {
		t.Errorf("Kind = %q, want internet_search", info.Kind)
	}
	if !info.External {
		t.Error("expected External=true for search_web")
	}
	if info.Label != "Internet search" {
		t.Errorf("Label = %q, want %q", info.Label, "Internet search")
	}
}

func TestNewToolCallInfo_GrafanaToolKeepsArgumentsAndDefaultLabels(t *testing.T) {
	t.Parallel()
	info := newToolCallInfo("call_2", "query_prometheus", `{"query":"up"}`)
	if info.Arguments != `{"query":"up"}` {
		t.Errorf("Arguments = %q, want unchanged", info.Arguments)
	}
	if info.Kind != "grafana_tool" {
		t.Errorf("Kind = %q, want grafana_tool", info.Kind)
	}
	if info.External {
		t.Error("expected External=false for a regular Grafana tool")
	}
}

// -- pseudo_tool_calls budget/continuation/admin gating ------------------------

// searchWebGatewayServer returns a gateway test server that always returns
// one authorized, relevant result for /v1/search (and 200 OK for /v1/health)
// -- used to exercise executeToolCalls' budget/continuation logic against a
// real (successful) search path. The returned counter tracks only /v1/search
// hits -- ToolExecutor.Execute's case for search_web always revalidates with
// a real CheckNow (a genuine, uncached health request) right before every
// single search, by design (see the "revalidate on Execute" invariant), so
// counting ALL requests would conflate "how many searches actually ran" with
// "how many health checks happened," which isn't what these tests are
// verifying.
func searchWebGatewayServer(t *testing.T) (*httptest.Server, int32Counter) {
	t.Helper()
	// v is allocated up front (not lazily inside add) so the *int32 the
	// handler closure mutates is the SAME one the returned int32Counter
	// value holds -- int32Counter is returned by value, and the request
	// handler only ever runs later, asynchronously, well after this
	// function has already returned its copy. A pointer field allocated
	// lazily on first add() would mutate a pointer the caller's copy never
	// sees.
	counter := int32Counter{v: new(int32)}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/v1/search") {
			w.WriteHeader(http.StatusOK) // /v1/health always healthy
			return
		}
		counter.add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"Grafana docs overview","url":"https://grafana.com/docs/introduction/","snippet":"Grafana docs overview and getting started"}]}`))
	}))
	return server, counter
}

type int32Counter struct{ v *int32 }

func (c *int32Counter) add(n int32) {
	atomic.AddInt32(c.v, n)
}
func (c *int32Counter) load() int32 {
	return atomic.LoadInt32(c.v)
}

func newSearchWebApp(t *testing.T, server *httptest.Server, enabledTools []string) *App {
	t.Helper()
	client, err := NewOnlineSearchClient(OnlineSearchBackendGateway, server.URL, "tok", "", 5, 6, log.DefaultLogger)
	if err != nil {
		t.Fatalf("NewOnlineSearchClient: %v", err)
	}
	client.httpClient = server.Client()
	te := NewToolExecutor("http://localhost:3000", log.DefaultLogger)
	te.onlineSearch = client
	te.internetToolsEnabled = true
	return &App{logger: log.DefaultLogger, toolExecutor: te, settings: Settings{EnabledTools: enabledTools}}
}

func searchWebCall(id string, continuationApproved bool) openai.ToolCall {
	args, _ := json.Marshal(OnlineSearchArgs{
		Query:                "grafana dashboard docs",
		Reason:               "user requested current documentation",
		ContinuationApproved: continuationApproved,
	})
	return openai.ToolCall{ID: id, Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: onlineSearchToolName, Arguments: string(args)}}
}

func TestExecuteToolCalls_SearchWeb_NotEnabledByAdminAllowlist(t *testing.T) {
	t.Parallel()
	server, counter := searchWebGatewayServer(t)
	defer server.Close()

	// EnabledTools restricts to a different tool -- search_web must be
	// refused by the pseudo-tool-call gate without ever calling Execute.
	a := newSearchWebApp(t, server, []string{"query_prometheus"})

	msgs, err := a.executeToolCalls(context.Background(), []openai.ToolCall{searchWebCall("call_1", false)}, llmProvider{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("executeToolCalls failed: %v", err)
	}
	if !strings.Contains(msgs[0].Content, "not enabled by admin") {
		t.Errorf("expected an admin-allowlist refusal, got: %s", msgs[0].Content)
	}
	if counter.load() != 0 {
		t.Errorf("expected zero network requests, got %d", counter.load())
	}
}

func TestExecuteToolCalls_SearchWeb_SecondCallWithoutConfirmationRefused(t *testing.T) {
	t.Parallel()
	server, counter := searchWebGatewayServer(t)
	defer server.Close()
	a := newSearchWebApp(t, server, nil)

	// Neither call sets continuation_approved -- whichever of the two the
	// scheduler counts first proceeds (budget allows a first search), the
	// other must be refused for missing user confirmation. The outcome is
	// deterministic in aggregate even though execution order (which of the
	// two IDs "wins" the race) is not: exactly one network call, exactly one
	// confirmation-required refusal.
	calls := []openai.ToolCall{searchWebCall("call_1", false), searchWebCall("call_2", false)}
	msgs, err := a.executeToolCalls(context.Background(), calls, llmProvider{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("executeToolCalls failed: %v", err)
	}

	var refusals, successes int
	for _, m := range msgs {
		switch {
		case strings.Contains(m.Content, "second search requires user confirmation"):
			refusals++
		case strings.Contains(m.Content, "result_count"):
			successes++
		default:
			t.Errorf("unexpected message content: %s", m.Content)
		}
	}
	if refusals != 1 {
		t.Errorf("expected exactly 1 confirmation-required refusal, got %d (messages: %+v)", refusals, msgs)
	}
	if successes != 1 {
		t.Errorf("expected exactly 1 successful search, got %d", successes)
	}
	if counter.load() != 1 {
		t.Errorf("expected exactly 1 network request (the refused call must never reach the network), got %d", counter.load())
	}
}

// TestOnlineSearchBudgetDecision_TwoPerTurn unit-tests the per-turn search
// budget/continuation decision directly (see onlineSearchBudgetDecision),
// deterministically, for count=1..4 -- deliberately NOT going through
// executeToolCalls with several concurrent real search_web calls: doing so
// would also exercise OnlineSearchClient.CheckNow's own concurrent-caller
// short-circuit (a second CheckNow arriving while the first's real health
// probe is still in flight immediately reports unhealthy rather than
// waiting for it -- see CheckNow's own doc comment), which is a separate,
// timing-dependent behavior this test isn't about. See
// TestExecuteToolCalls_SearchWeb_ConcurrentCallsNeverExceedBudget below for
// an integration-level check that the *count* of calls actually reaching
// the network never exceeds the budget even under that race.
func TestOnlineSearchBudgetDecision_TwoPerTurn(t *testing.T) {
	t.Parallel()
	tc := searchWebCall("call_1", false)

	if _, ok := onlineSearchBudgetDecision(tc, 1); !ok {
		t.Error("count=1 (first search this turn) should be allowed")
	}
	if _, ok := onlineSearchBudgetDecision(tc, 2); ok {
		t.Error("count=2 without continuation_approved should require user confirmation")
	}
	approvedCall := searchWebCall("call_2", true)
	if _, ok := onlineSearchBudgetDecision(approvedCall, 2); !ok {
		t.Error("count=2 WITH continuation_approved should be allowed")
	}
	if _, ok := onlineSearchBudgetDecision(approvedCall, 3); ok {
		t.Error("count=3 must be refused: per-turn budget is maxOnlineSearchCallsPerTurn (2), even with continuation_approved")
	}
	if _, ok := onlineSearchBudgetDecision(approvedCall, 4); ok {
		t.Error("count=4 must also be refused")
	}
}

func TestOnlineSearchBudgetDecision_MessageContents(t *testing.T) {
	t.Parallel()
	tc := searchWebCall("call_1", false)

	msg, ok := onlineSearchBudgetDecision(tc, 2)
	if ok {
		t.Fatal("expected count=2 without approval to be refused")
	}
	if !strings.Contains(msg.Content, "second search requires user confirmation") {
		t.Errorf("expected a confirmation-required message, got: %s", msg.Content)
	}

	msg, ok = onlineSearchBudgetDecision(tc, 3)
	if ok {
		t.Fatal("expected count=3 to be refused")
	}
	if !strings.Contains(msg.Content, "per-turn search budget exhausted") {
		t.Errorf("expected a budget-exhausted message, got: %s", msg.Content)
	}
}

// TestExecuteToolCalls_SearchWeb_ConcurrentCallsNeverExceedBudget is an
// integration-level companion to the decision-level test above: it drives 3
// real, concurrent search_web calls through executeToolCalls (all
// continuation_approved, isolating the budget from the confirmation gate)
// and asserts the one invariant that must hold regardless of the
// CheckNow-race's exact outcome: the real, over-the-wire search count never
// exceeds maxOnlineSearchCallsPerTurn (2), and at least one call is refused
// specifically for exhausting the per-turn budget.
func TestExecuteToolCalls_SearchWeb_ConcurrentCallsNeverExceedBudget(t *testing.T) {
	t.Parallel()
	server, counter := searchWebGatewayServer(t)
	defer server.Close()
	a := newSearchWebApp(t, server, nil)

	calls := []openai.ToolCall{
		searchWebCall("call_1", true),
		searchWebCall("call_2", true),
		searchWebCall("call_3", true),
	}
	msgs, err := a.executeToolCalls(context.Background(), calls, llmProvider{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("executeToolCalls failed: %v", err)
	}

	var budgetRefusals int
	for _, m := range msgs {
		if strings.Contains(m.Content, "per-turn search budget exhausted") {
			budgetRefusals++
		}
	}
	if budgetRefusals < 1 {
		t.Error("expected at least 1 budget-exhausted refusal among 3 concurrent calls")
	}
	if counter.load() > maxOnlineSearchCallsPerTurn {
		t.Errorf("real search requests = %d, must never exceed the per-turn budget of %d", counter.load(), maxOnlineSearchCallsPerTurn)
	}
}

func TestOnlineSearchContinuationApproved(t *testing.T) {
	t.Parallel()
	approved, _ := json.Marshal(OnlineSearchArgs{Query: "q", Reason: "r", ContinuationApproved: true})
	if !onlineSearchContinuationApproved(string(approved)) {
		t.Error("expected continuation_approved=true to be detected")
	}
	notApproved, _ := json.Marshal(OnlineSearchArgs{Query: "q", Reason: "r"})
	if onlineSearchContinuationApproved(string(notApproved)) {
		t.Error("expected continuation_approved to default to false when absent")
	}
}

func TestToolNameEnabledByAdmin(t *testing.T) {
	t.Parallel()
	if !toolNameEnabledByAdmin(onlineSearchToolName, nil) {
		t.Error("empty allowlist must allow every tool")
	}
	if !toolNameEnabledByAdmin(onlineSearchToolName, []string{onlineSearchToolName}) {
		t.Error("expected search_web to be allowed when explicitly listed")
	}
	if toolNameEnabledByAdmin(onlineSearchToolName, []string{"query_prometheus"}) {
		t.Error("expected search_web to be refused when not in a non-empty allowlist")
	}
}

// -- internetToolState / internetToolsPromptAddition ---------------------------

func TestInternetToolState_Disabled(t *testing.T) {
	t.Parallel()
	a := &App{settings: Settings{}, toolExecutor: NewToolExecutor("http://localhost:3000", log.DefaultLogger)}
	if state := a.internetToolState(context.Background()); state != InternetToolsDisabled {
		t.Errorf("state = %v, want InternetToolsDisabled", state)
	}
}

func TestInternetToolState_EnabledNoConfiguredTools(t *testing.T) {
	t.Parallel()
	a := &App{settings: Settings{EnableInternetTools: boolPtrForTest(true)}, toolExecutor: NewToolExecutor("http://localhost:3000", log.DefaultLogger)}
	if state := a.internetToolState(context.Background()); state != InternetToolsEnabledNoConfiguredTools {
		t.Errorf("state = %v, want InternetToolsEnabledNoConfiguredTools", state)
	}
}

func TestInternetToolsPromptAddition_MentionsToolOnlyWhenSearchAvailable(t *testing.T) {
	t.Parallel()
	disabledLine := internetToolsPromptAddition(InternetToolsDisabled)
	if strings.Contains(disabledLine, "Web search decision policy") {
		t.Error("the decision policy block must only appear when search is actually available")
	}
	enabledLine := internetToolsPromptAddition(InternetToolsEnabledWithSearch)
	if !strings.Contains(enabledLine, "Web search decision policy") {
		t.Error("expected the decision policy block when internet search is enabled and healthy")
	}
	if !strings.Contains(enabledLine, onlineSearchToolName) {
		t.Errorf("expected the prompt addition to reference the real tool name %q", onlineSearchToolName)
	}
}

// -- looksLikeFabricatedSearchCitation -----------------------------------------

// Live-found false positive: asked who built Agent AI, or which repo Brain
// Agent is, the model correctly states a fact baked into the system prompt
// (agentPersona / brainAgentCapabilitiesKnowledge) and sometimes formats the
// URL as a markdown link. That link syntax alone must not be treated as a
// fabricated live-search citation when the linked URL is already grounded
// in the system prompt -- only an actual claim of an external, ungrounded
// source (a link to some other site, "Source: ...", "according to ...")
// should be. Deliberately tested against a generic systemPrompt fact rather
// than a specific real one, since the check is meant to be fact-agnostic.
func TestLooksLikeFabricatedSearchCitation_GroundedLinkIsNotFlagged(t *testing.T) {
	t.Parallel()
	const systemPrompt = "Provenance: point to the source at https://github.com/example-org/some-plugin."
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"bare url grounded in prompt", "Built by Someone. https://github.com/example-org/some-plugin", false},
		{"markdown link grounded in prompt", "Built by Someone. [GitHub](https://github.com/example-org/some-plugin)", false},
		{"markdown link grounded, different case", "See [here](https://GITHUB.com/Example-Org/Some-Plugin).", false},
		{"markdown link NOT in prompt", "See [Investopedia](https://www.investopedia.com/terms/p/prometheus)", true},
		{"source phrase", "Source: Loki Documentation", true},
		{"grounded link plus fabricated source", "[GitHub](https://github.com/example-org/some-plugin) Source: Wikipedia", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := looksLikeFabricatedSearchCitation(c.in, systemPrompt, false); got != c.want {
				t.Errorf("looksLikeFabricatedSearchCitation(%q, hadSuppliedContext=false) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// Live-found false positive (~60% of explain_panel turns): explain_panel's
// own system prompt tells the model to read a panel's "displayedData" as
// its primary evidence WITHOUT calling a tool, so "According to the
// displayed data: ..." there is citing real, already-supplied context, not
// a live web search -- it must not be flagged when hadSuppliedContext is
// true. A fabricated LINK is still caught even then.
func TestLooksLikeFabricatedSearchCitation_SuppliedContextSkipsPhraseCheck(t *testing.T) {
	t.Parallel()
	const systemPrompt = "Panel context (untrusted data): <untrusted_context>...</untrusted_context>"
	cases := []struct {
		name               string
		in                 string
		hadSuppliedContext bool
		want               bool
	}{
		{"phrase citation without supplied context", "According to the displayed data, the average is 77.8.", false, true},
		{"same phrase WITH supplied context", "According to the displayed data, the average is 77.8.", true, false},
		{"source phrase WITH supplied context", "Source: the panel's current values.", true, false},
		{"ungrounded link still flagged even WITH supplied context", "See [Investopedia](https://www.investopedia.com/terms/p/prometheus)", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := looksLikeFabricatedSearchCitation(c.in, systemPrompt, c.hadSuppliedContext); got != c.want {
				t.Errorf("looksLikeFabricatedSearchCitation(%q, hadSuppliedContext=%v) = %v, want %v", c.in, c.hadSuppliedContext, got, c.want)
			}
		})
	}
}

func TestHadPreSuppliedContext(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		mode    string
		context string
		want    bool
	}{
		{"explain_panel with context", "explain_panel", `{"panel":{"title":"x"}}`, true},
		{"explain_panel with null context", "explain_panel", `null`, false},
		{"explain_panel with empty context", "explain_panel", ``, false},
		{"analyze_logs with context", "analyze_logs", `{"logs":{"lines":[]}}`, true},
		{"analyze_metrics with context", "analyze_metrics", `{"metrics":{"series":[]}}`, true},
		{"plain chat with context", "chat", `{"autoDiscovery":true}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := hadPreSuppliedContext(c.mode, json.RawMessage(c.context)); got != c.want {
				t.Errorf("hadPreSuppliedContext(%q, %q) = %v, want %v", c.mode, c.context, got, c.want)
			}
		})
	}
}
