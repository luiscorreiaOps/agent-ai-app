package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	"github.com/prometheus/client_golang/prometheus"
	openai "github.com/sashabaranov/go-openai"
)

type decisionFunc func(context.Context, decisionState, []openai.Tool) (decisionRanking, error)

func (f decisionFunc) RankTools(ctx context.Context, state decisionState, tools []openai.Tool) (decisionRanking, error) {
	return f(ctx, state, tools)
}

func testRanking(pool []openai.Tool, choice string, confidence float64) decisionRanking {
	probs := map[string]float64{decisionNone: 0}
	for _, tool := range pool {
		if tool.Function != nil {
			probs[tool.Function.Name] = 0
		}
	}
	probs[choice] = 1
	return decisionRanking{Choice: choice, Confidence: confidence, Probabilities: probs}
}

func hasTool(pool []openai.Tool, name string) bool {
	for _, tool := range pool {
		if tool.Function != nil && tool.Function.Name == name {
			return true
		}
	}
	return false
}

func TestDecisionSettings(t *testing.T) {
	t.Parallel()
	settings, err := LoadSettings(backend.AppInstanceSettings{JSONData: []byte(`{}`)})
	if err != nil || settings.DecisionRoutingMode != "off" || settings.DecisionTopK != 5 || settings.DecisionTimeoutMs != 1500 {
		t.Fatalf("unexpected defaults: %+v, %v", settings, err)
	}
	for _, body := range []string{
		`{"decisionRoutingMode":"bad"}`, `{"decisionProvider":"chat"}`,
		`{"decisionRoutingMode":"enforce"}`, `{"decisionMinConfidence":-0.1}`,
		`{"decisionMinConfidence":1.1}`, `{"decisionEndpointURL":"file:///tmp/secret"}`,
		`{"decisionEndpointURL":"http://user:password@localhost:8000/v1/systemone"}`,
		`{"decisionEndpointURL":"http://localhost:8000/v1/systemone?key=secret"}`,
	} {
		if _, err := LoadSettings(backend.AppInstanceSettings{JSONData: []byte(body)}); err == nil {
			t.Errorf("accepted invalid configuration %s", body)
		}
	}
	settings, err = LoadSettings(backend.AppInstanceSettings{
		JSONData:                []byte(`{"decisionRoutingMode":"shadow","decisionProvider":"surogate","decisionEndpointURL":"http://rune:8000/v1/decisions","decisionModel":"rune","decisionTopK":99,"decisionTimeoutMs":99999,"decisionMinConfidence":0}`),
		DecryptedSecureJSONData: map[string]string{"decisionApiKey": " decision-secret "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if settings.DecisionTopK != 10 || settings.DecisionTimeoutMs != 10000 || *settings.DecisionMinConfidence != 0 || settings.DecisionAPIKey != "decision-secret" {
		t.Fatal("settings not normalized")
	}
	raw, _ := json.Marshal(settings)
	if strings.Contains(string(raw), "decision-secret") {
		t.Fatal("decision credential serialized")
	}
}

func TestDecisionModesAndFallback(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, mode, choice   string
		confidence           float64
		fail, lazy, light    bool
		wantProm, wantSearch bool
		wantCalls            int
	}{
		{name: "off", mode: "off", choice: "query_prometheus", confidence: 1, wantProm: true},
		{name: "shadow", mode: "shadow", choice: "query_prometheus", confidence: 1, wantProm: true, wantCalls: 1},
		{name: "shadow lazy", mode: "shadow", choice: "query_prometheus", confidence: 1, lazy: true, wantSearch: true, wantCalls: 1},
		{name: "enforce", mode: "enforce", choice: "query_prometheus", confidence: 1, wantProm: true, wantSearch: true, wantCalls: 1},
		{name: "error", mode: "enforce", fail: true, wantProm: true, wantCalls: 1},
		{name: "uncertain lazy", mode: "enforce", choice: "query_prometheus", confidence: 0.1, lazy: true, wantSearch: true, wantCalls: 1},
		{name: "none", mode: "enforce", choice: decisionNone, confidence: 1, wantProm: true, wantCalls: 1},
		{name: "unknown", mode: "enforce", choice: "invented_tool", confidence: 1, wantProm: true, wantCalls: 1},
		{name: "light enforce", mode: "enforce", choice: "query_prometheus", confidence: 1, light: true, wantProm: true, wantSearch: true, wantCalls: 1},
		{name: "light fallback", mode: "enforce", fail: true, light: true, wantCalls: 1},
		{name: "light shadow", mode: "shadow", choice: "query_prometheus", confidence: 1, light: true, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			a := &App{settings: Settings{DecisionRoutingMode: tc.mode, EnableToolSearch: &tc.lazy, LightModeForDefaultAgent: tc.light},
				decisionProvider: decisionFunc(func(_ context.Context, _ decisionState, pool []openai.Tool) (decisionRanking, error) {
					calls++
					if tc.fail {
						return decisionRanking{}, fmt.Errorf("offline")
					}
					return testRanking(pool, tc.choice, tc.confidence), nil
				})}
			_, session := a.prepareRequestTools(t.Context(), "generic", ChatRequest{Prompt: "CPU usage?"})
			tools := session.tools()
			if hasTool(tools, "query_prometheus") != tc.wantProm || hasTool(tools, toolSearchToolName) != tc.wantSearch || calls != tc.wantCalls {
				t.Fatalf("unexpected tools/calls: %v / %d", tools, calls)
			}
			if tc.mode == "enforce" && tc.choice == "query_prometheus" && tc.confidence == 1 && hasTool(tools, "query_loki") {
				t.Fatal("unselected specialized tool advertised")
			}
		})
	}
}

func TestDecisionAllowlistAndTimeout(t *testing.T) {
	t.Parallel()
	a := &App{settings: Settings{DecisionRoutingMode: "enforce", DecisionTimeoutMs: 20, EnabledTools: []string{"query_loki"}},
		decisionProvider: decisionFunc(func(ctx context.Context, _ decisionState, pool []openai.Tool) (decisionRanking, error) {
			if hasTool(pool, "query_prometheus") || hasTool(pool, "search_web") || len(pool) != 1 {
				t.Error("router saw unavailable tools")
			}
			<-ctx.Done()
			return decisionRanking{}, ctx.Err()
		})}
	start := time.Now()
	ctx, session := a.prepareRequestTools(t.Context(), "generic", ChatRequest{})
	if time.Since(start) > time.Second || !hasTool(session.tools(), "query_loki") {
		t.Fatal("timeout did not preserve baseline")
	}
	if _, err := NewToolExecutor("http://localhost:3000", nil).Execute(ctx, "query_prometheus", `{"query":"up"}`); err == nil {
		t.Fatal("disabled tool executable")
	}
}

func TestDecisionStateMinimizedAndRedacted(t *testing.T) {
	t.Parallel()
	secret := "glsa_abcdefghijklmnopqrstuvwxyz0123456789"
	req := ChatRequest{Prompt: "Check " + secret, Context: json.RawMessage(`{"token":"` + secret + `"}`)}
	for i := 0; i < 8; i++ {
		req.Messages = append(req.Messages, ChatMessage{Role: "user", Content: fmt.Sprintf("%d %s", i, secret)})
	}
	s := buildDecisionState(req)
	raw, _ := json.Marshal(s)
	if strings.Contains(string(raw), secret) || len(s.History) != 4 || !strings.HasPrefix(s.History[0].Content, "4 ") {
		t.Fatalf("unbounded or unredacted state: %s", raw)
	}
}

func TestHTTPDecisionProviders(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/v1/systemone", "/v1/decisions"} {
		t.Run(path, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != path || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer decision-only-key" {
					t.Error("wrong decision transport")
				}
				var body struct {
					Model     string        `json:"model"`
					State     decisionState `json:"state"`
					Questions map[string]struct {
						Type     string            `json:"type"`
						Criteria map[string]string `json:"criteria"`
					} `json:"questions"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("invalid request")
				}
				if body.Model != "local-decider" || body.Questions["tools"].Type != "choice" || body.State.Prompt != "CPU?" {
					t.Error("wrong decision envelope")
				}
				probs := make(map[string]float64)
				for name := range body.Questions["tools"].Criteria {
					probs[name] = 0
				}
				probs["query_prometheus"] = 1
				_ = json.NewEncoder(w).Encode(map[string]any{"answers": map[string]any{"tools": map[string]any{"type": "choice", "choice": "query_prometheus", "confidence": 1, "probabilities": probs}}})
			}))
			defer server.Close()
			p := newHTTPDecisionProvider(Settings{DecisionEndpointURL: server.URL + path, DecisionModel: "local-decider", DecisionAPIKey: "decision-only-key", DecisionTimeoutMs: 1000})
			ranking, err := p.RankTools(t.Context(), decisionState{Prompt: "CPU?"}, llmTools("generic"))
			if err != nil || ranking.Choice != "query_prometheus" {
				t.Fatalf("ranking failed: %v", err)
			}
		})
	}
}

func TestHTTPDecisionRejectsMalformedResponsesAndRedirects(t *testing.T) {
	t.Parallel()
	pool := filterEnabledTools(llmTools("generic"), []string{"query_prometheus"})
	for _, response := range []string{
		`not json`, `{}`, `{"answers":{"tools":{"choice":"query_prometheus","probabilities":{"query_prometheus":1,"__none__":0}}}}`,
		`{"answers":{"tools":{"type":"choice","choice":"invented","confidence":1,"probabilities":{"invented":1,"__none__":0}}}}`,
		`{"answers":{"tools":{"type":"choice","choice":"query_prometheus","confidence":1,"probabilities":{"query_prometheus":1}}}}`,
		`{"answers":{"tools":{"type":"choice","choice":"query_prometheus","confidence":1,"probabilities":{"query_prometheus":0.2,"__none__":0.8}}}}`,
		`{"answers":{"tools":{"type":"choice","choice":"query_prometheus","confidence":1,"probabilities":{"query_prometheus":1,"__none__":0.8}}}}`,
		strings.Repeat("x", maxDecisionResponseBytes+1),
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, response) }))
		p := newHTTPDecisionProvider(Settings{DecisionEndpointURL: server.URL, DecisionTimeoutMs: 1000})
		if _, err := p.RankTools(t.Context(), decisionState{}, pool); err == nil {
			t.Error("malformed result accepted")
		}
		server.Close()
	}
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { redirected.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	p := newHTTPDecisionProvider(Settings{DecisionEndpointURL: server.URL, DecisionTimeoutMs: 1000})
	if _, err := p.RankTools(t.Context(), decisionState{}, pool); err == nil || redirected.Load() != 0 {
		t.Fatal("decision redirect followed")
	}
}

func TestLightDecisionToolBudgetAndSchema(t *testing.T) {
	t.Parallel()
	a := &App{settings: Settings{DecisionRoutingMode: "enforce", LightModeForDefaultAgent: true, DecisionTopK: 10}, decisionProvider: decisionFunc(func(_ context.Context, _ decisionState, pool []openai.Tool) (decisionRanking, error) {
		r := testRanking(pool, "query_prometheus", 1)
		r.Probabilities["query_prometheus"] = 0.55
		for _, name := range []string{"query_loki", "query_tempo", "analyze_metric_anomaly", "forecast_capacity"} {
			r.Probabilities[name] = 0.1125
		}
		return r, nil
	})}
	_, session := a.prepareRequestTools(t.Context(), "generic", ChatRequest{})
	check := func() {
		t.Helper()
		tools := session.tools()
		if len(tools) > 6 || !hasTool(tools, "list_datasources") || !hasTool(tools, toolSearchToolName) {
			t.Fatalf("Light Mode expanded to %d tools", len(tools))
		}
		for _, tool := range tools {
			if len([]rune(tool.Function.Description)) > 240 {
				t.Error("verbose Light Mode tool")
			}
		}
	}
	check()
	for _, query := range []string{"loki", "tempo", "kubernetes", "capacity"} {
		if _, err := session.search(`{"query":"` + query + `"}`); err != nil {
			t.Fatal(err)
		}
		check()
	}
	tool := compactToolDefinition(filterEnabledTools(llmTools("generic"), []string{"query_prometheus"})[0])
	raw, _ := json.Marshal(tool.Function.Parameters)
	var schema map[string]any
	_ = json.Unmarshal(raw, &schema)
	if schema["required"] == nil || schema["properties"] == nil {
		t.Fatal("schema constraints lost")
	}
}

func TestRequestToolSearchIsolation(t *testing.T) {
	t.Parallel()
	te := NewToolExecutor("http://localhost:3000", nil)
	var wg sync.WaitGroup
	for _, name := range []string{"query_prometheus", "query_loki"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session := newRequestTools(filterEnabledTools(llmTools("generic"), []string{name}), true)
			ctx := withRequestTools(t.Context(), session)
			for range 20 {
				result, err := te.Execute(ctx, toolSearchToolName, `{"query":"query"}`)
				if err != nil {
					t.Error(err)
					return
				}
				var found searchToolResult
				if json.Unmarshal([]byte(result), &found) != nil || found.Found != 1 || found.Tools[0].Name != name {
					t.Errorf("cross-request tool discovery: %s", result)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestLightDiscoveryRefreshesExistingSelection(t *testing.T) {
	t.Parallel()
	s := newRequestTools(llmTools("generic"), true)
	s.light = true
	s.active = make(map[string]bool)
	s.order = nil
	s.activate([]string{"query_prometheus", "query_loki", "query_tempo"})
	// Refresh an old selection while discovering a fourth tool. Both must
	// be presented next; the oldest unrequested schema is the one evicted.
	s.activate([]string{"query_prometheus", "forecast_capacity"})
	tools := s.tools()
	if !hasTool(tools, "query_prometheus") || !hasTool(tools, "forecast_capacity") || hasTool(tools, "query_loki") {
		t.Fatal("discovery evicted the schema just requested")
	}
}

func TestWorkerKeepsSpecialistToolsWithLightAndSearch(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req openai.ChatCompletionRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("invalid worker request")
		}
		if !hasTool(req.Tools, "query_prometheus") || hasTool(req.Tools, toolSearchToolName) || hasTool(req.Tools, "query_loki") {
			t.Error("worker inherited parent presentation or lost its specialist tools")
		}
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{Content: "Fixture completed."}, FinishReason: openai.FinishReasonStop}}})
	}))
	defer server.Close()
	enabled := true
	a := &App{settings: Settings{LightModeForDefaultAgent: true, EnableToolSearch: &enabled}, toolExecutor: NewToolExecutor("http://localhost:3000", log.DefaultLogger)}
	parent := withRequestTools(t.Context(), newRequestTools(filterEnabledTools(llmTools("generic"), []string{"list_alerts"}), true))
	provider := newLLMProvider(server.URL+"/v1", "unused", "fixture", 5)
	result := a.runDispatchedWorker(parent, openai.ToolCall{ID: "worker", Function: openai.FunctionCall{Arguments: `{"worker_type":"metric_investigator","task":"Check CPU usage"}`}}, provider, nil)
	if !strings.Contains(result, "Fixture completed") {
		t.Fatalf("worker failed: %s", result)
	}
}

func TestDecisionMetrics(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := newMetrics(reg)
	m.recordDecision("systemone", "shadow", "shadow", 0.1)
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, f := range families {
		found[f.GetName()] = true
	}
	if !found["grafana_agentai_decision_requests_total"] || !found["grafana_agentai_decision_duration_seconds"] {
		t.Fatal("missing routing metrics")
	}
}

// Exercise real chat loops and Grafana HTTP execution, including pseudo calls
// and the UI's streaming resource path. No downloaded model is required.
func TestChatToolDiscoveryAndDecisionRouting(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		route, light, stream, pseudo bool
	}{
		{name: "lazy chat"}, {name: "lazy stream", stream: true},
		{name: "routed chat", route: true}, {name: "routed stream light pseudo", route: true, light: true, stream: true, pseudo: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var queries atomic.Int32
			grafana := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/health":
					_, _ = io.WriteString(w, `{"version":"12.3.1"}`)
				case "/api/datasources":
					_, _ = io.WriteString(w, `[{"uid":"prom","type":"prometheus"}]`)
				case "/api/datasources/proxy/uid/prom/api/v1/query_range":
					queries.Add(1)
					_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer grafana.Close()
			var rounds atomic.Int32
			llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request openai.ChatCompletionRequest
				if json.NewDecoder(r.Body).Decode(&request) != nil {
					t.Error("bad LLM request")
				}
				round := rounds.Add(1)
				if tc.light && len(request.Tools) > 6 {
					t.Errorf("Light Mode expanded to %d tools", len(request.Tools))
				}
				message := openai.ChatCompletionMessage{Role: "assistant", Content: "Prometheus returned no samples."}
				finish := openai.FinishReasonStop
				toolName, arguments := "", ""
				if !tc.route && round == 1 {
					if hasTool(request.Tools, "query_prometheus") {
						t.Error("lazy initial tool exposed")
					}
					toolName, arguments = toolSearchToolName, `{"query":"prometheus"}`
				} else if (tc.route && round == 1) || (!tc.route && round == 2) {
					if !hasTool(request.Tools, "query_prometheus") {
						t.Error("required schema absent from next round")
					}
					toolName, arguments = "query_prometheus", `{"query":"up"}`
				}
				if toolName != "" {
					if tc.pseudo {
						message.Content = `<function=` + toolName + `>` + arguments + `</function>`
					} else {
						message.Content = ""
						message.ToolCalls = []openai.ToolCall{{ID: fmt.Sprintf("call_%d", round), Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: toolName, Arguments: arguments}}}
						finish = openai.FinishReasonToolCalls
					}
				}
				_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{Choices: []openai.ChatCompletionChoice{{Message: message, FinishReason: finish}}})
			}))
			defer llm.Close()
			app := newTestApp(t, llm.URL+"/v1", "unused")
			app.logger = log.DefaultLogger
			app.toolExecutor = NewToolExecutor(grafana.URL, log.DefaultLogger)
			app.settings.EnableToolSearch = new(bool)
			*app.settings.EnableToolSearch = !tc.route
			app.settings.LightModeForDefaultAgent = tc.light
			if tc.route {
				app.settings.DecisionRoutingMode = "enforce"
				app.decisionProvider = decisionFunc(func(_ context.Context, _ decisionState, pool []openai.Tool) (decisionRanking, error) {
					return testRanking(pool, "query_prometheus", 1), nil
				})
			}
			req := ChatRequest{Mode: "chat", Prompt: "Query Prometheus for up."}
			if tc.stream {
				var done bool
				err := app.streamChatCompletion(t.Context(), req, backend.CallResourceResponseSenderFunc(func(response *backend.CallResourceResponse) error {
					var chunk ChatResponse
					if json.Unmarshal(response.Body, &chunk) == nil && chunk.Done {
						done = true
					}
					return nil
				}), nil)
				if err != nil || !done {
					t.Fatalf("stream failed: %v", err)
				}
			} else {
				content, _, err := app.chatCompletion(t.Context(), req)
				if err != nil || !strings.Contains(content, "Prometheus") {
					t.Fatalf("chat failed: %s %v", content, err)
				}
			}
			if queries.Load() != 1 {
				t.Fatalf("expected one actual Grafana query, got %d", queries.Load())
			}
		})
	}
}
