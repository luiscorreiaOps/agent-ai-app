package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/resource/httpadapter"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ChatMessage represents a single message in a conversation.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest represents an incoming chat analysis request.
type ChatRequest struct {
	Mode        string           `json:"mode"`
	Agent       string           `json:"agent,omitempty"`
	Prompt      string           `json:"prompt"`
	Context     json.RawMessage  `json:"context"`
	Messages    []ChatMessage    `json:"messages,omitempty"`
	Attachments []ChatAttachment `json:"attachments,omitempty"`
}

// ChatAttachment is a single file the user attached to their message --
// either inlined as text (logs, configs, query results) or as a base64
// image for vision-capable models. Text-only models can reject image
// requests outright, so those provider errors are translated before they
// reach the chat UI.
type ChatAttachment struct {
	Name    string `json:"name"`
	Content string `json:"content"` // raw text, or base64 for images
	// Type is "text" or "image".
	Type     string `json:"type"`
	MimeType string `json:"mimeType,omitempty"`
}

// ChatResponse represents the chat completion response.
type ChatResponse struct {
	Content       string          `json:"content"`
	Done          bool            `json:"done"`
	ToolCall      *ToolCallInfo   `json:"toolCall,omitempty"`
	ToolResult    *ToolResultInfo `json:"toolResult,omitempty"`
	ContextTokens int             `json:"contextTokens,omitempty"`
	MaxTokens     int             `json:"maxTokens,omitempty"`
	// Status is a short-lived activity label for the UI (e.g. "Compactando
	// contexto...") -- distinct from the generic "thinking" dots, shown only
	// while a specific background step is happening, cleared on the next
	// content/tool chunk.
	Status string `json:"status,omitempty"`
	// WorkerEvent is a live status update for one dispatched worker subagent
	// (see worker_dispatch.go) -- distinct from Status (a single global
	// label) since several workers can be running concurrently, each with
	// its own chip in the frontend.
	WorkerEvent *WorkerEventInfo `json:"workerEvent,omitempty"`
}

// ToolCallInfo describes a tool invocation sent to the frontend for display.
// Kind/Label/StatusLabel/DoneLabel/External are optional, backward-compatible
// additions (see newToolCallInfo) that let an internet-backed tool
// (search_web) render as a distinct "Internet search" indicator instead of
// the generic "Using tools..." block -- driven entirely by the real tool
// name on the backend, never by LLM-generated text.
type ToolCallInfo struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	// Kind is "grafana_tool" (default) or "internet_search".
	Kind string `json:"kind,omitempty"`
	// Label is a UI-safe display name for this tool call.
	Label string `json:"label,omitempty"`
	// StatusLabel is shown while the call is still pending/in flight.
	StatusLabel string `json:"statusLabel,omitempty"`
	// DoneLabel is shown once the call has completed.
	DoneLabel string `json:"doneLabel,omitempty"`
	// External is true only for internet-backed tools -- lets the frontend
	// show a discreet "Public internet" badge.
	External bool `json:"external,omitempty"`
}

// newToolCallInfo builds the ToolCallInfo sent to the frontend the moment a
// tool call starts executing. search_web gets its own kind/labels and, most
// importantly, has its Arguments replaced with "{}" -- the raw query hasn't
// been through this tool's own backend sanitization yet at notify time (see
// executeToolCalls/OnlineSearchClient.Search), so the frontend must never
// render it as-is.
func newToolCallInfo(name string, args string) *ToolCallInfo {
	info := &ToolCallInfo{
		Name:        name,
		Arguments:   args,
		Kind:        "grafana_tool",
		Label:       name,
		StatusLabel: "Using Grafana tools...",
		DoneLabel:   "Used Grafana tool",
	}
	if name == onlineSearchToolName {
		info.Arguments = "{}"
		info.Kind = "internet_search"
		info.Label = "Internet search"
		info.StatusLabel = "Searching the web..."
		info.DoneLabel = "Searched the web"
		info.External = true
	}
	return info
}

// ToolResultInfo is sent once a tool call has finished, carrying what the
// tool actually did. Separate from ToolCallInfo because that one is emitted
// before execution starts -- the API calls aren't known yet at that point.
type ToolResultInfo struct {
	Name string `json:"name"`
	// APICalls lists the Grafana API requests this tool issued, e.g.
	// "GET /api/datasources". Empty for tools that call no Grafana API.
	APICalls []string `json:"apiCalls,omitempty"`
}

// Usage holds token usage information.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

var validModes = map[string]bool{
	"chat":            true,
	"explain_panel":   true,
	"analyze_logs":    true,
	"analyze_metrics": true,
}

func (a *App) registerRoutes() {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", a.handleHealth)
	mux.HandleFunc("GET /agents", a.handleAgents)
	mux.HandleFunc("GET /limits", a.handleLimits)
	mux.HandleFunc("GET /integrations", a.handleIntegrations)
	mux.HandleFunc("POST /chat", a.handleChat)
	// Same access level as every other resource route (Grafana's own
	// plugins.app:access permission on this plugin id) -- no separate
	// per-route role check exists anywhere in this backend today, so this
	// isn't a new exception. Requests_total/duration/tokens_used are
	// already tracked (see metrics.go); this just gives them a route.
	mux.Handle("GET /metrics", promhttp.HandlerFor(a.metricsRegistry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/", a.handleNotFound)

	a.httpHandler = httpadapter.New(mux)
}

// CallResource routes requests, handling streaming endpoints directly.
func (a *App) CallResource(ctx context.Context, req *backend.CallResourceRequest, sender backend.CallResourceResponseSender) error {
	if req.Path == "chat/stream" && req.Method == http.MethodPost {
		return a.handleStreamResource(ctx, req, sender)
	}
	return a.httpHandler.CallResource(ctx, req, sender)
}

func (a *App) handleStreamResource(ctx context.Context, req *backend.CallResourceRequest, sender backend.CallResourceResponseSender) error {
	user := requestUser(ctx)
	if !a.getLimiter(user).Allow() {
		return sendErrorResponse(sender, http.StatusTooManyRequests, "rate limit exceeded")
	}

	// Every cheap validation happens BEFORE ever attempting/queueing for a
	// chat slot, not after -- see handleChat's identical reordering for why
	// (tryAcquireChatSlotQueued can now legitimately block for up to
	// ChatQueueWaitSeconds, and chatQueueDepth is a shared, limited
	// resource -- a malformed/oversized body must never be able to tie up
	// that capacity before being rejected). handleChat's equivalent
	// http.HandlerFunc path limits its body via http.MaxBytesReader before
	// ever decoding it -- this route can't do the same
	// (CallResourceRequest.Body already arrives as a fully-materialized
	// []byte over the plugin protocol, not a streamed io.Reader this
	// handler gets to wrap), but nothing stops checking the length upfront.
	if len(req.Body) > maxChatBodyBytes {
		return sendErrorResponse(sender, http.StatusRequestEntityTooLarge, "request body too large")
	}

	var chatReq ChatRequest
	if err := json.Unmarshal(req.Body, &chatReq); err != nil {
		return sendErrorResponse(sender, http.StatusBadRequest, "invalid request body: "+err.Error())
	}

	chatReq.Prompt = sanitizePrompt(chatReq.Prompt)

	if chatReq.Prompt == "" {
		return sendErrorResponse(sender, http.StatusBadRequest, "prompt is required")
	}

	if !validModes[chatReq.Mode] {
		return sendErrorResponse(sender, http.StatusBadRequest, "invalid mode: "+chatReq.Mode)
	}

	if err := sanitizeContextSize(chatReq.Context, maxContextBytes); err != nil {
		return sendErrorResponse(sender, http.StatusBadRequest, err.Error())
	}

	if err := validateMessages(chatReq.Messages); err != nil {
		return sendErrorResponse(sender, http.StatusBadRequest, err.Error())
	}

	if err := validateAttachments(chatReq.Attachments, attachmentMaxBytes(a.settings)); err != nil {
		return sendErrorResponse(sender, http.StatusBadRequest, err.Error())
	}

	release, ok := a.tryAcquireChatSlotQueued(ctx)
	if !ok {
		return sendErrorResponse(sender, http.StatusTooManyRequests, "too many requests — global capacity reached (waited, but no slot freed up in time)")
	}
	defer release()

	start := time.Now()
	var finalContent string
	err := a.streamChatCompletion(ctx, chatReq, sender, func(content string) { finalContent = content })
	loggedAgent := restrictAgentForRole(resolveAgent(chatReq.Agent), requesterRole(ctx), a.settings.RestrictSpecialistAgentsForViewers)
	a.auditLogChat(user, requesterRole(ctx), chatReq.Mode, loggedAgent, chatReq.Prompt, finalContent, err, time.Since(start).Seconds())
	if err != nil && hasImageAttachment(chatReq.Attachments) {
		if isMultimodalUnsupportedError(err) {
			return sendErrorResponse(sender, http.StatusBadRequest, imageAttachmentsUnsupportedMessage)
		}
		return sendErrorResponse(sender, http.StatusBadGateway, imageAttachmentsFailedMessage)
	}
	return err
}

const imageAttachmentsUnsupportedMessage = "The current model does not support image attachments. Use a vision-capable model or remove the image and send the message again."
const imageAttachmentsFailedMessage = "The current model/provider could not process this image attachment."

func hasImageAttachment(attachments []ChatAttachment) bool {
	for _, a := range attachments {
		if a.Type == "image" {
			return true
		}
	}
	return false
}

func isMultimodalUnsupportedError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "multimodal") &&
		(strings.Contains(message, "does not support") || strings.Contains(message, "not support"))
}

func sendErrorResponse(sender backend.CallResourceResponseSender, status int, message string) error {
	body, _ := json.Marshal(map[string]string{"error": message})
	return sender.Send(&backend.CallResourceResponse{
		Status: status,
		Headers: map[string][]string{
			"Content-Type": {"application/json"},
		},
		Body: body,
	})
}

// handleHealth backs both the chat UI's background health poll (any Viewer,
// see usePluginSettings.ts) and Configuration's "Test connection" button
// (Admin only, per plugin.json). A failure's raw message can include the
// LLM endpoint's URL/port/model (see humanizeConnectErr) -- useful for an
// Admin debugging their own config, unnecessary internal topology for
// anyone else looking at the chat page. Gate the detail on role, same
// trustworthy source (requesterRole) used for every other permission check
// in this backend, not on which page happened to call this route.
func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	result, err := a.cachedCheckHealth(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}

	status := http.StatusOK
	statusStr := "ok"
	message := result.Message
	model := a.settings.Model

	if result.Status != backend.HealthStatusOk {
		status = http.StatusBadGateway
		statusStr = "error"
		if requesterRole(r.Context()) != "Admin" {
			message = "The assistant is temporarily unavailable. An admin needs to check the LLM configuration."
			model = ""
		}
	}

	writeJSON(w, status, map[string]string{
		"status":  statusStr,
		"message": message,
		"model":   model,
	})
}

// handleLimits exposes admin-configured limits/feature toggles the frontend
// needs before rendering (e.g. attachment size, which surfaces are enabled)
// -- Viewer-accessible, like the chat page itself, since it carries no
// sensitive data, just numbers and booleans.
func (a *App) handleLimits(w http.ResponseWriter, _ *http.Request) {
	standaloneChat := true
	if a.settings.EnableStandaloneChat != nil {
		standaloneChat = *a.settings.EnableStandaloneChat
	}
	dashboardIntegration := true
	if a.settings.EnableDashboardIntegration != nil {
		dashboardIntegration = *a.settings.EnableDashboardIntegration
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"attachmentMaxBytes":         attachmentMaxBytes(a.settings),
		"maxAttachments":             maxAttachmentsPerMessage,
		"maxAttachmentsTotalBytes":   maxAttachmentsTotalBytes(),
		"enableStandaloneChat":       standaloneChat,
		"enableDashboardIntegration": dashboardIntegration,
		"auditLogFullContent":        a.settings.AuditLogFullContent,
		"responseLanguage":           a.settings.ResponseLanguage,
		"maintenanceMode":            a.settings.MaintenanceMode,
	})
}

// handleIntegrations reports the live status of every optional "plus"
// integration (see integrations.go) for the Configuration page's "Grafana
// Integrations" panel. Only ever called from that Admin-only page, but
// carries nothing sensitive (no tokens, just install/health status), so it
// isn't separately role-gated here, matching handleLimits above.
func (a *App) handleIntegrations(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.integrationsStatus(r.Context()))
}

func (a *App) handleAgents(w http.ResponseWriter, r *http.Request) {
	activeCount := resolveAgentActiveCount(a.settings.AgentActiveCount)
	list := effectiveAgentList(a.settings.AgentLabels, a.settings.AgentContexts, activeCount)
	if viewerSpecialistsBlocked(requesterRole(r.Context()), a.settings.RestrictSpecialistAgentsForViewers) {
		filtered := make([]AgentInfo, 0, 1)
		for _, ag := range list {
			if ag.ID == "generic" {
				filtered = append(filtered, ag)
			}
		}
		list = filtered
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *App) handleChat(w http.ResponseWriter, r *http.Request) {
	user := requestUser(r.Context())
	if !a.getLimiter(user).Allow() {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{
			"error": "rate limit exceeded",
		})
		return
	}

	// Every cheap validation happens BEFORE ever attempting/queueing for a
	// chat slot, not after -- tryAcquireChatSlotQueued can now legitimately
	// block a caller for up to ChatQueueWaitSeconds (default 30s) waiting
	// for capacity, and chatQueueDepth is a shared, limited resource across
	// every caller. Checking a request's validity only AFTER it already
	// consumed a queue-wait slot would let a malformed/oversized/garbage
	// body tie up that shared capacity for the full wait duration before
	// ever being rejected -- pushing out legitimate queued callers for
	// nothing. Same principle as maxChatBodyBytes itself (M-04): reject
	// cheap and early, before anything expensive/shared is touched.
	r.Body = http.MaxBytesReader(w, r.Body, maxChatBodyBytes)

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid request body: " + err.Error(),
		})
		return
	}

	req.Prompt = sanitizePrompt(req.Prompt)

	if req.Prompt == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "prompt is required",
		})
		return
	}

	if !validModes[req.Mode] {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid mode: " + req.Mode,
		})
		return
	}

	if err := sanitizeContextSize(req.Context, maxContextBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": err.Error(),
		})
		return
	}

	if err := validateMessages(req.Messages); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": err.Error(),
		})
		return
	}

	if err := validateAttachments(req.Attachments, attachmentMaxBytes(a.settings)); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": err.Error(),
		})
		return
	}

	release, ok := a.tryAcquireChatSlotQueued(r.Context())
	if !ok {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{
			"error": "too many requests — global capacity reached (waited, but no slot freed up in time)",
		})
		return
	}
	defer release()

	start := time.Now()
	content, usage, err := a.chatCompletion(r.Context(), req)
	duration := time.Since(start).Seconds()
	loggedAgent := restrictAgentForRole(resolveAgent(req.Agent), requesterRole(r.Context()), a.settings.RestrictSpecialistAgentsForViewers)
	a.auditLogChat(user, requesterRole(r.Context()), req.Mode, loggedAgent, req.Prompt, content, err, duration)

	if err != nil {
		a.logger.Error("chat completion failed", "error", err, "mode", req.Mode, "duration_s", duration)
		a.metrics.recordRequest(a.settings.Model, "error", duration, 0, 0)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "LLM request failed: " + err.Error(),
		})
		return
	}

	promptTokens, completionTokens := 0, 0
	if usage != nil {
		promptTokens = usage.PromptTokens
		completionTokens = usage.CompletionTokens
	}

	a.logger.Info("chat completion succeeded", "mode", req.Mode, "model", a.settings.Model,
		"duration_s", duration, "prompt_tokens", promptTokens, "completion_tokens", completionTokens)
	a.metrics.recordRequest(a.settings.Model, "success", duration, promptTokens, completionTokens)

	writeJSON(w, http.StatusOK, ChatResponse{
		Content: content,
		Done:    true,
	})
}

func (a *App) handleNotFound(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotFound, map[string]string{
		"error": "not found",
	})
}

// requestUser identifies the caller for rate-limiting and audit logging --
// from Grafana's own authenticated plugin context (backend.User.Login), the
// same trustworthy source requesterRole already uses for permission checks.
// Security-audit finding: the client-supplied X-Grafana-User header (used
// here previously) is not authenticated -- live-confirmed a forged value in
// that header landed straight in this plugin's logs and rate-limit bucket
// key, letting a caller spoof audit attribution or evade its own per-user
// limit by rotating the header. Falls back to "anonymous" for a request
// Grafana's own backend initiated, which carries no user.
func requestUser(ctx context.Context) string {
	user := backend.PluginConfigFromContext(ctx).User
	if user == nil || user.Login == "" {
		return "anonymous"
	}
	return user.Login
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
