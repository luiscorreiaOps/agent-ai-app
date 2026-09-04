// Regression test for the "panel-preview session leaks into main chat
// history" bug: launching the assistant from a dashboard panel's menu
// (isPanelPreview, see ChatInterface.tsx) is documented as "a read-only,
// one-shot preview -- not an open conversation", but handleSend called
// chatHistoryService.saveSession() unconditionally, so every one-shot
// panel Q&A silently showed up in the main chat page's "Previous
// conversations" list. Fixed by gating every saveSession/setSearchParams
// call in handleSend behind !isPanelPreview.

const fakeHistory: { location: { pathname: string; search: string } } = {
  location: { pathname: '/a/shortbobcat2735-agentai-app/chat', search: '' },
};
const historyListeners: Array<(upd: any) => void> = [];
// "mock"-prefixed so Jest's factory-hoisting allows referencing it inside
// jest.mock below -- a single shared jest.fn() (not a fresh one per
// getAppEvents() call) so tests can inspect every notifyWarning() call.
const mockPublish = jest.fn();

jest.mock('@grafana/runtime', () => ({
  locationService: {
    getLocation: () => fakeHistory.location,
    getHistory: () => ({
      listen: (cb: (upd: any) => void) => {
        historyListeners.push(cb);
        return () => {
          const i = historyListeners.indexOf(cb);
          if (i >= 0) {
            historyListeners.splice(i, 1);
          }
        };
      },
      replace: (upd: { pathname: string; search: string }) => {
        fakeHistory.location = upd;
        historyListeners.forEach((cb) => cb({ location: upd }));
      },
      push: (upd: { pathname: string; search: string }) => {
        fakeHistory.location = upd;
        historyListeners.forEach((cb) => cb({ location: upd }));
      },
    }),
  },
  getAppEvents: () => ({ publish: mockPublish }),
  config: { bootData: { user: { id: 1, orgId: 1 } } },
}));

jest.mock('../../../api/client', () => ({
  fetchAgents: jest.fn().mockResolvedValue([]),
  fetchLimits: jest.fn().mockResolvedValue({
    attachmentMaxBytes: 51200,
    enableStandaloneChat: true,
    enableDashboardIntegration: true,
    auditLogFullContent: false,
    responseLanguage: 'english',
  }),
  testConnection: jest.fn().mockResolvedValue({ status: 'OK', message: 'ok' }),
  // One content chunk then done -- enough for handleSend's loop to reach
  // its post-completion save step without needing a real backend.
  streamChat: jest.fn(async function* () {
    yield { content: 'a reply', done: false };
    yield { content: '', done: true };
  }),
}));

jest.mock('../../../services/context', () => ({
  contextService: {
    getUserContext: () => ({ name: 'test-user' }),
    getDataSources: () => [],
    getCurrentDashboard: async () => null,
  },
}));

// CodeBlock/MermaidBlock pull in react-syntax-highlighter and a dynamic
// import('mermaid') respectively -- neither is transpiled for Jest and
// neither is relevant to what this test exercises (it never sends a
// message containing a code fence or a mermaid diagram).
jest.mock('./components/CodeBlock', () => ({ CodeBlock: ({ children }: any) => <pre>{children}</pre> }));
jest.mock('./components/MermaidBlock', () => ({ MermaidBlock: ({ children }: any) => <pre>{children}</pre> }));

// GrafanaTheme2 is a large, deeply-nested object; ChatInterface.styles.ts
// (and its sub-components' own styles.ts files) read many different paths
// off it. A recursive Proxy stands in for the whole tree: any property
// access returns either a callable/stringable stand-in or another Proxy,
// so `theme.shape.radius.default`, `theme.colors.text.primary`, etc. all
// resolve to *something* usable in a template literal instead of throwing.
function makeThemeProxy(): any {
  const handler: ProxyHandler<any> = {
    get(_target, prop) {
      if (prop === Symbol.toPrimitive || prop === 'toString' || prop === 'valueOf') {
        return () => '0';
      }
      if (typeof prop === 'symbol') {
        return undefined;
      }
      const fn = (..._args: any[]) => makeThemeProxy();
      return new Proxy(fn, handler);
    },
    apply() {
      return '0';
    },
  };
  return new Proxy(() => '0', handler);
}

// Full mock, same pattern as AppConfig.test.tsx -- @grafana/ui's real
// package entry point is one shared barrel file that unconditionally
// requires its entire component tree (Select, DateTimePickers/
// react-calendar, ...) before exporting anything, none of which is
// transpiled for Jest and isn't relevant to what this test exercises.
jest.mock('@grafana/ui', () => ({
  useStyles2: (getStyles: any) => (typeof getStyles === 'function' ? getStyles(makeThemeProxy()) : {}),
  useTheme2: () => makeThemeProxy(),
  Alert: ({ children, title }: any) => <div role="alert"><div>{title}</div>{children}</div>,
  TextArea: (props: any) => <textarea {...props} />,
  Icon: (props: any) => <span data-icon-name={props.name} />,
  Modal: ({ children, isOpen, title }: any) => (isOpen ? <div role="dialog" aria-label={title}>{children}</div> : null),
  Dropdown: ({ children }: any) => <>{children}</>,
  Menu: ({ children }: any) => <div>{children}</div>,
  MenuItem: (props: any) => <button onClick={props.onClick}>{props.label}</button>,
  Divider: () => <hr />,
}));

import { render, screen, fireEvent, waitFor, act } from '@testing-library/react';
import { ChatInterface } from './ChatInterface';
import { chatHistoryService } from '../../../services/chatHistory';
import { contextService } from '../../../services/context';
import { streamChat, fetchLimits } from '../../../api/client';
import type { PluginExtensionPanelContext } from '@grafana/data';

// Real PluginExtensionPanelContext shape needed by ChatInterface's
// panel-preview mount effect (title/dashboard.title/timeRange.from+to) --
// see the "Launched from a panel's context menu" effect in ChatInterface.tsx,
// which builds and auto-sends the preview question from exactly these
// fields the moment panelContext is set.
const fakePanelContext = {
  pluginId: 'test-datasource',
  title: 'Taxa de erro',
  dashboard: { uid: 'dash-1', title: 'Test dashboard' },
  targets: [],
  timeRange: { from: 'now-6h', to: 'now' },
} as unknown as PluginExtensionPanelContext;

describe('ChatInterface -- panel-preview session persistence', () => {
  let saveSessionSpy: jest.SpyInstance;

  beforeEach(() => {
    localStorage.clear();
    saveSessionSpy = jest.spyOn(chatHistoryService, 'saveSession');
  });

  afterEach(() => {
    saveSessionSpy.mockRestore();
  });

  it('never persists a session when launched as a panel preview', async () => {
    // Panel-preview mode has no input box at all (see ChatInterface.tsx --
    // the whole .inputArea is hidden when isPanelPreview) -- it auto-sends
    // its one question from a mount-only effect instead, as soon as
    // panelContext is set.
    await act(async () => {
      render(<ChatInterface panelContext={fakePanelContext} onDismiss={jest.fn()} />);
    });

    await waitFor(() => expect(screen.queryByText('a reply')).toBeInTheDocument());

    expect(saveSessionSpy).not.toHaveBeenCalled();
  });

  // Real bug, live-reproduced: closing the panel-preview modal and reopening
  // it faster than a request naturally completes (~8-9s) fired ANOTHER
  // request on top of the still-running one -- unmounting only aborts that
  // instance's own controller, it does not stop the backend's already-
  // dispatched round trip. Doing this a few times piled up several
  // concurrent requests, and when multiple landed back at once, processing
  // all their completions in one synchronous burst froze the tab for 10+
  // seconds (Chrome's "Page Unresponsive" dialog). panelPreviewRequestInFlight
  // must refuse to fire a second request while a previous one is still out.
  it('refuses a second panel-preview request while a previous one is still in flight', async () => {
    const streamChatMock = streamChat as unknown as jest.Mock;
    streamChatMock.mockClear();
    let releaseFirst: () => void = () => {};
    const firstGate = new Promise<void>((resolve) => {
      releaseFirst = resolve;
    });
    streamChatMock.mockImplementationOnce(async function* () {
      await firstGate;
      yield { content: 'first reply', done: true };
    });

    const first = render(<ChatInterface panelContext={fakePanelContext} onDismiss={jest.fn()} />);
    // Let the mount effect actually call streamChat and start awaiting the gate.
    await act(async () => {
      await Promise.resolve();
    });

    // Simulate closing that preview (unmount) and opening a fresh one while
    // the first request is still outstanding.
    first.unmount();
    render(<ChatInterface panelContext={fakePanelContext} onDismiss={jest.fn()} />);

    await waitFor(() =>
      expect(screen.queryByText(/Still finishing the previous panel question/)).toBeInTheDocument()
    );
    // The second mount must not have called streamChat again.
    expect(streamChatMock).toHaveBeenCalledTimes(1);

    // Let the first request finish so it doesn't leak into later tests.
    releaseFirst();
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
  });

  it('still persists a session in normal (non-preview) chat mode', async () => {
    render(<ChatInterface />);

    const input = () => screen.getByTestId('chat-input');
    // usePluginSettings() resolves testConnection() asynchronously before
    // llmReady flips true -- handleKeyDown's Enter handler is a no-op until
    // then (see ChatInterface.tsx), so wait for the input to actually unlock.
    await waitFor(() => expect((input() as HTMLTextAreaElement).disabled).toBe(false));

    await act(async () => {
      fireEvent.change(input(), { target: { value: 'explain this' } });
      fireEvent.keyDown(input(), { key: 'Enter', code: 'Enter' });
    });

    await waitFor(() => expect(screen.queryByText('a reply')).toBeInTheDocument());
    expect(saveSessionSpy).toHaveBeenCalled();
  });
});

// Regression tests for MELHORIA-PERFORMANCE-PRODUCAO.md item 3: the analysis
// context sent to the backend on EVERY message used to unconditionally
// include every configured datasource (name/type/uid) -- real prompt tokens
// spent on every turn for something the backend's own list_datasources tool
// already covers on demand. Fixed to omit that list entirely for a normal
// chat turn, and to include only the CURRENT panel's own datasource(s) when
// launched from one.
describe('ChatInterface -- analysis context no longer floods every message with all datasources', () => {
  const streamChatMock = streamChat as unknown as jest.Mock;

  beforeEach(() => {
    localStorage.clear();
    streamChatMock.mockClear();
  });

  it('omits datasources entirely for a normal chat turn', async () => {
    jest.spyOn(contextService, 'getDataSources').mockReturnValue([
      { name: 'Prometheus', type: 'prometheus', uid: 'prom-uid' },
      { name: 'Loki', type: 'loki', uid: 'loki-uid' },
    ]);

    render(<ChatInterface />);

    const input = () => screen.getByTestId('chat-input');
    await waitFor(() => expect((input() as HTMLTextAreaElement).disabled).toBe(false));

    await act(async () => {
      fireEvent.change(input(), { target: { value: 'explain this' } });
      fireEvent.keyDown(input(), { key: 'Enter', code: 'Enter' });
    });

    await waitFor(() => expect(streamChatMock).toHaveBeenCalled());
    const context = streamChatMock.mock.calls[0][2];
    expect(context.datasources).toBeUndefined();
  });

  it('scopes datasources to only the current panel\'s own, when launched from one', async () => {
    jest.spyOn(contextService, 'getDataSources').mockReturnValue([
      { name: 'Prometheus', type: 'prometheus', uid: 'prom-uid' },
      { name: 'Loki', type: 'loki', uid: 'loki-uid' },
    ]);

    const panelContextWithDatasource = {
      pluginId: 'test-datasource',
      title: 'Taxa de erro',
      dashboard: { uid: 'dash-1', title: 'Test dashboard' },
      targets: [{ datasource: { uid: 'prom-uid' } }],
      timeRange: { from: 'now-6h', to: 'now' },
    } as unknown as PluginExtensionPanelContext;

    await act(async () => {
      render(<ChatInterface panelContext={panelContextWithDatasource} onDismiss={jest.fn()} />);
    });

    await waitFor(() => expect(streamChatMock).toHaveBeenCalled());
    const context = streamChatMock.mock.calls[0][2];
    expect(context.datasources).toEqual([{ name: 'Prometheus', type: 'prometheus', uid: 'prom-uid' }]);
  });
});

// Regression tests for MELHORIA-PERFORMANCE-PRODUCAO.md item 6: the UI only
// ever checked one attachment's RAW file size against attachmentMaxBytes --
// no cap on how many attachments one message could carry, and no accounting
// for the ~33% base64 inflation an image attachment picks up before it's
// actually sent, so a combination that passed every individual check could
// still blow past the backend's payload cap. Both are enforced client-side
// now, using the maxAttachments/maxAttachmentsTotalBytes /limits now expose.
describe('ChatInterface -- attachment count and combined-size limits', () => {
  const fetchLimitsMock = fetchLimits as unknown as jest.Mock;

  beforeEach(() => {
    mockPublish.mockClear();
  });

  function makeTextFile(name: string, content: string): File {
    return new File([content], name, { type: 'text/plain' });
  }

  async function renderReady() {
    render(<ChatInterface />);
    const input = () => screen.getByTestId('landing-file-input');
    await waitFor(() => expect((input() as HTMLInputElement).disabled).toBe(false));
    return input;
  }

  it('rejects files beyond maxAttachments, keeping only the first N', async () => {
    fetchLimitsMock.mockResolvedValueOnce({
      attachmentMaxBytes: 51200,
      maxAttachments: 2,
      maxAttachmentsTotalBytes: 1_000_000,
      enableStandaloneChat: true,
      enableDashboardIntegration: true,
      auditLogFullContent: false,
      responseLanguage: 'english',
    });
    const input = await renderReady();

    const files = [makeTextFile('a.txt', 'aaa'), makeTextFile('b.txt', 'bbb'), makeTextFile('c.txt', 'ccc')];
    await act(async () => {
      fireEvent.change(input(), { target: { files } });
    });

    await waitFor(() => expect(screen.queryByText('a.txt')).toBeInTheDocument());
    expect(screen.queryByText('b.txt')).toBeInTheDocument();
    expect(screen.queryByText('c.txt')).not.toBeInTheDocument();
    expect(mockPublish).toHaveBeenCalledWith(
      expect.objectContaining({ payload: [expect.stringContaining('Only 2 attachments')] })
    );
  });

  it('rejects a file that would push the combined encoded size over maxAttachmentsTotalBytes', async () => {
    fetchLimitsMock.mockResolvedValueOnce({
      attachmentMaxBytes: 51200,
      maxAttachments: 10,
      maxAttachmentsTotalBytes: 15,
      enableStandaloneChat: true,
      enableDashboardIntegration: true,
      auditLogFullContent: false,
      responseLanguage: 'english',
    });
    const input = await renderReady();

    // 10 bytes each -- first fits alone (10 <= 15), second would make 20 > 15.
    const files = [makeTextFile('first.txt', '1234567890'), makeTextFile('second.txt', '1234567890')];
    await act(async () => {
      fireEvent.change(input(), { target: { files } });
    });

    await waitFor(() => expect(screen.queryByText('first.txt')).toBeInTheDocument());
    expect(screen.queryByText('second.txt')).not.toBeInTheDocument();
    expect(mockPublish).toHaveBeenCalledWith(
      expect.objectContaining({ payload: [expect.stringContaining('combined attachment size')] })
    );
  });
});

describe('ChatInterface -- dispatch_worker live activity chips', () => {
  const streamChatMock = streamChat as unknown as jest.Mock;

  beforeEach(() => {
    localStorage.clear();
    streamChatMock.mockClear();
  });

  it('shows a chip for a running worker and keeps it visible once the worker reports done', async () => {
    streamChatMock.mockImplementationOnce(async function* () {
      yield {
        content: '',
        done: false,
        workerEvent: {
          taskId: 'call_1',
          workerType: 'metric_investigator',
          label: 'Metrics Analyzer',
          task: 'check CPU usage',
          status: 'Starting investigation...',
          phase: 'running',
        },
      };
      yield {
        content: '',
        done: false,
        workerEvent: {
          taskId: 'call_1',
          workerType: 'metric_investigator',
          label: 'Metrics Analyzer',
          task: 'check CPU usage',
          status: 'Done',
          phase: 'done',
        },
      };
      yield { content: 'CPU usage is normal.', done: true };
    });

    render(<ChatInterface />);

    const input = () => screen.getByTestId('chat-input');
    await waitFor(() => expect((input() as HTMLTextAreaElement).disabled).toBe(false));

    await act(async () => {
      fireEvent.change(input(), { target: { value: 'check CPU usage' } });
      fireEvent.keyDown(input(), { key: 'Enter', code: 'Enter' });
    });

    await waitFor(() => expect(screen.getByTestId('worker-activity-chip')).toBeInTheDocument());
    expect(screen.getByText('Metrics Analyzer')).toBeInTheDocument();
    // Stays visible immediately after the 'done' event (WORKER_CHIP_LINGER_MS
    // hasn't elapsed yet) -- only removed a moment later, not instantly.
    expect(screen.getByText('Done')).toBeInTheDocument();
  });

  it('starts a new turn with no leftover chip from a prior one', async () => {
    streamChatMock.mockImplementationOnce(async function* () {
      yield {
        content: '',
        done: false,
        workerEvent: {
          taskId: 'call_1',
          workerType: 'general_investigator',
          label: 'Investigator',
          task: 'check alerts',
          status: 'Starting investigation...',
          phase: 'running',
        },
      };
      // Stream ends WITHOUT a terminal workerEvent -- the exact case the
      // next-turn reset (setActiveWorkers([]) at send-time) exists for.
      yield { content: 'done', done: true };
    });

    render(<ChatInterface />);
    const input = () => screen.getByTestId('chat-input');
    await waitFor(() => expect((input() as HTMLTextAreaElement).disabled).toBe(false));

    await act(async () => {
      fireEvent.change(input(), { target: { value: 'check alerts' } });
      fireEvent.keyDown(input(), { key: 'Enter', code: 'Enter' });
    });
    await waitFor(() => expect(screen.getByTestId('worker-activity-chip')).toBeInTheDocument());

    streamChatMock.mockImplementationOnce(async function* () {
      yield { content: 'a second reply', done: true };
    });
    await act(async () => {
      fireEvent.change(input(), { target: { value: 'second message' } });
      fireEvent.keyDown(input(), { key: 'Enter', code: 'Enter' });
    });

    await waitFor(() => expect(screen.getByText('a second reply')).toBeInTheDocument());
    expect(screen.queryByTestId('worker-activity-chip')).not.toBeInTheDocument();
  });
});
