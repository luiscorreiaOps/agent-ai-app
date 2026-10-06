# Changelog

All notable changes to this plugin will be documented in this file, starting from its first public release.

## Unreleased

- Added optional Decision Routing for Jev/Decider and Surogate Rune decision APIs, with Off, Observe and Reduce tools modes, secure credentials, bounded timeouts and fallback.
- Kept Light Mode compact with at most three specialized tools and shortened descriptions when routing succeeds.
- Fixed Tool Search to activate discovered schemas in subsequent normal and streaming chat rounds using an isolated catalog per request.
- Kept specialist worker tool subsets independent of the parent's Light Mode and Tool Search presentation.
- Added routing metrics, regression tests and an isolated local Grafana validation profile using synthetic HTTP fixtures.
- Updated OpenTelemetry to 1.45.0 to fix GO-2026-6505 found by the local review vulnerability checks.
- Updated Jest and vulnerable npm dependencies for the review security gates. The root webpack configuration retains Grafana's scaffold helpers and bundle setup; ESLint runs as a separate mandatory CI check, removing the webpack lint plugin's unpatched dependency.

## 1.0.0

Initial public release.

- AI assistant chat (standalone page, command palette, panel menu, Dashboard Chat) backed by an OpenAI-compatible LLM endpoint of your choice.
- Live, read-only tool-calling against Grafana: datasources, folders, dashboards, Prometheus/Loki/Tempo queries, and alerts -- no fixed assumptions about folder structure or environment naming.
- Automated root-cause investigation: ask about a firing alert and the assistant automatically gathers the relevant logs, traces, and any historical correlation around it in a single step.
- Optional long-term memory via the standalone Brain Agent plugin, when installed and enabled: the assistant can remember facts across sessions, automatically recall relevant memories for the dashboard/panel you're viewing, and queue its own inferred observations for admin review before they're saved.
- Up to 3 custom specialist agents with their own domain context, temperature, and context-window settings.
- Configurable backup/fallback LLM providers, tried automatically if the primary fails before any content has been shown.
- Optional audit logging (metadata by default, full message content opt-in) to Grafana's own backend logs.
- Admin-configurable guardrails appended on top of the built-in safety rules.
- Basic role-awareness (the assistant knows the requester's Grafana role) and settings-save conflict detection for safe multi-admin use.
- Optional zero-config integrations with `grafana-llm-app`, if installed: an additional LLM provider fallback, and (opt-in, off by default) extra read-only tools from its bundled MCP server (OnCall, ClickHouse, CloudWatch, Elasticsearch, roles/permissions, Pyroscope, and more).
- Requires Grafana 12 with a React 19-compatible shared JSX runtime patch: >=12.0.10 <12.1.0, >=12.1.7 <12.2.0, or >=12.2.5. Grafana 11 isn't supported.
- Hardened against two real failure modes found live against a 14B-parameter model (qwen2.5:14b-instruct): a pseudo-tool-call JSON blob leaking into the visible response instead of executing, and a response answering in the wrong language entirely (not just switching mid-answer) relative to the user's prompt. Both are now detected and corrected (one bounded retry for the language case) rather than shown to the user as-is.
- Fixed a real authorization-bypass vulnerability (GO-2026-4762) in a transitive gRPC dependency, found via `govulncheck` -- bumped `google.golang.org/grpc` to the patched version.
