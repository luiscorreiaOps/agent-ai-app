# Optional Decision Routing

Decision Routing selects candidate tools before the generative LLM sees their
schemas. It does not generate tool arguments, run tools, change datasource
permissions, select another agent, or replace the existing tool-calling loop.
Grafana operations remain read-only.

Under **Configuration > AI Provider > Tool Routing**, choose:

| Mode | Behavior |
| --- | --- |
| Off (default) | No decision request. Existing Light Mode and Tool Search settings apply. |
| Observe (`shadow`) | Call the decision provider and record its suggestion; keep the existing tool presentation. |
| Reduce tools (`enforce`) | Present up to five ranked candidates, essential discovery tools and `search_tools`. |

Errors, invalid responses, timeouts, a `__none__` choice or confidence below the
configured threshold preserve the existing tool configuration. The default
timeout is 1,500 ms, maximum 10,000 ms; confidence defaults to 0.35. Confidence
is the provider's statistic, not necessarily its largest option probability.
Tune the threshold and timeout against your actual workload.

## Light Mode

For the Default agent, successful routing uses at most three candidate tools,
plus `list_datasources`, `list_alerts` and `search_tools` when available. Tool and
parameter descriptions are shortened while schema constraints are preserved.
The compact Light Mode system prompt remains in use. Discovery keeps at most
three specialized tools active: newer discoveries replace older selections.
This limit persists across rounds; repeated searches cannot expand to the full
catalog. The overall context size still depends on conversation and tool data.

Routing can bring a small number of specialized tools into Light Mode instead
of its fixed discovery-only list. Off, Observe and fallback preserve the old
Light Mode list. Explicit admin tool restrictions and feature availability
apply before routing or discovery. Dispatched specialist workers use their own
small tool subset and request state, independent of the parent's presentation.
Light routing uses built-in Grafana tools; internet and Brain Agent integrations
stay excluded, matching the compact prompt's existing boundaries.

## Providers

Configure the **full endpoint URL**, including the decision API path:

| Provider setting | Endpoint example | Model |
| --- | --- | --- |
| Jev / Decider (`systemone`) | `https://api.typesafe.ai/v1/systemone` | Model ID from TypeSafe |
| Jev / Decider (`systemone`) | `http://decider:8000/v1/systemone` | Model ID accepted by the local server |
| Surogate Rune (`surogate`) | `http://rune:8000/v1/decisions` | Served Rune model ID |

The shared HTTP adapter sends `model`, a bounded `state` and a `questions.tools`
Choice with tool names as criteria. It reads `answers.tools` and validates the
choice, confidence and complete probability distribution. HTTP redirects are
refused. More than 254 tool candidates falls back rather than dropping tools
silently. Only one decision request is made per user request, before the tool
rounds and LLM provider retries; discovery can expand the shortlist afterward.

The decision API key is separate from the LLM key and Grafana token. Store it
in Grafana `secureJsonData.decisionApiKey`; all other decision settings belong
in `jsonData`. No new client-side request to the provider is introduced.

The router receives up to 4,000 characters of the current question, four recent
user/assistant messages of up to 1,000 characters each, up to 4,000 characters
of dashboard context and bounded tool descriptions. Recognizable credentials
are redacted before truncation. Attachments, agent runbooks, system prompts and
tool results are excluded. Redaction is best-effort; a hosted endpoint receives
this context at the additional provider. Use a local endpoint for local routing.
The catalog and active tool schemas belong to the individual request, including
parallel conversations and workers.

## Local development without downloading a model

Build with the existing frontend and backend workflows, then start the isolated
Grafana at port 3002:

```sh
npm run build
go run github.com/magefile/mage@v1.17.2 -v
docker compose -p agentai-decision-validation -f docker-compose.decision-routing.yaml up -d
```

This profile explicitly enables routing and Light Mode. Its Python fixture is a
deterministic HTTP contract stub for both decision and chat endpoints, with a
synthetic Prometheus datasource. It performs no model inference, downloads no
weights and sends no prompt to a hosted AI provider. It is not an accuracy or
performance benchmark. The Grafana login is the usual development `admin` /
`admin`; bind it only to localhost. To execute Grafana queries, configure a
Grafana service-account token as described in the main README.

For a real minimum local test, replace the fixture URL/model with a separately
served Decider 2B. The upstream HTTP server supports CPU and GPU; its GGUF
library path is separate from that HTTP server. Rune has a much larger memory
footprint. See the upstream [Decider guide](https://github.com/Mapika/decider),
[Jev API](https://docs.typesafe.ai/introduction/quickstart) and
[Rune API](https://github.com/invergent-ai/surogate/blob/main/docs/inference/decisions.md).

Stop the isolated profile after validation:

```sh
docker compose -p agentai-decision-validation -f docker-compose.decision-routing.yaml down
```

## Validation and measurements

`npm run validate:review` runs the repository's Grafana review checks, builds
the plugin with webpack and mage, packages it and runs the Grafana plugin
validator. Backend tests cover both HTTP envelopes, off/shadow/enforce, fallback,
Light Mode budgets, concurrent discovery, real tool execution against a fixture
Grafana API, and normal/streaming/pseudo tool-call rounds. Frontend tests cover
configuration persistence and separate secure credential storage.

The plugin's existing `/resources/metrics` endpoint additionally exposes:

- `grafana_agentai_decision_requests_total{provider,mode,outcome}`
- `grafana_agentai_decision_duration_seconds{provider}`

Routing logs contain selected names, confidence, outcome and a categorical
fallback reason. They do not include the question, response body or key.
To evaluate usefulness, compare catalog exposure, successful investigations,
valid arguments, prompt tokens and total latency with the same LLM across Off,
Tool Search and Reduce tools. Synthetic fixture success proves the integration,
not better tool selection by a real decision model.

### Local validation record

Validated on 2026-10-05 on `feat/decision-routing`, based on `develop`:

| Check | Result |
| --- | --- |
| `npm run validate:review` | Passed, exit 0; same command used by the repository CI |
| Go checks | vet, formatting, golangci-lint, 545 top-level tests with race detection passed |
| Security | gosec, govulncheck, OSV, gitleaks and both npm audits passed; npm reported zero vulnerabilities |
| Frontend | ESLint, TypeScript and all 95 tests in nine suites passed |
| Package | Production webpack build, all six mage SDK binaries and packaging checks passed |
| Official Grafana validator | Zero errors; one expected unsigned-plugin warning |
| Grafana 12.3.1 runtime | Off, Observe, Reduce tools, both provider paths, streaming, HTTP-error fallback and `__none__` fallback passed |
| Light runtime | Prometheus routing exposed four schemas; response budget remained 750 tokens |

Runtime checks used the Compose fixture and a temporary Viewer service-account
token to execute queries through the real Grafana datasource proxy. The token
was deleted and the isolated containers stopped afterward. No model was loaded
and no model weights were downloaded. The local runtime was Node 22.23.1 and
Go 1.26.8; CI obtains its Go version from `go.mod`.

The review gate also found existing dependency vulnerabilities. OpenTelemetry,
Jest and patched npm dependencies were updated. The root webpack configuration
uses Grafana's scaffold helpers and bundle setup while the mandatory separate
ESLint check replaces the webpack lint plugin's unpatched dependency chain.
Managed `.config` files and the plugin ID/type were kept intact. Grafana signing
and final publication review remain separate from these local checks.
