import type { ChatSession } from '../types/chat.types';
import type { Message } from '../types/llm.types';

/**
 * Turning a conversation into a file the user keeps.
 *
 * Markdown rather than the JSON the storage layer already produces: what
 * people do with a conversation is paste it into a ticket, a pull request,
 * or a runbook, and JSON serves none of those. The JSON export stays where
 * it is, for re-importing a session into the plugin.
 *
 * What gets written is what the user saw, plus the tool calls behind it.
 * Those are the part that makes an answer checkable months later -- an
 * exported conversation that says "the error rate is 3%" without the query
 * that produced it is an assertion, not evidence.
 */

/** Roles are labelled the way the chat labels them, not by their wire name. */
function roleHeading(message: Message): string {
    switch (message.role) {
        case 'user':
            return '### You';
        case 'assistant':
            return '### Agent AI';
        case 'system':
            return '### System';
        default:
            return `### ${message.role}`;
    }
}

/**
 * Tool calls are folded into a details block: they are the evidence, but
 * they are long, and a reader wants the answer first.
 */
function toolExecutionsToMarkdown(message: Message): string[] {
    const executions = message.toolExecutions ?? [];
    if (executions.length === 0) {
        return [];
    }

    const lines: string[] = ['', '<details>', `<summary>${executions.length} tool call${executions.length > 1 ? 's' : ''}</summary>`, ''];
    for (const execution of executions) {
        const name = execution.name || 'tool';
        lines.push(`**${name}**`);
        if (execution.arguments) {
            lines.push('', '```json', execution.arguments.trim(), '```');
        }
        if (execution.apiCalls?.length) {
            lines.push('', ...execution.apiCalls.map((call) => `- \`${call}\``));
        }
        lines.push('');
    }
    lines.push('</details>');
    return lines;
}

function attachmentsToMarkdown(message: Message): string[] {
    const attachments = message.attachments ?? [];
    if (attachments.length === 0) {
        return [];
    }
    // Named, not embedded: a base64 image would make the file unreadable and
    // enormous, and the name is what tells the reader what was attached.
    return ['', `_Attachments: ${attachments.map((a) => a.name).join(', ')}_`];
}

export function sessionToMarkdown(session: ChatSession): string {
    const lines: string[] = [`# ${session.title || 'Conversation'}`, ''];

    const meta: string[] = [];
    if (session.createdAt) {
        meta.push(`Started ${new Date(session.createdAt).toLocaleString()}`);
    }
    if (session.updatedAt && session.updatedAt !== session.createdAt) {
        meta.push(`last updated ${new Date(session.updatedAt).toLocaleString()}`);
    }
    if (session.agent && session.agent !== 'generic') {
        meta.push(`agent: ${session.agentLabel ?? session.agent}`);
    }
    if (meta.length) {
        lines.push(`_${meta.join(' — ')}_`, '');
    }

    for (const message of session.messages ?? []) {
        // Skipped rather than rendered: tool messages are the raw results the
        // model read, already summarised in its answer, and they would dwarf
        // the conversation.
        if (message.role === 'tool') {
            continue;
        }

        let heading = roleHeading(message);
        if (message.edited) {
            heading += ' _(edited)_';
        }
        if (message.interrupted) {
            heading += ' _(interrupted)_';
        }
        lines.push(heading, '');

        if (message.noticeTitle) {
            lines.push(`> **${message.noticeTitle}**`, '>');
            lines.push(...message.content.split('\n').map((line) => `> ${line}`));
        } else {
            lines.push(message.content);
        }

        lines.push(...attachmentsToMarkdown(message));
        lines.push(...toolExecutionsToMarkdown(message));
        lines.push('', '---', '');
    }

    // Trailing separator from the last message.
    while (lines.length && (lines[lines.length - 1] === '' || lines[lines.length - 1] === '---')) {
        lines.pop();
    }
    return lines.join('\n') + '\n';
}

/**
 * A filename someone can find again: the conversation's own title, reduced
 * to something every filesystem accepts, plus the date.
 */
export function exportFileName(session: ChatSession, extension: string): string {
    const slug = (session.title || 'conversation')
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, '-')
        .replace(/^-+|-+$/g, '')
        .slice(0, 60);
    const date = new Date(session.updatedAt || session.createdAt || Date.now())
        .toISOString()
        .slice(0, 10);
    return `${slug || 'conversation'}-${date}.${extension}`;
}

/**
 * Hands the file to the browser.
 *
 * Two details here are load-bearing, both discovered the hard way -- the
 * first version opened the blob URL as a page instead of downloading it.
 *
 * The anchor is never added to the document. Grafana installs a
 * document-level click handler (interceptLinkClicks) that, for any anchor
 * carrying an href and no target, calls preventDefault() and then navigates
 * to that href itself. A blob URL contains "://", so it takes the
 * "absolute link" branch and becomes window.location.href -- the download is
 * cancelled and the file is displayed instead. A click on a detached element
 * never bubbles to document, so the interceptor never sees it. The explicit
 * target is a second guard: the interceptor ignores anchors that declare
 * one, and the browser ignores target when download is present.
 *
 * The object URL is revoked on the next tick rather than immediately:
 * revoking it in the same frame as the click cancels the download in some
 * browsers, since the fetch has not started yet.
 */
export function downloadTextFile(filename: string, content: string, mimeType = 'text/markdown'): void {
    const blob = new Blob([content], { type: `${mimeType};charset=utf-8` });
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = url;
    link.download = filename;
    link.target = '_self';
    link.rel = 'noopener';
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 0);
}

/** Downloads one session as Markdown. */
export function downloadSessionAsMarkdown(session: ChatSession): void {
    downloadTextFile(exportFileName(session, 'md'), sessionToMarkdown(session));
}

/** Downloads one session as JSON -- the shape importSession reads back. */
export function downloadSessionAsJSON(session: ChatSession): void {
    downloadTextFile(exportFileName(session, 'json'), JSON.stringify(session, null, 2), 'application/json');
}
