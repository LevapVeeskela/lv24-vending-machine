# Runbook: живая проверка shadow-гейта (15 минут)

Цель: владелец на своей машине проверяет, что `vend` реально ходит в Drex и
Binance, размечает исходы и пишет отчёт. Ордера не отправляются, деньги не
двигаются: M1 работает только в `shadow`.

Проверка занимает 15 минут, из них ~6 минут — ожидание разметки одного ручного
запроса с таймфреймом `1m`. Всё, что нужно отправить на ревью, перечислено в
конце; секреты, конфиг и журнал отправлять не нужно.

## Предусловия

- `vend` установлен и `vend version` печатает версию
  ([install.ru.md](install.ru.md)).
- `vend init` уже выполнен; известны пути конфига и данных
  (`vend config path`, каталог данных — рядом с `vending.db`).
- В окружении есть ключ decision-провайдера и токен локального API:

  ```bash
  export VENDING_SECRET_CONN_DREX_APIKEY='<token>'
  export VENDING_SECRET_CONN_LOCAL_APITOKEN='<token>'
  ```

  PowerShell: `$env:VENDING_SECRET_CONN_DREX_APIKEY = '<token>'` и так же для
  `VENDING_SECRET_CONN_LOCAL_APITOKEN`. Значения — разные случайные строки.
- Секреты не попадают в файлы, скриншоты и переписку.

## Шаг 0. Синхронизируйте системные часы

Время вердикта и приёма сигнала задаёт окно разметки, а расписание
уведомлений и `entryLagMs` считаются по UTC. Сбитые часы сдвигают метку и
алерты, поэтому сначала синхронизируйте время:

- Windows: `w32tm /resync` (или «Параметры → Время и язык → Дата и время →
  Синхронизировать сейчас»).
- Linux: `sudo timedatectl set-ntp true`, проверить `timedatectl`.
- macOS: «Системные настройки → Основные → Дата и время → Установить дату и
  время автоматически».

## Шаг 1. Живые пробы doctor (5 минут)

Сначала офлайн-проверка, затем живые вызовы. Сохраните вывод сразу: этот же
файл пойдёт на ревью, второй платный вызов в шаге 5 не нужен.

Linux / macOS:

```bash
vend doctor 2>&1 | tee doctor.txt
vend doctor --live --symbol BTCUSDT --save-raw <file> 2>&1 | tee -a doctor.txt
```

Windows PowerShell (`tee` нет; `*>` перезаписывает, `*>>` дописывает):

```powershell
.\vend.exe doctor *> doctor.txt
.\vend.exe doctor --live --symbol BTCUSDT --save-raw <file> *>> doctor.txt
```

`--live` делает три опциональные сетевые пробы, каждая с дедлайном 20 секунд:

| Проба | Что делает | Успех |
|---|---|---|
| `decision` | один вызов decision-провайдера с фиксированным синтетическим state и набором вопросов гейта; ответ проходит через `gate.Evaluate` | `decision: ok verdict=<ALLOW\|REJECT\|ABSTAIN> code=<код> cost=<decimal> USD` |
| `candles` | две закрытые свечи `1h` по `--symbol` (по умолчанию `BTCUSDT`) у marketdata-провайдера | `candles: ok` |
| `telegram` | одно тестовое сообщение, только если `notify.telegram.enabled` | `telegram: ok` |

`ABSTAIN` с кодом `invalid_output` — это **FAIL** пробы `decision`: ответ не
прошёл проверку вопросов гейта. Остальные вердикты (`ALLOW`, `REJECT`,
`ABSTAIN` с кодом `low_confidence`/`insufficient_data` и т. п.) означают, что
живой вызов и политика отработали, и проба успешна. Пример успешной строки:
`decision: ok verdict=ALLOW code=allow cost=0.0001 USD`.

`--save-raw <file>` пишет сырой ответ decision-провайдера атомарно с правами
0600 и печатает `save_raw: ok`. Файл может содержать служебные данные вендора:
не коммитить и не пересылать.

`--symbol` и `--save-raw` требуют `--live`; без него команда завершается кодом 2.

### Что означают коды

Строка `decision` печатает `ok verdict=... code=... cost=... USD` либо короткий
код ошибки; остальные пробы печатают `ok` либо короткий код ошибки:

| Код | Значение | Что делать |
|---|---|---|
| `ok` | проба прошла | — |
| `auth` | нет или неверный ключ, либо исчерпана квота | проверить `secretRef` и `VENDING_SECRET_*`, баланс/квоту провайдера |
| `payment_required` | провайдер требует оплату или квоту | пополнить/включить квоту у провайдера |
| `rate_limit` | троттлинг | подождать и повторить позже |
| `invalid_request` | провайдер отклонил запрос: модель, профиль, URL | сверить `providers.decision.*` с `vend config validate` |
| `invalid_output` | ответ не соответствует контракту | повторить; если повторяется — сообщить с `--save-raw` |
| `unavailable` | временная недоступность или сеть | повторить через несколько минут |
| `timeout` | не уложились в 20 секунд | проверить сеть/провайдера, повторить |
| `ip_banned` | IP забанен провайдером | не ретраить; сменить сеть/провайдера |
| `region_unavailable` | регион недоступен | использовать доступный регион/провайдера |
| `unknown` | не классифицировано, печатается как `unavailable` | повторить и сообщить |

Если `config` невалиден, живые пробы возвращают `invalid_request`; если реестр
провайдеров не собрался — `unavailable`. Любая FAIL-проба даёт код возврата 1.
`--json` печатает `{"ok":bool,"checks":[{name,ok,detail,live?}]}` — удобно
приложить к отчёту.

## Шаг 2. Конфиг на проверку (2 минуты)

В конфиге (`vend config path`) включите trend и короткий горизонт. Фрагмент —
только изменяемые ключи, остальное оставьте как создал `init`:

```json
{
  "mode": "shadow",
  "outcome": {
    "horizonBars": 3,
    "feeBps": 10,
    "labelIntervalSec": 30
  },
  "providers": {
    "decision": {
      "id": "decision-drex",
      "model": "drex-max-v1.1",
      "profile": "drex",
      "secretRef": "conn/drex/apiKey"
    },
    "marketdata": {
      "id": "marketdata-binance",
      "baseUrl": "https://api.binance.com"
    }
  },
  "signals": {
    "trend": {
      "enabled": true,
      "venue": "binance",
      "symbols": ["BTCUSDT"],
      "timeframe": "5m",
      "fast": 12,
      "slow": 26,
      "sides": ["buy"]
    }
  },
  "notify": {
    "telegram": {
      "enabled": false,
      "chatId": "",
      "secretRef": "conn/telegram/botToken"
    }
  }
}
```

Проверка без сети и ключей:

```bash
vend config validate
```

Что важно в этих значениях:

- Ручной запрос из шага 3 идёт с `timeframe: 1m`. При `horizonBars: 3` это
  горизонт 3 минуты: разметчик ждёт `(3 + 1) × 1m` = 4 минуты после вердикта,
  поэтому метка появляется через 5–6 минут с учётом цикла 30 секунд.
- `timeframe: 5m` у trend — отдельное расписание: скан идёт через 2 секунды
  после закрытия каждого 5m-бара, а не по таймеру разметки.
- `labelIntervalSec: 30` — цикл разметки, уведомлений и статус-файла; на
  расписание trend он не влияет.
- `feeBps: 10` — за одну сторону; `netReturn` вычитает 20 bps round-trip.
- `sides: ["buy"]` — только покупки; trend выдаёт вход на пересечении EMA
  fast/slow на закрытом баре.
- `venue: "binance"` — площадка инструмента в сигналах; значение проверяется
  валидацией.
- Telegram можно включить (`enabled: true`, `chatId`, `secretRef`), тогда
  `doctor --live` дополнительно отправит одно тестовое сообщение. Для быстрой
  проверки гейта он не обязателен.

## Шаг 3. Запустить shadow-цикл (6 минут ожидания)

```bash
vend serve --addr 127.0.0.1:8420
```

В stderr появится `listening on http://127.0.0.1:8420`. Процесс не закрывать:
раннер работает внутри `serve`. Первый цикл выполняется сразу, далее каждые
30 секунд.

Сразу после запуска отправьте один ручной запрос с `timeframe: "1m"` — это
обязательный шаг проверки. Trend на 5m может не дать пересечения на спокойном
рынке, а ручной 1m-запрос гарантирует метку за 5–6 минут. Пример (подставьте
свой токен через переменную окружения и текущее UTC-время):

```bash
curl -sS -X POST http://127.0.0.1:8420/v1/gate \
  -H "Authorization: Bearer $VENDING_SECRET_CONN_LOCAL_APITOKEN" \
  -H "Content-Type: application/json" \
  --data '{"signalId":"smoke-1m-1","instrument":{"venue":"binance","symbol":"BTCUSDT"},"side":"buy","intentKind":"entry","timeframe":"1m","ts":"<now UTC RFC3339>","price":"100000","source":"smoke"}'
```

В PowerShell 5.1 вызывайте `curl.exe`; токен — `$env:VENDING_SECRET_CONN_LOCAL_APITOKEN`.
Ответ `gate.verdict` означает, что сигнал принят и попал в журнал; метка
появится сама, повторять запрос не нужно.

## Шаг 4. Что смотреть после 5–6 минут

### `vend status --json`

```bash
vend status --json
```

Поля: `version`, `policyHash`, `events`, `labeled`, `lastLabelAt`,
`lastCycleError`, `updatedAt`, `lastSeq`.

Ожидаемо:

- `events` > 0 — в журнале есть `gate.request`, `decision.response`,
  `gate.verdict` (по 3 события на сигнал, плюс метки и уведомления);
- `labeled` ≥ 1 — метка ручного 1m-запроса: горизонт 3 бара плюс один
  запасной = 4 минуты после вердикта, плюс цикл 30 секунд. Если метки нет через
  8 минут, см. «Если что-то не так»;
- `lastLabelAt` — время последней метки; после шага 3 оно не должно быть пустым;
- `policyHash` — 64 hex, тот же, что печатает `vend doctor`;
- `lastCycleError` пустой; если там текст — цикл упал, см. «Если что-то не так»;
- `updatedAt` не старше `labelIntervalSec` + несколько секунд.

Без запущенного `serve` статус-файла нет: `events`/`labeled` считаются по
журналу, `policy: none`, `last label: never`.

### `vend gate report`

```bash
vend gate report
```

После 5–6 минут ожидаемо:

```text
assumptions: horizon=3 bars, fees=20 bps round-trip, price source=marketdata-binance, equal size, no slippage, no funding
verdicts: ALLOW=<n> REJECT=<n> ABSTAIN=<n> SKIP=<n>
labeled: <n>
ожидают разметки: <n>
без исхода по сроку: 0
policy: <12 hex>
other policies: 0
backfill: labeled=0 excluded=0
decision cost: <decimal> USD
latency p50/p95: <n>ms/<n>ms
insufficient data: <n> labeled, need 100
```

- `verdicts` считает уникальные решения по последнему вердикту на `decisionId`;
  `gate.request`/`decision.response`/`gate.verdict` в журнале — по событию на
  сигнал, поэтому `events` в статусе больше суммы вердиктов.
- `labeled` — число меток, привязанных к решениям выбранной политики;
  `backfill` — поздние сигналы (по умолчанию только считаются, не сравниваются).
- `ожидают разметки` — entry-сигналы без метки и без `outcome.unlabelable`;
  `без исхода по сроку` — сигналы, для которых после 7 суток так и не нашлось
  честного окна и записан `outcome.unlabelable` с причиной `timeout`.
- `decision cost` — сумма `usage.costUsd` уникальных решений;
  `latency p50/p95` — по `usage.latencyMs`.
- `insufficient data` до 100 меток — **норма**, а не ошибка. Сравнения
  (группы, bootstrap CI, Brier/BSS/AUC/ECE, разбивка по моделям) появятся
  только от 100 меток; BSS печатается как `bss: n/a`, если base rate 0 или 1.
- Сравнения смешивают только последнюю пару `(horizonBars, feeBps)`; метки с
  другой парой не подмешиваются (в `--json` это `otherLabelParams`).

`entryLagMedian`, `entryLagP95` (в долях одного бара) и `entryShifted` входят в
допущения отчёта и печатаются в `--share`; в человекочитаемом `gate report` их
нет. `entryLagMs` — задержка от открытия входного бара до момента, когда сигнал
стал доступен (время вердикта, иначе время приёма). Вход сдвигается на
следующий бар, если бар открылся раньше момента доступности больше чем на
`min(10% интервала, 30 секунд)`; без запасного бара сигнал ждёт следующего
цикла и не пишет исход без переноса. В `--share` рядом с долями бара
печатается то же значение в секундах.

### `vend gate report --share`

```bash
vend gate report --share
```

Печатает обезличенную сводку: версию, префикс policy (8 hex), число решений и
UTC-дней, число меток, число ожидающих разметки и просроченных без исхода,
счётчики вердиктов, метрики или `insufficient data`, допущения (горизонт,
round-trip fee, entry lag median/p95 в долях бара и в секундах, entry shifted,
equal size/no slippage/no funding) и ссылку на GitHub Discussions. В ней нет
символов, площадок, цен, state, путей, идентификаторов и сумм доходности.
Файл через `--out <file>` пишется с правами 0600.

## Шаг 5. Что отправить на ревью

Соберите четыре файла (имена любые). `doctor.txt` уже сохранён в шаге 1 —
второй платный вызов `doctor --live` не нужен:

```bash
vend status --json > status.json
vend gate report --share > share.md
vend journal verify > verify.txt 2>&1
```

Отправляйте:

1. `doctor.txt` — вывод живых проб и кодов (без токенов: doctor их не печатает);
2. `status.json` — счётчики, policy hash, последняя ошибка цикла;
3. `share.md` — обезличенная сводка (проверьте глазами перед отправкой);
4. `verify.txt` — результат `journal verify` (должен быть `ok`, число событий).

Не отправляйте: конфиг, переменные окружения, `--save-raw` файл, `vending.db`,
скриншоты с ключами. Если `doctor` или `serve` печатают путь/токен — вырежьте
его.

## Если что-то не так

| Симптом | Причина и действие |
|---|---|
| `doctor` возвращает 1 | смотрите строку `FAIL <check>`: конфиг, каталог, БД, журнал, провайдеры |
| `decision: FAIL verdict=ABSTAIN code=invalid_output` | живой ответ не прошёл проверку вопросов гейта; повторить, при повторе — прислать `--save-raw` |
| `decision: auth` | ключ не задан/неверен или квота; проверить `VENDING_SECRET_CONN_DREX_APIKEY` |
| `candles: invalid_request` | символ/интервал не принят Binance; проверить `--symbol BTCUSDT` |
| `candles: ip_banned` | IP забанен; не ретраить, сменить сеть |
| `events: 0` через 5 минут | `serve` не запущен или смотрит в другой `--data-dir` |
| `labeled: 0` через 8 минут | нет ручного 1m-запроса, не хватает закрытых баров, нет сети или неверный `--data-dir`; проверить `lastCycleError` |
| `verdicts: ABSTAIN=<n>` | провайдер ошибся; код причины — в `gate.verdict` (`provider_auth`, `provider_unavailable`, `invalid_output`, `provider_misconfigured`) |
| `lastCycleError` не пуст | цикл продолжает работать; ошибка повторяется каждый интервал — прислать текст в отчёте |
| `journal chain broken at seq N` | журнал повреждён; не продолжать, сохранить файл и сообщить |

## Итог проверки

Запишите: дату/время (UTC), версию, префикс policy, число событий, меток и
вердиктов, коды `doctor --live`, результат `journal verify`. Эти данные —
вход в G1 и в решение о следующей итерации клина.
