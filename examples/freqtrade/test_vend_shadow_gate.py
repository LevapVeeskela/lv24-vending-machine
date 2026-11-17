"""Offline tests for the Freqtrade shadow-gate example.

Only the Python standard library is used. Freqtrade and pandas are replaced
by minimal interface stubs written here, so the real packages are neither
imported nor required. Every HTTP call goes to a loopback server bound to
127.0.0.1 on a free port.

Run from the repository root:

    python -m unittest discover -s examples -p "test_*.py"
"""

from __future__ import annotations

import contextlib
import io
import json
import logging
import os
import socket
import sys
import threading
import time
import types
import unittest
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
EXAMPLE_DIR = Path(__file__).resolve().parent
CLIENT_DIR = ROOT / "examples" / "clients" / "python"
CONTRACT = ROOT / "contracts" / "jsonschema" / "gate" / "v1.json"

sys.path.insert(0, str(EXAMPLE_DIR))
sys.path.insert(0, str(CLIENT_DIR))


def install_interface_stubs() -> None:
    """Install minimal freqtrade.strategy and pandas stubs in sys.modules.

    The stubs carry names only: no Freqtrade or pandas behaviour is copied.
    They are installed before the example is imported.
    """
    # Under `unittest discover -s examples` this test module is imported as
    # part of the examples/freqtrade package, so reuse that module object and
    # attach the interface stub to it. When run directly, create it.
    freqtrade = sys.modules.get("freqtrade")
    if freqtrade is None:
        freqtrade = types.ModuleType("freqtrade")
        sys.modules["freqtrade"] = freqtrade
    strategy = types.ModuleType("freqtrade.strategy")

    class IStrategy:  # noqa: D101 - interface stub, no logic
        pass

    strategy.IStrategy = IStrategy
    freqtrade.strategy = strategy

    pandas = types.ModuleType("pandas")

    class DataFrame:  # noqa: D101 - interface stub, no logic
        pass

    pandas.DataFrame = DataFrame

    sys.modules["freqtrade"] = freqtrade
    sys.modules["freqtrade.strategy"] = strategy
    sys.modules["pandas"] = pandas


install_interface_stubs()

import vend_shadow_gate  # noqa: E402  (import after the stubs)


TOKEN = "test-token-not-a-secret"
PAIR = "BTC/USDT:USDT"
VERDICT_BODY = {
    "mode": "shadow",
    "signalId": "sig-example-1",
    "verdict": {
        "verdict": "ALLOW",
        "code": "allow",
        "reasons": ["all gate checks passed"],
        "checks": [],
        "policyHash": "0" * 64,
        "decidedAt": "2026-11-17T08:00:00Z",
    },
}


def gate_required_fields() -> list[str]:
    """Return the required top-level fields of the gate request contract."""
    contract = json.loads(CONTRACT.read_text(encoding="utf-8"))
    return list(contract["required"])


class RecordingHandler(BaseHTTPRequestHandler):
    """Record one request and answer with the configured status and body."""

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
        body = self.server.response_body  # type: ignore[attr-defined]
        try:
            self.send_response(self.server.response_status)  # type: ignore[attr-defined]
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError, ConnectionAbortedError):
            pass
        finally:
            self.server.received.set()  # type: ignore[attr-defined]

    def log_message(self, *args: object) -> None:
        pass


class VendShadowGateTest(unittest.TestCase):
    def setUp(self) -> None:
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), RecordingHandler)
        self.server.daemon_threads = True
        self.server.requests = []
        self.server.received = threading.Event()
        self.server.response_status = 200
        self.server.response_body = json.dumps(VERDICT_BODY).encode("utf-8")
        self._thread = threading.Thread(
            target=self.server.serve_forever,
            kwargs={"poll_interval": 0.05},
            daemon=True,
        )
        self._thread.start()
        host, port = self.server.server_address[:2]
        self.base_url = f"http://{host}:{port}"
        self.addCleanup(self._stop_server)
        self.addCleanup(os.environ.pop, "VEND_GATE_TOKEN", None)
        self.addCleanup(os.environ.pop, "VEND_GATE_URL", None)

    def _stop_server(self) -> None:
        self.server.shutdown()
        self.server.server_close()
        self._thread.join(5)

    def use_gate(self, url: str | None = None) -> None:
        os.environ["VEND_GATE_TOKEN"] = TOKEN
        os.environ["VEND_GATE_URL"] = url or self.base_url

    def strategy(self) -> vend_shadow_gate.VendShadowGate:
        return vend_shadow_gate.VendShadowGate()

    def confirm(self, strategy: vend_shadow_gate.VendShadowGate) -> bool:
        return strategy.confirm_trade_entry(
            pair=PAIR,
            order_type="limit",
            amount=0.01,
            rate=97500.5,
            time_in_force="GTC",
            current_time=datetime.now(timezone.utc),
            entry_tag=None,
            side="long",
        )

    def wait_for_requests(self, count: int, timeout: float = 5.0) -> None:
        deadline = time.monotonic() + timeout
        while len(self.server.requests) < count and time.monotonic() < deadline:
            self.server.received.wait(0.05)
            self.server.received.clear()
        self.assertEqual(len(self.server.requests), count)

    def test_confirm_trade_entry_posts_one_valid_gate_request(self) -> None:
        self.use_gate()

        self.assertTrue(self.confirm(self.strategy()))
        self.wait_for_requests(1)

        request = self.server.requests[0]
        self.assertEqual(request["method"], "POST")
        self.assertEqual(request["path"], "/v1/gate")
        self.assertEqual(request["headers"]["Authorization"], f"Bearer {TOKEN}")
        self.assertTrue(request["headers"]["Content-Type"].startswith("application/json"))

        body = json.loads(request["body"])
        self.assertEqual(set(body), set(gate_required_fields()))
        self.assertEqual(set(body["instrument"]), {"venue", "symbol"})
        self.assertEqual(body["instrument"]["symbol"], PAIR)
        self.assertEqual(body["side"], "buy")
        self.assertEqual(body["intentKind"], "entry")
        self.assertEqual(body["timeframe"], "1h")
        self.assertEqual(body["source"], "freqtrade:VendShadowGate")
        self.assertEqual(body["state"], {})
        self.assertEqual(body["price"], "97500.50000000")
        self.assertTrue(body["signalId"].startswith("freqtrade-"))

        ts = datetime.strptime(body["ts"], "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=timezone.utc)
        self.assertLess(abs((datetime.now(timezone.utc) - ts).total_seconds()), 5.0)

    def test_signal_id_is_unique_per_call(self) -> None:
        self.use_gate()
        strategy = self.strategy()

        self.assertTrue(self.confirm(strategy))
        self.assertTrue(self.confirm(strategy))
        self.wait_for_requests(2)

        first = json.loads(self.server.requests[0]["body"])["signalId"]
        second = json.loads(self.server.requests[1]["body"])["signalId"]
        self.assertNotEqual(first, second)

    def test_closed_port_does_not_block_and_respects_timeout(self) -> None:
        probe = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        probe.bind(("127.0.0.1", 0))
        port = probe.getsockname()[1]
        probe.close()
        self.use_gate(f"http://127.0.0.1:{port}")

        started = time.monotonic()
        self.assertTrue(self.confirm(self.strategy()))
        elapsed = time.monotonic() - started
        self.assertLess(elapsed, vend_shadow_gate.GATE_TIMEOUT_SECONDS + 0.5)

    def test_unauthorized_does_not_block(self) -> None:
        self.server.response_status = 401
        self.server.response_body = json.dumps({"code": "unauthorized"}).encode("utf-8")
        self.use_gate()

        self.assertTrue(self.confirm(self.strategy()))
        self.wait_for_requests(1)

    def test_server_error_does_not_block(self) -> None:
        self.server.response_status = 500
        self.server.response_body = json.dumps({"code": "internal"}).encode("utf-8")
        self.use_gate()

        self.assertTrue(self.confirm(self.strategy()))
        self.wait_for_requests(1)

    def test_missing_token_disables_reporting(self) -> None:
        os.environ.pop("VEND_GATE_TOKEN", None)
        os.environ["VEND_GATE_URL"] = self.base_url

        self.assertTrue(self.confirm(self.strategy()))
        time.sleep(0.2)
        self.assertEqual(self.server.requests, [])

    def test_token_is_not_printed_or_logged(self) -> None:
        self.use_gate()
        records: list[logging.LogRecord] = []

        class Capture(logging.Handler):
            def emit(self, record: logging.LogRecord) -> None:
                records.append(record)

        handler = Capture()
        root = logging.getLogger()
        root.addHandler(handler)
        self.addCleanup(root.removeHandler, handler)

        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            self.assertTrue(self.confirm(self.strategy()))
            self.wait_for_requests(1)
            self.server.response_status = 401
            self.assertTrue(self.confirm(self.strategy()))
            self.wait_for_requests(2)

        captured = out.getvalue() + err.getvalue()
        captured += "".join(record.getMessage() for record in records)
        self.assertNotIn(TOKEN, captured)

    def test_example_uses_interface_stubs(self) -> None:
        self.assertIs(
            vend_shadow_gate.IStrategy,
            sys.modules["freqtrade.strategy"].IStrategy,
        )
        self.assertIs(vend_shadow_gate.DataFrame, sys.modules["pandas"].DataFrame)

    def test_recipe_block_matches_example_file(self) -> None:
        recipes = sorted((ROOT / "docs" / "recipes").glob("freqtrade*.md"))
        self.assertTrue(recipes, "no Freqtrade recipe found")
        expected = Path(__file__).with_name("vend_shadow_gate.py").read_text(encoding="utf-8")

        for recipe in recipes:
            with self.subTest(recipe=recipe.name):
                text = recipe.read_text(encoding="utf-8")
                start = text.index("<!-- vend-example: examples/freqtrade/vend_shadow_gate.py -->")
                end = text.index("<!-- /vend-example -->", start)
                block = text[start:end].splitlines()[1:]
                self.assertEqual(block[0], "```python")
                self.assertEqual(block[-1], "```")
                self.assertEqual("\n".join(block[1:-1]).strip("\n"), expected.strip("\n"))


if __name__ == "__main__":
    unittest.main()
