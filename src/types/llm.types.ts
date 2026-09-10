// LLM and AI-related type definitions

/**
 * Represents a single tool call the backend made while answering (query_prometheus,
 * query_loki, list_folders, etc.) -- surfaced by the streaming chat endpoint.
 */
export interface ToolExecution {
    /** The LLM's own tool_call id -- see ToolCallInfo.id in context/types.ts for why this is needed to tell apart concurrent calls to the same tool. */
    id?: string;
    name: string;
    arguments?: string;
    status: 'pending' | 'success' | 'error';
    error?: string;
    kind?: 'grafana_tool' | 'internet_search';
    label?: string;
    statusLabel?: string;
    doneLabel?: string;
    external?: boolean;
    /** Grafana API requests this tool actually issued, filled in when the call completes. The only visibility into argument-less tools like list_datasources. */
    apiCalls?: string[];
}

/**
 * File attachment that can be sent with messages
 */
export interface Attachment {
    name: string;
    content: string; // base64 for images, text content for text files
    type: 'image' | 'text';
    mimeType?: string; // MIME type for images (e.g., 'image/png', 'image/jpeg')
}

/**
 * A message in the conversation
 */
export interface Message {
    role: 'user' | 'assistant' | 'system' | 'tool';
    content: string;
    attachments?: Attachment[];
    interrupted?: boolean;
    /** Tool calls the backend made while producing this message */
    toolExecutions?: ToolExecution[];
    /** True if the user edited this message after it was originally sent */
    edited?: boolean;
    /** True if this is the synthetic "backend unavailable" notice -- rendered
     * as a blue info banner instead of a normal chat bubble. */
    isUnavailableNotice?: boolean;
    /** Optional title for a synthetic notice rendered as an Alert. */
    noticeTitle?: string;
    /** When editing this message replaced a later part of the conversation
     * (this message wasn't the last one), the messages that used to follow
     * it -- starting with this message's own PREVIOUS content -- are kept
     * here instead of being discarded, so editing never silently deletes
     * history. Rendered as a collapsed "previous conversation" block, read
     * only (not itself editable/regenerable). Undefined when the edited
     * message already was the last one (nothing to preserve). */
    replacedBranch?: Message[];
}
