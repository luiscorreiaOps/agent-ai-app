import React, { useState, useEffect, useLayoutEffect, useRef, useMemo, useCallback, Suspense } from 'react';
import { ThinkingBlock } from './components/ThinkingBlock';
import { ToolCallContainer } from './components/ToolCallContainer';
import { ActivityAccordion } from './components/ActivityAccordion';
import { WorkerActivityTracker } from './components/WorkerActivityTracker';
import { FilePreview } from './components/FilePreview';
import { AttachmentModal } from './components/AttachmentModal';

// Grafana's own location abstraction -- NOT react-router-dom. Graft's original
// code used react-router-dom hooks (useSearchParams/useNavigate/useLocation),
// but Grafana externalizes react-router-dom to whatever version its own core
// bundles, which doesn't guarantee v6 hooks exist -- confirmed broken here at
// runtime ("useSearchParams is not a function") despite compiling fine.
// locationService is Grafana's supported, version-stable way to read/write the
// URL and history from a plugin.
import { locationService, getAppEvents } from '@grafana/runtime';

// Grafana packages
import { Alert, TextArea, useStyles2, useTheme2, Icon, Modal, Dropdown, Menu, MenuItem, Divider } from '@grafana/ui';
import { GrafanaTheme2, AppEvents, type PluginExtensionPanelContext, type DateTime } from '@grafana/data';

// Local services -- streamChat/sendChat talk to THIS plugin's own Go backend
// (agent-ai-app resources/chat), which owns tool-calling, the LLM
// endpoint, and the system prompt server-side. No MCP client, no
// grafana-llm-app dependency, no frontend-side orchestrator.
import { streamChat, fetchAgents, fetchLimits, type ChatHistory } from '../../../api/client';
import { PLUGIN_ID } from '../../../constants';
import type { AnalysisContext, AgentInfo, WorkerEventInfo } from '../../../context';
import { summarizePanelData } from '../../../services/panelData';
import type { Message, ToolExecution } from '../../../types/llm.types';
import { contextService, DataSourceContext, DashboardContext } from '../../../services/context';
import { chatHistoryService, type ChatSession } from '../../../services/chatHistory';
import { getQuickPrompts, saveQuickPrompt, type QuickPrompt } from '../../../services/quickPrompts';
import { getLandingText, type ResponseLanguage } from '../../../services/landingText';
import { truncateMessages } from '../../../services/truncation';
// Distinct color per specialized agent -- used both for the history list tag
// and for the active-agent icon glow, so the same agent always reads as the
// same color everywhere. Assignment is positional (agentColors.ts), so it
// stays fixed regardless of what the user renames the agent to.
import { colorForAgent } from '../../../agentColors';

// Local hooks
import { useRollingPlaceholder, usePluginSettings, useAutoScroll } from './hooks';

// Styles
import { getStyles } from './ChatInterface.styles';

// Lazy-loaded: CodeBlock pulls in react-syntax-highlighter, MermaidBlock
// pulls in mermaid (already its own dynamic import() internally, see that
// file) -- neither is needed for a plain-text reply with no code fence or
// diagram, which is the common case. Wrapping the components themselves
// (not just mermaid's own internal import) keeps their own module code out
// of the initial bundle too, not just their heaviest dependency.
const CodeBlock = React.lazy(() => import('./components/CodeBlock').then((m) => ({ default: m.CodeBlock })));
const MermaidBlock = React.lazy(() => import('./components/MermaidBlock').then((m) => ({ default: m.MermaidBlock })));
// Sensible file types for this assistant's purpose: reference text/config/
// log/query files, plus images (screenshots) for vision-capable models.
const ATTACHMENT_ACCEPT = 'image/*,text/*,.md,.json,.yaml,.yml,.log,.csv,.ts,.js,.tsx,.jsx';
const ATTACHMENT_TEXT_EXTENSIONS = ['.txt', '.md', '.json', '.yaml', '.yml', '.log', '.csv', '.ts', '.js', '.tsx', '.jsx'];
const WAITING_RESPONSE_MESSAGES = [
  'Analyzing',
  'Gathering more context',
  'Checking the available data',
  'Almost ready',
  'One more moment',
];
const WAITING_STEP_MESSAGES = [
  'Working through the steps',
  'Checking tool results',
  'Reviewing the collected data',
  'Combining the findings',
  'Preparing the final answer',
];
const WAITING_RESPONSE_MESSAGE_DELAY_TICKS = 20;
const WAITING_RESPONSE_MESSAGE_STEP_TICKS = 20;
const RESPONSE_REVEAL_INTERVAL_MS = 35;
const RESPONSE_REVEAL_MIN_CHARS = 28;
const RESPONSE_REVEAL_MAX_CHARS = 80;
// How long a dispatch_worker chip stays visible after reporting 'done'/
// 'error' before WorkerActivityTracker drops it -- long enough that a fast
// worker's completion is still noticeable, short enough not to clutter the
// view once the main answer has moved on.
const WORKER_CHIP_LINGER_MS = 1500;

// Size actually sent over the wire for one attachment -- for an image, that
// is ONLY the base64 portion of the data URL (handleSend strips the
// "data:...;base64," prefix before sending, see file.content.split(',')[1]),
// which is ~33% larger than the original file's raw byte size; for text, the
// raw string length is what's sent as-is, no encoding inflation. Checking
// file.size directly (as the per-file cap above does) misses that inflation
// entirely -- this is what the COMBINED-total check needs instead.
const encodedAttachmentSize = (content: string, type: 'image' | 'text'): number => {
  if (type === 'image') {
    const base64 = content.split(',')[1];
    return (base64 ?? content).length;
  }
  return content.length;
};

const shuffleWaitingMessages = (messages: string[]) => {
  const shuffled = [...messages];
  for (let i = shuffled.length - 1; i > 0; i -= 1) {
    const j = Math.floor(Math.random() * (i + 1));
    [shuffled[i], shuffled[j]] = [shuffled[j], shuffled[i]];
  }
  return shuffled;
};

// Module-scope, not component state -- ChatInterface can mount more than
// once in the same page load (e.g. React re-mounting the tree, or the panel
// extension's modal instance), each with its own health check resolving
// llmReady independently. A per-component state flag would let each mount
// replay the sweep once; this flag is shared across all of them so the
// whole page only ever plays it a single time.
let logoShinePlayedGlobally = false;

// Normal chat uses 3 thinking dots; the panel-preview flow uses 6 -- a more
// pronounced animation reinforcing that this read-only entry point can take
// longer to answer than a regular message.
// Grafana's icon set only has "plus-circle"/"plus-square" -- neither matches
// iconButton's own circle treatment (border matching agentInlineButton's, see
// ChatInterface.styles.ts), so this stays a bare inline glyph, sized/colored
// by the button around it (currentColor + no fixed viewBox padding beyond
// the stroke itself), rather than pulling in an icon dependency.
const PlusIcon = () => (
  <svg width="20" height="20" viewBox="0 0 20 20" fill="none" xmlns="http://www.w3.org/2000/svg" aria-hidden="true">
    <path d="M10 3v14M3 10h14" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" />
  </svg>
);

// Grafana's own toast (top-right, dismissible, themed) instead of a native
// alert() -- a blocking browser dialog stalls the whole tab (and looks
// jarring inside an embedded Grafana panel/page) for what's just a
// validation notice, not something that needs to halt everything until
// acknowledged.
const notifyWarning = (message: string) => {
  getAppEvents().publish({ type: AppEvents.alertWarning.name, payload: [message] });
};

const DEFAULT_THINKING_DOT_DELAYS = ['0s', '0.2s', '0.4s'];
const PREVIEW_THINKING_DOT_DELAYS = ['0s', '0.15s', '0.3s', '0.45s', '0.6s', '0.75s'];

// Fixed icon per quick-prompt slot (icons are not user-editable, only
// title/content are) -- keyed by QuickPrompt.id.
const QUICK_PROMPT_ICONS: Record<string, React.ReactNode> = {
  introduction: (
    <svg viewBox="0 0 24 24" width="20" height="20" stroke="currentColor" strokeWidth="2" fill="none" strokeLinecap="round" strokeLinejoin="round">
      <polygon points="1 6 1 22 8 18 16 22 23 18 23 2 16 6 8 2 1 6"></polygon>
      <line x1="8" y1="2" x2="8" y2="18"></line>
      <line x1="16" y1="6" x2="16" y2="22"></line>
    </svg>
  ),
  incidents: (
    <svg viewBox="0 0 24 24" width="20" height="20" stroke="currentColor" strokeWidth="2" fill="none" strokeLinecap="round" strokeLinejoin="round">
      <path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"></path>
      <line x1="12" y1="9" x2="12" y2="13"></line>
      <line x1="12" y1="17" x2="12.01" y2="17"></line>
    </svg>
  ),
  information: (
    <svg viewBox="0 0 24 24" width="20" height="20" stroke="currentColor" strokeWidth="2" fill="none" strokeLinecap="round" strokeLinejoin="round">
      <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"></path>
      <polyline points="14 2 14 8 20 8"></polyline>
      <line x1="16" y1="13" x2="8" y2="13"></line>
      <line x1="16" y1="17" x2="8" y2="17"></line>
      <polyline points="10 9 9 9 8 9"></polyline>
    </svg>
  ),
};


// Turns Grafana's raw time range strings ("now-6h" / "now") into a natural
// English phrase for the panel-preview pre-filled question -- "Period:
// now-6h to now." reads as a literal debug string, not a real sentence.
const formatRelativeTimeRange = (fromRaw: string | DateTime, toRaw: string | DateTime): string => {
  const from = String(fromRaw);
  const to = String(toRaw);
  const match = /^now-(\d+)([mhdw])$/.exec(from);
  if (to === 'now' && match) {
    const amount = Number(match[1]);
    const unitWord: Record<string, string> = { m: 'minute', h: 'hour', d: 'day', w: 'week' };
    const word = unitWord[match[2]] ?? match[2];
    if (amount === 1) {
      return `in the last ${word}`;
    }
    return `in the last ${amount} ${word}s`;
  }
  return `over the period from ${from} to ${to}`;
};

// "today HH:MM" for a conversation started today, "MM/DD HH:MM" otherwise.
const formatConversationTimestamp = (ts: number): string => {
  const date = new Date(ts);
  const time = date.toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit' });
  const isToday = new Date().toDateString() === date.toDateString();
  if (isToday) {
    return `today ${time}`;
  }
  return `${date.toLocaleDateString('en-US', { month: '2-digit', day: '2-digit' })} ${time}`;
};

// Helper function to get time-based greeting
const getTimeBasedGreeting = (language: ResponseLanguage): string => {
  const hour = new Date().getHours();
  const t = getLandingText(language);
  if (hour >= 5 && hour < 12) {
    return t.greetingMorning;
  } else if (hour >= 12 && hour < 18) {
    return t.greetingAfternoon;
  } else {
    return t.greetingEvening;
  }
};

// Helper function to get greeting message with optional user name
const getGreetingMessage = (language: ResponseLanguage, userName?: string): string => {
  const timeGreeting = getTimeBasedGreeting(language);
  return userName ? `${timeGreeting}, ${userName}!` : timeGreeting;
};

// Custom hook for rolling placeholder text with typing animation
// Hook definitions have been moved to ./ChatInterface/hooks/

// Helper function to normalize markdown content
const normalizeMarkdown = (content: string): string => {
  // Replace multiple consecutive newlines with a single newline
  return content.replace(/\n\n+/g, '\n');
};

// Builds the AnalysisContext sent alongside the prompt to the backend.
// The backend (Go) owns the system prompt and tool-calling
// entirely server-side -- the frontend only needs to pass along the panel/
// dashboard the user was looking at (if any) and let the backend auto-discover
// datasources/dashboards for everything else.
const buildAnalysisContext = (
  dashboard: DashboardContext,
  dataSources: DataSourceContext[],
  panelOverride?: Readonly<PluginExtensionPanelContext>,
): AnalysisContext => {
  // Deliberately NOT sending the full datasources list on every message
  // anymore -- it cost real prompt tokens on every single turn, and the
  // backend already has the list_datasources tool for on-demand discovery
  // whenever the model actually needs to know what's configured. When the
  // chat opens from a specific panel, its own datasource(s) are still worth
  // sending directly below -- that's a fact about what the user is looking
  // at, not a lookup the model would otherwise have to make.
  const context: AnalysisContext = {
    autoDiscovery: true,
  };

  if (dashboard?.uid) {
    context.dashboard = { title: dashboard.title || 'Untitled dashboard' };
  }

  if (panelOverride) {
    context.panel = {
      title: panelOverride.title,
      queries: panelOverride.targets?.map(t => (t as any).expr || (t as any).query || '').filter(Boolean),
      timeRange: { from: String(panelOverride.timeRange.from), to: String(panelOverride.timeRange.to) },
      // The frames the dashboard already fetched. See services/panelData.ts:
      // sending them turns "re-run this query to see the values" into "read
      // the values", which is both one agent round cheaper and truer to what
      // the user is actually looking at.
      displayedData: summarizePanelData(panelOverride),
    };

    const panelDatasourceUids = Array.from(new Set(
      (panelOverride.targets || [])
        .map(t => (t as any).datasource?.uid)
        .filter((uid: unknown): uid is string => typeof uid === 'string' && uid.length > 0)
    ));
    if (panelDatasourceUids.length > 0) {
      context.datasources = dataSources
        .filter(ds => panelDatasourceUids.includes(ds.uid))
        .map(ds => ({ name: ds.name, type: ds.type, uid: ds.uid }));
    }
  }

  return context;
};





type MarkdownBlock =
  | { type: 'heading'; level: number; text: string }
  | { type: 'paragraph'; text: string }
  | { type: 'blockquote'; text: string }
  | { type: 'unordered-list'; items: string[] }
  | { type: 'ordered-list'; items: string[] }
  | { type: 'code'; language: string; text: string }
  | { type: 'table'; headers: string[]; rows: string[][] };

const safeMarkdownHref = (href: string): string | undefined => {
  return /^(https?:|mailto:)/i.test(href) ? href : undefined;
};

const isTableSeparator = (line: string): boolean => {
  const cells = line.trim().replace(/^\|/, '').replace(/\|$/, '').split('|');
  return cells.length > 1 && cells.every((cell) => /^:?-{3,}:?$/.test(cell.trim()));
};

const splitTableCells = (line: string): string[] => {
  return line.trim().replace(/^\|/, '').replace(/\|$/, '').split('|').map((cell) => cell.trim());
};

const parseMarkdownBlocks = (content: string): MarkdownBlock[] => {
  const lines = normalizeMarkdown(content).split(/\r?\n/);
  const blocks: MarkdownBlock[] = [];
  let i = 0;

  while (i < lines.length) {
    const line = lines[i];

    if (!line.trim()) {
      i += 1;
      continue;
    }

    const fence = /^```([A-Za-z0-9_-]*)\s*$/.exec(line.trim());
    if (fence) {
      const codeLines: string[] = [];
      i += 1;
      while (i < lines.length && !/^```\s*$/.test(lines[i].trim())) {
        codeLines.push(lines[i]);
        i += 1;
      }
      if (i < lines.length) {
        i += 1;
      }
      blocks.push({ type: 'code', language: fence[1] || '', text: codeLines.join('\n') });
      continue;
    }

    const heading = /^(#{1,6})\s+(.+)$/.exec(line);
    if (heading) {
      blocks.push({ type: 'heading', level: heading[1].length, text: heading[2].trim() });
      i += 1;
      continue;
    }

    if (line.includes('|') && i + 1 < lines.length && isTableSeparator(lines[i + 1])) {
      const headers = splitTableCells(line);
      const rows: string[][] = [];
      i += 2;
      while (i < lines.length && lines[i].includes('|') && lines[i].trim()) {
        rows.push(splitTableCells(lines[i]));
        i += 1;
      }
      blocks.push({ type: 'table', headers, rows });
      continue;
    }

    if (/^\s*[-*+]\s+/.test(line)) {
      const items: string[] = [];
      while (i < lines.length && /^\s*[-*+]\s+/.test(lines[i])) {
        items.push(lines[i].replace(/^\s*[-*+]\s+/, '').trim());
        i += 1;
      }
      blocks.push({ type: 'unordered-list', items });
      continue;
    }

    if (/^\s*\d+\.\s+/.test(line)) {
      const items: string[] = [];
      while (i < lines.length && /^\s*\d+\.\s+/.test(lines[i])) {
        items.push(lines[i].replace(/^\s*\d+\.\s+/, '').trim());
        i += 1;
      }
      blocks.push({ type: 'ordered-list', items });
      continue;
    }

    if (/^\s*>\s?/.test(line)) {
      const quoteLines: string[] = [];
      while (i < lines.length && /^\s*>\s?/.test(lines[i])) {
        quoteLines.push(lines[i].replace(/^\s*>\s?/, '').trim());
        i += 1;
      }
      blocks.push({ type: 'blockquote', text: quoteLines.join(' ') });
      continue;
    }

    const paragraphLines: string[] = [];
    while (
      i < lines.length &&
      lines[i].trim() &&
      !/^```/.test(lines[i].trim()) &&
      !/^(#{1,6})\s+/.test(lines[i]) &&
      !/^\s*[-*+]\s+/.test(lines[i]) &&
      !/^\s*\d+\.\s+/.test(lines[i]) &&
      !/^\s*>\s?/.test(lines[i])
    ) {
      paragraphLines.push(lines[i].trim());
      i += 1;
    }
    blocks.push({ type: 'paragraph', text: paragraphLines.join(' ') });
  }

  return blocks;
};

const renderInlineMarkdown = (text: string): React.ReactNode[] => {
  const nodes: React.ReactNode[] = [];
  const pattern = /(`[^`]+`|\[[^\]]+\]\([^)]+\)|\*\*[^*]+\*\*|__[^_]+__|\*[^*]+\*|_[^_]+_)/g;
  let lastIndex = 0;
  let match: RegExpExecArray | null;

  while ((match = pattern.exec(text)) !== null) {
    if (match.index > lastIndex) {
      nodes.push(text.slice(lastIndex, match.index));
    }

    const token = match[0];
    const key = `${match.index}-${token}`;
    const link = /^\[([^\]]+)\]\(([^)]+)\)$/.exec(token);
    if (link) {
      const safeHref = safeMarkdownHref(link[2].trim());
      nodes.push(safeHref ? (
        <a key={key} href={safeHref} target="_blank" rel="noopener noreferrer">
          {link[1]}
        </a>
      ) : link[1]);
    } else if (token.startsWith('`')) {
      nodes.push(<code key={key}>{token.slice(1, -1)}</code>);
    } else if (token.startsWith('**') || token.startsWith('__')) {
      nodes.push(<strong key={key}>{token.slice(2, -2)}</strong>);
    } else {
      nodes.push(<em key={key}>{token.slice(1, -1)}</em>);
    }

    lastIndex = pattern.lastIndex;
  }

  if (lastIndex < text.length) {
    nodes.push(text.slice(lastIndex));
  }

  return nodes;
};

const MemoizedMarkdown = React.memo(({ content, theme, onRender, isStreaming }: { content: string; theme: GrafanaTheme2; onRender: () => void; isStreaming: boolean }) => {
  const blocks = React.useMemo(() => parseMarkdownBlocks(content), [content]);

  return (
    <>
      {blocks.map((block, index) => {
        if (block.type === 'code') {
          if (block.language === 'mermaid') {
            return (
              <Suspense key={index} fallback={<pre>{block.text}</pre>}>
                <MermaidBlock theme={theme} onRender={onRender} isStreaming={isStreaming}>
                  {block.text}
                </MermaidBlock>
              </Suspense>
            );
          }

          return block.language ? (
            <Suspense key={index} fallback={<pre>{block.text}</pre>}>
              <CodeBlock language={block.language} theme={theme}>
                {block.text}
              </CodeBlock>
            </Suspense>
          ) : (
            <pre key={index}><code>{block.text}</code></pre>
          );
        }

        if (block.type === 'heading') {
          const Heading = `h${block.level}` as keyof JSX.IntrinsicElements;
          return <Heading key={index}>{renderInlineMarkdown(block.text)}</Heading>;
        }

        if (block.type === 'blockquote') {
          return <blockquote key={index}>{renderInlineMarkdown(block.text)}</blockquote>;
        }

        if (block.type === 'unordered-list') {
          return <ul key={index}>{block.items.map((item, itemIndex) => <li key={itemIndex}>{renderInlineMarkdown(item)}</li>)}</ul>;
        }

        if (block.type === 'ordered-list') {
          return <ol key={index}>{block.items.map((item, itemIndex) => <li key={itemIndex}>{renderInlineMarkdown(item)}</li>)}</ol>;
        }

        if (block.type === 'table') {
          return (
            <table key={index}>
              <thead>
                <tr>{block.headers.map((header, cellIndex) => <th key={cellIndex}>{renderInlineMarkdown(header)}</th>)}</tr>
              </thead>
              <tbody>
                {block.rows.map((row, rowIndex) => (
                  <tr key={rowIndex}>
                    {block.headers.map((_, cellIndex) => <td key={cellIndex}>{renderInlineMarkdown(row[cellIndex] ?? '')}</td>)}
                  </tr>
                ))}
              </tbody>
            </table>
          );
        }

        return <p key={index}>{renderInlineMarkdown(block.text)}</p>;
      })}
    </>
  );
});

MemoizedMarkdown.displayName = 'MemoizedMarkdown';




export interface ChatInterfaceProps {
  /** Panel context snapshot passed when launched from a Grafana panel menu. */
  panelContext?: Readonly<PluginExtensionPanelContext>;
  /** Called when the modal wrapper should be closed (only set in modal mode). */
  onDismiss?: () => void;
  /**
   * Shared mutable ref written on every render so the title-bar "Open in Agent AI"
   * button (which lives outside this React subtree) can read the latest session
   * ID at click time without any React context crossing.
   */
  sessionRef?: React.MutableRefObject<{ sessionId?: string } | null>;
  /**
   * Static UI chrome (greeting, welcome subtitle, default quick prompts) is
   * localized to this -- separate from the LLM's own reply language, which
   * the backend system prompt already handles per-request. Defaults to
   * 'english' so standalone/test renders of this component are unaffected.
   */
  responseLanguage?: ResponseLanguage;
}

export const ChatInterface = ({ panelContext, onDismiss, sessionRef, responseLanguage = 'english' }: ChatInterfaceProps = {}) => {
  const styles = useStyles2(getStyles);
  const theme = useTheme2();
  const [input, setInput] = useState('');
  const [messages, setMessages] = useState<Message[]>([]);
  const [conversationStartedAt, setConversationStartedAt] = useState<number | null>(null);
  // resumedMarker/historyLoadedMessageCountRef: a real user-reported gap --
  // conversationTimestamp (above) only ever shows the ORIGINAL creation
  // time of a session, once, at the very top. Reopening an old session from
  // history and continuing it showed no visual cue at all that the new
  // reply came much later than the old messages above it -- this renders a
  // second timestamp divider, but only right before the first NEW message
  // sent after resuming (not on every reopen -- just reading old history
  // without sending anything shouldn't add a marker that never corresponds
  // to an actual resume-and-continue moment).
  const historyLoadedMessageCountRef = useRef<number | null>(null);
  const [resumedMarker, setResumedMarker] = useState<{ atIndex: number; timestamp: number } | null>(null);
  const [quickPrompts, setQuickPrompts] = useState<QuickPrompt[]>(() => getQuickPrompts(responseLanguage));
  const [editingPromptId, setEditingPromptId] = useState<string | null>(null);
  const [editPromptTitle, setEditPromptTitle] = useState('');
  const [editPromptContent, setEditPromptContent] = useState('');
  const [isLoading, setIsLoading] = useState(false);
  const [waitingResponseTick, setWaitingResponseTick] = useState(0);
  const [waitingResponseMessages, setWaitingResponseMessages] = useState<string[]>(() => shuffleWaitingMessages(WAITING_RESPONSE_MESSAGES));
  const [waitingStepMessages, setWaitingStepMessages] = useState<string[]>(() => shuffleWaitingMessages(WAITING_STEP_MESSAGES));

  // Location/history compatibility layer backed by Grafana's own locationService
  // (see the import above for why -- react-router-dom's hooks are not usable here).
  const [locationState, setLocationState] = useState(() => locationService.getLocation());
  useEffect(() => {
    const unlisten = locationService.getHistory().listen((upd: any) => {
      setLocationState(upd.location ?? upd);
    });
    return () => unlisten();
  }, []);
  const searchParams = useMemo(() => new URLSearchParams(locationState.search), [locationState.search]);
  const location = locationState;
  const setSearchParams = (query: Record<string, string | undefined>) => {
    const newSearch = new URLSearchParams();
    Object.entries(query).forEach(([k, v]) => {
      if (v !== undefined) { newSearch.set(k, v); }
    });
    const pathname = locationService.getLocation().pathname;
    const searchStr = newSearch.toString();
    locationService.getHistory().replace({ pathname, search: searchStr ? `?${searchStr}` : '' });
  };

  useEffect(() => {
    if (!isLoading) {
      setWaitingResponseTick(0);
      return;
    }

    setWaitingResponseMessages(shuffleWaitingMessages(WAITING_RESPONSE_MESSAGES));
    setWaitingStepMessages(shuffleWaitingMessages(WAITING_STEP_MESSAGES));
    const interval = window.setInterval(() => {
      setWaitingResponseTick((tick) => tick + 1);
    }, 1000);
    return () => window.clearInterval(interval);
  }, [isLoading]);
  const navigate = (path: string, opts?: { replace?: boolean; state?: any }) => {
    const history = locationService.getHistory();
    if (opts?.replace) {
      history.replace(path, opts?.state);
    } else {
      history.push(path, opts?.state);
    }
  };

  const [isListening, setIsListening] = useState(false);
  const [currentSessionId, setCurrentSessionId] = useState<string | undefined>();
  const [showHistory, setShowHistory] = useState(false);
  const [historySessions, setHistorySessions] = useState<ChatSession[]>([]);
  // pending: true means "waiting on the assistant's response" -- the badge
  // shows 0% with no token breakdown, instead of either nothing at all or a
  // stale number left over from a previous message/agent. Cleared the
  // moment a real contextTokens/maxTokens pair arrives with the response.
  const [contextUsage, setContextUsage] = useState<{ tokens: number; maxTokens: number; pending?: boolean } | null>(null);
  const [activityStatus, setActivityStatus] = useState<string | null>(null);
  // Live chips for dispatch_worker subagents currently running this turn --
  // several can be active at once (see pkg/plugin/worker_dispatch.go), each
  // keyed by its dispatching tool_call ID. A worker's chip is kept briefly
  // after it reports 'done'/'error' (see WORKER_CHIP_LINGER_MS below)
  // instead of vanishing the instant the event arrives, so a fast worker's
  // completion is still visible to the user for a moment.
  const [activeWorkers, setActiveWorkers] = useState<WorkerEventInfo[]>([]);
  // The backend announces a rate-limit retry once with its initial wait
  // (see pkg/plugin/streaming.go), then goes quiet until it actually retries
  // -- without this, the "retrying in Ns..." text just sat frozen for the
  // whole wait. Ticks down locally, purely cosmetic; the real retry timing
  // is still owned by the backend.
  const [rateLimitCountdown, setRateLimitCountdown] = useState<number | null>(null);
  const [agents, setAgents] = useState<AgentInfo[]>([]);
  const [selectedAgent, setSelectedAgent] = useState('generic');
  const [copiedMessageIndex, setCopiedMessageIndex] = useState<number | null>(null);
  const [editingMessageIndex, setEditingMessageIndex] = useState<number | null>(null);
  // Which edited messages currently have their replacedBranch (see
  // llm.types.ts) expanded -- collapsed by default, keyed by message index.
  const [expandedBranches, setExpandedBranches] = useState<Set<number>>(new Set());
  const fileInputRef = useRef<HTMLInputElement>(null);

  const abortControllerRef = useRef<AbortController | null>(null);
  // User-reported: closing the panel-preview/modal window mid-response
  // should count as stopping it -- otherwise the request keeps running
  // (and, per the interrupt race just fixed above, could still write
  // content nobody can see anymore) after the dialog the user closed is
  // long gone. Grafana's own Modal wires its close button/backdrop-click/
  // Esc directly to the onDismiss it hands ChatInterface, which just
  // unmounts this component -- there's no click handler of ours to
  // intercept, so an unmount cleanup is the one place that reliably fires
  // for every way of closing it.
  useEffect(() => {
    return () => {
      abortControllerRef.current?.abort();
      // Real bug: aborting the fetch above stops the network stream, but
      // the typewriter-style reveal effect (scheduleAssistantReveal) runs
      // its own independent setInterval to progressively type out content
      // ALREADY received, entirely decoupled from the request itself. If
      // the panel-preview modal (or any instance) unmounts while that
      // interval is still mid-reveal, nothing was clearing it -- it kept
      // firing forever, calling setState on an unmounted component every
      // RESPONSE_REVEAL_INTERVAL_MS. Each preview closed in that window
      // leaked one more of these permanently-running intervals, compounding
      // with repeated use until the tab visibly stutters/hangs. clearInterval
      // is called directly (not via the clearAssistantReveal callback,
      // which isn't declared until after this effect in source order) --
      // safe because this cleanup only ever runs after the full render body
      // -- including that declaration -- has already executed.
      if (assistantRevealTimerRef.current) {
        clearInterval(assistantRevealTimerRef.current);
        assistantRevealTimerRef.current = null;
      }
    };
  }, []);
  const processedPromptRef = useRef<string | null>(null);
  const thinkingStartTimeRef = useRef<number | null>(null);
  // Tracks the latest messages array without creating a render dependency.
  // Used by the post-chat save callback to read state without a setMessages updater.
  const latestMessagesRef = useRef<Message[]>(messages);
  const assistantRevealTimerRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const assistantRevealTargetRef = useRef('');
  const assistantRevealVisibleRef = useRef('');
  const assistantRevealToolExecutionsRef = useRef<ToolExecution[]>([]);
  const assistantRevealDoneResolverRef = useRef<(() => void) | null>(null);
  const [selectedFiles, setSelectedFiles] = useState<Array<{ name: string; content: string; type: 'image' | 'text'; mimeType?: string }>>([]);
  const [previewAttachment, setPreviewAttachment] = useState<{ name: string; content: string; type: 'image' | 'text'; mimeType?: string } | null>(null);

  const updateVisibleAssistantMessage = useCallback((visibleContent: string, toolExecutions: ToolExecution[]) => {
    setMessages((prev) => {
      const updated = [...prev];
      const lastMessage = updated[updated.length - 1];
      if (!lastMessage || lastMessage.role !== 'assistant') {
        return prev;
      }
      updated[updated.length - 1] = {
        ...lastMessage,
        content: visibleContent,
        toolExecutions: toolExecutions.length > 0 ? toolExecutions : undefined,
      };
      return updated;
    });
  }, []);

  const clearAssistantReveal = useCallback(() => {
    if (assistantRevealTimerRef.current) {
      clearInterval(assistantRevealTimerRef.current);
      assistantRevealTimerRef.current = null;
    }
  }, []);

  const resolveAssistantReveal = useCallback(() => {
    const resolve = assistantRevealDoneResolverRef.current;
    assistantRevealDoneResolverRef.current = null;
    resolve?.();
  }, []);

  const resetAssistantReveal = useCallback(() => {
    clearAssistantReveal();
    assistantRevealTargetRef.current = '';
    assistantRevealVisibleRef.current = '';
    assistantRevealToolExecutionsRef.current = [];
    resolveAssistantReveal();
  }, [clearAssistantReveal, resolveAssistantReveal]);

  const nextRevealSize = useCallback((remaining: string) => {
    if (remaining.length <= RESPONSE_REVEAL_MIN_CHARS) {
      return remaining.length;
    }
    const windowText = remaining.slice(RESPONSE_REVEAL_MIN_CHARS, RESPONSE_REVEAL_MAX_CHARS);
    const boundary = windowText.search(/[\s\n.,;:!?]/);
    return boundary >= 0 ? RESPONSE_REVEAL_MIN_CHARS + boundary + 1 : Math.min(remaining.length, RESPONSE_REVEAL_MAX_CHARS);
  }, []);

  const scheduleAssistantReveal = useCallback((fullContent: string, toolExecutions: ToolExecution[]) => {
    assistantRevealTargetRef.current = fullContent;
    assistantRevealToolExecutionsRef.current = toolExecutions;

    if (assistantRevealVisibleRef.current.length > fullContent.length || !fullContent.startsWith(assistantRevealVisibleRef.current)) {
      assistantRevealVisibleRef.current = '';
    }

    if (assistantRevealVisibleRef.current.length < fullContent.length) {
      const remaining = fullContent.slice(assistantRevealVisibleRef.current.length);
      assistantRevealVisibleRef.current = fullContent.slice(
        0,
        assistantRevealVisibleRef.current.length + nextRevealSize(remaining)
      );
    }
    updateVisibleAssistantMessage(assistantRevealVisibleRef.current, toolExecutions);

    if (assistantRevealVisibleRef.current.length >= fullContent.length) {
      clearAssistantReveal();
      updateVisibleAssistantMessage(fullContent, toolExecutions);
      resolveAssistantReveal();
      return;
    }

    if (assistantRevealTimerRef.current) {
      return;
    }

    assistantRevealTimerRef.current = setInterval(() => {
      const target = assistantRevealTargetRef.current;
      const visible = assistantRevealVisibleRef.current;
      if (visible.length >= target.length) {
        clearAssistantReveal();
        updateVisibleAssistantMessage(target, assistantRevealToolExecutionsRef.current);
        resolveAssistantReveal();
        return;
      }
      const remaining = target.slice(visible.length);
      const nextVisible = target.slice(0, visible.length + nextRevealSize(remaining));
      assistantRevealVisibleRef.current = nextVisible;
      updateVisibleAssistantMessage(nextVisible, assistantRevealToolExecutionsRef.current);
    }, RESPONSE_REVEAL_INTERVAL_MS);
  }, [clearAssistantReveal, nextRevealSize, resolveAssistantReveal, updateVisibleAssistantMessage]);

  const waitForAssistantRevealComplete = useCallback(() => {
    if (!assistantRevealTargetRef.current || assistantRevealVisibleRef.current.length >= assistantRevealTargetRef.current.length) {
      return Promise.resolve();
    }
    return new Promise<void>((resolve) => {
      const previousResolve = assistantRevealDoneResolverRef.current;
      assistantRevealDoneResolverRef.current = () => {
        previousResolve?.();
        resolve();
      };
    });
  }, []);

  useEffect(() => {
    return () => clearAssistantReveal();
  }, [clearAssistantReveal]);

  // Floors at 1s (never shows "0s...") -- the real completion clears
  // activityStatus on its own once the next chunk actually arrives.
  useEffect(() => {
    if (rateLimitCountdown === null || rateLimitCountdown <= 1) {
      return;
    }
    const timeout = setTimeout(() => {
      setRateLimitCountdown((seconds) => (seconds !== null ? seconds - 1 : null));
      setActivityStatus(`Rate limited, retrying in ${rateLimitCountdown - 1}s...`);
    }, 1000);
    return () => clearTimeout(timeout);
  }, [rateLimitCountdown]);

  // Use custom hooks - check the assistant's own backend health (endpoint + model
  // are configured entirely server-side; there is only one mode, no toggle).
  const { llmConfigured, llmHealthy, isLoading: settingsLoading } = usePluginSettings();
  const llmReady = llmConfigured && llmHealthy;
  // While a quick-prompt tile is being edited, everything else on the
  // landing screen (the chat input, attach/dictate/agent controls, the
  // other tiles) locks until that edit is saved or cancelled -- otherwise a
  // user can type into the main chat box with an unsaved edit still open
  // underneath it, which reads as two independent things happening at once.
  const quickPromptEditing = editingPromptId !== null;
  const inputLocked = !llmReady || quickPromptEditing;

  // Plays the landing logo's one-shot shine sweep only once the backend is
  // actually confirmed reachable -- not on every render, and never again
  // once it has played anywhere on the page (see logoShinePlayedGlobally).
  const [logoShinePlayed, setLogoShinePlayed] = useState(logoShinePlayedGlobally);
  useEffect(() => {
    if (llmReady && !logoShinePlayedGlobally) {
      logoShinePlayedGlobally = true;
      setLogoShinePlayed(true);
    }
  }, [llmReady]);
  // Launched from a panel's context menu: a read-only, one-shot preview --
  // auto-sent question, no way to reply, visually muted so it never reads as
  // a normal chat someone can keep talking to (deliberate cost control: no
  // follow-up questions means no extra GPU usage from this entry point).
  const isPanelPreview = Boolean(panelContext);
  // standaloneContainerRef/standaloneHeight: real-measured fix for a live
  // bug -- a static `100dvh` on the root container assumes it starts at
  // viewport y=0, but in standalone full-page mode it actually starts BELOW
  // Grafana's own top nav bar (a real height neither Grafana nor this
  // plugin exposes as a CSS constant, and not the same in every Grafana
  // version/skin/kiosk-mode). That mismatch left a residual gap (confirmed
  // live via DOM inspection: ~25px) that the OUTER page could still scroll
  // by -- enough to carry chatHeader (back button + context-usage badge)
  // off-screen in a long conversation even after `overflow: hidden` fixed
  // the bigger, whole-page-grows version of this same bug. Measuring the
  // container's own actual top offset and subtracting it from 100dvh closes
  // that gap regardless of whatever chrome Grafana renders above it.
  const standaloneContainerRef = useRef<HTMLDivElement>(null);
  const [standaloneHeight, setStandaloneHeight] = useState('100dvh');
  useLayoutEffect(() => {
    if (onDismiss || isPanelPreview) {
      return; // modal/panel-preview size themselves differently -- see the style prop below
    }
    const measure = () => {
      const top = standaloneContainerRef.current?.getBoundingClientRect().top ?? 0;
      setStandaloneHeight(`calc(100dvh - ${top}px)`);
    };
    measure();
    window.addEventListener('resize', measure);
    return () => window.removeEventListener('resize', measure);
  }, [onDismiss, isPanelPreview]);
  // inputAreaRefCallback/inputAreaBottomPadding: inputArea (disclaimer +
  // textarea box) is now a translucent, blurred overlay floating over
  // messageList (see ChatInterface.styles.ts's inputArea/container
  // comments) instead of a normal flex sibling that reserves its own space
  // -- so messageList's own content now needs a bottom padding equal to
  // inputArea's real height, or the last message(s) render physically
  // underneath it (only dimly visible through the blur, not click/read
  // accessible above it -- real, reproduced bug: the tail end of a longer
  // response, including its own copy button, ended up permanently
  // unreachable this way). inputArea's height genuinely varies (file
  // attachment previews, multi-line textarea growth, error banners), so
  // this is measured live rather than hardcoded.
  //
  // A CALLBACK ref (not a plain useRef read inside a useEffect) is required
  // here -- real, reproduced bug: messages.length === 0 renders an entirely
  // different landing layout with no inputArea/messageList at all, so a
  // useLayoutEffect keyed on mount would capture a null ref before the user
  // ever sends a first message, see it's null, bail out, and never run
  // again once the real chat layout (and its real inputArea div) mounts
  // after that first send -- leaving inputAreaBottomPadding stuck at its
  // initial 0 forever. A callback ref fires again on every mount/unmount of
  // the node it's attached to, including this landing-to-chat transition.
  const inputAreaObserverRef = useRef<ResizeObserver | null>(null);
  const [inputAreaBottomPadding, setInputAreaBottomPadding] = useState(0);
  const inputAreaRefCallback = useCallback((el: HTMLDivElement | null) => {
    inputAreaObserverRef.current?.disconnect();
    inputAreaObserverRef.current = null;
    if (!el || typeof ResizeObserver === 'undefined') {
      setInputAreaBottomPadding(0);
      return;
    }
    const observer = new ResizeObserver(() => {
      // offsetHeight (border-box, padding+border included) -- NOT
      // entry.contentRect.height, which explicitly excludes inputArea's own
      // padding and would undercount its real rendered height.
      setInputAreaBottomPadding(el.offsetHeight);
    });
    observer.observe(el);
    inputAreaObserverRef.current = observer;
  }, []);
  const [shouldAutoScroll, setShouldAutoScroll] = useState(true);
  const [showScrollButton, setShowScrollButton] = useState(false);
  const {
    messagesEndRef,
    messageListRef,
    scrollToBottom,
    handleScroll,
    scrollDownPage,
  } = useAutoScroll({ shouldAutoScroll, setShouldAutoScroll, showScrollButton, setShowScrollButton });
  // Mirrors shouldAutoScroll for the MutationObserver callback below, which
  // is created once per mount (see its own doc comment) and would otherwise
  // only ever see the shouldAutoScroll value from whichever render set up
  // the observer, not its current value at the time a mutation actually fires.
  const shouldAutoScrollRef = useRef(shouldAutoScroll);
  useEffect(() => {
    shouldAutoScrollRef.current = shouldAutoScroll;
  }, [shouldAutoScroll]);

  // Use rolling placeholder hook for animated text
  const rollingPlaceholder = useRollingPlaceholder();

  // Get user context for personalized greeting
  const userContext = contextService.getUserContext();
  const userName = userContext.name || userContext.login;
  const greetingMessage = getGreetingMessage(responseLanguage, userName);

  // Keep latestMessagesRef in sync on every render so the post-chat save
  // callback can read the final messages state without a setMessages updater.
  useEffect(() => {
    latestMessagesRef.current = messages;
  });

  // Agents don't execute anything different yet -- same tools, same guardrails --
  // they only layer a deeper domain focus onto the base system prompt server-side.
  useEffect(() => {
    fetchAgents()
      .then(setAgents)
      .catch(() => setAgents([]));
  }, []);

  // Mirrors the backend's own default (see pkg/plugin/attachments.go) so the
  // UI has a sane bound even before this loads; overwritten with the real
  // admin-configured value once fetched.
  const [attachmentMaxBytes, setAttachmentMaxBytes] = useState(51200);
  // Mirror the backend's own defaults (maxAttachmentsPerMessage,
  // maxAttachmentsTotalBytes in pkg/plugin/attachments.go) the same way.
  const [maxAttachments, setMaxAttachments] = useState(10);
  const [maxAttachmentsTotalBytes, setMaxAttachmentsTotalBytes] = useState(12 * 1024 * 1024 - 512 * 1024);
  // Off until the real admin-configured value loads -- never show the
  // monitoring notice speculatively before we actually know it's on.
  const [auditLogFullContent, setAuditLogFullContent] = useState(false);
  const [lightModeForDefaultAgent, setLightModeForDefaultAgent] = useState(false);
  useEffect(() => {
    fetchLimits()
      .then((limits) => {
        setAttachmentMaxBytes(limits.attachmentMaxBytes);
        if (limits.maxAttachments) {
          setMaxAttachments(limits.maxAttachments);
        }
        if (limits.maxAttachmentsTotalBytes) {
          setMaxAttachmentsTotalBytes(limits.maxAttachmentsTotalBytes);
        }
        setAuditLogFullContent(limits.auditLogFullContent);
        setLightModeForDefaultAgent(limits.lightModeForDefaultAgent ?? false);
      })
      .catch(() => {});
  }, []);

  // Keep sessionRef in sync when currentSessionId changes so the title-bar
  // "Open in Agent AI" button can always read the latest sessionId at click time.
  useEffect(() => {
    if (sessionRef) {
      sessionRef.current = { sessionId: currentSessionId };
    }
  }, [currentSessionId, sessionRef]);

  const handleFileSelect = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = e.target.files;
    if (!files || files.length === 0) {
      return;
    }

    const newFiles: Array<{ name: string; content: string; type: 'image' | 'text'; mimeType?: string }> = [];
    // Running total of what actually gets SENT over the wire (the
    // base64-only portion for images, matching handleSend's own
    // file.content.split(',')[1] -- the data URL prefix never leaves the
    // browser), not the raw file.size checked above. Starts from what's
    // already staged so this catches "10 files each just under the
    // per-file cap, but their combined base64 size blows the request
    // payload limit" -- attachmentMaxBytes alone never could.
    let runningTotalBytes = selectedFiles.reduce((sum, f) => sum + encodedAttachmentSize(f.content, f.type), 0);

    for (let i = 0; i < files.length; i++) {
      const file = files[i];
      if (selectedFiles.length + newFiles.length >= maxAttachments) {
        notifyWarning(`Only ${maxAttachments} attachments are allowed per message.`);
        break;
      }
      if (file.size > attachmentMaxBytes) {
        notifyWarning(`File ${file.name} is ${(file.size / 1024).toFixed(1)}KB -- attachments are limited to ${(attachmentMaxBytes / 1024).toFixed(0)}KB.`);
        continue;
      }

      let content: string;
      let type: 'image' | 'text';
      let mimeType: string | undefined;
      if (file.type.startsWith('image/')) {
        // Sent to the LLM using the vision content format. The configured
        // model must support multimodal requests; text-only models reject
        // this server-side with a clear user-facing error.
        mimeType = file.type;
        type = 'image';
        const reader = new FileReader();
        content = await new Promise<string>((resolve) => {
          reader.onloadend = () => resolve(reader.result as string);
          reader.readAsDataURL(file);
        });
      } else if (file.type.startsWith('text/') || ATTACHMENT_TEXT_EXTENSIONS.some((ext) => file.name.endsWith(ext))) {
        type = 'text';
        const reader = new FileReader();
        content = await new Promise<string>((resolve) => {
          reader.onloadend = () => resolve(reader.result as string);
          reader.readAsText(file);
        });
      } else {
        notifyWarning(`File ${file.name} is not supported. Only Text or Image files are supported.`);
        continue;
      }

      const encodedSize = encodedAttachmentSize(content, type);
      if (runningTotalBytes + encodedSize > maxAttachmentsTotalBytes) {
        notifyWarning(`File ${file.name} would push this message's combined attachment size over the ${(maxAttachmentsTotalBytes / (1024 * 1024)).toFixed(1)}MB limit -- remove another attachment first.`);
        continue;
      }
      runningTotalBytes += encodedSize;
      newFiles.push({ name: file.name, content, type, mimeType });
    }

    setSelectedFiles((prev) => [...prev, ...newFiles]);
    if (fileInputRef.current) {
      fileInputRef.current.value = '';
    }
  };

  const removeFile = (index: number) => {
    setSelectedFiles((prev) => prev.filter((_, i) => i !== index));
  };

  const clearFiles = () => {
    setSelectedFiles([]);
    if (fileInputRef.current) {
      fileInputRef.current.value = '';
    }
  };

  // Sync state with URL and load session if specified
  useEffect(() => {
    if (onDismiss) { return; } // modal mode — no URL session to restore or reset
    const sessionId = searchParams.get('session');
    const isChatActive = searchParams.get('chat');

    if (sessionId) {
      const session = chatHistoryService.getSession(sessionId);
      if (session && session.id !== currentSessionId) {
        setMessages(session.messages);
        setCurrentSessionId(session.id);
        setSelectedAgent(session.agent ?? 'generic');
        setConversationStartedAt(session.createdAt);
        // See historyLoadedMessageCountRef's doc comment -- remembers how
        // many messages existed at load time, so handleSend can tell "the
        // first new message after resuming" apart from just reopening old
        // history to read it.
        historyLoadedMessageCountRef.current = session.messages.length;
        setResumedMarker(null);
      }
    } else if (!isChatActive && !isLoading) {
      // If we are navigating to landing page, abort any ongoing request
      if (abortControllerRef.current) {
        abortControllerRef.current.abort();
        abortControllerRef.current = null;
        setIsLoading(false);
      }
      // Only reset if we have messages to clear
      if (messages.length > 0 || currentSessionId !== undefined) {
        setMessages([]);
        setCurrentSessionId(undefined);
        setConversationStartedAt(null);
        historyLoadedMessageCountRef.current = null;
        setResumedMarker(null);
      }
    }
  }, [searchParams, currentSessionId, messages.length, isLoading, onDismiss]);

  // Handle pre-filled prompt from navigation state (separate effect to avoid loop)
  useEffect(() => {
    const state = location.state as { prompt?: string; returnTo?: string } | null;
    if (state?.prompt && state.prompt !== processedPromptRef.current) {
      processedPromptRef.current = state.prompt;
      setInput(state.prompt);
      // Clear the state so it doesn't persist on refresh/navigation
      navigate(location.pathname, { replace: true, state: { ...state, prompt: undefined } });
    }
  }, [location.state, location.pathname, navigate]);

  // Launched from a panel's context menu: this is a read-only preview, not an
  // open conversation (see isPanelPreview below) -- send the question
  // automatically instead of waiting for the user, since there's no input
  // box to send it from in this mode anyway (mount-only).
  useEffect(() => {
    if (!panelContext) { return; }
    const dsUid = panelContext.targets?.[0]?.datasource?.uid;
    // A panel whose datasource is set via a dashboard template variable
    // (e.g. "${datasource}") reports that raw, unresolved variable string
    // here instead of a real UID -- passing it along as if it were a real
    // UID fed a bogus value into the model's tool calls and broke them.
    const dsHint = dsUid && !dsUid.startsWith('$') ? ` (datasource uid: ${dsUid})` : '';
    const periodPhrase = formatRelativeTimeRange(panelContext.timeRange.from, panelContext.timeRange.to);
    handleSend(
      `Explain the "${panelContext.title}" panel on the ` +
      `"${panelContext.dashboard.title}" dashboard${dsHint}, ${periodPhrase}.`
    );
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  // Pre-fill input from panel context URL params — used when "Open in Agent AI"
  // is clicked from the modal title bar (full-page navigation path)
  useEffect(() => {
    if (onDismiss) { return; } // modal mode — URL params belong to the host page
    const panelTitle     = searchParams.get('panelTitle');
    const dashboardTitle = searchParams.get('dashboardTitle');
    const from           = searchParams.get('from');
    const to             = searchParams.get('to');
    const dsUid          = searchParams.get('dsUid');
    if (!panelTitle) { return; }

    const dsHint = dsUid ? ` (datasource uid: ${dsUid})` : '';
    const timeHint = from && to ? ` Time range: ${from} to ${to}.` : '';
    setInput(
      `Tell me about the "${panelTitle}" panel` +
      `${dashboardTitle ? ` on the "${dashboardTitle}" dashboard` : ''}${dsHint}.${timeHint}`
    );
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  // Initial scroll to bottom when chat loads
  useEffect(() => {
    if (messages.length > 0 && !isLoading) {
      // Scroll to bottom when opening a history session
      // Use 'auto' for instant scroll to ensure it reaches the bottom
      setTimeout(() => scrollToBottom('auto'), 200);
    }
  }, [currentSessionId, messages.length, isLoading, scrollToBottom]);

  // Auto-scroll during streaming or when new messages appear
  useEffect(() => {
    if (messages.length > 0) {
      const lastMsg = messages[messages.length - 1];
      // Scroll if it's a user message (new question) -- 'auto' for the same
      // race-avoidance reason as the streaming branch below.
      if (lastMsg.role === 'user') {
        scrollToBottom('auto');
        setShouldAutoScroll(true);
      }
      // Only auto-scroll during streaming if user is near bottom or has auto-scroll enabled.
      // Real, reproduced bug: 'smooth' (this call's default) animates over
      // ~300-500ms, but a large single content jump (a tool-call accordion
      // collapsing right as its final text arrives, common on longer
      // responses) can make that animation run LONGER than
      // programmaticScrollRef's 600ms self-inflicted-scroll guard window --
      // the animation's own trailing scroll events then fire after the
      // guard has already lifted, handleScroll samples a still-mid-animation
      // scrollTop as "not at bottom", and permanently sets
      // shouldAutoScroll(false) for the rest of that response even though
      // the user never touched the scrollbar. 'auto' (instant, no
      // animation) has no window to race against.
      else if (lastMsg.role === 'assistant' && isLoading && shouldAutoScroll) {
        scrollToBottom('auto');
      }
    }
  }, [messages, isLoading, scrollToBottom, shouldAutoScroll]);

  // Real, reproduced bug: the stream loop's final updateAssistantMessage
  // call (the full response, often the single biggest content jump of the
  // whole exchange) and the setIsLoading(false) that follows it both run
  // in the same synchronous stretch with no await between them -- React 18's
  // automatic batching commits them together, so by the time the effect
  // above re-runs for that render, isLoading already reads false and its
  // `isLoading && shouldAutoScroll` guard skips the scroll entirely. The
  // user is left looking at wherever the second-to-last chunk left the
  // viewport, with the tail of the response cut off below the fold.
  //
  // Deliberately unconditional on shouldAutoScroll here (unlike the
  // mid-stream effect above): a long response can trip handleScroll's
  // isAtBottom heuristic into a false negative even once during streaming
  // (a native scroll event landing just outside the programmatic-scroll
  // guard window), permanently latching shouldAutoScroll to false for the
  // rest of that same response with no user-driven scroll ever having
  // happened -- from the user's perspective they never touched the
  // scrollbar, so a response finishing should always end fully visible
  // regardless of what that heuristic concluded mid-flight.
  const wasLoadingRef = useRef(false);
  useEffect(() => {
    if (wasLoadingRef.current && !isLoading) {
      // 'auto': if shouldAutoScroll had already gone false mid-stream, this
      // single call has to cover the ENTIRE remaining distance to the true
      // bottom at once -- exactly the large-jump case 'smooth' is least
      // reliable for (see the mid-stream effect's own comment above). Now
      // that scrollToBottom sets scrollTop directly against scrollHeight
      // (see its own comment in useAutoScroll.ts) rather than using
      // scrollIntoView, one call reliably reaches the true end.
      scrollToBottom('auto');
    }
    wasLoadingRef.current = isLoading;
  }, [isLoading, scrollToBottom]);

  // Belt-and-suspenders for the same class of bug as the isLoading-edge
  // effect above, but for the general case: ANY reflow of the message list
  // that grows its content AFTER the last scrollToBottom call (markdown/
  // syntax-highlighting finishing a render pass a tick later, an image
  // finishing layout, an accordion collapsing into its final size) is
  // invisible to state-based effects entirely, since nothing about
  // `messages`/`isLoading` changes when that happens -- only the DOM does.
  // A MutationObserver reacts to the DOM growth directly instead of trying
  // to enumerate every state transition that might cause it.
  useEffect(() => {
    const container = messageListRef.current;
    // messageListRef is a plain ref, not a callback ref -- it reads null on
    // whatever render runs before messageList itself first mounts (e.g. the
    // landing page, before the first message exists), and a plain ref's
    // identity never changes to re-trigger this effect once it actually
    // mounts. messages.length is in the dependency array purely to force a
    // retry the moment a first message appears and the container becomes real.
    if (!container || typeof MutationObserver === 'undefined') { return; }
    const observer = new MutationObserver(() => {
      if (shouldAutoScrollRef.current) {
        scrollToBottom('auto');
      }
    });
    observer.observe(container, { childList: true, subtree: true, characterData: true });
    return () => observer.disconnect();
  }, [messageListRef, scrollToBottom, messages.length > 0]);



  const handleStop = () => {
    resetAssistantReveal();
    if (abortControllerRef.current) {
      abortControllerRef.current.abort();
      abortControllerRef.current = null;
    }
    setIsLoading(false);

    // Mark the last message as interrupted
    setMessages((prev) => {
      const updated = [...prev];
      if (updated.length > 0) {
        const lastMsg = updated[updated.length - 1];
        if (lastMsg.role === 'assistant') {
          updated[updated.length - 1] = { ...lastMsg, interrupted: true };
        }
      }
      return updated;
    });
  };

  const cancelMessageEdit = () => {
    setEditingMessageIndex(null);
    setInput('');
  };

  const handleSend = async (overrideContent?: string) => {
    const source = overrideContent ?? input;
    if (!source.trim()) {
      return;
    }

    // Set URL param to indicate active chat -- never in panel-preview mode,
    // which renders inside a modal on top of whatever dashboard the user was
    // already looking at (see module.tsx's openModal); rewriting the URL
    // here would rewrite THAT dashboard's URL out from under it.
    if (!isPanelPreview) {
      setSearchParams({ chat: 'true' });
    }

    let content = source;
    const attachments: Array<{ name: string; content: string; type: 'image' | 'text'; mimeType?: string }> = [];

    if (selectedFiles.length > 0) {
      for (const file of selectedFiles) {
        if (file.type === 'image') {
          // Use mimeType from file, or extract from data URL as fallback
          const mimeType = file.mimeType || file.content.match(/^data:([^;]+);base64,/)?.[1] || 'image/jpeg';
          const base64 = file.content.split(',')[1];
          attachments.push({ name: file.name, content: base64, type: 'image', mimeType });
        } else {
          attachments.push({ name: file.name, content: file.content, type: 'text' });
        }
      }
    }

    if (messages.length === 0) {
      setConversationStartedAt(Date.now());
    } else if (
      historyLoadedMessageCountRef.current !== null &&
      messages.length === historyLoadedMessageCountRef.current &&
      !resumedMarker
    ) {
      // First new message since resuming a session loaded from history --
      // see resumedMarker's doc comment. Anchored to the message count at
      // load time (not e.g. messages.length - 1 computed later), since more
      // messages may be appended afterward and the marker must stay put
      // right where the old history ends and the new reply begins.
      setResumedMarker({ atIndex: historyLoadedMessageCountRef.current, timestamp: Date.now() });
    }
    // Pin the badge at a pending 0% the instant any message goes out --
    // first message of a conversation, or a later one after switching
    // agents -- instead of leaving a stale number from a previous
    // message/agent on screen until the real numbers arrive. The point is
    // that the user watches the assistant's own reply be what moves it,
    // never their own message or a leftover value from before.
    setContextUsage((prev) => ({ tokens: 0, maxTokens: prev?.maxTokens ?? 1, pending: true }));

    const isEditing = editingMessageIndex !== null;
    // Real user-reported bug: editing a message used to drop everything that
    // came after it (messages.slice(0, editingMessageIndex)) -- the reply to
    // the OLD content is no longer valid, but the rest of the conversation
    // isn't gone, just stale relative to the new content. Preserve it here
    // (starting with this message's own previous content) instead of
    // discarding it -- see replacedBranch's doc comment (llm.types.ts) and
    // its collapsed, read-only rendering below.
    // Flattened to one level (replacedBranch stripped from each archived
    // entry) -- without this, editing a message BEFORE another one that
    // was already edited would nest its replacedBranch inside this one,
    // growing without bound across repeated rounds of edits in the same
    // conversation (real risk for the local storage budget, see
    // chatHistory.ts's MAX_STORAGE_BYTES). One prior version per message
    // is enough -- no need to stack history-of-history.
    const replacedBranch = isEditing
      ? messages.slice(editingMessageIndex).map((m) => ({ ...m, replacedBranch: undefined }))
      : undefined;
    const userMessage: Message = {
      role: 'user',
      content,
      attachments: attachments.length > 0 ? attachments : undefined,
      edited: isEditing ? true : undefined,
      replacedBranch: replacedBranch && replacedBranch.length > 1 ? replacedBranch : undefined,
    };
    const newMessages = isEditing
      ? [...messages.slice(0, editingMessageIndex), userMessage]
      : [...messages, userMessage];
    setEditingMessageIndex(null);

    resetAssistantReveal();
    setMessages(newMessages);
    setInput('');
    clearFiles();
    setIsLoading(true);

    // Reserve/refresh the session id right away, before the assistant has
    // answered -- previously this only happened after the full response
    // completed, so navigating away mid-response (e.g. to "Manage agents...")
    // on a brand-new conversation had no session id to return to and landed
    // back on a blank chat instead of this one. saveSession() updates the
    // existing record in place when a sessionId is passed, so this is safe
    // to call again below once the real answer is in.
    //
    // Never in panel-preview mode: it's a read-only, single-shot Q&A launched
    // from a dashboard panel's menu (see isPanelPreview above), not an open
    // conversation -- persisting it here meant every "Explain this panel"
    // click silently showed up in the main chat page's "Previous
    // conversations" list, which the user never asked to save anything into.
    let provisionalSessionId: string | undefined;
    if (!isPanelPreview) {
      const provisionalSession = chatHistoryService.saveSession(newMessages, currentSessionId, selectedAgent, activeAgentLabel);
      provisionalSessionId = provisionalSession.id;
      setCurrentSessionId(provisionalSession.id);
      setSearchParams({ chat: 'true', session: provisionalSession.id });
    }

    try {
      const dashboard = await contextService.getCurrentDashboard();
      const dataSources = contextService.getDataSources();
      const context = buildAnalysisContext(dashboard, dataSources, panelContext);

      // Create a placeholder message for the assistant
      const assistantMessage: Message = { role: 'assistant', content: '' };
      setMessages((prev) => [...prev, assistantMessage]);

      const controller = new AbortController();
      abortControllerRef.current = controller;

      // Track final content for saving to history
      let finalContent = '';
      const finalToolExecutions: ToolExecution[] = [];
      setActivityStatus(null);
      setRateLimitCountdown(null);
      // Safety net for a prior turn's worker chip that never got a terminal
      // event (e.g. the stream errored out before dispatch_worker's own
      // 'done'/'error' arrived) -- a new turn should never start with a
      // leftover chip from the last one.
      setActiveWorkers([]);

      const truncatedMessages = truncateMessages(newMessages, 10);
      const chatHistory: ChatHistory[] = truncatedMessages
        .filter((m): m is Message & { role: 'user' | 'assistant' } => m.role === 'user' || m.role === 'assistant')
        .map((m) => ({ role: m.role, content: m.content }));

      const updateAssistantMessage = (fullContent: string, toolExecutions: ToolExecution[]) => {
        scheduleAssistantReveal(fullContent, toolExecutions);
      };

      // Single streaming call to this plugin's own backend -- it owns tool-calling
      // (query_prometheus, query_loki, list_folders, list_dashboards, get_dashboard,
      // list_alerts...), the system prompt, and the LLM endpoint entirely server-side.
      // Launched from a panel's context menu -> the more focused, cheaper
      // explain_panel backend mode; everything else uses the general chat mode.
      const mode = panelContext ? 'explain_panel' : 'chat';
      let streamCompleted = false;
      for await (const chunk of streamChat(mode, content, context, chatHistory, controller.signal, selectedAgent, attachments)) {
        // Real, reproduced bug: handleStop() calls controller.abort() and
        // marks the message "Interrupted", but a chunk already sitting in
        // observableToAsyncIterable's internal queue (client.ts) -- or one
        // that arrives before BackendSrv's own cancellation plumbing
        // catches up -- still gets delivered here and, worse, still gets
        // written into the message via updateAssistantMessage below,
        // sometimes producing a real answer AFTER the message was already
        // shown as interrupted. Checking controller.signal.aborted at the
        // top of every iteration (not just relying on the eventual
        // AbortError to end the loop) makes "stop" actually stop touching
        // this message, regardless of what's still buffered underneath.
        if (controller.signal.aborted) {
          break;
        }
        // The backend announces a tool call right before executing it, not
        // after (see pkg/plugin/pseudo_tool_calls.go's notify callback) --
        // whatever tool call is still pending only actually finishes once
        // something else arrives: more content, the next tool call, or the
        // stream ending. Resolve it here instead of assuming instant success.
        const lastExecution = finalToolExecutions[finalToolExecutions.length - 1];
        if (lastExecution?.status === 'pending' && (chunk.toolCall || chunk.content || chunk.done)) {
          lastExecution.status = 'success';
        }
        if (chunk.toolCall) {
          finalToolExecutions.push({
            name: chunk.toolCall.name,
            arguments: chunk.toolCall.arguments,
            status: 'pending',
            kind: chunk.toolCall.kind,
            label: chunk.toolCall.label,
            statusLabel: chunk.toolCall.statusLabel,
            doneLabel: chunk.toolCall.doneLabel,
            external: chunk.toolCall.external,
          });
        }
        if (chunk.content) {
          finalContent += chunk.content;
        }
        if (chunk.contextTokens && chunk.maxTokens) {
          setContextUsage({ tokens: chunk.contextTokens, maxTokens: chunk.maxTokens });
        }
        if (chunk.status) {
          setActivityStatus(chunk.status);
          const rateLimitMatch = chunk.status.match(/^Rate limited, retrying in (\d+)s\.\.\.$/);
          setRateLimitCountdown(rateLimitMatch ? parseInt(rateLimitMatch[1], 10) : null);
        } else if (chunk.toolCall || chunk.content) {
          // A real tool call or content chunk means whatever background step
          // was announced (e.g. compaction) has finished.
          setActivityStatus(null);
          setRateLimitCountdown(null);
        }
        if (chunk.workerEvent) {
          const event = chunk.workerEvent;
          setActiveWorkers((prev) => {
            const next = prev.filter((w) => w.taskId !== event.taskId);
            next.push(event);
            return next;
          });
          // A finished/failed worker's chip stays visible a moment longer
          // instead of vanishing the instant its last event arrives (see
          // WORKER_CHIP_LINGER_MS) -- only removes THIS worker's chip, and
          // only if it's still the same terminal event (a fresh
          // dispatch of the same worker type reusing a new taskId is
          // unaffected; the same taskId can't be reused since the backend
          // mints one dispatch_worker tool_call ID per dispatch).
          if (event.phase !== 'running') {
            setTimeout(() => {
              setActiveWorkers((prev) => prev.filter((w) => w.taskId !== event.taskId));
            }, WORKER_CHIP_LINGER_MS);
          }
        }
        updateAssistantMessage(finalContent, finalToolExecutions);
        if (chunk.done) {
          streamCompleted = true;
          setActivityStatus(null);
          setRateLimitCountdown(null);
          break;
        }
      }

      // A user-initiated stop (see the aborted check inside the loop above)
      // is not the same failure as the one below -- it already has its own
      // correct handling (handleStop marks the message interrupted), so
      // exit quietly here instead of falling through to "stream ended
      // without completion" and overwriting that with a scary error message.
      if (controller.signal.aborted) {
        return;
      }

      // A backend-side error (e.g. the LLM endpoint rate-limiting mid tool
      // loop) closes the underlying connection abruptly instead of sending a
      // terminal chunk -- the browser reads that as a normal stream end, not
      // a network error, so the for-await loop above exits quietly with
      // whatever partial content/tool calls arrived (often none) and no
      // exception ever reaches the catch block below. Treat "the stream
      // ended without a done chunk" as a failure explicitly, so the user
      // always sees why nothing more came back instead of a silently
      // stalled conversation.
      if (!streamCompleted) {
        throw new Error('Stream ended without completion signal');
      }
      await waitForAssistantRevealComplete();

      // Save chat to history after completion.
      // Read latestMessagesRef (not setMessages) to get the final state without
      // running side-effects inside a state updater (which React may invoke more
      // than once under StrictMode / concurrent rendering).
      const lastMsg = latestMessagesRef.current[latestMessagesRef.current.length - 1];
      const finalAssistantMessage: Message = {
        ...lastMsg,
        content: finalContent,
        toolExecutions: finalToolExecutions.length > 0 ? finalToolExecutions : lastMsg?.toolExecutions,
      };
      const finalMessages = [...newMessages, finalAssistantMessage];
      if (!isPanelPreview) {
        const savedSession = chatHistoryService.saveSession(finalMessages, provisionalSessionId, selectedAgent, activeAgentLabel);
        setCurrentSessionId(savedSession.id);
        setSearchParams({ chat: 'true', session: savedSession.id });
      }
    } catch (error: any) {
      resetAssistantReveal();
      if (error.name === 'AbortError') {
        return;
      }
      const rawErrorMessage = String(error?.message || '');
      const attemptedImageAttachment = attachments.some((attachment) => attachment.type === 'image');
      const imageUnsupported = rawErrorMessage.toLowerCase().includes('model does not support image attachments') ||
        rawErrorMessage.toLowerCase().includes('multimodal data provided');
      const imageProcessingFailed = attemptedImageAttachment ||
        imageUnsupported ||
        rawErrorMessage.toLowerCase().includes('could not process this image attachment');
      // Shown to end users as a chat message, so unknown failures still avoid
      // technical language. Known attachment/model mismatch errors stay
      // explicit because the user can fix them by removing the image or
      // switching to a vision-capable model.
      const errorMessage = imageUnsupported
        ? 'The current model does not support image attachments. Use a vision-capable model or remove the image and send the message again.'
        : imageProcessingFailed
        ? 'The current model/provider could not process this image attachment.'
        : 'This service is currently down. It should be back shortly.';
      const noticeTitle = imageUnsupported
        ? 'Image attachments not supported'
        : imageProcessingFailed
        ? 'Image attachment could not be processed'
        : 'Assistant temporarily unavailable';

      // Create the error assistant message
      const errorAssistantMessage: Message = {
        role: 'assistant',
        content: errorMessage,
        isUnavailableNotice: true,
        noticeTitle,
      };

      // Replace the placeholder assistant message with the error
      // We know we added a placeholder, so always replace the last assistant message
      setMessages((prev) => {
        const updatedMessages = [...prev];
        const lastMsg = updatedMessages.length > 0 ? updatedMessages[updatedMessages.length - 1] : null;

        // Replace the last assistant message (our placeholder) with the error
        if (lastMsg && lastMsg.role === 'assistant') {
          updatedMessages[updatedMessages.length - 1] = { ...lastMsg, content: errorMessage, isUnavailableNotice: true, noticeTitle };
          return updatedMessages;
        }

        // Fallback: append if somehow no assistant placeholder exists
        return [...prev, errorAssistantMessage];
      });

      // Save the conversation with the error message
      const finalMessages = [...newMessages, errorAssistantMessage];
      if (!isPanelPreview) {
        const savedSession = chatHistoryService.saveSession(finalMessages, provisionalSessionId, selectedAgent, activeAgentLabel);
        setCurrentSessionId(savedSession.id);
        setSearchParams({ chat: 'true', session: savedSession.id });
      }
    } finally {
      setIsLoading(false);
    }
  };

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Escape' && editingMessageIndex !== null) {
      e.preventDefault();
      cancelMessageEdit();
      return;
    }

    // Shift+Enter has a real native default (the browser inserts the
    // newline itself, we just avoid preventing it below) -- Alt+Enter does
    // NOT have any such default in a plain <textarea>, so it has to be
    // inserted manually at the caret, same as ChatGPT's own input does.
    if (e.key === 'Enter' && e.altKey) {
      e.preventDefault();
      const target = e.currentTarget;
      const start = target.selectionStart;
      const end = target.selectionEnd;
      const nextValue = input.slice(0, start) + '\n' + input.slice(end);
      setInput(nextValue);
      requestAnimationFrame(() => {
        target.selectionStart = target.selectionEnd = start + 1;
        // Native Enter scrolls the textarea to follow the caret on its
        // own; manually inserting the newline and moving selectionStart/
        // selectionEnd above does NOT -- real bug: once the box's fixed
        // rows={2} height fills up, the caret moves below the visible
        // area and stays hidden until the user presses the down arrow
        // (a real, unintercepted key event, which DOES scroll). Scrolling
        // to scrollHeight here covers the normal case (typing continues
        // at/near the end of the text) the same way it would have
        // happened natively.
        target.scrollTop = target.scrollHeight;
      });
      return;
    }
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      if (llmReady) {
        handleSend();
      }
    }
  };

  const handleReset = () => {
    // Abort any ongoing request to prevent state updates after navigation
    if (abortControllerRef.current) {
      abortControllerRef.current.abort();
      abortControllerRef.current = null;
    }
    setIsLoading(false);

    // Check if we should return to history
    const state = location.state as { returnTo?: string } | null;
    if (state?.returnTo === 'history') {
      navigate('history');
      return;
    }

    setMessages([]);
    setConversationStartedAt(null);
    historyLoadedMessageCountRef.current = null;
    setResumedMarker(null);
    setSearchParams({});
    setInput('');
    clearFiles();
    setCurrentSessionId(undefined);
  };

  const handleDictation = () => {
    if ('webkitSpeechRecognition' in window || 'SpeechRecognition' in window) {
      const SpeechRecognition = (window as any).webkitSpeechRecognition || (window as any).SpeechRecognition;
      const recognition = new SpeechRecognition();
      recognition.continuous = false;
      recognition.interimResults = false;

      recognition.onstart = () => {
        setIsListening(true);
      };

      recognition.onend = () => {
        setIsListening(false);
      };

      recognition.onresult = (event: any) => {
        const transcript = event.results[0][0].transcript;
        setInput((prev) => prev + (prev ? ' ' : '') + transcript);
      };

      recognition.start();
    } else {
      notifyWarning('Speech recognition is not supported in this browser.');
    }
  };

  // Only usable agents are offered -- an unconfigured specialist slot
  // (no saved context yet) is indistinguishable from Default server-side
  // (specializationBlock returns nothing for it), so selecting it would
  // silently do nothing. Instead of showing it with a caveat, it simply
  // isn't in the list until it has real context to run on; "Manage
  // agents..." stays visible below so it's still discoverable.
  const agentOptions = agents.length
    ? agents.filter((a) => a.id === 'generic' || a.hasContext)
    : [{ id: 'generic', label: 'Default', description: 'General-purpose Grafana assistant.', hasContext: true }];
  const activeAgentLabel = agentOptions.find((a) => a.id === selectedAgent)?.label ?? 'Default';

  const onSelectAgent = (a: AgentInfo) => {
    setSelectedAgent(a.id);
    // Otherwise the badge keeps showing whatever the PREVIOUS agent's real
    // usage was, which reads as "this is the new agent's context usage" --
    // it isn't known until that agent actually answers something.
    setContextUsage((prev) => (prev ? { tokens: 0, maxTokens: prev.maxTokens, pending: true } : prev));
  };

  // Attach + Dictate used to be two separate icon buttons; folded into one
  // "+" button whose menu offers both, so the input row shows one icon
  // instead of two. `locked` covers both call sites' different reasons a
  // click should be a no-op (landing: inputLocked; active chat: !llmReady).
  const attachMenu = (locked: boolean) => (
    <Menu>
      <MenuItem
        label="Attach file"
        icon="attach"
        disabled={locked}
        onClick={() => !locked && fileInputRef.current?.click()}
      />
      <MenuItem
        label={isListening ? 'Stop dictation' : 'Dictate'}
        icon="record-audio"
        disabled={locked}
        onClick={() => !locked && handleDictation()}
      />
    </Menu>
  );

  const agentMenu = (
    <Menu>
      {agentOptions.map((a) => (
        <MenuItem
          key={a.id}
          label={a.label}
          description={a.description}
          active={a.id === selectedAgent}
          onClick={() => onSelectAgent(a)}
        />
      ))}
      <Divider spacing={0.5} />
      <MenuItem
        label="Manage agents..."
        icon="cog"
        onClick={() => {
          // Preserve the current conversation so "Back to chat" on the
          // Agents page returns here instead of a fresh, empty chat --
          // matching how a session is normally addressed (chat=true&session=id).
          const returnTo = currentSessionId
            ? `/a/${PLUGIN_ID}/chat?chat=true&session=${encodeURIComponent(currentSessionId)}`
            : `/a/${PLUGIN_ID}/chat`;
          locationService.push(`/a/${PLUGIN_ID}/agents?return=${encodeURIComponent(returnTo)}`);
        }}
      />
    </Menu>
  );

  return (
    <div
      ref={standaloneContainerRef}
      className={styles.container}
      // Grafana's own modal wrapper (outside this component, not stylable
      // from a plugin's `body`) has an opaque background of its own --
      // making OUR container transparent only ever revealed ITS solid
      // backdrop, not the dashboard behind the whole dialog. True
      // transparency isn't achievable here; sizing to content still is.
      //
      // Standalone full-page mode (no onDismiss, not a panel preview) needs
      // its own explicit viewport-relative height: confirmed live (DOM
      // inspection) that none of Grafana's own page-chrome ancestors
      // (.main-view, .page-content, .page-panes, .page-container) set a
      // concrete height either -- they all just auto-size to content, same
      // as our own `container`'s `height: 100%` (which only resolves
      // against a real value if some ancestor has one). Without a fixed
      // height here, `messageList`'s own `overflow-y: auto` never gets a
      // bounded box to actually scroll inside of -- the whole page grows
      // and the BROWSER WINDOW scrolls instead, taking the sticky chatHeader
      // (back button + context-usage badge + history button) off-screen
      // with it in a long conversation instead of it staying pinned. 100dvh
      // (not 100%) plus overflow:hidden forces a real bounded box here
      // regardless of what the ancestor chain resolves to, which is what
      // makes messageList's internal scroll (and chatHeader's sticky
      // positioning within it) actually take effect. Modal mode (onDismiss
      // set) is left alone -- Grafana's own <Modal> already provides a
      // correctly bounded, scrollable box there.
      style={isPanelPreview ? {
        height: 'auto',
        maxHeight: '80vh',
      } : !onDismiss ? {
        height: standaloneHeight,
        overflow: 'hidden',
      } : undefined}
    >
      {isPanelPreview && lightModeForDefaultAgent && selectedAgent === 'generic' && (
        <div style={{ position: 'absolute', top: '6px', right: '12px', fontSize: '10px', opacity: 0.15, textTransform: 'uppercase', pointerEvents: 'none', userSelect: 'none', zIndex: 1 }}>Light Mode</div>
      )}
      {messages.length === 0 ? (
        <div className={styles.landingContainer}>
          <button
            type="button"
            className={styles.historyButtonDiscreet}
            data-testid="landing-history-button"
            title="Previous conversations"
            aria-label="Previous conversations"
            onClick={() => {
              setHistorySessions(chatHistoryService.getAllSessions());
              setShowHistory(true);
            }}
          >
            <Icon name="history" size="lg" />
          </button>
          <button
            type="button"
            className={styles.settingsButton}
            data-testid="settings-button"
            title="Plugin configuration"
            aria-label="Plugin configuration"
            onClick={() => { window.location.href = `/plugins/${PLUGIN_ID}?page=configuration`; }}
          >
            <Icon name="cog" size="lg" />
          </button>
          <div className={styles.landingContent}>

            <div className={styles.logo}>
              <img
                src={`public/plugins/${PLUGIN_ID}/img/logo-pixo-large.png`}
                alt="Agent AI"
                className={styles.logoImage}
                draggable={false}
                onContextMenu={(e) => e.preventDefault()}
              />
              {logoShinePlayed && (
                <span
                  className={styles.logoShine}
                  style={{ WebkitMaskImage: `url(public/plugins/${PLUGIN_ID}/img/logo-pixo-large.png)`, maskImage: `url(public/plugins/${PLUGIN_ID}/img/logo-pixo-large.png)` }}
                />
              )}
            </div>
            <h1 className={styles.title} data-testid="landing-title">{greetingMessage}</h1>

            <h2 className={styles.subtitle}>{getLandingText(responseLanguage).subtitle}</h2>

            <div className={styles.landingInputWrapper}>
              {lightModeForDefaultAgent && selectedAgent === 'generic' && (
                <div style={{ position: 'absolute', top: '6px', right: '12px', fontSize: '10px', opacity: 0.15, textTransform: 'uppercase', pointerEvents: 'none', userSelect: 'none' }}>Light Mode</div>
              )}

              {/* Not configured -- shown to end users, so no technical language
                  or link to admin config; reads as a temporary outage, not
                  something the viewer is expected to go fix. */}
              {!settingsLoading && !llmConfigured && (
                <Alert
                  title="Assistant temporarily unavailable"
                  severity="info"
                  style={{ marginBottom: '4px' }}
                >
                  This service is currently down. It should be back shortly.
                </Alert>
              )}

              {/* LLM config unhealthy warning banner */}
              {!settingsLoading && llmConfigured && !llmHealthy && (
                <Alert
                  title="LLM config unavailable"
                  severity="error"
                  style={{ marginBottom: '4px' }}
                >
                  This assistant's backend isn't responding. Check the{' '}
                  <a href={`/plugins/${PLUGIN_ID}?page=configuration`} style={{ textDecoration: 'underline' }}>
                    assistant configuration
                  </a>{' '}
                  (endpoint URL, model, timeout).
                </Alert>
              )}

              {selectedFiles.length > 0 && (
                <div className={styles.filePreviewList}>
                  {selectedFiles.map((file, index) => (
                    <FilePreview
                      key={index}
                      file={file}
                      onRemove={() => removeFile(index)}
                      onExpand={() => setPreviewAttachment(file)}
                    />
                  ))}
                </div>
              )}

              <TextArea
                id="agent-ai-landing-chat-input"
                name="agent-ai-landing-chat-input"
                data-testid="chat-input"
                value={input}
                onChange={(e) => setInput(e.currentTarget.value)}
                onKeyDown={handleKeyDown}
                placeholder={!llmReady ? 'Set up the LLM config to start chatting...' : quickPromptEditing ? 'Finish editing the prompt below first...' : rollingPlaceholder}
                rows={2}
                disabled={inputLocked}
                style={{ resize: 'none', flex: 1, border: 'none', outline: 'transparent' }}
                className={`${styles.landingTextArea} ${!llmReady ? styles.placeholderItalic : ''}`}
              />
              <div className={styles.landingInputFooter}>
                <div className={styles.landingActions}>
                  <input
                    type="file"
                    ref={fileInputRef}
                    style={{ display: 'none' }}
                    onChange={handleFileSelect}
                    accept={ATTACHMENT_ACCEPT}
                    multiple
                    data-testid="landing-file-input"
                    disabled={inputLocked}
                  />
                  <Dropdown overlay={attachMenu(inputLocked)}>
                    <button
                      type="button"
                      className={styles.iconButton}
                      disabled={inputLocked}
                      aria-label="Attach file or dictate"
                      title={!llmReady ? 'LLM config not configured' : quickPromptEditing ? 'Finish editing the prompt below first' : 'Attach file or dictate'}
                      style={inputLocked ? { opacity: 0.5, cursor: 'not-allowed' } : undefined}
                    >
                      <PlusIcon />
                    </button>
                  </Dropdown>
                  <Dropdown overlay={agentMenu}>
                    <button
                      type="button"
                      disabled={quickPromptEditing}
                      className={`${styles.agentInlineButton} ${selectedAgent !== 'generic' ? styles.agentInlineButtonActive : ''}`}
                      style={selectedAgent !== 'generic' ? {
                        '--agent-color': colorForAgent(selectedAgent),
                        '--agent-color-bg': `${colorForAgent(selectedAgent)}14`,
                      } as React.CSSProperties : (quickPromptEditing ? { opacity: 0.5, cursor: 'not-allowed' } : undefined)}
                      data-testid="agent-selector"
                      title={quickPromptEditing ? 'Finish editing the prompt below first' : `Agent: ${activeAgentLabel}`}
                      aria-label={`Agent: ${activeAgentLabel}`}
                    >
                      <Icon name="users-alt" size="sm" />
                    </button>
                  </Dropdown>
                  <button onClick={() => handleSend()} disabled={isLoading || inputLocked} className={styles.landingSendButton} aria-label="Send message" data-testid="send-message-button" title={!llmReady ? 'LLM config not configured' : quickPromptEditing ? 'Finish editing the prompt below first' : 'Send message'}>
                    <svg viewBox="0 0 24 24" width="16" height="16" stroke="white" strokeWidth="2" fill="none" strokeLinecap="round" strokeLinejoin="round">
                      <line x1="12" y1="19" x2="12" y2="5"></line>
                      <polyline points="5 12 12 5 19 12"></polyline>
                    </svg>
                  </button>
                </div>
              </div>
            </div>

            {auditLogFullContent && (
              <div
                data-testid="audit-notice"
                style={{ fontSize: '11px', opacity: 0.55, textAlign: 'center', marginTop: '2px' }}
              >
                Message content may be logged for audit purposes.
              </div>
            )}

            <div className={styles.footerLinks}>
              {quickPrompts.map((qp) => {
                const isEditing = editingPromptId === qp.id;
                if (isEditing) {
                  return (
                    <div key={qp.id} className={`${styles.footerLink} ${styles.footerLinkEditing}`} data-testid={`quick-prompt-edit-${qp.id}`}>
                      <input
                        className={styles.quickPromptEditTitle}
                        value={editPromptTitle}
                        onChange={(e) => setEditPromptTitle(e.currentTarget.value)}
                        placeholder="Title"
                      />
                      <textarea
                        className={styles.quickPromptEditContent}
                        value={editPromptContent}
                        onChange={(e) => setEditPromptContent(e.currentTarget.value)}
                        placeholder="Content (what will be used as the question)"
                        rows={3}
                      />
                      <div className={styles.quickPromptEditActions}>
                        <button
                          className={styles.messageActionButton}
                          title="Save"
                          onClick={(e) => {
                            e.stopPropagation();
                            setQuickPrompts(saveQuickPrompt(qp.id, editPromptTitle.trim() || qp.title, editPromptContent.trim() || qp.content, responseLanguage));
                            setEditingPromptId(null);
                          }}
                        >
                          <Icon name="check" size="sm" />
                        </button>
                        <button
                          className={styles.messageActionButton}
                          title="Cancel"
                          onClick={(e) => {
                            e.stopPropagation();
                            setEditingPromptId(null);
                          }}
                        >
                          <Icon name="times" size="sm" />
                        </button>
                      </div>
                    </div>
                  );
                }
                return (
                  <div
                    key={qp.id}
                    className={styles.footerLink}
                    onClick={() => llmReady && !quickPromptEditing && handleSend(qp.content)}
                    title={!llmReady ? 'LLM config not configured' : quickPromptEditing ? 'Finish editing the other prompt first' : undefined}
                    style={!llmReady || quickPromptEditing ? { opacity: 0.5, cursor: 'not-allowed' } : undefined}
                    data-testid={`quick-prompt-${qp.id}`}
                  >
                    <div className={styles.footerIconBadge}>
                      {QUICK_PROMPT_ICONS[qp.id]}
                    </div>
                    <div className={styles.quickPromptTextArea}>
                      <div className={styles.linkTitle}>{qp.title}</div>
                      <div className={styles.linkDesc}>{qp.content}</div>
                    </div>
                    <Icon
                      name="pen"
                      size="xs"
                      className={styles.quickPromptEditIcon}
                      onClick={(e) => {
                        e.stopPropagation();
                        if (quickPromptEditing) {
                          return;
                        }
                        setEditPromptTitle(qp.title);
                        setEditPromptContent(qp.content);
                        setEditingPromptId(qp.id);
                      }}
                    />
                  </div>
                );
              })}
            </div>
          </div>
        </div>
      ) : (
        <>
          {/* Chat header hidden in modal mode — Grafana's title bar + our modalHeader serve the same purpose */}
          {!onDismiss && (
          <div className={styles.chatHeader} data-testid="chat-header">
            <div className={styles.headerLeft}>
              <button
                type="button"
                className={styles.historyButtonDiscreetInline}
                title="Back"
                aria-label="Back"
                onClick={handleReset}
                data-testid="back-button"
              >
                <Icon name="arrow-left" size="xl" />
              </button>
            </div>
            <div className={styles.headerRight}>
              {auditLogFullContent && (
                <span
                  data-testid="audit-notice-header"
                  title="Message content may be logged for audit purposes."
                  style={{ fontSize: '11px', opacity: 0.55 }}
                >
                  Monitored
                </span>
              )}
              {contextUsage && (
                <div
                  className={styles.contextUsageBadge}
                  title={contextUsage.pending ? 'Waiting for the assistant\'s response...' : `Context used: ${contextUsage.tokens} / ${contextUsage.maxTokens} tokens`}
                  data-testid="context-usage-badge"
                >
                  {contextUsage.pending ? 0 : Math.min(100, Math.round((contextUsage.tokens / contextUsage.maxTokens) * 100))}% context
                  {!contextUsage.pending && selectedAgent !== 'generic' && ` (${contextUsage.tokens}/${contextUsage.maxTokens})`}
                </div>
              )}
              <button
                type="button"
                className={styles.historyButtonDiscreetInline}
                title="Previous conversations"
                aria-label="Previous conversations"
                onClick={() => {
                  setHistorySessions(chatHistoryService.getAllSessions());
                  setShowHistory(true);
                }}
                data-testid="history-button"
              >
                <Icon name="history" size="lg" />
              </button>
            </div>
          </div>
          )}
          <div
            className={`${styles.messageList} ${isPanelPreview ? styles.panelPreviewMessageList : ''}`}
            ref={messageListRef}
            onScroll={handleScroll}
            style={!isPanelPreview ? { paddingBottom: inputAreaBottomPadding } : undefined}
          >
            {conversationStartedAt && (
              <div className={styles.conversationTimestamp} data-testid="conversation-timestamp">
                {formatConversationTimestamp(conversationStartedAt)}
              </div>
            )}
            {messages.map((msg, index) => {
              const isLastMessage = index === messages.length - 1;
              const isStreaming = isLastMessage && isLoading && msg.role === 'assistant';
              const messageCopied = copiedMessageIndex === index;
              // See resumedMarker's doc comment -- a second timestamp
              // divider, shown once right before the first new message
              // sent after resuming an old session from history.
              const resumedDivider = resumedMarker && index === resumedMarker.atIndex ? (
                <div className={styles.conversationTimestamp} data-testid="conversation-resumed-timestamp">
                  {formatConversationTimestamp(resumedMarker.timestamp)}
                </div>
              ) : null;

              // Parse thinking content
              let thinkingContent = null;
              let mainContent = msg.content;
              let isThinkingStreaming = false;

              const trimmedContent = msg.content.trimStart();
              if (msg.role === 'assistant' && trimmedContent.startsWith('<think>')) {
                const thinkEndIndex = msg.content.indexOf('</think>');
                if (thinkEndIndex !== -1) {
                  // Find where <think> actually starts in the original string (to preserve leading whitespace if needed, though usually we ignore it)
                  const thinkStartIndex = msg.content.indexOf('<think>');
                  thinkingContent = msg.content.substring(thinkStartIndex + 7, thinkEndIndex);
                  mainContent = msg.content.substring(thinkEndIndex + 8);
                  isThinkingStreaming = false; // Thinking is complete
                } else {
                  // Streaming case: </think> not found yet, treat all as thinking
                  const thinkStartIndex = msg.content.indexOf('<think>');
                  thinkingContent = msg.content.substring(thinkStartIndex + 7);
                  mainContent = '';
                  isThinkingStreaming = isStreaming; // Still streaming thinking
                }
              }
              const hasVisibleActivitySteps = msg.role === 'assistant' && (thinkingContent !== null || (msg.toolExecutions && msg.toolExecutions.length > 0));
              const activeWaitingMessages = hasVisibleActivitySteps ? waitingStepMessages : waitingResponseMessages;
              const showWaitingMessage = waitingResponseTick >= WAITING_RESPONSE_MESSAGE_DELAY_TICKS;
              const waitingMessage = showWaitingMessage
                ? activeWaitingMessages[
                    Math.floor((waitingResponseTick - WAITING_RESPONSE_MESSAGE_DELAY_TICKS) / WAITING_RESPONSE_MESSAGE_STEP_TICKS) % activeWaitingMessages.length
                  ]
                : '';

              // Real, reproduced bug: this used to copy msg.content verbatim
              // -- for a model that emits a raw <think>...</think> block
              // (see mainContent's own parsing above), that put the whole
              // internal reasoning trace on the clipboard alongside the
              // real answer, not just what's actually shown on screen.
              // mainContent is exactly what's rendered as the visible
              // answer (thinkingContent is shown separately, inside its own
              // collapsible block, and copying that isn't this button's
              // job) -- trimmed since stripping the </think> tag can leave
              // leading whitespace/newlines that only ever exist to
              // separate it from the reasoning block, not real content.
              const handleCopyMessage = async () => {
                await navigator.clipboard.writeText(mainContent.trim());
                setCopiedMessageIndex(index);
                setTimeout(() => setCopiedMessageIndex(null), 2000);
              };

              if (msg.isUnavailableNotice) {
                const previousMessage = messages[index - 1];
                const followsImageMessage = previousMessage?.role === 'user' && previousMessage.attachments?.some((attachment) => attachment.type === 'image');
                const legacyGenericUnavailable = msg.content === 'This service is currently down. It should be back shortly.';
                const alertTitle = legacyGenericUnavailable && followsImageMessage
                  ? 'Image attachment could not be processed'
                  : msg.noticeTitle ?? 'Assistant temporarily unavailable';
                const alertContent = legacyGenericUnavailable && followsImageMessage
                  ? 'The current model/provider could not process this image attachment.'
                  : msg.content;
                return (
                  <React.Fragment key={index}>
                    {resumedDivider}
                    <div className={`${styles.messageWrapper} ${styles.assistantMessageWrapper}`}>
                      <Alert title={alertTitle} severity="info">
                        {alertContent}
                      </Alert>
                    </div>
                  </React.Fragment>
                );
              }

              return (
                <React.Fragment key={index}>
                {resumedDivider}
                <div className={`${styles.messageWrapper} ${msg.role === 'user' ? styles.userMessageWrapper : styles.assistantMessageWrapper} `}>
                  <div className={`${styles.message} ${msg.role === 'user' ? (isPanelPreview ? styles.userMessagePreview : styles.userMessage) : styles.assistantMessage} `}>
                    <div className={styles.messageContent}>
                      {(thinkingContent !== null || (msg.role === 'assistant' && msg.toolExecutions && msg.toolExecutions.length > 0)) && (
                        <ActivityAccordion
                          stepCount={(thinkingContent !== null ? 1 : 0) + (msg.toolExecutions?.length ?? 0)}
                          issueCount={msg.toolExecutions?.filter((exec) => exec.status === 'error').length ?? 0}
                          isRunning={isStreaming}
                        >
                          {thinkingContent !== null && (
                            <ThinkingBlock
                              content={thinkingContent}
                              isStreaming={isThinkingStreaming}
                              startTime={isThinkingStreaming ? thinkingStartTimeRef.current : null}
                            />
                          )}
                          {msg.role === 'assistant' && msg.toolExecutions && msg.toolExecutions.length > 0 && (
                            <ToolCallContainer toolExecutions={msg.toolExecutions} theme={theme} isStreaming={isStreaming} />
                          )}
                        </ActivityAccordion>
                      )}
                      {mainContent && (
                        <MemoizedMarkdown
                          content={mainContent}
                          theme={theme}
                          onRender={scrollToBottom}
                          isStreaming={isStreaming}
                        />
                      )}
                      {isStreaming && thinkingContent === null && !mainContent && (
                        <div className={styles.thinkingIndicator}>
                          <div className={styles.thinkingDots} style={isPanelPreview ? { gap: '6px' } : undefined}>
                            {(isPanelPreview ? PREVIEW_THINKING_DOT_DELAYS : DEFAULT_THINKING_DOT_DELAYS).map((delay) => (
                              <span
                                key={delay}
                                className={styles.thinkingDot}
                                // Inline background so it reliably overrides the
                                // base gradient regardless of emotion class order.
                                style={isPanelPreview ? { animationDelay: delay, background: theme.colors.text.secondary } : { animationDelay: delay }}
                              />
                            ))}
                          </div>
                          {showWaitingMessage && (
                            <span className={styles.thinkingStatusText}>{waitingMessage}</span>
                          )}
                        </div>
                      )}
                    </div>
                    {/* Display attachments for user messages */}
                    {msg.role === 'user' && msg.attachments && msg.attachments.length > 0 && (
                      <div className={styles.filePreviewList}>
                        {msg.attachments.map((attachment: { name: string; content: string; type: 'image' | 'text'; mimeType?: string }, attIndex: number) => (
                          <FilePreview
                            key={attIndex}
                            file={attachment}
                            onExpand={() => setPreviewAttachment(attachment)}
                          />
                        ))}
                      </div>
                    )}
                  </div>
                  {
                    msg.role === 'assistant' && !isStreaming && (
                      <div className={styles.messageActions}>
                        {msg.interrupted ? (
                          <div style={{ display: 'flex', alignItems: 'center', gap: '4px', color: theme.colors.warning.text, fontSize: '11px' }}>
                            <Icon name="exclamation-circle" size="sm" />
                            <span>Interrupted</span>
                          </div>
                        ) : (
                          <button className={styles.messageActionButton} onClick={handleCopyMessage} title="Copy message">
                            <Icon name={messageCopied ? 'check' : 'copy'} size="xs" />
                          </button>
                        )}
                      </div>
                    )
                  }
                  {msg.role === 'user' && (
                    <div className={styles.messageActions}>
                      {msg.edited && (
                        <span className={styles.editedLabel} data-testid="edited-label">Edited</span>
                      )}
                      {!isPanelPreview && (
                        <button className={styles.messageActionButton} onClick={() => {
                          setEditingMessageIndex(index);
                          setInput(msg.content);
                          // Small timeout to allow state update before focus
                          setTimeout(() => {
                            const textarea = document.querySelector('textarea');
                            if (textarea) {
                              textarea.focus();
                            }
                          }, 0);
                        }} title="Edit message">
                          <Icon name="pen" size="xs" />
                        </button>
                      )}
                      <button className={styles.messageActionButton} onClick={handleCopyMessage} title="Copy message">
                        <Icon name={messageCopied ? 'check' : 'copy'} size="xs" />
                      </button>
                    </div>
                  )}
                  {/* Editing this message used to silently discard everything that came
                      after it (see replacedBranch's doc comment, llm.types.ts) -- this
                      preserves and surfaces it instead, collapsed by default since it's
                      historical/read-only, not part of the live conversation anymore. */}
                  {msg.role === 'user' && msg.replacedBranch && (
                    <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-end' }}>
                      <button
                        className={styles.replacedBranchToggle}
                        data-testid="replaced-branch-toggle"
                        onClick={() => setExpandedBranches((prev) => {
                          const next = new Set(prev);
                          if (next.has(index)) { next.delete(index); } else { next.add(index); }
                          return next;
                        })}
                      >
                        <span>
                          {`Previous conversation · ${msg.replacedBranch.length} message${msg.replacedBranch.length === 1 ? '' : 's'}`}
                        </span>
                        <Icon name={expandedBranches.has(index) ? 'angle-down' : 'angle-right'} size="sm" />
                      </button>
                      {expandedBranches.has(index) && (
                        <div className={styles.replacedBranchPanel} data-testid="replaced-branch-panel">
                          {msg.replacedBranch.map((old, oldIndex) => (
                            <div key={oldIndex} className={styles.replacedBranchEntry}>
                              <strong>{old.role === 'user' ? 'You' : 'Assistant'}:</strong> {old.content || '(empty)'}
                            </div>
                          ))}
                        </div>
                      )}
                    </div>
                  )}
                </div >
                </React.Fragment>
              );
            })}
            {/* The panel preview is a single-shot Q&A, not an open
                conversation -- a transient "rate limited, retrying..."
                banner there reads as an immediate failure before any real
                content has appeared. If it ultimately can't get a response,
                the isUnavailableNotice fallback above already covers that
                gracefully; there's nothing rate-limit-specific worth telling
                the user in this mode. */}
            {activityStatus && !isPanelPreview && (
              <div className={styles.activityStatusBanner}>
                <div className={styles.activityStatusBadge} data-testid="activity-status-badge">
                  {activityStatus}
                </div>
              </div>
            )}
            {!isPanelPreview && <WorkerActivityTracker workers={activeWorkers} />}
            {/* Real, reproduced bug: this sentinel used to sit AFTER the
                entire input area, a sibling of messageList rather than its
                last child -- scrollToBottom's scrollIntoView() on it had no
                scrollable ancestor to actually move (messageList, the one
                true scroll container, isn't even an ancestor of a sibling
                element). It "worked" before only because the whole PAGE
                could still scroll as a fallback; now that the container fix
                above makes messageList the sole scroll region, it must
                physically be the last element inside messageList for
                scrollIntoView to have any effect at all. */}
            <div ref={messagesEndRef} />
          </div >
          <div>
            {showScrollButton && (
              <button
                className={styles.scrollButton}
                // User-reported: this floated at a hardcoded bottom:120px,
                // tuned for when inputArea was a normal flex sibling of a
                // roughly-fixed height -- now that inputArea is a
                // translucent overlay whose real height varies (see
                // inputAreaBottomPadding's own doc comment) and floats
                // independently, a fixed 120px could sit behind/inside it
                // instead of clearly above it. Anchoring to the same
                // measured height keeps it just above the real input box
                // regardless of how tall it currently is.
                style={!isPanelPreview ? { bottom: inputAreaBottomPadding + 16 } : undefined}
                onClick={scrollDownPage}
                title="Scroll to bottom"
              >
                <Icon name="arrow-down" size="xl" />
              </button>
            )}
          </div>
          {!isPanelPreview && (
          <div className={styles.inputArea} ref={inputAreaRefCallback}>

            {/* Not configured -- end-user-facing, reads as a temporary outage. */}
            {!settingsLoading && !llmConfigured && (
              <Alert
                title="Assistant temporarily unavailable"
                severity="info"
                style={{ marginBottom: '8px' }}
              >
                This service is currently down. It should be back shortly.
              </Alert>
            )}

            {/* LLM config unhealthy warning banner */}
            {!settingsLoading && llmConfigured && !llmHealthy && (
              <Alert
                title="LLM config unavailable"
                severity="error"
                style={{ marginBottom: '8px' }}
              >
                This assistant's backend isn't responding. Check the{' '}
                <a href={`/plugins/${PLUGIN_ID}?page=configuration`} style={{ textDecoration: 'underline' }}>
                  assistant configuration
                </a>{' '}
                (endpoint URL, model, timeout).
              </Alert>
            )}
            {selectedFiles.length > 0 && (
              <div className={styles.filePreviewList}>
                {selectedFiles.map((file, index) => (
                  <FilePreview
                    key={index}
                    file={file}
                    onRemove={() => removeFile(index)}
                    onExpand={() => setPreviewAttachment(file)}
                  />
                ))}
              </div>
            )}<div className={styles.disclaimer}>
              Agent AI can make mistakes. Check important info before acting on it.
            </div>
            <div className={`${styles.inputWrapper} ${isLoading ? styles.inputWrapperLoading : ''} `}>
              {lightModeForDefaultAgent && selectedAgent === 'generic' && (
                <div style={{ position: 'absolute', top: '6px', right: '12px', fontSize: '10px', opacity: 0.15, textTransform: 'uppercase', pointerEvents: 'none', userSelect: 'none' }}>Light Mode</div>
              )}
              {editingMessageIndex !== null && (
                <div className={styles.editingMessageBanner} data-testid="editing-message-banner">
                  <div className={styles.editingMessageInfo}>
                    <Icon name="pen" size="sm" />
                    <span>Editing message</span>
                  </div>
                  <button
                    type="button"
                    className={styles.editingMessageCancel}
                    onClick={cancelMessageEdit}
                    data-testid="cancel-message-edit"
                    title="Cancel edit and continue normal chat"
                  >
                    Cancel
                  </button>
                </div>
              )}
              <TextArea
                id="agent-ai-chat-input"
                name="agent-ai-chat-input"
                data-testid="chat-input"
                value={input}
                onChange={(e) => setInput(e.currentTarget.value)}
                placeholder={!llmReady ? 'Set up the LLM config to start chatting...' : ''}
                rows={1}
                className={`${styles.textArea} ${!llmReady ? styles.placeholderItalic : ''}`}
                onKeyDown={handleKeyDown}
              />
              <div className={styles.inputFooter}>
                {/* Action icons on the right */}
                <div className={styles.inputActions}>
                  <input
                    type="file"
                    ref={fileInputRef}
                    style={{ display: 'none' }}
                    onChange={handleFileSelect}
                    accept={ATTACHMENT_ACCEPT}
                    multiple
                    data-testid="file-input"
                    disabled={!llmReady}
                  />
                  <Dropdown overlay={attachMenu(!llmReady)}>
                    <button
                      type="button"
                      className={styles.iconButton}
                      disabled={!llmReady}
                      aria-label="Attach file or dictate"
                      title={!llmReady ? 'LLM config not configured' : 'Attach file or dictate'}
                      style={!llmReady ? { opacity: 0.5, cursor: 'not-allowed' } : undefined}
                    >
                      <PlusIcon />
                    </button>
                  </Dropdown>
                  <Dropdown overlay={agentMenu}>
                    <button
                      type="button"
                      className={`${styles.agentInlineButton} ${selectedAgent !== 'generic' ? styles.agentInlineButtonActive : ''}`}
                      style={selectedAgent !== 'generic' ? {
                        '--agent-color': colorForAgent(selectedAgent),
                        '--agent-color-bg': `${colorForAgent(selectedAgent)}14`,
                      } as React.CSSProperties : undefined}
                      data-testid="agent-selector-inline"
                      title={`Agent: ${activeAgentLabel}`}
                      aria-label={`Agent: ${activeAgentLabel}`}
                    >
                      <Icon name="users-alt" size="sm" />
                    </button>
                  </Dropdown>
                  <button
                    type="button"
                    className={styles.sendIconButton}
                    onClick={isLoading ? handleStop : (llmReady ? () => handleSend() : undefined)}
                    disabled={!llmReady && !isLoading}
                    aria-label={isLoading ? 'Stop' : (editingMessageIndex !== null ? 'Save edited message' : 'Send message')}
                    title={!llmReady ? 'LLM config not configured' : (isLoading ? "Stop" : (editingMessageIndex !== null ? 'Save edit' : "Send"))}
                    style={!llmReady ? { opacity: 0.5, cursor: 'not-allowed' } : (isLoading ? { background: theme.colors.secondary.main } : undefined)}
                  >
                    {isLoading ? (
                      <div style={{ width: 16, height: 16, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
                        <div style={{ width: 10, height: 10, backgroundColor: theme.colors.error.main, borderRadius: 2 }}></div>
                      </div>
                    ) : (
                      <svg viewBox="0 0 24 24" width="16" height="16" stroke="white" strokeWidth="2" fill="none" strokeLinecap="round" strokeLinejoin="round">
                        <line x1="12" y1="19" x2="12" y2="5"></line>
                        <polyline points="5 12 12 5 19 12"></polyline>
                      </svg>
                    )}
                  </button>
                </div>
              </div>
            </div>
          </div>
          )}
        </>
      )}

      {previewAttachment && (
        <AttachmentModal
          isOpen={true}
          attachment={previewAttachment}
          onClose={() => setPreviewAttachment(null)}
        />
      )}
      <Modal title="Previous conversations" className={styles.historyModal} isOpen={showHistory} onDismiss={() => setShowHistory(false)}>
        {historySessions.length === 0 ? (
          <div className={styles.historyEmpty}>
            <Icon name="comment-alt-message" size="xxl" className={styles.historyEmptyIcon} />
            <div>No saved conversations yet.</div>
          </div>
        ) : (
          <div className={styles.historyList}>
            {historySessions.map((session) => (
              <div key={session.id} className={styles.historyItem}>
                <div className={styles.historyItemIcon}>
                  <Icon name="comment-alt-message" size="sm" />
                </div>
                <div
                  className={styles.historyItemMain}
                  onClick={() => {
                    setSearchParams({ session: session.id });
                    setShowHistory(false);
                  }}
                >
                  <div className={styles.historyItemTitle}>
                    {session.title}
                    {session.agent && session.agent !== 'generic' && (
                      <span
                        className={styles.historyItemAgentTag}
                        style={{
                          color: colorForAgent(session.agent),
                          borderColor: colorForAgent(session.agent),
                          background: `${colorForAgent(session.agent)}1A`,
                        }}
                      >
                        {session.agentLabel ?? session.agent}
                      </span>
                    )}
                  </div>
                  <div className={styles.historyItemDate}>
                    {new Date(session.updatedAt).toLocaleString()}
                  </div>
                </div>
                <Icon
                  name="trash-alt"
                  className={styles.historyItemDelete}
                  onClick={() => {
                    chatHistoryService.deleteSession(session.id);
                    setHistorySessions(chatHistoryService.getAllSessions());
                  }}
                />
              </div>
            ))}
          </div>
        )}
      </Modal>
    </div>
  );
};
