package plugin

import (
	"encoding/json"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

// makeTestPool returns a small slice of fake tools for use in search tests.
func makeTestPool() []openai.Tool {
	makeTool := func(name, desc string) openai.Tool {
		return openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        name,
				Description: desc,
				Parameters:  json.RawMessage(`{"type":"object","properties":{},"required":[]}`),
			},
		}
	}
	return []openai.Tool{
		makeTool("query_prometheus", "Execute a PromQL query against the configured Prometheus datasource."),
		makeTool("query_loki", "Execute a LogQL query against the configured Loki datasource and return log lines."),
		makeTool("list_alerts", "List currently firing or pending alerts from Grafana alerting."),
		makeTool("get_dashboard", "Get a Grafana dashboard's full structure including panels and queries."),
		makeTool("analyze_slo_burn_rate", "Computes how fast an error budget is being consumed from SLO queries."),
		makeTool("diagnose_kubernetes_workload", "Cross-references kube-state-metrics for a Kubernetes workload restarts and health."),
		makeTool(toolSearchToolName, "Discover and activate specialized tools."), // should be excluded from results
	}
}

// ─── searchTools ─────────────────────────────────────────────────────────────

func TestSearchTools_FindsRelevantToolByName(t *testing.T) {
	t.Parallel()

	pool := makeTestPool()
	result, err := searchTools(`{"query":"prometheus"}`, pool)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var res searchToolResult
	if err := json.Unmarshal([]byte(result), &res); err != nil {
		t.Fatalf("invalid JSON result: %v", err)
	}
	if res.Found == 0 {
		t.Fatal("expected at least 1 match for 'prometheus'")
	}
	found := false
	for _, tool := range res.Tools {
		if tool.Name == "query_prometheus" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected query_prometheus in results")
	}
}

func TestSearchTools_FindsRelevantToolByDescription(t *testing.T) {
	t.Parallel()

	pool := makeTestPool()
	// "kubernetes" is in the description of diagnose_kubernetes_workload
	result, err := searchTools(`{"query":"kubernetes pod"}`, pool)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var res searchToolResult
	if err := json.Unmarshal([]byte(result), &res); err != nil {
		t.Fatalf("invalid JSON result: %v", err)
	}
	if res.Found == 0 {
		t.Fatal("expected at least 1 match for 'kubernetes pod'")
	}
}

func TestSearchTools_ExcludesSearchToolsItself(t *testing.T) {
	t.Parallel()

	pool := makeTestPool()
	// Generic query that could match anything
	result, err := searchTools(`{"query":"tools discover"}`, pool)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var res searchToolResult
	if err := json.Unmarshal([]byte(result), &res); err != nil {
		t.Fatalf("invalid JSON result: %v", err)
	}
	for _, tool := range res.Tools {
		if tool.Name == toolSearchToolName {
			t.Errorf("search_tools must not appear in its own results")
		}
	}
}

func TestSearchTools_UnknownQueryReturnsZeroMatches(t *testing.T) {
	t.Parallel()

	pool := makeTestPool()
	result, err := searchTools(`{"query":"blockchain nft web3 completely_unknown"}`, pool)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var res searchToolResult
	if err := json.Unmarshal([]byte(result), &res); err != nil {
		t.Fatalf("invalid JSON result: %v", err)
	}
	if res.Found != 0 {
		t.Errorf("expected 0 matches for unrelated query, got %d", res.Found)
	}
	if !strings.Contains(res.Message, "No specialized tools matched") {
		t.Errorf("expected 'no match' message, got: %q", res.Message)
	}
}

func TestSearchTools_EmptyQueryReturnsError(t *testing.T) {
	t.Parallel()

	pool := makeTestPool()
	_, err := searchTools(`{"query":""}`, pool)
	if err == nil {
		t.Error("expected error for empty query")
	}
}

func TestSearchTools_InvalidJSONReturnsError(t *testing.T) {
	t.Parallel()

	pool := makeTestPool()
	_, err := searchTools(`not json`, pool)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestSearchTools_ResultContainsSchemas(t *testing.T) {
	t.Parallel()

	pool := makeTestPool()
	result, err := searchTools(`{"query":"prometheus"}`, pool)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var res searchToolResult
	if err := json.Unmarshal([]byte(result), &res); err != nil {
		t.Fatalf("invalid JSON result: %v", err)
	}
	for _, tool := range res.Tools {
		if len(tool.Parameters) == 0 {
			t.Errorf("tool %s has empty parameters schema", tool.Name)
		}
		if tool.Description == "" {
			t.Errorf("tool %s has empty description", tool.Name)
		}
	}
}

func TestSearchTools_LokiQuery(t *testing.T) {
	t.Parallel()

	pool := llmTools("generic")
	result, err := searchTools(`{"query":"loki logs"}`, pool)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var res searchToolResult
	if err := json.Unmarshal([]byte(result), &res); err != nil {
		t.Fatalf("invalid JSON result: %v", err)
	}
	if res.Found == 0 {
		t.Fatal("expected loki tools to be found")
	}
}

func TestSearchTools_MessagePresentOnMatches(t *testing.T) {
	t.Parallel()

	pool := makeTestPool()
	result, err := searchTools(`{"query":"prometheus"}`, pool)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var res searchToolResult
	if err := json.Unmarshal([]byte(result), &res); err != nil {
		t.Fatalf("invalid JSON result: %v", err)
	}
	if res.Message == "" {
		t.Error("expected non-empty message in result")
	}
}

// ─── searchToolDef ────────────────────────────────────────────────────────────

func TestSearchToolDef_IsValid(t *testing.T) {
	t.Parallel()

	def := searchToolDef()
	if def.Function == nil {
		t.Fatal("Function must not be nil")
	}
	if def.Function.Name != toolSearchToolName {
		t.Errorf("expected name=%q, got %q", toolSearchToolName, def.Function.Name)
	}
	if def.Function.Description == "" {
		t.Error("description must not be empty")
	}
	var schema map[string]any
	raw, ok := def.Function.Parameters.(json.RawMessage)
	if !ok {
		t.Fatal("Parameters must be json.RawMessage")
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("Parameters is not valid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Error("Parameters schema type must be 'object'")
	}
}

// ─── buildKeywords ────────────────────────────────────────────────────────────

func TestBuildKeywords_FiltersShortTokens(t *testing.T) {
	t.Parallel()

	kws := buildKeywords("a to the prometheus")
	for _, kw := range kws {
		if len([]rune(kw)) < 3 {
			t.Errorf("keyword %q is shorter than 3 runes", kw)
		}
	}
}

func TestBuildKeywords_HandlesSlashSeparators(t *testing.T) {
	t.Parallel()

	kws := buildKeywords("loki/logs")
	found := map[string]bool{}
	for _, kw := range kws {
		found[kw] = true
	}
	if !found["loki"] {
		t.Error("expected 'loki' in keywords from 'loki/logs'")
	}
	if !found["logs"] {
		t.Error("expected 'logs' in keywords from 'loki/logs'")
	}
}

// ─── allTools with EnableToolSearch ──────────────────────────────────────────

func TestAllTools_ToolSearchModeReturnsCoreToolsPlusSearchTool(t *testing.T) {
	t.Parallel()

	enabled := true
	a := &App{
		settings: Settings{
			EnableToolSearch: &enabled,
		},
	}
	tools := a.allTools(t.Context(), "generic")

	// Must include search_tools
	var hasSearch bool
	for _, tool := range tools {
		if tool.Function != nil && tool.Function.Name == toolSearchToolName {
			hasSearch = true
			break
		}
	}
	if !hasSearch {
		t.Error("expected search_tools in the tool list when EnableToolSearch=true")
	}

	// Must NOT include specialized tools (like query_prometheus) in the initial set
	for _, tool := range tools {
		if tool.Function == nil {
			continue
		}
		name := tool.Function.Name
		if name == toolSearchToolName {
			continue
		}
		isCoreOrAllowed := false
		for _, core := range toolSearchCoreTools {
			if name == core {
				isCoreOrAllowed = true
				break
			}
		}
		if !isCoreOrAllowed {
			t.Errorf("non-core tool %q exposed when EnableToolSearch=true", name)
		}
	}
}

func TestAllTools_ToolSearchModeSearchToolIsFirst(t *testing.T) {
	t.Parallel()

	enabled := true
	a := &App{
		settings: Settings{
			EnableToolSearch: &enabled,
		},
	}
	tools := a.allTools(t.Context(), "generic")
	if len(tools) == 0 {
		t.Fatal("expected at least one tool")
	}
	if tools[0].Function == nil || tools[0].Function.Name != toolSearchToolName {
		t.Errorf("expected search_tools to be first, got %q",
			func() string {
				if tools[0].Function != nil {
					return tools[0].Function.Name
				}
				return "<nil>"
			}())
	}
}

func TestAllTools_ToolSearchDisabledReturnsAllTools(t *testing.T) {
	t.Parallel()

	a := &App{
		settings: Settings{
			EnableToolSearch: nil, // disabled (default)
		},
	}
	tools := a.allTools(t.Context(), "generic")

	// Should have the standard count (32 for generic agent, no internet tools)
	if len(tools) != 32 {
		t.Errorf("expected 32 tools when EnableToolSearch=false, got %d", len(tools))
	}

	// search_tools should NOT be in the list
	for _, tool := range tools {
		if tool.Function != nil && tool.Function.Name == toolSearchToolName {
			t.Error("search_tools must not appear when EnableToolSearch is disabled")
		}
	}
}

// ─── ToolExecutor.setSearchPool / getSearchPool ───────────────────────────────

func TestToolExecutor_SetAndGetSearchPool(t *testing.T) {
	t.Parallel()

	te := NewToolExecutor("http://localhost:3000", nil)
	pool := llmTools("generic")
	te.setSearchPool(pool)

	got := te.getSearchPool()
	if len(got) != len(pool) {
		t.Errorf("expected pool len %d, got %d", len(pool), len(got))
	}
}

func TestToolExecutor_GetSearchPool_NilWhenNotSet(t *testing.T) {
	t.Parallel()

	te := NewToolExecutor("http://localhost:3000", nil)
	got := te.getSearchPool()
	if got != nil {
		t.Error("expected nil pool when not set")
	}
}

func TestToolExecutor_Execute_SearchToolsCase(t *testing.T) {
	t.Parallel()

	te := NewToolExecutor("http://localhost:3000", nil)
	// Pool not set -- falls back to full llmTools
	result, err := te.Execute(t.Context(), toolSearchToolName, `{"query":"prometheus"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var res searchToolResult
	if err := json.Unmarshal([]byte(result), &res); err != nil {
		t.Fatalf("invalid JSON result: %v", err)
	}
	if res.Found == 0 {
		t.Error("expected at least one prometheus-related tool")
	}
}
