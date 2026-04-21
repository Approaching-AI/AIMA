#!/usr/bin/env python3
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


PORT = int(os.environ.get("U3_OPENAI_PORT", "18089"))
TARGET_HARDWARE = os.environ.get("U3_TARGET_HARDWARE", "nvidia-gb10-arm64")
TARGET_MODEL = os.environ.get("U3_TARGET_MODEL", "gemma-4-26b-a4b-it")
TARGET_ENGINE = os.environ.get("U3_TARGET_ENGINE", "vllm-gemma4-blackwell")


def response_content(system_prompt: str) -> str:
    if "knowledge base analyzer" in system_prompt.lower():
        return json.dumps(
            [
                {
                    "type": "missing_benchmark",
                    "hardware": TARGET_HARDWARE,
                    "model": TARGET_MODEL,
                    "engine": TARGET_ENGINE,
                    "priority": "high",
                    "reasoning": "Freshly registered GB10 device has no local validation evidence yet.",
                    "suggested_action": "Validate the recommended combo and report benchmark evidence.",
                }
            ]
        )
    if "deployment health analyzer" in system_prompt.lower():
        return "[]"
    if "optimization advisor" in system_prompt.lower():
        return json.dumps({"optimizations": [], "reasoning": "No optimization required", "confidence": "low"})
    return json.dumps({"engine": TARGET_ENGINE, "config": {}, "reasoning": "default", "confidence": "low"})


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        return

    def _json(self, status, payload):
        body = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        if self.path != "/v1/chat/completions":
            self._json(404, {"error": "not found"})
            return
        length = int(self.headers.get("Content-Length", "0") or "0")
        raw = self.rfile.read(length) if length else b"{}"
        try:
            req = json.loads(raw.decode("utf-8"))
        except Exception:
            req = {}
        messages = req.get("messages") or []
        system_prompt = ""
        if messages and isinstance(messages[0], dict):
            system_prompt = str(messages[0].get("content", ""))
        content = response_content(system_prompt)
        self._json(
            200,
            {
                "choices": [
                    {
                        "message": {
                            "content": content,
                        }
                    }
                ]
            },
        )


if __name__ == "__main__":
    server = ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
    server.serve_forever()
