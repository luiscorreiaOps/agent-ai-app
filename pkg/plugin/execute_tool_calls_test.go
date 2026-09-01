package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	openai "github.com/sashabaranov/go-openai"
)

func TestIsMutatingToolCall(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"store_memory", "upsert_memory", "suggest_memory", "delete_memory", "condense_memory"} {
		if !isMutatingToolCall(name) {
			t.Errorf("%s should be classified as mutating", name)
		}
	}
	for _, name := range []string{"query_prometheus", "query_loki", "list_datasources", "analyze_log_patterns", "investigate_incident"} {
		if isMutatingToolCall(name) {
			t.Errorf("%s should NOT be classified as mutating", name)
		}
	}
}

func promEchoMock(t *testing.T, onQuery func(query string)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/datasources" {
			_ = json.NewEncoder(w).Encode([]map[string]any{{"type": "prometheus", "uid": "prom-uid"}})
			return
		}
		query := r.URL.Query().Get("query")
		if onQuery != nil {
			onQuery(query)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data":   map[string]any{"resultType": "matrix", "result": []any{}},
			"echo":   query,
		})
	}))
}

func TestExecuteToolCalls_ResultsAlignedWithToolCallID(t *testing.T) {
	t.Parallel()

	server := promEchoMock(t, nil)
	defer server.Close()

	a := &App{logger: log.DefaultLogger, toolExecutor: NewToolExecutor(server.URL, log.DefaultLogger)}
	calls := []openai.ToolCall{
		{ID: "call_1", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "query_prometheus", Arguments: `{"query":"metric_one"}`}},
		{ID: "call_2", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "query_prometheus", Arguments: `{"query":"metric_two"}`}},
		{ID: "call_3", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "query_prometheus", Arguments: `{"query":"metric_three"}`}},
	}

	msgs, err := a.executeToolCalls(context.Background(), calls, llmProvider{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("executeToolCalls failed: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3", len(msgs))
	}
	want := []string{"metric_one", "metric_two", "metric_three"}
	for i := range calls {
		if msgs[i].ToolCallID != calls[i].ID {
			t.Errorf("msgs[%d].ToolCallID = %q, want %q (must stay aligned by index even when run concurrently)", i, msgs[i].ToolCallID, calls[i].ID)
		}
		if !strings.Contains(msgs[i].Content, want[i]) {
			t.Errorf("msgs[%d].Content = %q, want it to contain %q", i, msgs[i].Content, want[i])
		}
	}
}

func TestExecuteToolCalls_ReadOnlyCallsRunConcurrently(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var current, max int
	server := promEchoMock(t, func(string) {
		mu.Lock()
		current++
		if current > max {
			max = current
		}
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		current--
		mu.Unlock()
	})
	defer server.Close()

	a := &App{logger: log.DefaultLogger, toolExecutor: NewToolExecutor(server.URL, log.DefaultLogger)}
	calls := make([]openai.ToolCall, 4)
	for i := range calls {
		calls[i] = openai.ToolCall{
			ID:       fmt.Sprintf("call_%d", i),
			Type:     openai.ToolTypeFunction,
			Function: openai.FunctionCall{Name: "query_prometheus", Arguments: fmt.Sprintf(`{"query":"metric_%d"}`, i)},
		}
	}

	start := time.Now()
	if _, err := a.executeToolCalls(context.Background(), calls, llmProvider{}, nil, nil, nil); err != nil {
		t.Fatalf("executeToolCalls failed: %v", err)
	}
	elapsed := time.Since(start)

	mu.Lock()
	gotMax := max
	mu.Unlock()

	if gotMax < 2 {
		t.Errorf("max concurrent in-flight requests = %d, want at least 2 (calls should overlap, not run one at a time)", gotMax)
	}
	// 4 calls x 50ms would take ~200ms run sequentially; concurrent (up to
	// toolCallConcurrency=4 at once) should take close to one 50ms slot.
	if elapsed > 180*time.Millisecond {
		t.Errorf("elapsed = %v, want well under the ~200ms a sequential run would take", elapsed)
	}
}

func TestExecuteToolCalls_NotifyErrorPropagates(t *testing.T) {
	t.Parallel()

	a := &App{logger: log.DefaultLogger, toolExecutor: NewToolExecutor("http://unused", log.DefaultLogger)}
	calls := []openai.ToolCall{
		{ID: "call_1", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "list_datasources", Arguments: "{}"}},
	}
	wantErr := errors.New("stream closed")

	_, err := a.executeToolCalls(context.Background(), calls, llmProvider{}, func(name, args string) error { return wantErr }, nil, nil)
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
}

func TestExecuteToolCalls_MutatingCallsNeverOverlapEachOther(t *testing.T) {
	t.Parallel()

	var busy int32
	var overlapDetected int32
	mcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&busy, 1) != 1 {
			atomic.StoreInt32(&overlapDetected, 1)
		}
		time.Sleep(30 * time.Millisecond)
		atomic.AddInt32(&busy, -1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  map[string]any{"content": []map[string]string{{"type": "text", "text": "stored"}}},
		})
	}))
	defer mcpServer.Close()

	te := NewToolExecutor("http://unused", log.DefaultLogger)
	te.mcp = newMCPClient(mcpServer.URL, func() string { return "token" }, log.DefaultLogger)
	// Seed the tool cache directly (same trick as
	// TestToolExecutor_Execute_MCPTransportFailureReturnsHonestMessageNotRawError)
	// instead of a real tools/list round trip.
	te.mcp.tools = []openai.Tool{{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: "store_memory"}}}

	a := &App{logger: log.DefaultLogger, toolExecutor: te}
	calls := []openai.ToolCall{
		{ID: "m1", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "store_memory", Arguments: `{"project":"p","fact":"one"}`}},
		{ID: "m2", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "store_memory", Arguments: `{"project":"p","fact":"two"}`}},
	}

	msgs, err := a.executeToolCalls(context.Background(), calls, llmProvider{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("executeToolCalls failed: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	if atomic.LoadInt32(&overlapDetected) != 0 {
		t.Error("two mutating tool calls executed concurrently -- they must run strictly one at a time")
	}
}

func TestExecuteToolCalls_MutatingAndReadOnlyOverlapWithEachOther(t *testing.T) {
	t.Parallel()

	var readOnlyStarted = make(chan struct{}, 1)
	server := promEchoMock(t, func(string) {
		select {
		case readOnlyStarted <- struct{}{}:
		default:
		}
	})
	defer server.Close()

	mcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// MCPClient checks this status endpoint before the real tools/call
		// request -- must not consume the readOnlyStarted signal meant for
		// the actual call below, or this always times out on the real one.
		if r.URL.Path == inTransitStatusPath {
			_ = json.NewEncoder(w).Encode(map[string]bool{"enabled": false})
			return
		}
		// Block until the read-only call has started, proving the two
		// groups run concurrently rather than one waiting for the other.
		select {
		case <-readOnlyStarted:
		case <-time.After(time.Second):
			t.Error("read-only call never started while the mutating call was in flight -- they should overlap")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  map[string]any{"content": []map[string]string{{"type": "text", "text": "stored"}}},
		})
	}))
	defer mcpServer.Close()

	te := NewToolExecutor(server.URL, log.DefaultLogger)
	te.mcp = newMCPClient(mcpServer.URL, func() string { return "token" }, log.DefaultLogger)
	te.mcp.tools = []openai.Tool{{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: "store_memory"}}}

	a := &App{logger: log.DefaultLogger, toolExecutor: te}
	calls := []openai.ToolCall{
		{ID: "m1", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "store_memory", Arguments: `{"project":"p","fact":"one"}`}},
		{ID: "r1", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "query_prometheus", Arguments: `{"query":"up"}`}},
	}

	if _, err := a.executeToolCalls(context.Background(), calls, llmProvider{}, nil, nil, nil); err != nil {
		t.Fatalf("executeToolCalls failed: %v", err)
	}
}
