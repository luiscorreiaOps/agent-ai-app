import { getBackendSrv } from '@grafana/runtime';
import type { Observable } from 'rxjs';
import { ChatRequest, ChatAttachment, ChatResponse, AnalysisContext, AnalysisMode, AgentInfo } from '../context';
import { PLUGIN_ID } from '../constants';

const RESOURCE_BASE = `/api/plugins/${PLUGIN_ID}/resources`;

export interface ChatHistory {
  role: 'user' | 'assistant' | 'tool' | 'system';
  content: string;
}

export async function sendChat(
  mode: AnalysisMode,
  prompt: string,
  context: AnalysisContext,
  messages?: ChatHistory[],
  agent?: string,
  attachments?: ChatAttachment[]
): Promise<ChatResponse> {
  const request: ChatRequest = { mode, agent, prompt, context, messages, attachments };
  return getBackendSrv().post(`${RESOURCE_BASE}/chat`, request);
}

export async function testConnection(): Promise<{ status: string; message: string }> {
  // This is polled continuously to drive the chat UI's own "LLM config
  // unavailable" banner (a deliberate, friendlier in-app message) -- a
  // down LLM endpoint is an expected, handled state here, not a surprise
  // worth Grafana's own global error toast on top of it.
  return getBackendSrv().get(`${RESOURCE_BASE}/health`, undefined, undefined, { showErrorAlert: false });
}

export async function fetchAgents(): Promise<AgentInfo[]> {
  return getBackendSrv().get(`${RESOURCE_BASE}/agents`);
}

export interface Limits {
  attachmentMaxBytes: number;
  /** Max number of attachments on one message (see pkg/plugin/attachments.go). */
  maxAttachments?: number;
  /** Ceiling for the SUM of all attachments' encoded (post-base64) sizes on
   * one message -- leaves headroom under Grafana's own request-payload cap
   * for the rest of the request (prompt, history, context). */
  maxAttachmentsTotalBytes?: number;
  enableStandaloneChat: boolean;
  enableDashboardIntegration: boolean;
  /** Whether an admin has enabled full-content audit logging (see Configuration's "Audit logging" section). When true, the UI shows a discreet notice that message content may be logged. */
  auditLogFullContent: boolean;
  lightModeForDefaultAgent: boolean;
  /** Admin-configured default reply language (Configuration page's "Default reply language"); empty/unset means English. */
  responseLanguage?: string;
  /** When true, the standalone chat page shows a maintenance notice instead of the real chat -- see Configuration's "Assistant Features" section. Does not affect the dashboard-panel-menu chat. */
  maintenanceMode?: boolean;
}

/** Admin-configured limits/feature toggles the UI needs (e.g. attachment size, which surfaces are enabled). */
export async function fetchLimits(): Promise<Limits> {
  return getBackendSrv().get(`${RESOURCE_BASE}/limits`);
}

/** Live status of one optional "plus" integration with another Grafana plugin -- see pkg/plugin/integrations.go. */
export interface IntegrationStatus {
  id: string;
  name: string;
  status: 'ok' | 'degraded' | 'absent';
  enabled: boolean;
  detail?: string;
}

/** Fetches the live install/health status of every optional integration this plugin knows about, for Configuration's "Grafana Integrations" panel. */
export async function fetchIntegrationsStatus(): Promise<IntegrationStatus[]> {
  return getBackendSrv().get(`${RESOURCE_BASE}/integrations`);
}

const SETTINGS_URL = `/api/plugins/${PLUGIN_ID}/settings`;

/**
 * Thrown by saveSettingsWithVersionCheck when someone else saved a change
 * to this plugin's settings since the caller last read them. Grafana's own
 * plugin-settings write endpoint has no concurrency control of its own
 * (whoever POSTs last silently overwrites everything, including fields the
 * other admin never touched) -- this is this plugin's only defense against
 * two admins editing Configuration/Agents at the same time and clobbering
 * each other. It's enforced here, client-side, at the one point that
 * matters in practice (the real admin UI); it is not a substitute for a
 * server-side lock, which Grafana's generic settings endpoint doesn't
 * expose a way to add.
 */
export class SettingsConflictError extends Error {
  constructor() {
    super('Settings were changed by someone else since this page loaded. Reload the page to see the latest values, then try again.');
    this.name = 'SettingsConflictError';
  }
}

/** Reads the plugin's current settingsVersion counter (0 if never set). */
export async function fetchSettingsVersion(): Promise<number> {
  const settings = await getBackendSrv().get(SETTINGS_URL);
  return settings?.jsonData?.settingsVersion ?? 0;
}

/**
 * Merges jsonDataPatch into the plugin's current settings and saves,
 * bumping the shared settingsVersion counter -- throws SettingsConflictError
 * instead of saving if the live version no longer matches expectedVersion
 * (the version the caller read when it last loaded/synced its form).
 * Returns the new version so the caller can keep tracking it for its next
 * save. Grafana's settings endpoint replaces jsonData wholesale, so this
 * always reads the live settings first and spreads them under the patch,
 * instead of clobbering fields the caller's own form doesn't manage.
 */
export async function saveSettingsWithVersionCheck(
  expectedVersion: number,
  jsonDataPatch: Record<string, unknown>,
  secureJsonData?: Record<string, string>
): Promise<number> {
  const current = await getBackendSrv().get(SETTINGS_URL);
  const currentVersion = current?.jsonData?.settingsVersion ?? 0;
  if (currentVersion !== expectedVersion) {
    throw new SettingsConflictError();
  }
  const nextVersion = currentVersion + 1;
  await getBackendSrv().post(SETTINGS_URL, {
    enabled: true,
    pinned: true,
    jsonData: {
      ...current?.jsonData,
      ...jsonDataPatch,
      settingsVersion: nextVersion,
    },
    secureJsonData: secureJsonData ?? {},
  });
  return nextVersion;
}

export interface AgentsConfig {
  labels: Record<string, string>;
  contexts: Record<string, string>;
  /** Optional per-agent sampling temperature override (0.0-2.0). Absent = provider default. */
  temperatures: Record<string, number>;
  /** Optional per-agent context-window override, in tokens (up to 120000). Absent = global default. */
  contextTokens: Record<string, number>;
  /** How many custom "agent-N" slots currently exist, beyond the built-in Default. */
  activeCount: number;
  /** Optimistic-concurrency token -- see saveSettingsWithVersionCheck. */
  version: number;
}

/** Reads the per-agent custom labels/context text from the plugin's own settings. */
export async function fetchAgentsConfig(): Promise<AgentsConfig> {
  const settings = await getBackendSrv().get(SETTINGS_URL);
  return {
    labels: settings?.jsonData?.agentLabels ?? {},
    contexts: settings?.jsonData?.agentContexts ?? {},
    temperatures: settings?.jsonData?.agentTemperatures ?? {},
    contextTokens: settings?.jsonData?.agentContextTokens ?? {},
    activeCount: settings?.jsonData?.agentActiveCount ?? 3,
    version: settings?.jsonData?.settingsVersion ?? 0,
  };
}

/**
 * Persists updated agent labels/contexts/temperatures/context-tokens/count.
 * Requires Grafana Admin (same permission Grafana enforces for any plugin
 * settings write). Throws SettingsConflictError (see above) if someone else
 * saved a settings change since config.version was read -- callers must
 * catch this and prompt a reload instead of retrying blindly. Returns the
 * new version to keep tracking for the next save.
 */
export async function saveAgentsConfig(config: AgentsConfig): Promise<number> {
  return saveSettingsWithVersionCheck(config.version, {
    agentLabels: config.labels,
    agentContexts: config.contexts,
    agentTemperatures: config.temperatures,
    agentContextTokens: config.contextTokens,
    agentActiveCount: config.activeCount,
  });
}

// Bridges an RxJS Observable into a plain AsyncIterable so `for await`
// can consume it -- Observables aren't natively async-iterable, and
// getBackendSrv().chunked() (used by streamChat below, the only way to
// get a streamed response through Grafana's own BackendSrv instead of a
// raw fetch()) returns one.
function observableToAsyncIterable<T>(source: Observable<T>): AsyncIterable<T> {
  return {
    [Symbol.asyncIterator]() {
      const queue: T[] = [];
      const pending: Array<{ resolve: (r: IteratorResult<T>) => void; reject: (e: unknown) => void }> = [];
      let finished = false;
      let failure: unknown;

      const subscription = source.subscribe({
        next: (value) => {
          const waiter = pending.shift();
          if (waiter) {
            waiter.resolve({ value, done: false });
          } else {
            queue.push(value);
          }
        },
        error: (err) => {
          finished = true;
          failure = err;
          let waiter;
          while ((waiter = pending.shift())) {
            waiter.reject(err);
          }
        },
        complete: () => {
          finished = true;
          let waiter;
          while ((waiter = pending.shift())) {
            waiter.resolve({ value: undefined as unknown as T, done: true });
          }
        },
      });

      return {
        next(): Promise<IteratorResult<T>> {
          if (queue.length > 0) {
            return Promise.resolve({ value: queue.shift() as T, done: false });
          }
          if (finished) {
            return failure ? Promise.reject(failure) : Promise.resolve({ value: undefined as unknown as T, done: true });
          }
          return new Promise((resolve, reject) => pending.push({ resolve, reject }));
        },
        return(): Promise<IteratorResult<T>> {
          subscription.unsubscribe();
          return Promise.resolve({ value: undefined as unknown as T, done: true });
        },
      };
    },
  };
}

function extractBackendErrorMessage(err: any): string {
  const data = err?.data;
  if (data?.error || data?.message) {
    return data.error || data.message;
  }

  let decoded = '';
  if (ArrayBuffer.isView(data)) {
    decoded = new TextDecoder().decode(data as Uint8Array);
  } else if (data && typeof data === 'object' && typeof data.byteLength === 'number') {
    decoded = new TextDecoder().decode(new Uint8Array(data));
  } else if (typeof data === 'string') {
    decoded = data;
  }

  if (decoded) {
    try {
      const parsed = JSON.parse(decoded);
      if (parsed?.error || parsed?.message) {
        return parsed.error || parsed.message;
      }
    } catch {
      return decoded;
    }
  }

  return err?.message || `HTTP ${err?.status ?? 'error'}`;
}

export async function* streamChat(
  mode: AnalysisMode,
  prompt: string,
  context: AnalysisContext,
  messages?: ChatHistory[],
  signal?: AbortSignal,
  agent?: string,
  attachments?: ChatAttachment[]
): AsyncGenerator<ChatResponse> {
  const request: ChatRequest = { mode, agent, prompt, context, messages, attachments };

  // Security-audit finding M1: this used to be a raw fetch() with a manual
  // credentials:'include' -- the only call in this file bypassing
  // getBackendSrv(), which every other request here goes through for
  // Grafana's own session/auth handling. chunked() is BackendSrv's own
  // streaming primitive (built for exactly this -- see its doc comment:
  // "useful when reading values from a long living HTTP connection"),
  // emitting each raw response chunk as a FetchResponse<Uint8Array> over
  // an RxJS Observable instead of a Response body reader.
  const observable = getBackendSrv().chunked({
    url: `${RESOURCE_BASE}/chat/stream`,
    method: 'POST',
    data: request,
    abortSignal: signal,
  });

  const decoder = new TextDecoder();
  let buffer = '';

  try {
    for await (const response of observableToAsyncIterable(observable)) {
      if (!response.data) {
        continue;
      }
      buffer += decoder.decode(response.data, { stream: true });
      const lines = buffer.split('\n');
      buffer = lines.pop() || '';

      for (const line of lines) {
        const trimmed = line.trim();
        if (!trimmed) {
          continue;
        }
        try {
          const chunk: ChatResponse = JSON.parse(trimmed);
          yield chunk;
          if (chunk.done) {
            return;
          }
        } catch {
          // skip non-JSON lines
        }
      }
    }
  } catch (err: any) {
    // getBackendSrv().chunked() marks a cancelled request (abortSignal
    // fired) with `.cancelled: true` on its FetchError, not a DOMException
    // named "AbortError" the way raw fetch()'s own AbortController gave
    // us -- ChatInterface.tsx's catch block specifically checks
    // `error.name === 'AbortError'` to silently swallow a user-initiated
    // stop instead of showing "service is down". Re-throwing a real
    // AbortError here keeps that check working unchanged.
    if (err?.name === 'AbortError') {
      throw err;
    }
    if (err?.cancelled) {
      throw new DOMException('The user aborted a request.', 'AbortError');
    }
    // A non-2xx response surfaces as an Observable error here (a
    // FetchError-shaped object -- see @grafana/runtime's BackendSrv
    // types), not a resolved response with .ok/.json() the way raw
    // fetch() gave us. Depending on the BackendSrv path, .data may already
    // be parsed JSON or still be the raw Uint8Array/string response body.
    throw new Error(extractBackendErrorMessage(err));
  }

  // Flush remaining buffer after stream ends
  if (buffer.trim()) {
    try {
      const chunk: ChatResponse = JSON.parse(buffer.trim());
      yield chunk;
    } catch {
      // skip non-JSON trailing content
    }
  }
}
