package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
)

func followCorrelationMock(t *testing.T) *httptest.Server {
	t.Helper()
	var mux http.ServeMux
	mux.HandleFunc("/api/datasources/correlations", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"correlations":[
			{
				"sourceUID": "loki-uid",
				"targetUID": "tempo-uid",
				"label": "Logs to traces",
				"config": {"field": "trace_id", "target": {"query": "${__value.raw}"}}
			}
		]}`))
	})
	mux.HandleFunc("/api/datasources", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"type":"loki","uid":"loki-uid"},{"type":"tempo","uid":"tempo-uid"}]`))
	})
	mux.HandleFunc("/api/datasources/proxy/uid/tempo-uid/api/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"traces":[{"traceID":"abc123real"}]}`))
	})
	return httptest.NewServer(&mux)
}

func TestFollowCorrelation_InterpolatesAndRunsTargetQuery(t *testing.T) {
	t.Parallel()

	server := followCorrelationMock(t)
	defer server.Close()

	te := NewToolExecutor(server.URL, log.DefaultLogger)
	result, err := te.followCorrelation(context.Background(), `{"source_datasource_uid":"loki-uid","field":"trace_id","field_value":"abc123real"}`)
	if err != nil {
		t.Fatalf("followCorrelation returned error: %v", err)
	}

	var parsed struct {
		TargetType    string `json:"target_type"`
		ResolvedQuery string `json:"resolved_query"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("unmarshal result: %v (raw: %s)", err, result)
	}
	if parsed.TargetType != "tempo" {
		t.Errorf("target_type = %q, want tempo", parsed.TargetType)
	}
	if parsed.ResolvedQuery != "abc123real" {
		t.Errorf("resolved_query = %q, want the interpolated value", parsed.ResolvedQuery)
	}
	if !strings.Contains(result, "abc123real") {
		t.Errorf("result should contain the real query result: %s", result)
	}
}

func TestFollowCorrelation_NoMatchReturnsGracefulMessage(t *testing.T) {
	t.Parallel()

	server := followCorrelationMock(t)
	defer server.Close()

	te := NewToolExecutor(server.URL, log.DefaultLogger)
	result, err := te.followCorrelation(context.Background(), `{"source_datasource_uid":"loki-uid","field":"pod","field_value":"whatever"}`)
	if err != nil {
		t.Fatalf("followCorrelation returned error: %v", err)
	}
	if !strings.Contains(result, "No correlation found") {
		t.Errorf("result = %q, want a graceful no-match message", result)
	}
}

func TestFollowCorrelation_AmbiguousMatchListsLabels(t *testing.T) {
	t.Parallel()

	var mux http.ServeMux
	mux.HandleFunc("/api/datasources/correlations", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"correlations":[
			{"sourceUID":"loki-uid","targetUID":"tempo-uid","label":"To traces","config":{"field":"id","target":{"query":"${__value.raw}"}}},
			{"sourceUID":"loki-uid","targetUID":"prom-uid","label":"To metrics","config":{"field":"id","target":{"query":"${__value.raw}"}}}
		]}`))
	})
	server := httptest.NewServer(&mux)
	defer server.Close()

	te := NewToolExecutor(server.URL, log.DefaultLogger)
	result, err := te.followCorrelation(context.Background(), `{"source_datasource_uid":"loki-uid","field":"id","field_value":"x"}`)
	if err != nil {
		t.Fatalf("followCorrelation returned error: %v", err)
	}
	if !strings.Contains(result, "More than one correlation") || !strings.Contains(result, "To traces") || !strings.Contains(result, "To metrics") {
		t.Errorf("result = %q, want both labels listed for disambiguation", result)
	}
}

func TestFollowCorrelation_RequiresAllArgs(t *testing.T) {
	t.Parallel()

	te := NewToolExecutor("http://unused", log.DefaultLogger)
	if _, err := te.followCorrelation(context.Background(), `{"source_datasource_uid":"loki-uid"}`); err == nil {
		t.Error("expected an error when field/field_value are missing")
	}
}

// The datasource allowlist has to hold here too, not just in
// resolveDatasourceUID -- otherwise an admin restricting datasources would
// still leak another datasource's correlation label/description/query
// template (and let the model run a query against its target) through this
// tool alone.
func TestFollowCorrelation_RejectsDisallowedSourceUID(t *testing.T) {
	t.Parallel()

	server := followCorrelationMock(t)
	defer server.Close()

	te := NewToolExecutor(server.URL, log.DefaultLogger)
	te.allowedDatasourceUIDs = map[string]bool{"tempo-uid": true} // loki-uid (the source) excluded

	if _, err := te.followCorrelation(context.Background(), `{"source_datasource_uid":"loki-uid","field":"trace_id","field_value":"abc123real"}`); err == nil {
		t.Error("a source datasource outside the allowlist must be refused")
	}
}

func TestFollowCorrelation_RejectsDisallowedTargetUID(t *testing.T) {
	t.Parallel()

	server := followCorrelationMock(t)
	defer server.Close()

	te := NewToolExecutor(server.URL, log.DefaultLogger)
	te.allowedDatasourceUIDs = map[string]bool{"loki-uid": true} // tempo-uid (the target) excluded

	if _, err := te.followCorrelation(context.Background(), `{"source_datasource_uid":"loki-uid","field":"trace_id","field_value":"abc123real"}`); err == nil {
		t.Error("a target datasource outside the allowlist must be refused")
	}
}

func TestInterpolateCorrelationTarget_ReplacesPlaceholder(t *testing.T) {
	t.Parallel()

	out, err := interpolateCorrelationTarget(json.RawMessage(`{"query":"{trace_id=\"${__value.raw}\"}"}`), "abc123")
	if err != nil {
		t.Fatalf("interpolateCorrelationTarget error: %v", err)
	}
	if !strings.Contains(string(out), "abc123") {
		t.Errorf("interpolated = %s, want the real value substituted", out)
	}
	if strings.Contains(string(out), "__value.raw") {
		t.Errorf("interpolated = %s, placeholder should be fully replaced", out)
	}
}
