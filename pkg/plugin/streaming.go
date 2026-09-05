package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	openai "github.com/sashabaranov/go-openai"
)

// maxToolRoundsPerLeg bounds how many tool-calling rounds run before a
// checkpoint fires (see toolBudgetCheckpointNudge): a brief status update to
// the user and a nudge to the model to wrap up efficiently, then
// auto-continuing on its own -- never requiring the user to notice a cutoff
// and manually ask "continue". A single user question rarely needs anywhere
// near this many round-trips; it exists as a generous per-checkpoint budget,
// not a target.
const maxToolRoundsPerLeg = 25

// maxToolLegs bounds how many of those checkpoints can pass before this
// plugin gives up and forces a final answer regardless -- maxToolRounds
// (maxToolRoundsPerLeg * maxToolLegs = 100) is a hard backstop against a
// genuinely runaway/confused model looping tool calls forever (e.g. a
// hypothetical "1000 tools" scenario), while still letting a real,
// unusually deep investigation auto-continue across a few checkpoints
// instead of being cut off after the first 25.
const maxToolLegs = 4

const maxToolRounds = maxToolRoundsPerLeg * maxToolLegs

// toolBudgetCheckpointNudge reports whether roundsUsed (1-indexed count of
// tool-calling rounds completed so far this turn) lands exactly on a
// per-leg checkpoint boundary that isn't also the final hard limit, and if
// so, the system message to nudge the model with. Only ever fires between
// legs -- never on the last one, where the caller's own "give your best
// answer now" message takes over instead.
func toolBudgetCheckpointNudge(roundsUsed int) (nudge bool, message string) {
	if roundsUsed == 0 || roundsUsed%maxToolRoundsPerLeg != 0 || roundsUsed >= maxToolRounds {
		return false, ""
	}
	return true, "You've used a large number of tool calls investigating this. You have more available -- continue efficiently: if you already have enough information to answer, do so now instead of calling more tools."
}

// streamChatCompletion sends a streaming chat completion request with tool-calling
// support and relays chunks via the sender. onDone, if non-nil, is called
// exactly once with the final assistant content the moment it's known
// (before either successful return) -- used for audit logging in the
// caller, which is the one place that also has the request's start time.
func (a *App) streamChatCompletion(ctx context.Context, req ChatRequest, sender backend.CallResourceResponseSender, onDone func(content string)) error {
	agent := resolveAgent(req.Agent)
	agent = restrictAgentForRole(agent, requesterRole(ctx), a.settings.RestrictSpecialistAgentsForViewers)
	maxContextTokens := maxContextTokensForAgent(agent, a.settings.MaxContextTokens, a.settings.AgentContextTokens)
	maxTokens := maxResponseTokensForMode(req.Mode, a.settings.MaxTokens, agent == "generic" && a.settings.LightModeForDefaultAgent)
	var grafanaVersion string
	brainAgentState := brainAgentStateUnknown
	var brainAgentVersion string
	if a.toolExecutor != nil {
		grafanaVersion = a.toolExecutor.grafanaVersion(ctx)
		if a.settings.EnableBrainAgentTools != nil && *a.settings.EnableBrainAgentTools {
			brainAgentState, brainAgentVersion = a.toolExecutor.brainAgentInstallState(ctx)
		} else {
			// See llm.go's identical branch for the live-found bug this
			// fixes: leaving state at brainAgentStateUnknown let a
			// fabricated "I've saved that" claim through uncorrected when
			// this integration is off.
			brainAgentState = brainAgentIntegrationOff
		}
	}
	systemPrompt := buildSystemPrompt(req.Mode, agent, req.Context, a.settings.FastMode, a.settings.LightModeForDefaultAgent, a.settings.AgentContexts, a.settings.AgentLabels, resolveAgentActiveCount(a.settings.AgentActiveCount), a.settings.CustomGuardrails, a.settings.ResponseLanguage, a.settings.DisableGuardrailsForDebug, requesterRole(ctx), grafanaVersion, brainAgentState, brainAgentVersion)
	systemPrompt += "\n\n" + internetToolsPromptAddition(a.internetToolState(ctx))
	systemPrompt += a.prefetchMemoryContext(ctx, req.Context)

	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
	}

	// Append prior conversation history for multi-turn context.
	for _, m := range req.Messages {
		if m.Role == "user" || m.Role == "assistant" {
			messages = append(messages, openai.ChatCompletionMessage{
				Role:    m.Role,
				Content: m.Content,
			})
		}
	}

	// Append the current user prompt, folding in any file attachments.
	messages = append(messages, buildUserMessage(req.Prompt, req.Attachments))

	if len(a.providers) == 0 {
		return fmt.Errorf("no LLM provider configured")
	}
	provider := a.providers[0]

	// Some smaller/free models (observed with openai/gpt-oss-20b:free via
	// OpenRouter, and live with qwen2.5:14b) occasionally finish with an
	// empty message right after a tool round -- the tool calls work, but the
	// model just doesn't produce a summary. Bounded nudges asking explicitly
	// for an answer recover most of these; if it's still empty after both,
	// give up rather than loop forever against a model that just won't
	// answer. Live validation found a single nudge wasn't always enough.
	emptyContentRetries := 0
	const maxEmptyContentRetries = 2

	// toolWasCalled tracks whether ANY round actually executed a tool this
	// turn -- see llm.go's fabricatedMemorySuccessPhrases doc comment.
	var toolWasCalled bool

	// avoidanceRetried bounds the tool-call-avoidance recovery below (see
	// its call site) to one retry per turn -- same bound as llm.go's
	// identical variable and worker_dispatch.go's workerAskedInsteadOfActing.
	var avoidanceRetried bool

	for round := range maxToolRounds {
		messages = compactIfNeeded(ctx, provider.client, provider.model, messages, maxContextTokens, func(status string) {
			_ = sendStreamChunk(sender, ChatResponse{Status: status})
		})

		buildReq := func(p llmProvider) openai.ChatCompletionRequest {
			var tools []openai.Tool
			if true {
				tools = a.allTools(ctx, agent)
			}
			r := openai.ChatCompletionRequest{
				Model:               p.model,
				Messages:            messages,
				MaxCompletionTokens: maxTokens,
				Tools:               tools,
			}
			if temp, ok := resolvedTemperature(a.settings.AgentTemperatures, agent); ok {
				r.Temperature = temp
			}
			return r
		}
		onRetry := func(wait time.Duration) {
			_ = sendStreamChunk(sender, ChatResponse{Status: fmt.Sprintf("Rate limited, retrying in %ds...", int(wait.Seconds()))})
		}

		var resp openai.ChatCompletionResponse
		var err error
		if round == 0 {
			// The one point where a failure is still invisible to the user --
			// try every configured provider in order before giving up, and
			// lock in whichever one answers for the rest of this request.
			resp, provider, err = a.firstProviderResponse(ctx, buildReq, onRetry, func() {
				_ = sendStreamChunk(sender, ChatResponse{Status: "Trying another configured provider..."})
			})
		} else {
			resp, err = createChatCompletionWithRetry(ctx, provider.client, buildReq(provider), rateLimitMaxRetries(a.settings), onRetry)
		}
		if err != nil {
			return fmt.Errorf("chat completion (round %d): %w", round, err)
		}

		if len(resp.Choices) == 0 {
			return fmt.Errorf("no choices in response (round %d)", round)
		}

		choice := resp.Choices[0]

		notifyToolCall := func(name, args string) error {
			return sendStreamChunk(sender, ChatResponse{
				Content:  "",
				ToolCall: newToolCallInfo(name, args),
			})
		}
		notifyToolResult := func(name string, apiCalls []string) error {
			return sendStreamChunk(sender, ChatResponse{
				ToolResult: &ToolResultInfo{Name: name, APICalls: apiCalls},
			})
		}
		notifyWorkerEvent := func(event WorkerEventInfo) error {
			return sendStreamChunk(sender, ChatResponse{WorkerEvent: &event})
		}

		// If the model wants to call tools, execute them and loop
		if choice.FinishReason == openai.FinishReasonToolCalls && len(choice.Message.ToolCalls) > 0 {
			toolWasCalled = true
			// Add assistant's tool_calls message to history -- sanitized
			// separately from what's passed to executeToolCalls below (see
			// sanitizeToolCallArguments) so the tool executor's own error
			// message can still see exactly what the model actually sent.
			historyMsg := choice.Message
			historyMsg.ToolCalls = sanitizeToolCallArguments(choice.Message.ToolCalls)
			messages = append(messages, historyMsg)

			toolMessages, err := a.executeToolCalls(ctx, choice.Message.ToolCalls, provider, notifyToolCall, notifyToolResult, notifyWorkerEvent)
			if err != nil {
				return err
			}
			messages = append(messages, toolMessages...)

			if nudge, msg := toolBudgetCheckpointNudge(round + 1); nudge {
				a.logger.Warn("tool round checkpoint reached, auto-continuing", "roundsUsed", round+1, "maxRounds", maxToolRounds)
				if err := sendStreamChunk(sender, ChatResponse{Status: fmt.Sprintf("Still investigating (checked %d tool calls so far)...", round+1)}); err != nil {
					return err
				}
				messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: msg})
			}

			continue // Next round with tool results
		}

		// Some Llama models occasionally emit their function-call syntax as
		// plain content instead of populating tool_calls -- treat that the
		// same as a real tool call rather than showing the raw tag.
		if cleanText, pseudoCalls := extractPseudoToolCalls(choice.Message.Content); len(pseudoCalls) > 0 {
			toolWasCalled = true
			assistantMsg := choice.Message
			assistantMsg.Content = cleanText
			assistantMsg.ToolCalls = pseudoCalls
			messages = append(messages, assistantMsg)

			// cleanText (whatever's left after stripping the recognized
			// pseudo-tool-call syntax) is kept in message history above (the
			// model needs to see its own prior turn) but is NEVER shown to
			// the user -- matches the REAL tool_calls branch above, which
			// never streams choice.Message.Content either, and for the same
			// reason: this is pre-tool-result narration written before the
			// model has any real data, not an answer. A previous version of
			// this guard only checked looksLikeLanguageMismatch, which
			// missed a real live incident (2026-08-08): asked to dispatch
			// two workers, cleanText was a coherent, correctly-languaged
			// multi-step "here's the plan" narrative (illustrative/fabricated
			// example tool-call JSON included) that read as if real
			// investigation were already happening -- language-safe, but
			// still pure narration adding no value the user needed, and the
			// same shape as the one case a bare language check can never
			// catch (a model that narrates competently in the right
			// language). Suppressing it here means the frontend's existing
			// "thinking" indicator stays visible through the whole round
			// instead of showing this narration as if it were progress.

			toolMessages, err := a.executeToolCalls(ctx, pseudoCalls, provider, notifyToolCall, notifyToolResult, notifyWorkerEvent)
			if err != nil {
				return err
			}
			messages = append(messages, toolMessages...)

			if nudge, msg := toolBudgetCheckpointNudge(round + 1); nudge {
				a.logger.Warn("tool round checkpoint reached, auto-continuing", "roundsUsed", round+1, "maxRounds", maxToolRounds)
				if err := sendStreamChunk(sender, ChatResponse{Status: fmt.Sprintf("Still investigating (checked %d tool calls so far)...", round+1)}); err != nil {
					return err
				}
				messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: msg})
			}

			continue
		}

		content := choice.Message.Content

		if content == "" && emptyContentRetries < maxEmptyContentRetries {
			emptyContentRetries++
			a.logger.Warn("model returned empty final content, nudging for a real answer", "round", round)
			// See llm.go's identical fix: an empty Content (no MultiContent)
			// makes go-openai omit "content" from the JSON entirely, which
			// some OpenAI-compatible backends (Ollama observed live) reject
			// outright as "invalid message content type: <nil>".
			emptyAssistantMsg := choice.Message
			emptyAssistantMsg.Content = "(empty response)"
			messages = append(messages, emptyAssistantMsg)
			messages = append(messages, openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleSystem,
				Content: "Your previous response was empty. Provide a complete, direct answer now based on everything you've found so far. Do not call any more tools.",
			})
			continue
		}

		// Checked BEFORE the language-mismatch guardrail below (which also
		// matches this same content via its own looksLikeToolCallAvoidance
		// check) because that path's recovery rebuilds the request WITHOUT
		// tools -- fine for an actual language mismatch, useless here, where
		// the fix requires calling a tool. This retry instead stays in the
		// normal round loop (tools still available) and mirrors
		// workerAskedInsteadOfActing's identical recovery in
		// worker_dispatch.go: append what the model said, tell it plainly
		// there's no one to answer, and let it retry with a real tool call
		// on the next round. Live incident (2026-08-11): a tool error had
		// already told the model exactly what to do (a real example, the
		// actual valid datasource UIDs), and it asked the user for that same
		// information anyway instead of retrying -- reproduced repeatedly
		// against THIS function specifically (the one real chat traffic
		// actually goes through), not just chatCompletion.
		if !avoidanceRetried && looksLikeToolCallAvoidance(content) {
			avoidanceRetried = true
			messages = append(messages, choice.Message)
			messages = append(messages, openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleSystem,
				Content: "There is no one to answer that question right now. If a tool call errored, its error message already told you what to do -- a real example, the actual valid options, or which tool to call first. Use that information and call a tool again now. Never ask the user for something a tool result already gave you.",
			})
			continue
		}

		// Same language-mismatch guardrail as the non-streaming chatCompletion
		// (llm.go) -- this whole function is only "streaming" in its HTTP
		// transport to the frontend; the LLM call for a normal round (above)
		// is a plain non-streaming createChatCompletionWithRetry, so the full
		// content is already in hand here, before anything has been sent to
		// the user, exactly like the non-streaming path. Real UI traffic goes
		// through THIS function, not chatCompletion -- a fix applied only to
		// the latter (as this repo's language_switch_test.go originally was)
		// never actually reaches a real user, only curl/API testing against
		// the non-streaming /chat resource route.
		if looksLikeLanguageMismatch(req.Prompt, a.settings.ResponseLanguage, content) {
			a.logger.Debug("flagged content preview (original attempt)", "preview", previewForLog(content))
			buildReqNoTools := func(p llmProvider) openai.ChatCompletionRequest {
				return openai.ChatCompletionRequest{Model: p.model, Messages: messages, MaxCompletionTokens: maxTokens}
			}
			if fixed, _, ok := retryForLanguageSwitch(ctx, a, provider, buildReqNoTools, rateLimitMaxRetries(a.settings), req.Prompt); ok {
				content = fixed
			} else {
				content = languageMismatchFallbackMessage(a.settings.ResponseLanguage)
			}
			// The reasoning trace that went with the ORIGINAL (replaced)
			// content is no longer trustworthy either -- see
			// sanitizeReasoning's doc comment.
			choice.Message.ReasoningContent = ""
		} else {
			choice.Message.ReasoningContent = sanitizeReasoning(req.Prompt, a.settings.ResponseLanguage, choice.Message.ReasoningContent)
		}
		if !toolWasCalled && brainAgentDefinitelyUnavailable(brainAgentState) && looksLikeFabricatedMemorySuccess(content) {
			a.logger.Warn("model fabricated a memory-save success claim with no memory tool available, correcting", "brainAgentState", brainAgentState)
			content = brainAgentUnavailableMessage(brainAgentState)
			choice.Message.ReasoningContent = ""
		}
		if !toolWasCalled && looksLikeFabricatedSearchCitation(content, systemPrompt, hadPreSuppliedContext(req.Mode, req.Context)) {
			a.logger.Warn("model fabricated a search citation with no search_web call this turn, correcting")
			content = fabricatedSearchCitationMessage
			choice.Message.ReasoningContent = ""
		}

		// Model returned content — send it directly instead of re-requesting,
		// since a second streaming request may not reproduce the same answer.
		// The frontend gets the reasoning trace reconstructed as a
		// "<think>...</think>" prefix (see withThinkingPrefix) so
		// ThinkingBlock can render it; message history/audit log below stay
		// on the plain content, which is what the model should see of its
		// own prior turn and what actually matters for an audit trail.
		if displayContent := withThinkingPrefix(content, choice.Message.ReasoningContent); displayContent != "" {
			if safeContent := sanitizeAssistantChatOutput(displayContent); safeContent != "" {
				if err := sendStreamChunk(sender, ChatResponse{Content: safeContent}); err != nil {
					return err
				}
			}
		}
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleAssistant,
			Content: content,
		})
		if onDone != nil {
			onDone(content)
		}
		tokens := estimateMessagesTokens(messages)
		return sendStreamChunk(sender, ChatResponse{
			Done:          true,
			ContextTokens: tokens,
			MaxTokens:     maxContextTokens,
		})
	}

	// The hard overall budget (maxToolRounds, across all
	// maxToolLegs checkpoints) is exhausted and the model still hasn't
	// produced a final answer -- ask for the best answer now, honestly
	// disclosing that this used extended investigation and may be
	// incomplete, rather than presenting a partial answer as if it were
	// complete.
	a.logger.Warn("Tool-calling budget exhausted across all checkpoints, requesting final summary", "maxRounds", maxToolRounds)
	messages = append(messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleSystem,
		Content: "You have reached the maximum number of tool calls for this investigation, even after extended continuation. Give your best answer now based on everything you've found so far -- clearly and honestly state what you were able to verify and what you were NOT able to fully check, rather than presenting a partial answer as complete. Do not attempt any more tool calls.",
	})
	return a.streamFinalResponse(ctx, messages, sender, maxContextTokens, maxTokens, agent, provider, onDone)
}

// streamFinalResponse re-issues the request as a stream to get the final content response.
// It includes token usage estimates in the final done chunk. Always uses
// provider.streamClient (a longer HTTP timeout than the per-round tool-check
// client) since this is the one call that can legitimately take a while to
// fully arrive. provider is whichever one already answered this request's
// first call (see firstProviderResponse) -- never re-resolved here. Never
// passes tools: this is the final-answer path after the tool-round limit is
// hit, where tool-calling is deliberately disabled.
func (a *App) streamFinalResponse(ctx context.Context, messages []openai.ChatCompletionMessage, sender backend.CallResourceResponseSender, maxContextTokens int, maxTokens int, agent string, provider llmProvider, onDone func(content string)) error {
	streamReq := openai.ChatCompletionRequest{
		Model:               provider.model,
		Messages:            messages,
		MaxCompletionTokens: maxTokens,
		Stream:              true,
	}
	if temp, ok := resolvedTemperature(a.settings.AgentTemperatures, agent); ok {
		streamReq.Temperature = temp
	}
	stream, err := provider.streamClient.CreateChatCompletionStream(ctx, streamReq)
	if err != nil {
		return fmt.Errorf("create stream: %w", err)
	}
	defer func() { _ = stream.Close() }()

	var completionContent strings.Builder
	// Reconstructs "<think>...</think>" around a "thinking" model's
	// reasoning delta (see reasoningKeyRewriteTransport/withThinkingPrefix)
	// as it streams in, interleaved with the real answer -- opened the
	// moment reasoning first arrives, closed the moment real content first
	// arrives after it. completionContent (history/audit) intentionally
	// only ever collects the real content, never the reasoning.
	thinkOpened := false
	thinkClosed := false

	// outputBudget bounds this loop's real per-delta chunks (see
	// chatOutputBudget) -- this is the one place in this file that streams
	// genuinely incremental pieces of assistant content repeatedly, so it's
	// the one place a running byte/rune budget across chunks (as opposed to
	// a single sanitizeAssistantChatOutput pass on one already-complete
	// string) actually applies.
	outputBudget := newChatOutputBudget()

	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			// Include the streamed completion in the token estimate.
			messages = append(messages, openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleAssistant,
				Content: completionContent.String(),
			})
			if onDone != nil {
				onDone(completionContent.String())
			}
			tokens := estimateMessagesTokens(messages)
			return sendStreamChunk(sender, ChatResponse{
				Content:       "",
				Done:          true,
				ContextTokens: tokens,
				MaxTokens:     maxContextTokens,
			})
		}
		if err != nil {
			a.logger.Error("Stream recv error, sending done", "error", err)
			messages = append(messages, openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleAssistant,
				Content: completionContent.String(),
			})
			tokens := estimateMessagesTokens(messages)
			_ = sendStreamChunk(sender, ChatResponse{
				Content:       "",
				Done:          true,
				ContextTokens: tokens,
				MaxTokens:     maxContextTokens,
			})
			return fmt.Errorf("stream recv: %w", err)
		}

		if len(response.Choices) > 0 {
			delta := response.Choices[0].Delta.Content
			reasoningDelta := response.Choices[0].Delta.ReasoningContent
			completionContent.WriteString(delta)

			var out strings.Builder
			if reasoningDelta != "" {
				if !thinkOpened {
					out.WriteString("<think>")
					thinkOpened = true
				}
				out.WriteString(reasoningDelta)
			}
			if delta != "" {
				if thinkOpened && !thinkClosed {
					out.WriteString("</think>")
					thinkClosed = true
				}
				out.WriteString(delta)
			}
			if out.Len() == 0 {
				continue
			}
			safeChunk := outputBudget.Apply(out.String())
			if safeChunk == "" {
				continue
			}
			if err := sendStreamChunk(sender, ChatResponse{Content: safeChunk}); err != nil {
				return err
			}
		}
	}
}

func sendStreamChunk(sender backend.CallResourceResponseSender, chunk ChatResponse) error {
	body, err := json.Marshal(chunk)
	if err != nil {
		return fmt.Errorf("marshal chunk: %w", err)
	}
	body = append(body, '\n')

	return sender.Send(&backend.CallResourceResponse{
		Status: http.StatusOK,
		Headers: map[string][]string{
			"Content-Type": {"application/x-ndjson"},
		},
		Body: body,
	})
}
