"""Offline tests for vend_gate.

Every test runs against a real HTTP server bound to 127.0.0.1:0, so no
external network is touched. Run from this directory:

    python -m unittest -v test_vend_gate
"""

from __future__ import annotations

import json
import socket
import threading
import time
import unittest
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from vend_gate import VendGate

TOKEN = "test-token-not-a-secret"

VERDICT_BODY = {
    "mode": "shadow",
    "signalId": "sig-test-1",
    "verdict": {
        "verdict": "ALLOW",
        "code": "ok",
        "reasons": [],
        "checks": [],
        "policyHash": "sha256:test",
        "decidedAt": "2026-11-16T06:00:00Z",
    },
}


def make_payload() -> dict:
    """A GateRequest shaped like contracts/examples/gate-request.json."""
    return {
        "signalId": "sig-test-1",
        "instrument": {"venue": "binance", "symbol": "BTCUSDT"},
        "side": "buy",
        "intentKind": "entry",
        "timeframe": "1h",
        "ts": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "price": "97500.50",
        "source": "test:unit",
        "state": {"rsi14": 62.5},
    }


class RecordingHandler(BaseHTTPRequestHandler):
    """Records the request and answers with a configurable status/body."""

    protocol_version = "HTTP/1.1"

    def do_POST(self) -> None:
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length)
        self.server.requests.append(  # type: ignore[attr-defined]
            {
                "method": self.command,
                "path": self.path,
                "headers": dict(self.headers.items()),
                "body": raw,
            }
        )
        delay = self.server.delay  # type: ignore[attr-defined]
        if delay:
            time.sleep(delay)
        body = self.server.response_body  # type: ignore[attr-defined]
        try:
            self.send_response(self.server.response_status)  # type: ignore[attr-defined]
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError, ConnectionAbortedError):
            # The client already timed out and closed the socket.
            pass

    def log_message(self, *args: object) -> None:
        pass


class VendGateTest(unittest.TestCase):
    def setUp(self) -> None:
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), RecordingHandler)
        self.server.daemon_threads = True
        self.server.requests = []
        self.server.delay = 0.0
        self.server.response_status = 200
        self.server.response_body = json.dumps(VERDICT_BODY).encode("utf-8")
        self._thread = threading.Thread(
            target=self.server.serve_forever, kwargs={"poll_interval": 0.05}, daemon=True
        )
        self._thread.start()
        host, port = self.server.server_address[:2]
        self.base_url = f"http://{host}:{port}"

    def tearDown(self) -> None:
        self.server.shutdown()
        self.server.server_close()
        self._thread.join(5)

    def gate(self, **kwargs) -> VendGate:
        return VendGate(self.base_url, TOKEN, **kwargs)

    def test_send_posts_expected_request(self) -> None:
        payload = make_payload()
        thread = self.gate().send(payload)
        thread.join(5)

        self.assertFalse(thread.is_alive())
        self.assertEqual(len(self.server.requests), 1)
        request = self.server.requests[0]
        self.assertEqual(request["method"], "POST")
        self.assertEqual(request["path"], "/v1/gate")
        self.assertEqual(request["headers"]["Authorization"], f"Bearer {TOKEN}")
        self.assertTrue(
            request["headers"]["Content-Type"].startswith("application/json"),
            request["headers"]["Content-Type"],
        )
        self.assertEqual(json.loads(request["body"]), payload)

    def test_send_is_fire_and_forget(self) -> None:
        self.server.delay = 0.3
        started = time.monotonic()
        thread = self.gate().send(make_payload())
        elapsed = time.monotonic() - started

        self.assertLess(elapsed, 0.2, "send must not wait for the gate")
        self.assertTrue(thread.daemon, "worker must not keep the process alive")
        thread.join(5)
        self.assertFalse(thread.is_alive())
        self.assertEqual(len(self.server.requests), 1)

    def test_send_returns_verdict_on_success(self) -> None:
        result = self.gate().send_and_wait(make_payload())

        self.assertIsNotNone(result)
        self.assertEqual(result["mode"], "shadow")
        self.assertEqual(result["verdict"]["verdict"], "ALLOW")

    def test_server_error_does_not_raise(self) -> None:
        self.server.response_status = 500
        self.server.response_body = json.dumps(
            {"code": "internal", "message": "gate evaluation failed"}
        ).encode("utf-8")

        self.assertIsNone(self.gate().send_and_wait(make_payload()))
        thread = self.gate().send(make_payload())
        thread.join(5)
        self.assertEqual(len(self.server.requests), 2)

    def test_unauthorized_does_not_raise(self) -> None:
        self.server.response_status = 401
        self.server.response_body = json.dumps({"code": "unauthorized"}).encode("utf-8")

        self.assertIsNone(self.gate().send_and_wait(make_payload()))

    def test_timeout_does_not_raise(self) -> None:
        self.server.delay = 0.4
        started = time.monotonic()
        result = self.gate(timeout=0.05).send_and_wait(make_payload())
        elapsed = time.monotonic() - started

        self.assertIsNone(result)
        self.assertLess(elapsed, 0.35, "client must give up at its own timeout")

    def test_connection_refused_does_not_raise(self) -> None:
        probe = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        probe.bind(("127.0.0.1", 0))
        port = probe.getsockname()[1]
        probe.close()

        gate = VendGate(f"http://127.0.0.1:{port}", TOKEN, timeout=0.5)

        self.assertIsNone(gate.send_and_wait(make_payload()))
        gate.send(make_payload()).join(5)

    def test_token_is_not_exposed(self) -> None:
        gate = self.gate()
        self.assertNotIn(TOKEN, repr(gate))
        self.assertNotIn(TOKEN, str(gate))

    def test_bare_host_port_gets_http_scheme(self) -> None:
        host, port = self.server.server_address[:2]
        gate = VendGate(f"{host}:{port}", TOKEN)
        self.assertIsNotNone(gate.send_and_wait(make_payload()))

    def test_empty_token_is_rejected(self) -> None:
        with self.assertRaises(ValueError):
            VendGate(self.base_url, "")


if __name__ == "__main__":
    unittest.main()
