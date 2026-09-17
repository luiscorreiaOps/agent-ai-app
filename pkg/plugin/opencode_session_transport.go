package plugin

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
)

// OpenCode Go requires a stable session identifier on every request
// (x-opencode-session) for routing and prompt caching. Without it the
// endpoint rejects chat completions with 400 MissingSessionID. See
// https://opencode.ai/docs/go/#where-can-i-use-it
//
// The session id is generated once per plugin process: stable across
// requests (what the gateway wants) without any configuration.

var (
	opencodeSessionOnce sync.Once
	opencodeSessionIDv  string
)

func opencodeSessionID() string {
	opencodeSessionOnce.Do(func() {
		b := make([]byte, 12)
		if _, err := rand.Read(b); err != nil {
			opencodeSessionIDv = "agent-ai-grafana"
			return
		}
		opencodeSessionIDv = "agent-ai-" + hex.EncodeToString(b)
	})
	return opencodeSessionIDv
}

// opencodeSessionTransport injects the OpenCode Go session header (and a
// self-identifying User-Agent, as the Go docs ask clients to do) on
// requests to opencode.ai only. Inert for every other provider.
//
// openCodeConfigured enables injection for ANY host: it is set when the
// admin picked the 'opencode' provider kind in Configuration, covering
// proxied/custom OpenCode deployments behind a different hostname. Host
// detection (opencode.ai) still applies as a zero-config fallback.
type opencodeSessionTransport struct {
	base               http.RoundTripper
	openCodeConfigured bool
}

func (t *opencodeSessionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == "opencode.ai" || t.openCodeConfigured {
		if req.Header.Get("x-opencode-session") == "" {
			req.Header.Set("x-opencode-session", opencodeSessionID())
		}
		if ua := req.Header.Get("User-Agent"); ua == "" || strings.Contains(ua, "openai-go") || strings.HasPrefix(ua, "OpenAI/Go") {
			req.Header.Set("User-Agent", "agent-ai-app-grafana/1.0.0")
		}
	}
	return t.base.RoundTrip(req)
}
