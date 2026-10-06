package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	openai "github.com/sashabaranov/go-openai"
)

type requestToolsKey struct{}

// requestTools owns discovery and presentation for one chat request. The pool
// is immutable; concurrent search calls update only this request's active set.
// A shortlist is a presentation hint, not a new execution permission.
type requestTools struct {
	pool   []openai.Tool
	lazy   bool
	light  bool
	mu     sync.Mutex
	active map[string]bool
	order  []string
}

var lightRoutingCoreTools = []string{"list_datasources", "list_alerts"}

func newRequestTools(pool []openai.Tool, lazy bool) *requestTools {
	s := &requestTools{pool: append([]openai.Tool(nil), pool...), lazy: lazy, active: make(map[string]bool)}
	s.activate(toolSearchCoreTools)
	return s
}

func withRequestTools(ctx context.Context, s *requestTools) context.Context {
	return context.WithValue(ctx, requestToolsKey{}, s)
}

func requestToolsFromContext(ctx context.Context) *requestTools {
	s, _ := ctx.Value(requestToolsKey{}).(*requestTools)
	return s
}

func (s *requestTools) allows(name string) bool {
	if name == toolSearchToolName {
		return s.lazy
	}
	for _, t := range s.pool {
		if t.Function != nil && t.Function.Name == name {
			return true
		}
	}
	return false
}

func (s *requestTools) coreNames() []string {
	if s.light {
		return lightRoutingCoreTools
	}
	return toolSearchCoreTools
}

func (s *requestTools) activate(names []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range names {
		if !s.allows(name) || name == toolSearchToolName {
			continue
		}
		// Refresh an existing tool's position too: a newly requested schema
		// must not immediately be evicted by the Light Mode budget.
		for i, activeName := range s.order {
			if activeName == name {
				s.order = append(s.order[:i], s.order[i+1:]...)
				break
			}
		}
		s.order = append(s.order, name)
		s.active[name] = true
	}
	if !s.light {
		return
	}
	// Keep at most three specialized schemas in Light Mode, including after
	// repeated discovery. Newly discovered tools replace the oldest ones.
	core := make(map[string]bool)
	for _, name := range s.coreNames() {
		core[name] = true
	}
	var specialized []string
	for _, name := range s.order {
		if !core[name] {
			specialized = append(specialized, name)
		}
	}
	for len(specialized) > lightDecisionTopK {
		delete(s.active, specialized[0])
		specialized = specialized[1:]
	}
	s.order = specialized
	for _, name := range s.coreNames() {
		if s.allows(name) {
			s.active[name] = true
		}
	}
}

func (s *requestTools) tools() []openai.Tool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.lazy {
		return append([]openai.Tool(nil), s.pool...)
	}
	out := []openai.Tool{searchToolDef()}
	for _, t := range s.pool {
		if t.Function != nil && s.active[t.Function.Name] {
			out = append(out, t)
		}
	}
	if s.light {
		for i := range out {
			out[i] = compactToolDefinition(out[i])
		}
	}
	return out
}

func (s *requestTools) search(arguments string) (string, error) {
	result, err := searchTools(arguments, s.pool)
	if err != nil {
		return "", err
	}
	var found searchToolResult
	if err := json.Unmarshal([]byte(result), &found); err != nil {
		return "", fmt.Errorf("decode tool discovery: %w", err)
	}
	if s.light && len(found.Tools) > lightDecisionTopK {
		found.Tools = found.Tools[:lightDecisionTopK]
		found.Found = len(found.Tools)
		found.Message += " Light Mode activates at most three matches per search; use a specific query to narrow results."
	}
	names := make([]string, 0, len(found.Tools))
	for i := range found.Tools {
		names = append(names, found.Tools[i].Name)
		if s.light {
			found.Tools[i].Description = truncateRunes(found.Tools[i].Description, 240)
			found.Tools[i].Parameters = compactSchema(found.Tools[i].Parameters)
		}
	}
	s.activate(names)
	out, err := json.Marshal(found)
	return string(out), err
}

func (s *requestTools) promptAddition() string {
	if !s.lazy {
		return ""
	}
	return "\n\nUse tools whose schemas are already supplied directly. If the task needs another tool, call search_tools with a specific goal; discovered schemas are supplied on the next round. The shortlist is advisory and can be expanded."
}

// Copy definitions before shortening descriptions; never mutate shared tool
// metadata or remove schema constraints such as required, enum or properties.
func compactToolDefinition(t openai.Tool) openai.Tool {
	if t.Function == nil {
		return t
	}
	f := *t.Function
	f.Description = truncateRunes(f.Description, 240)
	if f.Name == toolSearchToolName {
		f.Description = "Discover and activate tools missing from the current shortlist. Search with a specific task or tool name. Light Mode keeps at most three specialized tools active."
	}
	if raw, err := json.Marshal(f.Parameters); err == nil {
		f.Parameters = compactSchema(raw)
	}
	t.Function = &f
	return t
}

func compactSchema(raw json.RawMessage) json.RawMessage {
	var schema any
	if json.Unmarshal(raw, &schema) != nil {
		return raw
	}
	var shorten func(any)
	shorten = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			if description, ok := v["description"].(string); ok {
				v["description"] = truncateRunes(description, 96)
			}
			for _, child := range v {
				shorten(child)
			}
		case []any:
			for _, child := range v {
				shorten(child)
			}
		}
	}
	shorten(schema)
	if compact, err := json.Marshal(schema); err == nil {
		return compact
	}
	return raw
}
