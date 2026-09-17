package plugin

import (
	"context"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

// requesterRole returns the Grafana org role (Admin/Editor/Viewer) of the
// user who made this request, or "" if unknown (e.g. a request Grafana's own
// backend initiated, like Alerting, carries no user). This is Grafana's own
// role info, attached to every backend request by the platform itself --
// distinct from (and not a replacement for) whatever permissions the
// configured service account itself has; see effectiveGuardrails' trust-
// boundary note in guardrails.go.
func requesterRole(ctx context.Context) string {
	user := backend.PluginConfigFromContext(ctx).User
	if user == nil {
		return ""
	}
	return user.Role
}

// maxAuditContentChars bounds how much prompt/response text goes into a
// single audit log line when AuditLogFullContent is enabled -- large enough
// to be useful for a real investigation, small enough not to blow up a
// single log line.
const maxAuditContentChars = 4000

func truncateForAudit(s string) string {
	if len(s) <= maxAuditContentChars {
		return s
	}
	return s[:maxAuditContentChars] + "...(truncated)"
}

// auditLogChat records one completed chat exchange to the backend's own
// structured logger -- Grafana's existing log pipeline (stdout, Loki,
// whatever the operator already has configured) IS the audit trail here;
// this plugin doesn't invent a separate store or its own retention job for
// it. By default only metadata is recorded (who, which agent/mode, how
// long, whether it succeeded) -- this is invisible and always-on, no
// user-facing setting. Enabling Settings.AuditLogFullContent additionally
// records the prompt/final response text (truncated), for when an admin
// genuinely needs to review what was asked/answered; the frontend shows a
// discreet notice whenever that's on (see /limits' auditLogFullContent).
func (a *App) auditLogChat(user, role, mode, agent, prompt, response string, err error, durationSeconds float64) {
	fields := []any{
		"user", user,
		"role", role,
		"mode", mode,
		"agent", agent,
		"promptChars", len(prompt),
		"responseChars", len(response),
		"duration_s", durationSeconds,
		"success", err == nil,
	}
	if err != nil {
		fields = append(fields, "error", err.Error())
	}
	if a.settings.AuditLogFullContent {
		fields = append(fields, "prompt", truncateForAudit(prompt), "response", truncateForAudit(response))
	}
	a.logger.Info("chat audit", fields...)
}

// maxAuditFieldChars bounds a client-supplied identifier before it reaches
// a log line -- a session id is a short generated string, so anything
// longer is either a bug or someone trying to write their own content into
// the audit trail.
const maxAuditFieldChars = 128

func truncateAuditField(s string) string {
	if len(s) <= maxAuditFieldChars {
		return s
	}
	return s[:maxAuditFieldChars] + "...(truncated)"
}

// auditLogExport records one conversation leaving the platform as a file.
//
// Exchanging a conversation is already recorded by auditLogChat; downloading
// a formatted copy of it -- tool calls included -- to someone's local disk
// was not recorded anywhere, even though that is the moment the content
// stops being governed by this plugin. Copy/paste always made that possible;
// a one-click, nicely formatted export is what turns it from theoretically
// possible into routine, which is exactly what an audit trail is for.
//
// Metadata only, and always-on, for the same reason auditLogChat's metadata
// is: an admin reviewing activity later needs to know that it happened, not
// what was in it. Nothing derived from what the user wrote reaches this
// line -- the conversation's title was dropped from the payload for exactly
// that reason, since it is the first 60 characters of their opening
// message. Reading a conversation back is what auditLogChat and
// AuditLogFullContent are already for; pairing this line with those is done
// on the session id.
func (a *App) auditLogExport(user, role, format, sessionID string, messageCount int) {
	a.logger.Info("export audit",
		"user", user,
		"role", role,
		"format", format,
		"sessionId", truncateAuditField(sessionID),
		"messageCount", messageCount,
	)
}
