// Mock Grafana modules to avoid ESM transformation issues
jest.mock('@grafana/ui', () => ({
  Field: ({ children, label }: any) => <div><label>{label}</label>{children}</div>,
  FieldSet: ({ children, label }: any) => <fieldset><legend>{label}</legend>{children}</fieldset>,
  Input: (props: any) => <input aria-label={props['aria-label']} value={props.value} onChange={props.onChange} />,
  SecretInput: (props: any) => <input aria-label={props['aria-label']} type="password" value={props.value} onChange={props.onChange} />,
  Button: ({ children, onClick, disabled }: any) => <button onClick={onClick} disabled={disabled}>{children}</button>,
  Alert: ({ children, title }: any) => <div role="alert"><div>{title}</div>{children}</div>,
  Switch: (props: any) => <input type="checkbox" aria-label={props['aria-label']} checked={props.value} onChange={props.onChange} />,
  TextArea: (props: any) => <textarea aria-label={props['aria-label']} value={props.value} onChange={props.onChange} />,
  RadioButtonGroup: ({ options, value, onChange }: any) => (
    <div role="radiogroup">
      {options.map((opt: any) => (
        <button key={opt.value} aria-pressed={opt.value === value} onClick={() => onChange(opt.value)}>
          {opt.label}
        </button>
      ))}
    </div>
  ),
  Tooltip: ({ children }: any) => <>{children}</>,
  useStyles2: (getStyles: any) =>
    getStyles({
      colors: {
        border: { weak: '#333' },
        success: { main: '#1a7f37', contrastText: '#fff' },
        error: { main: '#cf222e', contrastText: '#fff' },
        warning: { main: '#e0b400' },
      },
      spacing: (n: number) => `${n * 8}px`,
    }),
}));

jest.mock('@grafana/runtime', () => ({
  getBackendSrv: () => ({
    post: jest.fn().mockResolvedValue({}),
    get: jest.fn((url: string) => {
      if (url.includes('/integrations')) {
        return Promise.resolve([]);
      }
      return Promise.resolve({ status: 'ok', message: 'Connected' });
    }),
  }),
}));

jest.mock('../../api/client', () => ({
  ...jest.requireActual('../../api/client'),
  saveSettingsWithVersionCheck: jest.fn(),
}));

import { act, fireEvent, render, screen } from '@testing-library/react';
import { AppConfig } from './AppConfig';
import * as apiClient from '../../api/client';

// AppConfig fetches integrations status in a fire-and-forget useEffect on
// mount. None of the tests below assert on that fetch, but its mocked
// promise still resolves and updates state during the test -- flushing it
// via this helper (instead of a bare render()) keeps that resolution
// inside an act() boundary, so it doesn't log an "update not wrapped in
// act(...)" warning for a state change none of these tests care about.
async function renderConfig() {
  render(<AppConfig plugin={mockPlugin} query={{} as any} />);
  await act(async () => {});
}

const mockPlugin = {
  meta: {
    id: 'shortbobcat2735-agentai-app',
    name: 'Agent AI Analysis',
    type: 'app' as const,
    module: '',
    baseUrl: '',
    info: {
      author: { name: 'Internal' },
      description: '',
      logos: { small: '', large: '' },
      links: [],
      screenshots: [],
      updated: '',
      version: '',
    },
    jsonData: {
      endpointURL: 'https://example.com/v1',
      model: 'test-model',
      timeoutSeconds: 60,
      maxTokens: 4096,
    },
    secureJsonFields: {},
    enabled: true,
  },
} as any;

describe('AppConfig', () => {
  it('defaults decision routing to off and keeps its endpoint hidden', async () => {
    await renderConfig();
    fireEvent.click(screen.getByText('Tool Routing'));
    expect(screen.getByRole('button', { name: 'Off' })).toHaveAttribute('aria-pressed', 'true');
    expect(screen.queryByLabelText('Decision endpoint URL')).not.toBeInTheDocument();
  });

  it('requires a decision endpoint and model before saving an enabled route', async () => {
    const save = jest.mocked(apiClient.saveSettingsWithVersionCheck).mockClear().mockResolvedValue(1);
    try {
      await renderConfig();
      fireEvent.click(screen.getByText('Tool Routing'));
      fireEvent.click(screen.getByRole('button', { name: 'Observe' }));
      fireEvent.click(screen.getByText(/save settings/i));
      expect(screen.getByText(/Decision endpoint URL and model are required/)).toBeInTheDocument();
      expect(save).not.toHaveBeenCalled();
    } finally {
      save.mockClear();
    }
  });

  it('saves routing and Light Mode together with a separate secure credential', async () => {
    const save = jest.mocked(apiClient.saveSettingsWithVersionCheck).mockClear().mockResolvedValue(1);
    try {
      const plugin = { ...mockPlugin, meta: { ...mockPlugin.meta, jsonData: {
        ...mockPlugin.meta.jsonData, lightModeForDefaultAgent: true, enableToolSearch: true,
      } } };
      render(<AppConfig plugin={plugin} query={{} as any} />);
      await act(async () => {});
      fireEvent.click(screen.getByText('Tool Routing'));
      fireEvent.click(screen.getByRole('button', { name: 'Reduce tools' }));
      fireEvent.change(screen.getByLabelText('Decision endpoint URL'), { target: { value: 'http://decider:8000/v1/systemone' } });
      fireEvent.change(screen.getByLabelText('Decision model'), { target: { value: 'Mapika/decider-2b' } });
      fireEvent.change(screen.getByLabelText('Decision API key'), { target: { value: 'decision-test-key' } });
      await act(async () => { fireEvent.click(screen.getByText(/save settings/i)); });
      expect(save).toHaveBeenCalledWith(0, expect.objectContaining({
        lightModeForDefaultAgent: true, enableToolSearch: true, decisionRoutingMode: 'enforce',
        decisionEndpointURL: 'http://decider:8000/v1/systemone', decisionModel: 'Mapika/decider-2b',
      }), { decisionApiKey: 'decision-test-key' });
      const jsonData = save.mock.calls[0][1];
      expect(JSON.stringify(jsonData)).not.toContain('decision-test-key');
      expect(screen.getByLabelText('Decision API key')).toHaveValue('');
    } finally {
      save.mockClear();
    }
  });

  it('preserves a configured decision credential when another setting is saved', async () => {
    const save = jest.mocked(apiClient.saveSettingsWithVersionCheck).mockClear().mockResolvedValue(1);
    try {
      const plugin = { ...mockPlugin, meta: { ...mockPlugin.meta, secureJsonFields: { decisionApiKey: true } } };
      render(<AppConfig plugin={plugin} query={{} as any} />);
      await act(async () => {});
      await act(async () => { fireEvent.click(screen.getByText(/save settings/i)); });
      expect(save.mock.calls[0][2]).not.toHaveProperty('decisionApiKey');
    } finally {
      save.mockClear();
    }
  });
  it('renders the configuration form', async () => {
    await renderConfig();
    expect(screen.getByTestId('app-config')).toBeInTheDocument();
  });

  it('renders endpoint URL input', async () => {
    await renderConfig();
    expect(screen.getByLabelText(/^endpoint url$/i)).toBeInTheDocument();
  });

  it('renders model input', async () => {
    await renderConfig();
    expect(screen.getByLabelText(/^model$/i)).toBeInTheDocument();
  });

  it('renders API key input', async () => {
    await renderConfig();
    expect(screen.getByLabelText(/^api key$/i)).toBeInTheDocument();
  });

  it('renders save button', async () => {
    await renderConfig();
    expect(screen.getByText(/save settings/i)).toBeInTheDocument();
  });

  it('renders test connection button', async () => {
    await renderConfig();
    expect(screen.getByText(/test connection/i)).toBeInTheDocument();
  });

  it('displays existing endpoint URL from jsonData', async () => {
    await renderConfig();
    const input = screen.getByLabelText(/^endpoint url$/i) as HTMLInputElement;
    expect(input.value).toBe('https://example.com/v1');
  });

  describe('Collapsible categories', () => {
    it('only "AI Provider" starts expanded -- the other four categories start collapsed', async () => {
      await renderConfig();
      // AI Provider (expanded): its fields are already visible.
      expect(screen.getByLabelText(/^endpoint url$/i)).toBeInTheDocument();
      // The other four categories exist (their toggle button is visible)
      // but their content hasn't been rendered yet.
      expect(screen.getByText(/^grafana & integrations$/i)).toBeInTheDocument();
      expect(screen.queryByLabelText(/grafana token/i)).not.toBeInTheDocument();
      expect(screen.getByText(/^assistant experience$/i)).toBeInTheDocument();
      expect(screen.queryByText(/^standalone chat$/i)).not.toBeInTheDocument();
      expect(screen.getByText(/^internet tools$/i)).toBeInTheDocument();
      expect(screen.queryByText(/^enable internet tools$/i)).not.toBeInTheDocument();
      expect(screen.getByText(/^security & limits$/i)).toBeInTheDocument();
      expect(screen.queryByLabelText(/additional guardrails/i)).not.toBeInTheDocument();
    });

    it('category toggle buttons show only the category name -- no Show/Hide prefix', async () => {
      await renderConfig();
      // The 5 category names appear verbatim, without a "Show "/"Hide " prefix.
      for (const name of ['AI Provider', 'Grafana & Integrations', 'Assistant Experience', 'Internet Tools', 'Security & Limits']) {
        const regex = new RegExp(`^${name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`, 'i');
        expect(screen.getByText(regex)).toBeInTheDocument();
      }
    });

    it('expanding a category reveals its fields (Assistant Experience example)', async () => {
      await renderConfig();
      fireEvent.click(screen.getByText(/^assistant experience$/i));
      expect(screen.getByText(/^standalone chat$/i)).toBeInTheDocument();
      expect(screen.getByText(/^maintenance mode$/i)).toBeInTheDocument();
    });
  });
});
