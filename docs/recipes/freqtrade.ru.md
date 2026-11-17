# Рецепт: Freqtrade → shadow-гейт

[English version](freqtrade.md)

Как подключить Freqtrade к локальному shadow-гейту LV24: стратегия остаётся
прежней, а на входе в сделку она отправляет один `GateRequest` в `vend serve`
и **не ждёт ответа**. Вердикт пишется в журнал для последующей разметки и
отчёта; на саму сделку он в M1 не влияет.

> Пример написан с нуля под публичный интерфейс `IStrategy`. Код Freqtrade
> (GPL-3.0) в этот репозиторий не копируется и не вендорится: мы только
> импортируем его классы и реализуем собственный хук `confirm_trade_entry`.

## Что понадобится

- Go-ядро с локальным API: `vend` из этого репозитория.
- Python 3.10+ и установленный Freqtrade (его зависимости — на стороне
  пользователя).
- Клиент [`examples/clients/python/vend_gate.py`](../../examples/clients/python/vend_gate.py)
  — только стандартная библиотека Python.

## 1. Поднять гейт

Токен обязателен, сервер слушает только loopback:

```bash
go build -o vend ./core/cmd/vend
VENDING_SECRET_CONN_LOCAL_APITOKEN=<token> ./vend serve   # 127.0.0.1:8420
```

Проверка живости (без авторизации):

```bash
curl -s http://127.0.0.1:8420/healthz
# {"status":"ok","mode":"shadow","version":"..."}
```

Тот же токен положите в окружение процесса Freqtrade, например
`VEND_GATE_TOKEN`. В конфиг стратегии и в репозиторий токен не попадает.

## 2. Положить клиент рядом со стратегией

Скопируйте `vend_gate.py` и `vend_shadow_gate.py` из `examples/freqtrade/` в
каталог `user_data/strategies/` (или добавьте оба пути в `sys.path`).

## 3. Минимальная стратегия

Готовый файл — [`examples/freqtrade/vend_shadow_gate.py`](../../examples/freqtrade/vend_shadow_gate.py).
Ниже он приведён целиком; тест `examples/freqtrade/test_vend_shadow_gate.py`
сверяет этот блок с файлом и падает при расхождении.

Пример проверен на минимальных заглушках интерфейсов `IStrategy` и
`DataFrame`, написанных в тесте; с настоящим Freqtrade автор его не запускал.

Сверено с документацией Freqtrade (freqtrade.io, версия `stable`), 2026-11-17:
сигнатура `confirm_trade_entry`, пара `get_analyzed_dataframe(pair, timeframe)`
и доступность `self.timeframe`.

<!-- vend-example: examples/freqtrade/vend_shadow_gate.py -->
```python
"""Freqtrade strategy that reports entries to the local LV24 shadow gate.

Written from scratch for the public IStrategy interface; no Freqtrade source
is vendored here. The gate is advisory in M1: ``confirm_trade_entry`` always
returns True, and a missing, slow or failing gate never blocks a trade.

Reporting is asynchronous: ``confirm_trade_entry`` builds the request and puts
it on a bounded queue, and one daemon thread per strategy sends the queue in
order. The entry callback never performs a network call.

Environment:
    VEND_GATE_TOKEN  bearer token of the local gate; without it the strategy
                     runs normally and sends nothing.
    VEND_GATE_URL    gate origin, default ``http://127.0.0.1:8420``.

Copy this file and ``vend_gate.py`` into your Freqtrade
``user_data/strategies/`` directory. See ``docs/recipes/freqtrade.md``.
"""

import math
import os
import queue
import sys
import threading
import uuid
from datetime import datetime, timezone
from pathlib import Path

from freqtrade.strategy import IStrategy
from pandas import DataFrame

sys.path.insert(0, str(Path(__file__).resolve().parent))
from vend_gate import VendGate  # noqa: E402

DEFAULT_GATE_URL = "http://127.0.0.1:8420"
GATE_TIMEOUT_SECONDS = 2.0
# The queue is bounded on purpose: a full queue drops the newest signal
# instead of delaying the time-critical entry callback.
GATE_QUEUE_SIZE = 100
# One lock for the lazy sender: a strategy instance may be called from several
# Freqtrade worker threads.
_SENDER_LOCK = threading.Lock()

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


class _GateSender:
    """Send gate requests in order from one daemon thread.

    The bounded queue decouples the time-critical entry callback from the
    network. ``submit`` never waits: when the queue is full the signal is
    dropped silently. The worker swallows every error and stays alive, so a
    gate outage only costs the signals that could not be delivered.

    ``drain`` is the test and shutdown hook: it waits for the queue to empty
    through a condition variable instead of polling with sleeps.
    """

    def __init__(self, client: VendGate) -> None:
        self._client = client
        self._pending: queue.Queue[dict] = queue.Queue(maxsize=GATE_QUEUE_SIZE)
        self._condition = threading.Condition()
        self._queued = 0
        self._thread = threading.Thread(
            target=self._run,
            name="vend-shadow-gate",
            daemon=True,
        )

    def start(self) -> None:
        self._thread.start()

    def submit(self, payload: dict) -> None:
        """Queue one payload without waiting; drop it when the queue is full."""
        with self._condition:
            try:
                self._pending.put_nowait(payload)
            except queue.Full:
                return
            self._queued += 1

    def drain(self, timeout: float) -> bool:
        """Wait until every queued payload is sent, or until timeout.

        Returns:
            True when nothing is pending, False on timeout.
        """
        with self._condition:
            return self._condition.wait_for(lambda: self._queued == 0, timeout)

    def _run(self) -> None:
        while True:
            payload = self._pending.get()
            try:
                self._client.send_and_wait(payload)
            except Exception:  # noqa: BLE001 - the sender must outlive any failure
                pass
            finally:
                with self._condition:
                    self._queued -= 1
                    self._condition.notify_all()


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

    def _sender(self) -> _GateSender | None:
        """Return the strategy's sender, starting its thread on first use.

        A missing token or an invalid URL disables reporting: no queue and no
        thread are created, and every entry is still allowed. The thread is a
        daemon and there is at most one per strategy.
        """
        client = self._gate()
        if client is None:
            return None
        sender = getattr(self, "_vend_sender", None)
        if sender is not None:
            return sender
        with _SENDER_LOCK:
            sender = getattr(self, "_vend_sender", None)
            if sender is None:
                sender = _GateSender(client)
                sender.start()
                self._vend_sender = sender
            return sender

    def wait_until_idle(self, timeout: float = 5.0) -> bool:
        """Wait until the sender has sent every queued signal.

        Returns:
            True when nothing is pending (also when reporting is disabled),
            False on timeout. The entry callback never calls this method; it
            exists for tests and for a graceful shutdown.
        """
        sender = getattr(self, "_vend_sender", None)
        if sender is None:
            return True
        return sender.drain(timeout)

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
        sender = self._sender()
        if sender is not None:
            try:
                # Bounded queue, no network and no waiting in the callback.
                sender.submit(payload)
            except Exception:  # noqa: BLE001 - the gate must never block a trade
                pass
        # Shadow mode: entries are always allowed; the verdict is journaled.
        return True
```
<!-- /vend-example -->

Если нужен реальный venue биржи, подставьте его в `instrument.venue`
(например, из `self.config["exchange"]["name"]`), но не меняйте `pair`:
символ передаётся ровно в том виде, в каком его даёт Freqtrade.

### Что попадает в `state`

При каждом входе стратегия читает последнюю проанализированную свечу через
публичный вызов провайдера данных `self.dp.get_analyzed_dataframe(pair,
self.timeframe)` и отправляет четыре числа:

| Ключ | Значение |
|---|---|
| `candle_close` | close последней проанализированной свечи |
| `ma_fast` | среднее за 12 баров из `populate_indicators` |
| `ma_slow` | среднее за 48 баров из `populate_indicators` |
| `ma_gap` | `ma_fast / ma_slow − 1` |

Отправляются только конечные числа: NaN, бесконечности, `None` и нечисловые
значения отбрасываются по одному ключу. `ma_gap` пропускается, если `ma_fast`
или `ma_slow` отсутствует, не конечно, или `ma_slow` равен нулю. Если провайдера
данных нет, кадр пуст или что-то бросает исключение, запрос всё равно уходит с
`state: {}`, а вход разрешается.

Гейт сам добавляет к тому, что видит модель, свои поля: `symbol`, `venue`,
`timeframe`, `side`, `intent_kind`, `timestamp` и `close` (берётся из `price`).
Ключ с таким именем в вашем `state` будет перезаписан — поэтому close свечи
отправляется как `candle_close`.

Свои числовые признаки можно добавить в `state`: ключи в snake_case, конечные
числа, плоский объект. Нужно именно число, а не строка: `{"rsi_14": 55.5}` —
число для гейта, `{"rsi_14": "55.5"}` — нет.

Автор не подбирал эти четыре ключа как предсказатели и не измерял, как они
влияют на вердикт или его качество. Это минимальный пример передачи рыночного
контекста, а не набор признаков.

### Символы

Ядро сравнивает инструменты в канонической форме: trim, верхний регистр,
удаление разделителей `-`, `_`, `/` и отбрасывание фьючерсного суффикса после
первого `:`. Поэтому все варианты ниже — один и тот же рынок `BTCUSDT`:

```text
BTC/USDT      → BTCUSDT
BTC-USDT      → BTCUSDT
BTCUSDT       → BTCUSDT
BTCUSDT:USDT  → BTCUSDT
```

Ничего не нормализуйте в стратегии: передавайте `pair` как есть, включая
двоеточие у фьючерсов. Пример фьючерсного инструмента CCXT:

```python
"instrument": {"venue": "binancefutures", "symbol": "BTC/USDT:USDT"},
```

`venue` — просто метка площадки в запросе; канонизация касается только
`symbol`.

Исходы фьючерсных пар считаются по спотовым свечам Binance: funding и разница
цен между фьючерсом и спотом не учитываются.

Если пары нет на Binance Spot (делистинг, экзотический контракт), запрос свечей
отклоняется как `invalid_request`. Разметчик повторяет его не дольше 24 часов
после закрытия окна, затем пишет `outcome.unlabelable` с причиной
`source_rejected`, и сигнал остаётся без исхода. В отчёте такие сигналы видны
строкой `source rejected: N` (в `--share` — `- source rejected: N`) и не
теряются среди ожидающих разметки.

## 4. Запуск и проверка

```bash
freqtrade trade --strategy VendShadowGate --dry-run
vend gate report
vend journal verify
```

- `vend gate report` показывает накопленные вердикты и разметку исходов.
- `vend journal verify` проверяет целостность журнала.
- Если гейт выключен или занят, клиент молча теряет вызов: торговля
  продолжается, в журнале просто не будет записи.
- Вход не задерживается: стратегия только кладёт запрос в ограниченную
  очередь, а отправляет его фоновый поток. Сигнал может быть потерян при
  переполнении очереди, при недоступном гейте и при остановке бота до
  отправки. Гейт записывает решение, даже если клиент не дождался ответа.

## 5. Ограничения M1

- Гейт **не блокирует** входы: `confirm_trade_entry` всегда возвращает `True`.
- Гейт не отправляет ордера и не знает о размере позиции; это теневой режим.
- Клиент не ретраит запросы. Уникальный `signalId` — ответственность стратегии.
- Один вызов — один HTTP POST на `127.0.0.1:8420`; сеть наружу не используется.
- Документация Freqtrade советует не делать сетевых запросов в
  `confirm_trade_entry`: колбэк критичен ко времени. Пример следует этому
  совету: `confirm_trade_entry` только собирает запрос, кладёт его в
  ограниченную очередь (`GATE_QUEUE_SIZE`, 100) и возвращает `True`; один
  daemon-поток на стратегию выполняет HTTP POST. Вход не задерживается.
  Доставка негарантированная: переполнение отбрасывается молча, запрос,
  стоящий в очереди на момент остановки бота, теряется. Гейт сам доводит
  решение до конца и записывает его в журнал, даже если клиент уже перестал
  ждать ответа.
- Сигналы отправляются по одному, по порядку. Если гейт отвечает дольше
  клиентского таймаута (`GATE_TIMEOUT_SECONDS`, 2 секунды), каждый сигнал в
  очереди ждёт до 2 секунд на каждый сигнал перед ним и на столько же позже
  попадает в гейт и получает время решения.
- Живые ключи биржи для этого рецепта не нужны: достаточно `--dry-run`.
- Поддерживаемые таймфреймы — положительное число и единица `m`, `h`, `d` или
  `w` в нижнем регистре (`5m`, `1h`, `4h`, `1d`, `1w`). На всё остальное,
  включая месячный `1M` у Freqtrade, гейт отвечает HTTP 400: такой сигнал
  нельзя было бы разметить.

## 6. Что дальше

Когда тень наберёт статистику, следующим шагом станет `paper`-режим, где
вердикт `REJECT` уже влияет на вход. Это отдельное изменение контракта и
поведения — см. [спецификацию риска](../spec/06-risk-execution.md) и
[ADR-0008](../adr/0008-wedge-first-scope.md); до него менять `return True`
на вердикт гейта нельзя.
