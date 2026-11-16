# 02. Монорепа, тулинг, CI

Статус: draft v0.1 · Зависит от: `01-architecture.md`

## 1. Решение по языку ядра

**Go 1.27.** Обоснование (детали и альтернативы — `docs/adr/0001-go-core.md`):

- консистентность с `lv24-brain` (тот же стек, те же паттерны, переиспользуем
  опыт и код: `ports/adapters`, registry, CLI+server);
- скорость итераций и низкий порог входа для провайдеров и агентов;
- отличная конкурентность для WS-подписок и event loop без async-колорита;
- один статический бинарник, тривиальная кросс-компиляция под Win/macOS/Linux;
- `oapi-codegen` + drift-тесты уже отработаны в `lv24/main/ai-gateway`;
- тяжёлые вычисления (оптимизация параметров, ML-инференс) выносятся в отдельные
  модули/процессы с контрактом, а не диктуют язык всему ядру.

Rust остаётся опцией для: WASM-рантайма стратегий (если wazero не хватит),
крипто-подписи/бинарных протоколов, высоконагруженных бэктест-кернелов.
Контракт позволяет заменить модуль, не переписывая ядро.

## 2. Дерево репозитория

```
main/
├── .github/workflows/          # ci, release, nightly, docs
├── .editorconfig .gitignore .gitattributes
├── AGENTS.md CLAUDE.md         # правила для агентов
├── README.md LICENSE SECURITY.md CONTRIBUTING.md
├── Taskfile.yml                # cross-cutting задачи (gen, check, build, release)
├── package.json                # npm workspaces (private root)
├── go.work                     # core + providers + tools
├── .golangci.yml .oxlintrc.json .oxfmtrc.json
│
├── contracts/                  # ИСТОЧНИК ПРАВДЫ для всех границ
│   ├── openapi/core-v1.yaml
│   ├── jsonschema/{common,events,agent,provider,proposal,risk}/v1*.json
│   └── examples/               # валидные примеры для тестов и доков
│
├── core/                       # Go module: .../core
│   ├── cmd/vend/               # CLI
│   ├── cmd/vendd/              # демон/сервер
│   ├── api/gen/                # сгенерированные server-типы (не править)
│   ├── contracts/              # сгенерированные типы границ
│   ├── internal/
│   │   ├── domain/             # модели, инварианты, ошибки, деньги
│   │   ├── ports/              # интерфейсы + дескрипторы + capability
│   │   ├── app/                # use-cases (без I/O)
│   │   ├── runtime/            # bus, scheduler, markethub, supervisor, aggregator
│   │   ├── agents/             # встроенные роли и их промпты/политики
│   │   ├── risk/               # gate + политики + версии
│   │   ├── execution/          # OMS, reconciliation, idempotency
│   │   ├── backtest/           # replay, sim exchange, метрики
│   │   ├── store/              # sqlite, postgres, migrations, repos
│   │   ├── bus/                # in-process pub/sub + outbox
│   │   ├── journal/            # append-only, hash-chain, replay
│   │   ├── providers/          # registry, catalog, resolver, health
│   │   ├── apihttp/            # REST/SSE handlers (тонкие)
│   │   ├── config/             # слои конфига, валидация
│   │   ├── secrets/            # keychain, encrypted file
│   │   ├── strategy/           # встроенные стратегии + WASM ABI
│   │   └── telemetry/          # logs, metrics, traces, redaction
│   ├── porttest/               # conformance-наборы для провайдеров
│   └── testdata/{golden,fixtures,recordings}
│
├── providers/                  # отдельные Go-модули, собираются в бинарник
│   ├── exchange-binance/       # venue + marketdata
│   ├── exchange-bybit/
│   ├── broker-alpaca/
│   ├── marketdata-ccxt/        # REST-мост для длинного хвоста бирж
│   ├── llm-openaicompat/       # все OpenAI-совместимые + локальные
│   ├── llm-anthropic/
│   ├── decision-drex/          # /v1/systemone, wire-compatible Jev
│   ├── decision-local/         # ONNX/llama.cpp адаптер (v1.1)
│   ├── notify-telegram/
│   └── registry_gen.go         # генерируется: импорт включённых паков
│
├── apps/
│   ├── desktop/                # Tauri v2 + React 19 + TS (FSD)
│   │   ├── src/{app,pages,widgets,features,entities,shared}
│   │   └── src-tauri/{src,capabilities,icons,tauri.conf.json}
│   ├── web/                    # React 19 + TS (тот же packages/ui-web)
│   └── mobile/                 # Flutter + melos
│       ├── app/
│       └── packages/{vending_api,vending_auth,vending_realtime,vending_ui,vending_models}
│
├── packages/
│   ├── contracts-ts/           # типы + клиент из contracts/
│   ├── contracts-dart/         # (источник для mobile codegen)
│   ├── ui-web/                 # дизайн-система и общие экраны desktop/web
│   ├── sdk-ts/                 # публичный SDK (v1.1)
│   └── config/                 # eslint/oxlint/tsconfig пресеты
│
├── docs/{spec,adr,runbooks,guides}
├── deploy/{docker,compose}
├── scripts/                    # gen, drift, release, seed
└── testdata/                   # общие фикстуры (рынок, сценарии)
```

`ideas/` в репозиторий не входит: это внешняя папка с клонами ресёрча,
добавляется в `.gitignore` на случай `git init` в корне рабочей папки.

## 3. Workspace-механика

### Go
```go
// go.work
go 1.27
use (
    ./core
    ./providers/exchange-binance
    ./providers/exchange-bybit
    ./providers/broker-alpaca
    ./providers/marketdata-ccxt
    ./providers/llm-openaicompat
    ./providers/llm-anthropic
    ./providers/decision-drex
    ./providers/notify-telegram
    ./providers/notify-webhook
    ./tools/lintimports
    ./tools/registrygen
)
```
- Версии зависимостей фиксируются `go.work.sum`; релиз собирается с `GOWORK=off`
  после `go work sync`, чтобы исключить дрейф.
- `providers.lock.json` (BOM) хранит версии и хэши паков, попадает в релизные
  артефакты и в `vend doctor`.

### Node
- npm workspaces: `apps/web`, `apps/desktop`, `packages/*`.
- Один lockfile, `engines.node >= 24`, `packageManager` зафиксирован.
- Vite 8, React 19, TypeScript 5.9, MobX 7 + `openapi-fetch` (как в
  `lv24-brain-desktop`), React Router 7.
- Линт/формат: `oxlint` + `oxfmt` (быстро, уже используется в `lv24/main/frontend`),
  `tsc --noEmit` как обязательный шаг.

### Flutter
- `apps/mobile` — melos-воркспейс; каждый пакет версионируется отдельно.
- State: Riverpod; HTTP: `dio`; realtime: SSE-клиент + `drift`-кэш;
  secure storage: `flutter_secure_storage`; биометрия: `local_auth`;
  графики: `fl_chart`/`candlesticks` (финальный выбор — по итогам ресёрча).
- `dart analyze --fatal-infos`, `dart format --set-exit-if-changed`, `flutter test`.

## 4. Кодогенерация

`task gen` (детерминированно, без сети):

| Шаг | Инструмент | Выход |
|---|---|---|
| OpenAPI → Go server | `oapi-codegen` | `core/api/gen/server.gen.go` |
| OpenAPI → TS | `openapi-typescript` | `packages/contracts-ts/src/schema.d.ts` |
| OpenAPI → Dart | `openapi_generator` | `apps/mobile/packages/vending_api/lib/generated/` |
| JSON Schema → Go | `go-jsonschema` | `core/contracts/*.gen.go` |
| JSON Schema → TS | `json-schema-to-typescript` | `packages/contracts-ts/src/events/*.ts` |
| JSON Schema → Dart | `quicktype` | `packages/vending_models/lib/generated/` |
| Provider registry | `go run ./tools/registrygen` | `providers/registry_gen.go` |

Правила:
- Сгенерированное **коммитится**; CI перегенерирует и падает на diff
  (`task gen:check`) — тот же приём, что `api/drift_test.go` в `ai-gateway`.
- Никакой ручной правки сгенерированных файлов (`// Code generated ... DO NOT EDIT`).
- Кодоген не имеет права ходить в сеть: все зависимости — локальные.

## 5. Конфигурация

Слои (последний побеждает): defaults → `~/.config/lv24-vending/config.yaml`
(Windows: `%APPDATA%\lv24-vending\`) → env `VENDING_*` → CLI-флаги.

```yaml
apiVersion: vending.lv24/v1
mode: paper
dataDir: ~/.local/share/lv24-vending
api: {bind: 127.0.0.1:8420, auth: local-token}
market:
  defaults: {timeframes: [1m, 5m, 1h], maxSubscriptions: 200}
risk:
  policy: default
  limits: {maxOrderNotional: 1000, maxGrossExposure: 5000, maxDailyLoss: 250}
agents:
  routing:
    analyst: {provider: llm-openaicompat, model: gpt-5.5, fallback: local/llama}
    risk:    {provider: decision-drex, model: drex-max-v1.1}
  budgets: {dailyUsd: 10, perRunUsd: 0.25}
providers:
  connections:
    - id: binance-main
      provider: exchange-binance
      mode: paper
      config: {sandbox: true, market: spot}
      secrets: {apiKey: conn/binance-main/apiKey, apiSecret: conn/binance-main/apiSecret}
```

Секреты в YAML запрещены схемой: поле `secrets` — только ссылки.

## 6. Соглашения кода

- Ошибки: типизированные (`domain.Error{Code, Message, Retryable, Cause}`),
  оборачивание `%w`, никаких паник через границы; `panic` только в `main` для
  невозможных состояний.
- `context.Context` — первым аргументом, обязательный таймаут на внешние вызовы.
- Деньги: `decimal.Decimal` (shopspring) или собственный fixed-point; float
  запрещён линтером в `domain`/`execution`.
- Идентификаторы: ULID; время: UTC.
- Тесты: table-driven, `-race`, golden-файлы для контрактов и симуляций;
  интеграционные — под тегом `integration`; сетевые — через `httptest`/записи.
- Комментарии — по делу, на английском; публичные интерфейсы документированы.

## 7. CI (нулевые расходы)

Публичный репозиторий, только стандартные GitHub-hosted runners, без матриц
сверх необходимого, retention артефактов 7 дней.

| Job | Что проверяет | Триггер |
|---|---|---|
| `contracts` | `task gen:check`, валидация схем, примеры | PR, push |
| `core` | `go vet`, `golangci-lint`, `go test -race`, `go build` | PR, push |
| `core-windows` | сборка и тесты под Windows (пути, keychain) | PR (path-filter) |
| `providers` | conformance-тесты с mock-серверами | PR (path-filter) |
| `web` | `tsc`, `oxlint`, `vitest`, `vite build` | PR (path-filter) |
| `desktop` | `tsc`, `vitest`, `cargo check`, `cargo test` | PR (path-filter) |
| `mobile` | `dart analyze`, `flutter test`, сборка APK debug | PR (path-filter) |
| `security` | `govulncheck`, `npm audit --omit=dev`, `cargo audit` | push, weekly |
| `licenses` | `go-licenses`, `license-checker`, `cargo-deny`, `flutter pub deps` — запрет AGPL/GPL в ядре и mobile | PR, weekly |
| `docs` | сборка/проверка ссылок в spec | PR (path-filter) |

Path-фильтры обязательны: изменение `docs/**` не запускает Go/Flutter.

Релизы — по тегу `v*`: GoReleaser (linux/darwin/windows, amd64/arm64), Tauri
bundler (nsis/msi/dmg/appimage), позже npm-пакет CLI. Подпись — по мере
появления сертификатов; до этого артефакты помечаются `unsigned`.
Никаких платных runners, Codespaces, Marketplace-приложений и AI-аддонов.

## 8. Ветвление и версии

- Trunk-based: `main` всегда зелёный, короткоживущие ветки, squash-merge.
- Conventional Commits → `CHANGELOG.md` (release-please или собственный скрипт).
- SemVer для `core`; провайдеры — независимые версии + `providers.lock.json`.
- Контракты: `v1` до первого публичного релиза; далее — только аддитивно.

## 9. Риски монорепы и меры

| Риск | Мера |
|---|---|
| Расхождение сгенерированных контрактов | drift-тест в CI, обязательный `task gen:check` |
| Разъезд версий провайдеров | BOM `providers.lock.json`, `go work sync` в релизе |
| Медленный CI | path-фильтры, кэш Go/npm/pub, split job'ов |
| Смешение слоёв | линтер импортов (`tools/lintimports`) в CI |
| «Магические» фичи вендора | только capability-модель, деградации — явные |
| Разрастание `internal` | правила пакетов в `AGENTS.md`, ревью границ |
