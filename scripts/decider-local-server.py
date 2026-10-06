#!/usr/bin/env python3
"""Local development bridge: Decider 2B GGUF -> Jev-compatible tool decisions.

Uses the upstream Decider.system_one readout, never generated chat text.
Install with setup-decider-local.sh. No weights or ML packages enter the plugin.
"""

import argparse
import json
import os
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
import time
import tempfile

MODEL_ID = "Mapika/decider-2b"
MAX_BODY_BYTES = 256 * 1024


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON key")
        result[key] = value
    return result


def reject_constant(_value):
    raise ValueError("non-finite JSON number")


def parse_request(raw, model_id):
    if not raw or len(raw) > MAX_BODY_BYTES:
        raise ValueError("invalid request size")
    body = json.loads(raw, object_pairs_hook=unique_object, parse_constant=reject_constant)
    if not isinstance(body, dict) or body.get("model") != model_id:
        raise ValueError("unknown model")
    if not isinstance(body.get("state"), (str, dict, list)):
        raise ValueError("state must be text, an object or an array")
    questions = body.get("questions")
    # This small development bridge serves the one atomic Choice used by
    # Agent AI. The upstream library supports other decision types too.
    if not isinstance(questions, dict) or len(questions) != 1:
        raise ValueError("one Choice question required")
    question = next(iter(questions.values()))
    if not isinstance(question, dict) or question.get("type") != "choice":
        raise ValueError("Choice required")
    criteria = question.get("criteria")
    if not isinstance(criteria, dict) or not 2 <= len(criteria) <= 255:
        raise ValueError("Choice requires 2 to 255 criteria")
    instructions = question.get("instructions")
    if not isinstance(instructions, (str, dict, list)) or not instructions:
        raise ValueError("instructions required")
    if any(not key or not isinstance(value, str) for key, value in criteria.items()):
        raise ValueError("criteria must map nonempty names to text descriptions")
    return body


class DecisionHandler(BaseHTTPRequestHandler):
    def setup(self):
        super().setup()
        self.connection.settimeout(15)

    def reply(self, status, body):
        raw = json.dumps(body, allow_nan=False).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        try:
            self.wfile.write(raw)
        except (BrokenPipeError, ConnectionResetError):
            pass  # The plugin's decision deadline can expire during inference.

    def do_GET(self):
        if self.path == "/health":
            self.reply(200, {"ok": True, "model": self.server.model_id, "backend": "gguf-cpu"})
        elif self.path == "/v1/models":
            self.reply(200, {"models": [{"name": self.server.model_id}]})
        else:
            self.reply(404, {"error": "unknown endpoint"})

    def do_POST(self):
        if self.path != "/v1/systemone":
            self.reply(404, {"error": "unknown endpoint"})
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if length < 1 or length > MAX_BODY_BYTES:
                self.reply(413, {"error": "invalid request size"})
                return
            body = parse_request(self.rfile.read(length), self.server.model_id)
            started = time.monotonic()
            # Single HTTPServer and n_seq_max=1 serialize model access. The
            # checkpoint's tokenizer, layout and fitted temperatures are used.
            result = self.server.model.system_one(body["state"], body["questions"])
            result["model"] = self.server.model_id
            elapsed = time.monotonic() - started
            answer = next(iter(result["answers"].values()))
            print(json.dumps({"event": "decision", "choice": answer["choice"],
                              "confidence": answer["confidence"], "seconds": round(elapsed, 3),
                              "input_tokens": result["usage"]["input_tokens"]}), flush=True)
            self.reply(200, result)
        except (ValueError, KeyError, TypeError):
            self.reply(422, {"error": "invalid decision request or context exceeds local model limits"})
        except RuntimeError:
            self.reply(503, {"error": "local inference failed"})

    def log_message(self, _format, *_args):
        pass  # Do not log URLs, request bodies, prompts or credentials.


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8003)
    parser.add_argument("--threads", type=int, choices=range(1, 9), default=4)
    parser.add_argument("--model-id", default=MODEL_ID)
    runtime_dir = Path(os.environ.get("AGENTAI_DECIDER_DIR",
                                    str(Path(tempfile.gettempdir()) / f"agentai-decider-2b-{os.getuid()}")))
    parser.add_argument("--model-path", type=Path, default=runtime_dir /
                        "model/decider-2b-v11-Q4_K_M.gguf")
    args = parser.parse_args()
    if not args.model_path.is_file():
        parser.error("model file missing; run scripts/setup-decider-local.sh first")
    import torch
    from decider.infer import Decider
    torch.set_num_threads(args.threads)
    model = Decider(str(args.model_path), device="cpu", use_graphs=False,
                    gguf_options={"n_gpu_layers": 0, "n_threads": args.threads,
                                  "n_ctx": 8192, "n_batch": 8192, "n_seq_max": 1})
    server = HTTPServer((args.host, args.port), DecisionHandler)
    server.model = model
    server.model_id = args.model_id
    print(json.dumps({"event": "ready", "model": args.model_id, "backend": "gguf-cpu",
                      "threads": args.threads, "host": args.host, "port": args.port}), flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
