"""Freqtrade strategy that reports entries to the local LV24 shadow gate.

Written from scratch for the public IStrategy interface; no Freqtrade source
is vendored here. The gate is advisory in M1: ``confirm_trade_entry`` always
returns True, and a missing, slow or failing gate never blocks a trade.

Environment:
    VEND_GATE_TOKEN  bearer token of the local gate; without it the strategy
                     runs normally and sends nothing.
    VEND_GATE_URL    gate origin, default ``http://127.0.0.1:8420``.

Copy this file and ``vend_gate.py`` into your Freqtrade
``user_data/strategies/`` directory. See ``docs/recipes/freqtrade.md``.
"""

import math
import os
import sys
import uuid
from datetime import datetime, timezone
from pathlib import Path

from freqtrade.strategy import IStrategy
from pandas import DataFrame

sys.path.insert(0, str(Path(__file__).resolve().parent))
from vend_gate import VendGate  # noqa: E402

DEFAULT_GATE_URL = "http://127.0.0.1:8420"
GATE_TIMEOUT_SECONDS = 2.0

# Candle column -> state key, copied from the last analyzed candle. ``ma_gap``
# is derived from the two averages and needs both of them. The candle close is
# sent as ``candle_close``: the gate fills ``close`` itself from ``price`` and
# would overwrite a key with that name.
STATE_COLUMNS = (
    ("close", "candle_close"),
    ("ma_fast", "ma_fast"),
    ("ma_slow", "ma_slow"),
)


def finite_number(value: object) -> float | None:
    """Return value as a finite float, or None when it is not one.

    The gate contract accepts only finite JSON numbers, so NaN, infinities,
    None and non-numeric values are dropped instead of being sent.
    """
    try:
        number = float(value)
    except (TypeError, ValueError):
        return None
    if not math.isfinite(number):
        return None
    return number


class VendShadowGate(IStrategy):
    timeframe = "1h"
    startup_candle_count = 60
    minimal_roi = {"0": 10}
    stoploss = -0.10
    process_only_new_candles = True
    use_exit_signal = False

    def _gate(self) -> VendGate | None:
        """Return the cached client, or None when reporting is not configured.

        The token is read from the environment on first use and kept in
        memory only. A missing token or an invalid URL disables reporting
        instead of breaking the strategy.
        """
        client = getattr(self, "_vend_gate", None)
        if client is not None:
            return client
        token = os.environ.get("VEND_GATE_TOKEN", "").strip()
        if not token:
            return None
        try:
            client = VendGate(
                base_url=os.environ.get("VEND_GATE_URL", DEFAULT_GATE_URL),
                token=token,
                timeout=GATE_TIMEOUT_SECONDS,
            )
        except ValueError:
            return None
        self._vend_gate = client
        return client

    def _market_state(self, pair: str) -> dict[str, float]:
        """Return the finite indicators of the last analyzed candle for pair.

        The candle comes from the strategy's public data provider interface:
        ``get_analyzed_dataframe(pair, timeframe)`` returns the analyzed frame
        and the time of its last candle. Only the last row is read, and only
        finite numbers are kept: ``state`` on the wire is a plain object of
        finite numbers.

        Any failure (no data provider, an empty frame, a missing column, a
        non-finite value, an exception) yields ``{}``. Reporting context must
        never block or fail an entry.
        """
        try:
            dataprovider = getattr(self, "dp", None)
            if dataprovider is None:
                return {}
            dataframe, _ = dataprovider.get_analyzed_dataframe(pair, self.timeframe)
            candle = dataframe.iloc[-1]
            state: dict[str, float] = {}
            for column, key in STATE_COLUMNS:
                number = finite_number(candle.get(column))
                if number is not None:
                    state[key] = number
            fast = state.get("ma_fast")
            slow = state.get("ma_slow")
            if fast is not None and slow is not None and slow != 0.0:
                gap = finite_number(fast / slow - 1.0)
                if gap is not None:
                    state["ma_gap"] = gap
            return state
        except Exception:  # noqa: BLE001 - context collection never blocks an entry
            return {}

    def populate_indicators(self, dataframe: DataFrame, metadata: dict) -> DataFrame:
        dataframe["ma_fast"] = dataframe["close"].rolling(12).mean()
        dataframe["ma_slow"] = dataframe["close"].rolling(48).mean()
        return dataframe

    def populate_entry_trend(self, dataframe: DataFrame, metadata: dict) -> DataFrame:
        dataframe.loc[
            (dataframe["ma_fast"] > dataframe["ma_slow"])
            & (dataframe["ma_fast"].shift(1) <= dataframe["ma_slow"].shift(1)),
            "enter_long",
        ] = 1
        return dataframe

    def confirm_trade_entry(
        self,
        pair: str,
        order_type: str,
        amount: float,
        rate: float,
        time_in_force: str,
        current_time: datetime,
        entry_tag: str | None,
        side: str,
        **kwargs,
    ) -> bool:
        payload = {
            # Unique per entry attempt: reusing an id with another body is a
            # conflict on the gate side.
            "signalId": f"freqtrade-{uuid.uuid4().hex}",
            # pair is sent exactly as Freqtrade gives it; the core
            # canonicalises BTC/USDT, BTC-USDT, BTCUSDT and BTCUSDT:USDT.
            "instrument": {"venue": "freqtrade", "symbol": pair},
            "side": "buy" if side == "long" else "sell",
            "intentKind": "entry",
            "timeframe": self.timeframe,
            # ts is the moment the signal became known, in UTC.
            "ts": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
            # Money on the wire is a decimal string, not a float.
            "price": f"{float(rate):.8f}",
            "source": "freqtrade:VendShadowGate",
            # Market context from the last analyzed candle; {} when it cannot
            # be read. The gate accepts finite numbers only.
            "state": self._market_state(pair),
        }
        client = self._gate()
        if client is not None:
            try:
                # Fire-and-forget: the answer is not awaited.
                client.send(payload)
            except Exception:  # noqa: BLE001 - the gate must never block a trade
                pass
        # Shadow mode: entries are always allowed; the verdict is journaled.
        return True
