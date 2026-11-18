# Быстрый старт

[English version](quickstart.md)

Три пути, от самого короткого:

| Путь | Время | Что нужно | Идёт в зачёт G1 |
|---|---|---|---|
| `vend demo` | секунды | только бинарник | нет — синтетический журнал |
| `vend init --quick` + `doctor --strict` | около 5 минут | ключ decision-провайдера и сеть для свечей | да, если оставить запущенным |
| Полный разбор и офлайн-replay | 15 минут | ключ decision-провайдера или записанная фикстура | только живая часть |

G1 — первый гейт: не меньше 100 размеченных решений за ≥ 7 UTC-дней без паузы
дольше 36 часов. `vend status` и `vend gate report` всегда печатают текущий
прогресс строкой
`g1: labels N/100, active days D/7, max gap Hh, eligible=yes/no`;
подробности — в [run-7-days.ru.md](run-7-days.ru.md).

Всё выполняется из корня репозитория. Платных сервисов и обращений к CI нет.

## Путь 1 — посмотреть за 60 секунд (`vend demo`)

`vend demo` пишет синтетическую неделю во временный журнал и печатает по нему
полный отчёт. Без ключа, без сети, без вызова провайдеров:

```bash
go build -o vend ./core/cmd/vend
./vend demo
```

В Windows соберите `vend.exe` и вызывайте `.\vend.exe demo`.

В журнале 7 UTC-дней, не меньше 120 решений и не меньше 100 размеченных
исходов; вероятности и исходы детерминированы (фиксированный seed), а Brier,
ECE, AUC и counterfactual считает тот же builder отчёта, что и для реального
журнала. Каждый крупный раздел сопровождается баннером `SYNTHETIC — not real market
data`; команда завершается за секунды.

Журнал demo помечен `runId=demo` и никогда не сойдёт за реальную работу:
`status` и `report` печатают `SYNTHETIC — not G1 evidence` и
`g1: 0/100, 0/7`, а `gate report --share` завершается кодом 2 с сообщением
`synthetic journal is not G1 evidence`.

Полезные флаги:

```bash
./vend demo --json                  # те же числа с "synthetic": true
./vend demo --keep                  # сохранить временный каталог данных
./vend demo --data-dir ./demo-data  # явный каталог
```

Без `--keep` временный каталог удаляется; явный `--data-dir` не удаляется. С
`--keep` команда печатает путь и готовую команду
`vend status --data-dir <path>`. Флаг `--data-dir` — только для нового каталога:
существующий `vending.db` отклоняется с кодом 4. Синтетическая метка остаётся в
журнале навсегда, поэтому не направляйте demo в реальный журнал и не начинайте
реальный прогон G1 в каталоге demo.

## Путь 2 — свой гейт за 5 минут (`init --quick`)

### 1. Получить бинарник

```bash
go build -o vend ./core/cmd/vend
./vend version
```

Windows PowerShell:

```powershell
go build -o vend.exe ./core/cmd/vend
.\vend.exe version
```

Если бинарник уже установлен, строку `go build` пропустите и вызывайте `vend`.
В текущем состоянии репозитория релиз **не опубликован**, поэтому рабочий
путь — сборка из исходников; см. [install.ru.md](install.ru.md).

### 2. Записать профиль быстрого старта

```bash
./vend init --quick
```

Windows: `.\vend.exe init --quick`.

`init --quick` создаёт конфиг и SQLite-журнал, печатает оба пути, имя профиля
`g1-10x15m` и следующие команды. Профиль — это конфиг по умолчанию, в котором
изменён только источник сигналов:

- `signals.trend.enabled: true` (обычный `init` оставляет `false` и пустые
  `symbols`, и тогда за семь дней не появится ни одного сигнала);
- десять USDT-символов: BTCUSDT, ETHUSDT, SOLUSDT, BNBUSDT, XRPUSDT,
  DOGEUSDT, ADAUSDT, LINKUSDT, AVAXUSDT, LTCUSDT;
- `timeframe: 15m`, `sides: ["buy", "sell"]`, `outcome.labelIntervalSec: 300`;
- гейт, провайдеры, fast/slow и комиссии остаются по умолчанию.

Существующий конфиг не перезаписывается: `init --quick` завершается кодом 4.
Передайте `--force`, чтобы заменить файл (правки при этом теряются), или
`--config <path>`, чтобы записать другой файл. `--json` добавляет к обычным
полям `profile` и `nextSteps`; значений секретов в нём нет.

### 3. Задать два секрета

`init --quick` печатает готовые шаблоны для bash и PowerShell; вместо значений
стоит `<token>`, на диск они не пишутся. Задайте обе переменные в той же
оболочке, где будут запускаться `doctor` и `serve`.

Bash — как печатает `init`:

```bash
export VENDING_SECRET_CONN_LOCAL_APITOKEN='<token>'
export VENDING_SECRET_CONN_DREX_APIKEY='<token>'
```

PowerShell — как печатает `init`:

```powershell
$env:VENDING_SECRET_CONN_LOCAL_APITOKEN = "<token>"
$env:VENDING_SECRET_CONN_DREX_APIKEY = "<token>"
```

`VENDING_SECRET_CONN_LOCAL_APITOKEN` — bearer-токен локального API; без него
сервер не стартует. `VENDING_SECRET_CONN_DREX_APIKEY` — ключ
decision-провайдера, выведенный из `secretRef` по умолчанию
`conn/drex/apiKey`. Значения должны быть разными случайными строками. Каждый
`secretRef` превращается в `VENDING_SECRET_<REF>`: верхний регистр, каждый
символ вне `[A-Za-z0-9]` заменяется на `_`.

### 4. Пройти офлайн-предстарт

```bash
./vend doctor --strict
```

`--strict` требует профиль G1 и завершается кодом 1, называя поле, если:

- `signals.trend.enabled` равен `false`;
- символов меньше 10;
- таймфрейм trend не `15m` (или не разбирается);
- `outcome.labelIntervalSec` больше половины таймфрейма;
- `providers.decision.secretRef` пуст или его `VENDING_SECRET_*` не задан;
- `VENDING_SECRET_CONN_LOCAL_APITOKEN` не задан.

Одновременно идут обычные офлайн-проверки: конфиг, каталог данных, база,
цепочка журнала, реестр провайдеров и policy hash. При успехе печатается
`profile ready; volume unverified` и следующая команда
`vend signals estimate --days 7`. Формулировка намеренная: doctor доказывает,
что установка настроена производить сигналы, а не что их достаточно — объём
измеряет только сетевой estimate. `doctor` без `--strict` работает как раньше.

`--json` возвращает `ok`, `checks`, `strict: true`, `profile`,
`volumeVerified: false` и `nextSteps`.

### 5. Запустить сервер

```bash
./vend serve --addr 127.0.0.1:8420
```

Сервер слушает только loopback и запускает фоновый раннер: каждые
`outcome.labelIntervalSec` (в этом профиле 300 с) он размечает исходы,
отправляет уведомления и пишет `shadow-status.json`; при включённом
`signals.trend` скан идёт через две секунды после закрытия каждого 15m-бара.
Процесс должен оставаться запущенным.

Перед неделей проверьте, сколько сигналов даёт конфиг:

```bash
./vend signals estimate --days 7
```

Ориентир — `verdict: enough`. Раздел «Ориентир по объёму» в
[run-7-days.ru.md](run-7-days.ru.md) объясняет числа и что менять при нехватке.
Дальше переходите к
[запросу в гейт](#2-отправить-запрос-в-гейт).

## Путь 3 — полный разбор

Остальная страница — подробный путь: настройка провайдера, HTTP-запрос,
вердикты, разметка, отчёты, статус и офлайн-replay. Быстрому профилю правки
конфига из первого подраздела не нужны.

### 1. Настроить провайдера в конфиге

```bash
./vend config path      # абсолютный путь к конфигу
./vend config validate  # проверка без сети и без ключей
```

В файле меняются эти значения (остальное остаётся как создал `init`); нужный
фрагмент:

```json
{
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
  }
}
```

Что проверяет валидация:

- `model` — конкретный id; алиасы вида `*-latest` отклоняются;
- `profile` — `drex` или `jev`;
- `secretRef` — ссылка `conn/<id>/<field>`, не сам ключ;
- `marketdata.baseUrl` — http(s) URL (по умолчанию Binance spot).

Полный набор ключей (разметка, Telegram, trend, арена) — в
[spec/03-core-cli.md](spec/03-core-cli.md).

Если вы начали с обычного `init` (без `--quick`), `signals.trend` выключен, а
`symbols` пуст, поэтому раннер сам не создаст ни одного сигнала. Либо выполните
`init --quick --config <другой-путь>` и скопируйте блок `signals.trend`, либо
включите trend вручную и проверьте его командой `vend doctor --strict`.

### 2. Отправить запрос в гейт

```bash
curl -sS -X POST http://127.0.0.1:8420/v1/gate \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  --data @contracts/examples/gate-request.json
```

В PowerShell 5.1 `curl` — алиас `Invoke-WebRequest`; вызывайте `curl.exe`.

`contracts/examples/gate-request.json` — шаблон. Замените `signalId` на
уникальный, а `ts` — на текущее UTC-время: со старым `ts` сигнал попадёт в
журнал как `backfill` (он размечается, но по умолчанию не входит в сравнения
отчёта), а повтор с тем же `signalId` вернёт записанный вердикт. Вместо
`<token>` подставьте значение `VENDING_SECRET_CONN_LOCAL_APITOKEN`
(PowerShell: `$env:VENDING_SECRET_CONN_LOCAL_APITOKEN`).

Ответ (HTTP 200):

```json
{
  "mode": "shadow",
  "signalId": "sig-001",
  "verdict": {
    "verdict": "ALLOW",
    "code": "allow",
    "reasons": ["all gate checks passed"],
    "checks": [
      { "name": "intent", "passed": true, "detail": "intentKind=entry" },
      { "name": "provider", "passed": true, "detail": "provider response ok" }
    ],
    "decisionId": "<decision-id>",
    "model": "drex-max-v1.1",
    "policyHash": "<64 hex>",
    "decidedAt": "<RFC 3339>"
  }
}
```

В реальном ответе `checks` содержит все проверки (`intent`, `provider`,
`answers`, `data_quality`, `risk_check`, `directional`, `confidence`); выше
показаны две.

`mode` — фактический режим из конфига (`shadow` по умолчанию) и возвращается
как есть. Гейт только оценивает: M1 не выставляет ордера, `live` отклоняется
валидацией конфига.

Тело запроса — `GateRequest`: `signalId`, `instrument{venue,symbol}`, `side`
(`buy|sell`), `intentKind` (`entry|exit`), `timeframe`, `ts`, `price > 0`,
`source`, `state` (объект конечных чисел). Пустой `signalId`, неизвестные поля
и данные после JSON отклоняются.

### 3. Прочитать вердикт

- `ALLOW` — все проверки прошли в порогах политики.
- `REJECT` — гейт заблокировал намерение (CLI отдаёт код 7, HTTP остаётся 200).
- `ABSTAIN` — провайдер ошибся или не определился; вердикт всё равно пишется в
  журнал и возвращается с HTTP 200.
- `SKIP` — оценка обойдена.

На каждый запрос в журнале появляются `gate.request` → `decision.response`
(если провайдер ответил) → опционально `decision.model_changed` →
`gate.verdict`, все с одним `decisionId`.

### 4. Разметка исходов идёт сама

Раннер размечает каждый entry-запрос (`intentKind: entry`) без исхода. Метка
берёт первый бар,
открывшийся не раньше времени сигнала и закрывшийся после вердикта гейта (при
отсутствии вердикта — после приёма), ровно `outcome.horizonBars` подряд идущих
баров, вход по open первого, выход по close последнего, net за вычетом
`2 × outcome.feeBps` (по умолчанию `feeBps: 10` за сторону, 20 round-trip).
Если первый бар открылся раньше момента, когда сигнал стал доступен, более чем
на `min(10% бара, 30 секунд)`, вход сдвигается на следующий бар
(`entryShifted`), а без запасного бара сигнал ждёт следующего цикла.
`entryLagMs` хранит задержку от open входного бара до этого момента. Свечи
берутся у `providers.marketdata`, поэтому на этом шаге нужна сеть до биржи.

При `horizonBars: 12` и 15m-барах быстрого профиля первая метка появляется
через `(12 + 1) × 15m ≈ 3.25` часа. С таймфреймом по умолчанию `1h` это около
13 часов. Для быстрой проверки используйте
[runbook](runbook-live-smoke.ru.md): там `horizonBars: 3` и ручной запрос с
`timeframe: "1m"`.

Сигналы, пришедшие позже чем через один таймфрейм
(`receivedAt − ts > timeframe`), помечаются `backfill`: они могут входить в бар,
уже закрывшийся к моменту приёма, и не входят в сравнения отчёта без
`--include-backfill`; в счётчик меток G1 они не входят никогда.

### 5. Отчёт после накопления меток

```bash
./vend gate report
```

Отчёт читает только журнал, сеть не нужна. Сначала проверяется hash-chain
журнала: разрыв даёт ненулевой код и номер seq. `--allow-broken-chain` всё равно
печатает отчёт (только для восстановления, не для обычной работы). Меньше 100
размеченных исходов — счётчики и `insufficient data`. От 100 меток появляются
доходы по группам и bootstrap-интервал; для интервала нужно ещё ≥ 30 исходов в
каждой группе и ≥ 5 различных дней входа. Качество модели (Brier, Brier skill
score, AUC, ECE, кривая надёжности) считается только при ≥ 30 калибровочных
парах: ниже порога каждая метрика — `n/a`, а строка имеет вид
`calibration pairs: N (need 30)`. Разбивка по моделям начинается от 30 меток
на модель.

Сразу после строки `window:` отчёт печатает блок прогресса G1:

```text
g1: labels 23/100, active days 4/7, max gap 9.5h, eligible=no
last label age: 0.6h
expected label interval: 13h
labels due: 1
```

`labels` — размеченные исходы выбранной политики (backfill исключён при любом
флаге); `active days` — число UTC-дат с
`gate.verdict`; `max gap` — самая длинная пауза между соседними вердиктами;
`eligible=yes` требует 100 меток, 7 дней и разрыв не больше 36 часов.
`labels due` — entry-сигналы, у которых окно разметки истекло, а ни метки, ни
`outcome.unlabelable` нет; `unknown` означает, что окно нельзя вывести из
журнала. Как читать блок и что считать провалом — в
[run-7-days.ru.md](run-7-days.ru.md).

Полезные варианты:

```bash
./vend gate report --json                    # машиночитаемый отчёт
./vend gate report --policy <64-hex-hash>    # сравнения только по одной политике
./vend gate report --include-backfill        # включить поздние метки в сравнение
./vend gate report --share                   # обезличенная markdown-сводка
./vend gate report --share --out summary.md  # то же в файл с правами 0600
./vend gate report --allow-broken-chain      # только для восстановления: отчёт при разрыве цепочки
```

`--out` и `--save-raw` не перезаписывают существующий файл без `--force` и не
пишут внутрь каталога данных; ошибка называет причину и флаг. `vending.db` и
конфиг не перезаписываются никогда.

`--share` печатает агрегаты, строки G1, метрики качества модели (когда они
есть), допущения и медиану/p95 entry lag (в долях бара) с числом сдвинутых
входов. Она также включает агрегированную разницу средней доходности ALLOW и
REJECT с её 95% интервалом. В ней нет символов, площадок, цен, размеров
позиций, state, путей, идентификаторов и отдельных доходностей. В сводке есть
строки `days` (окно по времени приёма) и `active days` (число разных UTC-дат с
хотя бы одним `gate.request`). Синтетический журнал отклоняется: код 2 и
сообщение `synthetic journal is not G1 evidence`. Реальную сводку публикуйте
вручную. `--share` нельзя совмещать с `--json`; `--out` требует `--share`.

### 6. Статус и целостность

```bash
./vend status          # снимок раннера + счётчики журнала + блок G1
./vend journal verify  # проверка hash-chain
```

`status` показывает версию, число событий, число меток, блок прогресса G1,
время последней метки, префикс policy hash и последнюю ошибку цикла.
`journal verify` пересчитывает цепочку; разрыв — ошибка с номером
последовательности. До первого `serve` `status --json` опускает пустые
`policyHash`, `lastLabelAt`, `lastCycleError`, `updatedAt` и `lastSeq`, а не
печатает `"0001-01-01T00:00:00Z"` или пустую строку; объект `g1` при этом
всё равно строится по журналу.

`vend help`, `vend help <command>`, `--help` и `-h` печатают справку и
возвращают 0; неизвестная команда или тема справки — код 2.

## Офлайн-replay — без ключа

Тот же поток гейта на записанном ответе decision-модели. Если `vend` уже
установлен, строку сборки пропустите:

```bash
go build -o vend ./core/cmd/vend
./vend init
./vend gate test \
  --request contracts/examples/gate-request.json \
  --fixture core/cmd/vend/testdata/decision-response.json
./vend journal verify
./vend gate report
```

В Windows соберите `vend.exe` и вызывайте `.\vend.exe` с теми же аргументами.

Фикстура — синтетический ответ облака, который проверяется по набору вопросов
гейта так же строго, как живой: ключи опций, диапазоны вероятностей, сумма к
единице, argmax и пересчитанная уверенность. `gate test` пишет те же события
журнала, что и живая оценка, поэтому офлайн-путь безопасно проверяет связку.
Это не доказательство G1: за фикстурой нет рыночных данных, а replay — не
новое решение.

Повтор того же запроса с тем же `signalId` — replay: вердикт печатается с
`replayed: true`, провайдер не вызывается, второй `gate.request` не пишется.
Другая `--fixture` для уже записанного `signalId` — ошибка, а не молчаливый
replay: команда завершается ненулевым кодом с сообщением
`fixture differs from journaled decision; change signalId or use another --data-dir`.

`gate report` после одного запроса печатает среди прочего:

```text
window: days=1 active=1
g1: labels 0/100, active days 1/7, max gap 0h, eligible=no
last label age: never
expected label interval: 13h
labels due: 0
labeled: 0
awaiting labeling: 1
insufficient data: 0 labeled, need 100
```

Это нормально: за фикстурой нет рыночных данных, поэтому меток нет, а
единственный вердикт — сегодняшний ответ. Разметить записанные свечи
офлайн:

```bash
./vend outcomes label --candles candles.json
```

Файл — JSON-массив свечей (`symbol`, `interval`, `openTime`, `closeTime`,
`open`, `high`, `low`, `close`, `volume`; десятичные — строками). Разметчик
фильтрует по каноническому символу и интервалу и требует `horizonBars` подряд
идущих баров. Без `--candles` используется настроенный marketdata-провайдер.
Сигналы, у которых в форвард-окне пропущен или сдвинут бар, не размечаются и
считаются в `unlabelable`.

## `signalId` и идемпотентность

`signalId` — ключ идемпотентности:

- тот же запрос с идентичным телом повторно возвращает записанный вердикт с тем
  же `decisionId`, без вызова провайдера;
- тот же `signalId` с **другим** телом — конфликт: HTTP 409
  `{"code":"conflict",...}`; для нового сигнала нужен новый `signalId`;
- одновременные дубликаты одного тела делят один вызов провайдера;
- `gate test` помечает повтор уже записанного `signalId` как `replayed: true`;
  другая `--fixture` для этого id завершает команду ненулевым кодом, а не
  проигрывает старый вердикт;
- `signalId` обязателен, пустое значение отклоняется.

Отпечаток тела — канонический JSON запроса: порядок полей и пробелы не важны,
любое изменённое значение — важно.

## Где лежит состояние

Пути по умолчанию (переопределяются `--config`, `--data-dir`,
`VENDING_CONFIG_FILE`, `VENDING_DATA_DIR`; флаг важнее env):

| ОС | Конфиг | Данные |
|---|---|---|
| Windows | `%APPDATA%\lv24-vending\config.json` | `%LOCALAPPDATA%\lv24-vending` |
| macOS | `~/Library/Application Support/lv24-vending/config.json` | `~/Library/Application Support/lv24-vending` |
| Linux | `$XDG_CONFIG_HOME/lv24-vending/config.json` | `$XDG_DATA_HOME/lv24-vending` |

В каталоге данных: `vending.db` (журнал) и `shadow-status.json`. Журнал, в
котором есть события `vend demo`, остаётся синтетическим навсегда: не
копируйте demo-журнал поверх реального.

## Если что-то не так

| Симптом | Причина и решение |
|---|---|
| `config already exists` (код 4) | `vend init --force` или другой `--config` |
| `local API token is not set` (код 5) | задать `VENDING_SECRET_CONN_LOCAL_APITOKEN` |
| `secret "conn/..." is not set` (код 5) | задать соответствующий `VENDING_SECRET_*`, проверить `vend doctor` |
| `synthetic journal is not G1 evidence` (код 2) | `--share` запущен на demo-журнале или журнале со demo-событиями; отчитывайтесь по реальному каталогу данных |
| `invalid --addr` (код 2) | host только `127.0.0.1`, порт 1–65535 |
| `signalId already used with a different request body` (409) | новый `signalId` или идентичное тело |
| `insufficient data: N labeled, need 100` | не ошибка; держать раннер запущенным |
| `--share cannot be combined with --json` / `--out requires --share` | исправить флаги (код 2) |
| `invalid --kind "x"` (код 2) | в подсказке перечислены допустимые виды событий; взять один из них |
| `output file already exists` (код ≠0) | передать `--force`, чтобы заменить файл; путь внутри каталога данных отклоняется всегда |
| `journal chain broken at seq N` | журнал повреждён; `gate report` и `journal verify` завершаются ненулевым кодом. Восстановить из бэкапа; `gate report --allow-broken-chain` — только для восстановления |

`vend doctor` проверяет конфиг, каталог данных, базу, провайдеров, наличие
секретов и текущий policy hash без сетевых вызовов; `--strict` добавляет
инварианты профиля G1 и никогда не объявляет G1 без сетевой оценки объёма.

Полная таблица первого запуска, с фактическими сообщениями и кодами выхода —
в [troubleshooting](troubleshooting.ru.md).
