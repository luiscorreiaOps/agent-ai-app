#!/usr/bin/env python3
"""Local HTTP fixtures for routing smoke tests; no ML inference or dependencies."""

import argparse
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

validation_lock = threading.Lock()
validation = {"decision_requests": 0, "prometheus_requests": 0, "llm_requests": []}


def pick_tool(prompt, names):
    text = prompt.lower()
    for words, tool in [
        (("prometheus", "cpu", "metric", "métrica"), "query_prometheus"),
        (("loki", "logs", "log"), "query_loki"),
        (("tempo", "trace", "rastro"), "query_tempo"),
        (("alert", "alerta"), "list_alerts"),
    ]:
        if tool in names and any(word in text for word in words):
            return tool
    return "__none__"


class FixtureHandler(BaseHTTPRequestHandler):
    def reply(self, status, body):
        encoded = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def do_GET(self):
        if self.path == "/validation":
            # Only schema names and counters: never retain prompts or keys.
            with validation_lock:
                self.reply(200, validation)
        elif self.path == "/v1/models":
            self.reply(200, {"data": [{"id": "fixture"}]})
        elif self.path.startswith("/api/v1/"):
            with validation_lock:
                validation["prometheus_requests"] += 1
            self.reply(200, {"status": "success", "data": {"resultType": "matrix", "result": []}})
        else:
            self.reply(200, {"status": "ok", "fixture": True})

    def do_POST(self):
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if length < 1 or length > 256 * 1024:
                self.reply(413, {"error": "fixture request size limit"})
                return
            body = json.loads(self.rfile.read(length))
            if self.path in ("/v1/systemone", "/v1/decisions"):
                with validation_lock:
                    validation["decision_requests"] += 1
                state = body.get("state", {})
                prompt = state.get("prompt", "") if isinstance(state, dict) else str(state)
                criteria = body["questions"]["tools"]["criteria"]
                choice = pick_tool(prompt, criteria)
                self.reply(200, {"model": "fixture", "answers": {"tools": {
                    "type": "choice", "choice": choice, "confidence": 1,
                    "probabilities": {name: float(name == choice) for name in criteria},
                }}})
            elif self.path == "/v1/chat/completions":
                messages = body.get("messages", [])
                tools = {tool["function"]["name"] for tool in body.get("tools", [])}
                with validation_lock:
                    validation["llm_requests"].append({
                        "tools": sorted(tools),
                        "max_completion_tokens": body.get("max_completion_tokens"),
                    })
                    validation["llm_requests"] = validation["llm_requests"][-100:]
                prompt = next((m.get("content", "") for m in reversed(messages) if m.get("role") == "user"), "")
                completed = any(m.get("role") == "tool" and "search_tools" not in str(m.get("content", "")) for m in messages)
                choice = pick_tool(str(prompt), tools)
                message = {"role": "assistant", "content": "Local fixture validation complete. No generative model inference was performed."}
                finish = "stop"
                if choice != "__none__" and not completed:
                    args = {} if choice == "list_alerts" else {"query": "up"}
                    message = {"role": "assistant", "tool_calls": [{
                        "id": "fixture_call", "type": "function",
                        "function": {"name": choice, "arguments": json.dumps(args)},
                    }]}
                    finish = "tool_calls"
                self.reply(200, {"model": "fixture", "choices": [{"index": 0, "message": message, "finish_reason": finish}],
                                 "usage": {"prompt_tokens": 1, "completion_tokens": 1}})
            else:
                self.reply(404, {"error": "unknown fixture endpoint"})
        except (ValueError, KeyError, TypeError):
            self.reply(400, {"error": "invalid fixture request"})


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8000)
    args = parser.parse_args()
    ThreadingHTTPServer((args.host, args.port), FixtureHandler).serve_forever()
