package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	"time"
)

func TestToolExecutor_ListDatasources(t *testing.T) {
	t.Parallel()

	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/datasources" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"name": "Prometheus", "type": "prometheus", "uid": "prom-uid", "url": "http://prom:9090"},
			{"name": "Loki", "type": "loki", "uid": "loki-uid", "url": "http://loki:3100"},
		})
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	result, err := te.Execute(context.Background(), "list_datasources", "{}")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	var datasources []struct {
		Name string `json:"name"`
		Type string `json:"type"`
		UID  string `json:"uid"`
	}
	if err := json.Unmarshal([]byte(result), &datasources); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if len(datasources) != 2 {
		t.Fatalf("expected 2 datasources, got %d", len(datasources))
	}

	if datasources[0].Name != "Prometheus" || datasources[0].UID != "prom-uid" {
		t.Errorf("unexpected first datasource: %+v", datasources[0])
	}
}

func TestToolExecutor_ListCorrelations(t *testing.T) {
	t.Parallel()

	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/datasources/correlations" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"correlations": [
				{
					"sourceUID": "local-prometheus",
					"targetUID": "local-loki",
					"label": "Ver logs do demo-app",
					"description": "A partir de uma metrica, abre os logs do mesmo servico.",
					"config": {"field": "job", "target": {"expr": "{job=\"demo-app\"}", "queryType": "range"}}
				}
			],
			"totalCount": 1
		}`))
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	result, err := te.Execute(context.Background(), "list_correlations", "{}")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	var correlations []struct {
		SourceUID string `json:"sourceUID"`
		TargetUID string `json:"targetUID"`
		Field     string `json:"field"`
		Label     string `json:"label"`
	}
	if err := json.Unmarshal([]byte(result), &correlations); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if len(correlations) != 1 {
		t.Fatalf("expected 1 correlation, got %d", len(correlations))
	}
	if correlations[0].SourceUID != "local-prometheus" || correlations[0].TargetUID != "local-loki" || correlations[0].Field != "job" {
		t.Errorf("unexpected correlation: %+v", correlations[0])
	}
}

func TestToolExecutor_QueryPrometheus(t *testing.T) {
	t.Parallel()

	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/datasources":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"name": "Prometheus", "type": "prometheus", "uid": "prom-uid"},
			})
		default:
			// Datasource proxy query
			query := r.URL.Query().Get("query")
			if query == "" {
				t.Error("expected query parameter")
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{
					"resultType": "matrix",
					"result": []map[string]any{
						{
							"metric": map[string]string{"instance": "node1"},
							"values": [][]any{{float64(time.Now().Unix()), "0.45"}},
						},
					},
				},
			})
		}
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	args := `{"query":"rate(node_cpu_seconds_total[5m])","step":"60s"}`
	result, err := te.Execute(context.Background(), "query_prometheus", args)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result == "" {
		t.Error("expected non-empty result")
	}

	// Verify it contains metric data
	var promResp map[string]any
	if err := json.Unmarshal([]byte(result), &promResp); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	if promResp["status"] != "success" {
		t.Errorf("expected success status, got %v", promResp["status"])
	}
}

func TestToolExecutor_UnknownTool(t *testing.T) {
	t.Parallel()

	te := NewToolExecutor("http://localhost:1", log.DefaultLogger)
	_, err := te.Execute(context.Background(), "unknown_tool", "{}")
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
}

func TestToolExecutor_NoDatasource(t *testing.T) {
	t.Parallel()

	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	_, err := te.Execute(context.Background(), "query_prometheus", `{"query":"up"}`)
	if err == nil {
		t.Fatal("expected error when no datasource found")
	}
}

func TestResolveTime(t *testing.T) {
	t.Parallel()

	now := time.Unix(1700000000, 0) // Fixed Unix timestamp

	tests := []struct {
		input    string
		expected string
	}{
		{"now", "1700000000"},
		{"now-1h", "1699996400"},
		{"now-5m", "1699999700"},
		{"1700000000", "1700000000"},
	}

	for _, tt := range tests {
		got := resolveTime(tt.input, now)
		if got != tt.expected {
			t.Errorf("resolveTime(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// Regression test: resolveTimeValue used time.ParseDuration directly, which
// has no "d" (day) unit -- "now-7d"/"now-30d" (a natural, common ask) used
// to silently fall through with the duration parse simply ignored, still
// claiming success and returning `now` UNCHANGED -- a caller asking for the
// last 30 days silently got a zero-width "now..now" window instead of an
// error or the actual 30-day-ago timestamp. Found live validating
// build_change_timeline against real data outside 24h-old but inside 30d.
func TestResolveTime_DaySuffix(t *testing.T) {
	t.Parallel()

	now := time.Unix(1700000000, 0)
	wantDay := int64(86400)

	tests := []struct {
		input        string
		expectedUnix int64
	}{
		{"now-1d", now.Unix() - wantDay},
		{"now-7d", now.Unix() - 7*wantDay},
		{"now-30d", now.Unix() - 30*wantDay},
		{"7d", now.Unix() - 7*wantDay}, // bare duration, same "that long ago" reading as other bare units
	}

	for _, tt := range tests {
		got := resolveTime(tt.input, now)
		want := fmt.Sprintf("%d", tt.expectedUnix)
		if got != want {
			t.Errorf("resolveTime(%q) = %q, want %q (a real offset, not now unchanged)", tt.input, got, want)
		}
	}
}

// A genuinely unparseable relative expression must be left AS-IS (ok=false),
// same as an absolute timestamp -- never silently resolved to `now`.
func TestResolveTime_UnparseableSuffixLeftUnchanged(t *testing.T) {
	t.Parallel()

	now := time.Unix(1700000000, 0)
	for _, input := range []string{"now-3x", "now-", "now-abc"} {
		got := resolveTime(input, now)
		if got == fmt.Sprintf("%d", now.Unix()) {
			t.Errorf("resolveTime(%q) = %q, want it left unchanged (as %q) rather than silently resolved to now", input, got, input)
		}
		if got != input {
			t.Errorf("resolveTime(%q) = %q, want the original string unchanged", input, got)
		}
	}
}

func TestTruncateString(t *testing.T) {
	t.Parallel()

	short := "hello"
	if got := truncateString(short, 10); got != short {
		t.Errorf("expected %q, got %q", short, got)
	}

	long := "hello world this is a long string"
	got := truncateString(long, 10)
	if len(got) > 30 {
		t.Errorf("expected truncated string, got length %d", len(got))
	}
	if got != "hello worl... [truncated]" {
		t.Errorf("got %q", got)
	}
}

func TestToolExecutor_ListDashboards(t *testing.T) {
	t.Parallel()

	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/search" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("type") != "dash-db" {
			t.Errorf("expected type=dash-db, got %s", r.URL.Query().Get("type"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"title": "Kubernetes Overview", "uid": "k8s-001", "tags": []string{"kubernetes"}, "url": "/d/k8s-001"},
			{"title": "Node Metrics", "uid": "node-001", "tags": []string{"node"}, "url": "/d/node-001"},
		})
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	result, err := te.Execute(context.Background(), "list_dashboards", "{}")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	var dashboards []struct {
		Title string   `json:"title"`
		UID   string   `json:"uid"`
		Tags  []string `json:"tags"`
	}
	if err := json.Unmarshal([]byte(result), &dashboards); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if len(dashboards) != 2 {
		t.Fatalf("expected 2 dashboards, got %d", len(dashboards))
	}
	if dashboards[0].Title != "Kubernetes Overview" || dashboards[0].UID != "k8s-001" {
		t.Errorf("unexpected first dashboard: %+v", dashboards[0])
	}
	if len(dashboards[0].Tags) != 1 || dashboards[0].Tags[0] != "kubernetes" {
		t.Errorf("unexpected tags: %v", dashboards[0].Tags)
	}
}

func TestToolExecutor_ListDashboardsWithQuery(t *testing.T) {
	t.Parallel()

	var receivedQuery string
	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedQuery = r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	_, err := te.Execute(context.Background(), "list_dashboards", `{"query":"kubernetes"}`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if receivedQuery != "kubernetes" {
		t.Errorf("expected query=kubernetes, got %q", receivedQuery)
	}
}

func TestToolExecutor_GetDashboard(t *testing.T) {
	t.Parallel()

	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dashboards/uid/k8s-001" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"dashboard": map[string]any{
				"title":       "Kubernetes Overview",
				"description": "Cluster overview dashboard",
				"tags":        []string{"kubernetes"},
				"panels": []map[string]any{
					{
						"title": "CPU Usage",
						"type":  "timeseries",
						"targets": []map[string]any{
							{"expr": "rate(node_cpu_seconds_total[5m])", "refId": "A"},
						},
					},
					{
						"title": "Row: Storage",
						"type":  "row",
						"panels": []map[string]any{
							{
								"title": "Disk Usage",
								"type":  "gauge",
								"targets": []map[string]any{
									{"expr": "node_filesystem_avail_bytes", "refId": "A"},
								},
							},
						},
					},
				},
				"templating": map[string]any{
					"list": []map[string]any{
						{"name": "namespace", "current": map[string]string{"text": "default", "value": "default"}},
					},
				},
			},
		})
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	result, err := te.Execute(context.Background(), "get_dashboard", `{"uid":"k8s-001"}`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	var summary struct {
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
		Variables   []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"variables"`
		Panels []struct {
			Title   string   `json:"title"`
			Type    string   `json:"type"`
			Queries []string `json:"queries"`
		} `json:"panels"`
	}
	if err := json.Unmarshal([]byte(result), &summary); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if summary.Title != "Kubernetes Overview" {
		t.Errorf("expected title 'Kubernetes Overview', got %q", summary.Title)
	}
	if summary.Description != "Cluster overview dashboard" {
		t.Errorf("expected description, got %q", summary.Description)
	}
	// Should have 3 panels: CPU Usage, Row: Storage, Disk Usage (nested)
	if len(summary.Panels) != 3 {
		t.Fatalf("expected 3 panels, got %d", len(summary.Panels))
	}
	if summary.Panels[0].Title != "CPU Usage" {
		t.Errorf("expected first panel 'CPU Usage', got %q", summary.Panels[0].Title)
	}
	if len(summary.Panels[0].Queries) != 1 || summary.Panels[0].Queries[0] != "rate(node_cpu_seconds_total[5m])" {
		t.Errorf("unexpected queries: %v", summary.Panels[0].Queries)
	}
	// Nested panel
	if summary.Panels[2].Title != "Disk Usage" {
		t.Errorf("expected nested panel 'Disk Usage', got %q", summary.Panels[2].Title)
	}
	// Variables
	if len(summary.Variables) != 1 || summary.Variables[0].Name != "namespace" {
		t.Errorf("unexpected variables: %v", summary.Variables)
	}
}

func TestToolExecutor_GetDashboard_MissingUID(t *testing.T) {
	t.Parallel()

	te := NewToolExecutor("http://localhost:1", log.DefaultLogger)
	_, err := te.Execute(context.Background(), "get_dashboard", `{}`)
	if err == nil {
		t.Fatal("expected error for missing UID")
	}
}

func TestToolExecutor_ListAlerts(t *testing.T) {
	t.Parallel()

	var receivedPath string
	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		// Grafana's built-in Alertmanager API -- no external datasource lookup needed.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"labels":      map[string]string{"alertname": "HighCPU", "severity": "critical", "namespace": "default"},
				"annotations": map[string]string{"summary": "CPU usage is above 90%"},
				"state":       "firing",
				"startsAt":    "2026-03-29T10:00:00Z",
			},
			{
				"labels":      map[string]string{"alertname": "HighMemory", "severity": "warning"},
				"annotations": map[string]string{"summary": "Memory usage is above 80%"},
				"state":       "firing",
				"startsAt":    "2026-03-29T11:00:00Z",
			},
		})
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	result, err := te.Execute(context.Background(), "list_alerts", `{}`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if receivedPath != "/api/alertmanager/grafana/api/v2/alerts" {
		t.Errorf("expected built-in Alertmanager API path, got %q", receivedPath)
	}

	var alerts []map[string]any
	if err := json.Unmarshal([]byte(result), &alerts); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if len(alerts) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(alerts))
	}
}

func TestToolExecutor_ListAlerts_WithFilter(t *testing.T) {
	t.Parallel()

	var receivedFilter string
	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedFilter = r.URL.Query().Get("filter")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	_, err := te.Execute(context.Background(), "list_alerts", `{"filter":"severity=critical"}`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if receivedFilter != "severity=critical" {
		t.Errorf("expected filter=severity=critical, got %q", receivedFilter)
	}
}

func TestToolExecutor_AnalyzeActiveAlerts_NoFiringAlerts(t *testing.T) {
	t.Parallel()

	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"labels": map[string]string{"alertname": "Resolved"}, "state": "inactive"},
		})
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	result, err := te.Execute(context.Background(), "analyze_active_alerts", "{}")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result, "No firing alerts") {
		t.Errorf("result = %q, want a no-firing-alerts message", result)
	}
}

func TestToolExecutor_AnalyzeActiveAlerts_NoMCPStillReturnsRawAlerts(t *testing.T) {
	t.Parallel()

	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"labels": map[string]string{"alertname": "HighCPU", "namespace": "default"}, "state": "active"},
		})
	}))
	defer grafanaMock.Close()

	// No te.mcp configured -- degrades gracefully to raw alert data only.
	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	result, err := te.Execute(context.Background(), "analyze_active_alerts", "{}")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result, "HighCPU") {
		t.Errorf("result missing alert name: %q", result)
	}
	if strings.Contains(result, "BRAIN AGENT HISTORICAL CORRELATION") {
		t.Errorf("result should not mention brain-agent correlation without an mcp client: %q", result)
	}
}

func TestToolExecutor_AnalyzeActiveAlerts_WithMCPCorrelation(t *testing.T) {
	t.Parallel()

	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/alertmanager/grafana/api/v2/alerts":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"labels": map[string]string{"alertname": "HighCPU", "namespace": "default"}, "state": "active"},
			})
		case inTransitStatusPath:
			_ = json.NewEncoder(w).Encode(map[string]bool{"enabled": false})
		case mcpToolsPath:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"result": map[string]any{
					"content": []map[string]string{
						{"type": "text", "text": "2026-01-10: HighCPU root-caused to a runaway batch job"},
					},
				},
			})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	te.mcp = newMCPClient(grafanaMock.URL, func() string { return "token" }, log.DefaultLogger)

	result, err := te.Execute(context.Background(), "analyze_active_alerts", "{}")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result, "BRAIN AGENT HISTORICAL CORRELATION") {
		t.Errorf("result missing correlation marker: %q", result)
	}
	if !strings.Contains(result, "runaway batch job") {
		t.Errorf("result missing memory content: %q", result)
	}
}

func TestToolExecutor_AnalyzeActiveAlerts_GroupsByAlertname(t *testing.T) {
	t.Parallel()

	var mcpCalls int32
	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/alertmanager/grafana/api/v2/alerts":
			w.Header().Set("Content-Type", "application/json")
			// Same alertname firing on 3 pods -- must become ONE group with
			// ONE search_memory call, not 3.
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"labels": map[string]string{"alertname": "HighCPU", "namespace": "prod", "pod": "checkout-1"}, "state": "active"},
				{"labels": map[string]string{"alertname": "HighCPU", "namespace": "prod", "pod": "checkout-2"}, "state": "active"},
				{"labels": map[string]string{"alertname": "HighCPU", "namespace": "prod", "pod": "checkout-3"}, "state": "active"},
				{"labels": map[string]string{"alertname": "DiskFull", "namespace": "prod", "pod": "db-1"}, "state": "active"},
			})
		case inTransitStatusPath:
			_ = json.NewEncoder(w).Encode(map[string]bool{"enabled": false})
		case mcpToolsPath:
			atomic.AddInt32(&mcpCalls, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": 1,
				"result": map[string]any{"content": []map[string]string{{"type": "text", "text": "no matches found"}}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	te.mcp = newMCPClient(grafanaMock.URL, func() string { return "token" }, log.DefaultLogger)

	result, err := te.Execute(context.Background(), "analyze_active_alerts", "{}")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result, "4 firing alert(s) in 2 group(s)") {
		t.Errorf("result = %q, want it to report 4 alerts in 2 groups", result)
	}
	if !strings.Contains(result, "HighCPU (3 instance(s))") {
		t.Errorf("result = %q, want the HighCPU group to show 3 instances", result)
	}
	// One search_memory call per GROUP (2), not per alert instance (4).
	if n := atomic.LoadInt32(&mcpCalls); n != 2 {
		t.Errorf("mcp called %d times, want 2 (one per group)", n)
	}
}

func TestToolExecutor_TokenPath_ReadsFromFile(t *testing.T) {
	t.Parallel()

	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("glsa_file_token_123"), 0o600); err != nil {
		t.Fatal(err)
	}

	var receivedAuth string
	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	te.tokenPath = tokenFile
	_, _ = te.Execute(context.Background(), "list_datasources", "{}")

	if receivedAuth != "Bearer glsa_file_token_123" {
		t.Errorf("expected 'Bearer glsa_file_token_123', got %q", receivedAuth)
	}
}

func TestToolExecutor_TokenPath_OverridesDefaultHeaders(t *testing.T) {
	t.Parallel()

	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("glsa_file_token"), 0o600); err != nil {
		t.Fatal(err)
	}

	var receivedAuth string
	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	te.defaultHeaders = map[string]string{
		"Authorization": "Bearer old_static_token",
	}
	te.tokenPath = tokenFile
	_, _ = te.Execute(context.Background(), "list_datasources", "{}")

	if receivedAuth != "Bearer glsa_file_token" {
		t.Errorf("expected file token to override static, got %q", receivedAuth)
	}
}

func TestToolExecutor_TokenPath_TrimsWhitespace(t *testing.T) {
	t.Parallel()

	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("  glsa_trimmed  \n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var receivedAuth string
	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	te.tokenPath = tokenFile
	_, _ = te.Execute(context.Background(), "list_datasources", "{}")

	if receivedAuth != "Bearer glsa_trimmed" {
		t.Errorf("expected trimmed token, got %q", receivedAuth)
	}
}

func TestToolExecutor_TokenPath_MissingFileFallsBack(t *testing.T) {
	t.Parallel()

	var receivedAuth string
	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	te.defaultHeaders = map[string]string{
		"Authorization": "Bearer static_fallback",
	}
	te.tokenPath = "/nonexistent/path/token"
	_, _ = te.Execute(context.Background(), "list_datasources", "{}")

	if receivedAuth != "Bearer static_fallback" {
		t.Errorf("expected static fallback when file missing, got %q", receivedAuth)
	}
}

func TestToolExecutor_TokenPath_EmptyFileFallsBack(t *testing.T) {
	t.Parallel()

	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	var receivedAuth string
	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	te.defaultHeaders = map[string]string{
		"Authorization": "Bearer static_fallback",
	}
	te.tokenPath = tokenFile
	_, _ = te.Execute(context.Background(), "list_datasources", "{}")

	if receivedAuth != "Bearer static_fallback" {
		t.Errorf("expected static fallback when file empty, got %q", receivedAuth)
	}
}

func TestToolExecutor_TokenPath_PicksUpNewToken(t *testing.T) {
	t.Parallel()

	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("token_v1"), 0o600); err != nil {
		t.Fatal(err)
	}

	var receivedAuth string
	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	te.tokenPath = tokenFile

	// First request uses v1
	_, _ = te.Execute(context.Background(), "list_datasources", "{}")
	if receivedAuth != "Bearer token_v1" {
		t.Fatalf("first request: expected token_v1, got %q", receivedAuth)
	}

	// Update file
	if err := os.WriteFile(tokenFile, []byte("token_v2"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Second request picks up v2
	_, _ = te.Execute(context.Background(), "list_datasources", "{}")
	if receivedAuth != "Bearer token_v2" {
		t.Errorf("second request: expected token_v2, got %q", receivedAuth)
	}
}

func TestToolExecutor_QueryPrometheus_EscapesDsUID(t *testing.T) {
	t.Parallel()

	var receivedRawPath string
	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawPath != "" {
			receivedRawPath = r.URL.RawPath
		} else {
			receivedRawPath = r.URL.Path
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/datasources" {
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"name": "Prometheus", "type": "prometheus", "uid": "uid/with/../traversal"},
			})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data":   map[string]any{"resultType": "matrix", "result": []any{}},
			})
		}
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	_, err := te.Execute(context.Background(), "query_prometheus", `{"query":"up"}`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// The path-traversal characters should be percent-encoded
	if strings.Contains(receivedRawPath, "/../") {
		t.Errorf("dsUID was not path-escaped: %s", receivedRawPath)
	}
	if !strings.Contains(receivedRawPath, "%2F") {
		t.Errorf("expected percent-encoded slashes in path, got: %s", receivedRawPath)
	}
}

func TestToolExecutor_QueryPrometheus_ExplicitDatasourceUIDBypassesAutoDiscovery(t *testing.T) {
	t.Parallel()

	var datasourcesCalls int
	var proxyUID string
	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/datasources" {
			datasourcesCalls++
			// Two Prometheus datasources -- auto-discovery alone would be
			// ambiguous, but an explicit datasource_uid must skip it
			// entirely and go straight to the one named.
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"name": "Prometheus (cluster-a)", "type": "prometheus", "uid": "prom-a"},
				{"name": "Prometheus (cluster-b)", "type": "prometheus", "uid": "prom-b"},
			})
			return
		}
		if strings.Contains(r.URL.Path, "/proxy/uid/") {
			proxyUID = strings.Split(strings.TrimPrefix(r.URL.Path, "/api/datasources/proxy/uid/"), "/")[0]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data":   map[string]any{"resultType": "matrix", "result": []any{}},
		})
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	_, err := te.Execute(context.Background(), "query_prometheus", `{"query":"up","datasource_uid":"prom-b"}`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if proxyUID != "prom-b" {
		t.Errorf("proxy uid = %q, want the explicitly requested prom-b", proxyUID)
	}
	if datasourcesCalls != 0 {
		t.Errorf("expected 0 /api/datasources calls when datasource_uid is already given, got %d", datasourcesCalls)
	}
}

func TestToolExecutor_QueryPrometheus_AmbiguousWithoutDatasourceUIDErrors(t *testing.T) {
	t.Parallel()

	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"name": "Prometheus (cluster-a)", "type": "prometheus", "uid": "prom-a"},
			{"name": "Prometheus (cluster-b)", "type": "prometheus", "uid": "prom-b"},
		})
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	_, err := te.Execute(context.Background(), "query_prometheus", `{"query":"up"}`)
	if err == nil {
		t.Fatal("expected an error when 2 Prometheus datasources exist and none was specified")
	}
	if !strings.Contains(err.Error(), "prom-a") || !strings.Contains(err.Error(), "prom-b") {
		t.Errorf("error = %q, want it to name both candidates", err.Error())
	}
}

func TestToolExecutor_ListAlerts_NoDuplicateAppend(t *testing.T) {
	t.Parallel()

	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Alert has BOTH top-level state and nested status.state
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"labels": map[string]string{"alertname": "Test"},
				"state":  "active",
				"status": map[string]any{"state": "active"},
			},
		})
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	result, err := te.Execute(context.Background(), "list_alerts", `{"state":"firing"}`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	var alerts []map[string]any
	if err := json.Unmarshal([]byte(result), &alerts); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Should appear exactly once, not twice
	if len(alerts) != 1 {
		t.Errorf("expected 1 alert (no duplicates), got %d", len(alerts))
	}
}

func TestToolExecutor_ListAlertRules(t *testing.T) {
	t.Parallel()

	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ruler/grafana/api/v1/rules" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string][]map[string]any{
			"default": {
				{
					"name": "HighCPU",
					"rules": []map[string]any{
						{
							"alert":  "HighCPU",
							"expr":   "rate(node_cpu_seconds_total[5m]) > 0.9",
							"labels": map[string]string{"severity": "critical"},
							"annotations": map[string]string{
								"summary":     "CPU usage high",
								"description": "Node CPU exceeds 90%",
							},
						},
					},
				},
			},
		})
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	result, err := te.Execute(context.Background(), "list_alert_rules", `{}`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result == "" {
		t.Fatal("expected non-empty result")
	}

	// Verify it's valid JSON
	var parsed any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
}

func TestTruncateString_NoTruncation(t *testing.T) {
	t.Parallel()
	input := "short"
	got := truncateString(input, 100)
	if got != input {
		t.Errorf("truncateString(%q, 100) = %q, want unchanged", input, got)
	}
}

func TestTruncateString_TruncatesASCII(t *testing.T) {
	t.Parallel()
	input := "Hello, World!"
	got := truncateString(input, 5)
	if got != "Hello... [truncated]" {
		t.Errorf("truncateString(%q, 5) = %q", input, got)
	}
}

func TestTruncateString_DoesNotSplitMultiByte(t *testing.T) {
	t.Parallel()
	// 🔥 is 4 bytes. Build "🔥🔥" = 8 bytes, truncate at 5 bytes
	input := "🔥🔥"
	got := truncateString(input, 5)
	// Should walk back to byte 4 (start of second 🔥) and truncate there
	if !strings.HasPrefix(got, "🔥") {
		t.Errorf("expected prefix '🔥', got %q", got)
	}
	// Verify valid UTF-8
	for _, r := range got {
		if r == '\uFFFD' {
			t.Error("found replacement character — truncation split a multi-byte rune")
		}
	}
}

func TestTruncateString_CJKCharacters(t *testing.T) {
	t.Parallel()
	// 你 = 3 bytes, 好 = 3 bytes, 世 = 3 bytes, 界 = 3 bytes = 12 bytes
	input := "你好世界"
	got := truncateString(input, 7)
	// Should truncate at byte 6 (end of 好) since byte 7 is mid-rune
	if !strings.HasPrefix(got, "你好") {
		t.Errorf("expected prefix '你好', got %q", got)
	}
	for _, r := range got {
		if r == '\uFFFD' {
			t.Error("found replacement character")
		}
	}
}

func TestToolExecutor_DatasourceCacheHit(t *testing.T) {
	t.Parallel()

	callCount := 0
	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/datasources" {
			callCount++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"name": "Prometheus", "type": "prometheus", "uid": "prom-uid"},
				{"name": "Loki", "type": "loki", "uid": "loki-uid"},
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success"}`))
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)

	// First call populates cache
	uid1, err := te.resolveDatasourceUID(context.Background(), "prometheus", "")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if uid1 != "prom-uid" {
		t.Errorf("uid = %q, want prom-uid", uid1)
	}

	// Second call should use cache (no additional /api/datasources request)
	uid2, err := te.resolveDatasourceUID(context.Background(), "loki", "")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if uid2 != "loki-uid" {
		t.Errorf("uid = %q, want loki-uid", uid2)
	}

	if callCount != 1 {
		t.Errorf("expected 1 /api/datasources call, got %d", callCount)
	}
}

func TestResolveDatasourceUID_ProvidedUIDAlwaysWinsWithoutAnAPICall(t *testing.T) {
	t.Parallel()

	callCount := 0
	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"name": "Prometheus", "type": "prometheus", "uid": "prom-uid"},
		})
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)

	uid, err := te.resolveDatasourceUID(context.Background(), "prometheus", "explicit-uid")
	if err != nil {
		t.Fatalf("resolveDatasourceUID failed: %v", err)
	}
	if uid != "explicit-uid" {
		t.Errorf("uid = %q, want the explicitly provided one, unchanged", uid)
	}
	if callCount != 0 {
		t.Errorf("expected 0 /api/datasources calls when a datasource_uid is already provided, got %d", callCount)
	}
}

func TestResolveDatasourceUID_MultipleOfSameTypeErrorsNamingCandidates(t *testing.T) {
	t.Parallel()

	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"name": "Prometheus (cluster-a)", "type": "prometheus", "uid": "prom-a"},
			{"name": "Prometheus (cluster-b)", "type": "prometheus", "uid": "prom-b"},
		})
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)

	_, err := te.resolveDatasourceUID(context.Background(), "prometheus", "")
	if err == nil {
		t.Fatal("expected an error when more than one datasource of the type exists and none was specified")
	}
	if !strings.Contains(err.Error(), "prom-a") || !strings.Contains(err.Error(), "prom-b") {
		t.Errorf("error = %q, want it to name both candidate UIDs", err.Error())
	}
	if !strings.Contains(err.Error(), "list_datasources") {
		t.Errorf("error = %q, want it to point the caller at list_datasources", err.Error())
	}
}

func TestResolveDatasourceUID_NoneOfTypeIsAClearError(t *testing.T) {
	t.Parallel()

	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"name": "Loki", "type": "loki", "uid": "loki-uid"},
		})
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)

	_, err := te.resolveDatasourceUID(context.Background(), "tempo", "")
	if err == nil {
		t.Fatal("expected an error when no datasource of the type exists")
	}
	if !strings.Contains(err.Error(), "tempo") {
		t.Errorf("error = %q, want it to name the missing type", err.Error())
	}
}

// Security-audit finding L1: list_folders' underlying search hardcodes
// limit=searchFolderLimit with no pagination -- an instance with more
// folders than that silently returns an incomplete list. The chosen fix is
// signaling the truncation rather than paginating, so the LLM can decide to
// refine its query instead of treating a cut-off list as complete.
func TestToolExecutor_ListFolders_NotesTruncationAtLimit(t *testing.T) {
	t.Parallel()

	folders := make([]map[string]any, searchFolderLimit)
	for i := range folders {
		folders[i] = map[string]any{"title": "folder", "uid": "uid", "folderUid": ""}
	}

	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(folders)
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	result, err := te.Execute(context.Background(), "list_folders", "{}")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !strings.Contains(result, "truncated") {
		t.Errorf("result with %d folders (== searchFolderLimit) should note truncation, got: %s", searchFolderLimit, result)
	}
}

func TestToolExecutor_ListFolders_NoTruncationNoteBelowLimit(t *testing.T) {
	t.Parallel()

	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"title": "folder-a", "uid": "uid-a", "folderUid": ""},
		})
	}))
	defer grafanaMock.Close()

	te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
	result, err := te.Execute(context.Background(), "list_folders", "{}")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if strings.Contains(result, "truncated") {
		t.Errorf("result with 1 folder should not note truncation, got: %s", result)
	}
}

// The datasource allowlist is a security control, not a hint: a UID the model
// picked up from a dashboard, an alert rule, or the panel context never goes
// through the filtered discovery path, so the check has to sit on the
// resolution itself.
func TestResolveDatasourceUID_Allowlist(t *testing.T) {
	grafanaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"name":"Prom A","type":"prometheus","uid":"allowed"},
			{"name":"Prom B","type":"prometheus","uid":"forbidden"}
		]`))
	}))
	defer grafanaMock.Close()

	t.Run("rejects an explicit uid outside the list", func(t *testing.T) {
		te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
		te.allowedDatasourceUIDs = map[string]bool{"allowed": true}

		if _, err := te.resolveDatasourceUID(context.Background(), "prometheus", "forbidden"); err == nil {
			t.Fatal("a uid outside the allowlist must be refused")
		}
	})

	t.Run("accepts an allowed explicit uid", func(t *testing.T) {
		te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
		te.allowedDatasourceUIDs = map[string]bool{"allowed": true}

		uid, err := te.resolveDatasourceUID(context.Background(), "prometheus", "allowed")
		if err != nil || uid != "allowed" {
			t.Fatalf("uid=%q err=%v", uid, err)
		}
	})

	t.Run("auto-resolution skips excluded datasources", func(t *testing.T) {
		// Two datasources of the same type, only one allowed: resolution must
		// succeed instead of complaining about ambiguity.
		te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
		te.allowedDatasourceUIDs = map[string]bool{"allowed": true}

		uid, err := te.resolveDatasourceUID(context.Background(), "prometheus", "")
		if err != nil || uid != "allowed" {
			t.Fatalf("uid=%q err=%v", uid, err)
		}
	})

	t.Run("empty list means no restriction", func(t *testing.T) {
		te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)

		if _, err := te.resolveDatasourceUID(context.Background(), "prometheus", "forbidden"); err != nil {
			t.Fatalf("without an allowlist every uid must pass: %v", err)
		}
	})

	t.Run("list_datasources hides what is excluded", func(t *testing.T) {
		te := NewToolExecutor(grafanaMock.URL, log.DefaultLogger)
		te.allowedDatasourceUIDs = map[string]bool{"allowed": true}

		out, err := te.listDatasources(context.Background())
		if err != nil {
			t.Fatalf("listDatasources: %v", err)
		}
		if strings.Contains(out, "forbidden") {
			t.Errorf("an excluded datasource must not be offered to the model: %s", out)
		}
		if !strings.Contains(out, "allowed") {
			t.Errorf("the allowed datasource must stay visible: %s", out)
		}
	})
}
