# 03. Ядро и CLI

Статус: релиз-кандидат `0.1.0-m1` (2026-11-16), **не опубликован**. M0 (wedge
core) готов; M1 часть 1 готова 2026-11-16 (локальный shadow-гейт, разметка
исходов, отчёт только по журналу); M1 часть 2 готова 2026-11-16 (shadow-раннер,
trend-сигналы, Telegram, арена моделей, `--share`, policy-фильтр, локальный
релизный инструмент). RC добавляет `doctor --live`, канонические символы,
`entryLagMs`/`entryShifted`, `outcome.unlabelable`, группировку отчёта по
(horizon, fee), обновлённый `--share`, оценку объёма `signals estimate`, поля
окна `days`/`activeDays` в JSON-отчёте и операционную документацию. Phase B
добавляет `init --quick`, `doctor --strict`, `vend demo` и блок G1
(`labels`, `active days`, `max gap`, `labelsDue`) в `status`/`report`.
Зависит от:
`01-architecture.md`, `02-monorepo.md`.

> Реализовано в M0/M1 — см. §2.0. Подразделы §2.1–§2.10 описывают целевую
> платформу; из них реализованы только отмеченные `journal head/tail/verify` и `serve`,
> остальное — **не реализовано**: адаптер TradingView webhook, desktop/web/mobile,
> paper/live, команды Telegram (двусторонний бот).
> Не вызывать реальные API в тестах; живые вызовы — только ключом владельца.

## 1. Два бинарника

| Бинарник | Роль | Особенности |
|---|---|---|
| `vend` | CLI, «пульт» | Мгновенный старт, `--json`, скриптуемость, офлайн-команды |
| `vendd` | демон/сервер | Event loop, провайдеры, API, планировщик, OMS |

`vend` не содержит торговой логики: он либо работает офлайн (конфиг, схемы,
бэктест), либо обращается к локальному API `vendd`. Это даёт одинаковое поведение
desktop/web/mobile и CLI — все ходят в один контракт.

> В M1 существует только `vend`: его `serve` поднимает локальный API и фоновый
> раннер; отдельный демон `vendd`, `run/` и планировщик — цель следующих этапов.

Жизненный цикл:

```
vend run start            # если vendd не запущен — auto-start (detached) + hint
vend daemon status|start|stop|restart
vendd                     # foreground, логи в stderr
vendd --config path --addr 127.0.0.1:8420
```

Состояние: `~/.local/share/lv24-vending/` (`%LOCALAPPDATA%` на Windows, `~/Library/
Application Support` на macOS): `vending.db` и `shadow-status.json` в M1; `run/`
(pid, unix-socket), `logs/`, `cache/`, `packs/` — целевая платформа.

## 2. Команды

### 2.0 Реализовано в M0/M1 (shadow-гейт)

Установка (бинарник или сборка) — [install](../install.md) /
[install.ru](../install.ru.md); живая проверка на 15 минут —
[runbook-live-smoke.ru.md](../runbook-live-smoke.ru.md); выпуск предрелиза —
[release-checklist](../release-checklist.md).

```bash
vend help [COMMAND]
vend version [--json]
vend init [--quick] [--config FILE] [--data-dir DIR] [--force] [--json]
vend config path|get KEY|validate [--config FILE] [--data-dir DIR] [--json]
vend doctor [--strict] [--live] [--symbol SYMBOL] [--save-raw FILE] [--config FILE] [--data-dir DIR] [--json]
vend demo [--data-dir DIR] [--json] [--keep] [--share]
vend gate test --request FILE [--fixture FILE] [--save-raw FILE] [--force] [--config FILE] [--data-dir DIR] [--json]
vend gate report [--policy HASH] [--include-backfill] [--share [--out FILE [--force]]] [--allow-broken-chain] [--json] [--config FILE] [--data-dir DIR]
vend serve [--addr 127.0.0.1:8420] [--config FILE] [--data-dir DIR]
vend outcomes label [--candles FILE] [--json] [--config FILE] [--data-dir DIR]
vend signals estimate [--days N] [--json] [--config FILE] [--data-dir DIR]
vend status [--json] [--config FILE] [--data-dir DIR]
vend journal head [--json] [--data-dir DIR]
vend journal tail [--kind KIND] [--limit N] [--json] [--data-dir DIR]
vend journal verify [--json] [--data-dir DIR]
```

`vend help`, `vend help <command>`, `vend --help`, `vend -h`, `vend <command> --help`
и `vend <command> -h` печатают справку в stdout и возвращают 0; неизвестная тема
справки — код 2 со списком команд. Вызов без команды печатает usage в stderr и
возвращает 2. Справка перечисляет те же команды и флаги, что и код.

`vend init` создаёт конфиг и пустой журнал, печатает пути, режим и блок
`next steps:` из четырёх команд: `vend config validate`, `vend doctor`,
`vend gate test` и `vend serve` — с подставленными `--config`/`--data-dir`.
`--json` добавляет к прежним полям аддитивное `nextSteps` (массив строк с теми же
командами); существующие поля не менялись.

`vend init --quick` пишет профиль `g1-10x15m`: `signals.trend.enabled=true`,
десять USDT-символов (BTCUSDT, ETHUSDT, SOLUSDT, BNBUSDT, XRPUSDT, DOGEUSDT,
ADAUSDT, LINKUSDT, AVAXUSDT, LTCUSDT), `signals.trend.timeframe=15m`,
`signals.trend.sides=["buy","sell"]`, `outcome.labelIntervalSec=300`; гейт,
провайдеры, fast/slow и комиссии остаются из defaults. Профиль проходит
`config.Validate` (15m ≥ 2×300 с). Команда печатает шаблоны `export` для bash и
`$env:` для PowerShell с именами `VENDING_SECRET_CONN_LOCAL_APITOKEN` и
`VENDING_SECRET_<REF>` от `providers.decision.secretRef`; вместо значений стоит
`<token>`, секреты на диск не пишутся. Затем печатается `next steps:`:
`vend config validate`, `vend doctor --strict`, `vend serve`. `--json` добавляет
аддитивные `profile` и `nextSteps`; полей под значения ключей в выводе нет.
Существующий конфиг не перезаписывается (код 4) без `--force`; `--force`
работает как у обычного `init`.

Глобальные флаги `--config FILE`, `--data-dir DIR`, `--json` принимаются в любом
месте аргументов (`--flag value` или `--flag=value`, одинарный или двойной дефис);
`--` прекращает их разбор. Приоритет путей: флаг > `VENDING_CONFIG_FILE` /
`VENDING_DATA_DIR` > ОС-default. Файловые выводы `--out` и `--save-raw` не
перезаписывают существующий файл без `--force` и не пишут внутрь каталога данных
(там `vending.db` и `shadow-status.json`): команда возвращает ошибку с причиной и
подсказкой про `--force`, а `vending.db` и конфиг не перезаписываются никогда.
Конфиг — JSON
(`apiVersion: "vending.lv24/v1"`, режим `shadow|paper|sandbox`; `live`
отклоняется валидацией):

```json
{
  "apiVersion": "vending.lv24/v1",
  "mode": "shadow",
  "gate": { "version": "gate-v1", "minPass": 0.55, "minBlock": 0.5 },
  "outcome": { "horizonBars": 12, "feeBps": 10, "labelIntervalSec": 300 },
  "providers": {
    "decision": {
      "id": "decision-drex", "model": "drex-max-v1.1", "profile": "drex",
      "baseUrl": "", "secretRef": "conn/drex/apiKey", "pricePerBillion": "60"
    },
    "marketdata": { "id": "marketdata-binance", "baseUrl": "https://api.binance.com" }
  },
  "notify": {
    "telegram": { "enabled": false, "chatId": "", "secretRef": "conn/telegram/botToken" }
  },
  "signals": {
    "trend": {
      "enabled": false, "venue": "binance", "symbols": ["BTCUSDT"], "timeframe": "1h",
      "fast": 12, "slow": 26, "sides": ["buy"]
    }
  },
  "arena": {
    "enabled": false, "profile": "drex", "model": "drex-max-v1.1",
    "baseUrl": "", "secretRef": "conn/drex/apiKey", "pricePerBillion": "60"
  }
}
```

Значения выше — defaults `vend init`. Валидация: `gate.minPass`/`minBlock` в
(0, 1]; `outcome.horizonBars` 1–1000; `outcome.feeBps` 0–10000 (за одну
сторону, отчёт печатает round-trip = 2×); `outcome.labelIntervalSec` 1–86400;
`providers.decision.model` — конкретный id без `*-latest`; `profile` — `drex`
или `jev`; `providers.marketdata.baseUrl` — http(s); `notify.telegram` при
`enabled` требует `chatId` и `secretRef`; `signals.trend` при `enabled` требует
непустой `venue`, непустые `symbols`, `timeframe`, `0 < fast < slow`, `sides` из
`buy|sell`;
`arena` при `enabled` требует `profile` `drex|jev`, конкретный `model` (без
`*-latest`/`*-preview`), `secretRef`, `pricePerBillion > 0` и `baseUrl` —
https либо loopback http (`127.0.0.1`, `::1`).

Режим из конфига — фактический: он возвращается в `GET /healthz` и в ответе
гейта. M1 ничего не исполняет (ордеров нет); `paper`/`sandbox` — задел
следующих этапов.

Окружение: `VENDING_CONFIG_FILE`, `VENDING_DATA_DIR`; секрет провайдера —
`VENDING_SECRET_<REF>`, где REF — `secretRef` в верхнем регистре с заменой каждого
символа вне `[A-Za-z0-9]` на `_` (пример: `conn/drex/apiKey` →
`VENDING_SECRET_CONN_DREX_APIKEY`, `conn/telegram/botToken` →
`VENDING_SECRET_CONN_TELEGRAM_BOTTOKEN`). Токен локального API —
`VENDING_SECRET_CONN_LOCAL_APITOKEN` (обязателен, без него сервер не стартует).
Реальных значений в документации нет — только `<token>`, `<chat-id>` и `<path>`.

`vend serve`: слушает только `127.0.0.1`, default `127.0.0.1:8420`; токен обязателен;
тело ≤ 64 КиБ; только `application/json`; максимум 8 параллельных вычислений;
таймауты чтения/записи/idle; graceful shutdown; ошибки — `{code,message,hint,
retryable,details?}`; access-лог содержит только method/path/status/duration.
`serve` также запускает фоновый shadow-раннер (§2.0.1) и останавливает его до
закрытия журнала.

API (локальный, loopback):

- `GET /healthz` → `{"status":"ok","mode":"shadow","version":"..."}` (без auth).
- `POST /v1/gate` с `Authorization: Bearer <token>`, `Content-Type: application/json`,
  телом = GateRequest (`signalId`, `instrument{venue,symbol}`, `side` buy|sell,
  `intentKind` entry|exit, `timeframe`, `ts`, `price`, `source`, `state` — объект
  конечных чисел). `timeframe` — положительное число и единица `m`, `h`, `d`
  или `w` в нижнем регистре (`5m`, `4h`, `1d`); иное (`4H`, `1M`, `60`)
  отклоняется с HTTP 400 до записи в журнал, потому что такой сигнал нельзя
  разметить. Ответ 200:
  `{"mode":"shadow","signalId":"...","verdict":{"verdict":"ALLOW|REJECT|ABSTAIN|SKIP",
  "code":"...","reasons":[...],"checks":[...],"decisionId":"...","model":"...",
  "policyHash":"<64 hex>","decidedAt":"..."}}`.
  Идемпотентность по `signalId`: повтор с идентичным телом возвращает записанный
  вердикт с тем же `decisionId` без обращения к провайдеру; повтор с **другим**
  телом — HTTP 409 `conflict` (ничего не пишется, нужен новый `signalId`).
  Ошибка провайдера всё равно записывает ABSTAIN; HTTP для вычисленного
  вердикта — 200. Код причины ошибки провайдера: `provider_auth`
  (`auth`/`payment_required`), `provider_misconfigured` (`invalid_request`),
  `provider_unavailable` (`unavailable`/`timeout`/неизвестное),
  `invalid_output` (`invalid_output`). Для auth/misconfigured причина
  обобщена: текст провайдера с токеном или телом запроса в журнал не попадает.

События журнала на один сигнал: `gate.request` → `decision.response` (когда провайдер
ответил) → опционально `decision.model_changed` → `gate.verdict`; все связаны одним
`decisionId` и `signalId`. Событие model changed — когда фактическая модель при том же
запрошенном id отличается от предыдущей (ADR-0010).

`gate test --fixture` проигрывает записанный ответ decision offline;
`--save-raw` пишет сырое тело вендора атомарно (0600) и завершается ошибкой при
идемпотентном повторе (сырого тела нет); существующий файл заменяется только с
`--force`. Повтор с тем же `signalId` — replay: вывод помечается
`replayed: true`, провайдер не вызывается, новых событий нет. Если `--fixture` не
совпадает с записанным `decision.response`, команда завершается ненулевым кодом с
сообщением `fixture differs from journaled decision; change signalId or use
another --data-dir`; одинаковый fixture — replay с кодом 0. С `--json` печатается
объект вердикта.

`doctor` без сети проверяет конфиг, каталог данных (запись), БД, целостность
журнала, реестр провайдеров, наличие секрета (без значения) и policy hash.
`--live` добавляет три опциональные сетевые пробы с дедлайном 20 с: `decision`
(один вызов с фиксированным синтетическим state; ответ проходит через
`gate.Evaluate`, печатает `ok verdict=<ALLOW|REJECT|ABSTAIN> code=<код>
cost=<decimal> USD`; ABSTAIN с кодом `invalid_output` — FAIL пробы), `candles`
(две закрытые свечи `1h` по `--symbol`,
по умолчанию `BTCUSDT`) и `telegram` (одно тестовое сообщение, только если
`notify.telegram.enabled`). Проба печатает `ok` или короткий код: `auth`,
`payment_required`, `rate_limit`, `invalid_request`, `invalid_output`,
`unavailable`, `timeout`, `ip_banned`, `region_unavailable` (`unknown`
печатается как `unavailable`). Токены, URL и тела не печатаются. `--symbol` и
`--save-raw` требуют `--live`; `--save-raw FILE` пишет сырой ответ decision
атомарно (0600). `--json` отдаёт `{"ok":bool,"checks":[{name,ok,detail,live?}]}`;
любая FAIL-проба даёт код возврата 1.

`doctor --strict` добавляет к тем же офлайн-проверкам инварианты профиля G1 и
возвращает 1 при первом нарушении, называя поле: `trend`
(`signals.trend.enabled=true`), `symbols` (не меньше 10), `timeframe` (ровно
`15m` и разбирается `domain.ParseTimeframe`), `label_interval`
(`outcome.labelIntervalSec > 0` и не больше половины таймфрейма),
`decision_secret` (`providers.decision.secretRef` непустой и его
`VENDING_SECRET_*` задан). Токен `VENDING_SECRET_CONN_LOCAL_APITOKEN`
проверяется всегда, как и в обычном `doctor`. При успехе печатается
`profile ready; volume unverified` и `next: vend signals estimate --days 7`;
строка «G1 ready» не печатается никогда — объём сигналов проверяется только
сетью. `--json` добавляет `{"strict":true,"profile":...,
"volumeVerified":false,"nextSteps":[...]}`. Без `--strict` поведение прежнее:
отсутствие секрета decision-провайдера остаётся предупреждением.

`outcomes label`: для каждого entry-запроса (`intentKind: entry`) без метки —
свечи запрашиваются от начала бара, в котором лежит якорь, если оно позже `ts`
сигнала; первый бар с OpenTime ≥ ts сигнала, закрывшийся после вердикта гейта
(при отсутствии вердикта — после приёма); ровно N подряд идущих баров (таймфрейм
m/h/d/w); вход = open первого бара, выход = close бара N; gross со знаком side;
net = gross − 2·feeBps/10000; MFE/MAE; `receivedAt` = время приёма сигнала.
Символы сравниваются канонически (`domain.CanonicalSymbol`): trim, верхний
регистр, удаление `/`, `-`, `_` и фьючерсного суффикса после первого `:`;
`BTC/USDT`, `BTC-USDT` и `BTCUSDT` — один рынок `BTCUSDT`.
Фьючерсный суффикс (`BTCUSDT:USDT`) в M1 **отклоняется гейтом до вызова
провайдера**: единственный источник свечей — spot, и такой сигнал нельзя
разметить; ошибка (HTTP `bad_request`, доменный код `invalid_request`) называет
причину.
Если входной бар открылся раньше момента доступности сигнала более чем на
`min(10 % бара, 30 секунд)`, вход сдвигается на следующий бар (`entryShifted`)
при наличии запасного бара; без запасного бара сигнал ждёт следующего цикла и
исход без переноса не пишется. `entryLagMs` — задержка от open входного бара до
этого момента (0, если бар открылся позже); поле записывается всегда, поэтому
старые исходы без него не считаются нулевой задержкой. Сигнал, пришедший позже
чем через один таймфрейм (`receivedAt − ts > timeframe`), помечается
`backfill`; candlesHash = 64 hex
SHA-256 использованных баров (Decimals нормализованы к 12 знакам); `--candles
FILE` — offline JSON-массив объектов свечей, без него — настроенный
marketdata-провайдер; повтор не дублирует метки. Сигналы без честного
форвард-окна (пропуск или сдвиг баров) не размечаются и считаются в
`unlabelable`; пропуск после вызова источника и разрыв ставят сигнал на паузу
1, 2, 4 … интервала (не реже раза в час), а предупреждение пишется один раз на
сигнал. Перед записью `outcome.unlabelable` по сроку (7 суток после окна)
делается одна попытка разметки. Ошибка источника `invalid_request` повторяется
не дольше 24 часов после закрытия окна, затем пишется одно
`outcome.unlabelable` с причиной `source_rejected`, и сигнал больше не
запрашивается; до этого момента действует обычный backoff. После 7-суточного
срока любая другая ошибка источника повторяется не дольше 24 часов, затем
пишется `timeout`. Терминальная запись не повторяется и источник по такому
сигналу не вызывается. Контракт определяет для них событие
`outcome.unlabelable {signalId, decisionId, reason}`. `vend outcomes label`
печатает labeled/skipped/already/failed/unlabelable.

`gate report`: только журнал, без сети. Всегда: счётчики вердиктов, окно
`window: days=N active=N` (дни по времени приёма `gate.request` в журнале,
а не по `ts` из тела сигнала; `days` — от первой до последней UTC-даты
включительно, `active` — число разных UTC-дат с хотя бы одним запросом), число
размеченных, число ожидающих разметки entry-сигналов (`awaiting labeling`),
число сигналов без исхода по сроку (`no outcome by deadline`,
`outcome.unlabelable` с причиной `timeout`), число сигналов, отклонённых
источником (`source rejected`, причина `source_rejected`), policy hash (и
число решений других политик), backfill
`labeled/excluded`, суммарная стоимость решений, latency p50/p95 (по
usage.latencyMs), блок арены при наличии `arena.decision`. Сравнения смешивают
только последнюю по seq пару `(horizonBars, feeBps)`; метки с другой парой
считаются в `otherLabelParams` (JSON), а не подмешиваются. Допущения отчёта:
горизонт, round-trip fee, price source, equal size/no slippage/no funding,
`entryLagMedian`/`entryLagP95` (доля одного бара и миллисекунды) и
`entryShifted` (число сдвинутых входов); lag считается только по исходам, где
поле `entryLagMs` записано. Перед отчётом проверяется hash-chain журнала: разрыв
даёт exit ≠ 0 с номером seq и причиной; `--allow-broken-chain` продолжает отчёт
по прежнему поведению. Меньше 100 размеченных — `insufficient data`, сравнений
НЕТ.

После `window:` печатается блок прогресса G1, построенный по журналу:

```text
g1: labels N/100, active days D/7, max gap Hh, eligible=yes|no
last label age: <h>h|never
expected label interval: <h>h|n/a
labels due: <n>|unknown
```

`labels` — policy-scoped размеченные исходы; backfill не входит никогда, в том
числе с `--include-backfill` (поэтому с этим флагом `labels` может быть меньше
`labeled:`); `active days` — число разных UTC-дат с `gate.verdict`
(ABSTAIN считается ответом), а не с `gate.request`; `max gap` — самая длинная
пауза между соседними вердиктами (порог непрерывности 36 ч, константа);
`eligible` — true только при `labels ≥ 100`, `activeDays ≥ 7`,
`maxGapHours ≤ 36` и несинтетическом журнале; `last label age` — возраст
последнего `outcome.labeled` (`never`, если меток нет); `expected label
interval` — `(horizonBars + 1) × timeframe`; `labelsDue` — entry-сигналы, у
которых окно разметки истекло, а ни `outcome.labeled`, ни
`outcome.unlabelable` нет (`unknown`, если окно нельзя вывести). Те же числа в
`--json` — объект `g1` (`labels`, `targetLabels`, `activeDays`, `targetDays`,
`maxGapHours`, `eligible`, `synthetic`, `lastLabelAgeHours`,
`expectedLabelIntervalHours`, `labelsDue`) и верхнеуровневый `synthetic`.
Синтетика: любое событие с `runId=demo` даёт `synthetic: true`, G1-счётчики
обнуляются, human/`status` печатают `SYNTHETIC — not G1 evidence` и
`g1: 0/100, 0/7`; `--share` на таком журнале — exit 2 с сообщением
`synthetic journal is not G1 evidence`; `--json` отдаёт `synthetic: true`, но
G1 не засчитывает. `--share` печатает те же числа строками `g1 labels`,
`g1 active days`, `g1 max gap hours`, `g1 eligible`, `g1 labels due`,
`g1 last label age hours`, `g1 expected label interval hours` (и
`g1 synthetic: true (not G1 evidence)` для синтетики).

При ≥ 100: суммы и средние net по группам all/ALLOW/REJECT/ABSTAIN,
bootstrap-разница ALLOW−REJECT (95 % CI, 2000 прогонов, seed 42; интервал
считается только при ≥ 30 наблюдений в каждой группе и ≥ 5 различных UTC-днях
входа, иначе печатается точечная средняя и `insufficient group size` или
`insufficient days`), Brier, base rate, constant Brier, Brier skill score, AUC,
ECE, 10-бакетная кривая надёжности, разбивка по моделям (от 30 меток и ≥ 30
калибровочных пар на модель). При < 30 калибровочных парах human и `--share`
печатают `calibration pairs: N (need 30)`, а `brier`, `base rate`,
`constant brier`, `bss`, `auc`, `ece` и `reliability` — `n/a`; нулей нет.
`bss: n/a` также при base rate 0 или 1 (константный прогноз идеален, skill score
не определён), `auc: n/a` при одном наблюдаемом классе. Флаги:
`--policy <64-hex>` — метки и сравнения только для выбранной политики (счётчики
вердиктов остаются по всему журналу, число решений других политик печатается
отдельно); `--include-backfill` — включить поздние метки в сравнение (по
умолчанию они только считаются), но не в G1: счётчик меток G1 игнорирует
backfill при любом флаге. `--share` печатает обезличенную
markdown-сводку: версию, префикс policy (8 hex), число решений, `days` и
`active days` по времени приёма `gate.request` (UTC; `days` — включительный
интервал от первой до последней даты, `active days` — число разных дат), число
меток, число ожидающих разметки, просроченных без исхода и отклонённых
источником (`awaiting labeling`, `no outcome by deadline`, `source rejected`),
счётчики вердиктов, метрики или `insufficient data`, допущения с entry lag
median/p95 (в долях бара и в секундах) и entry shifted, ссылку на GitHub
Discussions. В сводке нет символов, площадок, цен, размеров позиций, state,
путей, идентификаторов и отдельных доходностей; агрегированная разница средней
доходности ALLOW и REJECT с её 95 % интервалом включена, когда метрик достаточно.
`--out FILE` пишет её с правами 0600, отказывается перезаписывать существующий
файл без `--force` и писать внутрь каталога данных. `--share` несовместим с
`--json`, `--out` требует `--share`. В `--json` те же числа окна отдаются
необязательными полями `days` и `activeDays`; существующие поля не менялись.
Деньги — только Decimal (строки); float — только вероятности/метрики.

`vend status`: снимок `shadow-status.json` (версия, policyHash, последняя
ошибка цикла, updatedAt, lastSeq) плюс пересчёт событий и меток по журналу.
`lastLabelAt` — время последнего события `outcome.labeled` в журнале: и
работающий цикл, и раннер после рестарта берут его оттуда, поэтому рестарт не
обнуляет и не сдвигает значение. Без запущенного
раннера `policy: none`, `last label: never`. `--json` отдаёт
поля `version`, `policyHash`, `events`, `labeled`, `lastLabelAt`,
`lastCycleError`, `updatedAt`, `lastSeq`. До первого `serve` (`shadow-status.json`
ещё нет) `policyHash`, `lastLabelAt`, `lastCycleError`, `updatedAt` и `lastSeq`
опускаются как пустые, а не отдаются в виде `"0001-01-01T00:00:00Z"` или пустой
строки; `events` и `labeled` по-прежнему считаются по журналу.
Human-вывод печатает после `labeled:` тот же блок G1, что и `gate report`
(`g1:`, `last label age`, `expected label interval`, `labels due`);
`--json` добавляет объект `g1` с теми же полями и `synthetic`, построенный по
журналу, а не по снимку раннера.

Хэши журнала: все `hash`, `prevHash`, `policyHash`, `candlesHash` — 64 строчных hex БЕЗ
префикса `sha256:`. Правило цепочки:
`hash = SHA-256(prevHash + "\n" + canonicalJSON(событие без hash))`; первый prevHash =
64 нуля; canonical payload = отсортированные ключи, без пробелов, числа литералами,
дубликаты ключей отвергаются; SQLite хранит canonical payload, Verify сравнивает
хранимые байты с каноном. policyHash покрывает пороги политики, порядок проверок и
полный набор вопросов (id, инструкции, ключи/описания опций). Детали гарантий —
в `06-risk-execution.md` §11.

### 2.0.1 Фоновый shadow-раннер

`vend serve` запускает раннер: первый цикл сразу, далее каждые
`outcome.labelIntervalSec` (default 300). Цикл: (a) разметка исходов,
(b) скан trend-сигналов и оценка каждого через gateflow, (c) уведомления,
(d) атомарная запись `shadow-status.json` в каталог данных (0600). При
включённом `signals.trend` скан trend идёт по своему таймеру — через 2 секунды
после закрытия каждого бара trend, — а не по таймеру разметки. Ошибка
любой фазы не останавливает цикл: она попадает в `lastCycleError` и в лог, а
следующий цикл повторяется. При остановке `serve` раннер завершает текущий шаг
до закрытия журнала.

`signals.trend` (при `enabled`): локальный источник без своего I/O; читает
закрытые свечи настроенного marketdata, считает EMA fast/slow и выдаёт вход на
пересечении. Символы канонизируются (`domain.CanonicalSymbol`) и
дедуплицируются; `venue` — непустой id площадки, проверяется валидацией.
Первый скан символа оценивает только последний закрытый бар (история нужна
лишь для прогрева EMA); повторный — только бары, закрывшиеся после
предыдущего скана, поэтому уже оценённые бары не переигрываются, а пересечения
на пропущенных барах не теряются. `signalId` детерминирован
(`trend:<symbol>:<timeframe>:<closeTimeUnix>:<side>`), поэтому повторная
оценка того же бара идемпотентна.

`vend signals estimate [--days N]` — офлайн-оценка объёма до недели: для каждого
символа `signals.trend` запрашивает у настроенного marketdata-провайдера
закрытые свечи за N суток (1–30, по умолчанию 7) плюс прогрев EMA, считает
пересечения тем же чистым расчётом, что и боевой скан (`trend.Crossings`), с
учётом `signals.trend.sides` и печатает по строке на символ
`SYMBOL signals=N per_day=X.X`, затем `total: signals=N per_day=X.X`,
`target: 15 per day (100 labeled in 7 days)`, `verdict: enough|low` и
`past frequency does not predict future frequency`. Команда не открывает
журнал, не вызывает decision-провайдера и Telegram и не требует секретов.
Выключенный `signals.trend` или пустые `symbols` — код 2 (usage) с подсказкой;
ошибка провайдера по одному символу печатается строкой `SYMBOL error=<code>`,
остальные символы считаются, код возврата 6. Запрос свечей привязан к началу
окна (`From` = начало окна минус прогрев), поэтому провайдер с постраничной
выдачей отдаёт всё окно, а не только последнюю страницу; если история символа
начинается внутри окна, вместо заниженной оценки печатается
`SYMBOL error=invalid_request` (код возврата 6, подсказка — уменьшить `--days`).
`--json` отдаёт один объект
`{days, targetPerDay, symbols:[{symbol, signals, perDay, error?}],
total:{signals, perDay}, verdict}`; отдельная схема для него в `contracts/` не
заводится — служебные ответы CLI (version, doctor, status) схем не имеют.

`vend demo [--data-dir DIR] [--json] [--keep] [--share]` — офлайн-демонстрация
без провайдеров, сети и ключей. Команда создаёт временный каталог (если
`--data-dir` не задан), пишет журнал с `runId=demo` за 7 UTC-дней, не меньше
120 решений и не меньше 100 `outcome.labeled` с фиксированным seed и печатает
полный human-отчёт того же builder'а, что и обычный журнал: вероятности и
исходы детерминированы, Brier/ECE/AUC и counterfactual считаются реальным
кодом. Баннер `SYNTHETIC - not real market data` стоит в начале и в каждом
крупном разделе (минимум: заголовок, метрики, допущения). `--json` отдаёт
`synthetic: true` и те же числа. `--share` не поддерживается: exit 2 с
сообщением `synthetic journal is not G1 evidence`. Временный каталог удаляется
без `--keep`; явный `--data-dir` не удаляется, а существующий журнал в нём
отклоняется (exit 4), чтобы demo никогда не перезаписал реальные данные. С
`--keep` печатаются путь и подсказка `vend status --data-dir <path>`. Demo не
вызывает провайдеров, не пишет за пределы своего каталога и не засчитывается в
G1: `status --data-dir <demo>` показывает `SYNTHETIC` и `g1: 0/100, 0/7`.
Отдельной JSON-схемы у команды нет.

### 2.0.2 Уведомления и арена

`notify.telegram` (при `enabled`) шлёт через Bot API: смену фактической модели
(`decision.model_changed`), 3 подряд ABSTAIN с кодом `provider_unavailable`,
отдельные алерты `provider_auth` и `provider_misconfigured` и недельную сводку
counts (`decisions`, `labeled`, `ALLOW/REJECT/ABSTAIN`), когда первый
`gate.request` старше 7 суток и с последней успешной сводки прошло 7 суток.
Алерты строятся по журналу: алерт нужен, только если есть вердикт с этим кодом
и seq больше seq последнего доставленного алерта этого кода, и с доставки
прошёл час; без новых сбойных вердиктов повтора нет, рестарт ничего не
отправляет повторно. Алерт `provider_auth`/`provider_misconfigured`
отправляется, только если последний вердикт с этим кодом не старше 24 часов:
давно исправленный сбой не будит оператора. За цикл отправляется не более
одного уведомления. Недоставленная отправка повторяется с паузой 1, 2, 4 …
цикла (не реже раза в час). Успешная отправка пишется событием `notify.sent
{kind,channel,ok,code?,uptoSeq?}`; `ok=false` пишется только при первой неудаче
серии, повторные неудачи серии в журнал не попадают. Локальный лимит провайдера
(одно сообщение в минуту) сбоем доставки не считается: `notify.sent` не
пишется, счётчик неудач не растёт, следующая попытка назначается на остаток
задержки (или на следующий цикл), в лог — только DEBUG. Токен бота берётся по
`secretRef`
(`VENDING_SECRET_*`), `chatId` (`<chat-id>`: числовой id или
`@channelusername`) секретом не является. Тексты — только агрегаты,
без символов, цен, путей и идентификаторов.

Арена (`arena.enabled`) — второе, совещательное мнение: gateflow вызывает
второй decision-провайдер отдельно, с дедлайном 20 с, вне пути основного ответа;
медленная или упавшая арена не меняет и не задерживает вердикт. Результат —
одно событие `arena.decision {decisionId, model, response, verdict}`; отчёт
агрегирует решения, согласие моделей, средний доход ALLOW, AUC и стоимость по
моделям. Арена удваивает стоимость решения (два вызова провайдера на сигнал),
поэтому включать её стоит осознанно.

### 2.0.3 Локальная сборка релиза

`go run ./tools/release` из корня репозитория собирает `vend` под
windows/amd64, linux/amd64+arm64, darwin/amd64+arm64 в `dist/` и пишет
`dist/SHA256SUMS`. Артефакты: `vend_windows_amd64.exe`, `vend_linux_amd64`,
`vend_linux_arm64`, `vend_darwin_amd64`, `vend_darwin_arm64`; манифест —
строки `<sha256>  <filename>`. Сборка локальная и воспроизводимая:
`CGO_ENABLED=0`, `-trimpath`, без VCS-штампа, версия `0.1.0-m1`; ничего не
публикуется и не загружается, бинарники не подписаны. Неполный набор
артефактов манифест не создаёт.

### 2.0.4 Целевая платформа (не реализовано)

Нижеследующие подразделы §2.1–§2.10 описывают целевую платформу. Полностью не
реализованы §2.2–§2.8; в §2.1 и §2.9–§2.10 реализованы только отмеченные
команды. Они сохранены как план; не использовать остальные команды в работе.

### 2.1 Онбординг

Реализованы `vend init` (неинтерактивный, с defaults из §2.0; `--quick` —
профиль G1; печатает next steps), `vend version` и `vend doctor` (§2.0;
`--strict` — инварианты профиля G1); интерактивный init, шаблоны,
`update` и `completion` — цель.

```bash
vend init                       # интерактивно: режим, биржа, ключи, агенты
vend init --template crypto-paper --non-interactive
vend doctor                     # окружение, БД, часы, сеть, провайдеры, ключи
vend doctor --json
vend version [--json]
vend update [--check]
vend completion powershell      # bash|zsh|fish|powershell
```

### 2.2 Провайдеры

```bash
vend provider catalog [--kind venue|marketdata|llm|decision|notifier]
vend provider show exchange-binance          # capabilities + config schema
vend provider connect exchange-binance --id binance-main --mode paper
vend provider test binance-main              # ключи, права, время сервера, rate limit
vend provider health [--watch]
vend provider remove binance-main
```

`provider show` выводит capability-матрицу человекочитаемо:

```
exchange-binance v1.4.0  (venue, marketdata)
  asset classes  spot, margin, futures
  order types    market, limit, stop, stop_limit, trailing, oco
  tif            gtc, ioc, fok, day, gtd
  features       post_only, reduce_only, user_data_stream, sandbox, batch
  rate limits    orders: 50/10s, market: 1200/min
  degradation    trailing_stop -> emulated by core (explicit)
```

### 2.3 Рынок

```bash
vend market instruments --venue binance-main --query BTC
vend market quote BTC/USDT
vend market candles BTC/USDT --tf 1h --limit 200 [--json|--csv]
vend market orderbook BTC/USDT --depth 20
vend market stream BTC/USDT --tf 1m [--jsonl]
```

`--jsonl` — поток строк (для пайпов), `--csv` — для таблиц/Excel.

### 2.4 Счёт и ордера

```bash
vend account list
vend account balances --account binance-main
vend account positions --account binance-main
vend account orders --open
vend account pnl --period 30d [--by instrument|strategy|agent]

vend order place --account binance-main --symbol BTC/USDT \
    --side buy --type limit --qty 0.01 --price 60000 --tif gtc --post-only
vend order place --json @order.json
vend order cancel <order-id|client-id>
vend order cancel-all --account binance-main
vend order show <id> --events
```

Ордер всегда проходит RiskGate. Если нужен approve:
`exit 9` + JSON `{ "status": "needs_approval", "approvalId": "..." }`.

### 2.5 Стратегии и запуски

```bash
vend strategy list [--builtin]
vend strategy new my-grid --from grid
vend strategy validate ./packs/my-grid
vend run start grid-btc --account binance-main --symbol BTC/USDT --mode paper
vend run list
vend run inspect <run-id>        # состояние, позиция, P&L, последние решения
vend run pause|resume|stop <run-id>
vend run logs <run-id> --follow
```

### 2.6 Агенты

```bash
vend agent list                       # роли, модели, бюджеты, статус
vend agent show analyst
vend agent test analyst --input examples/market-snapshot.json
vend agent run analyst --run <run-id> --once
vend agent explain <decision-id>      # полный trace: вход, модель, риск, исход
vend agent budget [--set dailyUsd=10]
vend agent matrix                     # кто за что отвечает (роль x вендор)
```

`vend agent matrix` — ключевая команда для «гибкой настройки, какой агент за что
отвечает»: таблица `role × venue × instrument-class → agent/model`, с подсветкой
пробелов и конфликтов.

### 2.7 Риск

```bash
vend risk policy list|show default
vend risk policy validate ./policies/strict.yaml
vend risk policy apply strict --account binance-main
vend risk simulate --proposal proposal.json --account binance-main
vend risk kill on [--reason "manual"] [--cancel-open]
vend risk kill off
vend risk status
vend approve list
vend approve accept <id> [--note "..."]
vend approve reject <id> --reason "..."
```

### 2.8 Бэктест

```bash
vend backtest run grid-btc --from 2025-01-01 --to 2026-01-01 \
    --symbol BTC/USDT --tf 1h --cash 10000 --fees 0.001 --slippage bps:5
vend backtest run grid-btc --data ./testdata/btc-1h.parquet --deterministic
vend backtest report <id> --metrics sharpe,drawdown,winrate --equity-curve
vend backtest compare <id-a> <id-b>
vend backtest walkforward grid-btc --windows 6 --train 0.7
```

Требование: **parity**. Один и тот же код стратегии исполняется live и в
бэктесте; отличие — только в портах (`Clock`, `Venue`, `MarketData`).

### 2.9 Журнал

Реализованы `journal head`, `journal verify` и `journal tail` (см. §2.0);
остальное — цель.

`vend journal tail [--kind KIND] [--limit N] [--json]` только читает журнал.
Он печатает последние N событий (1–1000, по умолчанию 20) в порядке возрастания
`seq`, при `--kind` — только события с точным именем вида (`gate.request`,
`decision.response`, `decision.model_changed`, `gate.verdict`, `outcome.labeled`,
`outcome.unlabelable`, `notify.sent`, `arena.decision`). Человеческий вывод — строка
`seq ts kind decisionId` без payload: в payload бывают цены и состояние
пользователя; отсутствующий `decisionId` печатается как `-`. `--json` отдаёт
массив объектов `{seq, ts, kind, decisionId, payload}`, payload — как в журнале.
Пустой журнал даёт пустой вывод и код 0. Неизвестный `--kind` — код 2, сообщение
`invalid --kind "..."` и список допустимых видов в тексте и в
`details.allowed`; неверный `--limit` — код 2 (usage). Цепочку команда не
проверяет (для этого `journal verify`) и ничего не пишет.

Цель:

```bash
vend journal tail --follow [--kind decision|order|risk|fill]
vend journal query --from 2026-11-01 --run <id> --jsonl
vend journal export --format jsonl|csv --out ./audit/
vend journal replay <run-id> --from <ts> --to <ts>   # детерминированный реплей
vend journal verify                                  # проверка hash-chain
```

### 2.10 Сервер и live

Реализован только `vend serve` на loopback без `--open` (§2.0); `live` и
desktop/web/mobile — цель.

```bash
vend serve [--addr 127.0.0.1:8420] [--open]
vend live status
vend live unlock --account binance-main --ttl 8h    # подтверждение в TTY/UI
vend live lock
```

Live-unlock: интерактивное подтверждение (ввод последних 4 символов account id +
фраза `I UNDERSTAND`), TTL, привязка к отпечатку ключей; сбрасывается при kill
switch, рестарте демона и изменении ключей.

## 3. Форматы вывода и коды возврата

- По умолчанию — человекочитаемая таблица с цветом (отключается `NO_COLOR`/`--no-color`).
- `--json` — один объект; `--jsonl` — поток; `--csv` — плоские таблицы.
- `--quiet` — только идентификаторы/значения; `-v/-vv` — диагностика в stderr.
- Прогресс — только в TTY (stderr), в пайпах молчит.

| Код | Значение |
|---|---|
| 0 | Успех |
| 1 | Общая ошибка |
| 2 | Неверное использование/валидация флагов |
| 3 | Не найдено (run, order, provider) |
| 4 | Конфликт состояния (уже запущено, несовместимый режим) |
| 5 | Нет прав / заблокировано (live не разблокирован, ключи не заданы) |
| 6 | Ошибка провайдера (сеть, 429, 5xx, отказ биржи) |
| 7 | Отказ риск-гейта |
| 8 | Таймаут |
| 9 | Требуется подтверждение (approval) |

Справка (`--help`, `-h`, `help`, `help <cmd>`, `<cmd> --help`) возвращает 0;
неизвестная команда или тема справки — 2. HTTP 409 `conflict` в CLI даёт exit 4 и
`code: conflict` (без двойного префикса `vend: conflict:`).

Ошибки — структурированные: `{code, message, hint, docsUrl, retryable, details}`.
`hint` обязателен: CLI должен говорить, что делать дальше.

## 4. Конфиг и профили (целевая платформа vs реализовано в M1)

Реализовано в M1 (JSON-конфиг, см. §2.0):

```bash
vend config path
vend config get providers.decision.model
vend config validate
```

`config get` принимает точечный путь по фактической схеме конфига; ключи
`config set`, `config diff` и иерархия scope (будущее) не реализованы.
Будущее (не реализовано): `config set ... --scope ...`, `config diff --scope ...`,
иерархия scope `global → account → run` с переопределением, запись денежных изменений
в `audit_log` с автором и diff. Эти команды и механики — целевая платформа.

## 5. Стратегии: встроенные и внешние

### 5.1 Strategy API и `StrategySpec`

```go
type Strategy interface {
    Spec() StrategySpec
    OnStart(ctx context.Context, s State) error
    OnBar(ctx context.Context, b Bar) []Signal
    OnQuote(ctx context.Context, q Quote) []Signal
    OnTick(ctx context.Context, t Tick) []Signal
    OnOrder(ctx context.Context, o OrderEvent) []Signal
    OnTrade(ctx context.Context, f Fill) []Signal
    OnTimer(ctx context.Context, at time.Time) []Signal
    OnStop(ctx context.Context, s State)
}

type StrategySpec struct {
    Params      []ParamSpec  // name, type, default, min, max, step, side
    Timeframes  []string
    WarmupBars  int
    SupportsShort bool
}
```

Правила: нет I/O, нет системного времени, нет глобального состояния; один и тот
же код в backtest/sandbox/live; параметры валидируются по `StrategySpec`, и из
него же генерируются CLI-флаги, формы UI и пространство оптимизации.

Встроенные (Go, компилируются в бинарник): `dca`, `grid`, `trend`, `rebalance`,
`breakout`. Полный список — в `core/internal/strategy/builtin`.

Внешние — WASM-пак:

```
my-strategy/
├── pack.json          # {apiVersion, kind: Strategy, id, version, abi: v1, params}
├── strategy.wasm      # сборка из Rust/Go/TinyGo
└── README.md
```

ABI `v1` (минимальный и стабильный):

```text
exports:
  abi_version() -> u32
  init(config_ptr, len) -> i32
  on_event(event_ptr, len) -> signals_ptr   # JSON in / JSON out
  dealloc(ptr, len)
imports (host):
  log(level, ptr, len)
  indicator(name_ptr, len, params_ptr, len) -> value
  emit_diagnostic(ptr, len)
```

Правила: нет сети, нет файловой системы, нет системного времени; часы приходят в
событии; лимит памяти 64 МБ и 10M топлива на вызов; детерминизм обязателен.
Пак валидируется (`vend strategy validate`), подписывается хэшем, версия ABI
проверяется при загрузке.

## 6. Внутренний event loop (упрощённо)

```go
for {
    select {
    case <-ctx.Done():
        return shutdown()
    case ev := <-marketEvents:            // lossy, коалесцируется
        hub.Apply(ev)
        runtime.OnMarket(ev)              // стратегии, watchers
    case ev := <-criticalEvents:          // at-least-once, outbox
        journal.Append(ev)
        runtime.OnCritical(ev)            // fills, orders, risk
    case <-ticker.C:
        scheduler.Tick(now)
        health.Probe(ctx)
        reconciliation.MaybeRun(ctx)
    }
}
```

Инварианты:
- Один горутин-владелец состояния `run`; внешние вызовы — через каналы/акторы.
- Никаких блокирующих вызовов в event loop: провайдеры — с таймаутами и в пулах.
- Критические события сначала в outbox, затем в обработку (crash-safe).
- Дедуп по `eventId`; порядок на инструмент — монотонный по `seq`.

## 7. Расширения (v1 и дальше)

| Точка | v1 | v2 |
|---|---|---|
| Стратегии | встроенные Go + WASM | marketplace паков |
| Агенты | YAML-манифесты + встроенные роли | внешние агенты по gRPC |
| Провайдеры | Go-модули, компилируются | out-of-process паки (gRPC, любой язык) |
| Политики риска | YAML + встроенные проверки | CEL/Rego-выражения |
| Нотификации | telegram/webhook/push | матрица маршрутов |

Версионирование ABI: `pack.json.apiVersion` + `abi: v1`; несовместимый пак
отклоняется с понятной ошибкой, а не падает.

## 8. Пример сессии

```bash
$ vend init --template crypto-paper
✓ конфиг создан: C:\Users\me\AppData\Roaming\lv24-vending\config.yaml
✓ БД инициализирована (sqlite, WAL)
✓ paper-провайдер подключён: paper-main
! live не разблокирован — торговля только в paper

$ vend provider connect exchange-binance --id binance-main
? API key: ****
? API secret: ****
✓ ключи сохранены в Windows Credential Manager
✓ test: spot read ok, trade ok, server time skew 42ms
! права на вывод средств обнаружены — отключите их на бирже (fail closed)

$ vend run start grid-btc --account binance-main --symbol BTC/USDT --mode paper
✓ run 01J... started (paper), strategy grid v1
$ vend run inspect 01J...
  equity 10 000.00 USDT  pnl +0.42%  orders 12  fills 9  exposure 18%
  last decision 2m ago: buy 0.001 BTC @ 59 812.5 (confidence 0.71)
$ vend agent explain 01J-DEC-7
  role       analyst (llm-openaicompat/gpt-5.5)
  input      market.candles 1h x200, orderbook top20, funding
  proposal   buy 0.001 BTC, confidence 0.71, ttl 5m
  risk       PASS (7/7): notional 59.81 < 1000, exposure 18% < 60%, ...
  approval   not required (paper)
  outcome    filled @ 59 815.0, slippage 2.5 bps
```
