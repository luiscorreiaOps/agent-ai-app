package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

// newTestAppWithGrafana builds an App whose Grafana URL/token point at a
// local httptest server, so llmAppStatus's health check has somewhere real
// to call -- newTestApp (used elsewhere) deliberately has no reachable
// Grafana URL/token, which is fine for tests that don't touch integrations.
func newTestAppWithGrafana(t *testing.T, grafanaURL, grafanaToken string) *App {
	t.Helper()

	jsonData, err := json.Marshal(map[string]any{
		"endpointURL": "http://localhost:1/v1",
		"model":       "test-model",
		"grafanaURL":  grafanaURL,
	})
	if err != nil {
		t.Fatalf("marshal jsonData: %v", err)
	}
	inst, err := NewApp(context.Background(), backend.AppInstanceSettings{
		JSONData: jsonData,
		DecryptedSecureJSONData: map[string]string{
			"apiKey":       "key",
			"grafanaToken": grafanaToken,
		},
	})
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	return inst.(*App)
}

// onlyLLMAppInstalledHandler simulates a Grafana instance where only
// grafana-llm-app is installed -- 404 for every other integration's
// /api/plugins/:id/settings check, matching what a real instance without
// OnCall/Incident/SLO installed would return.
func onlyLLMAppInstalledHandler(llmAppBody string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == llmAppHealthPath {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(llmAppBody))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestIntegrationsStatus_ReportsOKWhenLLMAppHealthy(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(onlyLLMAppInstalledHandler(`{"details":{"llmProvider":{"ok":true}}}`))
	defer server.Close()

	app := newTestAppWithGrafana(t, server.URL, "grafana-token")
	statuses := app.integrationsStatus(context.Background())
	// Only the installed one is returned -- OnCall/Incident/SLO are absent
	// on this simulated instance and are filtered out entirely, not shown
	// as a red/not-installed entry.
	if len(statuses) != 1 {
		t.Fatalf("len(statuses) = %d, want 1 (absent integrations filtered out)", len(statuses))
	}
	if statuses[0].ID != "grafana-llm-app" {
		t.Errorf("ID = %q, want %q", statuses[0].ID, "grafana-llm-app")
	}
	if statuses[0].Status != IntegrationStatusOK {
		t.Errorf("Status = %q, want %q", statuses[0].Status, IntegrationStatusOK)
	}
	if !statuses[0].Enabled {
		t.Error("Enabled = false, want true (default)")
	}
}

func TestIntegrationsStatus_FiltersOutEverythingWhenNothingInstalled(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	app := newTestAppWithGrafana(t, server.URL, "grafana-token")
	statuses := app.integrationsStatus(context.Background())
	if len(statuses) != 0 {
		t.Errorf("statuses = %+v, want an empty list (nothing installed, so nothing to show)", statuses)
	}
}

func TestIntegrationsStatus_EnabledReflectsToggle(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"details":{"llmProvider":{"ok":true}}}`))
	}))
	defer server.Close()

	jsonData, err := json.Marshal(map[string]any{
		"endpointURL":             "http://localhost:1/v1",
		"model":                   "test-model",
		"grafanaURL":              server.URL,
		"enableLLMAppIntegration": false,
	})
	if err != nil {
		t.Fatalf("marshal jsonData: %v", err)
	}
	inst, err := NewApp(context.Background(), backend.AppInstanceSettings{
		JSONData:                jsonData,
		DecryptedSecureJSONData: map[string]string{"apiKey": "key", "grafanaToken": "grafana-token"},
	})
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	app := inst.(*App)

	statuses := app.integrationsStatus(context.Background())
	if statuses[0].Enabled {
		t.Error("Enabled = true, want false (explicitly disabled)")
	}
	// Status still reflects real detection even when disabled -- the admin
	// can see "this would work" independent of their opt-out choice.
	if statuses[0].Status != IntegrationStatusOK {
		t.Errorf("Status = %q, want %q (disabling the toggle shouldn't change what's detected)", statuses[0].Status, IntegrationStatusOK)
	}
}

func TestHandleIntegrations_ReturnsStatusList(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(onlyLLMAppInstalledHandler(`{"details":{"llmProvider":{"ok":true}}}`))
	defer server.Close()

	app := newTestAppWithGrafana(t, server.URL, "grafana-token")

	req := &backend.CallResourceRequest{Path: "integrations", Method: http.MethodGet}
	var body []byte
	sender := backend.CallResourceResponseSenderFunc(func(res *backend.CallResourceResponse) error {
		body = res.Body
		return nil
	})
	if err := app.CallResource(context.Background(), req, sender); err != nil {
		t.Fatalf("CallResource returned error: %v", err)
	}

	var resp []IntegrationStatus
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp) != 1 || resp[0].ID != "grafana-llm-app" {
		t.Errorf("resp = %+v, want a single grafana-llm-app entry (the only one installed)", resp)
	}
}
