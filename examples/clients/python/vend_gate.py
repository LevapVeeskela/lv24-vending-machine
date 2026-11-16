"""Minimal stdlib client for the local vend shadow gate.

The client POSTs one GateRequest JSON document to ``/v1/gate`` on the
loopback API started by ``vend serve``. It is fire-and-forget on purpose:
in M1 the gate is advisory, so a slow, busy or dead gate must never block
the caller's strategy.

Only the Python standard library is used. The bearer token is kept in
memory, never logged and never printed.
"""

from __future__ import annotations

import json
import threading
import urllib.request
from typing import Any, Mapping

DEFAULT_TIMEOUT = 2.0
GATE_PATH = "/v1/gate"


class VendGate:
    """Send shadow-gate requests to a local ``vend serve`` instance.

    Args:
        base_url: API origin, for example ``http://127.0.0.1:8420``. A bare
            ``host:port`` is accepted and gets an ``http://`` prefix.
        token: bearer token expected by the gate.
        timeout: per-request timeout in seconds; must be positive.

    Raises:
        ValueError: on an empty token or base URL, or a non-positive timeout.
    """

    def __init__(self, base_url: str, token: str, timeout: float = DEFAULT_TIMEOUT) -> None:
        if not base_url:
            raise ValueError("base_url is required")
        if not token:
            raise ValueError("token is required")
        if timeout <= 0:
            raise ValueError("timeout must be positive")
        if "://" not in base_url:
            base_url = "http://" + base_url
        self._url = base_url.rstrip("/") + GATE_PATH
        self._token = token
        self._timeout = float(timeout)

    def send(self, payload: Mapping[str, Any]) -> threading.Thread:
        """Queue one gate request and return immediately.

        The payload is serialised before the thread starts, so later mutation
        by the caller cannot leak into the request. The worker is a daemon
        thread: it never keeps the process alive and never raises.

        Returns:
            The worker thread, useful for ``join`` in tests and shutdown code.
        """
        body = self._encode(payload)
        thread = threading.Thread(
            target=self._post,
            args=(body,),
            name="vend-gate",
            daemon=True,
        )
        thread.start()
        return thread

    def send_and_wait(self, payload: Mapping[str, Any]) -> dict[str, Any] | None:
        """POST synchronously; used by tests and batch tools.

        Returns:
            The decoded JSON object on success, or ``None`` on any transport,
            HTTP or decoding error. Never raises.
        """
        return self._post(self._encode(payload))

    @staticmethod
    def _encode(payload: Mapping[str, Any]) -> bytes:
        return json.dumps(payload, separators=(",", ":")).encode("utf-8")

    def _post(self, body: bytes) -> dict[str, Any] | None:
        request = urllib.request.Request(
            self._url,
            data=body,
            method="POST",
            headers={
                "Authorization": f"Bearer {self._token}",
                "Content-Type": "application/json",
                "Accept": "application/json",
            },
        )
        # Deliberate broad catch: a gate outage is not a trading error and
        # must not surface to the strategy. The token never appears in the
        # messages we drop, and nothing is logged here.
        try:
            with urllib.request.urlopen(request, timeout=self._timeout) as response:
                raw = response.read()
        except Exception:  # noqa: BLE001 - fire-and-forget contract
            return None
        try:
            decoded = json.loads(raw)
        except ValueError:
            return None
        return decoded if isinstance(decoded, dict) else None

    def __repr__(self) -> str:
        return f"VendGate(url={self._url!r}, token=***)"
