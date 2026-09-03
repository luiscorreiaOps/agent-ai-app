package plugin

import (
	"context"
	"encoding/json"

	openai "github.com/sashabaranov/go-openai"
)

// allTools returns every tool exposed to the LLM this turn: the static,
// hand-integrated set below plus (when brain-agent is detected and enabled)
// its memory tools -- store_memory, search_memory, delete_memory,
// brain_diagnostics, search_memory_by_time, condense_memory. See mcp.go.
func (a *App) allTools(ctx context.Context, agent string) []openai.Tool {
	tools := llmTools(agent)
	if a.toolExecutor != nil && a.toolExecutor.mcp != nil {
		tools = append(tools, a.toolExecutor.mcp.Tools(ctx)...)
	}
	// search_web (Internet Tools) is exposed only when every gate is true:
	// the admin turned EnableInternetTools on, a backend client actually got
	// constructed, AND AdvertisedAvailable() is currently positive. Any
	// current/future internet-backed tool must go behind this same gate. For
	// SearXNG/Gateway that's a real cached health check (a missing/expired/
	// negative cache means "unavailable" here, never "assume it's fine");
	// for the DuckDuckGo default it's always true -- see AdvertisedAvailable's
	// own doc comment for why. Either way this only ever reads a cache/field,
	// never blocks this call on a live network check.
	if a.settings.EnableInternetTools != nil &&
		*a.settings.EnableInternetTools &&
		a.toolExecutor != nil &&
		a.toolExecutor.onlineSearch != nil &&
		a.toolExecutor.onlineSearch.AdvertisedAvailable() {
		tools = append(tools, onlineSearchTool())
	}
	if agent == "generic" && a.settings.LightModeForDefaultAgent {
		tools = filterEnabledTools(tools, []string{"list_dashboards", "get_dashboard", "list_folders", "list_alerts", "dispatch_worker"})
	}
	return filterEnabledTools(tools, a.settings.EnabledTools)
}

// filterEnabledTools restricts tools to only those named in enabled, when
// enabled is non-empty -- an empty/nil enabled list means "no restriction",
// returning tools unchanged (the default, existing behavior). A name in
// enabled that doesn't match any real tool has no effect (not an error) --
// this only ever narrows the list, never adds to it.
func filterEnabledTools(tools []openai.Tool, enabled []string) []openai.Tool {
	if len(enabled) == 0 {
		return tools
	}
	allow := make(map[string]bool, len(enabled))
	for _, name := range enabled {
		allow[name] = true
	}
	filtered := make([]openai.Tool, 0, len(tools))
	for _, t := range tools {
		if t.Function != nil && allow[t.Function.Name] {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// llmTools returns the set of tools exposed to the LLM for data queries.
// dispatch_worker is intentionally available only to configured specialist
// agents (agent-N), never to the generic Default assistant.
func llmTools(agent string) []openai.Tool {
	tools := []openai.Tool{
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "dispatch_worker",
				Description: "Investigate a subtask that needs multiple tool calls (logs, metrics, traces, or alerts), and returns a short summary of what it found. Call this instead of query_prometheus/query_loki/query_tempo/list_alerts whenever the user asks you to investigate, check, or look into something with more than one word of description (e.g. \"check for errors in the checkout service\", \"look for a metric anomaly\") -- pick worker_type from what the subtask is about. Prefer calling this two or three times over doing the investigation yourself: each call investigates independently and in parallel.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"worker_type": {
							"type": "string",
							"enum": ["log_investigator", "metric_investigator", "trace_investigator", "general_investigator"],
							"description": "Which focused specialist to dispatch: log_investigator (Loki logs/log patterns), metric_investigator (Prometheus metrics/anomalies/capacity/SLO burn rate), trace_investigator (Tempo traces/bottlenecks), general_investigator (alerts/dashboards/anything else)."
						},
						"task": {
							"type": "string",
							"description": "A specific, self-contained subtask description for the worker to investigate. The worker will NOT see the rest of this conversation, so include every detail it needs (service/namespace names, time range, what to look for)."
						}
					},
					"required": ["worker_type", "task"]
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "query_prometheus",
				Description: "Execute a PromQL query against the configured Prometheus/Mimir datasource, and return current metric values. Use this when you need actual numeric data about system state, performance, incidents, SRE dashboards, or resource usage.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"datasource_uid": {
							"type": "string",
							"description": "Optional: target a specific Prometheus datasource by UID when this Grafana has more than one (call list_datasources to see them). If there is more than one and this is omitted, the call fails asking for it explicitly."
						},
						"query": {
							"type": "string",
							"description": "PromQL expression, e.g. rate(node_cpu_seconds_total{mode=\"idle\"}[5m])"
						},
						"start": {
							"type": "string",
							"description": "Start time as RFC3339 or relative like 'now-1h'. Defaults to now-5m."
						},
						"end": {
							"type": "string",
							"description": "End time as RFC3339 or relative like 'now'. Defaults to now."
						},
						"step": {
							"type": "string",
							"description": "Query step interval, e.g. 15s, 1m, 5m. Defaults to 60s."
						}
					},
					"required": ["query"]
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "query_loki",
				Description: "Execute a LogQL query against the configured Loki datasource and return log lines or metric results. Use this to explain logs, investigate incidents, and correlate errors with namespaces, pods, and dashboards.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"datasource_uid": {
							"type": "string",
							"description": "Optional: target a specific Loki datasource by UID when this Grafana has more than one (call list_datasources to see them). If there is more than one and this is omitted, the call fails asking for it explicitly."
						},
						"query": {
							"type": "string",
							"description": "LogQL expression, e.g. {namespace=\"default\"} |= \"error\""
						},
						"start": {
							"type": "string",
							"description": "Start time as RFC3339 or relative like 'now-1h'. Defaults to now-1h."
						},
						"end": {
							"type": "string",
							"description": "End time as RFC3339 or relative like 'now'. Defaults to now."
						},
						"limit": {
							"type": "integer",
							"description": "Maximum number of log lines to return. Defaults to 100."
						}
					},
					"required": ["query"]
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "list_loki_labels",
				Description: "Discover the real Loki label names in use (no args), or the real values for one label (pass \"label\", e.g. \"job\"). ALWAYS call this before query_loki if you are not 100% certain of the exact label name/value to filter on -- guessing label keys (app vs job vs application vs namespace, etc.) wastes calls and often returns zero results even when matching logs exist. A saved dashboard's own Loki panel query is also a reliable source of the correct label scheme, via get_dashboard.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"datasource_uid": {
							"type": "string",
							"description": "Optional: target a specific Loki datasource by UID when this Grafana has more than one (call list_datasources to see them). If there is more than one and this is omitted, the call fails asking for it explicitly."
						},
						"label": {
							"type": "string",
							"description": "Optional: a specific label name (e.g. \"job\") to list its real values. Omit to list all available label names first."
						},
						"start": {
							"type": "string",
							"description": "Start time as RFC3339 or relative like 'now-24h'. Defaults to now-24h."
						},
						"end": {
							"type": "string",
							"description": "End time as RFC3339 or relative like 'now'. Defaults to now."
						}
					},
					"required": []
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "analyze_metric_anomaly",
				Description: "Compare a PromQL metric's current values against the SAME query run over a baseline window shifted back in time (yesterday, last week), and flag points that deviate significantly from the baseline. Use this INSTEAD OF query_prometheus when the question is about whether something looks abnormal/unusual, not just what the current value is.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"datasource_uid": {
							"type": "string",
							"description": "Optional: target a specific Prometheus datasource by UID when this Grafana has more than one (call list_datasources to see them). If there is more than one and this is omitted, the call fails asking for it explicitly."
						},
						"query": {
							"type": "string",
							"description": "PromQL expression, e.g. rate(http_requests_total{status=\"500\"}[5m])"
						},
						"start": {
							"type": "string",
							"description": "Start time of the CURRENT window, RFC3339 or relative like 'now-1h'. Defaults to now-1h."
						},
						"end": {
							"type": "string",
							"description": "End time of the CURRENT window. Defaults to now."
						},
						"step": {
							"type": "string",
							"description": "Query step interval, e.g. 15s, 1m, 5m. Defaults to 60s."
						},
						"baseline_offset": {
							"type": "string",
							"description": "How far back to shift the whole window for the baseline comparison, e.g. '1d' (yesterday, default) or '7d' (last week)."
						}
					},
					"required": ["query"]
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "forecast_capacity",
				Description: "Fit a linear trend over a PromQL metric's recent history and project forward to estimate when it crosses a given threshold (e.g. 'when does disk usage hit 100%', 'when do we run out of connections'). The query MUST return exactly one series (aggregate with sum()/avg() first if it would otherwise return several, e.g. one per pod).",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"datasource_uid": {
							"type": "string",
							"description": "Optional: target a specific Prometheus datasource by UID when this Grafana has more than one (call list_datasources to see them). If there is more than one and this is omitted, the call fails asking for it explicitly."
						},
						"query": {
							"type": "string",
							"description": "PromQL expression returning exactly one series, e.g. avg(disk_used_percent{device=\"/data\"})"
						},
						"start": {
							"type": "string",
							"description": "Start of the history window to fit the trend on, RFC3339 or relative like 'now-24h'. Defaults to now-24h."
						},
						"end": {
							"type": "string",
							"description": "End of the history window. Defaults to now."
						},
						"step": {
							"type": "string",
							"description": "Query step interval, e.g. 1m, 5m. Defaults to 5m."
						},
						"threshold": {
							"type": "number",
							"description": "The value to project a crossing against, e.g. 100 for a percentage hitting 100%"
						},
						"direction": {
							"type": "string",
							"description": "\"rising\" (default -- e.g. disk usage growing toward the threshold) or \"falling\" (e.g. free space shrinking toward it)"
						}
					},
					"required": ["query", "threshold"]
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "diagnose_kubernetes_workload",
				Description: "Cross-references kube-state-metrics for a Kubernetes workload's restarts, memory/CPU usage vs configured limits, ready-vs-desired replicas, AND whether any of its containers are currently stuck in a bad waiting state (ImagePullBackOff, CrashLoopBackOff, ContainerCreating stuck, etc.). That last check matters even when restarts=0 and ready looks fine -- a container that has never successfully started has zero restarts by definition, and a rolling-update deployment can still show its old replicas as \"available\" while a new one is stuck. Requires kube-state-metrics and cAdvisor container metrics to be scraped by this instance's Prometheus -- NOT guaranteed in every deployment; a \"found: false\" check means the metric wasn't available here, not that the workload is healthy.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"datasource_uid": {
							"type": "string",
							"description": "Optional: target a specific Prometheus datasource by UID when this Grafana has more than one (call list_datasources to see them). If there is more than one and this is omitted, the call fails asking for it explicitly."
						},
						"namespace": {
							"type": "string",
							"description": "Kubernetes namespace"
						},
						"name": {
							"type": "string",
							"description": "Workload/deployment/pod name prefix"
						}
					},
					"required": ["namespace", "name"]
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "analyze_container_lifecycle",
				Description: "Discovers why a container died: kube-state-metrics' last-terminated reason (OOMKilled, Error, Completed, ...) and exit code, its memory usage vs limit, plus recent Loki logs (grouped by pattern) from around the same window -- AND whether it's currently stuck in a bad waiting state (ImagePullBackOff, CrashLoopBackOff, ContainerCreating stuck, etc.) instead of having died at all. That last check matters even when there's no terminated-reason/exit-code data -- a container that never successfully started has neither. Requires kube-state-metrics/cAdvisor -- NOT guaranteed in every deployment.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"datasource_uid": {
							"type": "string",
							"description": "Optional: target a specific Prometheus datasource by UID when this Grafana has more than one (call list_datasources to see them). If there is more than one and this is omitted, the call fails asking for it explicitly."
						},
						"namespace": {
							"type": "string",
							"description": "Kubernetes namespace"
						},
						"pod": {
							"type": "string",
							"description": "Pod name prefix"
						}
					},
					"required": ["namespace", "pod"]
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "analyze_node_health",
				Description: "Checks a node/instance's own resource pressure via node_exporter: CPU busy percent, I/O wait percent, memory used percent, and the worst filesystem's disk used percent. Goes beyond pod-level checks to the underlying machine. Requires node_exporter to be scraped by this instance's Prometheus -- NOT guaranteed in every deployment.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"datasource_uid": {
							"type": "string",
							"description": "Optional: target a specific Prometheus datasource by UID when this Grafana has more than one (call list_datasources to see them). If there is more than one and this is omitted, the call fails asking for it explicitly."
						},
						"instance": {
							"type": "string",
							"description": "node_exporter's \"instance\" label value or a substring of it (e.g. a hostname), matched with a regex"
						}
					},
					"required": ["instance"]
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "inspect_kubernetes_events",
				Description: "Groups Kubernetes events (FailedScheduling, OOMKilling, ...) already shipped to Loki, by involvedObject+reason when they're structured JSON, or by normalized text pattern otherwise -- deduplicated, top occurrences only. Sourced from Loki, NOT a direct Kubernetes API call. Use list_loki_labels first if you don't know how events are shipped to Loki in this environment.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"datasource_uid": {
							"type": "string",
							"description": "Optional: target a specific Loki datasource by UID when this Grafana has more than one (call list_datasources to see them). If there is more than one and this is omitted, the call fails asking for it explicitly."
						},
						"selector": {
							"type": "string",
							"description": "LogQL stream selector for wherever Kubernetes events are shipped, e.g. {job=\"kube-events\"}"
						},
						"start": {
							"type": "string",
							"description": "Start time as RFC3339 or relative like 'now-1h'. Defaults to now-1h."
						},
						"end": {
							"type": "string",
							"description": "End time as RFC3339 or relative like 'now'. Defaults to now."
						},
						"limit": {
							"type": "integer",
							"description": "Maximum number of log lines to scan before grouping. Defaults to 500, capped at 2000."
						}
					},
					"required": ["selector"]
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "analyze_slo_burn_rate",
				Description: "Computes how fast an error budget is being consumed (burn rate) from your own good/total PromQL queries for an SLI and an SLO target -- e.g. good_query='sum(increase(http_requests_total{status!~\"5..\"}[1h]))', total_query='sum(increase(http_requests_total[1h]))', slo_target=0.999. A burn rate above 1 means the budget is being consumed faster than the SLO period allows. Requires the org to already have real good/total queries defined -- this tool does no SLO definition of its own, just the arithmetic.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"datasource_uid": {
							"type": "string",
							"description": "Optional: target a specific Prometheus datasource by UID when this Grafana has more than one (call list_datasources to see them). If there is more than one and this is omitted, the call fails asking for it explicitly."
						},
						"good_query": {
							"type": "string",
							"description": "Instant PromQL expression returning ONE number: count of 'good' events in the window, e.g. sum(increase(http_requests_total{status!~\"5..\"}[1h]))"
						},
						"total_query": {
							"type": "string",
							"description": "Instant PromQL expression returning ONE number: count of ALL events in the same window"
						},
						"slo_target": {
							"type": "number",
							"description": "The SLO target as a fraction, e.g. 0.999 for 99.9%"
						},
						"budget_window": {
							"type": "string",
							"description": "Optional: the SLO period, e.g. '30d', to also estimate time-to-exhaustion at the current burn rate"
						}
					},
					"required": ["good_query", "total_query", "slo_target"]
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "analyze_log_patterns",
				Description: "Search Loki logs and group the results by normalized pattern (UUIDs, timestamps, IPs, and numbers replaced with placeholders), instead of returning raw lines. Use this INSTEAD OF query_loki whenever you expect many similar log lines (investigating an error, a noisy service) -- it tells you which distinct message patterns occurred and how often, rather than making you re-derive that from hundreds of near-duplicate lines.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"datasource_uid": {
							"type": "string",
							"description": "Optional: target a specific Loki datasource by UID when this Grafana has more than one (call list_datasources to see them). If there is more than one and this is omitted, the call fails asking for it explicitly."
						},
						"selector": {
							"type": "string",
							"description": "LogQL expression, e.g. {namespace=\"default\"} |= \"error\""
						},
						"start": {
							"type": "string",
							"description": "Start time as RFC3339 or relative like 'now-1h'. Defaults to now-1h."
						},
						"end": {
							"type": "string",
							"description": "End time as RFC3339 or relative like 'now'. Defaults to now."
						},
						"limit": {
							"type": "integer",
							"description": "Maximum number of log lines to scan before grouping. Defaults to 500, capped at 2000."
						}
					},
					"required": ["selector"]
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "query_tempo",
				Description: "Query the configured Tempo datasource. Use TraceQL search to find traces during incidents, or provide a traceID to inspect a specific distributed trace and correlate spans with logs, metrics, dashboards, and alerts.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"datasource_uid": {
							"type": "string",
							"description": "Optional: target a specific Tempo datasource by UID when this Grafana has more than one (call list_datasources to see them). If there is more than one and this is omitted, the call fails asking for it explicitly."
						},
						"query": {
							"type": "string",
							"description": "TraceQL search expression, e.g. { status = error } or { duration > 2s }. Leave empty when traceID is provided."
						},
						"traceID": {
							"type": "string",
							"description": "Specific trace ID to fetch from Tempo."
						},
						"start": {
							"type": "string",
							"description": "Start time as RFC3339 or relative like 'now-1h'. Defaults to now-1h."
						},
						"end": {
							"type": "string",
							"description": "End time as RFC3339 or relative like 'now'. Defaults to now."
						},
						"limit": {
							"type": "integer",
							"description": "Maximum number of traces to return for search. Defaults to 20."
						}
					},
					"required": []
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "list_datasources",
				Description: "List all configured Grafana datasources with their names, types, and UIDs. Use this to confirm Mimir, Loki, Tempo, CloudWatch, and any other available data sources before querying.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {},
					"required": []
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "list_correlations",
				Description: "List every Grafana Correlation configured on this instance (Administration > Correlations) -- each one links a source datasource to a target datasource via a shared field (e.g. job, trace_id, pod). Call this to discover what correlations exist and their exact \"field\" name, then use follow_correlation to actually run one once you have a real field value from a query result.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {},
					"required": []
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "follow_correlation",
				Description: "Runs a Grafana Correlation (discovered via list_correlations) end to end: takes the real field value you already have (e.g. a trace_id from a log line, a pod name), interpolates it into the correlation's target query template, and executes it against the target datasource automatically -- instead of you having to interpolate the template and pick the right query tool yourself.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"source_datasource_uid": {
							"type": "string",
							"description": "The sourceUID of the correlation, from list_correlations"
						},
						"field": {
							"type": "string",
							"description": "The correlation's \"field\" name, from list_correlations"
						},
						"field_value": {
							"type": "string",
							"description": "The real value of that field from a query result you already have, e.g. an actual trace_id"
						},
						"label": {
							"type": "string",
							"description": "Optional: disambiguates when more than one correlation matches the same source datasource and field"
						}
					},
					"required": ["source_datasource_uid", "field", "field_value"]
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "build_change_timeline",
				Description: "Lists Grafana annotations (deploys, config changes, manual incident markers) in chronological order for a time window -- use this to check what changed around the time an incident started. Sourced only from Grafana's own Annotations API; does not include external CI/CD systems or build-info metrics.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"start": {
							"type": "string",
							"description": "Start time as RFC3339 or relative like 'now-24h'. Defaults to now-24h."
						},
						"end": {
							"type": "string",
							"description": "End time as RFC3339 or relative like 'now'. Defaults to now."
						},
						"tags": {
							"type": "array",
							"items": {"type": "string"},
							"description": "Optional: only annotations carrying at least one of these tags, e.g. [\"deploy\"]"
						},
						"limit": {
							"type": "integer",
							"description": "Maximum events to return. Defaults to 200."
						}
					},
					"required": []
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "find_dashboards",
				Description: "PREFER THIS over list_folders/list_dashboards whenever the user asks to see dashboards for a topic or area (e.g. 'SRE', 'Kafka', 'security', 'contratos'). One call: matches the topic against folder names (including nested subfolders -- most dashboards live one level below the folder a user names) AND against dashboard titles, and returns the matching dashboards directly with title/uid/folder. No need to chain list_folders then list_dashboards yourself.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"topic": {
							"type": "string",
							"description": "The area/topic to find dashboards for, e.g. 'SRE', 'Kafka', 'security'."
						}
					},
					"required": ["topic"]
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "list_folders",
				Description: "List ALL Grafana folders as a flat list, each with its own parentUID. Use find_dashboards instead if you're just trying to locate dashboards for a topic -- use this one only when you need the raw folder tree itself.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"parentUID": {
							"type": "string",
							"description": "Optional: only return direct children of this folder UID. Leave empty to get every folder."
						}
					},
					"required": []
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "list_dashboards",
				Description: "List Grafana dashboards with their titles, UIDs, tags, and folder. Use find_dashboards instead if you're answering 'show me the X dashboards' for a topic. Use this one for a plain title search or when you already know the exact folder.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"query": {
							"type": "string",
							"description": "Optional search query to filter dashboards by name. Leave empty to list all."
						},
						"folder": {
							"type": "string",
							"description": "Optional folder name or UID (from list_folders) -- returns dashboards in that folder AND its subfolders."
						}
					},
					"required": []
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "get_dashboard",
				Description: "Get a Grafana dashboard's full structure including panels, queries, variables, and descriptions. Use this to explain what a dashboard or panel/component does before querying live metrics or logs. Prefer a real UID from list_dashboards/find_dashboards when you have one, but a dashboard title also works -- it's looked up by search, and the result tells you whether it matched exactly, matched approximately, or found more than one candidate (in which case ask the user which one they meant instead of guessing).",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"uid": {
							"type": "string",
							"description": "Dashboard UID (from list_dashboards result), or its title if you don't have the UID handy"
						}
					},
					"required": ["uid"]
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "list_alerts",
				Description: "List currently firing or pending alerts from Grafana's built-in alerting (no external Alertmanager datasource needed). Use this to check for active incidents, understand alert states, and correlate alerts with metrics/logs.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"filter": {
							"type": "string",
							"description": "Optional Alertmanager filter expression, e.g. severity=critical or namespace=default"
						},
						"state": {
							"type": "string",
							"description": "Filter by alert state: 'firing', 'pending', or 'inactive'. Leave empty for all."
						}
					},
					"required": []
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "list_alert_rules",
				Description: "List all configured Grafana alert rules with their expressions, labels, and annotations. Use this to discover what rules exist and their exact name/UID -- for a deep look at ONE specific rule, use inspect_alert instead (this dumps everything unfiltered, which is unusable once there are more than a handful).",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {},
					"required": []
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "inspect_alert",
				Description: "Deep look at ONE specific alert rule (by rule_uid or alertname, from list_alert_rules): returns its real expression, for-duration, labels, and annotations, plus whether it has a runbook link and a proper for-duration configured. Use this INSTEAD OF list_alert_rules when the question is about one specific rule's configuration (is it misconfigured, does it have a runbook, why does it fire).",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"rule_uid": {
							"type": "string",
							"description": "The rule's UID, from list_alert_rules -- preferred when known"
						},
						"alertname": {
							"type": "string",
							"description": "The rule's alert name/title, if rule_uid isn't known"
						}
					},
					"required": []
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "check_observability_coverage",
				Description: "Checks whether a service has real logs, metrics, and dashboards -- useful for a newly-launched service to confirm it's actually observable. Does NOT check traces (no Tempo datasource is available in this environment) -- that field is always reported as an explicit unchecked gap, not a false negative.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"service_name": {
							"type": "string",
							"description": "The service/job name to check coverage for"
						}
					},
					"required": ["service_name"]
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "assess_alert_quality",
				Description: "Assesses one alert rule's quality signals: whether it has a runbook link, whether it has a \"for\" duration configured (missing one risks noise), and how many instances are firing right now. Does NOT measure historical flapping frequency -- only current configuration and current state.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"rule_uid": {
							"type": "string",
							"description": "The rule's UID, from list_alert_rules -- preferred when known"
						},
						"alertname": {
							"type": "string",
							"description": "The rule's alert name/title, if rule_uid isn't known"
						}
					},
					"required": []
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "analyze_active_alerts",
				Description: "Queries Grafana for currently firing alerts, GROUPED by alertname (an alert firing across many pods/instances counts as one group), and when brain-agent tools are enabled, cross-references each group (not each individual alert instance) against long-term memory to surface historical correlations and past root-cause notes.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"project": {
							"type": "string",
							"description": "Optional project or tenancy context for memory search (default: 'default')"
						},
						"max_alerts": {
							"type": "integer",
							"description": "Maximum number of firing alerts to consider before grouping. Defaults to 200."
						}
					},
					"required": []
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "investigate_alert",
				Description: "Deep-dive investigation of ONE firing alert (by alertname, from list_alerts/analyze_active_alerts): in a single call, automatically finds and returns the Loki logs and Tempo traces around that alert's firing window (using its own labels to build the queries), plus any brain-agent historical correlation -- so you don't have to chain query_loki/query_tempo/search_memory yourself. Returns raw evidence (logs, traces, past incidents) for you to reason about and summarize as a root cause -- it does not compute the root cause itself. This is the preferred first call when asked to write a postmortem or incident report for a named alert.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"alertname": {
							"type": "string",
							"description": "The alertname label of the firing alert to investigate, exactly as returned by list_alerts or analyze_active_alerts"
						},
						"namespace": {
							"type": "string",
							"description": "Optional: narrow to a specific namespace/job when multiple firing alerts share this alertname"
						},
						"project": {
							"type": "string",
							"description": "Optional project or tenancy context for the brain-agent memory search (default: 'default')"
						}
					},
					"required": ["alertname"]
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "investigate_incident",
				Description: "Broader version of investigate_alert: starts from any seed -- an alert name, a service/pod/job name, or anything else a user might describe an incident by -- not just an exact, currently-firing alertname. Resolves the seed against active alerts first, falling back to real Loki label values if no alert matches, then automatically gathers log patterns (via analyze_log_patterns), traces, and brain-agent historical correlation for the resolved window. Prefer this over investigate_alert whenever the seed might not be an exact active alertname (e.g. a service just mentioned by name, or an alert that already resolved). Pass metric_query only if you already know a specific PromQL expression relevant to this incident (e.g. from a dashboard) -- it is not inferred automatically.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"seed": {
							"type": "string",
							"description": "Alert name, service/pod/job name, or any other identifier this incident is about"
						},
						"metric_query": {
							"type": "string",
							"description": "Optional PromQL expression to also check for anomalies (via analyze_metric_anomaly) in this incident's window"
						},
						"namespace": {
							"type": "string",
							"description": "Optional: narrow to a specific namespace when multiple firing alerts share the same seed as their alertname"
						},
						"project": {
							"type": "string",
							"description": "Optional project or tenancy context for the brain-agent memory search (default: 'default')"
						}
					},
					"required": ["seed"]
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "analyze_trace_bottlenecks",
				Description: "Fetches one distributed trace by ID and identifies which span(s) account for a disproportionate share of the total trace duration (the real latency bottleneck), plus any span that came back with an error status. Use this instead of query_tempo with a traceID when the question is 'why was this request slow/what failed', not just 'show me the trace'.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"trace_id": {
							"type": "string",
							"description": "The trace ID to analyze, e.g. from query_tempo's search results or investigate_incident/investigate_alert's trace evidence"
						},
						"min_percent_of_trace": {
							"type": "number",
							"description": "Minimum share (0-100) of the total trace duration a span must account for to be flagged as a bottleneck. Defaults to 20."
						},
						"datasource_uid": {
							"type": "string",
							"description": "Optional: target a specific Tempo datasource by UID when this Grafana has more than one (call list_datasources to see them). If there is more than one and this is omitted, the call fails asking for it explicitly."
						}
					},
					"required": ["trace_id"]
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "build_service_topology",
				Description: "Derives a service call graph (which service calls which, and how often) from real parent-child span relationships across a sample of recent traces involving the given service -- not a single trace's necessarily-partial view, and not a guessed architecture diagram. Call counts are relative to the sampled traces only, not the service's total traffic.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"service_name": {
							"type": "string",
							"description": "Service name to seed the search from, exactly as it appears in Tempo (resource.service.name)"
						},
						"max_traces": {
							"type": "integer",
							"description": "How many recent traces to sample and walk. Defaults to 10, capped at 25."
						},
						"datasource_uid": {
							"type": "string",
							"description": "Optional: target a specific Tempo datasource by UID when this Grafana has more than one (call list_datasources to see them). If there is more than one and this is omitted, the call fails asking for it explicitly."
						}
					},
					"required": ["service_name"]
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "analyze_cloud_resource",
				Description: "Queries one AWS CloudWatch metric (namespace + metric name + dimensions) and reports its recent datapoints plus whether the latest value deviates anomalously from the window's own baseline. Reports found=false honestly (never a false 'healthy' reading) when CloudWatch has no matching datapoints -- namespace/metric/dimension names are easy to get slightly wrong. ONLY for AWS-hosted resources (EC2, RDS, ELB, etc.) actually monitored via CloudWatch -- a plain 'node CPU usage' or 'pod restarts' question about THIS Kubernetes cluster is a Prometheus/Mimir question (query_prometheus or dispatch_worker with metric_investigator), never CloudWatch, even though the word 'resource' sounds similar -- do not call this tool unless the user explicitly mentions AWS/CloudWatch/EC2 or a specific AWS resource.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"namespace": {
							"type": "string",
							"description": "CloudWatch namespace, e.g. AWS/EC2, AWS/RDS, or a custom application namespace"
						},
						"metric_name": {
							"type": "string",
							"description": "CloudWatch metric name, e.g. CPUUtilization"
						},
						"dimensions": {
							"type": "object",
							"additionalProperties": {"type": "string"},
							"description": "Dimension name/value pairs scoping the metric to one resource, e.g. {\"InstanceId\": \"i-0abc123\"}. Most CloudWatch metrics are meaningless without this."
						},
						"statistic": {
							"type": "string",
							"description": "One of Average, Sum, Maximum, Minimum, SampleCount. Defaults to Average."
						},
						"region": {
							"type": "string",
							"description": "Optional: only needed if different from the datasource's own configured default region"
						},
						"start": {
							"type": "string",
							"description": "Start time as RFC3339 or relative like 'now-1h'. Defaults to now-1h."
						},
						"end": {
							"type": "string",
							"description": "End time as RFC3339 or relative like 'now'. Defaults to now."
						},
						"datasource_uid": {
							"type": "string",
							"description": "Optional: target a specific CloudWatch datasource by UID when this Grafana has more than one (call list_datasources to see them). If there is more than one and this is omitted, the call fails asking for it explicitly."
						}
					},
					"required": ["namespace", "metric_name"]
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "query_datasource",
				Description: "Runs a read-only SQL SELECT (or WITH ... SELECT) query against a generic SQL datasource (currently: Postgres). Only a single, plain read statement is allowed -- no writes, no multi-statement queries, no DDL. Use this for structured relational data (e.g. a deployment/change-history table) that Prometheus/Loki/Tempo can't answer. Highest-scrutiny tool in this plugin -- if a query is rejected, rephrase it as a simpler single SELECT rather than trying to work around the restriction.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"query": {
							"type": "string",
							"description": "A single read-only SQL SELECT statement, e.g. SELECT service, version, deployed_at FROM deployments ORDER BY deployed_at DESC LIMIT 10"
						},
						"max_rows": {
							"type": "integer",
							"description": "Maximum rows to return. Defaults to 200, capped at 1000."
						},
						"datasource_uid": {
							"type": "string",
							"description": "Optional: target a specific SQL datasource by UID when this Grafana has more than one (call list_datasources to see them). If there is more than one and this is omitted, the call fails asking for it explicitly."
						}
					},
					"required": ["query"]
				}`),
			},
		},
	}
	if agent == "" || agent == "generic" {
		return filterToolByName(tools, "dispatch_worker")
	}
	return tools
}

func filterToolByName(tools []openai.Tool, blocked string) []openai.Tool {
	filtered := make([]openai.Tool, 0, len(tools))
	for _, tool := range tools {
		if tool.Function != nil && tool.Function.Name == blocked {
			continue
		}
		filtered = append(filtered, tool)
	}
	return filtered
}

// ToolCallArgs holds parsed arguments for tool calls.
type PrometheusQueryArgs struct {
	Query string `json:"query"`
	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
	Step  string `json:"step,omitempty"`
	// DatasourceUID targets a specific Prometheus datasource when a Grafana
	// instance has more than one (multi-cluster/multi-tenant setups) --
	// leave empty when there's only one, which auto-resolves as before. See
	// list_datasources / resolveDatasourceUID.
	DatasourceUID string `json:"datasource_uid,omitempty"`
}

type LokiQueryArgs struct {
	Query string `json:"query"`
	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
	Limit int    `json:"limit,omitempty"`
	// DatasourceUID targets a specific Loki datasource when more than one
	// exists -- see PrometheusQueryArgs.DatasourceUID.
	DatasourceUID string `json:"datasource_uid,omitempty"`
}

type TempoQueryArgs struct {
	Query   string `json:"query,omitempty"`
	TraceID string `json:"traceID,omitempty"`
	Start   string `json:"start,omitempty"`
	End     string `json:"end,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	// DatasourceUID targets a specific Tempo datasource when more than one
	// exists -- see PrometheusQueryArgs.DatasourceUID.
	DatasourceUID string `json:"datasource_uid,omitempty"`
}

// ListDashboardsArgs holds parsed arguments for list_dashboards.
type ListDashboardsArgs struct {
	Query  string `json:"query,omitempty"`
	Folder string `json:"folder,omitempty"`
}

// GetDashboardArgs holds parsed arguments for get_dashboard.
type GetDashboardArgs struct {
	UID string `json:"uid"`
}

// ListAlertsArgs holds parsed arguments for list_alerts.
type ListAlertsArgs struct {
	Filter string `json:"filter,omitempty"`
	State  string `json:"state,omitempty"`
}

// AnalyzeActiveAlertsArgs holds parsed arguments for analyze_active_alerts.
type AnalyzeActiveAlertsArgs struct {
	Project string `json:"project,omitempty"`
	// MaxAlerts caps how many firing alerts are considered before grouping
	// -- an instance firing across hundreds of pods shouldn't turn one
	// call into hundreds of lines plus one search_memory call each.
	MaxAlerts int `json:"max_alerts,omitempty"`
}

// InvestigateAlertArgs holds parsed arguments for investigate_alert.
type InvestigateAlertArgs struct {
	AlertName string `json:"alertname"`
	Namespace string `json:"namespace,omitempty"`
	Project   string `json:"project,omitempty"`
}
