package plugin

import (
	"context"
	"os"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

// goldenCase is one reference question this assistant should reliably
// answer by picking the right tool first -- see TestGoldenSet_ToolSelection.
type goldenCase struct {
	name string
	// prompt is deliberately English -- agent-ai-app answers in English by
	// default (see skill.go/llm.go's language rule), so this avoids
	// conflating a provider's tool-selection quality with its handling of
	// an explicit language override.
	prompt string
	// acceptableTools lists every tool name that counts as a correct first
	// call -- some questions have more than one reasonable entry point
	// (e.g. a named-alert investigation could start with list_alerts or go
	// straight to investigate_alert).
	acceptableTools []string
}

// goldenSet is intentionally small and hand-picked to cover this plugin's
// main tool categories (metrics, logs, alerts, discovery, investigation) --
// not meant to be exhaustive, meant to catch a provider/model that can't
// reliably pick the right tool for an unambiguous question. Extend this
// list as new tool categories are added or a real regression is found
// live, the same way looksLikeMidResponseLanguageSwitch and
// extractPseudoToolCalls both exist because of a specific model behavior
// observed in production, not a hypothetical.
var goldenSet = []goldenCase{
	{
		name:            "list_firing_alerts",
		prompt:          "Are there any alerts firing right now in Grafana?",
		acceptableTools: []string{"list_alerts", "analyze_active_alerts"},
	},
	{
		name:            "query_cpu_metric",
		prompt:          "What is the current CPU usage across pods? Use Prometheus.",
		acceptableTools: []string{"query_prometheus"},
	},
	{
		name:            "topic_dashboards",
		prompt:          "What dashboards do we have related to Kafka?",
		acceptableTools: []string{"find_dashboards", "list_dashboards", "list_folders"},
	},
	{
		name:            "investigate_named_alert",
		prompt:          "Investigate the HighErrorRate alert and tell me the likely root cause.",
		acceptableTools: []string{"investigate_alert", "list_alerts", "analyze_active_alerts"},
	},
	{
		name:            "list_datasources",
		prompt:          "What datasources are configured on this Grafana instance?",
		acceptableTools: []string{"list_datasources"},
	},
	{
		name:            "loki_label_uncertainty",
		prompt:          "Show me recent error logs for the checkout service, but I'm not sure what label it's tagged with in Loki.",
		acceptableTools: []string{"list_loki_labels", "query_loki", "find_dashboards", "list_dashboards"},
	},
	{
		name:            "postmortem_request",
		prompt:          "Write a postmortem for the CheckoutLatency alert.",
		acceptableTools: []string{"investigate_alert", "list_alerts", "analyze_active_alerts"},
	},
}

// TestGoldenSet_ToolSelection checks whether a configured LLM provider
// reliably picks the right tool for each goldenSet question -- skipped by
// default (no live API calls in normal `go test`/CI runs). Point it at any
// OpenAI-compatible endpoint to compare tool-selection quality across
// providers/models, e.g. before switching the default model or adding a
// new fallback provider:
//
//	AGENT_EVAL_ENDPOINT=http://localhost:11434/v1 AGENT_EVAL_MODEL=qwen2.5:14b-instruct \
//	  go test ./pkg/plugin/ -run TestGoldenSet_ToolSelection -v
//
// This only checks which tool the model calls first, not the quality of
// its final natural-language answer -- automatically judging a free-text
// answer's correctness would need its own LLM-as-judge (a real, separate
// dependency and cost this harness deliberately doesn't take on). Tool
// selection is the objectively-checkable half of "did this work."
func TestGoldenSet_ToolSelection(t *testing.T) {
	endpoint := os.Getenv("AGENT_EVAL_ENDPOINT")
	model := os.Getenv("AGENT_EVAL_MODEL")
	if endpoint == "" || model == "" {
		t.Skip("set AGENT_EVAL_ENDPOINT and AGENT_EVAL_MODEL to run this golden set against a real provider")
	}
	apiKey := os.Getenv("AGENT_EVAL_APIKEY")
	if apiKey == "" {
		apiKey = "unused"
	}

	provider := newLLMProvider(endpoint, apiKey, model, 60)
	systemPrompt := buildSystemPrompt("chat", "generic", nil, false, false, nil, nil, 3, "", "", false, "Admin", "", brainAgentStateUnknown, "")
	tools := llmTools("generic")

	passed := 0
	for _, gc := range goldenSet {
		t.Run(gc.name, func(t *testing.T) {
			resp, err := provider.client.CreateChatCompletion(context.Background(), openai.ChatCompletionRequest{
				Model: provider.model,
				Messages: []openai.ChatCompletionMessage{
					{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
					{Role: openai.ChatMessageRoleUser, Content: gc.prompt},
				},
				Tools: tools,
			})
			if err != nil {
				t.Fatalf("provider call failed: %v", err)
			}
			if len(resp.Choices) == 0 || len(resp.Choices[0].Message.ToolCalls) == 0 {
				t.Errorf("model did not call any tool for prompt %q", gc.prompt)
				return
			}
			got := resp.Choices[0].Message.ToolCalls[0].Function.Name
			for _, want := range gc.acceptableTools {
				if got == want {
					passed++
					return
				}
			}
			t.Errorf("tool = %q for prompt %q, want one of %v", got, gc.prompt, gc.acceptableTools)
		})
	}
	t.Logf("golden set tool-selection accuracy: %d/%d", passed, len(goldenSet))
}

func TestGoldenSet_DispatchWorkerToolSelectionForSpecialist(t *testing.T) {
	endpoint := os.Getenv("AGENT_EVAL_ENDPOINT")
	model := os.Getenv("AGENT_EVAL_MODEL")
	if endpoint == "" || model == "" {
		t.Skip("set AGENT_EVAL_ENDPOINT and AGENT_EVAL_MODEL to run this dispatch_worker golden set against a real provider")
	}
	apiKey := os.Getenv("AGENT_EVAL_APIKEY")
	if apiKey == "" {
		apiKey = "unused"
	}

	provider := newLLMProvider(endpoint, apiKey, model, 60)
	contexts := map[string]string{"agent-1": "You specialize in SRE investigations that correlate service health across alerts, metrics, logs, and traces."}
	systemPrompt := buildSystemPrompt("chat", "agent-1", nil, false, false, contexts, nil, 3, "", "", false, "Admin", "", brainAgentStateUnknown, "")
	tools := llmTools("agent-1")

	cases := []goldenCase{
		{
			name:            "investigate_service_across_signals",
			prompt:          "Investigate whether the checkout service is unhealthy across alerts, logs, metrics, and traces.",
			acceptableTools: []string{"dispatch_worker"},
		},
		{
			name:            "look_into_errors_and_latency",
			prompt:          "Look into recent errors and latency for the payments service using whatever observability signals are relevant.",
			acceptableTools: []string{"dispatch_worker"},
		},
		{
			name:            "multi_source_root_cause",
			prompt:          "Find the likely root cause of demo-app being unhealthy by checking more than one signal source.",
			acceptableTools: []string{"dispatch_worker"},
		},
	}

	passed := 0
	for _, gc := range cases {
		t.Run(gc.name, func(t *testing.T) {
			resp, err := provider.client.CreateChatCompletion(context.Background(), openai.ChatCompletionRequest{
				Model: provider.model,
				Messages: []openai.ChatCompletionMessage{
					{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
					{Role: openai.ChatMessageRoleUser, Content: gc.prompt},
				},
				Tools: tools,
			})
			if err != nil {
				t.Fatalf("provider call failed: %v", err)
			}
			if len(resp.Choices) == 0 || len(resp.Choices[0].Message.ToolCalls) == 0 {
				t.Errorf("model did not call any tool for prompt %q", gc.prompt)
				return
			}
			got := resp.Choices[0].Message.ToolCalls[0].Function.Name
			if got != "dispatch_worker" {
				t.Errorf("tool = %q for prompt %q, want dispatch_worker", got, gc.prompt)
				return
			}
			passed++
		})
	}
	t.Logf("dispatch_worker specialist tool-selection accuracy: %d/%d", passed, len(cases))
}
