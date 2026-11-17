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
| HTTP 200 с `"verdict":"ABSTAIN"`, `"code":"provider_auth"`, `"reasons":["provider auth error: provider rejected credentials or the quota is exhausted; check the provider secret (VENDING_SECRET_*) and quota"]` | В процессе `vend serve` нет секрета decision-провайдера, либо провайдер его отклонил, либо исчерпана квота. Гейт всё равно отвечает и пишет вердикт в журнал. | Задать `VENDING_SECRET_CONN_DREX_APIKEY` (имя выводится из `providers.decision.secretRef`) в окружении `serve`; проверить квоту у провайдера. `vend doctor --live` показывает ту же проверку без сигнала. |
| `vend: database not found at <data-dir>/vending.db` / `hint: run vend init to create it` (код 3, `vend gate report`, `vend status`, `vend journal ...`) | Журнала ещё нет или `--data-dir`/`VENDING_DATA_DIR` указывает на другой каталог. | Выполнить `vend init` или передать каталог данных, в котором лежит `vending.db`. |
| `output file already exists: <path> (pass --force to overwrite)` (код ≠ 0) | `--out` или `--save-raw` заменил бы существующий файл. | Передать `--force` или выбрать новый путь. Путь внутри каталога данных отклоняется всегда: журнал и status-файл защищены. |
| `invalid --kind "<kind>"` (код 2) | `journal tail --kind` получил несуществующий вид события. | Взять вид из подсказки (`details.allowed`), например `gate.request` или `gate.verdict`. |
| `journal chain broken at seq N` (код ≠ 0, `gate report` и `journal verify`) | Hash-chain журнала не сходится. | Восстановить журнал из бэкапа. `gate report --allow-broken-chain` печатает отчёт вопреки разрыву — только для восстановления. |
| `decision: unavailable (check the provider URL and network, then retry)` и `candles: unavailable (check the provider URL and network, then retry)` (код 1, `vend doctor --live`) | Эндпоинт провайдера недоступен: неверный `baseUrl`, заглушка выключена, нет сети. | Запустить провайдера/заглушку, проверить `providers.decision.baseUrl` и `providers.marketdata.baseUrl`, повторить. Полная таблица кодов — в [runbook](runbook-live-smoke.ru.md#если-что-то-не-так). |
| `decision: auth (check the provider secret and quota)` (код 1, `vend doctor --live`) | Секрет decision-провайдера отсутствует или отклонён. | Задать `VENDING_SECRET_CONN_DREX_APIKEY` в окружении `doctor` и проверить квоту. |
| `vend: cannot use the journal database at <data-dir>/vending.db: unable to open database file (14)` / `hint: check the data directory is writable and retry` (код 1) | Каталог данных существует, но процесс не может в него писать. | Выбрать доступный для записи `--data-dir`/`VENDING_DATA_DIR` или исправить права каталога. В Windows 11 это воспроизведено запретом записи на временный каталог через `icacls`; запрет затем снят. |

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
