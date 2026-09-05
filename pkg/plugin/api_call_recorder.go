package plugin

import (
	"context"
	"sync"
)

// What a tool actually did is invisible to the user. For query_prometheus the
// arguments are enough -- the PromQL query is the point. But list_datasources,
// list_folders and list_dashboards take no arguments at all, so their row in
// the UI says nothing beyond their name, even though those are exactly the
// calls whose reach one would want to check. Recording the HTTP requests
// actually issued against the Grafana API fills that gap, and does it with
// facts rather than documentation that would drift from the code.
//
// Carried on the context rather than on ToolExecutor: the tool calls of one
// round run concurrently (see executeToolCalls), so a shared field would mix
// several tools' requests together.

type apiCallRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *apiCallRecorder) record(call string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Upper bound: a tool looping over a paginated API must not grow the
	// event sent to the frontend without limit.
	const maxRecordedCalls = 20
	if len(r.calls) >= maxRecordedCalls {
		return
	}
	r.calls = append(r.calls, call)
}

func (r *apiCallRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	copy(out, r.calls)
	return out
}

type apiCallRecorderKey struct{}

// withAPICallRecorder attaches a fresh recorder to ctx and returns both.
func withAPICallRecorder(ctx context.Context) (context.Context, *apiCallRecorder) {
	rec := &apiCallRecorder{}
	return context.WithValue(ctx, apiCallRecorderKey{}, rec), rec
}

// recordAPICall notes one Grafana API request against the recorder in ctx, if
// any. A missing recorder is normal -- non-streaming paths don't create one.
func recordAPICall(ctx context.Context, method, path string) {
	if rec, ok := ctx.Value(apiCallRecorderKey{}).(*apiCallRecorder); ok && rec != nil {
		rec.record(method + " " + path)
	}
}
