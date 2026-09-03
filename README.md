<table>
<tr>
<td width="110" valign="middle" align="center"><img src="/public/plugins/shortbobcat2735-agentai-app/img/logo-pixo-large.png" width="90" alt="Agent AI" /></td>
<td valign="middle">

# Agent AI
*A read-only AI assistant for Grafana dashboards, metrics, logs, traces,
alerts, incidents, and specialist operational knowledge.*

</td>
</tr>
</table>

---

Agent AI lets teams ask Grafana questions in natural language and get
answers grounded in live data from the current instance. It can inspect
dashboards, query Prometheus/Loki/Tempo, review alerts, explain panels,
investigate incidents, and use custom specialist agents with your own
documentation.

The plugin runs tool calls from its own Go backend and does not require a
separate service to read Grafana data. Grafana operations are read-only:
Agent AI does not create, update, or delete dashboards, alerts, folders,
datasources, or other Grafana resources.

## Screenshots

<p>
  <img src="/public/plugins/shortbobcat2735-agentai-app/img/screenshots/chat-landing.png" width="760" alt="Chat" />
</p>
<p>
  <img src="/public/plugins/shortbobcat2735-agentai-app/img/screenshots/agents-page.png" width="760" alt="Custom specialist agents" />
</p>
<p>
  <img src="/public/plugins/shortbobcat2735-agentai-app/img/screenshots/panel-menu.png" width="760" alt="Panel menu integration" />
</p>
<p>
  <img src="/public/plugins/shortbobcat2735-agentai-app/img/screenshots/configuration-page.png" width="760" alt="Configuration" />
</p>

## Main capabilities

| Capability | What it does |
|---|---|
| Context-aware chat | Ask about dashboards, panels, metrics, logs, traces, alerts, services, or architecture using live Grafana data. |
| Light Mode | Optimize token usage on free-tier LLM providers by running the Default agent with a reduced context footprint. |
| Panel and dashboard analysis | Open Agent AI from a panel menu or dashboard context and ask about the exact queries, variables, datasource, and time range being viewed. |
| Specialist agents / subagents | Create focused agents for areas such as SRE, Kubernetes, security, platform engineering, or internal docs. Each one adds its own context while keeping the same live Grafana tools. |
| Alert and incident investigation | Start from a firing alert or incident seed and gather related alert rules, logs, traces, dashboards, and historical context in one flow. |
| Postmortem generation | Produce a structured Markdown incident report with Summary, Timeline, Impact, Root Cause, Detection, Resolution, and Action Items, grounded in gathered evidence. |
| Attachments, voice, and history | Attach files, use voice input, copy/export answers, edit messages, and continue previous conversations. |
| Long conversations | Older turns are summarized when the conversation approaches the model context limit; live context usage is shown in the UI. |
| Optional memory | When Brain Agent is installed and enabled, Agent AI can search approved memories/runbooks and suggest new memories for admin review. |
| Internet tools | Optional internet-backed search can be enabled for public documentation or product lookups; disable it for local-only operation. |
| Usage metrics | `GET /api/plugins/shortbobcat2735-agentai-app/resources/metrics` exposes Prometheus-format request, latency, and token metrics. |

## Specialist Agents / Subagents

Use the **Agents** page to create up to 9 custom specialist agents. Each
agent can have:

- A custom name shown in the chat agent selector.
- Up to 4,000 characters of `.md` or `.txt` context.
- A per-agent temperature override.
- A per-agent context-window setting from 100k to 120k tokens.

These agents are not separate servers and do not replace the main assistant.
They are specialist modes: Agent AI injects the selected agent's context into
the request, then the same backend tools remain available. That means a
Kubernetes agent can still query dashboards, logs, metrics, traces, alerts,
and datasources, but it answers with your Kubernetes notes, naming, playbooks,
and conventions in mind.

Good uses for specialist agents:

- SRE playbooks and incident response procedures.
- Kubernetes cluster conventions, namespaces, and workload patterns.
- Security investigation checklists and approved language.
- Platform team ownership maps, service naming, and runbook notes.
- Internal documentation that should guide answers without changing code.

Only Grafana Admin users can manage agents. Admins can also download each
agent's current context for backup or sharing. If needed, Configuration can
restrict Viewer users to the built-in Default agent.

## Live Tools

Agent AI discovers Grafana resources dynamically instead of assuming fixed
folder names, dashboard names, labels, or datasources.

| Area | Examples |
|---|---|
| Discovery | List datasources, folders, dashboards, and retrieve dashboard JSON. |
| Metrics | Query Prometheus, analyze anomalies, forecast capacity, inspect SLO burn rate. |
| Logs | Query Loki, discover labels, group log patterns, inspect Kubernetes events shipped to Loki. |
| Traces | Query Tempo, find trace bottlenecks, build service topology from sampled traces. |
| Alerts | List alerts/rules, inspect rule expressions, assess alert quality, investigate firing alerts. |
| Correlations | Read Grafana correlations and follow a correlation for a real field value. |
| Kubernetes | Diagnose workloads, container lifecycle, node health, and event patterns when the required telemetry exists. |
| Cloud and SQL | Analyze CloudWatch metrics and run read-only Postgres `SELECT`/`WITH` queries through configured datasources. |
| Memory | Search approved Brain Agent memory/runbooks and queue suggested memories when Brain Agent is enabled. |
| Internet | Search public web context when internet tools are enabled by an admin. |

When a tool needs a datasource and more than one matching datasource exists,
the assistant asks which one to use instead of silently guessing.

## Where to Use It

- **Side menu**: open **Agent AI** from Grafana's installed apps, or press
  `Ctrl+K` and search for Agent AI.
- **Panel menu**: on a dashboard panel, open the panel menu and choose
  **Extensions -> Agent AI** to start a chat pre-loaded with that panel's
  context.
- **Dashboard Chat**: ask broader questions about all panels in a dashboard.
- **Agents page**: create and maintain specialist agents.

## Access Model

> **Grafana service account token required.** To access real Grafana data,
> set a Grafana Service Account Token in Configuration. Without it, calls
> that list dashboards, query datasources, inspect alerts, or analyze panels
> return `401 Unauthorized`, and the assistant can only answer without live
> Grafana evidence.

All Grafana API calls use the configured service account, not the individual
chat user's Grafana permissions. Scope that service account to the folders,
datasources, and resources that any chat user is allowed to ask about.

## Configuration

The **Configuration** page is available to Grafana Admin users.

Required:

- **Endpoint URL**: an OpenAI-compatible Chat Completions endpoint
  (`/v1/chat/completions`).
- **Model**: the model name to request.
- **API Key**: bearer token for the model endpoint, stored securely.
- **Grafana Service Account Token**: required for live Grafana data.

Optional:

- **Default reply language**: English, Portuguese, or Spanish.
- **Assistant Features**: enable or disable standalone chat and panel-menu
  integration.
- **Specialist agent access**: optionally restrict Viewers to the Default
  agent.
- **Internet tools**: enable or disable public internet-backed search.
- **Light Mode**: reduce the context size to ~5k tokens for the Default agent, perfect for free tier limits.
- **Brain Agent tools**: enable optional long-term memory when Brain Agent is
  installed.
- **Provider fallback**: configure backup OpenAI-compatible providers.
- **Security and policies**: add organization-specific guardrails and audit
  logging.
- **Advanced settings**: timeout, max response tokens, retry count, attachment
  size, and related limits.

## Optional Brain Agent Memory

Brain Agent is a separate optional plugin. Agent AI works without it.

When Brain Agent is installed and enabled, Agent AI can search approved
memory and runbook entries, pre-fetch relevant context for the dashboard or
panel being viewed, and send suggested memories to Brain Agent for admin
review. Direct memory writes remain controlled by Brain Agent's own access
model.

## Requirements

- Grafana 12 or newer.
- An endpoint compatible with the OpenAI Chat Completions API.
- A Grafana service account token for live Grafana data.

## Installation

Install like any other Grafana app plugin: unzip the release into Grafana's
plugins directory, restart Grafana, enable the plugin, then open
**Configuration** to set the LLM endpoint, model, API key, and Grafana
service account token.

## Development

Frontend:

```bash
bun install
bun run build
```

Backend:

```bash
mage -v
```

See `docker-compose.yaml` and `provisioning/` for a local Grafana instance.

## License

MIT -- see [LICENSE](https://github.com/luiscorreiaops/agent-ai-app/blob/main/LICENSE).
