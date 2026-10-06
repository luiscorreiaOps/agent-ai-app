package plugin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This opt-in integration check sends the real plugin payload to a served
// decision model. Regular CI never downloads weights or starts inference.
func TestDecisionLocalModel(t *testing.T) {
	endpoint := os.Getenv("AGENTAI_DECISION_LIVE_URL")
	if endpoint == "" {
		t.Skip("set AGENTAI_DECISION_LIVE_URL to validate a real local decision model")
	}
	model := os.Getenv("AGENTAI_DECISION_LIVE_MODEL")
	if model == "" {
		model = "Mapika/decider-2b"
	}
	pool := filterEnabledTools(llmTools("generic"), []string{"query_prometheus", "query_loki", "query_tempo"})
	// The minimum smoke test checks unambiguous choices; the full catalog
	// checks prompt compatibility with overlapping specialist tools. Give
	// inference extra time without changing the plugin's production deadline.
	fullCatalog := os.Getenv("AGENTAI_DECISION_LIVE_FULL_CATALOG") == "1"
	if fullCatalog {
		pool = llmTools("generic")
	}
	timeout := 2 * time.Minute
	if value := os.Getenv("AGENTAI_DECISION_LIVE_TIMEOUT"); value != "" {
		var err error
		timeout, err = time.ParseDuration(value)
		if err != nil || timeout <= 0 || timeout > 2*time.Minute {
			t.Fatal("AGENTAI_DECISION_LIVE_TIMEOUT must be positive and at most 2m")
		}
	}
	for _, tc := range []struct {
		name, prompt, want string
	}{
		{"prometheus_en", "Use query_prometheus to run the PromQL expression up.", "query_prometheus"},
		{"prometheus_pt", "Consulte no Prometheus a expressão PromQL up para verificar as métricas atuais.", "query_prometheus"},
		{"loki", "Use query_loki to search Loki logs for error messages.", "query_loki"},
		{"tempo", "Use query_tempo to search Tempo traces for the checkout service.", "query_tempo"},
		{"no_tool", "What is 2 + 2? Answer without consulting any datasource.", decisionNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := Settings{DecisionEndpointURL: endpoint, DecisionModel: model, DecisionTimeoutMs: 10000}
			p := newHTTPDecisionProvider(s)
			p.client.Timeout = timeout
			if dir := os.Getenv("AGENTAI_DECISION_LIVE_ARTIFACTS"); dir != "" {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				p.client.Transport = &decisionCaptureTransport{t: t, path: filepath.Join(dir, tc.name+".request.json")}
			}
			started := time.Now()
			req := ChatRequest{Prompt: tc.prompt}
			if tc.name == "prometheus_pt" {
				req.Messages = []ChatMessage{
					{Role: "user", Content: "Vamos olhar as métricas do dashboard."},
					{Role: "assistant", Content: "Qual métrica deseja consultar?"},
				}
				req.Context = json.RawMessage(`{"dashboard":{"title":"SRE Metrics","uid":"synthetic"}}`)
			}
			ranking, err := p.RankTools(t.Context(), buildDecisionState(req), pool)
			if err != nil {
				t.Fatalf("real decision failed after %s: %v", time.Since(started), err)
			}
			t.Logf("choice=%s confidence=%.4f elapsed=%s candidates=%d", ranking.Choice, ranking.Confidence, time.Since(started), len(ranking.Probabilities))
			// The full catalog contains overlapping specialist tools. Validate
			// the real readout contract there, rather than treating one preferred
			// label as the only valid answer. The small, unambiguous catalog also
			// checks the expected tool class and abstention.
			if !fullCatalog && ranking.Choice != tc.want {
				t.Errorf("expected %s, model chose %s", tc.want, ranking.Choice)
			}
			if dir := os.Getenv("AGENTAI_DECISION_LIVE_ARTIFACTS"); dir != "" {
				raw, err := json.MarshalIndent(ranking, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, tc.name+".answer.json"), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

type decisionCaptureTransport struct {
	t    *testing.T
	path string
}

func (c *decisionCaptureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil {
		return nil, err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		return nil, err
	}
	// Only synthetic test questions are recorded. Never record HTTP headers.
	if err := os.WriteFile(c.path, pretty.Bytes(), 0o600); err != nil {
		c.t.Error(err)
		return nil, err
	}
	return http.DefaultTransport.RoundTrip(req)
}
