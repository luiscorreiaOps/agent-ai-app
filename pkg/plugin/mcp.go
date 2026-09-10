package plugin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	openai "github.com/sashabaranov/go-openai"
)

// mcpToolsPath is where brain-agent (a standalone Memory/RAG plugin, not
// grafana-llm-app) exposes its JSON-RPC MCP endpoint -- see its own
// pkg/plugin/resources.go's handleMCPDirect.
const mcpToolsPath = "/api/plugins/shortbobcat2735-brainagent-app/resources/mcp"

// inTransitStatusPath is brain-agent's own status endpoint for its
// "RPC Bus" toggle (see its handleStatusEncryption) -- the authoritative
// source now that the toggle lives in brain-agent's plugin settings, not
// a sentinel file both plugins used to read off the shared pod filesystem
// (security-audit finding M3: that file didn't survive a pod restart and
// wasn't safe with more than one replica).
const inTransitStatusPath = "/api/plugins/shortbobcat2735-brainagent-app/resources/encryption_in_transit/status"

// inTransitStatusCacheTTL bounds how often this client re-checks
// brain-agent's toggle -- same reasoning as mcpToolsCacheTTL: it changes
// rarely (an admin flips it), so checking it on every single MCP call
// would add a network round trip to every chat turn for no benefit.
const inTransitStatusCacheTTL = 30 * time.Second

// mcpDetectTimeout bounds a tools/list call -- like llmAppDetectTimeout, this
const mcpDetectTimeout = 5 * time.Second

// mcpCallTimeout bounds an individual tools/call -- brain-agent's calls are
// local Postgres/vector-store lookups, but this matches the tool executor's
// own per-call HTTP budget rather than assuming a tighter bound.
const mcpCallTimeout = 30 * time.Second

// mcpToolsCacheTTL bounds how often tools/list is re-fetched. The tool list
// only changes when brain-agent's own tool set changes (rare), and
// re-fetching it on every chat turn would add a network round trip to every
// single request for no benefit. This also bounds retry frequency when
// brain-agent isn't installed or is unreachable: a failed attempt still
// marks the cache fresh, so a broken/absent server costs one bounded 5s
// attempt per TTL window, not one per chat turn.
const mcpToolsCacheTTL = 5 * time.Minute

// mcpToolNameCollisions are MCP tool names that would duplicate a tool this
// plugin already implements directly against Grafana's REST API in
// tool_executor.go -- our own version wins (same conventions, already
// tuned/tested). Brain-agent's own tool names (store_memory, search_memory,
// etc.) don't collide with anything here today; this guard exists so an MCP
// source can be swapped/extended later without silently shadowing a
// first-party tool.
var mcpToolNameCollisions = map[string]bool{
	"list_datasources": true,
	"query_prometheus": true,
}

// mcpMaxDescriptionLen caps each MCP tool's description before it's sent to
// the LLM, bounding token overhead regardless of how verbose a given MCP
// source's tool descriptions turn out to be, so this is trimmed to the part
// that actually
// carries the meaning for tool selection.
const mcpMaxDescriptionLen = 200

type mcpRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type mcpToolAnnotations struct {
	ReadOnlyHint bool `json:"readOnlyHint"`
}

type mcpToolDef struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	InputSchema json.RawMessage    `json:"inputSchema"`
	Annotations mcpToolAnnotations `json:"annotations"`
}

type mcpRPCResponse struct {
	Result struct {
		Tools   []mcpToolDef `json:"tools"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// MCPClient calls brain-agent's MCP endpoint -- an opt-in integration only
// ever active when brain-agent is installed, reachable, and the admin has
// turned on EnableBrainAgentTools. MCP tool access does not require any LLM
// provider to be configured on brain-agent's side -- it only needs the
// Grafana service account token this plugin already requires for every
// other tool.
type MCPClient struct {
	grafanaURL   string
	resolveToken func() string
	httpClient   *http.Client
	logger       log.Logger

	mu        sync.Mutex
	tools     []openai.Tool
	toolsTime time.Time

	inTransitOn   bool
	inTransitTime time.Time
}

func newMCPClient(grafanaURL string, resolveToken func() string, logger log.Logger) *MCPClient {
	return &MCPClient{
		grafanaURL:   strings.TrimRight(grafanaURL, "/"),
		resolveToken: resolveToken,
		httpClient:   &http.Client{Timeout: mcpCallTimeout},
		logger:       logger,
	}
}

// inTransitEncodingEnabled reports brain-agent's own "RPC Bus" toggle,
// cached for inTransitStatusCacheTTL. Defaults to false (no encoding) on
// any error -- matching the previous file-based check's implicit default
// when the file didn't exist or couldn't be read, and safer than guessing
// "on" when the real state is unknown (a body encoded when brain-agent
// isn't expecting it would fail to parse there).
func (c *MCPClient) inTransitEncodingEnabled(ctx context.Context, token string) bool {
	c.mu.Lock()
	if !c.inTransitTime.IsZero() && time.Since(c.inTransitTime) < inTransitStatusCacheTTL {
		on := c.inTransitOn
		c.mu.Unlock()
		return on
	}
	c.mu.Unlock()

	on := false
	statusCtx, cancel := context.WithTimeout(ctx, mcpDetectTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(statusCtx, http.MethodGet, c.grafanaURL+inTransitStatusPath, nil)
	if err == nil {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		if resp, doErr := c.httpClient.Do(req); doErr == nil {
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode == http.StatusOK {
				var parsed struct {
					Enabled bool `json:"enabled"`
				}
				if json.NewDecoder(io.LimitReader(resp.Body, 1<<10)).Decode(&parsed) == nil {
					on = parsed.Enabled
				}
			}
		}
	}

	c.mu.Lock()
	c.inTransitOn = on
	c.inTransitTime = time.Now()
	c.mu.Unlock()
	return on
}

func (c *MCPClient) doRPC(ctx context.Context, timeout time.Duration, method string, params any) (*mcpRPCResponse, error) {
	token := c.resolveToken()
	if token == "" {
		return nil, fmt.Errorf("no grafana token configured")
	}

	reqBody, err := json.Marshal(mcpRPCRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return nil, fmt.Errorf("marshal mcp request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// brain-agent has its own toggle for wrapping request/response bodies in
	// base64 under an "X-Brain-Encryption" header -- see brain-agent's
	// crypto.go. This is NOT real encryption (base64 is a reversible
	// encoding, not a cipher); it's just a shared protocol convention this
	// client needs to speak so it can still parse responses when that
	// toggle happens to be on. Checked once and reused for both the body
	// and the header below -- checking twice could observe the toggle flip
	// mid-way (unlikely, but a real TOCTOU) and send a base64 body with no
	// header, or vice versa, either of which brain-agent would fail to parse.
	inTransitEncodingOn := c.inTransitEncodingEnabled(ctx, token)
	if inTransitEncodingOn {
		reqBody = []byte(base64.StdEncoding.EncodeToString(reqBody))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.grafanaURL+mcpToolsPath, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create mcp request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	if inTransitEncodingOn {
		req.Header.Set("X-Brain-Encryption", "base64")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute mcp request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("mcp server rejected our service account token (status %d) -- grafanaToken is likely invalid, expired, or lacks the required permission; regenerate it in this plugin's settings", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mcp server returned status %d", resp.StatusCode)
	}

	// Limit response body to prevent memory exhaustion (10 MB) -- same
	// bound and reasoning as tool_executor.go's doGrafanaRequest
	// (security-audit finding M-04).
	const maxMCPResponseBytes = 10 * 1024 * 1024
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxMCPResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read mcp response: %w", err)
	}
	if resp.Header.Get("X-Brain-Encryption") == "base64" {
		if decoded, decErr := base64.StdEncoding.DecodeString(string(respBody)); decErr == nil {
			respBody = decoded
		}
	}

	var rpcResp mcpRPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("decode mcp response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("mcp error: %s", rpcResp.Error.Message)
	}
	return &rpcResp, nil
}

// CheckHealth verifies the MCP server is reachable and authentication
// succeeds, for use by the plugin's own CheckHealth callback.
func (c *MCPClient) CheckHealth(ctx context.Context) error {
	_, err := c.doRPC(ctx, mcpDetectTimeout, "tools/list", nil)
	return err
}

// mcpMaxPropertyDescriptionLen caps each individual parameter's description
// within a tool's input schema. Measured live against a real tools/list
// response: per-property descriptions make up ~45% of total schema JSON size
// across the 52 non-collision read-only tools -- the single biggest lever
// left for shrinking the token overhead of an opted-in MCP integration,
// after the top-level tool description cap above.
const mcpMaxPropertyDescriptionLen = 100

// trimSchemaDescriptions truncates the "description" field of every entry
// under a JSON Schema's "properties" object. type/enum/required and every
// other field that affects correct tool invocation are left untouched --
// only the free-text explanation shrinks.
func trimSchemaDescriptions(schema json.RawMessage) json.RawMessage {
	var parsed map[string]any
	if err := json.Unmarshal(schema, &parsed); err != nil {
		return schema
	}
	props, ok := parsed["properties"].(map[string]any)
	if !ok {
		return schema
	}
	for name, raw := range props {
		prop, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if desc, ok := prop["description"].(string); ok {
			prop["description"] = truncateString(desc, mcpMaxPropertyDescriptionLen)
			props[name] = prop
		}
	}
	parsed["properties"] = props
	trimmed, err := json.Marshal(parsed)
	if err != nil {
		return schema
	}
	return trimmed
}

// Tools returns the MCP-provided tool definitions as openai.Tool, filtered
// to read-only tools (per each tool's own annotations.readOnlyHint) that
// don't collide with a tool this plugin already implements directly, plus
// deliberate exceptions: brain-agent's store_memory/upsert_memory/
// suggest_memory are writes, but they're the entire point of giving the
// model a persistent memory, so they're let through explicitly by name
// rather than by relaxing the read-only filter (delete_memory/
// condense_memory stay filtered out on purpose -- destructive memory
// operations aren't exposed to the LLM directly).
func (c *MCPClient) Tools(ctx context.Context) []openai.Tool {
	c.mu.Lock()
	if !c.toolsTime.IsZero() && time.Since(c.toolsTime) < mcpToolsCacheTTL {
		tools := c.tools
		c.mu.Unlock()
		return tools
	}
	c.mu.Unlock()

	resp, err := c.doRPC(ctx, mcpDetectTimeout, "tools/list", nil)

	c.mu.Lock()
	defer c.mu.Unlock()
	// Mark the cache fresh regardless of outcome, so a broken/absent MCP
	// server is retried at most once per TTL window, not once per chat turn.
	c.toolsTime = time.Now()
	if err != nil {
		if c.logger != nil {
			c.logger.Warn("MCP tools/list failed, serving previous cache (nil on first failure)", "error", err)
		}
		return c.tools
	}

	converted := make([]openai.Tool, 0, len(resp.Result.Tools))
	for _, t := range resp.Result.Tools {
		if (!t.Annotations.ReadOnlyHint && t.Name != "store_memory" && t.Name != "upsert_memory" && t.Name != "suggest_memory") || mcpToolNameCollisions[t.Name] {
			continue
		}
		schema := t.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		} else {
			schema = trimSchemaDescriptions(schema)
		}
		converted = append(converted, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        t.Name,
				Description: truncateString(t.Description, mcpMaxDescriptionLen),
				Parameters:  schema,
			},
		})
	}
	c.tools = converted
	return converted
}

// HasTool reports whether name is one of the tools this client currently
// exposes (i.e. it was present in the last successful tools/list, and passed
// the read-only/collision filter). The tool executor uses this to decide
// whether an otherwise-unrecognized tool call should be routed to MCP.
func (c *MCPClient) HasTool(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range c.tools {
		if t.Function.Name == name {
			return true
		}
	}
	return false
}

// Call executes one MCP tool via tools/call and returns its text result.
func (c *MCPClient) Call(ctx context.Context, name, arguments string) (string, error) {
	var args any
	if arguments != "" && arguments != "{}" {
		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return "", fmt.Errorf("parse arguments for %s: %w", name, err)
		}
	}

	resp, err := c.doRPC(ctx, mcpCallTimeout, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return "", err
	}

	var text strings.Builder
	for _, chunk := range resp.Result.Content {
		text.WriteString(chunk.Text)
	}
	if resp.Result.IsError {
		return "", fmt.Errorf("%s", text.String())
	}
	return truncateString(text.String(), 50000), nil
}

// brainAgentStatus checks whether brain-agent is installed and reachable, for
// the Configuration page's integration status panel -- same
// installed-vs-reachable distinction as llmAppStatus, but brain-agent has no
// separate "configured" state to be degraded into: it's either reachable
// (tools/list succeeds) or it isn't.
func brainAgentStatus(ctx context.Context, grafanaURL, token string) (string, string) {
	if token == "" {
		return IntegrationStatusAbsent, ""
	}
	client := newMCPClient(grafanaURL, func() string { return token }, nil)
	if err := client.CheckHealth(ctx); err != nil {
		if strings.Contains(err.Error(), "status 401") || strings.Contains(err.Error(), "status 403") {
			// brain-agent IS there -- Grafana's own proxy rejected our
			// grafanaToken. Reporting this as "absent" would send an admin
			// looking for a missing plugin install instead of a
			// stale/invalid service account token, the far likelier cause.
			return IntegrationStatusAbsent, ""
		}
		return IntegrationStatusAbsent, ""
	}
	return IntegrationStatusOK, ""
}
