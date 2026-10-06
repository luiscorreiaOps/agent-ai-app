package plugin

import (
	"github.com/prometheus/client_golang/prometheus"
)

type metrics struct {
	requestsTotal    *prometheus.CounterVec
	requestDuration  *prometheus.HistogramVec
	tokensUsed       *prometheus.CounterVec
	decisionRequests *prometheus.CounterVec
	decisionDuration *prometheus.HistogramVec
}

func newMetrics(reg prometheus.Registerer) *metrics {
	m := &metrics{
		decisionRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "grafana_agentai_decision_requests_total", Help: "Optional decision routing outcomes",
		}, []string{"provider", "mode", "outcome"}),
		decisionDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "grafana_agentai_decision_duration_seconds", Help: "Time spent selecting candidate tools", Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10},
		}, []string{"provider"}),
		requestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "grafana_llm_requests_total",
			Help: "Total number of LLM requests",
		}, []string{"model", "status"}),

		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "grafana_llm_request_duration_seconds",
			Help:    "Duration of LLM requests in seconds",
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60},
		}, []string{"model"}),

		tokensUsed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "grafana_llm_tokens_used_total",
			Help: "Total tokens used in LLM requests",
		}, []string{"model", "direction"}),
	}

	reg.MustRegister(m.requestsTotal, m.requestDuration, m.tokensUsed, m.decisionRequests, m.decisionDuration)
	return m
}

func (m *metrics) recordDecision(provider, mode, outcome string, duration float64) {
	m.decisionRequests.WithLabelValues(provider, mode, outcome).Inc()
	m.decisionDuration.WithLabelValues(provider).Observe(duration)
}

func (m *metrics) recordRequest(model, status string, durationSec float64, promptTokens, completionTokens int) {
	m.requestsTotal.WithLabelValues(model, status).Inc()
	m.requestDuration.WithLabelValues(model).Observe(durationSec)

	if promptTokens > 0 {
		m.tokensUsed.WithLabelValues(model, "prompt").Add(float64(promptTokens))
	}
	if completionTokens > 0 {
		m.tokensUsed.WithLabelValues(model, "completion").Add(float64(completionTokens))
	}
}
