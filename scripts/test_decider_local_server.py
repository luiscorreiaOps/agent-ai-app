"""Contract checks for the development bridge; no ML runtime or weights needed."""

import importlib.util
import json
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("local_decider", Path(__file__).with_name("decider-local-server.py"))
server = importlib.util.module_from_spec(spec)
spec.loader.exec_module(server)


def payload(**changes):
    body = {"model": server.MODEL_ID, "state": {"prompt": "CPU usage?"},
            "questions": {"tools": {"type": "choice", "instructions": "Which tool?",
                                    "criteria": {"query_prometheus": "Metrics", "__none__": "No tool needed"}}}}
    body.update(changes)
    return json.dumps(body).encode()


class LocalDecisionContract(unittest.TestCase):
    def test_accepts_jev_choice_and_structured_state(self):
        for state in ("CPU usage?", {"prompt": "CPU usage?"}, ["CPU usage?"]):
            self.assertEqual(server.parse_request(payload(state=state), server.MODEL_ID)["state"], state)

    def test_refuses_malformed_and_nonfinite_payloads(self):
        for raw in (b"", b"not json", b"[]", payload(state=True), payload(state=12),
                    payload(model="other"), payload(questions={}),
                    payload().replace(b'"CPU usage?"', b"NaN"),
                    payload().replace(b'"CPU usage?"', b"Infinity"),
                    b"x" * (server.MAX_BODY_BYTES + 1)):
            with self.subTest(raw=raw[:30]), self.assertRaises(ValueError):
                server.parse_request(raw, server.MODEL_ID)

    def test_refuses_duplicate_option_names(self):
        raw = payload().replace(b'"query_prometheus": "Metrics"',
                                b'"query_prometheus": "Metrics", "query_prometheus": "Logs"')
        with self.assertRaises(ValueError):
            server.parse_request(raw, server.MODEL_ID)

    def test_choice_bounds_and_atomic_question(self):
        for size in (1, 256):
            questions = {"tools": {"type": "choice", "instructions": "Which tool?",
                                    "criteria": {str(i): "Tool" for i in range(size)}}}
            with self.assertRaises(ValueError):
                server.parse_request(payload(questions=questions), server.MODEL_ID)
        body = json.loads(payload())
        body["questions"]["other"] = body["questions"]["tools"]
        with self.assertRaises(ValueError):
            server.parse_request(json.dumps(body).encode(), server.MODEL_ID)

    def test_refuses_invalid_instructions_and_descriptions(self):
        for instructions in (False, 42, "", {}, []):
            body = json.loads(payload())
            body["questions"]["tools"]["instructions"] = instructions
            with self.subTest(instructions=instructions), self.assertRaises(ValueError):
                server.parse_request(json.dumps(body).encode(), server.MODEL_ID)
        for criteria in ({"": "Empty name", "ok": "Tool"}, {"a": False, "b": "Tool"}):
            body = json.loads(payload())
            body["questions"]["tools"]["criteria"] = criteria
            with self.assertRaises(ValueError):
                server.parse_request(json.dumps(body).encode(), server.MODEL_ID)


if __name__ == "__main__":
    unittest.main()
