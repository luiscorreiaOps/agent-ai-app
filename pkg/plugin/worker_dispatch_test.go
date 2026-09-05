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

	openai "github.com/sashabaranov/go-openai"
)

func TestIsValidWorkerType(t *testing.T) {
	t.Parallel()

	for _, valid := range []string{workerTypeLogs, workerTypeMetrics, workerTypeTraces, workerTypeGeneral} {
		if !isValidWorkerType(valid) {
			t.Errorf("isValidWorkerType(%q) = false, want true", valid)
		}
	}
	if isValidWorkerType("made_up_type") {
		t.Error("isValidWorkerType(\"made_up_type\") = true, want false (must fall back to general instead of trusting an unrecognized model-supplied value)")
	}
}

func TestWorkerToolNames_RestrictedSubset(t *testing.T) {
	t.Parallel()

	full := llmTools("agent-1")
	fullNames := make(map[string]bool, len(full))
	for _, tool := range full {
		fullNames[tool.Function.Name] = true
	}

	for _, workerType := range []string{workerTypeLogs, workerTypeMetrics, workerTypeTraces, workerTypeGeneral} {
		names := workerToolNames(workerType)
		if len(names) == 0 {
			t.Errorf("workerToolNames(%q) returned no tools", workerType)
		}
		if len(names) >= len(full) {
			t.Errorf("workerToolNames(%q) returned %d tools, want fewer than the full catalog (%d) -- a worker's tool set must be a restricted subset", workerType, len(names), len(full))
		}
		for _, name := range names {
			if !fullNames[name] {
				t.Errorf("workerToolNames(%q) includes %q, which is not in the real tool catalog", workerType, name)
			}
			// dispatch_worker itself must never be offered back to a worker
			// -- a worker dispatching its own nested workers is out of scope
			// and would defeat the round/timeout budget.
			if name == "dispatch_worker" {
				t.Errorf("workerToolNames(%q) must never include dispatch_worker itself", workerType)
			}
		}
	}
}

func TestRunDispatchedWorker_InvalidArguments(t *testing.T) {
	t.Parallel()

	a := newTestApp(t, "http://localhost:1/v1", "key")
	tc := openai.ToolCall{ID: "call_1", Function: openai.FunctionCall{Name: "dispatch_worker", Arguments: "not json"}}

	result := a.runDispatchedWorker(context.Background(), tc, llmProvider{}, nil)
	if !strings.Contains(result, "Error") {
		t.Errorf("result = %q, want it to state an error for invalid arguments", result)
	}
}

func TestRunDispatchedWorker_EmptyTask(t *testing.T) {
	t.Parallel()

	a := newTestApp(t, "http://localhost:1/v1", "key")
	args, _ := json.Marshal(dispatchWorkerArgs{WorkerType: workerTypeGeneral, Task: "   "})
	tc := openai.ToolCall{ID: "call_1", Function: openai.FunctionCall{Name: "dispatch_worker", Arguments: string(args)}}

	result := a.runDispatchedWorker(context.Background(), tc, llmProvider{}, nil)
	if !strings.Contains(result, "non-empty task") {
		t.Errorf("result = %q, want it to reject an empty/whitespace-only task", result)
	}
}

// workerTwoRoundHandler simulates a worker's own nested tool-calling loop:
// the first non-streaming request gets a tool_calls response (calling
// innerToolName), the second gets a plain final-content response. Fails the
// test if more than 2 requests arrive (would mean the worker looped instead
// of finishing after its one internal tool call).
func workerTwoRoundHandler(t *testing.T, innerToolName, finalContent string) http.HandlerFunc {
	var n int32
	return func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&n, 1)
		w.Header().Set("Content-Type", "application/json")
		if count == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{
					{
						"index": 0,
						"message": map[string]any{
							"role":    "assistant",
							"content": "",
							"tool_calls": []map[string]any{
								{"id": "inner_1", "type": "function", "function": map[string]any{"name": innerToolName, "arguments": "{}"}},
							},
						},
						"finish_reason": "tool_calls",
					},
				},
			})
			return
		}
		if count > 2 {
			t.Errorf("worker made a 3rd LLM request, want it to stop after 1 inner tool call + 1 final answer")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"index": 0, "message": map[string]any{"role": "assistant", "content": finalContent}, "finish_reason": "stop"},
			},
		})
	}
}

func TestRunDispatchedWorker_ReturnsSynthesizedSummary(t *testing.T) {
	t.Parallel()

	llmServer := httptest.NewServer(workerTwoRoundHandler(t, "list_datasources", "Found 3 Prometheus datasources, all healthy."))
	defer llmServer.Close()

	a := newTestApp(t, llmServer.URL+"/v1", "key")
	provider := a.providers[0]

	args, _ := json.Marshal(dispatchWorkerArgs{WorkerType: workerTypeMetrics, Task: "check datasource health"})
	tc := openai.ToolCall{ID: "call_1", Function: openai.FunctionCall{Name: "dispatch_worker", Arguments: string(args)}}

	var eventsMu sync.Mutex
	var events []WorkerEventInfo
	notifyWorker := func(e WorkerEventInfo) error {
		eventsMu.Lock()
		defer eventsMu.Unlock()
		events = append(events, e)
		return nil
	}

	result := a.runDispatchedWorker(context.Background(), tc, provider, notifyWorker)

	if !strings.Contains(result, "Found 3 Prometheus datasources") {
		t.Errorf("result = %q, want it to contain the worker's synthesized summary", result)
	}
	if !strings.Contains(result, "Metrics Analyzer") {
		t.Errorf("result = %q, want it to name the worker type's label", result)
	}

	eventsMu.Lock()
	defer eventsMu.Unlock()
	if len(events) < 2 {
		t.Fatalf("got %d worker events, want at least a \"running\" start and a \"done\" finish", len(events))
	}
	first, last := events[0], events[len(events)-1]
	if first.Phase != "running" {
		t.Errorf("first event Phase = %q, want %q", first.Phase, "running")
	}
	if last.Phase != "done" {
		t.Errorf("last event Phase = %q, want %q", last.Phase, "done")
	}
	for _, e := range events {
		if e.TaskID != tc.ID {
			t.Errorf("event TaskID = %q, want %q (must match the dispatching tool_call's own ID)", e.TaskID, tc.ID)
		}
		if e.WorkerType != workerTypeMetrics {
			t.Errorf("event WorkerType = %q, want %q", e.WorkerType, workerTypeMetrics)
		}
	}
}

func TestWorkerToolNames_GeneralInvestigatorHasPrometheusFallback(t *testing.T) {
	t.Parallel()

	names := workerToolNames(workerTypeGeneral)
	found := false
	for _, name := range names {
		if name == "query_prometheus" {
			found = true
		}
	}
	if !found {
		t.Errorf("workerToolNames(%q) = %v, want it to include query_prometheus -- its only other query-ish tool, query_datasource, is SQL-only and cannot answer a metric question at all", workerTypeGeneral, names)
	}
}

// TestRunDispatchedWorker_RetriesOnceWhenAskingInsteadOfCalling reproduces a
// real live incident (2026-08-08): a worker's entire "finding" was asking
// which function/parameter to use instead of calling a real tool -- there is
// no one to answer that, so it must retry once with a corrective nudge
// rather than returning the question as if it were a real finding.
func TestRunDispatchedWorker_RetriesOnceWhenAskingInsteadOfCalling(t *testing.T) {
	t.Parallel()

	var n int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&n, 1)
		w.Header().Set("Content-Type", "application/json")
		switch count {
		case 1:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{
					{"index": 0, "message": map[string]any{"role": "assistant", "content": "The call is missing a required parameter. Could you specify which namespace to check?"}, "finish_reason": "stop"},
				},
			})
		case 2:
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "no one to answer") {
				t.Errorf("retry request did not include the corrective nudge; body: %s", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{
					{
						"index": 0,
						"message": map[string]any{
							"role":    "assistant",
							"content": "",
							"tool_calls": []map[string]any{
								{"id": "inner_1", "type": "function", "function": map[string]any{"name": "list_datasources", "arguments": "{}"}},
							},
						},
						"finish_reason": "tool_calls",
					},
				},
			})
		case 3:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{
					{"index": 0, "message": map[string]any{"role": "assistant", "content": "Found 2 datasources, both reachable."}, "finish_reason": "stop"},
				},
			})
		default:
			t.Errorf("unexpected extra LLM call %d, want the worker to stop after one retry", count)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer llmServer.Close()

	a := newTestApp(t, llmServer.URL+"/v1", "key")
	provider := a.providers[0]

	args, _ := json.Marshal(dispatchWorkerArgs{WorkerType: workerTypeGeneral, Task: "check something"})
	tc := openai.ToolCall{ID: "call_1", Function: openai.FunctionCall{Name: "dispatch_worker", Arguments: string(args)}}

	result := a.runDispatchedWorker(context.Background(), tc, provider, nil)

	if !strings.Contains(result, "Found 2 datasources") {
		t.Errorf("result = %q, want the retried real finding, not the original question", result)
	}
	if strings.Contains(result, "Could you specify") {
		t.Errorf("result = %q, must not contain the original punted question", result)
	}
}

// TestExecuteToolCalls_DispatchWorkerRunsConcurrently proves multiple
// dispatch_worker calls in the same round genuinely overlap -- they ride the
// SAME bounded concurrency pool as any other non-mutating tool call
// (toolCallConcurrency), with no separate worker-specific concurrency
// infrastructure needed.
func TestExecuteToolCalls_DispatchWorkerRunsConcurrently(t *testing.T) {
	t.Parallel()

	var current, max int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&current, 1)
		for {
			old := atomic.LoadInt32(&max)
			if n <= old || atomic.CompareAndSwapInt32(&max, old, n) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		atomic.AddInt32(&current, -1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"index": 0, "message": map[string]any{"role": "assistant", "content": "done"}, "finish_reason": "stop"},
			},
		})
	}))
	defer llmServer.Close()

	a := newTestApp(t, llmServer.URL+"/v1", "key")
	provider := a.providers[0]

	calls := make([]openai.ToolCall, 3)
	for i := range calls {
		args, _ := json.Marshal(dispatchWorkerArgs{WorkerType: workerTypeGeneral, Task: fmt.Sprintf("task %d", i)})
		calls[i] = openai.ToolCall{ID: fmt.Sprintf("call_%d", i), Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "dispatch_worker", Arguments: string(args)}}
	}

	start := time.Now()
	msgs, err := a.executeToolCalls(context.Background(), calls, provider, nil, nil, nil)
	if err != nil {
		t.Fatalf("executeToolCalls failed: %v", err)
	}
	elapsed := time.Since(start)

	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3", len(msgs))
	}
	for i, m := range msgs {
		if m.ToolCallID != calls[i].ID {
			t.Errorf("msgs[%d].ToolCallID = %q, want %q", i, m.ToolCallID, calls[i].ID)
		}
	}
	if got := atomic.LoadInt32(&max); got < 2 {
		t.Errorf("max concurrent dispatch_worker LLM calls = %d, want at least 2 (workers should overlap, not run one at a time)", got)
	}
	if elapsed > 120*time.Millisecond {
		t.Errorf("elapsed = %v, want well under the ~150ms a fully sequential run would take", elapsed)
	}
}

func TestChatCompletion_SpecialistDispatchWorkerWithMockLLM(t *testing.T) {
	t.Parallel()

	var call int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&call, 1)
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			args, _ := json.Marshal(dispatchWorkerArgs{
				WorkerType: workerTypeMetrics,
				Task:       "check checkout service health across metrics and alerts",
			})
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{
					{
						"index": 0,
						"message": map[string]any{
							"role":    "assistant",
							"content": "",
							"tool_calls": []map[string]any{
								{"id": "dispatch_1", "type": "function", "function": map[string]any{"name": "dispatch_worker", "arguments": string(args)}},
							},
						},
						"finish_reason": "tool_calls",
					},
				},
				"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 3},
			})
		case 2:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{
					{
						"index":         0,
						"message":       map[string]any{"role": "assistant", "content": "Worker found checkout latency stable and no active checkout alerts."},
						"finish_reason": "stop",
					},
				},
				"usage": map[string]int{"prompt_tokens": 12, "completion_tokens": 7},
			})
		case 3:
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "Worker found checkout latency stable") {
				t.Errorf("final main-model request did not include worker findings; body: %s", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{
					{
						"index":         0,
						"message":       map[string]any{"role": "assistant", "content": "The metrics worker found checkout latency stable and no active checkout alerts."},
						"finish_reason": "stop",
					},
				},
				"usage": map[string]int{"prompt_tokens": 20, "completion_tokens": 9},
			})
		default:
			t.Errorf("unexpected extra LLM call %d", n)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer llmServer.Close()

	app := newTestApp(t, llmServer.URL+"/v1", "key")
	app.settings.AgentContexts = map[string]string{"agent-1": "SRE specialist for checkout service investigations."}

	content, _, err := app.chatCompletion(context.Background(), ChatRequest{
		Agent:  "agent-1",
		Mode:   "chat",
		Prompt: "Investigate checkout health using subagents.",
	})
	if err != nil {
		t.Fatalf("chatCompletion failed: %v", err)
	}
	if !strings.Contains(content, "metrics worker found checkout latency stable") {
		t.Errorf("content = %q, want final answer based on worker findings", content)
	}
	if got := atomic.LoadInt32(&call); got != 3 {
		t.Errorf("LLM calls = %d, want 3 (main dispatch + worker answer + main final)", got)
	}
}
