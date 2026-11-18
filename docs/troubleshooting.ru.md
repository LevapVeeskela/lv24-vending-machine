# Типовые ошибки первого запуска

[English version](troubleshooting.md)

Каждое сообщение ниже воспроизведено локально бинарником `vend`, собранным из
этого репозитория, а для HTTP-случаев — заглушкой `tools/smokestub` на
`127.0.0.1`. Настоящие Drex, Binance и Telegram не вызывались. В прогонах
использовался учебный фиктивный токен; секретов и токенов в выводе нет. Пути,
порты и идентификаторы показаны заглушками (`<path>`, `<port>`, `<data-dir>`).

Коды выхода — фактические для этих прогонов: `0` успех, `1` общая ошибка,
`2` usage/валидация, `3` не найдено, `4` конфликт состояния, `5` запрещено,
`6` ошибка провайдера.

## Таблица: что видите → причина → что сделать

| Что видите | Причина | Что сделать |
|---|---|---|
| `starting on http://127.0.0.1:<port>`, затем `vend: listen tcp 127.0.0.1:<port>: bind: Only one usage of each socket address (protocol/network address/port) is normally permitted.` / `hint: check the address is free and retry` (код 1) | Порт уже занят другим процессом. Строка запуска печатается до bind; сервер не поднялся. | Остановить процесс, занявший порт, или передать другой `--addr 127.0.0.1:<свободный-порт>`. Владельца порта искать через `Get-NetTCPConnection -LocalPort <port>` в Windows или `ss -ltnp` в Linux. Не убивать процессы по шаблону имени. |
| `vend: local API token is not set` / `hint: set VENDING_SECRET_CONN_LOCAL_APITOKEN and retry` (код 5) | У `vend serve` нет bearer-токена локального API. | Задать `VENDING_SECRET_CONN_LOCAL_APITOKEN` в окружении процесса `vend serve` и запустить снова. В конфиг токен не кладётся. |
| `vend: cannot load config at <path>: file not found: <path>` / `hint: run vend init to create the config` (код 3, `vend serve` и большинство команд) | Пути из `--config` не существует. | Выполнить `vend init` или передать путь, который печатает `vend config path`. |
| `vend: config: read <path>: open <path>: The system cannot find the file specified.` / `hint: run vend init to create the config` (код 2, `vend config validate`) | Тот же отсутствующий файл, но `vend config validate` сообщает о нём как об ошибке валидации. | Выполнить `vend init` или передать существующий `--config`. |
| `vend: invalid_request: config: invalid JSON in <path>` / `hint: fix the JSON syntax or run vend init --force to recreate it` (код 2) | Файл конфига — не валидный JSON (например, обрезан). | Исправить JSON или пересоздать файл через `vend init --force`. Проверить через `vend config validate`. |
| `vend: registry: unknown decision provider "decision-nope"` / `hint: align providers.decision.id with a registered provider` (код 6) | `providers.decision.id` не совпадает ни с одним провайдером, зарегистрированным в этой сборке. | Указать `decision-drex` в `providers.decision.id`: других decision-провайдеров в M1 нет. Затем `vend config validate`. |
| `vend: invalid_request: config: live mode is not available in this release; use shadow, paper or sandbox` / `hint: fix the config value or run vend init --force to recreate it` (код 2) | В `mode` стоит `live`. | Поставить `mode: shadow` (режим M1) или другое допустимое значение; пути live-исполнения нет. |
| HTTP 401, `{"code":"unauthorized","message":"missing or invalid bearer token","hint":"send Authorization: Bearer \u003ctoken\u003e","retryable":false}` | `POST /v1/gate` отправлен без токена, с неверным токеном или без bearer-заголовка. | Передать `Authorization: Bearer <token>` со значением `VENDING_SECRET_CONN_LOCAL_APITOKEN`. Токен сравнивается в постоянном времени; другого способа авторизации нет. |
| HTTP 400, `{"code":"bad_request","message":"invalid request body","hint":"send exactly one JSON gate request object","retryable":false}` | Тело запроса — не один строгий JSON-объект (битый JSON, хвост после объекта, неизвестные поля). | Отправить ровно один JSON-объект; неизвестные поля отклоняются. Форма — `contracts/examples/gate-request.json`. |
| HTTP 400, `{"code":"bad_request","message":"invalid gate request: state must be an object","hint":"fix the request fields and retry","retryable":false}` | Обязательное поле отсутствует или имеет неверный тип; в этом примере не было `state`. Сообщение называет поле, не прошедшее валидацию. | Добавить названное поле. Обязательные: `signalId`, `instrument{venue,symbol}`, `side`, `intentKind`, `timeframe`, `ts`, `price`, `source`, `state`. |
| HTTP 400, `{"code":"bad_request","message":"invalid gate request: unsupported timeframe \"4H\" (want a positive amount followed by a lowercase m, h, d or w, e.g. \"4h\")","hint":"fix the request fields and retry","retryable":false}` | `timeframe` — не положительное число с единицей `m`, `h`, `d` или `w` в нижнем регистре (например `4H`, `1M`, `60`). Такой сигнал нельзя было бы разметить, поэтому гейт отклоняет его и ничего не пишет в журнал. | Отправьте поддерживаемый таймфрейм: `5m`, `1h`, `4h`, `1d` или `1w`. Сигнал с неподдерживаемым таймфреймом, записанный более старой сборкой, один раз помечается `outcome.unlabelable` с причиной `unsupported_timeframe`. |
| HTTP 400, `{"code":"bad_request","message":"invalid gate request: futures-style symbol \"BTC/USDT:USDT\" is not supported in M1 (spot only)","hint":"fix the request fields and retry","retryable":false}` | Символ содержит фьючерсный суффикс. M1 читает только spot-свечи, поэтому такой сигнал можно было бы оценить, но нельзя разметить; гейт отклоняет его до платного вызова провайдера. | Отправьте spot-символ (`BTC/USDT`, `BTC-USDT` или `BTCUSDT`). Фьючерсы в M1 не поддерживаются. |
| HTTP 200 с `"verdict":"ABSTAIN"`, `"code":"provider_auth"`, `"reasons":["provider auth error: provider rejected credentials or the quota is exhausted; check the provider secret (VENDING_SECRET_*) and quota"]` | В процессе `vend serve` нет секрета decision-провайдера, либо провайдер его отклонил, либо исчерпана квота. Гейт всё равно отвечает и пишет вердикт в журнал. | Задать `VENDING_SECRET_CONN_DREX_APIKEY` (имя выводится из `providers.decision.secretRef`) в окружении `serve`; проверить квоту у провайдера. `vend doctor --live` показывает ту же проверку без сигнала. |
| `vend: database not found at <data-dir>/vending.db` / `hint: run vend init to create it` (код 3, `vend gate report`, `vend status`, `vend journal ...`) | Журнала ещё нет или `--data-dir`/`VENDING_DATA_DIR` указывает на другой каталог. | Выполнить `vend init` или передать каталог данных, в котором лежит `vending.db`. |
| `output file already exists: <path> (pass --force to overwrite)` (код 2) | `--out` или `--save-raw` заменил бы существующий файл. | Передать `--force` или выбрать новый путь. Путь внутри каталога данных отклоняется всегда: журнал и status-файл защищены. |
| `invalid --kind "<kind>"` (код 2) | `journal tail --kind` получил несуществующий вид события. | Взять вид из подсказки (`details.allowed`), например `gate.request` или `gate.verdict`. |
| `journal chain broken at seq N` (код 1, `gate report` и `journal verify`) | Hash-chain журнала не сходится. | Восстановить журнал из бэкапа. `gate report --allow-broken-chain` печатает отчёт вопреки разрыву — только для восстановления. |
| `decision: unavailable (check the provider URL and network, then retry)` и `candles: unavailable (check the provider URL and network, then retry)` (код 1, `vend doctor --live`) | Эндпоинт провайдера недоступен: неверный `baseUrl`, заглушка выключена, нет сети. | Запустить провайдера/заглушку, проверить `providers.decision.baseUrl` и `providers.marketdata.baseUrl`, повторить. Полная таблица кодов — в [runbook](runbook-live-smoke.ru.md#если-что-то-не-так). |
| `decision: auth (check the provider secret and quota)` (код 1, `vend doctor --live`) | Секрет decision-провайдера отсутствует или отклонён. | Задать `VENDING_SECRET_CONN_DREX_APIKEY` в окружении `doctor` и проверить квоту. |
| `vend: cannot use the journal database at <data-dir>/vending.db: unable to open database file (14)` / `hint: check the data directory is writable and retry` (код 1) | Каталог данных существует, но процесс не может в него писать. | Выбрать доступный для записи `--data-dir`/`VENDING_DATA_DIR` или исправить права каталога. В Windows 11 это воспроизведено запретом записи на временный каталог через `icacls`; запрет затем снят. |
| `FAIL trend: signals.trend.enabled is false; run vend init --quick or set it to true`, `FAIL symbols: ...`, `FAIL timeframe: ...`, `FAIL label_interval: ...`, `FAIL decision_secret: ...`, `FAIL secrets: ...` (код 1, `vend doctor --strict`) | Установка не настроена под профиль G1. | Исправить названное поле; см. «Профиль быстрого старта, G1 и demo» ниже. |
| `SYNTHETIC — not G1 evidence` и `g1: labels 0/100, active days 0/7` | В журнале есть события `vend demo`. | Использовать реальный каталог данных; см. «Синтетический журнал» ниже. |
| `last label age` больше `expected label interval`, `labels due` больше нуля | Гейт отвечает, а разметчик стоит. | См. «Метки встали» ниже. |

## Профиль быстрого старта, G1 и demo

### `doctor --strict` падает

`--strict` требует профиль G1; первая непрошедшая проверка печатается как
`FAIL <check>:` и команда завершается кодом 1. Поля и исправления:

| Сообщение | Проверка | Что сделать |
|---|---|---|
| `FAIL trend: signals.trend.enabled is false; run vend init --quick or set it to true` | Конфиг создан обычным `init` или trend выключен. | Для новой установки — `vend init --quick --config <path> --data-dir <dir>`, либо поставить `signals.trend.enabled: true` в существующем конфиге. |
| `FAIL symbols: signals.trend.symbols has N symbols; the G1 profile needs at least 10` | Символов слишком мало, гейт молчит или почти молчит. | Добавить символы в `signals.trend.symbols`; в быстром профиле их десять, пары USDT. |
| `FAIL timeframe: signals.trend.timeframe is "1h"; the G1 profile needs "15m"` | Профиль требует ровно `15m`; при неразбираемом значении добавляется ошибка разбора. | Поставить `signals.trend.timeframe: "15m"` (или выполнить `init --quick`). |
| `FAIL label_interval: outcome.labelIntervalSec 600s must be at most half of signals.trend.timeframe 15m` | Окно разметки длиннее половины бара. | Уменьшить `outcome.labelIntervalSec` (в быстром профиле 300) или взять таймфрейм длиннее. |
| `FAIL decision_secret: VENDING_SECRET_CONN_DREX_APIKEY is not set; the decision provider needs it` | Переменная, выведенная из `providers.decision.secretRef`, пуста в этой оболочке. | Задать переменную; имя в сообщении — точное. |
| `FAIL decision_secret: providers.decision.secretRef is empty` | В конфиге нет `secretRef`. | Поставить `providers.decision.secretRef: "conn/drex/apiKey"` (ссылка, не ключ). |
| `FAIL secrets: VENDING_SECRET_CONN_LOCAL_APITOKEN is not set; vend serve requires it` | Нет токена loopback-API. | Задать `VENDING_SECRET_CONN_LOCAL_APITOKEN` в той же оболочке, где будет запущен `serve`. |
| `FAIL config: ...`, `FAIL data_dir: ...`, `FAIL database: ...`, `FAIL journal: ...`, `FAIL providers: ...`, `FAIL gate_policy: ...` | Не прошла обычная офлайн-проверка. | Взять соответствующую строку основной таблицы выше. |

Без `--strict` отсутствие секрета decision-провайдера — только предупреждение,
и `doctor` возвращает 0; токен локального API — жёсткая ошибка всегда.

Успешный прогон заканчивается так:

```text
profile ready; volume unverified
next: vend signals estimate --days 7
```

Это не вердикт G1. Профиль доказывает только, что установка настроена
производить сигналы; объём сигналов измеряет сетевой estimate. В `--json` тот
же факт выглядит как `"strict": true, "volumeVerified": false` и
`"nextSteps": ["vend signals estimate --days 7"]`.

### Ноль сигналов за неделю

`vend init` без `--quick` пишет `signals.trend.enabled: false` и пустой
`signals.trend.symbols`. В таком состоянии гейт отвечает только на запросы
`POST /v1/gate`; если бот их не шлёт, журнал остаётся пустым, а G1 — `0/100,
0/7` навсегда.

Исправление без перезаписи настроенного конфига:

```bash
vend init --quick --config <new-config> --data-dir <new-data-dir>
vend doctor --strict --config <new-config> --data-dir <new-data-dir>
vend signals estimate --days 7 --config <new-config> --data-dir <new-data-dir>
vend serve --config <new-config> --data-dir <new-data-dir>
```

`--force` у `init --quick` заменяет существующий конфиг и теряет правки;
лучше новый путь `--config` или ручная правка `signals.trend`. После правки
конфига `serve` нужно перезапустить: конфиг читается при старте.

Успешный `doctor --strict` не значит, что за неделю наберётся 100 меток.
Проверьте объём до старта:

```bash
vend signals estimate --days 7
```

`verdict: low` — это примерно меньше 15 сигналов в сутки; добавьте символы или
уменьшите таймфрейм (см. [run-7-days.ru.md](run-7-days.ru.md), «Ориентир по
объёму»). Тихий рынок тоже может не дать пересечения EMA за сутки: оценка —
это история, а не обещание.

### Метки встали

Симптом в `vend status`:

```text
g1: labels 41/100, active days 5/7, max gap 2h, eligible=no
last label age: 27h
expected label interval: 3.25h
labels due: 12
```

Гейт продолжает отвечать, но метки старше ожидаемого интервала, а entry-сигналы
прошли окно разметки. Проверяйте по порядку:

1. `vend status --json` — `lastCycleError` должен быть пустым, `updatedAt` —
   свежим. Старый `updatedAt` значит, что `serve` не работает.
2. `vend journal tail --kind outcome.unlabelable --limit 20` — причина
   (`timeout`, `source_rejected`, `unsupported_timeframe`) называет сбойный шаг.
3. `vend doctor --live` — проба `candles` проверяет настроенный эндпоинт
   свечей. `unavailable` — неверный `baseUrl`, нет сети или заглушка выключена;
   `rate_limit` — подождать.
4. После исправления перезапустить `serve`; ожидающие исходы повторяются до
   семисуточного срока, после которого становятся `outcome.unlabelable`.

`labels due: unknown` — это не ноль: журнал и команда не могут вывести окно
разметки. Передайте тот же `--config`, что и у раннера (для окна нужны
`horizonBars` и таймфрейм сигнала).

### Синтетический журнал

Если `report`, `status` или `--share` пишет `SYNTHETIC — not G1 evidence` и
`g1: labels 0/100, active days 0/7`, в журнале есть события `vend demo`
(`runId=demo`). Счётчики обнулены намеренно, `eligible` равен false, а
`gate report --share` завершается кодом 2 с сообщением
`synthetic journal is not G1 evidence`. `gate report --json` всё равно печатает
числа с `"synthetic": true`, но доказательством они не являются.

Флага, снимающего метку, нет, и поддержанного способа вырезать её тоже: журнал
защищён hash-chain. Начинайте реальный прогон в другом каталоге данных. Не
направляйте `serve` в каталог demo и не передавайте реальный журнал в
`vend demo --data-dir`.

## Сбор диагностики

Команды из корня репозитория; секретов они не печатают:

```bash
vend version
vend config validate
vend doctor
vend journal verify
vend status --json
```

Для неудачного HTTP-вызова сохранить код ответа и JSON-тело: в нём есть
`code`, `message`, `hint` и `retryable`. Не присылать конфиг, переменные
окружения, `vending.db`, файл `--save-raw` и скриншоты с токеном. Сводка
`--share` обезличена и предназначена для публикации.

## Пример Freqtrade

`examples/freqtrade/vend_shadow_gate.py` никогда не блокирует сделку:
`confirm_trade_entry` возвращает `True`, даже если гейт выключен, занят,
отвечает 401/500 или не уложился в таймаут. Если в `vend journal tail` ничего
нет, проверить `VEND_GATE_TOKEN` в процессе Freqtrade, `VEND_GATE_URL`
(по умолчанию `http://127.0.0.1:8420`) и что `vend serve` запущен. Пример
проверен на заглушках интерфейсов, а не на настоящей установке Freqtrade; см.
[рецепт](recipes/freqtrade.ru.md).
