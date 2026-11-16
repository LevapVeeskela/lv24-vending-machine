# Рецепт: Freqtrade → shadow-гейт

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

Скопируйте `vend_gate.py` в каталог `user_data/strategies/` (или добавьте его
путь в `sys.path`) и создайте файл стратегии, например
`user_data/strategies/vend_shadow_gate.py`.

## 3. Минимальная стратегия

Сигнал намеренно простой: две скользящие средние. Важна не логика входа, а
хук `confirm_trade_entry`.

```python
"""Freqtrade strategy that reports entries to the local LV24 shadow gate.

Written from scratch for the public IStrategy interface; no Freqtrade
source is vendored here.
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


class VendShadowGate(IStrategy):
    timeframe = "1h"
    startup_candle_count = 60
    minimal_roi = {"0": 10}
    stoploss = -0.10
    process_only_new_candles = True
    use_exit_signal = False

    # Один клиент на процесс. Bearer-токен живёт только в памяти.
    gate = VendGate(
        base_url="http://127.0.0.1:8420",
        token=os.environ["VEND_GATE_TOKEN"],
        timeout=2.0,
    )

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
            # Уникальный id на каждую попытку входа: повтор с тем же id и
            # другим телом гейт отклонит.
            "signalId": f"freqtrade-{uuid.uuid4().hex}",
            # pair передаётся как есть; ядро канонизирует BTC/USDT, BTC-USDT,
            # BTCUSDT и BTCUSDT:USDT в BTCUSDT.
            "instrument": {"venue": "freqtrade", "symbol": pair},
            "side": "buy" if side == "long" else "sell",
            "intentKind": "entry",
            "timeframe": self.timeframe,
            # ts — момент сигнала, UTC.
            "ts": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
            # Деньги на границе — строкой, не float.
            "price": f"{float(rate):.8f}",
            "source": "freqtrade:VendShadowGate",
            "state": {},
        }
        # Fire-and-forget: отправка в фоновом потоке, ответ не ждём.
        self.gate.send(payload)
        # Shadow: вход разрешён всегда, вердикт гейта только журналируется.
        return True
```

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

## 6. Что дальше

Когда тень наберёт статистику, следующим шагом станет `paper`-режим, где
вердикт `REJECT` уже влияет на вход. Это отдельное изменение контракта и
поведения — см. [спецификацию риска](../spec/06-risk-execution.md) и
[ADR-0008](../adr/0008-wedge-first-scope.md); до него менять `return True`
на вердикт гейта нельзя.
