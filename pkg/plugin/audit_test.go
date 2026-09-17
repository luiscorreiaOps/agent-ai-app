package plugin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
)

// fakeLogger captures Info() calls so tests can assert on exactly what an
// audit log line contains, without depending on the real logger's output
// format.
type fakeLogger struct {
	log.Logger
	infoArgs []interface{}
}

func (f *fakeLogger) Info(_ string, args ...interface{}) {
	f.infoArgs = args
}

func argsToMap(args []interface{}) map[string]interface{} {
	m := make(map[string]interface{})
	for i := 0; i+1 < len(args); i += 2 {
		key, ok := args[i].(string)
		if !ok {
			continue
		}
		m[key] = args[i+1]
	}
	return m
}

func TestAuditLogChat_MetadataOnlyByDefault(t *testing.T) {
	t.Parallel()

	fl := &fakeLogger{}
	a := &App{logger: fl, settings: Settings{}}

	a.auditLogChat("alice", "Viewer", "chat", "generic", "what is my password?", "I can't reveal that.", nil, 1.23)

	fields := argsToMap(fl.infoArgs)
	if fields["user"] != "alice" || fields["role"] != "Viewer" || fields["mode"] != "chat" || fields["agent"] != "generic" {
		t.Errorf("fields = %+v, missing expected metadata", fields)
	}
	if fields["success"] != true {
		t.Errorf("success = %v, want true", fields["success"])
	}
	if _, ok := fields["prompt"]; ok {
		t.Error("prompt should NOT be logged when AuditLogFullContent is false")
	}
	if _, ok := fields["response"]; ok {
		t.Error("response should NOT be logged when AuditLogFullContent is false")
	}
	if fields["promptChars"] != len("what is my password?") {
		t.Errorf("promptChars = %v, want %d", fields["promptChars"], len("what is my password?"))
	}
}

func TestAuditLogChat_FullContentWhenEnabled(t *testing.T) {
	t.Parallel()

	fl := &fakeLogger{}
	a := &App{logger: fl, settings: Settings{AuditLogFullContent: true}}

	a.auditLogChat("bob", "Editor", "chat", "generic", "hello", "hi there", nil, 0.5)

	fields := argsToMap(fl.infoArgs)
	if fields["prompt"] != "hello" {
		t.Errorf("prompt = %v, want %q", fields["prompt"], "hello")
	}
	if fields["response"] != "hi there" {
		t.Errorf("response = %v, want %q", fields["response"], "hi there")
	}
}

func TestAuditLogChat_RecordsError(t *testing.T) {
	t.Parallel()

	fl := &fakeLogger{}
	a := &App{logger: fl, settings: Settings{}}

	a.auditLogChat("carol", "Admin", "chat", "generic", "hi", "", errors.New("boom"), 2.0)

	fields := argsToMap(fl.infoArgs)
	if fields["success"] != false {
		t.Errorf("success = %v, want false", fields["success"])
	}
	if fields["error"] != "boom" {
		t.Errorf("error = %v, want %q", fields["error"], "boom")
	}
}

func TestTruncateForAudit(t *testing.T) {
	t.Parallel()

	short := "hello"
	if got := truncateForAudit(short); got != short {
		t.Errorf("truncateForAudit(short) = %q, want unchanged %q", got, short)
	}

	long := strings.Repeat("a", maxAuditContentChars+500)
	got := truncateForAudit(long)
	if len(got) <= maxAuditContentChars {
		t.Errorf("expected truncated marker appended, got len %d", len(got))
	}
	if !strings.HasSuffix(got, "...(truncated)") {
		t.Errorf("expected truncation suffix, got suffix %q", got[len(got)-20:])
	}
}

func TestRequesterRole_ReturnsRoleFromPluginContext(t *testing.T) {
	t.Parallel()

	ctx := backend.WithPluginContext(context.Background(), backend.PluginContext{
		User: &backend.User{Login: "viewer1", Role: "Viewer"},
	})
	if got := requesterRole(ctx); got != "Viewer" {
		t.Errorf("requesterRole() = %q, want %q", got, "Viewer")
	}
}

func TestRequesterRole_EmptyWhenNoUser(t *testing.T) {
	t.Parallel()

	// A request Grafana's own backend initiated (e.g. Alerting) carries no
	// User -- must not panic, must return "".
	ctx := backend.WithPluginContext(context.Background(), backend.PluginContext{})
	if got := requesterRole(ctx); got != "" {
		t.Errorf("requesterRole() = %q, want empty string", got)
	}
}

func TestRequesterRoleLine(t *testing.T) {
	t.Parallel()

	if got := requesterRoleLine(""); got != "" {
		t.Errorf("requesterRoleLine(\"\") = %q, want empty string", got)
	}
	if got := requesterRoleLine("Editor"); !strings.Contains(got, "Editor") {
		t.Errorf("requesterRoleLine(\"Editor\") = %q, want it to mention the role", got)
	}
}

func TestAuditLogExport_MetadataOnlyByDefault(t *testing.T) {
	t.Parallel()

	fl := &fakeLogger{}
	a := &App{logger: fl, settings: Settings{}}

	a.auditLogExport("alice", "Viewer", "md", "session-42", 6)

	fields := argsToMap(fl.infoArgs)
	if fields["user"] != "alice" || fields["role"] != "Viewer" || fields["format"] != "md" {
		t.Errorf("fields = %+v, missing expected metadata", fields)
	}
	if fields["sessionId"] != "session-42" {
		t.Errorf("sessionId = %v, want %q", fields["sessionId"], "session-42")
	}
	if fields["messageCount"] != 6 {
		t.Errorf("messageCount = %v, want 6", fields["messageCount"])
	}
	// The conversation's title is the first 60 characters of the user's
	// opening message, so it never reaches this route at all -- not even
	// with AuditLogFullContent on. This line records that an export
	// happened; reading the conversation back is auditLogChat's job.
	if _, ok := fields["title"]; ok {
		t.Error("no user-written text belongs in an export audit line")
	}
}

// AuditLogFullContent widens the chat audit line to the prompt/response
// text. It must not widen this one: there is no content here to reveal, and
// an export line that grew a title under a setting would put user-written
// text into the log through a route that never receives it.
func TestAuditLogExport_FullContentSettingAddsNothing(t *testing.T) {
	t.Parallel()

	withFullContent := &fakeLogger{}
	(&App{logger: withFullContent, settings: Settings{AuditLogFullContent: true}}).
		auditLogExport("bob", "Editor", "md", "session-7", 2)

	withoutFullContent := &fakeLogger{}
	(&App{logger: withoutFullContent, settings: Settings{}}).
		auditLogExport("bob", "Editor", "md", "session-7", 2)

	if len(withFullContent.infoArgs) != len(withoutFullContent.infoArgs) {
		t.Errorf("fields with full content = %+v, without = %+v, want identical",
			argsToMap(withFullContent.infoArgs), argsToMap(withoutFullContent.infoArgs))
	}
}

func TestAuditLogExport_TruncatesAnOversizedSessionID(t *testing.T) {
	t.Parallel()

	fl := &fakeLogger{}
	a := &App{logger: fl, settings: Settings{}}

	a.auditLogExport("alice", "Viewer", "md", strings.Repeat("x", 5000), 1)

	got, _ := argsToMap(fl.infoArgs)["sessionId"].(string)
	if len(got) > maxAuditFieldChars+len("...(truncated)") {
		t.Errorf("sessionId length = %d, want it bounded", len(got))
	}
}

// The download itself happens in the browser; this route exists so that
// taking a conversation off-platform leaves the same kind of trace as
// having the conversation in the first place.
func TestHandleExport_RecordsTheDownload(t *testing.T) {
	t.Parallel()

	fl := &fakeLogger{}
	a := &App{logger: fl, settings: Settings{}}

	rec := httptest.NewRecorder()
	body := `{"sessionId":"session-1","format":"md","messageCount":4}`
	a.handleExport(rec, httptest.NewRequest(http.MethodPost, "/export", strings.NewReader(body)))

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	fields := argsToMap(fl.infoArgs)
	if fields["format"] != "md" || fields["sessionId"] != "session-1" || fields["messageCount"] != 4 {
		t.Errorf("fields = %+v, want the export metadata", fields)
	}
	// No authenticated user on a bare httptest request -- the audit line
	// must still name someone rather than leaving the field empty.
	if fields["user"] != "anonymous" {
		t.Errorf("user = %v, want %q", fields["user"], "anonymous")
	}
}

func TestHandleExport_RejectsAnUnknownFormat(t *testing.T) {
	t.Parallel()

	fl := &fakeLogger{}
	a := &App{logger: fl, settings: Settings{}}

	rec := httptest.NewRecorder()
	body := `{"sessionId":"session-1","format":"exe","messageCount":1}`
	a.handleExport(rec, httptest.NewRequest(http.MethodPost, "/export", strings.NewReader(body)))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if fl.infoArgs != nil {
		t.Errorf("nothing should be logged for a rejected request, got %+v", fl.infoArgs)
	}
}

// The route takes four small fields. A body far past that is either a bug
// or an attempt to write content into the audit log through a metadata
// route, and must not reach the logger.
func TestHandleExport_RejectsAnOversizedBody(t *testing.T) {
	t.Parallel()

	fl := &fakeLogger{}
	a := &App{logger: fl, settings: Settings{}}

	rec := httptest.NewRecorder()
	body := `{"sessionId":"` + strings.Repeat("a", maxExportAuditBodyBytes) + `","format":"md","messageCount":1}`
	a.handleExport(rec, httptest.NewRequest(http.MethodPost, "/export", strings.NewReader(body)))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if fl.infoArgs != nil {
		t.Errorf("nothing should be logged for a rejected request, got %+v", fl.infoArgs)
	}
}
