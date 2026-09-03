import { AppPluginMeta, GrafanaTheme2, PluginConfigPageProps } from '@grafana/data';
import { Alert, Field, Input, SecretInput, Button, FieldSet, Switch, TextArea, Tooltip, RadioButtonGroup, useStyles2 } from '@grafana/ui';
import { ChangeEvent, useEffect, useState } from 'react';
import { getBackendSrv } from '@grafana/runtime';
import { css } from '@emotion/css';
import { brand } from '../../brand';
import { saveSettingsWithVersionCheck, SettingsConflictError, fetchIntegrationsStatus, type IntegrationStatus } from '../../api/client';

const getStyles = (theme: GrafanaTheme2) => ({
  sectionSubHeader: css`
    font-size: 13px;
    font-weight: 600;
    opacity: 0.85;
    margin-bottom: 4px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  `,
  sectionSubHeaderDivided: css`
    margin-top: 36px;
    padding-top: 24px;
    border-top: 1px solid ${theme.colors.border.weak};
  `,
  testResultOk: css`
    background: ${theme.colors.success.main};
    color: ${theme.colors.success.contrastText};
  `,
  testResultError: css`
    background: ${theme.colors.error.main};
    color: ${theme.colors.error.contrastText};
  `,
  integrationRow: css`
    display: flex;
    align-items: center;
    gap: ${theme.spacing(1)};
    padding: ${theme.spacing(1)} 0;
    border-bottom: 1px solid ${theme.colors.border.weak};
    &:last-child {
      border-bottom: none;
    }
  `,
  integrationDot: css`
    width: 10px;
    height: 10px;
    border-radius: 50%;
    flex-shrink: 0;
  `,
  integrationDotOk: css`
    background: ${theme.colors.success.main};
  `,
  integrationDotDegraded: css`
    background: ${theme.colors.warning.main};
  `,
  integrationName: css`
    flex: 1;
  `,
  categoryToggle: css`
    font-size: 15px;
    font-weight: 600;
  `,
});

// Audit logging toggles something meaningfully different from the other
// switches on this page (message content ends up in logs) -- red when on
// is a deliberate, stronger visual than the default blue "just a feature
// flag" look, so an admin never mistakes it for an ordinary toggle. Targets
// the underlying <input> directly: Switch spreads unknown props onto it,
// and `:checked + label` is exactly how Switch itself colors the on-state.
const lightSwitchOnWrapper = css`
  input:checked + label {
    background: #E5A800 !important;
    border-color: #E5A800 !important;
    &:hover {
      background: #C49000 !important;
    }
  }
`;

const auditSwitchOnWrapper = css`
  input:checked + label {
    background: ${brand.red} !important;
    border-color: ${brand.red} !important;
    &:hover {
      background: #8f1913 !important;
    }
  }
`;

// Endpoint/model pairs actually tested against this plugin's tool-calling
// flow, not just guessed from provider docs -- copy one straight in if you
// don't already have a provider in mind.
interface ProviderExample {
  name: string;
  endpoint: string;
  model: string;
  /** One-glance recommendation shown above the fuller note -- lets a new user pick without reading every line. */
  tagline: string;
  lightModeForDefaultAgent?: boolean;
  note: string;
}

const PROVIDER_EXAMPLES: ProviderExample[] = [
  {
    name: 'Groq',
    endpoint: 'https://api.groq.com/openai/v1',
    model: 'openai/gpt-oss-120b',
    tagline: 'Fastest free option (recommended)',
    note: 'Fast responses, and tool-calling works reliably. The "Light Mode" switch below is automatically enabled for the free tier.',
    lightModeForDefaultAgent: true,
  },
  {
    name: 'OpenRouter',
    endpoint: 'https://openrouter.ai/api/v1',
    model: 'openai/gpt-oss-20b:free',
    tagline: 'Supports many different models',
    note: 'Tool-calling confirmed working reliably across multi-step rounds.',
  },
  {
    name: 'Google Gemini',
    endpoint: 'https://generativelanguage.googleapis.com/v1beta/openai/',
    model: 'gemini-2.0-flash',
    tagline: 'High token limit and multi-tool support',
    note: 'Tool-calling and multi-step rounds work perfectly. Generous free tier limits.',
  },
];

interface FallbackProviderJsonData {
  endpointURL?: string;
  model?: string;
  lightModeForDefaultAgent?: boolean;
}

// 'duckduckgo' is the zero-config default: no container, no API key, no
// account -- calls DuckDuckGo's public Instant Answer API directly. Honestly
// limited (a short product/topic definition, not full search results; see
// the backend's online_search.go), but it's the only option that works the
// moment an admin flips "enable internet tools" with nothing else set up.
// 'searxng' is a self-hosted, open-source metasearch instance the admin
// points at -- no API key/account/billing, full search results (DuckDuckGo
// is still used as an automatic backend-side fallback if this instance is
// unreachable). 'gateway' lets an admin route through their own controlled
// service/provider instead. Any unrecognized value (old config, bad
// provisioning) must fall back to 'duckduckgo', never to a free-form string
// -- the LLM never gets to pick a provider or URL at conversation time
// regardless of this setting.
type OnlineSearchBackend = 'duckduckgo' | 'searxng' | 'gateway';

const DEFAULT_ONLINE_SEARCH_BACKEND: OnlineSearchBackend = 'duckduckgo';

const normalizeOnlineSearchBackend = (value?: string): OnlineSearchBackend =>
  value === 'gateway' || value === 'searxng' ? value : DEFAULT_ONLINE_SEARCH_BACKEND;

// Same relaxed-scheme rule as the backend's normalizeExternalSearchURL: a
// self-hosted SearXNG instance is commonly run on an internal network
// without a public TLS cert. This is purely a client-side hint (a
// placeholder/description), not enforcement -- the backend is the one that
// actually validates and rejects an unsafe URL on save.
const SEARXNG_URL_PLACEHOLDER = 'https://searxng.example.com or http://searxng.internal:8080';

// Mirror the backend's search result/timeout caps -- gives immediate
// feedback instead of a silent server-side clamp on save.
const DEFAULT_ONLINE_SEARCH_MAX_RESULTS = 5;
const MAX_ONLINE_SEARCH_MAX_RESULTS = 8;
const DEFAULT_ONLINE_SEARCH_TIMEOUT_SECONDS = 6;
const MAX_ONLINE_SEARCH_TIMEOUT_SECONDS = 15;

interface JsonData {
  endpointURL?: string;
  model?: string;
  lightModeForDefaultAgent?: boolean;
  timeoutSeconds?: number;
  maxTokens?: number;
  rateLimitMaxRetries?: number;
  attachmentMaxBytes?: number;
  enableStandaloneChat?: boolean;
  enableDashboardIntegration?: boolean;
  /** Shows a maintenance notice instead of the real chat on the standalone chat page -- see module.tsx's AppRoot. */
  maintenanceMode?: boolean;
  customGuardrails?: string;
  /** Default reply language when the user's message doesn't explicitly request a different one -- 'english' (default), 'portuguese', or 'spanish'. See pkg/plugin/guardrails.go's languageDirective. */
  responseLanguage?: string;
  fallbackProviders?: FallbackProviderJsonData[];
  auditLogFullContent?: boolean;
  /** Whether this plugin may automatically use grafana-llm-app (if installed and configured) as an LLM provider -- see pkg/plugin/llmapp.go. */
  enableLLMAppIntegration?: boolean;
  /** Whether to expose brain-agent's (the standalone Memory/RAG plugin) MCP tools (store_memory, search_memory, delete_memory, brain_diagnostics, search_memory_by_time, condense_memory) to the LLM -- see pkg/plugin/mcp.go. Defaults to OFF, unlike enableLLMAppIntegration: it's a separate plugin most instances won't have installed. */
  enableBrainAgentTools?: boolean;
  /** Global gate for every internet-backed tool (e.g. search_web). Off by default even when a backend/key is configured -- see pkg/plugin/resources.go. */
  enableInternetTools?: boolean;
  /** Which search provider backs the internet search tool. Not a secret -- gateway's token is. SearXNG needs no key at all. */
  onlineSearchBackend?: OnlineSearchBackend;
  /** Admin-controlled gateway URL when onlineSearchBackend is 'gateway'. The backend only ever calls fixed /v1/health and /v1/search paths on it. */
  searchGatewayURL?: string;
  /** Admin's self-hosted SearXNG instance base URL when onlineSearchBackend is 'searxng'. The backend only ever calls fixed /healthz and /search paths on it. Not a secret -- no key needed. */
  searxngURL?: string;
  onlineSearchMaxResults?: number;
  onlineSearchTimeoutSeconds?: number;
  /** When on, a requester whose Grafana org role is exactly "Viewer" is always silently downgraded to the Default agent -- custom agent-N slots never run for them, in the picker or in a direct API request. Off by default (today's behavior: unrestricted). */
  restrictSpecialistAgentsForViewers?: boolean;
  /** Optimistic-concurrency token shared with the Agents page -- see saveSettingsWithVersionCheck in api/client.ts. */
  settingsVersion?: number;
}

// Mirrors maxCustomGuardrailsChars in pkg/plugin/guardrails.go -- the backend
// truncates past this length regardless, this just gives immediate feedback
// instead of a silent server-side cut.
const MAX_CUSTOM_GUARDRAILS_CHARS = 2000;

// Mirrors maxFallbackProviders in pkg/plugin/app.go.
const MAX_FALLBACK_PROVIDERS = 2;

// Mirror maxTimeoutSeconds/maxMaxTokens/maxRateLimitMaxRetries in
// pkg/plugin/app.go and maxAttachmentMaxBytes in pkg/plugin/attachments.go --
// the backend clamps to these regardless, this just gives immediate
// feedback instead of a silent server-side cut on save.
const MAX_TIMEOUT_SECONDS = 300;
const MAX_MAX_TOKENS = 32768;
const MAX_RATE_LIMIT_RETRIES = 20;
const MAX_ATTACHMENT_MAX_KB = 2048; // 2 MB

interface FallbackProviderState {
  endpointURL: string;
  model: string;
  apiKey: string;
  apiKeySet: boolean;
}

interface Props extends PluginConfigPageProps<AppPluginMeta<JsonData>> {}

// Pre-filled as a working example so a brand-new install already shows a
// real, free-tier-friendly provider instead of a blank/paid-only OpenAI
// placeholder -- a new user only has to paste their own API key and save.
// Groq (https://console.groq.com/keys) offers a free tier for this model.
const DEFAULT_ENDPOINT_URL = 'https://api.groq.com/openai/v1';
const DEFAULT_MODEL = 'llama-3.3-70b-versatile';

export function AppConfig({ plugin }: Props) {
  const styles = useStyles2(getStyles);
  const { meta } = plugin;
  const jsonData = meta.jsonData || {};
  const secureJsonFields = meta.secureJsonFields || {};

  const [state, setState] = useState({
    endpointURL: jsonData.endpointURL || DEFAULT_ENDPOINT_URL,
    model: jsonData.model || DEFAULT_MODEL,
    lightModeForDefaultAgent: jsonData.lightModeForDefaultAgent ?? false,
    timeoutSeconds: jsonData.timeoutSeconds || 60,
    maxTokens: jsonData.maxTokens || 4096,
    rateLimitMaxRetries: jsonData.rateLimitMaxRetries ?? 3,
    // Stored/sent as bytes; edited here in KB, which is the natural unit for
    // sizing a text/config/log snippet or a small screenshot.
    attachmentMaxKB: Math.round((jsonData.attachmentMaxBytes || 51200) / 1024),
    enableStandaloneChat: jsonData.enableStandaloneChat ?? true,
    enableDashboardIntegration: jsonData.enableDashboardIntegration ?? true,
    maintenanceMode: jsonData.maintenanceMode ?? false,
    customGuardrails: jsonData.customGuardrails || '',
    responseLanguage: jsonData.responseLanguage || 'english',
    auditLogFullContent: jsonData.auditLogFullContent ?? false,
    restrictSpecialistAgentsForViewers: jsonData.restrictSpecialistAgentsForViewers ?? false,
    enableLLMAppIntegration: jsonData.enableLLMAppIntegration ?? true,
    enableBrainAgentTools: jsonData.enableBrainAgentTools ?? false,
    // On by default for new/local installs. An explicit saved false still
    // keeps the assistant local-only.
    enableInternetTools: jsonData.enableInternetTools ?? true,
    onlineSearchBackend: normalizeOnlineSearchBackend(jsonData.onlineSearchBackend),
    searxngURL: jsonData.searxngURL || '',
    searchGatewayURL: jsonData.searchGatewayURL || '',
    searchGatewayToken: '',
    searchGatewayTokenSet: Boolean(secureJsonFields.searchGatewayToken),
    onlineSearchMaxResults: jsonData.onlineSearchMaxResults || DEFAULT_ONLINE_SEARCH_MAX_RESULTS,
    onlineSearchTimeoutSeconds: jsonData.onlineSearchTimeoutSeconds || DEFAULT_ONLINE_SEARCH_TIMEOUT_SECONDS,
    apiKey: '',
    apiKeySet: Boolean(secureJsonFields.apiKey),
    grafanaToken: '',
    grafanaTokenSet: Boolean(secureJsonFields.grafanaToken),
    fallbackProviders: Array.from({ length: MAX_FALLBACK_PROVIDERS }, (_, i): FallbackProviderState => {
      const saved = jsonData.fallbackProviders?.[i];
      return {
        endpointURL: saved?.endpointURL || '',
        model: saved?.model || '',
        apiKey: '',
        apiKeySet: Boolean(secureJsonFields[`fallbackApiKey${i + 1}`]),
      };
    }),
  });

  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  // Optimistic-concurrency token shared with the Agents page -- see
  // saveSettingsWithVersionCheck in api/client.ts. plugin.meta.jsonData is a
  // snapshot from whenever this page was opened/last reloaded, so this is
  // only refreshed by a real page reload -- exactly the recovery step
  // SettingsConflictError below asks for.
  const [version, setVersion] = useState(jsonData.settingsVersion ?? 0);
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<{ status: string; message: string } | null>(null);
  // Five top-level collapsible categories -- only AI Provider starts
  // expanded, the rest start collapsed, opt-in click-to-expand via the
  // arrow icon on each category's own toggle button.
  const [showAIProvider, setShowAIProvider] = useState(true);
  // Sub-blocks inside "AI Provider" -- only the primary endpoint/model
  // fields are shown by default even when the category is expanded; the
  // load balancer and advanced provider-tuning fields are opt-in,
  // click-to-expand.
  const [showProviderFallback, setShowProviderFallback] = useState(false);
  const [showModelSettings, setShowModelSettings] = useState(false);
  const [showGrafanaIntegrations, setShowGrafanaIntegrations] = useState(false);
  const [showAssistantExperience, setShowAssistantExperience] = useState(false);
  const [showInternetTools, setShowInternetTools] = useState(false);
  const [showSecurityLimits, setShowSecurityLimits] = useState(false);
  // "Show examples" is its own small nested toggle inside AI Provider, not a
  // category -- independent of showAIProvider, matching its original
  // behavior exactly.
  const [showProviderExamples, setShowProviderExamples] = useState(false);

  // Fetched once on mount, independent of the collapse toggle above --
  // it's a live, best-effort health check on the backend (see
  // pkg/plugin/integrations.go), cheap and bounded, so there's no reason to
  // make the admin expand the section first just to trigger it.
  const [integrations, setIntegrations] = useState<IntegrationStatus[]>([]);
  useEffect(() => {
    let cancelled = false;
    fetchIntegrationsStatus()
      .then((result) => {
        if (!cancelled) {
          setIntegrations(result);
        }
      })
      .catch(() => {
        // Best-effort -- an admin who can't reach this endpoint for some
        // reason just sees an empty list, not an error banner.
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const onChangeString = (key: keyof typeof state) => (event: ChangeEvent<HTMLInputElement>) => {
    setState({ ...state, [key]: event.target.value });
  };

  const onChangeNumber = (key: keyof typeof state, max?: number) => (event: ChangeEvent<HTMLInputElement>) => {
    let value = parseInt(event.target.value, 10) || 0;
    if (max !== undefined && value > max) {
      value = max;
    }
    setState({ ...state, [key]: value });
  };

  const onChangeBool = (key: keyof typeof state) => (event: ChangeEvent<HTMLInputElement>) => {
    setState({ ...state, [key]: event.target.checked });
  };

  const onChangeGuardrails = (event: ChangeEvent<HTMLTextAreaElement>) => {
    setState({ ...state, customGuardrails: event.target.value.slice(0, MAX_CUSTOM_GUARDRAILS_CHARS) });
  };

  const onChangeResponseLanguage = (value: string) => {
    setState({ ...state, responseLanguage: value });
  };

  const onResetApiKey = () => {
    setState({ ...state, apiKey: '', apiKeySet: false });
  };

  const onResetGrafanaToken = () => {
    setState({ ...state, grafanaToken: '', grafanaTokenSet: false });
  };

  const onResetSearchGatewayToken = () => {
    setState({ ...state, searchGatewayToken: '', searchGatewayTokenSet: false });
  };

  const onChangeOnlineSearchBackend = (value: string) => {
    setState({ ...state, onlineSearchBackend: normalizeOnlineSearchBackend(value) });
  };

  const onChangeFallback = (index: number, key: 'endpointURL' | 'model' | 'apiKey') => (event: ChangeEvent<HTMLInputElement>) => {
    const fallbackProviders = state.fallbackProviders.map((fp, i) => (i === index ? { ...fp, [key]: event.target.value } : fp));
    setState({ ...state, fallbackProviders });
  };

  const onResetFallbackApiKey = (index: number) => () => {
    const fallbackProviders = state.fallbackProviders.map((fp, i) => (i === index ? { ...fp, apiKey: '', apiKeySet: false } : fp));
    setState({ ...state, fallbackProviders });
  };

  const onSave = async () => {
    // Real gap found live: nothing stopped saving enableInternetTools=true
    // with backend='searxng'/'gateway' and its required URL field left
    // empty -- the admin would only discover it's broken later, when the
    // model reports "internet access enabled but unavailable" mid-chat,
    // with no clue why. DuckDuckGo needs no URL at all, so it's
    // deliberately excluded from this check.
    if (state.enableInternetTools) {
      if (state.onlineSearchBackend === 'searxng' && !state.searxngURL.trim()) {
        setSaveError('SearXNG instance URL is required when Search backend is set to SearXNG (or switch to DuckDuckGo, which needs no URL).');
        return;
      }
      if (state.onlineSearchBackend === 'gateway' && !state.searchGatewayURL.trim()) {
        setSaveError('Search Gateway URL is required when Search backend is set to Gateway (or switch to DuckDuckGo, which needs no URL).');
        return;
      }
    }
    setSaving(true);
    setSaveError(null);
    try {
      const secureJsonData: Record<string, string> = {};
      if (state.apiKey) {
        secureJsonData.apiKey = state.apiKey;
      }
      if (state.grafanaToken) {
        secureJsonData.grafanaToken = state.grafanaToken;
      }
      if (state.searchGatewayToken) {
        secureJsonData.searchGatewayToken = state.searchGatewayToken;
      }
      state.fallbackProviders.forEach((fp, i) => {
        if (fp.apiKey) {
          secureJsonData[`fallbackApiKey${i + 1}`] = fp.apiKey;
        }
      });

      const newVersion = await saveSettingsWithVersionCheck(
        version,
        {
          endpointURL: state.endpointURL,
          model: state.model,
          lightModeForDefaultAgent: state.lightModeForDefaultAgent,
          timeoutSeconds: state.timeoutSeconds,
          maxTokens: state.maxTokens,
          rateLimitMaxRetries: state.rateLimitMaxRetries,
          attachmentMaxBytes: state.attachmentMaxKB * 1024,
          enableStandaloneChat: state.enableStandaloneChat,
          enableDashboardIntegration: state.enableDashboardIntegration,
          maintenanceMode: state.maintenanceMode,
          customGuardrails: state.customGuardrails,
          responseLanguage: state.responseLanguage,
          fallbackProviders: state.fallbackProviders.map((fp) => ({ endpointURL: fp.endpointURL, model: fp.model })),
          auditLogFullContent: state.auditLogFullContent,
          restrictSpecialistAgentsForViewers: state.restrictSpecialistAgentsForViewers,
          enableLLMAppIntegration: state.enableLLMAppIntegration,
          enableBrainAgentTools: state.enableBrainAgentTools,
          enableInternetTools: state.enableInternetTools,
          onlineSearchBackend: state.onlineSearchBackend,
          searxngURL: state.searxngURL,
          searchGatewayURL: state.searchGatewayURL,
          onlineSearchMaxResults: state.onlineSearchMaxResults,
          onlineSearchTimeoutSeconds: state.onlineSearchTimeoutSeconds,
        },
        secureJsonData
      );
      setVersion(newVersion);

      setState({
        ...state,
        apiKeySet: true,
        apiKey: '',
        grafanaTokenSet: state.grafanaToken ? true : state.grafanaTokenSet,
        grafanaToken: '',
        searchGatewayTokenSet: state.searchGatewayToken ? true : state.searchGatewayTokenSet,
        searchGatewayToken: '',
        fallbackProviders: state.fallbackProviders.map((fp) => ({
          ...fp,
          apiKeySet: fp.apiKey ? true : fp.apiKeySet,
          apiKey: '',
        })),
      });
    } catch (e: unknown) {
      setSaveError(e instanceof SettingsConflictError ? e.message : e instanceof Error ? e.message : 'Failed to save. Nothing was changed.');
    } finally {
      setSaving(false);
    }
  };

  const applyExample = (example: ProviderExample) => {
    setState((prev) => ({ ...prev, endpointURL: example.endpoint, model: example.model, lightModeForDefaultAgent: example.lightModeForDefaultAgent ?? prev.lightModeForDefaultAgent }));
  };

  const onTestConnection = async () => {
    setTesting(true);
    setTestResult(null);
    try {
      const result = await getBackendSrv().get(`/api/plugins/${meta.id}/resources/health`, undefined, undefined, { showErrorAlert: false });
      setTestResult({ status: result.status, message: result.message });
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : 'Unknown error';
      setTestResult({ status: 'error', message });
    } finally {
      setTesting(false);
    }
  };

  return (
    <div data-testid="app-config">
      {/* ================= AI Provider ================= */}
      <FieldSet>
        <Button
          size="sm"
          variant="secondary"
          fill="text"
          icon={showAIProvider ? 'angle-up' : 'angle-down'}
          onClick={() => setShowAIProvider((v) => !v)}
          className={styles.categoryToggle}
          style={{ marginBottom: showAIProvider ? '12px' : 0 }}
        >
          AI Provider
        </Button>

        {showAIProvider && (
          <>
            <Alert title="Choose an AI provider" severity="info" style={{ marginBottom: '16px' }}>
              <div style={{ display: 'flex', flexDirection: 'column', gap: '10px' }}>
                <div>Don&apos;t have a provider picked yet? Click one below to fill in its endpoint and model, then just add your own API key.</div>
                <Button
                  size="sm"
                  variant="secondary"
                  fill="text"
                  icon={showProviderExamples ? 'angle-up' : 'angle-down'}
                  onClick={() => setShowProviderExamples((v) => !v)}
                  style={{ alignSelf: 'flex-start' }}
                >
                  {showProviderExamples ? 'Hide examples' : 'Show examples'}
                </Button>
                {showProviderExamples && PROVIDER_EXAMPLES.map((ex) => (
                  <div key={ex.name} style={{ display: 'flex', alignItems: 'flex-start', gap: '10px' }}>
                    <Button size="sm" variant="secondary" onClick={() => applyExample(ex)} style={{ flexShrink: 0, minWidth: '110px' }}>
                      Use {ex.name}
                    </Button>
                    <div style={{ fontSize: '12px' }}>
                      <div style={{ fontWeight: 600 }}>{ex.tagline}</div>
                      <code>{ex.endpoint}</code> / <code>{ex.model}</code>
                      <div style={{ opacity: 0.8, marginTop: '2px' }}>{ex.note}</div>
                    </div>
                  </div>
                ))}
              </div>
            </Alert>

            <Field
              label="Endpoint URL"
              description="Base URL of the OpenAI-compatible API this agent talks to. Every provider ships one at a different path -- pick one of the tested examples above, or paste your own (OpenAI, Anthropic-compatible gateways, self-hosted, etc.)."
            >
              <Input
                aria-label="Endpoint URL"
                autoComplete="off"
                value={state.endpointURL}
                onChange={onChangeString('endpointURL')}
                placeholder={DEFAULT_ENDPOINT_URL}
                width={60}
              />
            </Field>

            <Field label="Model" description="Model name to use for completions. Must match the endpoint above.">
              <Input
                aria-label="Model"
                autoComplete="off"
                value={state.model}
                onChange={onChangeString('model')}
                placeholder={DEFAULT_MODEL}
                width={40}
              />
            </Field>

            <Field
              label="API Key"
              description="Bearer token for authentication (stored securely). For the pre-filled Groq endpoint, get a free key at console.groq.com/keys."
            >
              <SecretInput
                aria-label="API Key"
                autoComplete="new-password"
                isConfigured={state.apiKeySet}
                value={state.apiKey}
                onChange={onChangeString('apiKey')}
                onReset={onResetApiKey}
                width={60}
              />
            </Field>
            <div style={{ marginBottom: '-16px' }}><Field label="Light Mode" description="Light Mode (Default Agent) Reduces the context size to ~5k tokens for the Default agent by restricting its tools. Perfect for free tier limits."><div className={state.lightModeForDefaultAgent ? lightSwitchOnWrapper : undefined}><Switch value={state.lightModeForDefaultAgent} onChange={onChangeBool('lightModeForDefaultAgent')} /></div></Field></div>

            <div className={styles.sectionSubHeaderDivided}>
              <Button
                size="sm"
                variant="secondary"
                fill="text"
                icon={showProviderFallback ? 'angle-up' : 'angle-down'}
                onClick={() => setShowProviderFallback((v) => !v)}
                style={{ marginBottom: showProviderFallback ? '12px' : 0 }}
              >
                Provider Load Balancer
              </Button>
            </div>
            {showProviderFallback && (
              <>
                <div style={{ fontSize: '13px', opacity: 0.8, marginBottom: '12px' }}>
                  Optional. If the endpoint above fails on the very first reply of a conversation (rate limit, outage, invalid
                  key) -- before anything has been shown to you -- the next filled-in provider here is tried instead,
                  automatically. Once any provider answers, the rest of that conversation keeps using it; nothing switches
                  mid-answer.
                </div>
                {state.fallbackProviders.map((fp, i) => (
                  <div key={i} style={{ display: 'flex', gap: '12px', alignItems: 'flex-end', marginBottom: '8px', flexWrap: 'wrap' }}>
                    <Field label={`Endpoint URL #${i + 2}`}>
                      <Input
                        aria-label={`Backup endpoint URL ${i + 1}`}
                        autoComplete="off"
                        value={fp.endpointURL}
                        onChange={onChangeFallback(i, 'endpointURL')}
                        placeholder="e.g. https://generativelanguage.googleapis.com/v1beta/openai/"
                        width={45}
                      />
                    </Field>
                    <Field label="Model">
                      <Input
                        aria-label={`Backup model ${i + 1}`}
                        autoComplete="off"
                        value={fp.model}
                        onChange={onChangeFallback(i, 'model')}
                        placeholder="e.g. gemini-flash-latest"
                        width={30}
                      />
                    </Field>
                    <Field label="API Key">
                      <SecretInput
                        aria-label={`Backup API key ${i + 1}`}
                        autoComplete="new-password"
                        isConfigured={fp.apiKeySet}
                        value={fp.apiKey}
                        onChange={onChangeFallback(i, 'apiKey')}
                        onReset={onResetFallbackApiKey(i)}
                        width={40}
                      />
                    </Field>
                  </div>
                ))}
              </>
            )}

            <div className={styles.sectionSubHeaderDivided}>
              <Button
                size="sm"
                variant="secondary"
                fill="text"
                icon={showModelSettings ? 'angle-up' : 'angle-down'}
                onClick={() => setShowModelSettings((v) => !v)}
                style={{ marginBottom: showModelSettings ? '12px' : 0 }}
              >
                Provider settings
              </Button>
            </div>
            {showModelSettings && (
              <>
                <Field
                  label="Timeout (seconds)"
                  description={`Maximum time to wait for a response from the AI provider (e.g. the tool-call check, or Test connection below). Doesn't apply to the final streamed answer, which can legitimately take longer. Range: 1-${MAX_TIMEOUT_SECONDS}.`}
                >
                  <Input
                    aria-label="Timeout"
                    type="number"
                    min={1}
                    max={MAX_TIMEOUT_SECONDS}
                    value={state.timeoutSeconds}
                    onChange={onChangeNumber('timeoutSeconds', MAX_TIMEOUT_SECONDS)}
                    width={20}
                  />
                </Field>

                <Field label="Max Tokens" description={`Maximum tokens in the LLM response. Range: 1-${MAX_MAX_TOKENS}.`}>
                  <Input
                    aria-label="Max Tokens"
                    type="number"
                    min={1}
                    max={MAX_MAX_TOKENS}
                    value={state.maxTokens}
                    onChange={onChangeNumber('maxTokens', MAX_MAX_TOKENS)}
                    width={20}
                  />
                </Field>

                <Field
                  label="Rate limit retries"
                  description={`How many times to automatically retry after a 429 'too many requests' response, waiting a bit longer each time (5s, 10s, 20s, ...). Useful on free-tier LLM endpoints (e.g. Groq), which rate-limit per minute. Range: 0-${MAX_RATE_LIMIT_RETRIES} (0 disables retrying).`}
                >
                  <Input
                    aria-label="Rate limit retries"
                    type="number"
                    min={0}
                    max={MAX_RATE_LIMIT_RETRIES}
                    value={state.rateLimitMaxRetries}
                    onChange={onChangeNumber('rateLimitMaxRetries', MAX_RATE_LIMIT_RETRIES)}
                    width={20}
                  />
                </Field>
              </>
            )}
          </>
        )}
      </FieldSet>

      {/* ================= Grafana & Integrations ================= */}
      <FieldSet>
        <Button
          size="sm"
          variant="secondary"
          fill="text"
          icon={showGrafanaIntegrations ? 'angle-up' : 'angle-down'}
          onClick={() => setShowGrafanaIntegrations((v) => !v)}
          className={styles.categoryToggle}
          style={{ marginBottom: showGrafanaIntegrations ? '12px' : 0 }}
        >
          Grafana & Integrations
        </Button>

        {showGrafanaIntegrations && (
          <>
            <Field
              label="Grafana Service Account Token"
              description={
                <>
                  <strong>Required for live Grafana data.</strong>{' '}
                  Create a Viewer service account at Administration &gt; Users and access &gt; Service accounts, then add
                  a token and paste it here.
                </>
              }
            >
              <SecretInput
                aria-label="Grafana Token"
                autoComplete="new-password"
                isConfigured={state.grafanaTokenSet}
                value={state.grafanaToken}
                onChange={onChangeString('grafanaToken')}
                onReset={onResetGrafanaToken}
                width={60}
              />
            </Field>

            <div className={`${styles.sectionSubHeader} ${styles.sectionSubHeaderDivided}`}>Detected Grafana integrations</div>
            <div style={{ fontSize: '13px', opacity: 0.8, marginBottom: '12px' }}>
              Optional extras this plugin can use automatically when another official Grafana plugin is already
              installed and configured on this instance -- nothing to set up here. Only plugins actually installed
              on this Grafana instance are listed below; nothing changes for you if one isn&apos;t present.
            </div>
            {integrations.some((i) => i.id === 'grafana-llm-app') && (
              <Alert title="Grafana LLM key" severity="info" style={{ marginBottom: '12px' }}>
                Use the key already configured in Grafana. It is also used as a fallback if the primary key becomes unavailable.
              </Alert>
            )}
            {integrations.length === 0 && (
              <div style={{ fontSize: '13px', opacity: 0.6 }}>No optional integrations installed on this Grafana instance yet.</div>
            )}
            {integrations.map((integration) => (
              <div key={integration.id} className={styles.integrationRow}>
                <Tooltip
                  content={
                    integration.status === 'ok'
                      ? 'Installed and working -- this plugin can use it.'
                      : integration.detail ||
                        'Installed, but not configured or not responding correctly -- can’t be used yet.'
                  }
                >
                  <span
                    className={`${styles.integrationDot} ${
                      integration.status === 'ok' ? styles.integrationDotOk : styles.integrationDotDegraded
                    }`}
                  />
                </Tooltip>
                <span className={styles.integrationName}>{integration.name}</span>
                {integration.id === 'grafana-llm-app' && (
                  <Tooltip content="When on (default), this plugin may use grafana-llm-app's own configured LLM provider automatically as a last resort -- turn off to only ever use the API key(s) configured above.">
                    <span>
                      <Switch value={state.enableLLMAppIntegration} onChange={onChangeBool('enableLLMAppIntegration')} />
                    </span>
                  </Tooltip>
                )}
              </div>
            ))}
            {integrations.some((i) => i.id === 'brain-agent') && (
              <div className={styles.integrationRow}>
                <span className={styles.integrationName}>
                  Brain Agent tools (store_memory, search_memory, delete_memory, brain_diagnostics...)
                </span>
                <Tooltip content="Off by default. Adds brain-agent's memory tools to every chat request so the assistant can recall and store context across conversations -- turn on only once brain-agent is installed and you want this plugin to use it.">
                  <span>
                    <Switch value={state.enableBrainAgentTools} onChange={onChangeBool('enableBrainAgentTools')} />
                  </span>
                </Tooltip>
              </div>
            )}
          </>
        )}
      </FieldSet>

      {/* ================= Assistant Experience ================= */}
      <FieldSet>
        <Button
          size="sm"
          variant="secondary"
          fill="text"
          icon={showAssistantExperience ? 'angle-up' : 'angle-down'}
          onClick={() => setShowAssistantExperience((v) => !v)}
          className={styles.categoryToggle}
          style={{ marginBottom: showAssistantExperience ? '12px' : 0 }}
        >
          Assistant Experience
        </Button>

        {showAssistantExperience && (
          <>
            <Field
              label="Standalone chat"
              description="The plugin's own side-nav 'Chat' page and command palette entry. Turn off to run this purely as a dashboard-attached assistant (panel menu) with no separate app surface."
            >
              <Switch value={state.enableStandaloneChat} onChange={onChangeBool('enableStandaloneChat')} />
            </Field>

            <Field
              label="Dashboard integration"
              description="The 'Agent AI' entry in each panel's menu. Turn off to run this purely as a standalone assistant with no dashboard/panel attachment at all."
            >
              <Switch value={state.enableDashboardIntegration} onChange={onChangeBool('enableDashboardIntegration')} />
            </Field>

            <Field
              label="Maintenance mode"
              description="Shows a maintenance notice (with the sleeping fox) instead of the real chat on the standalone chat page -- for a planned outage/upgrade window. Does not affect the dashboard-panel-menu chat."
            >
              <Switch value={state.maintenanceMode} onChange={onChangeBool('maintenanceMode')} />
            </Field>

            <Field
              label="Default reply language"
              description={`The assistant answers in this language by default, regardless of what language the user wrote their message in. Someone can still ask explicitly (e.g. "answer in Portuguese") to override this for the rest of that conversation.`}
            >
              <RadioButtonGroup
                options={[
                  { label: 'English', value: 'english' },
                  { label: 'Português', value: 'portuguese' },
                  { label: 'Español', value: 'spanish' },
                  { label: '中文', value: 'chinese' },
                ]}
                value={state.responseLanguage}
                onChange={onChangeResponseLanguage}
              />
            </Field>

            <Field
              label="Restrict specialist agents for Viewers"
              description="When enabled, a Grafana user whose org role is exactly Viewer always uses the Default agent -- any custom agent-N slot never runs for them, in the picker or in a direct API request. Does not affect Editor/Admin users. Off by default (today's behavior: unrestricted)."
            >
              <Switch
                value={state.restrictSpecialistAgentsForViewers}
                onChange={onChangeBool('restrictSpecialistAgentsForViewers')}
              />
            </Field>
          </>
        )}
      </FieldSet>

      {/* ================= Internet Tools ================= */}
      <FieldSet>
        <Button
          size="sm"
          variant="secondary"
          fill="text"
          icon={showInternetTools ? 'angle-up' : 'angle-down'}
          onClick={() => setShowInternetTools((v) => !v)}
          className={styles.categoryToggle}
          style={{ marginBottom: showInternetTools ? '12px' : 0 }}
        >
          Internet Tools
        </Button>

        {showInternetTools && (
          <>
            <Alert title="Internet search boundary" severity="info" style={{ marginBottom: '12px' }}>
              DuckDuckGo (no setup, no key, no service to run) is selected by default -- it only returns a short
              product/topic definition, not full search results. When internet tools are disabled, Agent AI stays
              local and no internet-backed tool is exposed to the model. Admins can provision a self-hosted SearXNG
              URL for full search results, or choose Gateway to route search through their own service or another
              provider.
            </Alert>

            <Field
              label="Enable internet tools"
              description="Global gate for every current or future tool that sends data to the public internet. Off = local-only assistant."
            >
              <Switch value={state.enableInternetTools} onChange={onChangeBool('enableInternetTools')} />
            </Field>

            <Field
              label="Search backend"
              description="DuckDuckGo needs nothing else set up but only returns short product definitions. SearXNG (self-hosted) and Gateway give full search results but require the admin to run or point at a real service."
            >
              <RadioButtonGroup
                options={[
                  { label: 'DuckDuckGo (no setup)', value: 'duckduckgo' },
                  { label: 'SearXNG', value: 'searxng' },
                  { label: 'Admin Search Gateway', value: 'gateway' },
                ]}
                value={state.onlineSearchBackend}
                onChange={onChangeOnlineSearchBackend}
              />
            </Field>

            {state.onlineSearchBackend === 'duckduckgo' && (
              <Alert title="Limited by design" severity="warning" style={{ marginBottom: '12px' }}>
                DuckDuckGo's free API only returns a short definition for a recognized product/topic name (e.g.
                "Grafana", "Kubernetes") -- not a list of web pages. Compound questions fall back to the product name
                mentioned in them. Nothing to configure; switch to SearXNG or Gateway above for full search results.
              </Alert>
            )}

            {state.onlineSearchBackend === 'searxng' && (
              <Field
                label="SearXNG instance URL"
                description="Your own self-hosted SearXNG instance. No API key needed. HTTPS required, except for loopback/private/internal hosts. DuckDuckGo's Instant Answer API is used automatically if this instance doesn't answer."
              >
                <Input
                  aria-label="SearXNG instance URL"
                  value={state.searxngURL}
                  onChange={onChangeString('searxngURL')}
                  placeholder={SEARXNG_URL_PLACEHOLDER}
                  width={48}
                />
              </Field>
            )}

            {state.onlineSearchBackend === 'gateway' && (
              <>
                <Field
                  label="Search Gateway URL"
                  description="HTTPS URL controlled by the admin. The backend calls fixed paths /v1/health and /v1/search."
                >
                  <Input
                    aria-label="Search Gateway URL"
                    value={state.searchGatewayURL}
                    onChange={onChangeString('searchGatewayURL')}
                    placeholder="https://search-gateway.example.com"
                    width={48}
                  />
                </Field>
                <Field label="Search Gateway token" description="Optional bearer token, stored in Grafana secureJsonData.">
                  <SecretInput
                    aria-label="Search Gateway token"
                    autoComplete="new-password"
                    isConfigured={state.searchGatewayTokenSet}
                    value={state.searchGatewayToken}
                    onChange={onChangeString('searchGatewayToken')}
                    onReset={onResetSearchGatewayToken}
                    width={40}
                  />
                </Field>
              </>
            )}

            <Field label="Max search results" description={`Default ${DEFAULT_ONLINE_SEARCH_MAX_RESULTS}, backend cap ${MAX_ONLINE_SEARCH_MAX_RESULTS}.`}>
              <Input
                aria-label="Max search results"
                type="number"
                min={1}
                max={MAX_ONLINE_SEARCH_MAX_RESULTS}
                value={state.onlineSearchMaxResults}
                onChange={onChangeNumber('onlineSearchMaxResults', MAX_ONLINE_SEARCH_MAX_RESULTS)}
                width={20}
              />
            </Field>
            <Field label="Search timeout seconds" description={`Default ${DEFAULT_ONLINE_SEARCH_TIMEOUT_SECONDS}, backend cap ${MAX_ONLINE_SEARCH_TIMEOUT_SECONDS}.`}>
              <Input
                aria-label="Search timeout seconds"
                type="number"
                min={1}
                max={MAX_ONLINE_SEARCH_TIMEOUT_SECONDS}
                value={state.onlineSearchTimeoutSeconds}
                onChange={onChangeNumber('onlineSearchTimeoutSeconds', MAX_ONLINE_SEARCH_TIMEOUT_SECONDS)}
                width={20}
              />
            </Field>
          </>
        )}
      </FieldSet>

      {/* ================= Security & Limits ================= */}
      <FieldSet>
        <Button
          size="sm"
          variant="secondary"
          fill="text"
          icon={showSecurityLimits ? 'angle-up' : 'angle-down'}
          onClick={() => setShowSecurityLimits((v) => !v)}
          className={styles.categoryToggle}
          style={{ marginBottom: showSecurityLimits ? '12px' : 0 }}
        >
          Security & Limits
        </Button>

        {showSecurityLimits && (
          <>
            <Field
              label="Additional guardrails"
              description={`Extra rules appended on top of the assistant's built-in guardrails (never a replacement -- the built-in rules always apply). Use this for org-specific restrictions, e.g. "Never suggest deleting a dashboard" or "Never discuss customer names". ${state.customGuardrails.length}/${MAX_CUSTOM_GUARDRAILS_CHARS} characters.`}
            >
              <TextArea
                aria-label="Additional guardrails"
                value={state.customGuardrails}
                onChange={onChangeGuardrails}
                maxLength={MAX_CUSTOM_GUARDRAILS_CHARS}
                rows={4}
                placeholder="e.g. Never recommend deleting a dashboard or datasource, even if asked directly."
              />
            </Field>

            <Field
              label="Log full message content"
              description="Records assistant activity in Grafana's own logs -- by default just metadata (user, agent, timing, success/failure). Enable this only when full prompt/response auditing is required; users then see a discreet notice that message content may be logged."
            >
              <div className={state.auditLogFullContent ? auditSwitchOnWrapper : undefined}>
                <Switch
                  value={state.auditLogFullContent}
                  onChange={onChangeBool('auditLogFullContent')}
                />
              </div>
            </Field>

            <Field
              label="Max attachment size (KB)"
              description={`Maximum allowed size for uploaded files. Larger text/config/log files consume more prompt tokens; images only matter to vision-capable models and are otherwise harmlessly ignored. Range: 1-${MAX_ATTACHMENT_MAX_KB}.`}
            >
              <Input
                aria-label="Max attachment size (KB)"
                type="number"
                min={1}
                max={MAX_ATTACHMENT_MAX_KB}
                value={state.attachmentMaxKB}
                onChange={onChangeNumber('attachmentMaxKB', MAX_ATTACHMENT_MAX_KB)}
                width={20}
              />
            </Field>
          </>
        )}
      </FieldSet>

      {saveError && (
        <Alert title="Save failed" severity="warning" onRemove={() => setSaveError(null)} style={{ marginTop: '16px' }}>
          {saveError}
        </Alert>
      )}

      <div style={{ display: 'flex', gap: '8px', marginTop: '16px' }}>
        <Button onClick={onSave} disabled={saving}>
          {saving ? 'Saving...' : 'Save settings'}
        </Button>
        <Button variant="secondary" onClick={onTestConnection} disabled={testing}>
          {testing ? 'Testing...' : 'Test connection'}
        </Button>
      </div>

      {testResult && (
        <div
          data-testid="test-result"
          className={testResult.status === 'ok' ? styles.testResultOk : styles.testResultError}
          style={{
            marginTop: '12px',
            padding: '8px 12px',
            borderRadius: '4px',
          }}
        >
          {testResult.status === 'ok' ? '✓' : '✗'} {testResult.message}
        </div>
      )}
    </div>
  );
}
