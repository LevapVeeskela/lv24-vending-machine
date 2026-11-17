# Быстрый старт

[English version](quickstart.md)

Два пути до рабочего shadow-гейта:

- **Путь A (15 минут, с ключом decision-провайдера):** loopback-гейт отвечает на
  реальные запросы и в фоне размечает исходы.
- **Путь B (офлайн, без ключа):** записанный ответ decision-модели проигрывается
  через тот же поток, журнал проверяется. Без сети и без секретов.

Всё выполняется из корня репозитория. Платных сервисов и обращений к CI нет.

Для длительного прогона к G1 (≥ 7 дней, ≥ 100 меток) — отдельный документ
[run-7-days.ru.md](run-7-days.ru.md).

## 0. Установка

Сначала получите `vend`: готовый бинарник со страницы релизов или сборку из
исходников — см. [install.ru.md](install.ru.md). В текущем состоянии репозитория
релиз **не опубликован**, поэтому рабочий путь — сборка из исходников. Команды
на bash ниже используют `./vend` из корня репозитория (или `vend`, если он в
`PATH`); в Windows соберите `vend.exe` и запускайте `.\vend.exe`, как показано
на шаге 1.

## Путь A — 15 минут с ключом

### 1. Сборка (из исходников) и инициализация

```bash
go build -o vend ./core/cmd/vend
./vend init
./vend version
```

Windows PowerShell — те же команды с `vend.exe`:

```powershell
go build -o vend.exe ./core/cmd/vend
.\vend.exe init
.\vend.exe version
```

Если бинарник установлен на шаге 0, строку `go build` пропустите. `init`
создаёт JSON-конфиг и SQLite-журнал, печатает оба пути и следующие команды
(`config validate`, `doctor`, `gate test`, `serve`) с подставленными
`--config`/`--data-dir`. В Windows используйте форму `vend.exe` выше; запуск
сборки без расширения из PowerShell не поддерживается.

### 2. Настроить провайдера в конфиге

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

### 3. Положить секреты в окружение

Ключ не попадает в конфиг, журнал и логи. Каждый `secretRef` превращается в
`VENDING_SECRET_<REF>`: верхний регистр, каждый символ вне `[A-Za-z0-9]`
заменяется на `_`. Для `conn/drex/apiKey` это
`VENDING_SECRET_CONN_DREX_APIKEY`.

Bash:

```bash
export VENDING_SECRET_CONN_DREX_APIKEY='<token>'
export VENDING_SECRET_CONN_LOCAL_APITOKEN='<token>'
```

PowerShell:

```powershell
$env:VENDING_SECRET_CONN_DREX_APIKEY = '<token>'
$env:VENDING_SECRET_CONN_LOCAL_APITOKEN = '<token>'
```

`VENDING_SECRET_CONN_LOCAL_APITOKEN` — bearer-токен локального API; без него
сервер не стартует. Значения должны быть разными случайными строками.

### 4. Запустить shadow-цикл

```bash
./vend serve --addr 127.0.0.1:8420
```

Сервер слушает только loopback. Вместе с ним работает фоновый shadow-раннер:
каждые `outcome.labelIntervalSec` (по умолчанию 300) он размечает исходы,
отправляет уведомления и пишет `shadow-status.json` в каталог данных; при
включённом `signals.trend` скан trend идёт через 2 секунды после закрытия
каждого бара trend. Процесс должен оставаться запущенным.

Перед неделей проверьте объём сигналов вашего конфига: `vend signals estimate
--days 7` (см. [run-7-days.ru.md](run-7-days.ru.md)).

### 5. Отправить запрос в гейт

```bash
curl -sS -X POST http://127.0.0.1:8420/v1/gate \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  --data @contracts/examples/gate-request.json
```

В PowerShell 5.1 `curl` — алиас `Invoke-WebRequest`; вызывайте `curl.exe`.

`contracts/examples/gate-request.json` — шаблон. Замените `signalId` на
уникальный, а `ts` — на текущее UTC-время: со старым `ts` сигнал попадёт в
журнал как `backfill`, а повтор с тем же `signalId` вернёт записанный вердикт.
Вместо `<token>` подставьте значение `VENDING_SECRET_CONN_LOCAL_APITOKEN`
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

### 6. Прочитать вердикт

- `ALLOW` — все проверки прошли в порогах политики.
- `REJECT` — гейт заблокировал намерение (CLI отдаёт код 7, HTTP остаётся 200).
- `ABSTAIN` — провайдер ошибся или не определился; вердикт всё равно пишется в
  журнал и возвращается с HTTP 200.
- `SKIP` — оценка обойдена.

На каждый запрос в журнале появляются `gate.request` → `decision.response`
(если провайдер ответил) → опционально `decision.model_changed` →
`gate.verdict`, все с одним `decisionId`.

### 7. Разметка исходов идёт сама

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

С значениями по умолчанию (`horizonBars: 12`, таймфрейм `1h`) первая метка
появится только через несколько часов. Для быстрой проверки используйте
[runbook](runbook-live-smoke.ru.md): там `horizonBars: 3` и ручной запрос с
`timeframe: "1m"`.

Сигналы, пришедшие позже чем через один таймфрейм
(`receivedAt − ts > timeframe`), помечаются `backfill`: они могут входить в бар,
уже закрывшийся к моменту приёма, и не входят в сравнения отчёта без
`--include-backfill`.

### 8. Отчёт после накопления меток

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

`--share` печатает агрегаты, метрики качества модели (когда они есть),
допущения и медиану/p95 entry lag (в долях бара) с числом сдвинутых входов.
Она также включает агрегированную разницу средней доходности ALLOW и REJECT с
её 95% интервалом. В ней нет символов, площадок, цен, размеров позиций, state,
путей, идентификаторов и отдельных доходностей. В сводке есть строки `days`
(окно по времени приёма) и `active days` (число разных UTC-дат с хотя бы одним
`gate.request`). Публикуется вручную. `--share` нельзя совмещать с `--json`;
`--out` требует `--share`.

### 9. Статус и целостность

```bash
./vend status          # снимок раннера + счётчики журнала
./vend journal verify  # проверка hash-chain
```

`status` показывает версию, число событий, число меток, время последней метки,
префикс policy hash и последнюю ошибку цикла. `journal verify` пересчитывает
цепочку; разрыв — ошибка с номером последовательности. До первого `serve`
`status --json` опускает пустые `policyHash`, `lastLabelAt`, `lastCycleError`,
`updatedAt` и `lastSeq`, а не печатает `"0001-01-01T00:00:00Z"` или пустую
строку.

`vend help`, `vend help <command>`, `--help` и `-h` печатают справку и
возвращают 0; неизвестная команда или тема справки — код 2.

## Путь B — офлайн, без ключа

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

Повтор того же запроса с тем же `signalId` — replay: вердикт печатается с
`replayed: true`, провайдер не вызывается, второй `gate.request` не пишется.
Другая `--fixture` для уже записанного `signalId` — ошибка, а не молчаливый
replay: команда завершается ненулевым кодом с сообщением
`fixture differs from journaled decision; change signalId or use another --data-dir`.

`gate report` после одного запроса печатает среди прочего:

```text
window: days=1 active=1
labeled: 0
awaiting labeling: 1
insufficient data: 0 labeled, need 100
```

Это нормально: за фикстурой нет рыночных данных. Разметить записанные свечи
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

В каталоге данных: `vending.db` (журнал) и `shadow-status.json`.

## Если что-то не так

| Симптом | Причина и решение |
|---|---|
| `config already exists` (код 4) | `vend init --force` или другой `--config` |
| `local API token is not set` (код 5) | задать `VENDING_SECRET_CONN_LOCAL_APITOKEN` |
| `secret "conn/..." is not set` (код 5) | задать соответствующий `VENDING_SECRET_*`, проверить `vend doctor` |
| `invalid --addr` (код 2) | host только `127.0.0.1`, порт 1–65535 |
| `signalId already used with a different request body` (409) | новый `signalId` или идентичное тело |
| `insufficient data: N labeled, need 100` | не ошибка; держать раннер запущенным |
| `--share cannot be combined with --json` / `--out requires --share` | исправить флаги (код 2) |
| `invalid --kind "x"` (код 2) | в подсказке перечислены допустимые виды событий; взять один из них |
| `output file already exists` (код ≠0) | передать `--force`, чтобы заменить файл; путь внутри каталога данных отклоняется всегда |
| `journal chain broken at seq N` | журнал повреждён; `gate report` и `journal verify` завершаются ненулевым кодом. Восстановить из бэкапа; `gate report --allow-broken-chain` — только для восстановления |

`vend doctor` проверяет конфиг, каталог данных, базу, провайдеров, наличие
секретов и текущий policy hash без сетевых вызовов.

Полная таблица первого запуска, с фактическими сообщениями и кодами выхода —
в [troubleshooting](troubleshooting.ru.md).
