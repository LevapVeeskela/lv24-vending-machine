# Runbook: живая проверка shadow-гейта (30–40 минут)

Цель: владелец на своей машине проверяет, что `vend` реально ходит в Drex и
Binance, размечает исходы и пишет отчёт. Ордера не отправляются, деньги не
двигаются: M1 работает только в `shadow`.

Проверка занимает 30–40 минут, из них ~25 минут — ожидание разметки. Всё, что
нужно отправить на ревью, перечислено в конце; секреты, конфиг и журнал
отправлять не нужно.

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

## Шаг 1. Живые пробы doctor (5 минут)

Сначала офлайн-проверка, затем живые вызовы:

```bash
vend doctor
vend doctor --live --symbol BTCUSDT --save-raw <file>
```

`--live` делает три опциональные сетевые пробы, каждая с дедлайном 20 секунд:

| Проба | Что делает | Успех |
|---|---|---|
| `decision` | один вызов decision-провайдера с фиксированным синтетическим state и набором вопросов гейта | `decision: ok cost=<decimal> USD` |
| `candles` | две закрытые свечи `1h` по `--symbol` (по умолчанию `BTCUSDT`) у marketdata-провайдера | `candles: ok` |
| `telegram` | одно тестовое сообщение, только если `notify.telegram.enabled` | `telegram: ok` |

`--save-raw <file>` пишет сырой ответ decision-провайдера атомарно с правами
0600 и печатает `save_raw: ok`. Файл может содержать служебные данные вендора:
не коммитить и не пересылать.

`--symbol` и `--save-raw` требуют `--live`; без него команда завершается кодом 2.

### Что означают коды

Строка пробы печатает `ok` либо короткий код ошибки:

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

## Шаг 2. Конфиг на 25 минут (5 минут)

В конфиге (`vend config path`) включите trend и короткий горизонт. Фрагмент —
только изменяемые ключи, остальное оставьте как создал `init`:

```json
{
  "mode": "shadow",
  "outcome": {
    "horizonBars": 3,
    "feeBps": 10,
    "labelIntervalSec": 60
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

- `timeframe: 5m` и `horizonBars: 3` дают горизонт 15 минут; разметчик ждёт
  `(horizonBars + 1) × timeframe` = 20 минут после вердикта, поэтому первые
  метки появляются на 20–25-й минуте.
- `labelIntervalSec: 60` — цикл раннера раз в минуту (разметка, скан trend,
  уведомления, статус-файл).
- `feeBps: 10` — за одну сторону; `netReturn` вычитает 20 bps round-trip.
- `sides: ["buy"]` — только покупки; trend выдаёт вход на пересечении EMA
  fast/slow на закрытом баре.
- `venue: "binance"` — площадка инструмента в сигналах; значение проверяется
  валидацией.
- Telegram можно включить (`enabled: true`, `chatId`, `secretRef`), тогда
  `doctor --live` дополнительно отправит одно тестовое сообщение. Для быстрой
  проверки гейта он не обязателен.

## Шаг 3. Запустить shadow-цикл (25 минут ожидания)

```bash
vend serve --addr 127.0.0.1:8420
```

В stderr появится `listening on http://127.0.0.1:8420`. Процесс не закрывать:
раннер работает внутри `serve`. Первый цикл выполняется сразу, далее каждые
60 секунд.

Если trend не дал ни одного пересечения за это время (обычная ситуация на
спокойном рынке), гейт всё равно можно проверить вручную — отправьте один
запрос из [quickstart](quickstart.ru.md) (§5). Метка для него появится через
20 минут после вердикта.

## Шаг 4. Что смотреть после 25 минут

### `vend status --json`

```bash
vend status --json
```

Поля: `version`, `policyHash`, `events`, `labeled`, `lastLabelAt`,
`lastCycleError`, `updatedAt`, `lastSeq`.

Ожидаемо:

- `events` > 0 — в журнале есть `gate.request`, `decision.response`,
  `gate.verdict` (по 3 события на сигнал, плюс метки и уведомления);
- `labeled` ≥ 0; после 25 минут — обычно 0–1 при горизонте 20 минут;
- `lastLabelAt` — время последней метки или отсутствует, если меток ещё нет;
- `policyHash` — 64 hex, тот же, что печатает `vend doctor`;
- `lastCycleError` пустой; если там текст — цикл упал, см. «Если что-то не так»;
- `updatedAt` не старше `labelIntervalSec` + несколько секунд.

Без запущенного `serve` статус-файла нет: `events`/`labeled` считаются по
журналу, `policy: none`, `last label: never`.

### `vend gate report`

```bash
vend gate report
```

После 25 минут ожидаемо:

```text
assumptions: horizon=3 bars, fees=20 bps round-trip, price source=marketdata-binance, equal size, no slippage, no funding
verdicts: ALLOW=<n> REJECT=<n> ABSTAIN=<n> SKIP=<n>
labeled: <n>
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
стал доступен (время вердикта, иначе время приёма); если бар открылся раньше
более чем на 10% интервала, вход сдвигается на следующий бар и
`entryShifted=true` (при наличии запасного бара).

### `vend gate report --share`

```bash
vend gate report --share
```

Печатает обезличенную сводку: версию, префикс policy (8 hex), число решений и
UTC-дней, число меток, счётчики вердиктов, метрики или `insufficient data`,
допущения (горизонт, round-trip fee, entry lag median/p95, entry shifted, equal
size/no slippage/no funding) и ссылку на GitHub Discussions. В ней нет
символов, площадок, цен, state, путей, идентификаторов и сумм доходности.
Файл через `--out <file>` пишется с правами 0600.

## Шаг 5. Что отправить на ревью

Соберите четыре файла (имена любые):

```bash
vend doctor --live --symbol BTCUSDT --save-raw <file> > doctor.txt 2>&1
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
| `decision: auth` | ключ не задан/неверен или квота; проверить `VENDING_SECRET_CONN_DREX_APIKEY` |
| `candles: invalid_request` | символ/интервал не принят Binance; проверить `--symbol BTCUSDT` |
| `candles: ip_banned` | IP забанен; не ретраить, сменить сеть |
| `events: 0` через 5 минут | `serve` не запущен или смотрит в другой `--data-dir` |
| `labeled: 0` через 25 минут | не хватает закрытых баров, нет сети, или не было сигналов; проверить `lastCycleError` |
| `verdicts: ABSTAIN=<n>` | провайдер ошибся; код причины — в `gate.verdict` (`provider_auth`, `provider_unavailable`, `invalid_output`, `provider_misconfigured`) |
| `lastCycleError` не пуст | цикл продолжает работать; ошибка повторяется каждый интервал — прислать текст в отчёте |
| `journal chain broken at seq N` | журнал повреждён; не продолжать, сохранить файл и сообщить |

## Итог проверки

Запишите: дату/время (UTC), версию, префикс policy, число событий, меток и
вердиктов, коды `doctor --live`, результат `journal verify`. Эти данные —
вход в G1 и в решение о следующей итерации клина.
