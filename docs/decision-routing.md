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

For a real minimum local test, use the Decider 2B setup below. The upstream HTTP
server supports CPU and GPU; its GGUF library path is separate from that HTTP
server. Rune has a much larger memory footprint. See the upstream [Decider guide](https://github.com/Mapika/decider),
[Jev API](https://docs.typesafe.ai/introduction/quickstart) and
[Rune API](https://github.com/invergent-ai/surogate/blob/main/docs/inference/decisions.md).

Stop the isolated profile after validation:

```sh
docker compose -p agentai-decision-validation -f docker-compose.decision-routing.yaml down
```

## Real Decider 2B validation

The optional AVX2-capable Linux x86_64 development setup uses Decider 2B v11 Q4_K_M, a
1.27 GB quantized checkpoint, with four CPU threads and no GPU. Prerequisites:
`uv`, `curl`, `cmake`, a C++ compiler and `sha256sum`. Run from the repository:

```sh
bash scripts/setup-decider-local.sh
bash scripts/run-decider-local.sh --threads 4
```

Setup pins `decider-ai` 1.8.1, `llama-cpp-python` 0.3.36, CPU Torch 2.14.1,
Transformers 5.18.0 and NumPy 2.5.3. The model revision and SHA-256 checksums
are pinned too. Weights, interpreter and packages live outside the checkout,
under `/tmp/agentai-decider-2b-$(id -u)` by default. Set `AGENTAI_DECIDER_DIR`
in both terminals to use another directory. Nothing is added to the plugin's
bundle, dependencies or normal runtime. The review cleanup cannot delete this
external model directory.

The development bridge listens at `127.0.0.1:8003/v1/systemone`. It delegates
to upstream `Decider.system_one`, using the checkpoint's tokenizer, prompt
layout and fitted temperatures, and returns its actual Choice probabilities.
It does not generate chat text or fabricate a ranking. It supports the one
atomic Choice question used by Agent AI, serializes inference and runs offline
after installation. This bridge is a development tool, not a production server.

In another terminal, validate the **actual plugin-generated HTTP payload**:

```sh
# Minimum test: three candidate tools plus __none__.
AGENTAI_DECISION_LIVE_URL=http://127.0.0.1:8003/v1/systemone \
  go test -v -count=1 ./pkg/plugin -run '^TestDecisionLocalModel$'

# Full catalog; extra time is allowed only in this response-validation test.
AGENTAI_DECISION_LIVE_URL=http://127.0.0.1:8003/v1/systemone \
  AGENTAI_DECISION_LIVE_FULL_CATALOG=1 AGENTAI_DECISION_LIVE_TIMEOUT=120s \
  go test -v -count=1 ./pkg/plugin -run '^TestDecisionLocalModel$'
```

The samples cover Prometheus requests in English and Portuguese, Loki, Tempo
and a request needing no tool. The minimum test checks expected choices and
abstention; the full catalog validates the contract, since overlapping
specialist tools can provide alternative valid next steps. Both validate through the
plugin's existing HTTP adapter, including known criteria, finite confidence,
complete normalized probabilities and the maximum-probability choice. Set
`AGENTAI_DECISION_LIVE_ARTIFACTS` to an **absolute** directory to save these
synthetic payloads and parsed answers; headers and credentials are never saved.
Normal CI skips live inference and downloads no model. CPU latency is not an
acceptance criterion for this contract validation; the test timeout override
does not change the plugin's production limits.

To validate the real decision model inside Grafana, keep the model server
running and start the separate profile after building the plugin:

```sh
docker compose -p agentai-decider-real -f docker-compose.decider-local.yaml up -d
python3 scripts/validate-decider-grafana.py
docker compose -p agentai-decider-real -f docker-compose.decider-local.yaml down
```

This profile uses Linux host networking with Grafana at `127.0.0.1:3002` and
the synthetic chat/Prometheus fixture at `127.0.0.1:8000`. Only routing uses
the real Decider model. Light Mode and Tool Search are enabled, with a small
explicit development allowlist and a 10-second decision deadline. The smoke
check verifies normal and streaming requests, real Grafana datasource proxy
queries, at most six LLM tool schemas and the 750-token Light response budget.
It creates a temporary Viewer service account and removes it afterward. Stop
the CPU model with Ctrl+C when finished.

### Jev prompt compatibility

The router uses the documented [Jev request schema](https://docs.typesafe.ai/api):
`model`, structured `state` and a named `questions.tools` with `type: choice`,
an atomic next-tool instruction and tool descriptions in `criteria`. It includes
`__none__` and obeys the 255-option limit. The
[Choice primitive](https://docs.typesafe.ai/primitives/choice) chooses one best
next step: ranked probabilities are competing alternatives, rather than
independent relevance scores. Agent AI uses the top alternatives as LLM
candidates and leaves tool arguments and execution to its usual loop.

Local Decider inference validates the real payload and readout. It does not
constitute a live test of the hosted Jev service; that requires its model ID
and credentials.

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
was deleted and the isolated containers stopped afterward. That fixture-only
phase loaded no model and downloaded no weights. The local runtime was Node
22.23.1 and Go 1.26.8; CI obtains its Go version from `go.mod`.

The review gate also found existing dependency vulnerabilities. OpenTelemetry,
Jest and patched npm dependencies were updated. The root webpack configuration
uses Grafana's scaffold helpers and bundle setup while the mandatory separate
ESLint check replaces the webpack lint plugin's unpatched dependency chain.
Managed `.config` files and the plugin ID/type were kept intact. Grafana signing
and final publication review remain separate from these local checks.

### Real Decider follow-up

Validated Decider 2B v11 Q4_K_M on the same branch on 2026-10-05, using
upstream GGUF inference and the real plugin-generated request:

| Check | Result |
| --- | --- |
| Minimum live catalog | All five expected choices passed: Prometheus in EN/PT, Loki, Tempo and `__none__` |
| Full live catalog | All five response-contract checks passed with 32 tools plus `__none__`; included conversation history and dashboard context |
| Grafana 12.3.1 + real Decider | Normal and streaming flows passed; queries used the real datasource proxy with a temporary Viewer token |
| Light Mode | Four schemas (`list_alerts`, `list_datasources`, `query_prometheus`, `search_tools`); 750-token response budget preserved |
| Bridge unit checks | All five dependency-free checks passed |
| Follow-up `npm run validate:review` | Passed: Go race tests, security checks, 95 frontend tests, production build, six backend binaries and package validation |
| Follow-up official Grafana validator | Zero errors; one expected unsigned-plugin warning |
| Follow-up npm audits | Zero vulnerabilities, including development dependencies |

Full-catalog answers included `analyze_log_patterns` ahead of `query_loki` for
the log request and `analyze_metric_anomaly` for the Portuguese metrics request
with dashboard context. These were valid contract responses; the full-catalog
check is not an accuracy benchmark or an assertion that every preferred tool
is ranked first. CPU duration was excluded from the acceptance criteria.
Both sample payloads and parsed responses were captured outside the repository;
no real user prompts or credentials were recorded. Containers and the temporary
service account were removed after the Grafana check.

The follow-up npm audit reported additional advisories. Overrides select the
patched [KaTeX 0.18.2](https://github.com/advisories/GHSA-238p-pmpm-9mq7),
[PostCSS selector parser 7.1.6](https://github.com/advisories/GHSA-rj75-hqrm-r3gf)
and [source-map-js 1.2.2](https://github.com/advisories/GHSA-68fv-2mgg-jv7q).
