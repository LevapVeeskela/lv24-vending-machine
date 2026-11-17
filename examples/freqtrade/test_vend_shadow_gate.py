"""Offline tests for the Freqtrade shadow-gate example.

Only the Python standard library is used. Freqtrade and pandas are replaced
by minimal interface stubs written here, so the real packages are neither
imported nor required. Every HTTP call goes to a loopback server bound to
127.0.0.1 on a free port.

Waiting is event-driven: the sender exposes ``wait_until_idle``, the handler
reports when it is entered and blocks on an event. No test waits for a fixed
duration to synchronize.

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


class FakeRow(dict):
    """One analyzed candle: column name to value."""


class FakeFrame:
    """Analyzed dataframe stub exposing only ``.iloc[-1]``."""

    def __init__(self, rows: list[FakeRow]) -> None:
        self._rows = rows

    @property
    def iloc(self) -> "FakeILoc":
        return FakeILoc(self._rows)


class FakeILoc:
    """Positional access stub with the one operation the example uses."""

    def __init__(self, rows: list[FakeRow]) -> None:
        self._rows = rows

    def __getitem__(self, index: int) -> FakeRow:
        return self._rows[index]


class FakeDataProvider:
    """Data provider stub recording calls and returning one frame.

    Only ``get_analyzed_dataframe`` exists: the example must not need anything
    else from the data provider.
    """

    def __init__(
        self,
        dataframe: FakeFrame | None = None,
        error: Exception | None = None,
    ) -> None:
        self.dataframe = dataframe
        self.error = error
        self.calls: list[tuple[str, str]] = []

    def get_analyzed_dataframe(self, pair: str, timeframe: str) -> tuple[FakeFrame, datetime]:
        self.calls.append((pair, timeframe))
        if self.error is not None:
            raise self.error
        if self.dataframe is None:
            raise AssertionError("no dataframe configured")
        return self.dataframe, datetime.now(timezone.utc)


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
    """Record requests and answer with the configured status and body.

    When ``server.hold`` is an unset event, the handler reports that it was
    entered and waits for the event before recording and answering. Tests use
    it to hold the sender inside a request without sleeping.
    """

    protocol_version = "HTTP/1.1"

    def do_POST(self) -> None:
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length)
        hold = self.server.hold  # type: ignore[attr-defined]
        if hold is not None:
            self.server.entered.set()  # type: ignore[attr-defined]
            hold.wait(self.server.hold_timeout)  # type: ignore[attr-defined]
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


class FlakyClient:
    """Stand-in for VendGate whose first send raises."""

    def __init__(self) -> None:
        self.calls = 0

    def send_and_wait(self, payload: dict) -> None:
        self.calls += 1
        if self.calls == 1:
            raise RuntimeError("gate failed")


class VendShadowGateTest(unittest.TestCase):
    def setUp(self) -> None:
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), RecordingHandler)
        self.server.daemon_threads = True
        self.server.requests = []
        self.server.received = threading.Event()
        self.server.entered = threading.Event()
        self.server.hold = None
        self.server.hold_timeout = 5.0
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
        hold = self.server.hold
        if hold is not None:
            hold.set()
        self.server.shutdown()
        self.server.server_close()
        self._thread.join(5)

    def use_gate(self, url: str | None = None) -> None:
        os.environ["VEND_GATE_TOKEN"] = TOKEN
        os.environ["VEND_GATE_URL"] = url or self.base_url

    def hold_gate(self) -> threading.Event:
        """Make the handler wait before recording and answering."""
        hold = threading.Event()
        self.server.hold = hold
        self.addCleanup(hold.set)
        return hold

    def strategy(self) -> vend_shadow_gate.VendShadowGate:
        return vend_shadow_gate.VendShadowGate()

    def confirm(
        self,
        strategy: vend_shadow_gate.VendShadowGate,
        rate: float = 97500.5,
    ) -> bool:
        return strategy.confirm_trade_entry(
            pair=PAIR,
            order_type="limit",
            amount=0.01,
            rate=rate,
            time_in_force="GTC",
            current_time=datetime.now(timezone.utc),
            entry_tag=None,
            side="long",
        )

    def strategy_with(
        self,
        row: FakeRow | None = None,
        error: Exception | None = None,
    ) -> vend_shadow_gate.VendShadowGate:
        """Return a strategy whose data provider serves one analyzed candle."""
        strategy = self.strategy()
        dataframe = None if row is None else FakeFrame([row])
        strategy.dp = FakeDataProvider(dataframe, error)
        return strategy

    def settle(self, strategy: vend_shadow_gate.VendShadowGate, count: int) -> None:
        """Wait until the sender is idle and assert how many requests arrived."""
        self.assertTrue(strategy.wait_until_idle(5.0), "sender did not drain")
        self.assertEqual(len(self.server.requests), count)

    def posted_body(self, strategy: vend_shadow_gate.VendShadowGate) -> dict:
        """Confirm one entry and return the gate request body it sent."""
        self.assertTrue(self.confirm(strategy))
        self.settle(strategy, 1)
        return json.loads(self.server.requests[0]["body"])

    def test_confirm_trade_entry_posts_one_valid_gate_request(self) -> None:
        self.use_gate()
        strategy = self.strategy()

        self.assertTrue(self.confirm(strategy))
        self.settle(strategy, 1)

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

    def test_state_carries_last_candle_indicators(self) -> None:
        self.use_gate()
        strategy = self.strategy_with(
            FakeRow({"close": 100.0, "ma_fast": 102.0, "ma_slow": 100.0})
        )

        body = self.posted_body(strategy)

        self.assertEqual(set(body), set(gate_required_fields()))
        state = body["state"]
        self.assertEqual(set(state), {"candle_close", "ma_fast", "ma_slow", "ma_gap"})
        self.assertEqual(state["candle_close"], 100.0)
        self.assertEqual(state["ma_fast"], 102.0)
        self.assertEqual(state["ma_slow"], 100.0)
        self.assertAlmostEqual(state["ma_gap"], 0.02, places=12)
        self.assertEqual(strategy.dp.calls, [(PAIR, "1h")])

    def test_nan_ma_slow_is_dropped_from_state(self) -> None:
        self.use_gate()
        strategy = self.strategy_with(
            FakeRow({"close": 100.0, "ma_fast": 102.0, "ma_slow": float("nan")})
        )

        body = self.posted_body(strategy)

        self.assertEqual(set(body), set(gate_required_fields()))
        self.assertEqual(set(body["state"]), {"candle_close", "ma_fast"})
        self.assertNotIn("NaN", self.server.requests[0]["body"].decode("utf-8"))

    def test_missing_data_provider_sends_empty_state(self) -> None:
        self.use_gate()
        strategy = self.strategy()
        self.assertFalse(hasattr(strategy, "dp"))

        body = self.posted_body(strategy)

        self.assertEqual(set(body), set(gate_required_fields()))
        self.assertEqual(body["state"], {})

    def test_data_provider_error_sends_empty_state_and_allows_entry(self) -> None:
        self.use_gate()
        strategy = self.strategy_with(error=RuntimeError("dataframe unavailable"))

        body = self.posted_body(strategy)

        self.assertEqual(set(body), set(gate_required_fields()))
        self.assertEqual(body["state"], {})

    def test_signal_id_is_unique_per_call(self) -> None:
        self.use_gate()
        strategy = self.strategy()

        self.assertTrue(self.confirm(strategy))
        self.assertTrue(self.confirm(strategy))
        self.settle(strategy, 2)

        first = json.loads(self.server.requests[0]["body"])["signalId"]
        second = json.loads(self.server.requests[1]["body"])["signalId"]
        self.assertNotEqual(first, second)

    def test_sender_is_one_lazy_daemon_thread(self) -> None:
        self.use_gate()
        strategy = self.strategy()

        self.assertFalse(hasattr(strategy, "_vend_sender"))
        self.assertTrue(self.confirm(strategy))
        sender = strategy._vend_sender
        self.assertTrue(sender._thread.daemon)
        self.assertTrue(self.confirm(strategy))
        self.assertIs(strategy._vend_sender, sender)
        self.settle(strategy, 2)

    def test_confirm_returns_while_send_is_blocked(self) -> None:
        self.use_gate()
        self.hold_gate()
        strategy = self.strategy()

        started = time.monotonic()
        self.assertTrue(self.confirm(strategy))
        elapsed = time.monotonic() - started

        self.assertLess(elapsed, vend_shadow_gate.GATE_TIMEOUT_SECONDS)
        self.assertTrue(self.server.entered.wait(5.0), "sender did not reach the gate")
        self.assertEqual(self.server.requests, [])

    def test_signal_arrives_after_gate_unblocks(self) -> None:
        self.use_gate()
        hold = self.hold_gate()
        strategy = self.strategy()

        self.assertTrue(self.confirm(strategy))
        self.assertTrue(self.server.entered.wait(5.0), "sender did not reach the gate")
        self.assertEqual(self.server.requests, [])

        hold.set()

        self.assertTrue(strategy.wait_until_idle(5.0))
        self.assertEqual(len(self.server.requests), 1)

    def test_order_of_two_signals_is_preserved(self) -> None:
        self.use_gate()
        strategy = self.strategy()

        self.assertTrue(self.confirm(strategy, rate=100.0))
        self.assertTrue(self.confirm(strategy, rate=200.0))
        self.settle(strategy, 2)

        prices = [json.loads(request["body"])["price"] for request in self.server.requests]
        self.assertEqual(prices, ["100.00000000", "200.00000000"])

    def test_full_queue_drops_extra_signal_and_allows_entry(self) -> None:
        self.use_gate()
        original_size = vend_shadow_gate.GATE_QUEUE_SIZE
        vend_shadow_gate.GATE_QUEUE_SIZE = 2
        self.addCleanup(setattr, vend_shadow_gate, "GATE_QUEUE_SIZE", original_size)
        hold = self.hold_gate()
        strategy = self.strategy()

        self.assertTrue(self.confirm(strategy, rate=1.0))
        self.assertTrue(self.server.entered.wait(5.0), "sender did not reach the gate")
        # One request is in flight; the queue holds two more.
        self.assertTrue(self.confirm(strategy, rate=2.0))
        self.assertTrue(self.confirm(strategy, rate=3.0))
        # The queue is full: this signal is dropped and the call still returns.
        self.assertTrue(self.confirm(strategy, rate=4.0))

        hold.set()

        self.assertTrue(strategy.wait_until_idle(5.0))
        prices = [json.loads(request["body"])["price"] for request in self.server.requests]
        self.assertEqual(prices, ["1.00000000", "2.00000000", "3.00000000"])

    def test_send_failure_does_not_kill_sender(self) -> None:
        self.use_gate()
        strategy = self.strategy()
        client = FlakyClient()
        strategy._vend_gate = client

        self.assertTrue(self.confirm(strategy, rate=1.0))
        self.assertTrue(strategy.wait_until_idle(5.0))
        self.assertTrue(self.confirm(strategy, rate=2.0))
        self.assertTrue(strategy.wait_until_idle(5.0))

        self.assertEqual(client.calls, 2)
        self.assertEqual(self.server.requests, [])

    def test_closed_port_does_not_block(self) -> None:
        probe = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        probe.bind(("127.0.0.1", 0))
        port = probe.getsockname()[1]
        probe.close()
        self.use_gate(f"http://127.0.0.1:{port}")
        strategy = self.strategy()

        self.assertTrue(self.confirm(strategy))
        self.assertTrue(strategy.wait_until_idle(5.0))

    def test_unauthorized_does_not_block(self) -> None:
        self.server.response_status = 401
        self.server.response_body = json.dumps({"code": "unauthorized"}).encode("utf-8")
        self.use_gate()
        strategy = self.strategy()

        self.assertTrue(self.confirm(strategy))
        self.settle(strategy, 1)

    def test_server_error_does_not_block(self) -> None:
        self.server.response_status = 500
        self.server.response_body = json.dumps({"code": "internal"}).encode("utf-8")
        self.use_gate()
        strategy = self.strategy()

        self.assertTrue(self.confirm(strategy))
        self.settle(strategy, 1)

    def test_missing_token_disables_reporting(self) -> None:
        os.environ.pop("VEND_GATE_TOKEN", None)
        os.environ["VEND_GATE_URL"] = self.base_url
        strategy = self.strategy()

        self.assertTrue(self.confirm(strategy))

        self.assertFalse(hasattr(strategy, "_vend_sender"))
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
        strategy = self.strategy()

        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            self.assertTrue(self.confirm(strategy))
            self.assertTrue(strategy.wait_until_idle(5.0))
            self.server.response_status = 401
            self.assertTrue(self.confirm(strategy))
            self.assertTrue(strategy.wait_until_idle(5.0))

        self.assertEqual(len(self.server.requests), 2)
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
