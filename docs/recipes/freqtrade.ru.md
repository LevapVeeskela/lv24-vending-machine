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
            "state": {},
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

Если нужен реальный venue биржи, подставьте его в `instrument.venue`
(например, из `self.config["exchange"]["name"]`), но не меняйте `pair`:
символ передаётся ровно в том виде, в каком его даёт Freqtrade.

`state` — произвольные конечные числа (индикаторы) для контекста решения.
Пустой объект допустим, `null` — нет.

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
- Если гейт выключен, занят или отвечает дольше таймаута, клиент молча
  теряет вызов: торговля продолжается, в журнале просто не будет записи.

## 5. Ограничения M1

- Гейт **не блокирует** входы: `confirm_trade_entry` всегда возвращает `True`.
- Гейт не отправляет ордера и не знает о размере позиции; это теневой режим.
- Клиент не ретраит запросы. Уникальный `signalId` — ответственность стратегии.
- Один вызов — один HTTP POST на `127.0.0.1:8420`; сеть наружу не используется.
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
