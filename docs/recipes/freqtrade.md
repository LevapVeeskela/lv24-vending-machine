# Recipe: Freqtrade → shadow gate

[Русская версия](freqtrade.ru.md)

How to connect Freqtrade to the local LV24 shadow gate: the strategy stays as
it is, and on entry it sends one `GateRequest` to `vend serve` and **does not
wait for the answer**. The verdict is written to the journal for later
labeling and reporting; in M1 it does not affect the trade itself.

> The example is written from scratch against the public `IStrategy`
> interface. Freqtrade code (GPL-3.0) is not copied or vendored into this
> repository: we only import its classes and implement our own
> `confirm_trade_entry` hook.

## What you need

- The Go core with the local API: `vend` from this repository.
- Python 3.10+ and an installed Freqtrade (its dependencies are the user's
  responsibility).
- The client
  [`examples/clients/python/vend_gate.py`](../../examples/clients/python/vend_gate.py)
  — Python standard library only.

## 1. Start the gate

The token is required and the server listens on loopback only:

```bash
go build -o vend ./core/cmd/vend
VENDING_SECRET_CONN_LOCAL_APITOKEN=<token> ./vend serve   # 127.0.0.1:8420
```

Liveness check (no authorization):

```bash
curl -s http://127.0.0.1:8420/healthz
# {"status":"ok","mode":"shadow","version":"..."}
```

Put the same token into the Freqtrade process environment, for example
`VEND_GATE_TOKEN`. The token never goes into the strategy config or the
repository.

## 2. Put the client next to the strategy

Copy `vend_gate.py` and `vend_shadow_gate.py` from `examples/freqtrade/` into
`user_data/strategies/` (or add both paths to `sys.path`).

## 3. Minimal strategy

The ready file is
[`examples/freqtrade/vend_shadow_gate.py`](../../examples/freqtrade/vend_shadow_gate.py).
It is shown in full below; the test
`examples/freqtrade/test_vend_shadow_gate.py` compares this block with the
file and fails on any difference.

The example was checked against minimal `IStrategy` and `DataFrame` interface
stubs written in the test; the author has not run it with real Freqtrade.

<!-- vend-example: examples/freqtrade/vend_shadow_gate.py -->
```python
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

# Indicators copied from the last analyzed candle. ``ma_gap`` is derived from
# the two averages and needs both of them.
STATE_COLUMNS = ("close", "ma_fast", "ma_slow")


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
            for column in STATE_COLUMNS:
                number = finite_number(candle.get(column))
                if number is not None:
                    state[column] = number
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
```
<!-- /vend-example -->

If you need the real exchange venue, put it into `instrument.venue` (for
example, from `self.config["exchange"]["name"]`), but do not change `pair`:
the symbol is sent exactly as Freqtrade provides it.

### What goes into `state`

On every entry the strategy reads the last analyzed candle through the public
data provider call `self.dp.get_analyzed_dataframe(pair, self.timeframe)` and
sends four numbers:

| Key | Value |
|---|---|
| `close` | close of the last analyzed candle |
| `ma_fast` | the 12-bar mean computed in `populate_indicators` |
| `ma_slow` | the 48-bar mean computed in `populate_indicators` |
| `ma_gap` | `ma_fast / ma_slow − 1` |

Only finite numbers are sent: NaN, infinities, `None` and non-numeric values
are dropped key by key. `ma_gap` is omitted when `ma_fast` or `ma_slow` is
missing, non-finite, or `ma_slow` is zero. If the data provider is absent, the
frame is empty, or anything raises, the request still goes with `state: {}`
and the entry is still allowed.

You can add your own numeric features to `state`: use snake_case keys, finite
numbers and a flat object. A number is required, not a string:
`{"rsi_14": 55.5}` is a number on the gate side, `{"rsi_14": "55.5"}` is not.

The author did not select these four keys as predictors and did not measure
how they affect the verdict or its quality. They are a minimal example of
passing market context, not a feature set.

### Symbols

The core compares instruments in canonical form: trim, upper case, remove the
`-`, `_` and `/` separators, and drop the futures suffix after the first `:`.
So all the variants below are the same `BTCUSDT` market:

```text
BTC/USDT      → BTCUSDT
BTC-USDT      → BTCUSDT
BTCUSDT       → BTCUSDT
BTCUSDT:USDT  → BTCUSDT
```

Do not normalize anything in the strategy: pass `pair` as it is, including
the colon of a futures pair. An example of a CCXT futures instrument:

```python
"instrument": {"venue": "binancefutures", "symbol": "BTC/USDT:USDT"},
```

`venue` is just a venue label in the request; canonicalisation applies to
`symbol` only.

Outcomes of futures pairs are computed from Binance spot candles: funding and
the futures/spot price difference are not taken into account.

If the pair is not on Binance Spot (delisting, an exotic contract), the candle
request is rejected as `invalid_request`. The labeler retries it for no longer
than 24 hours after the window closes, then writes `outcome.unlabelable` with
the reason `source_rejected`, and the signal stays without an outcome. In the
report such signals are visible as the line `source rejected: N` (in `--share`
— `- source rejected: N`) and are not lost among the signals awaiting
labeling.

## 4. Run and check

```bash
freqtrade trade --strategy VendShadowGate --dry-run
vend gate report
vend journal verify
```

- `vend gate report` shows the accumulated verdicts and outcome labeling.
- `vend journal verify` checks the integrity of the journal.
- If the gate is off, busy, or answers longer than the timeout, the client
  silently drops the call: trading continues, there is simply no record in
  the journal.

## 5. M1 limitations

- The gate **does not block** entries: `confirm_trade_entry` always returns
  `True`.
- The gate does not send orders and does not know the position size; this is
  shadow mode.
- The client does not retry requests. A unique `signalId` is the strategy's
  responsibility.
- One call is one HTTP POST to `127.0.0.1:8420`; no external network is used.
- Live exchange keys are not needed for this recipe: `--dry-run` is enough.
- Supported timeframes are a positive amount followed by a lowercase `m`, `h`,
  `d` or `w` (`5m`, `1h`, `4h`, `1d`, `1w`). The gate answers HTTP 400 to
  anything else, including Freqtrade's monthly `1M`: such a signal could not be
  labeled.

## 6. What comes next

When the shadow run has enough statistics, the next step is `paper` mode,
where a `REJECT` verdict already affects the entry. That is a separate
contract and behavior change — see the
[risk specification](../spec/06-risk-execution.md) and
[ADR-0008](../adr/0008-wedge-first-scope.md); until then, do not replace
`return True` with the gate verdict.
