package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
)

// ToolExecutor executes tool calls by querying Grafana datasources.
type ToolExecutor struct {
	grafanaURL     string
	httpClient     *http.Client
	defaultHeaders map[string]string
	// tokenPath is a file path containing a Grafana service account token.
	// When set, the token is re-read on each request so that rotated tokens
	// (e.g. from a Kubernetes secret mount) are picked up without a restart.
	tokenPath string
	logger    log.Logger

	// Datasource cache to avoid fetching the full list on every tool call --
	// type -> every UID of that type (not just one), so resolveDatasourceUID
	// can tell a genuinely single-datasource deployment apart from a multi-
	// cluster/multi-tenant one with several datasources of the same type.
	dsCache     map[string][]string
	dsCacheMu   sync.Mutex
	dsCacheTime time.Time

	// Grafana's own version, cached indefinitely once fetched (it can't
	// change while this instance keeps running) -- grounds the system
	// prompt so "how do I..." UI/navigation questions (change my password,
	// create a dashboard, set up tracing) get menu paths for the ACTUAL
	// installed version, not a guess that might describe a different major
	// version's UI. See grafanaVersion/grafanaVersionLine (llm.go).
	versionCache   string
	versionCacheMu sync.Mutex

	// mcp is brain-agent's MCP client, wired in when detected and enabled
	// (see mcp.go, app.go). nil when absent or disabled -- every call site
	// must handle that case, never assume it's set.
	mcp *MCPClient

	// allowedDatasourceUIDs mirrors Settings.AllowedDatasourceUIDs. Empty
	// means unrestricted. Enforced in resolveDatasourceUID -- the single
	// point every datasource-bound tool goes through -- and applied to
	// discovery so the model is never shown a datasource it cannot use.
	allowedDatasourceUIDs map[string]bool

	// internetToolsEnabled mirrors Settings.EnableInternetTools at
	// construction time -- defense in depth, independent of onlineSearch
	// being non-nil: even if a client existed from a bug, partial reload, or
	// forced call, Execute must refuse before any health check or outbound
	// request when the admin gate is off (see the case "search_web" below).
	internetToolsEnabled bool
	// onlineSearch is the configured internet-search backend (SearXNG or an
	// admin Search Gateway), wired in app.go only when internetToolsEnabled
	// is true. nil when internet tools are off or misconfigured.
	onlineSearch *OnlineSearchClient
}

// NewToolExecutor creates a new tool executor.
// grafanaURL is the internal Grafana URL (e.g. http://localhost:3000).
func NewToolExecutor(grafanaURL string, logger log.Logger) *ToolExecutor {
	return &ToolExecutor{
		grafanaURL: strings.TrimSuffix(grafanaURL, "/"),
		httpClient: &http.Client{Timeout: 30 * time.Second},
		logger:     logger,
	}
}

// Execute runs a tool call and returns the result as a string.
func (te *ToolExecutor) Execute(ctx context.Context, name string, arguments string) (string, error) {
	switch name {
	case "query_prometheus":
		return te.queryPrometheus(ctx, arguments)
	case "query_loki":
		return te.queryLoki(ctx, arguments)
	case "list_loki_labels":
		return te.listLokiLabels(ctx, arguments)
	case "analyze_metric_anomaly":
		return te.analyzeMetricAnomaly(ctx, arguments)
	case "forecast_capacity":
		return te.forecastCapacity(ctx, arguments)
	case "diagnose_kubernetes_workload":
		return te.diagnoseKubernetesWorkload(ctx, arguments)
	case "analyze_container_lifecycle":
		return te.analyzeContainerLifecycle(ctx, arguments)
	case "analyze_node_health":
		return te.analyzeNodeHealth(ctx, arguments)
	case "inspect_kubernetes_events":
		return te.inspectKubernetesEvents(ctx, arguments)
	case "analyze_slo_burn_rate":
		return te.analyzeSLOBurnRate(ctx, arguments)
	case "analyze_log_patterns":
		return te.analyzeLogPatterns(ctx, arguments)
	case "query_tempo":
		return te.queryTempo(ctx, arguments)
	case "list_datasources":
		return te.listDatasources(ctx)
	case "list_correlations":
		return te.listCorrelations(ctx)
	case "follow_correlation":
		return te.followCorrelation(ctx, arguments)
	case "build_change_timeline":
		return te.buildChangeTimeline(ctx, arguments)
	case "list_folders":
		return te.listFolders(ctx, arguments)
	case "list_dashboards":
		return te.listDashboards(ctx, arguments)
	case "find_dashboards":
		return te.findDashboards(ctx, arguments)
	case "get_dashboard":
		return te.getDashboard(ctx, arguments)
	case "list_alerts":
		return te.listAlerts(ctx, arguments)
	case "list_alert_rules":
		return te.listAlertRules(ctx)
	case "inspect_alert":
		return te.inspectAlert(ctx, arguments)
	case "assess_alert_quality":
		return te.assessAlertQuality(ctx, arguments)
	case "check_observability_coverage":
		return te.checkObservabilityCoverage(ctx, arguments)
	case "analyze_active_alerts":
		return te.analyzeActiveAlerts(ctx, arguments)
	case "investigate_alert":
		return te.investigateAlert(ctx, arguments)
	case "investigate_incident":
		return te.investigateIncident(ctx, arguments)
	case "analyze_trace_bottlenecks":
		return te.analyzeTraceBottlenecks(ctx, arguments)
	case "build_service_topology":
		return te.buildServiceTopology(ctx, arguments)
	case "analyze_cloud_resource":
		return te.analyzeCloudResource(ctx, arguments)
	case "query_datasource":
		return te.queryDatasource(ctx, arguments)
	case onlineSearchToolName:
		// Order is deliberate and load-bearing: the admin gate is checked
		// FIRST, before te.onlineSearch's existence and before any health
		// check -- so a disabled gate never triggers a health check, DNS
		// lookup, or outbound socket, even for a forced/stale/pseudo-tool-
		// call reaching this dispatcher without the tool ever having been
		// exposed in allTools (invariant #15).
		if !te.internetToolsEnabled {
			return onlineSearchUnavailableResult(arguments, "Online search is disabled by admin.", []string{"Operate as local-only. Do not suggest web search or internet tools in this turn."})
		}
		if te.onlineSearch == nil {
			return onlineSearchUnavailableResult(arguments, "Online search is not configured.", []string{"Use local Grafana tools, provided context, and safe general knowledge."})
		}
		if !te.onlineSearch.CheckNow(ctx) {
			return te.onlineSearch.unavailableResult(arguments)
		}
		return te.onlineSearch.Search(ctx, arguments)
	default:
		if te.mcp != nil && te.mcp.HasTool(name) {
			result, err := te.mcp.Call(ctx, name, arguments)
			if err != nil && isMCPUnavailableError(err) {
				// Real live case: a raw "Error: mcp server returned status
				// 404" reached the user verbatim after they disabled the
				// Brain Agent plugin mid-session (HasTool's cache doesn't
				// clear immediately, so a call this plugin has done before
				// gets attempted again and fails). Told to the model as
				// honest, specific data instead of an opaque error string --
				// see brainAgentUnavailableMessage.
				state, _ := te.brainAgentInstallState(ctx)
				return brainAgentUnavailableMessage(state), nil
			}
			return result, err
		}
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

// isMCPUnavailableError reports whether err is a transport/routing-level MCP
// failure (brain-agent unreachable -- disabled, not installed, crashed,
// timed out) as opposed to a real application-level error brain-agent itself
// reported (mcp.go's Call returns that as resp.Result.Content's text, a
// distinct path) or a local argument-parsing error -- those carry real,
// actionable information and must reach the model as-is, not be rewritten
// into a generic unavailability message.
func isMCPUnavailableError(err error) bool {
	msg := err.Error()
	for _, s := range []string{"mcp server returned status", "execute mcp request", "create mcp request", "no grafana token configured"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// brainAgentUnavailableMessage is returned as the TOOL RESULT (not a Go
// error) in place of a raw MCP transport error, so the model receives
// specific, honest data to relay -- not installed vs installed-but-disabled
// get different, actionable guidance (per state, since Grafana's own plugin
// resource routing 404s identically for both -- see brainAgentInstallState).
func brainAgentUnavailableMessage(state brainAgentInstallState) string {
	switch state {
	case brainAgentNotInstalled:
		return "Brain Agent (long-term memory) is not installed on this Grafana instance, so this action is not possible right now. If the user wants persistent memory/recall across conversations, mention that installing the Brain Agent plugin would add that capability -- don't suggest 'enabling' something that isn't installed."
	case brainAgentDisabled:
		return "Brain Agent (long-term memory) is installed but currently DISABLED, so this action is not possible right now. If the user wants persistent memory/recall, mention that an admin can enable it under Administration > Plugins > Brain Agent to unlock this feature."
	case brainAgentAuthError:
		return "This plugin's own connection to Grafana is misconfigured (Grafana rejected its service account token), which is why Brain Agent couldn't be reached -- this isn't a Brain Agent problem. Tell the user an admin needs to regenerate the grafanaToken in this plugin's (agent-ai-app's) own settings."
	case brainAgentIntegrationOff:
		return "Brain Agent (long-term memory) is turned OFF for this assistant specifically (its own \"Enable Brain Agent Tools\" setting), so this action is not possible right now -- Brain Agent itself may be perfectly installed and enabled at the Grafana level, that's not the issue. If the user wants persistent memory/recall, mention that an admin can turn on \"Enable Brain Agent Tools\" in THIS plugin's (agent-ai-app's) own configuration."
	default:
		return "Brain Agent (long-term memory) is currently unreachable -- it may be temporarily down or misconfigured. Suggest the user try again shortly."
	}
}

func (te *ToolExecutor) queryPrometheus(ctx context.Context, arguments string) (string, error) {
	var args PrometheusQueryArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("parse prometheus args: %w", err)
	}

	if args.Query == "" {
		return "", fmt.Errorf("query is required")
	}
	if strings.TrimSpace(args.Query) == "*" {
		return "", fmt.Errorf("%q is not a valid PromQL query -- provide a real metric selector, e.g. \"up\", \"kube_pod_status_restart_count\", or \"rate(container_cpu_usage_seconds_total[5m])\"", args.Query)
	}

	dsUID, err := te.resolveDatasourceUID(ctx, "prometheus", args.DatasourceUID)
	if err != nil {
		return "", fmt.Errorf("find prometheus datasource: %w", err)
	}

	// Build query parameters
	params := url.Values{}
	params.Set("query", args.Query)

	if args.Step == "" {
		args.Step = "60s"
	}
	params.Set("step", args.Step)

	now := time.Now()
	if args.Start == "" {
		params.Set("start", fmt.Sprintf("%d", now.Add(-5*time.Minute).Unix()))
	} else {
		params.Set("start", resolveTime(args.Start, now))
	}
	if args.End == "" {
		params.Set("end", fmt.Sprintf("%d", now.Unix()))
	} else {
		params.Set("end", resolveTime(args.End, now))
	}

	apiPath := fmt.Sprintf("/api/datasources/proxy/uid/%s/api/v1/query_range?%s", url.PathEscape(dsUID), params.Encode())
	return te.doGrafanaRequest(ctx, http.MethodGet, apiPath, nil)
}

func (te *ToolExecutor) queryLoki(ctx context.Context, arguments string) (string, error) {
	var args LokiQueryArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("parse loki args: %w", err)
	}

	if args.Query == "" {
		return "", fmt.Errorf("query is required")
	}

	dsUID, err := te.resolveDatasourceUID(ctx, "loki", args.DatasourceUID)
	if err != nil {
		return "", fmt.Errorf("find loki datasource: %w", err)
	}

	params := url.Values{}
	params.Set("query", args.Query)

	now := time.Now()
	if args.Start == "" {
		params.Set("start", fmt.Sprintf("%d", now.Add(-1*time.Hour).UnixNano()))
	} else {
		params.Set("start", resolveTimeNano(args.Start, now))
	}
	if args.End == "" {
		params.Set("end", fmt.Sprintf("%d", now.UnixNano()))
	} else {
		params.Set("end", resolveTimeNano(args.End, now))
	}

	limit := args.Limit
	if limit <= 0 {
		limit = 100
	}
	params.Set("limit", fmt.Sprintf("%d", limit))

	apiPath := fmt.Sprintf("/api/datasources/proxy/uid/%s/loki/api/v1/query_range?%s", url.PathEscape(dsUID), params.Encode())
	return te.doGrafanaRequest(ctx, http.MethodGet, apiPath, nil)
}

// listLokiLabels discovers real Loki label names, or (when a label is given)
// the real values for that label -- so the model can confirm the correct
// label scheme (e.g. "job" vs "app" vs "namespace") before building a
// query_loki filter, instead of guessing blindly across several failed calls.
func (te *ToolExecutor) listLokiLabels(ctx context.Context, arguments string) (string, error) {
	var args struct {
		Label         string `json:"label"`
		Start         string `json:"start"`
		End           string `json:"end"`
		DatasourceUID string `json:"datasource_uid,omitempty"`
	}
	if strings.TrimSpace(arguments) != "" {
		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return "", fmt.Errorf("parse loki labels args: %w", err)
		}
	}

	dsUID, err := te.resolveDatasourceUID(ctx, "loki", args.DatasourceUID)
	if err != nil {
		return "", fmt.Errorf("find loki datasource: %w", err)
	}

	now := time.Now()
	params := url.Values{}
	if args.Start == "" {
		params.Set("start", fmt.Sprintf("%d", now.Add(-24*time.Hour).UnixNano()))
	} else {
		params.Set("start", resolveTimeNano(args.Start, now))
	}
	if args.End == "" {
		params.Set("end", fmt.Sprintf("%d", now.UnixNano()))
	} else {
		params.Set("end", resolveTimeNano(args.End, now))
	}

	var apiPath string
	if args.Label == "" {
		apiPath = fmt.Sprintf("/api/datasources/proxy/uid/%s/loki/api/v1/labels?%s", url.PathEscape(dsUID), params.Encode())
	} else {
		apiPath = fmt.Sprintf("/api/datasources/proxy/uid/%s/loki/api/v1/label/%s/values?%s", url.PathEscape(dsUID), url.PathEscape(args.Label), params.Encode())
	}
	return te.doGrafanaRequest(ctx, http.MethodGet, apiPath, nil)
}

func (te *ToolExecutor) queryTempo(ctx context.Context, arguments string) (string, error) {
	var args TempoQueryArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("parse tempo args: %w", err)
	}
	if args.Query == "" && args.TraceID == "" {
		// Bare "X is required" errors are fed straight back to the model as
		// the tool result -- a live incident (2026-08-11) showed the model
		// respond to this exact message by giving up and asking the USER to
		// supply query/trace_id, instead of self-correcting (e.g. running a
		// TraceQL search first). Spelling out the concrete next step here
		// costs nothing and lets the model retry on its own in the next
		// round rather than punting.
		return "", fmt.Errorf(`query or traceID is required -- pass "query" as a TraceQL selector (e.g. {resource.service.name="checkout"}) to search for traces, or "trace_id" if you already have a specific trace ID from a previous tool result`)
	}

	dsUID, err := te.resolveDatasourceUID(ctx, "tempo", args.DatasourceUID)
	if err != nil {
		return "", fmt.Errorf("find tempo datasource: %w", err)
	}

	if args.TraceID != "" {
		apiPath := fmt.Sprintf("/api/datasources/proxy/uid/%s/api/traces/%s", url.PathEscape(dsUID), url.PathEscape(args.TraceID))
		return te.doGrafanaRequest(ctx, http.MethodGet, apiPath, nil)
	}

	params := url.Values{}
	params.Set("q", args.Query)

	now := time.Now()
	if args.Start == "" {
		params.Set("start", fmt.Sprintf("%d", now.Add(-1*time.Hour).Unix()))
	} else {
		params.Set("start", resolveTime(args.Start, now))
	}
	if args.End == "" {
		params.Set("end", fmt.Sprintf("%d", now.Unix()))
	} else {
		params.Set("end", resolveTime(args.End, now))
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 20
	}
	params.Set("limit", fmt.Sprintf("%d", limit))

	apiPath := fmt.Sprintf("/api/datasources/proxy/uid/%s/api/search?%s", url.PathEscape(dsUID), params.Encode())
	return te.doGrafanaRequest(ctx, http.MethodGet, apiPath, nil)
}

func (te *ToolExecutor) listDatasources(ctx context.Context) (string, error) {
	body, err := te.doGrafanaRequest(ctx, http.MethodGet, "/api/datasources", nil)
	if err != nil {
		return "", err
	}

	// Parse and return only relevant fields
	var datasources []struct {
		Name string `json:"name"`
		Type string `json:"type"`
		UID  string `json:"uid"`
		URL  string `json:"url"`
	}
	if err := json.Unmarshal([]byte(body), &datasources); err != nil {
		return body, nil //nolint:nilerr // Return raw body if parsing fails
	}

	type dsSummary struct {
		Name string `json:"name"`
		Type string `json:"type"`
		UID  string `json:"uid"`
	}
	// Filtered at the source: the model must never be shown a datasource it
	// cannot query, or it will propose it and fail on the next call.
	summaries := make([]dsSummary, 0, len(datasources))
	for _, ds := range datasources {
		if !te.datasourceAllowed(ds.UID) {
			continue
		}
		summaries = append(summaries, dsSummary{Name: ds.Name, Type: ds.Type, UID: ds.UID})
	}

	out, _ := json.Marshal(summaries)
	return string(out), nil
}

// listCorrelations lists every Grafana Correlation defined on this instance.
// Correlations are a click-to-follow Explore/frontend feature -- Grafana's
// variable substitution (${__data.fields.X}) is an @internal, DataFrame-based
// mechanism with no server-side equivalent (confirmed against
// @grafana/data/@grafana/runtime's own type definitions). So this doesn't try
// to "run" a correlation -- it just exposes what field links which two
// datasources, generically, for whatever correlations an admin has defined.
// The LLM does the substitution itself: match a correlation's sourceUID
// against a datasource it already queried, find "field" among that result's
// labels, then build and run the target query with the matching query tool.
func (te *ToolExecutor) listCorrelations(ctx context.Context) (string, error) {
	body, err := te.doGrafanaRequest(ctx, http.MethodGet, "/api/datasources/correlations", nil)
	if err != nil {
		return "", err
	}

	var resp struct {
		Correlations []struct {
			SourceUID   string `json:"sourceUID"`
			TargetUID   string `json:"targetUID"`
			Label       string `json:"label"`
			Description string `json:"description"`
			Config      struct {
				Field  string          `json:"field"`
				Target json.RawMessage `json:"target"`
			} `json:"config"`
		} `json:"correlations"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return body, nil //nolint:nilerr // Return raw body if parsing fails
	}

	type correlationSummary struct {
		SourceUID   string          `json:"sourceUID"`
		TargetUID   string          `json:"targetUID"`
		Field       string          `json:"field"`
		Label       string          `json:"label,omitempty"`
		Description string          `json:"description,omitempty"`
		Target      json.RawMessage `json:"target,omitempty"`
	}
	summaries := make([]correlationSummary, len(resp.Correlations))
	for i, c := range resp.Correlations {
		summaries[i] = correlationSummary{
			SourceUID:   c.SourceUID,
			TargetUID:   c.TargetUID,
			Field:       c.Config.Field,
			Label:       c.Label,
			Description: c.Description,
			Target:      c.Config.Target,
		}
	}

	out, _ := json.Marshal(summaries)
	return string(out), nil
}

// searchFolderLimit is the /api/search?type=dash-folder page size used by
// listFolders/findDashboards/resolveFolderUIDs. A full paginated loop would
// handle instances with more folders than this, but real usage here is
// almost always "find the folder(s) relevant to this question," not "list
// every folder that exists" -- security-audit finding L1 chose signaling
// truncation over paginating, since it fixes the one realistic case
// (an instance with >1000 folders) without adding latency for everyone else.
const searchFolderLimit = 1000

// truncationNoteIfAtLimit returns a note to append to a tool result when a
// search hit exactly searchFolderLimit results -- almost certainly meaning
// more exist and were cut off, not a coincidence. Lets the LLM decide
// whether to refine its query instead of silently treating a truncated list
// as complete.
func truncationNoteIfAtLimit(resultCount int) string {
	if resultCount < searchFolderLimit {
		return ""
	}
	return fmt.Sprintf("\n\nNote: results truncated at %d items -- more may exist. Refine the search (e.g. by parentUID) if the folder you're looking for isn't listed.", searchFolderLimit)
}

// listFolders lists Grafana folders via the search API (type=dash-folder),
// optionally scoped to the direct children of parentUID. The search API
// (unlike /api/folders) reports parentUid on every result, so this is the
// one call that works uniformly for both "top-level" and "children of X".
func (te *ToolExecutor) listFolders(ctx context.Context, arguments string) (string, error) {
	var args struct {
		ParentUID string `json:"parentUID"`
	}
	if arguments != "" && arguments != "{}" {
		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return "", fmt.Errorf("parse list_folders args: %w", err)
		}
	}

	body, err := te.doGrafanaRequest(ctx, http.MethodGet, fmt.Sprintf("/api/search?type=dash-folder&limit=%d", searchFolderLimit), nil)
	if err != nil {
		return "", err
	}

	var folders []struct {
		Title     string `json:"title"`
		UID       string `json:"uid"`
		ParentUID string `json:"folderUid"`
	}
	if err := json.Unmarshal([]byte(body), &folders); err != nil {
		return body, nil //nolint:nilerr // Return raw body if parsing fails
	}

	type folderSummary struct {
		Title     string `json:"title"`
		UID       string `json:"uid"`
		ParentUID string `json:"parentUID,omitempty"`
	}
	// Many Grafana instances nest folders at least one level below an
	// implicit root -- filtering to true top-level-only when parentUID is
	// omitted can return almost nothing useful. Always return the full flat
	// list (each row already carries its own parentUID) and let the caller
	// filter by name/parent itself.
	summaries := make([]folderSummary, 0, len(folders))
	for _, f := range folders {
		if args.ParentUID != "" && f.ParentUID != args.ParentUID {
			continue
		}
		summaries = append(summaries, folderSummary{Title: f.Title, UID: f.UID, ParentUID: f.ParentUID})
	}

	out, _ := json.Marshal(summaries)
	return string(out) + truncationNoteIfAtLimit(len(folders)), nil
}

// findDashboards answers "show me the X dashboards" in a single call: it
// matches the topic against folder titles (expanding to every descendant
// folder, since dashboards often live in nested subfolders one or more
// levels below the folder a user names) AND against dashboard titles
// directly, then merges both result sets. This exists because chaining
// list_folders -> spot the right folder -> list_dashboards is a multi-step
// plan that smaller/mid-size models reliably lose track of mid-way,
// sometimes dumping the raw folder list back at the user instead of
// finishing the lookup.
func (te *ToolExecutor) findDashboards(ctx context.Context, arguments string) (string, error) {
	var args struct {
		Topic string `json:"topic"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("parse find_dashboards args: %w", err)
	}
	if strings.TrimSpace(args.Topic) == "" {
		return "", fmt.Errorf("topic is required")
	}

	folderBody, err := te.doGrafanaRequest(ctx, http.MethodGet, fmt.Sprintf("/api/search?type=dash-folder&limit=%d", searchFolderLimit), nil)
	if err != nil {
		return "", err
	}
	var folders []struct {
		Title     string `json:"title"`
		UID       string `json:"uid"`
		FolderUID string `json:"folderUid"`
	}
	if err := json.Unmarshal([]byte(folderBody), &folders); err != nil {
		return "", fmt.Errorf("parse folder search response: %w", err)
	}

	lowerTopic := strings.ToLower(args.Topic)
	matched := map[string]bool{}
	for _, f := range folders {
		if strings.Contains(strings.ToLower(f.Title), lowerTopic) {
			matched[f.UID] = true
		}
	}
	frontier := make([]string, 0, len(matched))
	for uid := range matched {
		frontier = append(frontier, uid)
	}
	for len(frontier) > 0 {
		var next []string
		for _, f := range folders {
			if matched[f.FolderUID] && !matched[f.UID] {
				matched[f.UID] = true
				next = append(next, f.UID)
			}
		}
		frontier = next
	}

	type dashHit struct {
		Title       string `json:"title"`
		UID         string `json:"uid"`
		FolderTitle string `json:"folderTitle"`
		FolderUID   string `json:"folderUid"`
	}
	seen := map[string]bool{}
	var results []dashHit

	addFromSearch := func(apiPath string) error {
		body, err := te.doGrafanaRequest(ctx, http.MethodGet, apiPath, nil)
		if err != nil {
			return err
		}
		var hits []dashHit
		if err := json.Unmarshal([]byte(body), &hits); err != nil {
			return nil //nolint:nilerr // best-effort merge, skip on unexpected shape
		}
		for _, h := range hits {
			if !seen[h.UID] {
				seen[h.UID] = true
				results = append(results, h)
			}
		}
		return nil
	}

	if len(matched) > 0 {
		apiPath := "/api/search?type=dash-db&limit=200"
		for uid := range matched {
			apiPath += "&folderUIDs=" + url.QueryEscape(uid)
		}
		if err := addFromSearch(apiPath); err != nil {
			return "", err
		}
	}
	if err := addFromSearch("/api/search?type=dash-db&limit=100&query=" + url.QueryEscape(args.Topic)); err != nil {
		return "", err
	}

	if len(results) == 0 {
		return fmt.Sprintf(`{"message": "no folders or dashboards matched %q -- try list_folders to see the real folder names"}`, args.Topic), nil
	}

	out, _ := json.Marshal(results)
	return string(out), nil
}

// resolveFolderUIDs accepts either a folder UID or a folder name (as the model
// may pass either) and returns that folder's UID plus every descendant
// folder's UID. Grafana's dashboard search only matches a folder's DIRECT
// dashboards, but dashboards often live one or more levels below the folder
// a user names (e.g. a top-level folder that's empty itself, with its real
// dashboards organized into child folders) -- a single-UID search would
// silently return nothing for exactly the folder a user is most likely to
// ask about.
func (te *ToolExecutor) resolveFolderUIDs(ctx context.Context, folder string) ([]string, error) {
	body, err := te.doGrafanaRequest(ctx, http.MethodGet, fmt.Sprintf("/api/search?type=dash-folder&limit=%d", searchFolderLimit), nil)
	if err != nil {
		return nil, err
	}
	var folders []struct {
		Title     string `json:"title"`
		UID       string `json:"uid"`
		FolderUID string `json:"folderUid"`
	}
	if err := json.Unmarshal([]byte(body), &folders); err != nil {
		return nil, fmt.Errorf("parse folder search response: %w", err)
	}

	var targetUID string
	for _, f := range folders {
		if f.UID == folder || strings.EqualFold(f.Title, folder) {
			targetUID = f.UID
			break
		}
	}
	if targetUID == "" {
		return nil, fmt.Errorf("no folder found matching %q -- call list_folders first to get the exact name/uid", folder)
	}

	visited := map[string]bool{targetUID: true}
	frontier := []string{targetUID}
	for len(frontier) > 0 {
		var next []string
		for _, f := range folders {
			if visited[f.FolderUID] && !visited[f.UID] {
				visited[f.UID] = true
				next = append(next, f.UID)
			}
		}
		frontier = next
	}

	uids := make([]string, 0, len(visited))
	for uid := range visited {
		uids = append(uids, uid)
	}
	return uids, nil
}

func (te *ToolExecutor) listDashboards(ctx context.Context, arguments string) (string, error) {
	var args ListDashboardsArgs
	if arguments != "" && arguments != "{}" {
		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return "", fmt.Errorf("parse list_dashboards args: %w", err)
		}
	}

	apiPath := "/api/search?type=dash-db&limit=100"
	if args.Query != "" {
		apiPath += "&query=" + url.QueryEscape(args.Query)
	}
	if args.Folder != "" {
		folderUIDs, err := te.resolveFolderUIDs(ctx, args.Folder)
		if err != nil {
			return "", err
		}
		for _, uid := range folderUIDs {
			apiPath += "&folderUIDs=" + url.QueryEscape(uid)
		}
	}

	body, err := te.doGrafanaRequest(ctx, http.MethodGet, apiPath, nil)
	if err != nil {
		return "", err
	}

	var dashboards []struct {
		Title       string   `json:"title"`
		UID         string   `json:"uid"`
		Tags        []string `json:"tags"`
		URL         string   `json:"url"`
		FolderTitle string   `json:"folderTitle"`
		FolderUID   string   `json:"folderUid"`
	}
	if err := json.Unmarshal([]byte(body), &dashboards); err != nil {
		return body, nil //nolint:nilerr // Return raw body if parsing fails
	}

	type dashSummary struct {
		Title       string   `json:"title"`
		UID         string   `json:"uid"`
		Tags        []string `json:"tags,omitempty"`
		FolderTitle string   `json:"folderTitle,omitempty"`
		FolderUID   string   `json:"folderUID,omitempty"`
	}
	summaries := make([]dashSummary, len(dashboards))
	for i, d := range dashboards {
		summaries[i] = dashSummary{Title: d.Title, UID: d.UID, Tags: d.Tags, FolderTitle: d.FolderTitle, FolderUID: d.FolderUID}
	}

	out, _ := json.Marshal(summaries)
	return string(out), nil
}

// looksLikeDashboardUID is a cheap heuristic, not a strict validator: real
// Grafana UIDs are short alphanumeric-plus-dash/underscore tokens with no
// spaces, so a value containing a space (a real dashboard *title* like
// "Demo App - Incidentes") is never a real UID.
func looksLikeDashboardUID(s string) bool {
	return !strings.Contains(s, " ")
}

func (te *ToolExecutor) getDashboard(ctx context.Context, arguments string) (string, error) {
	var args GetDashboardArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("parse get_dashboard args: %w", err)
	}
	if args.UID == "" {
		return "", fmt.Errorf("uid is required")
	}

	uid := args.UID
	// Real observed failure: the model called this with a dashboard's
	// human TITLE in the uid field (skipping find_dashboards/list_dashboards
	// first) for a dashboard that genuinely existed under that exact
	// title -- a flat "not found, confirm the exact name" reply was both
	// unhelpful and wrong, since the dashboard was right there. If this
	// doesn't look like a real UID, or a direct UID lookup 404s, fall back
	// to the same title search find_dashboards/list_dashboards use, so an
	// approximate or even exact-but-misrouted name still resolves.
	if looksLikeDashboardUID(uid) {
		apiPath := "/api/dashboards/uid/" + url.PathEscape(uid)
		if body, err := te.doGrafanaRequest(ctx, http.MethodGet, apiPath, nil); err == nil {
			return te.summarizeDashboardBody(body)
		} else if !strings.Contains(err.Error(), "status 404") {
			return "", err // a real error (auth, network, 5xx) -- not a not-found case to recover from
		}
	}

	resolvedUID, resolvedTitle, matches, err := te.resolveDashboardTitle(ctx, args.UID)
	if err != nil {
		return "", err
	}
	if resolvedUID == "" {
		msg := fmt.Sprintf("No dashboard found matching %q.", args.UID)
		if len(matches) > 0 {
			msg += " Did you mean one of these? " + strings.Join(matches, ", ")
		} else {
			msg += " Try find_dashboards or list_dashboards to see what's actually available, then ask the user which one they meant rather than guessing."
		}
		return fmt.Sprintf(`{"message": %q}`, msg), nil
	}
	if len(matches) > 1 {
		// More than one plausible match -- surface all of them instead of
		// silently picking one, so the caller can ask the user to confirm
		// rather than guess and potentially act on the wrong dashboard.
		return fmt.Sprintf(`{"message": %q}`, fmt.Sprintf(
			"%q matches more than one dashboard: %s. Ask the user which one they meant before proceeding.",
			args.UID, strings.Join(matches, ", "),
		)), nil
	}

	apiPath := "/api/dashboards/uid/" + url.PathEscape(resolvedUID)
	body, err := te.doGrafanaRequest(ctx, http.MethodGet, apiPath, nil)
	if err != nil {
		return "", err
	}
	summary, err := te.summarizeDashboardBody(body)
	if err != nil {
		return "", err
	}
	// Make the substitution explicit rather than silently returning a
	// different dashboard than what was literally asked for.
	return fmt.Sprintf(`{"note": %q, "dashboard": %s}`, fmt.Sprintf("interpreted %q as dashboard %q", args.UID, resolvedTitle), summary), nil
}

// resolveDashboardTitle searches for a dashboard by (approximate) title via
// Grafana's own search API, the same one find_dashboards/list_dashboards
// use. Returns the UID and title of a single confident match, or a list of
// candidate "Title (uid)" strings when the search is ambiguous or empty.
func (te *ToolExecutor) resolveDashboardTitle(ctx context.Context, query string) (uid string, title string, candidates []string, err error) {
	apiPath := "/api/search?type=dash-db&limit=25&query=" + url.QueryEscape(query)
	body, err := te.doGrafanaRequest(ctx, http.MethodGet, apiPath, nil)
	if err != nil {
		return "", "", nil, err
	}
	var hits []struct {
		Title string `json:"title"`
		UID   string `json:"uid"`
	}
	if jsonErr := json.Unmarshal([]byte(body), &hits); jsonErr != nil {
		return "", "", nil, nil //nolint:nilerr // best-effort search, not a hard failure
	}
	if len(hits) == 0 {
		return "", "", nil, nil
	}
	lowerQuery := strings.ToLower(query)
	for _, h := range hits {
		if strings.EqualFold(h.Title, query) {
			return h.UID, h.Title, nil, nil // exact title match (case-insensitive) -- confident enough to use directly
		}
	}
	for _, h := range hits {
		candidates = append(candidates, fmt.Sprintf("%s (%s)", h.Title, h.UID))
	}
	if len(hits) == 1 && strings.Contains(strings.ToLower(hits[0].Title), lowerQuery) {
		return hits[0].UID, hits[0].Title, nil, nil // exactly one substring match -- confident enough
	}
	return "", "", candidates, nil
}

func (te *ToolExecutor) summarizeDashboardBody(body string) (string, error) {

	// Extract a compact summary: title, panels with queries
	var raw struct {
		Dashboard json.RawMessage `json:"dashboard"`
	}
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return truncateString(body, 50000), nil
	}

	var dash struct {
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
		Panels      []struct {
			Title   string `json:"title"`
			Type    string `json:"type"`
			Targets []struct {
				Expr  string `json:"expr"`
				Query string `json:"query"`
				RefID string `json:"refId"`
			} `json:"targets"`
			Panels []struct {
				Title   string `json:"title"`
				Type    string `json:"type"`
				Targets []struct {
					Expr  string `json:"expr"`
					Query string `json:"query"`
					RefID string `json:"refId"`
				} `json:"targets"`
			} `json:"panels"`
		} `json:"panels"`
		Templating struct {
			List []struct {
				Name    string `json:"name"`
				Current struct {
					Text  string `json:"text"`
					Value string `json:"value"`
				} `json:"current"`
			} `json:"list"`
		} `json:"templating"`
	}
	if err := json.Unmarshal(raw.Dashboard, &dash); err != nil {
		return truncateString(body, 50000), nil
	}

	type panelSummary struct {
		Title   string   `json:"title"`
		Type    string   `json:"type"`
		Queries []string `json:"queries,omitempty"`
	}
	var panels []panelSummary
	for _, p := range dash.Panels {
		ps := panelSummary{Title: p.Title, Type: p.Type}
		for _, t := range p.Targets {
			q := t.Expr
			if q == "" {
				q = t.Query
			}
			if q != "" {
				ps.Queries = append(ps.Queries, q)
			}
		}
		if len(ps.Queries) > 0 || ps.Title != "" {
			panels = append(panels, ps)
		}
		// Nested panels (rows)
		for _, np := range p.Panels {
			nps := panelSummary{Title: np.Title, Type: np.Type}
			for _, t := range np.Targets {
				q := t.Expr
				if q == "" {
					q = t.Query
				}
				if q != "" {
					nps.Queries = append(nps.Queries, q)
				}
			}
			if len(nps.Queries) > 0 || nps.Title != "" {
				panels = append(panels, nps)
			}
		}
	}

	type variable struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	var vars []variable
	for _, v := range dash.Templating.List {
		val := v.Current.Value
		if val == "" {
			val = v.Current.Text
		}
		vars = append(vars, variable{Name: v.Name, Value: val})
	}

	summary := struct {
		Title       string         `json:"title"`
		Description string         `json:"description,omitempty"`
		Tags        []string       `json:"tags,omitempty"`
		Variables   []variable     `json:"variables,omitempty"`
		Panels      []panelSummary `json:"panels"`
	}{
		Title:       dash.Title,
		Description: dash.Description,
		Tags:        dash.Tags,
		Variables:   vars,
		Panels:      panels,
	}

	out, _ := json.Marshal(summary)
	return truncateString(string(out), 50000), nil
}

func (te *ToolExecutor) listAlerts(ctx context.Context, arguments string) (string, error) {
	var args ListAlertsArgs
	if arguments != "" && arguments != "{}" {
		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return "", fmt.Errorf("parse list_alerts args: %w", err)
		}
	}

	// This always queries Grafana-managed alerting (its built-in Alertmanager
	// API), not an external Alertmanager datasource -- no datasource UID needed.
	params := url.Values{}
	if args.Filter != "" {
		params.Set("filter", args.Filter)
	}

	apiPath := "/api/alertmanager/grafana/api/v2/alerts"
	if len(params) > 0 {
		apiPath += "?" + params.Encode()
	}

	body, err := te.doGrafanaRequest(ctx, http.MethodGet, apiPath, nil)
	if err != nil {
		return "", err
	}

	// If state filter requested, do client-side filtering. The Alertmanager
	// API this queries (/api/alertmanager/grafana/api/v2/alerts) reports
	// status.state using its OWN vocabulary -- "active"/"suppressed"/
	// "unprocessed" -- never "firing"/"pending"/"inactive" (that's a
	// DIFFERENT vocabulary, used by the alert-RULE-evaluation API at
	// /api/prometheus/grafana/api/v1/rules). This tool's own schema
	// documents "firing"/"pending"/"inactive" as the state values to pass
	// (matching how Grafana's UI and any reasonable caller would describe
	// an alert), so translate that vocabulary here rather than silently
	// matching nothing -- confirmed live: passing state="firing" against
	// the real field ("active") always filtered to zero results.
	if args.State != "" {
		wantedStates := map[string][]string{
			"firing":   {"active"},
			"pending":  {"unprocessed"},
			"inactive": {"suppressed"},
		}[strings.ToLower(args.State)]
		if wantedStates == nil {
			// Not one of the documented aliases -- match literally, in case
			// a caller already knows and passes the real Alertmanager value.
			wantedStates = []string{args.State}
		}
		matches := func(state string) bool {
			for _, w := range wantedStates {
				if state == w {
					return true
				}
			}
			return false
		}

		var alerts []map[string]any
		if err := json.Unmarshal([]byte(body), &alerts); err != nil {
			return body, nil //nolint:nilerr // Return raw body if parsing fails
		}
		filtered := make([]map[string]any, 0, len(alerts))
		for _, alert := range alerts {
			if state, ok := alert["state"].(string); ok && matches(state) {
				filtered = append(filtered, alert)
			} else if status, ok := alert["status"].(map[string]any); ok {
				if state, ok := status["state"].(string); ok && matches(state) {
					filtered = append(filtered, alert)
				}
			}
		}
		out, _ := json.Marshal(filtered)
		return truncateString(string(out), 50000), nil
	}

	return truncateString(body, 50000), nil
}

func (te *ToolExecutor) listAlertRules(ctx context.Context) (string, error) {
	return te.doGrafanaRequest(ctx, http.MethodGet, "/api/ruler/grafana/api/v1/rules", nil)
}

// analyzeActiveAlerts fetches currently firing Grafana alerts and, when
// brain-agent is available, cross-references each one's identifying labels
// against brain-agent's search_memory to surface any historical correlation
// (a past incident, root cause, or runbook note) alongside the raw alert.
// Degrades gracefully to raw alert data alone when te.mcp is nil.
func (te *ToolExecutor) analyzeActiveAlerts(ctx context.Context, arguments string) (string, error) {
	var args AnalyzeActiveAlertsArgs
	if arguments != "" && arguments != "{}" {
		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return "", fmt.Errorf("parse analyze_active_alerts args: %w", err)
		}
	}

	body, err := te.doGrafanaRequest(ctx, http.MethodGet, "/api/alertmanager/grafana/api/v2/alerts", nil)
	if err != nil {
		return "", err
	}

	var alerts []map[string]any
	if err := json.Unmarshal([]byte(body), &alerts); err != nil {
		return "", fmt.Errorf("parse alerts JSON: %w", err)
	}

	// See the matching comment in listAlerts: the Alertmanager API reports
	// status.state as "active" for a firing alert, never literally "firing".
	var firingAlerts []map[string]any
	for _, alert := range alerts {
		if state, ok := alert["state"].(string); ok && state == "active" {
			firingAlerts = append(firingAlerts, alert)
		} else if status, ok := alert["status"].(map[string]any); ok {
			if st, ok2 := status["state"].(string); ok2 && st == "active" {
				firingAlerts = append(firingAlerts, alert)
			}
		}
	}

	if len(firingAlerts) == 0 {
		return "No firing alerts in Grafana right now.", nil
	}

	maxAlerts := args.MaxAlerts
	if maxAlerts <= 0 {
		maxAlerts = 200
	}
	truncatedAlerts := false
	if len(firingAlerts) > maxAlerts {
		firingAlerts = firingAlerts[:maxAlerts]
		truncatedAlerts = true
	}

	// Group by alertname -- an alert firing across many pods/instances
	// (e.g. a node-level or per-pod alert) is one underlying problem, not
	// N unrelated ones; grouping keeps this cheap (one search_memory call
	// per group, not per instance) and lets the model see "this is firing
	// 40 times" as a single fact rather than 40 near-identical blocks.
	type alertGroup struct {
		name     string
		alerts   []map[string]any
		keywords []string
	}
	groupOrder := make([]string, 0)
	groups := make(map[string]*alertGroup)
	for _, alert := range firingAlerts {
		name, _, keywords := extractAlertIdentity(alert)
		g, ok := groups[name]
		if !ok {
			g = &alertGroup{name: name}
			groups[name] = g
			groupOrder = append(groupOrder, name)
		}
		g.alerts = append(g.alerts, alert)
		g.keywords = append(g.keywords, keywords...)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d firing alert(s) in %d group(s) by alertname.\n", len(firingAlerts), len(groupOrder))
	if truncatedAlerts {
		fmt.Fprintf(&b, "(truncated to the first %d firing alerts)\n", maxAlerts)
	}
	b.WriteString("\n")

	for _, name := range groupOrder {
		g := groups[name]
		fmt.Fprintf(&b, "--- Alert: %s (%d instance(s)) ---\n", g.name, len(g.alerts))

		alertJSON, _ := json.MarshalIndent(g.alerts, "", "  ")
		b.Write(alertJSON)
		b.WriteString("\n")

		if te.mcp != nil && len(g.keywords) > 0 {
			searchArgs := map[string]string{"query": strings.Join(dedupeStrings(g.keywords), " ")}
			if args.Project != "" {
				searchArgs["project"] = args.Project
			}
			searchJSON, _ := json.Marshal(searchArgs)

			memResult, err := te.mcp.Call(ctx, "search_memory", string(searchJSON))
			if err != nil {
				// Brain-agent being unreachable is a warning for this
				// group, never treated as "no historical match exists".
				fmt.Fprintf(&b, "\n>> Brain Agent memory search unavailable for this group: %s\n", err.Error())
			} else if memResult != "" && !strings.Contains(memResult, "currently empty") && !strings.Contains(memResult, "No matches found") {
				b.WriteString("\n>> BRAIN AGENT HISTORICAL CORRELATION FOUND:\n")
				b.WriteString(memResult)
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	}

	return truncateString(b.String(), 50000), nil
}

// dedupeStrings preserves order while dropping repeats -- a group's
// keywords accumulate the same alertname/service/namespace/pod values
// repeated once per instance, which would otherwise pad every
// search_memory query with duplicate terms for no benefit.
func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// extractAlertIdentity pulls the alertname, full label map, and a keyword
// list (alertname + service/job/namespace/pod values) out of one
// Alertmanager-shaped alert -- shared by analyzeActiveAlerts and
// investigateAlert so the two can never drift on how an alert's identity is
// read.
func extractAlertIdentity(alert map[string]any) (alertName string, labels map[string]string, keywords []string) {
	alertName = "Unknown Alert"
	labels = make(map[string]string)
	if rawLabels, ok := alert["labels"].(map[string]any); ok {
		for k, v := range rawLabels {
			valStr := fmt.Sprintf("%v", v)
			labels[k] = valStr
			switch k {
			case "alertname":
				alertName = valStr
				keywords = append(keywords, valStr)
			case "service", "job", "namespace", "pod":
				keywords = append(keywords, valStr)
			}
		}
	}
	return alertName, labels, keywords
}

// alertInvestigationMaxMatches caps how many alerts sharing the same
// alertname get individually investigated in one investigate_alert call --
// an alertname firing across dozens of pods (e.g. a node-level alert)
// would otherwise turn one tool call into dozens of Loki/Tempo queries with
// no backpressure.
const alertInvestigationMaxMatches = 5

// alertInvestigation is the evidence gathered for one matched alert --
// investigate_alert never computes a root cause itself, it only assembles
// the raw material (logs, traces, historical correlation) for the model to
// reason about.
type alertInvestigation struct {
	Alert                 map[string]any `json:"alert"`
	WindowStart           string         `json:"windowStart"`
	WindowEnd             string         `json:"windowEnd"`
	LogsQuery             string         `json:"logsQuery,omitempty"`
	Logs                  string         `json:"logs,omitempty"`
	LogsError             string         `json:"logsError,omitempty"`
	LogsSkippedReason     string         `json:"logsSkippedReason,omitempty"`
	TracesQuery           string         `json:"tracesQuery,omitempty"`
	Traces                string         `json:"traces,omitempty"`
	TracesError           string         `json:"tracesError,omitempty"`
	TracesSkippedReason   string         `json:"tracesSkippedReason,omitempty"`
	HistoricalCorrelation string         `json:"historicalCorrelation,omitempty"`
}

// buildLogSelector picks the most specific LogQL stream selector available
// from an alert's labels (namespace+pod > namespace+job > namespace > pod >
// job), biased toward error-shaped lines -- returns "" when nothing usable
// exists, so the caller can skip gracefully instead of guessing a query
// likely to return nothing or everything.
func buildLogSelector(labels map[string]string) string {
	namespace, pod, job := labels["namespace"], labels["pod"], labels["job"]
	var selector string
	switch {
	case namespace != "" && pod != "":
		selector = fmt.Sprintf(`{namespace=%q, pod=~%q}`, namespace, pod+".*")
	case namespace != "" && job != "":
		selector = fmt.Sprintf(`{namespace=%q, job=%q}`, namespace, job)
	case namespace != "":
		selector = fmt.Sprintf(`{namespace=%q}`, namespace)
	case pod != "":
		selector = fmt.Sprintf(`{pod=~%q}`, pod+".*")
	case job != "":
		selector = fmt.Sprintf(`{job=%q}`, job)
	default:
		return ""
	}
	return selector + ` |~ "(?i)error|exception|fail|panic|timeout"`
}

// buildTraceSelector picks a TraceQL service selector from a service-like
// label -- returns "" when none exists.
func buildTraceSelector(labels map[string]string) string {
	for _, key := range []string{"service", "job", "app"} {
		if v := labels[key]; v != "" {
			return fmt.Sprintf(`{resource.service.name=%q}`, v)
		}
	}
	return ""
}

// investigateAlert is the deliberate, expensive deep-dive on ONE firing
// alert -- kept separate from analyze_active_alerts (which stays cheap and
// broad) so an alertname shared across many firing alerts can't turn one
// call into an unbounded burst of Loki/Tempo queries. For each matched
// alert (capped at alertInvestigationMaxMatches), it automatically gathers
// the Loki logs and Tempo traces around the alert's firing window plus any
// brain-agent historical correlation, in one round trip -- a single
// Loki/Tempo/MCP failure degrades that one section only, never aborts the
// whole call.
func (te *ToolExecutor) investigateAlert(ctx context.Context, arguments string) (string, error) {
	var args InvestigateAlertArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("parse investigate_alert args: %w", err)
	}
	if strings.TrimSpace(args.AlertName) == "" {
		return "", fmt.Errorf("alertname is required")
	}

	body, err := te.doGrafanaRequest(ctx, http.MethodGet, "/api/alertmanager/grafana/api/v2/alerts", nil)
	if err != nil {
		return "", err
	}
	var alerts []map[string]any
	if err := json.Unmarshal([]byte(body), &alerts); err != nil {
		return "", fmt.Errorf("parse alerts JSON: %w", err)
	}

	var matches []map[string]any
	for _, alert := range alerts {
		state, _ := alert["state"].(string)
		if state == "" {
			if status, ok := alert["status"].(map[string]any); ok {
				state, _ = status["state"].(string)
			}
		}
		if state != "active" {
			continue
		}
		name, labels, _ := extractAlertIdentity(alert)
		if !strings.EqualFold(name, args.AlertName) {
			continue
		}
		if args.Namespace != "" && labels["namespace"] != args.Namespace {
			continue
		}
		matches = append(matches, alert)
		if len(matches) >= alertInvestigationMaxMatches {
			break
		}
	}

	if len(matches) == 0 {
		// The exact-match scan above already fetched every currently firing
		// alert -- reuse it for a substring-based "did you mean" suggestion
		// instead of just telling the caller to go look again. A real
		// observed failure in the analogous get_dashboard case: a flat
		// "not found, confirm the exact name" reply when a close (or even
		// exact) match genuinely existed was unhelpful and easy to avoid.
		lowerWant := strings.ToLower(args.AlertName)
		seen := map[string]bool{}
		var suggestions []string
		for _, alert := range alerts {
			state, _ := alert["state"].(string)
			if state == "" {
				if status, ok := alert["status"].(map[string]any); ok {
					state, _ = status["state"].(string)
				}
			}
			if state != "active" {
				continue
			}
			name, _, _ := extractAlertIdentity(alert)
			if name == "" || seen[name] {
				continue
			}
			if strings.Contains(strings.ToLower(name), lowerWant) || strings.Contains(lowerWant, strings.ToLower(name)) {
				seen[name] = true
				suggestions = append(suggestions, name)
			}
		}
		if len(suggestions) > 0 {
			return fmt.Sprintf("No firing alert named %q found. Did you mean one of these currently firing alerts? %s -- ask the user to confirm which one before investigating.", args.AlertName, strings.Join(suggestions, ", ")), nil
		}
		return fmt.Sprintf("No firing alert named %q found -- call list_alerts or analyze_active_alerts first to see current alert names.", args.AlertName), nil
	}

	now := time.Now()
	investigations := make([]alertInvestigation, 0, len(matches))
	for _, alert := range matches {
		_, labels, keywords := extractAlertIdentity(alert)

		windowStart, windowEnd := now.Add(-15*time.Minute), now
		if startsAtStr, ok := alert["startsAt"].(string); ok && startsAtStr != "" {
			if startsAt, err := time.Parse(time.RFC3339, startsAtStr); err == nil {
				windowStart, windowEnd = startsAt.Add(-5*time.Minute), now
			}
		}

		inv := alertInvestigation{
			Alert:       alert,
			WindowStart: windowStart.UTC().Format(time.RFC3339),
			WindowEnd:   windowEnd.UTC().Format(time.RFC3339),
		}

		if logSelector := buildLogSelector(labels); logSelector == "" {
			inv.LogsSkippedReason = "no usable namespace/pod/job label to build a log query from"
		} else {
			inv.LogsQuery = logSelector
			logArgs, _ := json.Marshal(LokiQueryArgs{
				Query: logSelector,
				Start: fmt.Sprintf("%d", windowStart.UnixNano()),
				End:   fmt.Sprintf("%d", windowEnd.UnixNano()),
				Limit: 50,
			})
			if result, err := te.queryLoki(ctx, string(logArgs)); err != nil {
				inv.LogsError = err.Error()
			} else {
				inv.Logs = truncateString(result, 10000)
			}
		}

		if traceSelector := buildTraceSelector(labels); traceSelector == "" {
			inv.TracesSkippedReason = "no usable service/job/app label to build a trace query from"
		} else {
			inv.TracesQuery = traceSelector
			traceArgs, _ := json.Marshal(TempoQueryArgs{
				Query: traceSelector,
				Start: fmt.Sprintf("%d", windowStart.Unix()),
				End:   fmt.Sprintf("%d", windowEnd.Unix()),
				Limit: 5,
			})
			if result, err := te.queryTempo(ctx, string(traceArgs)); err != nil {
				inv.TracesError = err.Error()
			} else {
				inv.Traces = truncateString(result, 10000)
			}
		}

		if te.mcp != nil && len(keywords) > 0 {
			searchArgs := map[string]string{"query": strings.Join(keywords, " ")}
			if args.Project != "" {
				searchArgs["project"] = args.Project
			}
			searchJSON, _ := json.Marshal(searchArgs)
			if memResult, err := te.mcp.Call(ctx, "search_memory", string(searchJSON)); err == nil &&
				memResult != "" && !strings.Contains(memResult, "currently empty") && !strings.Contains(memResult, "No matches found") {
				inv.HistoricalCorrelation = memResult
			}
		}

		investigations = append(investigations, inv)
	}

	out, _ := json.Marshal(map[string]any{"matches": len(investigations), "investigations": investigations})
	return truncateString(string(out), 50000), nil
}

// grafanaVersion returns this instance's real Grafana version (e.g.
// "12.3.7"), fetched once from its own /api/health endpoint (no auth
// concerns beyond this plugin's already-configured service account -- this
// endpoint is deliberately cheap and always available) and cached
// indefinitely. Returns "" on any failure (parse error, unreachable, etc.)
// rather than erroring the whole request -- a missing version line just
// means the model falls back to general Grafana knowledge without a
// specific version to anchor it, not a broken tool call.
func (te *ToolExecutor) grafanaVersion(ctx context.Context) string {
	if te.grafanaURL == "" {
		return ""
	}
	te.versionCacheMu.Lock()
	if te.versionCache != "" {
		v := te.versionCache
		te.versionCacheMu.Unlock()
		return v
	}
	te.versionCacheMu.Unlock()

	body, err := te.doGrafanaRequest(ctx, http.MethodGet, "/api/health", nil)
	if err != nil {
		return ""
	}
	var health struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(body), &health); err != nil || health.Version == "" {
		return ""
	}

	te.versionCacheMu.Lock()
	te.versionCache = health.Version
	te.versionCacheMu.Unlock()
	return health.Version
}

// brainAgentInstallState distinguishes brain-agent being genuinely absent
// from being installed-but-disabled -- brain-agent's own MCP endpoint
// (mcp.go) 404s identically in both cases (Grafana's resource routing has
// nothing to dispatch to either way), so that alone can never tell an admin
// "install it" apart from "just enable it". Grafana's own plugin registry
// (/api/plugins/:id/settings) knows the difference: absent entirely (404)
// vs present with enabled=false.
type brainAgentInstallState int

const (
	brainAgentStateUnknown brainAgentInstallState = iota
	brainAgentNotInstalled
	brainAgentDisabled
	brainAgentEnabled
	// brainAgentAuthError means Grafana itself rejected this plugin's own
	// service account token (401/403) while checking brain-agent's install
	// state -- a real configuration problem on THIS plugin's side (an
	// invalid/expired/under-scoped grafanaToken), not a fact about
	// brain-agent at all. Distinguished from brainAgentStateUnknown because
	// it has a specific, actionable fix (regenerate the token), same
	// reasoning as brainAgentStatus's existing IntegrationStatusDegraded
	// case for the Configuration page.
	brainAgentAuthError
	// brainAgentIntegrationOff means THIS assistant's own
	// EnableBrainAgentTools setting is off -- distinct from
	// brainAgentDisabled (which means brain-agent's own plugin, at the
	// Grafana level, is installed but disabled). Live-found real bug this
	// fixes: leaving state at brainAgentStateUnknown here (the pre-existing
	// behavior, since checking brain-agent's install state was skipped
	// entirely whenever this integration is off) made
	// brainAgentDefinitelyUnavailable return false, so a model confidently
	// claiming "I've saved that to memory" went uncorrected even though NO
	// memory tool existed in that turn's tool list at all. This state fixes
	// that (memory access really is 100% impossible whenever this is off,
	// regardless of brain-agent's own separate state) while keeping the
	// user-facing guidance accurate -- reusing brainAgentDisabled's message
	// verbatim would wrongly tell an admin to flip a toggle on brain-agent's
	// OWN Configuration page, when the actual fix is in agent-ai-app's own
	// settings instead.
	brainAgentIntegrationOff
)

// brainAgentInstallState queries Grafana's own plugin registry fresh on
// every call rather than caching -- unlike grafanaVersion (which truly can't
// change while this instance runs), this reflects an admin toggle that can
// flip at any moment (Administration > Plugins > Brain Agent), and this is
// only ever called once per chat turn (system-prompt grounding) plus, on the
// rarer error path, once more right when an MCP tool call fails -- not a hot
// loop. The returned version string (Grafana's own registry entry for
// brain-agent, e.g. "1.0.0") is "" whenever the state itself couldn't be
// determined (not installed / auth error / unknown) -- it's only ever real
// data when brain-agent's own /settings response was actually parsed.
func (te *ToolExecutor) brainAgentInstallState(ctx context.Context) (brainAgentInstallState, string) {
	if te.grafanaURL == "" {
		return brainAgentStateUnknown, ""
	}
	body, err := te.doGrafanaRequest(ctx, http.MethodGet, "/api/plugins/shortbobcat2735-brainagent-app/settings", nil)
	if err != nil {
		switch {
		case strings.Contains(err.Error(), "status 404"):
			return brainAgentNotInstalled, ""
		case strings.Contains(err.Error(), "status 401"), strings.Contains(err.Error(), "status 403"):
			return brainAgentAuthError, ""
		default:
			return brainAgentStateUnknown, ""
		}
	}
	var parsed struct {
		Enabled bool `json:"enabled"`
		Info    struct {
			Version string `json:"version"`
		} `json:"info"`
	}
	if json.Unmarshal([]byte(body), &parsed) != nil {
		return brainAgentStateUnknown, ""
	}
	if parsed.Enabled {
		return brainAgentEnabled, parsed.Info.Version
	}
	return brainAgentDisabled, parsed.Info.Version
}

const dsCacheTTL = 30 * time.Second

// datasourceUIDsByType returns every datasource UID of dsType (e.g.
// "prometheus", "loki", "tempo"), cached for dsCacheTTL to avoid refetching
// the full datasource list on every tool call.
func (te *ToolExecutor) datasourceUIDsByType(ctx context.Context, dsType string) ([]string, error) {
	te.dsCacheMu.Lock()
	if te.dsCache != nil && time.Since(te.dsCacheTime) < dsCacheTTL {
		uids := te.dsCache[dsType]
		te.dsCacheMu.Unlock()
		return uids, nil
	}
	te.dsCacheMu.Unlock()

	body, err := te.doGrafanaRequest(ctx, http.MethodGet, "/api/datasources", nil)
	if err != nil {
		return nil, err
	}

	var datasources []struct {
		Type string `json:"type"`
		UID  string `json:"uid"`
	}
	if err := json.Unmarshal([]byte(body), &datasources); err != nil {
		return nil, fmt.Errorf("parse datasources: %w", err)
	}

	cache := make(map[string][]string, len(datasources))
	for _, ds := range datasources {
		cache[ds.Type] = append(cache[ds.Type], ds.UID)
	}

	te.dsCacheMu.Lock()
	te.dsCache = cache
	te.dsCacheTime = time.Now()
	te.dsCacheMu.Unlock()

	return cache[dsType], nil
}

// resolveDatasourceUID picks the datasource UID a tool should query against.
// providedUID -- from the tool's own optional datasource_uid argument --
// always wins when set: Grafana's proxy call fails clearly if it's wrong,
// which is signal enough, and it's the only way to target a specific
// cluster/tenant when more than one datasource of dsType exists. Without
// one: exactly one datasource of dsType is used automatically (this
// plugin's original, single-datasource-per-type behavior, unchanged for the
// common case). Zero or more than one is an error instead of silently
// guessing -- a multi-cluster/multi-tenant Grafana must never have a tool
// silently pick "the first Prometheus it happens to see"; the error message
// itself names the candidates so the caller can retry immediately, without
// a round trip through list_datasources first.
// datasourceAllowed reports whether a UID may be queried. An empty allowlist
// means unrestricted -- the default, and what every existing install gets.
func (te *ToolExecutor) datasourceAllowed(uid string) bool {
	if len(te.allowedDatasourceUIDs) == 0 {
		return true
	}
	return te.allowedDatasourceUIDs[uid]
}

// allowedUIDs filters a UID list down to what the admin permits.
func (te *ToolExecutor) allowedUIDs(uids []string) []string {
	if len(te.allowedDatasourceUIDs) == 0 {
		return uids
	}
	out := make([]string, 0, len(uids))
	for _, uid := range uids {
		if te.allowedDatasourceUIDs[uid] {
			out = append(out, uid)
		}
	}
	return out
}

func (te *ToolExecutor) resolveDatasourceUID(ctx context.Context, dsType, providedUID string) (string, error) {
	if providedUID != "" {
		// A UID chosen by the model is still checked: it may have seen one
		// in a dashboard, an alert rule, or the panel context, none of which
		// go through the filtered discovery path.
		if !te.datasourceAllowed(providedUID) {
			return "", fmt.Errorf("datasource %q is not permitted by this plugin's configuration -- call list_datasources to see the ones you may query", providedUID)
		}
		return providedUID, nil
	}
	uids, err := te.datasourceUIDsByType(ctx, dsType)
	if err != nil {
		return "", err
	}
	uids = te.allowedUIDs(uids)
	switch len(uids) {
	case 0:
		return "", fmt.Errorf("no permitted datasource of type %q found", dsType)
	case 1:
		return uids[0], nil
	default:
		return "", fmt.Errorf("%d datasources of type %q found (%s) -- call list_datasources to see them, then retry with a specific datasource_uid", len(uids), dsType, strings.Join(uids, ", "))
	}
}

func (te *ToolExecutor) doGrafanaRequest(ctx context.Context, method, path string, body io.Reader) (string, error) {
	// The single point every tool goes through to reach the Grafana API, so
	// the only place to instrument to know what a tool actually did (see
	// api_call_recorder.go).
	recordAPICall(ctx, method, path)
	reqURL := te.grafanaURL + path
	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	// Every call to the Grafana API is made as this plugin's own service
	// account, never as whatever identity the calling browser happened to
	// send -- security-audit finding H-01: Cookie/Authorization/
	// X-Grafana-Org-Id from the client used to be forwarded here and could
	// override these defaults, which meant a client-supplied header (fully
	// attacker-controllable, unlike an httpOnly session cookie) decided
	// which org/identity the tool executor queried Grafana as. Grafana's own
	// guidance is a single service account per plugin instance with the
	// backend enforcing authorization itself (see requesterRole), not
	// per-request identity forwarding.
	for k, v := range te.defaultHeaders {
		req.Header.Set(k, v)
	}
	// If a token file path is configured, read it on each request so
	// rotated tokens (e.g. Kubernetes secret updates) are picked up.
	if te.tokenPath != "" {
		if token, err := readTokenFile(te.tokenPath); err != nil {
			te.logger.Warn("Failed to read token file, falling back to default headers", "path", te.tokenPath, "error", err)
		} else if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	req.Header.Set("Accept", "application/json")
	// Every call site sending a body sends JSON (e.g. analyze_cloud_resource/
	// query_datasource's POST /api/ds/query) -- without this, Grafana's own
	// request binding fails the body as malformed ("bad request data", 400)
	// even though the JSON itself is well-formed. Every existing GET-only
	// call site is unaffected (body is nil there).
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	te.logger.Debug("Tool executor request", "method", method, "path", path,
		"hasDefaultAuth", te.defaultHeaders["Authorization"] != "")

	resp, err := te.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("execute request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Limit response body to prevent memory exhaustion (10 MB)
	const maxResponseBytes = 10 * 1024 * 1024
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		te.logger.Error("Datasource request failed", "status", resp.StatusCode, "path", path, "body", truncateString(string(respBody), 200))
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return "", fmt.Errorf("grafana rejected this plugin's service account token (status %d) -- grafanaToken is likely invalid, expired, or lacks permission for this resource; an admin should regenerate it in the plugin's settings", resp.StatusCode)
		}
		return "", fmt.Errorf("datasource returned status %d: %s", resp.StatusCode, truncateString(string(respBody), 500))
	}

	te.logger.Debug("Tool executor response", "status", resp.StatusCode, "bodyLen", len(respBody))
	result := string(respBody)
	return truncateString(result, 50000), nil
}

// resolveTime converts relative time strings like "now-1h" to Unix timestamps.
func resolveTime(s string, now time.Time) string {
	return resolveTimeUnix(s, now)
}

func resolveTimeUnix(s string, now time.Time) string {
	t, ok := resolveTimeValue(s, now)
	if !ok {
		return s
	}
	return fmt.Sprintf("%d", t.Unix())
}

func resolveTimeNano(s string, now time.Time) string {
	t, ok := resolveTimeValue(s, now)
	if !ok {
		return s
	}
	return fmt.Sprintf("%d", t.UnixNano())
}

// resolveTimeValue parses a model-supplied time expression: "now", "now-10m",
// "now+1h", a bare relative duration ("10m", "-5m", meaning "that long ago"),
// or an absolute value the caller already understands (ok=false, left as-is).
func resolveTimeValue(s string, now time.Time) (time.Time, bool) {
	s = strings.TrimSpace(s)

	if s == "now" {
		return now, true
	}

	if strings.HasPrefix(s, "now") {
		rest := strings.TrimPrefix(s, "now")
		if rest == "" {
			return now, true
		}
		neg := strings.HasPrefix(rest, "-")
		durStr := strings.TrimPrefix(strings.TrimPrefix(rest, "-"), "+")
		// parseDayAwareDuration (not the stdlib time.ParseDuration) --
		// "now-7d"/"now-30d" are natural, common things for a model to ask
		// for, but time.ParseDuration has no "d" unit and used to silently
		// return `now` unchanged (still claiming success) whenever it
		// failed to parse the suffix, instead of reporting failure. A
		// caller asking for the last 30 days got a zero-width "now..now"
		// window with no indication anything went wrong -- found live
		// while validating build_change_timeline against real seeded data.
		d, err := parseDayAwareDuration(durStr)
		if err != nil {
			return time.Time{}, false
		}
		if neg {
			return now.Add(-d), true
		}
		return now.Add(d), true
	}

	// Models often pass a bare relative duration ("10m", "1h", "-5m", "7d")
	// instead of the "now-10m" form or an absolute timestamp. Treat a bare
	// duration as "that long ago" -- the only sensible reading for a query
	// start/end.
	if d, err := parseDayAwareDuration(strings.TrimPrefix(s, "-")); err == nil {
		return now.Add(-d), true
	}

	return time.Time{}, false
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	// Walk back from maxLen to find a valid rune start
	for maxLen > 0 && !utf8.RuneStart(s[maxLen]) {
		maxLen--
	}
	return s[:maxLen] + "... [truncated]"
}

// readTokenFile reads a bearer token from a file, trimming whitespace.
// Returns ("", nil) for empty files and ("", err) for read errors.
func readTokenFile(path string) (string, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- admin-configured service-account token file path
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
