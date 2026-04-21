#!/usr/bin/env python3
import json
import os
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


PORT = int(os.environ.get("U3_AIMASVC_PORT", "18087"))
DEVICE_ID = os.environ.get("U3_DEVICE_ID", "dev-u3-gb10")


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
        if self.path != "/api/v1/devices/self-register":
            self._json(404, {"error": "not found"})
            return
        length = int(self.headers.get("Content-Length", "0") or "0")
        raw = self.rfile.read(length) if length else b"{}"
        try:
            req = json.loads(raw.decode("utf-8"))
        except Exception:
            req = {}
        expires = (datetime.now(timezone.utc) + timedelta(days=30)).strftime("%Y-%m-%dT%H:%M:%SZ")
        self._json(
            200,
            {
                "device_id": DEVICE_ID,
                "token": "tok-u3",
                "recovery_code": "rec-u3",
                "token_expires_at": expires,
                "poll_interval_seconds": 5,
                "referral_code": "REF-U3",
                "share_text": "share-u3",
                "budget": {
                    "max_tasks": 10,
                    "used_tasks": 0,
                    "budget_usd": 1,
                    "spent_usd": 0,
                    "status": "active",
                    "is_bound": False,
                    "referral_count": 0,
                },
                "debug_echo": {
                    "invite_code": req.get("invite_code", ""),
                    "fingerprint": req.get("fingerprint", ""),
                },
            },
        )


if __name__ == "__main__":
    server = ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
    server.serve_forever()
