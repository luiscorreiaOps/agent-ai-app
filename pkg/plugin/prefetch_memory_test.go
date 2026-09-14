package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
)

func mcpSearchMemoryMock(t *testing.T, resultText string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == inTransitStatusPath {
			_ = json.NewEncoder(w).Encode(map[string]bool{"enabled": false})
			return
		}
		if r.URL.Path != mcpToolsPath {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]any{
				"content": []map[string]string{{"type": "text", "text": resultText}},
			},
		})
	}))
}

func TestPrefetchMemoryContext_ReturnsEmptyWithoutMCP(t *testing.T) {
	t.Parallel()
	a := &App{settings: Settings{}, toolExecutor: &ToolExecutor{}}
	got := a.prefetchMemoryContext(context.Background(), json.RawMessage(`{"dashboard":{"title":"Checkout"}}`))
	if got != "" {
		t.Errorf("got %q, want empty (no mcp client configured)", got)
	}
}

func TestPrefetchMemoryContext_ReturnsEmptyWhenExplicitlyDisabled(t *testing.T) {
	t.Parallel()
	server := mcpSearchMemoryMock(t, "some historical fact")
	defer server.Close()

	disabled := false
	a := &App{
		settings:     Settings{EnableMemoryPrefetch: &disabled},
		toolExecutor: &ToolExecutor{mcp: newMCPClient(server.URL, func() string { return "token" }, log.DefaultLogger)},
	}
	got := a.prefetchMemoryContext(context.Background(), json.RawMessage(`{"dashboard":{"title":"Checkout"}}`))
	if got != "" {
		t.Errorf("got %q, want empty (EnableMemoryPrefetch=false)", got)
	}
}

func TestPrefetchMemoryContext_ReturnsEmptyWithNoDashboardOrPanelTitle(t *testing.T) {
	t.Parallel()
	server := mcpSearchMemoryMock(t, "some historical fact")
	defer server.Close()

	a := &App{
		settings:     Settings{},
		toolExecutor: &ToolExecutor{mcp: newMCPClient(server.URL, func() string { return "token" }, log.DefaultLogger)},
	}
	for _, ctxJSON := range []string{``, `{}`, `{"panel":{"title":""}}`, `{"autoDiscovery":true}`} {
		if got := a.prefetchMemoryContext(context.Background(), json.RawMessage(ctxJSON)); got != "" {
			t.Errorf("context %q: got %q, want empty (no usable title)", ctxJSON, got)
		}
	}
}

func TestPrefetchMemoryContext_ReturnsFormattedBlockOnRealMatch(t *testing.T) {
	t.Parallel()
	server := mcpSearchMemoryMock(t, "2026-01-10: checkout p99 spikes root-caused to connection pool exhaustion")
	defer server.Close()

	a := &App{
		settings:     Settings{},
		toolExecutor: &ToolExecutor{mcp: newMCPClient(server.URL, func() string { return "token" }, log.DefaultLogger)},
	}
	got := a.prefetchMemoryContext(context.Background(), json.RawMessage(`{"panel":{"title":"Checkout Latency"}}`))
	if !strings.Contains(got, "Checkout Latency") {
		t.Errorf("got %q, want it to mention the panel title", got)
	}
	if !strings.Contains(got, "connection pool exhaustion") {
		t.Errorf("got %q, want it to include the fetched memory content", got)
	}
	if !strings.Contains(got, "auto-fetched") {
		t.Errorf("got %q, want it to disclose this was pre-fetched, not model-requested", got)
	}
}

func TestPrefetchMemoryContext_ReturnsEmptyOnNoMatch(t *testing.T) {
	t.Parallel()
	server := mcpSearchMemoryMock(t, "Memory is currently empty or no matches found.")
	defer server.Close()

	a := &App{
		settings:     Settings{},
		toolExecutor: &ToolExecutor{mcp: newMCPClient(server.URL, func() string { return "token" }, log.DefaultLogger)},
	}
	got := a.prefetchMemoryContext(context.Background(), json.RawMessage(`{"dashboard":{"title":"Checkout"}}`))
	if got != "" {
		t.Errorf("got %q, want empty (search_memory found nothing)", got)
	}
}

func TestPrefetchMemoryContext_PanelTitleTakesPrecedenceOverDashboard(t *testing.T) {
	t.Parallel()
	var receivedQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpcReq struct {
			Params struct {
				Arguments struct {
					Query string `json:"query"`
				} `json:"arguments"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&rpcReq)
		receivedQuery = rpcReq.Params.Arguments.Query
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"content": []map[string]string{{"type": "text", "text": "a fact"}}},
		})
	}))
	defer server.Close()

	a := &App{
		settings:     Settings{},
		toolExecutor: &ToolExecutor{mcp: newMCPClient(server.URL, func() string { return "token" }, log.DefaultLogger)},
	}
	a.prefetchMemoryContext(context.Background(), json.RawMessage(`{"panel":{"title":"Panel Title"},"dashboard":{"title":"Dashboard Title"}}`))
	if receivedQuery != "Panel Title" {
		t.Errorf("search_memory query = %q, want the panel title to take precedence", receivedQuery)
	}
}

func TestPrefetchMemoryContext_SlowBrainAgentNeverBlocksPastItsOwnShortTimeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == inTransitStatusPath {
			_ = json.NewEncoder(w).Encode(map[string]bool{"enabled": false})
			return
		}
		// Slower than memoryPrefetchTimeout and slower than the caller's
		// own context deadline below -- simulates a stuck/overloaded
		// brain-agent. Without its own strict timeout, prefetchMemoryContext
		// would inherit whichever bound is longer (up to mcpCallTimeout,
		// 30s) and the whole chat turn would feel frozen for that long.
		time.Sleep(3 * time.Second)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"content": []map[string]string{{"type": "text", "text": "a fact, arrived too late to matter"}}},
		})
	}))
	defer server.Close()

	a := &App{
		settings:     Settings{},
		toolExecutor: &ToolExecutor{mcp: newMCPClient(server.URL, func() string { return "token" }, log.DefaultLogger)},
	}

	// A background context with no deadline of its own -- proves the cutoff
	// comes from prefetchMemoryContext's own timeout, not from inheriting a
	// caller-supplied one.
	start := time.Now()
	got := a.prefetchMemoryContext(context.Background(), json.RawMessage(`{"dashboard":{"title":"Checkout"}}`))
	elapsed := time.Since(start)

	if got != "" {
		t.Errorf("got %q, want empty -- a timed-out prefetch must never surface a late/partial result", got)
	}
	if elapsed >= 3*time.Second {
		t.Errorf("elapsed = %v, want well under the mock's 3s response delay (own short timeout should have cut it off)", elapsed)
	}
}

func TestChatCompletion_IncludesPrefetchedMemoryInSystemPrompt(t *testing.T) {
	t.Parallel()

	mcpServer := mcpSearchMemoryMock(t, "checkout p99 spikes root-caused to connection pool exhaustion")
	defer mcpServer.Close()

	var sentSystemPrompt string
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Messages) > 0 {
			sentSystemPrompt = body.Messages[0].Content
		}
		chatCompletionOKHandler("ok")(w, r)
	}))
	defer llmServer.Close()

	a := &App{
		settings:  Settings{MaxTokens: 100},
		providers: []llmProvider{newLLMProvider(llmServer.URL, "key", "test-model", "", 10)},
		logger:    log.DefaultLogger,
		toolExecutor: &ToolExecutor{
			mcp: newMCPClient(mcpServer.URL, func() string { return "token" }, log.DefaultLogger),
		},
	}

	_, _, err := a.chatCompletion(context.Background(), ChatRequest{
		Mode:    "chat",
		Prompt:  "why is this slow?",
		Context: json.RawMessage(`{"panel":{"title":"Checkout Latency"}}`),
	})
	if err != nil {
		t.Fatalf("chatCompletion failed: %v", err)
	}
	if !strings.Contains(sentSystemPrompt, "connection pool exhaustion") {
		t.Errorf("system prompt sent to the LLM = %q, want it to include the pre-fetched memory", sentSystemPrompt)
	}
}
