# 01. Архитектура

Статус: draft v0.1 · Зависит от: `00-vision.md`

## 1. Форма системы

Ядро — гексагональная архитектура (ports & adapters), как в `lv24-brain`:
`domain` не знает про I/O, `app` содержит use-cases, `ports` объявляет
интерфейсы, `adapters/providers` реализуют их и подключаются в composition root.

```
                    ┌────────────────────────────────────────────┐
   CLI (vend) ─────▶│  api (HTTP/SSE)   runtime   scheduler       │
   Desktop ────────▶│        │             │          │          │
   Web ────────────▶│        ▼             ▼          ▼          │
   Mobile ─────────▶│   app (use-cases) ── bus ── journal          │
                    │        │                                    │
                    │        ▼                                    │
                    │   domain (models, invariants, policies)     │
                    │        ▲                                    │
                    │        │ ports (interfaces + descriptors)   │
                    └────────┼────────────────────────────────────┘
                             │
        ┌────────────┬───────┴───────┬────────────┬─────────────┐
        ▼            ▼               ▼            ▼             ▼
   venue-*      marketdata-*     llm-*       decision-*    notify-*
   (Go modules, отдельные пакеты, регистрируются в реестре)
```

Правила зависимостей (проверяются линтером импортов в CI):

- `domain` → только stdlib.
- `app` → `domain`, `ports`.
- `runtime` → `app`, `ports`, `bus`, `journal`.
- `adapters/*` → `domain`, `ports` (никогда `app`/`runtime`).
- `api` → `app` (тонкий транспорт, без бизнес-логики).
- `cmd/*` — единственное место, где собирается граф зависимостей.

## 2. Четыре плоскости провайдеров

Любая внешняя система описывается одним из типов. Тип определяет контракт,
дескриптор и набор conformance-тестов.

| Kind | Назначение | Примеры | Ключевые методы |
|---|---|---|---|
| `venue` | Исполнение и счёт | Binance, Bybit, Alpaca, IBKR | `PlaceOrder`, `CancelOrder`, `OpenOrders`, `Positions`, `Balances` |
| `marketdata` | Котировки и история | WS-коннекторы, CCXT-мост | `Subscribe`, `Candles`, `OrderBook`, `Trades`, `Instruments` |
| `llm` | Генеративные модели | OpenAI-совместимые, Anthropic, Ollama | `Chat`, `Stream`, `Embed` (опц.) |
| `decision` | Типизированные решения | Drex, Jev, локальная модель | `Decide` (вероятности по вариантам) |
| `notifier` | Уведомления | Telegram, webhook, push | `Send` |
| `store` | Персистентность | SQLite, Postgres | репозитории домена |

Провайдер может закрывать несколько плоскостей (Binance = venue + marketdata),
но это два независимых дескриптора и два интерфейса: подписка на данные не
должна требовать торговых прав.

## 3. Дескриптор и capability-модель

```go
type Kind string // venue | marketdata | llm | decision | notifier | store

type Descriptor struct {
    ID           string        // "binance", "alpaca", "drex"
    Kind         Kind
    Version      string        // semver пакета провайдера
    DisplayName  string
    Capabilities Capabilities  // структурированные возможности
    ConfigFields []ConfigField // схема настроек для UI + валидации
    DocsURL      string
    Deprecated   bool
}

type Capabilities struct {
    Venue      *VenueCaps
    MarketData *MarketDataCaps
    LLM        *LLMCaps
    Decision   *DecisionCaps
    Notifier   *NotifierCaps
}
```

`VenueCaps` — то, на чём строятся «фичи вендора»:

```go
type VenueCaps struct {
    AssetClasses []AssetClass // spot, margin, futures, options, fx, equities
    OrderTypes   []OrderType  // market, limit, stop, stop_limit, trailing, oco
    TimeInForce  []TIF        // gtc, ioc, fok, day, gtd
    Features     []Feature    // reduce_only, post_only, fractional, short,
                              // leverage, sandbox, user_data_stream, batch
    RateLimits   []RateLimit  // {scope, points, window, burst}
    MaxOrderRate int
}
```

`ConfigField` — декларативная форма подключения (тип, обязательность, секретность,
placeholder, валидатор), чтобы desktop/web/mobile рисовали форму подключения без
хардкода под каждого вендора:
`{key, label, type: string|secret|number|bool|select|url, required, secret, options, validate}`.

Правила:
- Дескриптор валидируется при регистрации (`descriptor.Validate()`).
- ID уникален в рамках Kind; конфликт — ошибка сборки графа, не рантайма.
- Capability-значения — единственный источник правды для UI и для выбора
  адаптера. Никаких `if provider == "binance"` в ядре.
- Возможность, которой нет у провайдера, **не эмулируется молча**. Резолвер
  либо находит другого провайдера, либо возвращает `ErrCapabilityUnavailable` с
  планом деградации (например, trailing stop эмулируется ядром локально — это
  явная, зарегистрированная деградация, а не скрытая).

## 4. Реестр и композиция

```go
type Registration struct {
    Descriptor Descriptor
    Factory    Factory
}

type Factory func(conn domain.Connection, deps Deps) (any, error)

type Deps struct {
    Credentials CredentialProvider
    HTTPClient  func() *http.Client // без redirects, с таймаутами и лимитами
    Clock       func() time.Time
    Logger      *slog.Logger
    Metrics     Metrics
    Redactor    Redactor
}
```

- Реестр concurrency-safe, без глобального состояния; собирается в `cmd/*`.
- Фабрика не читает env и не ходит в сеть при создании.
- `Connection` — пользовательская запись: id, provider, account label, config,
  secret refs, mode (`paper|live`), capabilities override (например, запрет
  фьючерсов на этом подключении).
- Conformance-набор на каждый Kind (`porttest`): единые тесты, которым обязан
  соответствовать любой провайдер (идемпотентность ордера, маппинг ошибок,
  поведение при 429, точность чисел).

## 5. Контракты между языками

Единственный источник правды — `contracts/`:

```
contracts/
  openapi/core-v1.yaml        # REST + SSE, команды и ресурсы
  jsonschema/
    common/v1.json            # Decimal, Money, InstrumentRef, ...
    events/v1/*.json          # journal-события (schemaVersion)
    agent/v1.json             # манифест агента
    provider/v1.json          # дескриптор провайдера
    proposal/v1.json          # выход агента/стратегии
    risk/v1.json              # политики и вердикты
```

Генерация:
- Go: `oapi-codegen` (server types) + `go-jsonschema`; drift-тест как в
  `lv24/main/ai-gateway/api/drift_test.go`.
- TS: `openapi-typescript` + `openapi-fetch`, типы событий из JSON Schema.
- Dart: `openapi_generator`/`json_serializable` (или собственный codegen из той
  же схемы).

Правила эволюции: только аддитивные изменения в рамках `v1`; удаление —
через `deprecated` + новую major-версию; события несут `schemaVersion` и
`eventId`; неизвестные поля клиенты игнорируют.

## 6. Runtime

```
marketdata ─▶ markethub ─▶ bus ─┬─▶ strategy engine ─▶ signal ─┐
                                ├─▶ agent supervisor ─▶ proposal ┤
                                └─▶ risk/portfolio watchers     │
                                                                ▼
                                             proposal aggregator (quorum/policy)
                                                                ▼
                                              RiskGate (детерминированные проверки)
                                                                ▼
                                     approval (если требуется) ─▶ OMS/executor
                                                                ▼
                                        venue provider ─▶ order events ─▶ journal
```

Компоненты:

- **bus** — типизированные топики, backpressure, приоритеты. Критические события
  (`order.*`, `fill`, `risk.*`) пишутся в outbox и доставляются at-least-once;
  рыночные тики — lossy, коалесцируются.
- **markethub** — подписки, нормализация, дедуп, детект gap и staleness
  (`data_age_ms`), снапшоты стакана, восстановление после реконнекта.
- **scheduler** — cron/интервалы с jitter, торговый календарь, market-hours,
  ограничение одновременных прогонов.
- **agent supervisor** — запуск агентов по событиям/расписанию, лимиты
  конкурентности, таймауты, бюджеты, circuit breaker на провайдера, ретраи с
  идемпотентным ключом.
- **aggregator** — сводит несколько proposals: детерминированные правила
  (`first`, `quorum`, `weighted`, `veto`), tie-break по приоритету роли и id;
  результат воспроизводим.
- **risk gate** — см. `05-risk-execution.md`.
- **OMS** — жизненный цикл ордера, идемпотентность, reconciliation.
- **journal** — append-only, hash-chain для аудита, реплей.

### 6.1 Топики

```
market.tick        market.candle      market.orderbook   market.status
account.snapshot   position.update    order.event         fill
strategy.signal    agent.proposal     risk.verdict        approval.request
decision.recorded  alert              system.health       provider.health
```

### 6.2 Режимы исполнения

| Mode | Что можно | Как включается |
|---|---|---|
| `backtest` | Только симулятор, часы виртуальные | `vend backtest` |
| `paper` (default) | Реальные данные, симулированное исполнение | по умолчанию |
| `dry-run` | Агенты работают, ордера не отправляются, только журнал | флаг у run |
| `live` | Реальные ордера | unlock: `vend live unlock --account X` + подтверждение |

Live-unlock хранится как подписанный токен с TTL и привязкой к account+mode,
сбрасывается при смене ключей, при kill switch и при рестарте демона (по policy).

### 6.3 Детерминированное ядро времени (backtest = sandbox = live)

Один и тот же event loop во всех трёх режимах, отличается только источник
событий и часы:

- Виртуальные часы в backtest, реальные — в live; wall clock внутри стратегии
  недоступен.
- Очередь с приоритетом `(timestamp_ns, priority, sequence)`; `sequence` —
  монотонный счётчик для детерминированного tie-break.
- Типы событий: `MarketData`, `Timer`, `Session`, `OrderCommand`, `OrderEvent`,
  `Fill`, `PortfolioSnapshot`, `RiskAlert`.
- Три фазы на каждый timestamp:
  1. matching/venue: обновить книгу/бар, исполнить resting orders;
  2. data → strategy: неизменяемый `MarketSlice`, `on_bar/on_quote/on_tick`;
  3. settle: выполнить команды, сматчить новые заявки, повторять до quiescence.
- Заявка из фазы 2 **не** исполняется по данным, которые стратегия уже видела
  (`FillTiming::NextEvent` по умолчанию; `SameBarClose` — только явный opt-in).
- Пайплайн филла: trigger → base price → slippage (в границах OHLC/книги) →
  participation cap → partial → fees → buying power → `Fill` + ledger.
- Ledger event-sourced: cash, position (long/short, lots, avg price), margin,
  PnL; деньги — fixed-point/Decimal, не `f64`.
- Corporate actions (split/dividend/delisting) применяются до `OnData`.
- Multi-asset: одна стратегия возвращает `TargetWeight`/`RebalanceIntent`, а не
  «купи 100 штук»; портфель один, matching engines — по инструментам.
- Детерминизм: стабильная сортировка символов, seeded RNG на компонент,
  стабильные `OrderId/FillId`; одинаковый вход → byte-identical отчёт.

## 7. Хранилище

Одна схема, два адаптера: SQLite (WAL, локально) и Postgres (сервер).

Основные таблицы:

```
tenants            users            sessions          api_tokens
provider_connections                credentials(ref only)
instruments        venues           accounts          balances
strategies         runs             run_state         schedules
agents             agent_versions   agent_runs        prompts
proposals          decisions        decision_inputs   decision_outcomes
risk_policies      risk_checks      approvals         kill_switches
orders             order_events     fills             positions
backtests          backtest_trades  metrics_snapshots
journal_events     outbox           audit_log         schema_migrations
```

Правила:
- Деньги и количества — `decimal` (строка/фиксированная точность), никогда float.
- Все времена — UTC, `timestamptz`/RFC3339 с наносекундами.
- Идентификаторы — ULID (сортируемость + отсутствие координации).
- `journal_events` партиционируется по месяцу (Postgres), чистится по retention.
- Тики рынка по умолчанию не хранятся; хранится агрегат (свечи) и ссылки на
  источник. Для реплея — опциональный датасет.
- Миграции — встроенные, версионированные, применяются на старте с блокировкой.

## 8. API

REST `/api/v1` + SSE `/api/v1/stream` (односторонний поток событий с
`Last-Event-ID` для resume). Это тот же паттерн, что в `lv24-brain-desktop`
(`openapi-fetch` + SSE), поэтому клиенты переиспользуют подход.

Группы:
- `system`: health, version, doctor, config
- `providers`: catalog, connections, test, capabilities, health
- `market`: instruments, candles, orderbook, ticker, subscriptions
- `accounts`: balances, positions, orders, fills, pnl
- `strategies` / `runs`: CRUD, start/stop, logs
- `agents`: catalog, versions, roles matrix, runs, decisions, explain
- `risk`: policies, checks, kill switch, approvals
- `backtests`: create, status, report, compare
- `journal`: query, export, replay
- `settings`: models routing, budgets, security

Локальный режим: bind `127.0.0.1`, bearer token из файла `0600`, CORS только для
desktop origin. Серверный: OIDC/device-flow, RBAC (`viewer|trader|admin`),
rate limits, аудит каждого мутирующего вызова.

## 9. Безопасность

- **Secrets**: `ports.SecretStore` → OS keychain (Windows Credential Manager,
  macOS Keychain, libsecret) или encrypted-file (age/Argon2id). В конфиге и БД
  только ссылки (`conn/<id>/apiKey`). Redaction во всех логах/трейсах.
- **Права агентов** проверяются кодом на границе инструментов: агент не может
  позвать `place_order`, у него нет такого инструмента; он может только вернуть
  `Proposal`. Разрешения (`read.market`, `read.account`, `propose.order`) —
  декларативные и валидируются при загрузке манифеста.
- **Изоляция провайдера**: исходящие хосты — allowlist из дескриптора; никаких
  redirects; лимит размера ответа; таймауты.
- **Prompt hygiene**: в промпт не попадают ключи, номера счетов и PII; входы
  редактируются перед отправкой (redactor), хэшируются для аудита.
- **Аудит**: hash-chain журнала, подпись релизов, SBOM, `govulncheck`/`npm audit`
  в CI.

## 10. Наблюдаемость

- Логи: `slog` JSON, correlation id (`run_id`, `decision_id`, `order_id`).
- Метрики Prometheus: латентность цикла, event loop lag, ошибки провайдеров,
  стоимость ИИ, количество отказов риск-гейта, расхождения reconciliation.
- Трейсы OTel: `market.event → agent.run → risk.check → order.submit`.
- **Decision trace** — первоклассная сущность: вход (хэш + снапшот), модель,
  промпт-хэш, токены/стоимость, выход, вердикт риска, approval, исход,
  калибровочные метрики.

## 11. Бюджеты производительности (v1)

| Метрика | Цель |
|---|---|
| Тик → proposal (без ИИ) | p95 < 20 мс |
| Тик → ордер (paper) | p95 < 50 мс |
| Агент: локальная decision-модель | p95 < 150 мс |
| Агент: облачная LLM | p95 < 8 с (таймаут 30 с) |
| REST p95 локально | < 30 мс |
| Event loop lag | p99 < 100 мс |
| Память демона (10 runs) | < 300 МБ |
| Старт демона | < 700 мс |

## 12. Отказы и деградация

| Отказ | Поведение |
|---|---|
| WS-разрыв биржи | exponential backoff, resubscribe, снапшот стакана, пометка `stale` |
| LLM таймаут/5xx | fallback-модель → `abstain`, решение фиксируется с причиной |
| Decision-провайдер недоступен | выходы/стопы исполняются без него; входы блокируются |
| Дубликат ордера | clientOrderId, OMS отклоняет; reconciliation сверяет |
| Расхождение позиции | pause run, alert, авто-reconcile по политике |
| Ключ отозван | provider unhealthy, run paused, уведомление, новые ордера запрещены |
| Kill switch | cancel-all, блокировка новых intents, live-unlock сброшен |
| Переполнение очереди | backpressure, lossy-тики отбрасываются, критические — в outbox |

## 13. Клиенты: общий контракт

- Desktop и web используют один `packages/ui-web` (FSD: app/pages/widgets/
  features/entities/shared) и один сгенерированный API-клиент.
- Mobile использует сгенерированный Dart-клиент и `packages/ui-flutter`.
- Realtime: SSE-события + REST-команды; на mobile — тот же поток с resume и
  локальным кэшем (drift/sqlite).
- Все клиенты показывают: режим (`PAPER/LIVE`), состояние провайдеров, свежесть
  данных, стоимость ИИ за период, статус kill switch.
