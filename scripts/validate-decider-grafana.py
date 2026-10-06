#!/usr/bin/env python3
"""Smoke the isolated Grafana profile with real Decider routing and a chat fixture."""

import base64
import json
import time
import urllib.request

GRAFANA = "http://127.0.0.1:3002"
FIXTURE = "http://127.0.0.1:8000"
PLUGIN = "shortbobcat2735-agentai-app"
SETTINGS = f"/api/plugins/{PLUGIN}/settings"
RESOURCES = f"/api/plugins/{PLUGIN}/resources"
AUTH = "Basic " + base64.b64encode(b"admin:admin").decode()


def request(path, body=None, method=None, root=GRAFANA, decode=True):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(root + path, data=data, method=method,
                                 headers={"Authorization": AUTH, "Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as response:
        raw = response.read().decode()
    return json.loads(raw) if decode else raw


def main():
    for _ in range(30):
        try:
            health = request("/api/health")
            break
        except OSError:
            time.sleep(1)
    else:
        raise RuntimeError("isolated Grafana did not become healthy")
    original = request(SETTINGS)["jsonData"]
    # Refuse to modify settings unless this is the isolated dev profile.
    assert health["version"] == "12.3.1", health
    assert original["endpointURL"] == FIXTURE + "/v1", "expected the chat fixture"
    assert original["decisionEndpointURL"] == "http://127.0.0.1:8003/v1/systemone"
    assert original["decisionModel"] == "Mapika/decider-2b"
    assert original["lightModeForDefaultAgent"] is True
    assert original["decisionRoutingMode"] == "enforce"
    account = request("/api/serviceaccounts", {"name": "decider-validation-" + str(time.time_ns()), "role": "Viewer"})
    try:
        token = request(f"/api/serviceaccounts/{account['id']}/tokens",
                        {"name": "local-validation", "secondsToLive": 3600})["key"]
        # Grafana stores app-settings update times with second precision.
        time.sleep(1.1)
        request(SETTINGS, {"enabled": True, "jsonData": dict(original, grafanaURL=GRAFANA, responseLanguage="en"),
                           "secureJsonData": {"grafanaToken": token}})
        checks = []
        for streaming in (False, True):
            before = request("/validation", root=FIXTURE)
            raw = request(RESOURCES + ("/chat/stream" if streaming else "/chat"),
                          {"mode": "chat", "prompt": "Use query_prometheus to run the PromQL expression up.",
                           "context": {}}, decode=False)
            chunks = [json.loads(line) for line in raw.splitlines() if line.strip()] if streaming else [json.loads(raw)]
            assert any(c.get("done") for c in chunks), chunks
            after = request("/validation", root=FIXTURE)
            calls = after["llm_requests"][len(before["llm_requests"]):]
            assert calls and "query_prometheus" in calls[0]["tools"], calls
            assert all(len(c["tools"]) <= 6 and c["max_completion_tokens"] <= 750 for c in calls), calls
            assert after["prometheus_requests"] == before["prometheus_requests"] + 1, after
            assert after["decision_requests"] == before["decision_requests"], "decision must use the real model"
            if streaming:
                assert any(c.get("toolCall", {}).get("name") == "query_prometheus" for c in chunks), chunks
                assert any(any("/api/v1/query_range" in p for p in c.get("toolResult", {}).get("apiCalls", []))
                           for c in chunks), chunks
            metrics = request(RESOURCES + "/metrics", decode=False)
            assert 'outcome="selected"' in metrics and 'provider="systemone"' in metrics, metrics
            checks.append({"streaming": streaming, "tools": calls[0]["tools"], "response_token_limit": 750})
        print(json.dumps({"grafana": health["version"], "decision_model": "Mapika/decider-2b",
                          "real_decision_inference": True, "generative_model": "fixture",
                          "light_mode": True, "checks": checks}, indent=2))
    finally:
        try:
            time.sleep(1.1)
            request(SETTINGS, {"enabled": True, "jsonData": original, "secureJsonData": {"grafanaToken": ""}})
        finally:
            request(f"/api/serviceaccounts/{account['id']}", method="DELETE")


if __name__ == "__main__":
    main()
