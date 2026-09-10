package plugin

import (
	"encoding/json"
	"fmt"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

// toolSearchToolName is the canonical name of the search_tools meta-tool.
// Using a constant prevents typo-related bugs when the name is referenced
// across tool_search.go, tool_executor.go, and their tests.
const toolSearchToolName = "search_tools"

// toolSearchArgs holds the parsed arguments for the search_tools meta-tool.
type toolSearchArgs struct {
	Query string `json:"query"`
}

// searchToolDef returns the OpenAI tool definition for the search_tools meta-tool.
// This tool is always exposed when tool-search mode is active, regardless of any
// filtering applied to the rest of the tool set.
func searchToolDef() openai.Tool {
	return openai.Tool{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name: toolSearchToolName,
			Description: "Discover and activate specialized tools for a specific task. " +
				"Call this FIRST when you need to perform any specialized operation such as " +
				"querying Prometheus metrics, Loki logs, Tempo traces, Kubernetes workload data, " +
				"alert investigation, or any other observability tool -- BEFORE trying to call them " +
				"directly. Describe what you want to accomplish in plain English. " +
				"The returned tool definitions are immediately available for you to use in this " +
				"conversation by calling them with the parameters described in their schemas. " +
				"Examples: 'query prometheus metrics', 'check loki logs for errors', " +
				"'investigate kubernetes pod', 'analyze slo burn rate', 'trace bottleneck'.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {
						"type": "string",
						"description": "Plain-English description of what you want to do, e.g. 'query prometheus metrics', 'check loki logs', 'investigate kubernetes pod', 'analyze traces', 'check slo burn rate'"
					}
				},
				"required": ["query"]
			}`),
		},
	}
}

// searchToolResultEntry describes one discovered tool.
type searchToolResultEntry struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// searchToolResult is the JSON returned by the search_tools tool.
type searchToolResult struct {
	Found   int                     `json:"found"`
	Tools   []searchToolResultEntry `json:"tools"`
	Message string                  `json:"message"`
}

// searchTools finds tools in pool whose name or description contains at least one keyword
// from the query, then returns a JSON payload describing the matches. The search_tools
// tool itself is never included in the results. Returns an error only for malformed input.
func searchTools(arguments string, pool []openai.Tool) (string, error) {
	var args toolSearchArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("search_tools: invalid arguments: %w", err)
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return "", fmt.Errorf("search_tools: query must not be empty")
	}

	keywords := buildKeywords(query)

	var matches []searchToolResultEntry
	seen := map[string]bool{}
	for _, t := range pool {
		if t.Function == nil {
			continue
		}
		name := t.Function.Name
		if name == toolSearchToolName || seen[name] {
			continue
		}
		haystack := strings.ToLower(name + " " + t.Function.Description)
		if matchesAnyKeyword(haystack, keywords) {
			schema, _ := json.Marshal(t.Function.Parameters)
			matches = append(matches, searchToolResultEntry{
				Name:        name,
				Description: t.Function.Description,
				Parameters:  json.RawMessage(schema),
			})
			seen[name] = true
		}
	}

	msg := "The tools listed below are now activated and available for you to call directly " +
		"in this conversation using their exact name and the parameters shown in their schemas."
	if len(matches) == 0 {
		msg = "No specialized tools matched your query. Try different keywords or describe " +
			"your goal differently. You can also call search_tools again with a broader term."
	}

	result := searchToolResult{
		Found:   len(matches),
		Tools:   matches,
		Message: msg,
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("search_tools: marshal result: %w", err)
	}
	return string(out), nil
}

// buildKeywords lowercases query, splits on whitespace/punctuation, and
// drops tokens shorter than 3 runes (avoids matching 'a', 'in', 'to', etc.).
func buildKeywords(query string) []string {
	query = strings.ToLower(query)
	// Replace common delimiters with spaces so "loki/logs" → ["loki", "logs"].
	for _, r := range []string{"/", "_", "-", ",", ".", ":"} {
		query = strings.ReplaceAll(query, r, " ")
	}
	raw := strings.Fields(query)
	out := make([]string, 0, len(raw))
	// Common stop-words that add noise without helping match tool names.
	stop := map[string]bool{"the": true, "and": true, "for": true, "with": true, "how": true, "use": true}
	for _, kw := range raw {
		if len([]rune(kw)) >= 3 && !stop[kw] {
			out = append(out, kw)
		}
	}
	return out
}

// matchesAnyKeyword reports whether haystack contains at least one keyword.
func matchesAnyKeyword(haystack string, keywords []string) bool {
	for _, kw := range keywords {
		if strings.Contains(haystack, kw) {
			return true
		}
	}
	return false
}
