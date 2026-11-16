# Задание: M0 «Клин: ядро»

Для агента-исполнителя. Дата: 2026-11-16 · Ветка: `feat/m0-wedge-core`
Основание: `docs/spec/10-roadmap.md` (M0), ADR-0008…0011.
Ревью каждого пакета делает оркестратор; критичные пакеты отмечены **[ревью]**.

## 0. Прочитать перед началом (только это)

- `AGENTS.md` — жёсткие запреты и границы слоёв.
- `docs/spec/10-roadmap.md` — раздел M0.
- `docs/spec/04-providers.md` §10 — контракт decision-модели на проводе.
- `docs/spec/06-risk-execution.md` §11 — формат события журнала.
- `docs/adr/0010-decision-model-guardrails.md`.

Остальную спеку не читать: она описывает целевую платформу, а не M0.

## 1. Жёсткие правила

1. Работать только в ветке `feat/m0-wedge-core`. Коммит — локально, один на пакет,
   Conventional Commits. **Не пушить. Не создавать PR.**
2. **Не создавать `.github/workflows` и любой облачный CI.** Проверки — только локально.
3. Не читать и не открывать каталог `../ideas/`, кроме одного файла, названного в P3.
   Там есть код под AGPL/GPL.
4. Секреты — никогда в коде, конфиге, тестах, логах. Тесты не ходят в сеть: только
   `httptest` и записанные фикстуры.
5. Деньги и цены — `domain.Decimal`, на границе — строки. `float64` допустим только
   для вероятностей и индикаторов.
6. Зависимости — только из списка §3. Нужна другая — остановиться и спросить.
7. Никаких `if provider == "..."` в `core`.
8. Сгенерированные файлы (`*.gen.go`) руками не править.
9. Не делать ничего сверх пакетов ниже: ни HTTP-сервера, ни Telegram, ни OMS, ни
   RiskGate, ни стратегий — это M1 и M2.
10. Идентификаторы и комментарии в коде — на английском; документы — на русском.

## 2. Уже решено (не менять)

```
main/
├── go.work                         # go 1.27; use ./core ./providers/* ./tools
├── Taskfile.yml
├── contracts/jsonschema/{common,decision,gate,events}/v1.json
├── contracts/examples/             # валидные примеры, используются тестами
├── core/                           # module github.com/LevapVeeskela/lv24-vending-machine/core
│   ├── cmd/vend/                   # единственный composition root
│   ├── contracts/                  # *.gen.go из JSON Schema
│   ├── domain/                     # публичный; импортирует только stdlib
│   ├── ports/                      # публичный; импортирует domain
│   ├── porttest/                   # conformance-наборы для провайдеров
│   └── internal/{gate,journal,store/sqlite,registry,config,resilience,cli}
├── providers/decision-drex/        # отдельный модуль .../providers/decision-drex
├── providers/marketdata-binance/   # отдельный модуль .../providers/marketdata-binance
└── tools/                          # модуль уже создан: пин go-jsonschema v0.24.1
```

- `domain` и `ports` — **не** в `internal/`: иначе модули провайдеров не смогут их
  импортировать.
- Модули связаны через `require … v0.0.0` + `replace => ../…` в каждом `go.mod`
  (взаимная зависимость `core` ↔ провайдеры допустима) и через `go.work`.
- Реестр провайдеров заполняется вручную в `core/cmd/vend/main.go`. Генератор
  реестра не писать.
- CLI — на stdlib `flag` с подкомандами, без cobra.
- Идентификаторы — ULID; время — UTC, RFC 3339 с наносекундами.
- Ошибки — `domain.Error{Code, Message, Retryable, Cause}` с `%w`.

## 3. Разрешённые зависимости

| Модуль | Лицензия | Где |
|---|---|---|
| `modernc.org/sqlite` | BSD-3-Clause | `core/internal/store/sqlite` |
| `github.com/oklog/ulid/v2` | Apache-2.0 | `core/domain` запрещено; только `internal` и `cmd` |
| `github.com/atombender/go-jsonschema` | MIT | только как tool в `tools/` |

Всё остальное — stdlib.

## 4. Пакеты работ

После **P3** остановиться и сдать отчёт: дальше — только после ревью.

### P1. Каркас

- `go.work`, `core/go.mod`, `Taskfile.yml` с задачами `gen`, `gen:check`, `test`,
  `lint:imports`, `check`. Каждая задача — одна команда `go …`, которую можно
  выполнить и без `task`.
- `tools/lintimports`: программа на stdlib, проверяет правила импорта:
  `core/domain` → только stdlib; `core/ports` → stdlib и `core/domain`;
  `providers/*` → stdlib, `core/domain`, `core/ports`, `core/porttest` (в тестах).
- `core/cmd/vend`: `vend version [--json]`.

Приёмка: `go build ./...` и `go run ./tools/lintimports` из корня проходят.

### P2. Контракты и кодоген **[ревью]**

- JSON Schema draft 2020-12, `additionalProperties: false`, у каждой схемы `$id`.
- `common/v1.json`: `Decimal` (строка, `^-?[0-9]+(\.[0-9]+)?$`), `Timestamp`,
  `InstrumentRef` (`{venue, symbol}`).
- `decision/v1.json`: `DecisionRequest`, `Question`, `Option`, `DecisionResponse`,
  `Answer`, `Usage`. Варианты вопроса — **массив** `options: [{key, description}]`
  (порядок значим). Типы вопроса: `choice | noul | score`.
  `Usage`: `inputTokens`, `outputTokens`, `costUsd` (Decimal, необязательно).
  `DecisionResponse.model` — фактическая модель из ответа вендора;
  `requestedModel` — что просили.
- `gate/v1.json`: `GateRequest` (`signalId`, `instrument`, `side: buy|sell`,
  `intentKind: entry|exit`, `timeframe`, `ts`, `price`, `source`, `state` — объект
  с числовыми признаками), `GateVerdict` (`verdict: ALLOW|REJECT|ABSTAIN|SKIP`,
  `code`, `reasons[]`, `checks[]`, `decisionId`, `model`, `policyHash`, `decidedAt`).
- `events/v1.json`: конверт события журнала по `06-risk-execution.md §11`:
  `seq, eventId, kind, ts, schemaVersion, payload, codeVersion, prevHash, hash`
  и необязательные `runId, decisionId, policyHash, snapshotHash`.
- `task gen`: `go tool go-jsonschema` из `tools/` → `core/contracts/*.gen.go`;
  флаги `--tags json`, без `--only-models` (нужна валидация обязательных полей и enum).
- `core/contracts/drift_test.go`: генерирует во временный каталог и сравнивает
  побайтно с закоммиченным; пропускается при `-short`.
- `core/contracts/examples_test.go`: каждый файл `contracts/examples/*.json`
  декодируется в свой тип без ошибок; по одному невалидному примеру на тип — с ошибкой.

Приёмка: `task gen:check` (или эквивалентная команда) не даёт diff; тесты зелёные.

### P3. Адаптер `decision-drex` **[ревью]**

- `core/domain`: `Decimal`, типы decision (`DecisionRequest`, `Question`, `Option`,
  `Answer`, `DecisionResponse`, `Usage`), `Error` и коды `auth`, `payment_required`,
  `rate_limit`, `invalid_request`, `invalid_output`, `unavailable`, `timeout`,
  `unknown`.
  `Decimal` — на `math/big`: `Parse`, `String`, `Add`, `Sub`, `Mul`, `Cmp`, `Sign`,
  `QuoRound(d, scale)`; без `float`.
- `core/ports`: `Descriptor`, `Kind`, `Decision` (`Descriptor()`, `Decide(ctx, req)`),
  `Deps` (`HTTPClient`, `Clock`, `Logger`, `Secrets`), `SecretStore`.
- `providers/decision-drex`:
  - профили `drex` (`https://console.nace.ai`, путь `/v1/systemone`) и `jev`
    (`https://api.typesafe.ai/v1`, путь `/systemone`); базовый адрес переопределяется
    конфигом;
  - JSON запроса собирается вручную с сохранением порядка: на проводе варианты идут
    в `criteria` — объект `key → description` для `choice`, массив для `score`;
    поля `options` на проводе нет;
  - `Authorization: Bearer <secret>`; секрет — из `Deps.Secrets`, в логи не попадает;
  - HTTP-клиент без редиректов, таймаут из запроса, лимит тела ответа 1 МиБ;
  - разбор ответа: `noul` — число 0…1; `choice` — `choice`, `probabilities`,
    `confidence`; `score` — `score`, `legend`, `probabilities`, `confidence`;
  - **валидация `choice`**: ключи `probabilities` ровно равны вариантам; значения
    конечны и в [0, 1]; `|Σp − 1| ≤ 0.001`; `choice` — argmax; пересчитанная
    `C = (p_max − 1/n) / (1 − 1/n)` отличается от присланной не более чем на 0.02.
    Любое нарушение → `invalid_output`. Для `score` `confidence` не пересчитывать;
  - `response.model` → `DecisionResponse.Model`; пусто → `invalid_output`;
  - ошибки: 401/403 → `auth`; 402 → `payment_required`; 422/400/413 →
    `invalid_request`; 429 → `rate_limit` (учесть `Retry-After`); 500/503/529 →
    `unavailable`; отмена контекста → `timeout`. Ретраев внутри адаптера нет;
  - стоимость: `costUsd = inputTokens × pricePerBillion / 1e9`, цена — Decimal из
    конфига (пример: Drex Max v1.1 — `"60"`, Jev — `"42"` за 1B входных токенов).
- `core/porttest.DecisionConformance(t, newProvider)`: несколько вопросов трёх
  типов; сохранение порядка вариантов в теле запроса; каждое нарушение валидации;
  каждый код ошибки; таймаут; расчёт стоимости; секрет не появляется в ошибках.
- Фикстуры: тело запроса и ответа взять из **одного** файла
  `../ideas/decision-model-trading/drex-decision-models/docs/request-format.md`
  (Apache-2.0), положить в `providers/decision-drex/testdata/` с файлом `SOURCE.md`
  (откуда, лицензия, коммит `b932571`). Добавить вариант ответа облачного API с
  полями `model`, `request_id`, `evaluation_time_ms`.

Приёмка: `go test ./...` в `core` и `providers/decision-drex` зелёные; тест
проверяет, что на фикстуре вендора пересчитанная `confidence` для `choice` равна
0.9889 ± 0.0001.

**Стоп. Отчёт. Ждать ревью.**

### P4. Политика гейта **[ревью]**

`core/internal/gate`: чистая функция `Evaluate(req, resp, policy) GateVerdict`, без I/O.

- Набор вопросов `gate-v1` (все `choice`): `data_quality` (`ok`, `insufficient`),
  `signal_alignment` (`aligned`, `neutral`, `conflict`), `market_regime`
  (`favorable`, `neutral`, `adverse`), `risk_check` (`pass`, `block`).
  Ключи вопросов неизменяемы; изменения — только добавлением.
- Порядок проверок фиксирован и входит в `policyHash`:
  1. `intentKind = exit` → `SKIP`, `exit_not_gated` (провайдер не вызывается);
  2. ошибка провайдера или `invalid_output` → `ABSTAIN`, `provider_unavailable` /
     `invalid_output`;
  3. `p(insufficient) ≥ minBlock` → `ABSTAIN`, `insufficient_data`;
  4. `p(block) ≥ minBlock` → `REJECT`, `risk_block`;
  5. `conflict` и `adverse` выбраны, оба `p_max ≥ minBlock` → `REJECT`,
     `directional_block`;
  6. `p(pass) < minPass` → `ABSTAIN`, `low_confidence`;
  7. иначе `ALLOW`.
- Пороги по умолчанию: `minPass = 0.55`, `minBlock = 0.50`; задаются политикой.
- Вердикт содержит все выполненные проверки по порядку.

Приёмка: table-driven тесты на каждый код; одинаковый вход → одинаковый вердикт.

### P5. Журнал **[ревью]**

- `core/internal/journal`: `hash = SHA-256(prevHash + "\n" + canonicalJSON(event без hash))`;
  канонический JSON — ключи отсортированы, без пробелов; `prevHash` первого
  события — 64 нуля.
- `core/internal/store/sqlite`: миграции встроены (`embed`), WAL; `Append` в одной
  транзакции `BEGIN IMMEDIATE`, `seq = max + 1`; `Verify` проходит всю цепочку и
  возвращает первый разрыв.
- Порт `ports.Journal`: `Append`, `Query(from, kind)`, `Verify`.

Приёмка: тест на 1 000 событий; изменение любого поля любого события ломает
`Verify` с указанием `seq`; конкурентный `Append` из 8 горутин не даёт дублей `seq`.

### P6. Публичные свечи

`providers/marketdata-binance`: `GET /api/v3/klines` без ключей; порт
`ports.CandleProvider`; пагинация, порядок по времени, дедуп, цены — `Decimal`.
Фикстуры собрать вручную по публичной документации Binance, в сеть не ходить.

Приёмка: `porttest.CandleConformance` — порядок, дедуп, пагинация, 429.

### P7. CLI

`vend init` (конфиг и БД), `vend doctor [--json]`, `vend config path|get|validate`,
`vend gate test --request FILE [--fixture FILE]`, `vend journal verify`.
Коды возврата и формат ошибок — по `docs/spec/03-core-cli.md §3`.
`SecretStore` в M0 — только переменные окружения `VENDING_SECRET_<REF>`; keychain — M1.

Приёмка: `vend init && vend gate test --request … --fixture … && vend journal verify`
отрабатывает на чистой машине без сети и без ключей.

## 5. Проверка перед каждым коммитом

```bash
go build ./... && go vet ./... && go test ./... && go run ./tools/lintimports
```

Выполнять из корня (workspace). Если `./...` из корня не находит пакеты — выполнять
по модулям: `go -C core …`, `go -C providers/decision-drex …`, `go -C tools …`.
Если команда падает — не коммитить, чинить.

## 6. Формат отчёта

- Пакет, список созданных файлов, вывод команды из §5 (последние строки).
- Что не сделано и почему.
- Вопросы, на которых остановился.
- Отклонения от этого задания — отдельным списком. Молча отклоняться нельзя.
