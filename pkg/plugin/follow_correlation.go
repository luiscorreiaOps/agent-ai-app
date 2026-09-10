package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// FollowCorrelationArgs holds parsed arguments for follow_correlation.
type FollowCorrelationArgs struct {
	SourceDatasourceUID string `json:"source_datasource_uid"`
	Field               string `json:"field"`
	FieldValue          string `json:"field_value"`
	// Label disambiguates when more than one correlation exists for the
	// same source+field -- optional, only needed in that case.
	Label string `json:"label,omitempty"`
}

// correlationConfig is the subset of Grafana's /api/datasources/correlations
// response this tool needs -- same shape listCorrelations already parses,
// duplicated here (rather than reusing listCorrelations's already-summarized
// return value) because this tool needs the raw target template, which
// listCorrelations's own output type doesn't carry through.
type correlationConfig struct {
	SourceUID   string `json:"sourceUID"`
	TargetUID   string `json:"targetUID"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Config      struct {
		Field  string          `json:"field"`
		Target json.RawMessage `json:"target"`
	} `json:"config"`
}

// interpolateCorrelationTarget replaces Grafana's "${__value.raw}"
// placeholder in every string value of a correlation's target template with
// the real field value the caller supplied. This is deliberately a narrow
// subset of Grafana's own correlation variable syntax (which also supports
// ${__data.fields.X} and more) -- covers the common, documented case of
// "take this field's value and drop it into the target query" without
// pretending to fully reimplement Grafana's template engine.
func interpolateCorrelationTarget(target json.RawMessage, fieldValue string) (json.RawMessage, error) {
	var parsed map[string]any
	if err := json.Unmarshal(target, &parsed); err != nil {
		return nil, fmt.Errorf("parse target template: %w", err)
	}
	interpolateValue(parsed, fieldValue)
	out, err := json.Marshal(parsed)
	if err != nil {
		return nil, fmt.Errorf("marshal interpolated target: %w", err)
	}
	return out, nil
}

func interpolateValue(m map[string]any, fieldValue string) {
	for k, v := range m {
		switch val := v.(type) {
		case string:
			m[k] = strings.ReplaceAll(val, "${__value.raw}", fieldValue)
		case map[string]any:
			interpolateValue(val, fieldValue)
		}
	}
}

// followCorrelation resolves a Grafana Correlation (source datasource +
// field -> target datasource + query template) for a real field value and
// dispatches the interpolated query to whichever existing tool handles that
// target datasource's type -- instead of leaving the model to interpolate
// the template and pick the right tool call itself, which list_correlations
// alone only describes but doesn't execute.
func (te *ToolExecutor) followCorrelation(ctx context.Context, arguments string) (string, error) {
	var args FollowCorrelationArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("parse follow_correlation args: %w", err)
	}
	if args.SourceDatasourceUID == "" || args.Field == "" || args.FieldValue == "" {
		return "", fmt.Errorf("source_datasource_uid, field, and field_value are all required")
	}
	// Same allowlist resolveDatasourceUID enforces -- a UID the model got
	// from a dashboard/panel/prior tool result never passes through
	// list_datasources' filtered discovery, so it has to be checked here too.
	if !te.datasourceAllowed(args.SourceDatasourceUID) {
		return "", fmt.Errorf("datasource %q is not permitted by this plugin's configuration -- call list_datasources to see the ones you may query", args.SourceDatasourceUID)
	}

	body, err := te.doGrafanaRequest(ctx, http.MethodGet, "/api/datasources/correlations", nil)
	if err != nil {
		return "", err
	}
	var resp struct {
		Correlations []correlationConfig `json:"correlations"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return "", fmt.Errorf("parse correlations: %w", err)
	}

	var matches []correlationConfig
	for _, c := range resp.Correlations {
		if c.SourceUID != args.SourceDatasourceUID || c.Config.Field != args.Field {
			continue
		}
		if args.Label != "" && c.Label != args.Label {
			continue
		}
		matches = append(matches, c)
	}

	if len(matches) == 0 {
		return fmt.Sprintf(`{"message": %q}`, fmt.Sprintf(
			"No correlation found for datasource %q field %q -- call list_correlations first to see what's actually configured.",
			args.SourceDatasourceUID, args.Field)), nil
	}
	if len(matches) > 1 {
		labels := make([]string, 0, len(matches))
		for _, m := range matches {
			labels = append(labels, m.Label)
		}
		return fmt.Sprintf(`{"message": %q}`, fmt.Sprintf(
			"More than one correlation matches datasource %q field %q: %s -- pass \"label\" to pick one.",
			args.SourceDatasourceUID, args.Field, strings.Join(labels, ", "))), nil
	}

	match := matches[0]
	if !te.datasourceAllowed(match.TargetUID) {
		return "", fmt.Errorf("the target datasource %q for this correlation is not permitted by this plugin's configuration", match.TargetUID)
	}
	interpolated, err := interpolateCorrelationTarget(match.Config.Target, args.FieldValue)
	if err != nil {
		return "", err
	}

	targetType, err := te.datasourceType(ctx, match.TargetUID)
	if err != nil {
		return fmt.Sprintf(`{"message": %q, "interpolated_target": %s}`,
			"Could not determine the target datasource's type -- here is the interpolated query template for you to run manually.", string(interpolated)), nil
	}

	var target struct {
		Query string `json:"query"`
		Expr  string `json:"expr"`
	}
	_ = json.Unmarshal(interpolated, &target)
	query := target.Query
	if query == "" {
		query = target.Expr
	}
	if query == "" {
		return fmt.Sprintf(`{"message": %q, "interpolated_target": %s}`,
			"Interpolated the correlation's target template, but couldn't find a recognizable query/expr field to auto-run -- here it is for you to run manually.", string(interpolated)), nil
	}

	switch targetType {
	case "loki":
		result, err := te.queryLoki(ctx, mustMarshal(LokiQueryArgs{Query: query}))
		return wrapFollowedCorrelationResult(match, targetType, query, result, err)
	case "tempo":
		result, err := te.queryTempo(ctx, mustMarshal(TempoQueryArgs{Query: query}))
		return wrapFollowedCorrelationResult(match, targetType, query, result, err)
	case "prometheus":
		result, err := te.queryPrometheus(ctx, mustMarshal(PrometheusQueryArgs{Query: query}))
		return wrapFollowedCorrelationResult(match, targetType, query, result, err)
	default:
		return fmt.Sprintf(`{"message": %q, "target_type": %q, "interpolated_target": %s}`,
			"Target datasource type isn't one this plugin can auto-run a correlation against yet -- here is the interpolated query for you to run manually.", targetType, string(interpolated)), nil
	}
}

type followedCorrelationResult struct {
	ToolResult
	CorrelationLabel string          `json:"correlation_label"`
	TargetType       string          `json:"target_type"`
	ResolvedQuery    string          `json:"resolved_query"`
	Result           json.RawMessage `json:"result"`
}

func wrapFollowedCorrelationResult(match correlationConfig, targetType, query, result string, err error) (string, error) {
	if err != nil {
		return "", fmt.Errorf("run correlated %s query: %w", targetType, err)
	}
	out, marshalErr := json.Marshal(followedCorrelationResult{
		ToolResult: ToolResult{
			Summary: fmt.Sprintf("followed correlation %q to a %s query", match.Label, targetType),
			Sources: []string{targetType + " (via correlation " + match.Label + ")"},
		},
		CorrelationLabel: match.Label,
		TargetType:       targetType,
		ResolvedQuery:    query,
		Result:           json.RawMessage(result),
	})
	if marshalErr != nil {
		return "", marshalErr
	}
	return truncateString(string(out), 50000), nil
}

// datasourceType looks up a datasource's type by UID -- a fresh,
// uncached fetch (this tool is called rarely, unlike findDatasource's
// type->uid lookup which every query_* tool call needs).
func (te *ToolExecutor) datasourceType(ctx context.Context, uid string) (string, error) {
	body, err := te.doGrafanaRequest(ctx, http.MethodGet, "/api/datasources", nil)
	if err != nil {
		return "", err
	}
	var datasources []struct {
		Type string `json:"type"`
		UID  string `json:"uid"`
	}
	if err := json.Unmarshal([]byte(body), &datasources); err != nil {
		return "", fmt.Errorf("parse datasources: %w", err)
	}
	for _, ds := range datasources {
		if ds.UID == uid {
			return ds.Type, nil
		}
	}
	// A live incident (2026-08-11) showed a model call this with a
	// hallucinated datasource_uid (a placeholder-looking string it invented
	// rather than one it actually looked up), then give up and ask the USER
	// to supply a real UID instead of calling list_datasources itself. The
	// datasources slice is already fetched above -- listing the real UIDs
	// right here costs nothing and lets the model self-correct next round
	// without a wasted extra round trip through list_datasources.
	var available []string
	for _, ds := range datasources {
		available = append(available, fmt.Sprintf("%s (%s)", ds.UID, ds.Type))
	}
	return "", fmt.Errorf("datasource %q not found -- real datasources available: %s", uid, strings.Join(available, ", "))
}

func mustMarshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// Every call site passes a plain struct literal with no channels,
		// funcs, or cyclic references -- json.Marshal cannot fail on these
		// in practice, but panicking here (rather than swallowing the
		// error) would surface a real bug immediately instead of silently
		// sending an empty/wrong query downstream.
		panic(fmt.Sprintf("mustMarshal: %v", err))
	}
	return string(b)
}
