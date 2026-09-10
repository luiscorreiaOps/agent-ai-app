/** Analysis mode types matching the backend API contract. */
export type AnalysisMode = 'chat' | 'explain_panel' | 'analyze_logs' | 'analyze_metrics';

export interface TimeRange {
  from: string;
  to: string;
}

export interface PanelContext {
  title: string;
  description?: string;
  queries?: string[];
  fields?: string[];
  data?: unknown[][];
  /** The values the panel has ALREADY loaded, downsampled and pre-formatted
   * by summarizePanelData (services/panelData.ts). Sent so the model reads
   * what is on screen instead of re-running the panel's own query through a
   * tool call. */
  displayedData?: string;
  thresholds?: Array<{ value: number; color: string }>;
  timeRange?: TimeRange;
}

export interface LogLine {
  timestamp: string;
  line: string;
  labels?: Record<string, string>;
}

export interface LogsContext {
  query?: string;
  labels?: Record<string, string>;
  lines: LogLine[];
  timeRange?: TimeRange;
}

export interface MetricSeries {
  metric: Record<string, string>;
  values: Array<[number, string]>;
}

export interface MetricsContext {
  query?: string;
  labels?: Record<string, string>;
  series: MetricSeries[];
  timeRange?: TimeRange;
}

export interface AnalysisContext {
  panel?: PanelContext;
  /** Set by ChatInterface's own auto-discovery when the user is currently
   * viewing a real Grafana dashboard -- just enough for the backend to
   * mention it by name, not the full panel/variable breakdown the removed
   * standalone "Dashboard Chat" page used to build. */
  dashboard?: { title: string };
  logs?: LogsContext;
  metrics?: MetricsContext;
  datasources?: Array<{ name: string; type: string; uid: string }>;
  dashboards?: Array<{ title: string; uid: string }>;
  autoDiscovery?: boolean;
}

export interface ChatAttachment {
  name: string;
  /** Raw text, or base64 for images. */
  content: string;
  type: 'image' | 'text';
  mimeType?: string;
}

export interface ChatRequest {
  mode: AnalysisMode;
  agent?: string;
  prompt: string;
  context: AnalysisContext;
  messages?: Array<{ role: string; content: string }>;
  attachments?: ChatAttachment[];
}

export interface AgentInfo {
  id: string;
  label: string;
  description: string;
  /** True once the user has saved non-empty specialization text for this agent. */
  hasContext: boolean;
}

export type ToolCallKind = 'grafana_tool' | 'internet_search';

export interface ToolCallInfo {
  name: string;
  arguments: string;
  kind?: ToolCallKind;
  label?: string;
  statusLabel?: string;
  doneLabel?: string;
  external?: boolean;
}

/** Worker phase: 'running' while the worker is still investigating, 'done'/'error' once it has finished -- tells the UI when to stop showing this chip as active. */
export type WorkerEventPhase = 'running' | 'done' | 'error';

/** One live status update for a dispatched worker subagent (see dispatch_worker in the backend). taskId is the tool_call ID that dispatched it, unique per call, so several concurrently-running workers never collide in the UI. */
export interface WorkerEventInfo {
  taskId: string;
  workerType: string;
  label: string;
  task: string;
  status: string;
  phase: WorkerEventPhase;
}

export interface ChatResponse {
  content: string;
  done: boolean;
  toolCall?: ToolCallInfo;
  contextTokens?: number;
  maxTokens?: number;
  /** Short-lived activity label (e.g. "Compacting conversation context...") -- distinct from the generic thinking indicator. */
  status?: string;
  /** Live status update for one dispatched worker subagent -- distinct from `status` (a single global label) since several workers can run concurrently, each with its own chip. */
  workerEvent?: WorkerEventInfo;
}
